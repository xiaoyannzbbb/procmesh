package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type raftReplicationControl struct{ runtime *rpcRuntime }

func (a raftReplicationControl) ClaimReplicationRuns(_ context.Context, term uint64, now time.Time) ([]backup.FrozenReplicationRun, error) {
	n := a.runtime.control()
	if n == nil {
		return nil, fmt.Errorf("control plane unavailable")
	}
	state := n.View()
	plans, err := planAutomaticReplicationRuns(state, term, now)
	if err != nil {
		return nil, err
	}
	for _, plan := range plans {
		cmd, err := control.EncodeCommand(control.CmdBackupRunCreate, plan.Create)
		if err != nil {
			return nil, err
		}
		if err := n.Apply(cmd, 5*time.Second); err != nil {
			continue
		}
		for _, taskID := range plan.MissingTaskIDs {
			task := plan.task(taskID)
			cmd, err := control.EncodeCommand(control.CmdBackupTaskUpdate, control.UpdateTaskBody{OperationID: "replication-missing-" + plan.Create.Run.RunID + ":" + taskID, LeaderTerm: term, Replication: true, Task: control.ClusterBackupTask{RunID: task.RunID, TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, NodeID: task.NodeID, Status: "FAILED", ErrorCode: "SOURCE_SNAPSHOT_MISSING", ErrorSummary: "no successful primary snapshot", UpdatedUnix: now.Unix()}})
			if err == nil {
				_ = n.Apply(cmd, 5*time.Second)
			}
		}
	}
	state = n.View()
	runnable, takeovers := runnableReplicationRuns(state, term, now)
	for _, id := range takeovers {
		leaseUntil := now.Add(30 * time.Second).Unix()
		cmd, err := control.EncodeCommand(control.CmdBackupRunClaim, control.RunClaimBody{OperationID: "replication-recover-" + id, RunID: id, LeaderTerm: term, UpdatedUnix: now.Unix(), LeaseUntilUnix: leaseUntil, Replication: true})
		if err != nil || n.Apply(cmd, 5*time.Second) != nil {
			continue
		}
		state = n.View()
		claimed := state.ReplicationRuns[id]
		tasks := replicationFrozenTasks(state, claimed)
		if len(tasks) != 0 {
			runnable = append(runnable, backup.FrozenReplicationRun{RunID: claimed.RunID, PolicyID: claimed.PolicyID, PolicyRevision: claimed.PolicyRevision, LeaderTerm: term, LeaseExpiresUnix: claimed.LeaseUntilUnix, MaxConcurrency: claimed.MaxConcurrency, Tasks: tasks})
		}
	}
	return runnable, nil
}

type automaticReplicationPlan struct {
	Create         control.CreateRunBody
	MissingTaskIDs []string
}

func (p automaticReplicationPlan) task(id string) control.ClusterBackupTask {
	for _, task := range p.Create.Tasks {
		if task.TaskID == id {
			return task
		}
	}
	return control.ClusterBackupTask{}
}

func runnableReplicationRuns(state control.State, term uint64, now time.Time) ([]backup.FrozenReplicationRun, []string) {
	ids := make([]string, 0, len(state.ReplicationRuns))
	for id := range state.ReplicationRuns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	runs := make([]backup.FrozenReplicationRun, 0)
	takeovers := make([]string, 0)
	for _, id := range ids {
		run := state.ReplicationRuns[id]
		if run.Status != "RUNNING" {
			continue
		}
		owner := state.ReplicationRunTerms[id]
		if owner == term && run.LeaseUntilUnix > now.Unix() {
			tasks := replicationFrozenTasks(state, run)
			if len(tasks) > 0 {
				runs = append(runs, backup.FrozenReplicationRun{RunID: run.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, LeaderTerm: term, LeaseExpiresUnix: run.LeaseUntilUnix, MaxConcurrency: run.MaxConcurrency, Tasks: tasks})
			}
			continue
		}
		if owner < term || run.LeaseUntilUnix <= now.Unix() {
			takeovers = append(takeovers, id)
		}
	}
	return runs, takeovers
}

func planAutomaticReplicationRuns(state control.State, term uint64, now time.Time) ([]automaticReplicationPlan, error) {
	ids := make([]string, 0, len(state.ReplicationPolicies))
	for id := range state.ReplicationPolicies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	plans := make([]automaticReplicationPlan, 0)
	for _, id := range ids {
		policy := state.ReplicationPolicies[id]
		if !policy.Enabled || (policy.Trigger != "AFTER_PRIMARY_BACKUP" && policy.Trigger != "SCHEDULE") {
			continue
		}
		if policy.Trigger == "AFTER_PRIMARY_BACKUP" {
			for _, primaryRun := range successfulPrimaryRuns(state, policy.PrimaryPolicyIDs, now.Unix()) {
				if plan, ok := automaticReplicationPlanForPrimary(state, policy, term, now, primaryRun, primaryRun.RunID); ok {
					plans = append(plans, plan)
				}
			}
		} else {
			fire, err := backup.PreviousOrEqualInTimezone(policy.ScheduleCron, policy.Timezone, now)
			if err != nil {
				return nil, err
			}
			qualifier := fmt.Sprintf("%d", fire.Unix())
			primary := latestPrimaryRun(state, policy.PrimaryPolicyIDs, fire.Unix())
			if plan, ok := automaticReplicationPlanForPrimary(state, policy, term, now, primary, qualifier); ok {
				plans = append(plans, plan)
			}
		}
	}
	return plans, nil
}

func automaticReplicationPlanForPrimary(state control.State, policy control.ReplicationPolicy, term uint64, now time.Time, primary control.ClusterBackupRun, qualifier string) (automaticReplicationPlan, bool) {
	runID := automaticReplicationID("run", policy.PolicyID, qualifier)
	if _, exists := state.ReplicationRuns[runID]; exists {
		return automaticReplicationPlan{}, false
	}
	run := control.ClusterBackupRun{RunID: runID, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision, TargetNodeIDs: replicationSourcesForPolicy(policy), Status: "RUNNING", CreatedUnix: now.Unix(), StartedUnix: now.Unix(), MaxConcurrency: policy.MaxConcurrency, LeaseUntilUnix: now.Add(30 * time.Second).Unix()}
	plan := automaticReplicationPlan{Create: control.CreateRunBody{OperationID: "replication-auto-" + runID, LeaderTerm: term, Replication: true, Run: run}}
	for _, route := range policy.Routes {
		source := successfulPrimaryTask(state, primary.RunID, route.SourceNodeID)
		for _, target := range route.TargetNodeIDs {
			taskID := automaticReplicationID("task", policy.PolicyID, qualifier, route.SourceNodeID, target)
			task := control.ClusterBackupTask{RunID: runID, TaskID: taskID, SourceNodeID: route.SourceNodeID, NodeID: target, Status: "PENDING", UpdatedUnix: now.Unix()}
			if source.SnapshotID != "" && source.SHA256 != "" {
				task.SnapshotID, task.SHA256 = source.SnapshotID, source.SHA256
				task.TaskID = automaticReplicationID("task", policy.PolicyID, source.SnapshotID, target)
			} else {
				plan.MissingTaskIDs = append(plan.MissingTaskIDs, taskID)
			}
			plan.Create.Tasks = append(plan.Create.Tasks, task)
		}
	}
	return plan, true
}

func latestPrimaryRun(state control.State, policyIDs []string, before int64) control.ClusterBackupRun {
	allowed := map[string]bool{}
	for _, id := range policyIDs {
		allowed[id] = true
	}
	var best control.ClusterBackupRun
	for _, run := range state.BackupRuns {
		if !allowed[run.PolicyID] || (run.Status != "SUCCEEDED" && run.Status != "SUCCESS" && run.Status != "PARTIAL") || run.FinishedUnix > before {
			continue
		}
		if run.FinishedUnix > best.FinishedUnix || (run.FinishedUnix == best.FinishedUnix && run.RunID > best.RunID) {
			best = run
		}
	}
	return best
}
func successfulPrimaryRuns(state control.State, policyIDs []string, before int64) []control.ClusterBackupRun {
	allowed := map[string]bool{}
	for _, id := range policyIDs {
		allowed[id] = true
	}
	out := make([]control.ClusterBackupRun, 0)
	for _, run := range state.BackupRuns {
		if allowed[run.PolicyID] && (run.Status == "SUCCEEDED" || run.Status == "SUCCESS" || run.Status == "PARTIAL") && run.FinishedUnix <= before {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FinishedUnix == out[j].FinishedUnix {
			return out[i].RunID < out[j].RunID
		}
		return out[i].FinishedUnix < out[j].FinishedUnix
	})
	return out
}
func successfulPrimaryTask(state control.State, runID, source string) control.ClusterBackupTask {
	var best control.ClusterBackupTask
	for _, task := range state.BackupTasks {
		if task.RunID == runID && task.NodeID == source && (task.Status == "SUCCEEDED" || task.Status == "SUCCESS") && task.UpdatedUnix >= best.UpdatedUnix {
			best = task
		}
	}
	return best
}
func replicationSourcesForPolicy(policy control.ReplicationPolicy) []string {
	seen := map[string]bool{}
	for _, route := range policy.Routes {
		seen[route.SourceNodeID] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func automaticReplicationID(kind string, values ...string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + joinReplicationValues(values)))
	return kind + "-" + hex.EncodeToString(sum[:12])
}
func joinReplicationValues(values []string) string {
	out := ""
	for i, v := range values {
		if i != 0 {
			out += "\x00"
		}
		out += v
	}
	return out
}

func replicationFrozenTasks(state control.State, run control.ClusterBackupRun) []backup.FrozenReplicationTask {
	out := make([]backup.FrozenReplicationTask, 0)
	for _, task := range state.ReplicationTasks {
		if task.RunID != run.RunID || !replicationRetryable(task.Status) || task.SnapshotID == "" || task.SHA256 == "" {
			continue
		}
		out = append(out, backup.FrozenReplicationTask{TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.NodeID, SnapshotID: task.SnapshotID, SHA256: task.SHA256, Status: task.Status})
	}
	return out
}

func replicationRetryable(status string) bool {
	return status == "PENDING" || status == "FAILED" || status == "TIMEOUT" || status == "UNAVAILABLE" || status == "CONFIG_MISSING" || status == "SKIPPED" || status == "RETENTION_FAILED"
}

func (a raftReplicationControl) UpdateReplicationTask(_ context.Context, update backup.ReplicationTaskUpdate) error {
	n := a.runtime.control()
	if n == nil {
		return fmt.Errorf("control plane unavailable")
	}
	cmd, err := control.EncodeCommand(control.CmdBackupTaskUpdate, control.UpdateTaskBody{OperationID: "replication-" + update.RunID + ":" + update.TaskID, LeaderTerm: update.LeaderTerm, Replication: true, Task: control.ClusterBackupTask{RunID: update.RunID, TaskID: update.TaskID, SourceNodeID: update.SourceNodeID, NodeID: update.TargetNodeID, SnapshotID: update.SnapshotID, SHA256: update.SHA256, Status: update.Status, Bytes: update.Bytes, ErrorCode: update.ErrorCode, ErrorSummary: update.ErrorSummary, UpdatedUnix: time.Now().Unix()}})
	if err != nil {
		return err
	}
	return n.Apply(cmd, 5*time.Second)
}

type peerReplicationForwarder interface {
	PeerReplication(context.Context, api.Route) (procmeshv1connect.PeerReplicationServiceClient, error)
}

type localReplicationDispatcher struct{ runtime *rpcRuntime }

func (d localReplicationDispatcher) DispatchReplicationTask(ctx context.Context, task backup.ReplicationTaskRequest) error {
	if d.runtime == nil || d.runtime.backup == nil {
		return fmt.Errorf("replication runtime unavailable")
	}
	if task.SourceNodeID == d.runtime.nodeID {
		bytes, err := d.runtime.backup.ReplicateSnapshot(ctx, task)
		if err != nil {
			return err
		}
		return (raftReplicationControl{runtime: d.runtime}).UpdateReplicationTask(ctx, backup.ReplicationTaskUpdate{RunID: task.RunID, TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.TargetNodeID, SnapshotID: task.SnapshotID, SHA256: task.SHA256, Status: "SUCCEEDED", Bytes: bytes, LeaderTerm: task.LeaderTerm})
	}
	addr := ""
	for _, member := range d.runtime.memberList() {
		if member.NodeID == task.SourceNodeID {
			addr = member.RPCAddress
			break
		}
	}
	if addr == "" || d.runtime.fwd == nil {
		return errcode.E(errcode.UNAVAILABLE, "source agent rpc unavailable")
	}
	client, err := d.runtime.fwd.PeerReplication(ctx, api.Route{NodeID: task.SourceNodeID, RPC: addr})
	if err != nil {
		return err
	}
	resp, err := client.ReplicateSnapshot(ctx, connect.NewRequest(&procmeshv1.ReplicateSnapshotRequest{RunId: task.RunID, TaskId: task.TaskID, PolicyId: task.PolicyID, PolicyRevision: task.PolicyRevision, SourceNodeId: task.SourceNodeID, TargetNodeId: task.TargetNodeID, SnapshotId: task.SnapshotID, Sha256: task.SHA256, LeaderTerm: task.LeaderTerm, LeaseExpiresUnix: task.LeaseExpiresUnix}))
	if err != nil {
		return err
	}
	return (raftReplicationControl{runtime: d.runtime}).UpdateReplicationTask(ctx, backup.ReplicationTaskUpdate{RunID: task.RunID, TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.TargetNodeID, SnapshotID: task.SnapshotID, SHA256: task.SHA256, Status: "SUCCEEDED", Bytes: resp.Msg.GetBytes(), LeaderTerm: task.LeaderTerm})
}

func (r *rpcRuntime) authorizeReplicationTask(leaderNodeID string, msg *procmeshv1.ReplicateSnapshotRequest) error {
	if r == nil || msg == nil || msg.GetSourceNodeId() != r.nodeID {
		return errcode.E(errcode.DENIED, "replication source mismatch")
	}
	n := r.control()
	if n == nil || msg.GetLeaderTerm() == 0 || msg.GetLeaderTerm() != n.CurrentTerm() || msg.GetLeaseExpiresUnix() <= time.Now().Unix() {
		return errcode.E(errcode.CONFLICT, "stale replication lease")
	}
	state := n.View()
	leader, ok := state.Members[leaderNodeID]
	if !ok || leader.Status != control.MemberAdmitted || leader.RaftAddr != n.LeaderAddr() {
		return errcode.E(errcode.DENIED, "replication caller is not current leader")
	}
	run, ok := state.ReplicationRuns[msg.GetRunId()]
	if !ok || run.PolicyID != msg.GetPolicyId() || run.PolicyRevision != msg.GetPolicyRevision() || state.ReplicationRunTerms[run.RunID] != msg.GetLeaderTerm() {
		return errcode.E(errcode.CONFLICT, "replication run changed")
	}
	task, ok := state.ReplicationTasks[msg.GetRunId()+":"+msg.GetTaskId()]
	if !ok || task.SourceNodeID != r.nodeID || task.NodeID != msg.GetTargetNodeId() || task.SnapshotID != msg.GetSnapshotId() || task.SHA256 != msg.GetSha256() || !replicationRetryable(task.Status) {
		return errcode.E(errcode.CONFLICT, "replication task changed")
	}
	return nil
}

var _ peerReplicationForwarder = (*agentForwarder)(nil)
