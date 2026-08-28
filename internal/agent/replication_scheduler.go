package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

const replicationFireKeyPrefix = "replication:"

func replicationFireKey(policyID string, fireUnix int64) string {
	return replicationFireKeyPrefix + policyID + ":" + strconv.FormatInt(fireUnix, 10)
}

type raftReplicationControl struct{ runtime *rpcRuntime }

type replicationCommandApplier interface {
	Apply(control.Command, time.Duration) error
}

func (a raftReplicationControl) ClaimReplicationRuns(_ context.Context, term uint64, now time.Time) ([]backup.FrozenReplicationRun, error) {
	n := a.runtime.control()
	if n == nil {
		return nil, fmt.Errorf("control plane unavailable")
	}
	state := n.View()
	fires, err := listDueReplicationFires(state, now)
	if err != nil {
		return nil, err
	}
	created := make([]backup.FrozenReplicationRun, 0, len(fires))
	for _, fire := range fires {
		if fire.skipRunning {
			if err := claimReplicationFire(n, fire, term, now, "SKIPPED"); err != nil {
				return nil, err
			}
			continue
		}
		plan, ok := buildAutomaticReplicationPlan(state, fire.policy, term, now, fire.qualifier)
		if !ok {
			continue
		}
		plan.FireKey = fire.key
		plan.ScheduledUnix = fire.fire.Unix()
		cmd, err := control.EncodeCommand(control.CmdBackupRunCreate, plan.Create)
		if err != nil {
			return nil, err
		}
		if err := n.Apply(cmd, 5*time.Second); err != nil {
			return nil, err
		}
		if err := claimReplicationFire(n, fire, term, now, "CLAIMED"); err != nil {
			return nil, err
		}
		if frozen := frozenReplicationRunFromPlan(plan, term); len(frozen.Tasks) > 0 {
			created = append(created, frozen)
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
	return append(created, runnable...), nil
}

func claimReplicationFire(n *control.Node, fire dueReplicationFire, term uint64, now time.Time, status string) error {
	if n == nil || fire.key == "" {
		return nil
	}
	_, _, err := n.ClaimBackupFire(control.FireClaimBody{
		OperationID:   "replication-fire-" + status + "-" + fire.key,
		FireKey:       fire.key,
		PolicyID:      fire.policy.PolicyID,
		ScheduledUnix: fire.fire.Unix(),
		LeaderTerm:    term,
		Status:        status,
	}, now)
	return err
}

func frozenReplicationRunFromPlan(plan automaticReplicationPlan, term uint64) backup.FrozenReplicationRun {
	run := plan.Create.Run
	tasks := make([]backup.FrozenReplicationTask, 0, len(plan.Create.Tasks))
	for _, task := range plan.Create.Tasks {
		if !replicationRetryable(task.Status) {
			continue
		}
		tasks = append(tasks, backup.FrozenReplicationTask{TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.NodeID, SnapshotID: task.SnapshotID, SHA256: task.SHA256, Status: task.Status})
	}
	return backup.FrozenReplicationRun{RunID: run.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, LeaderTerm: term, LeaseExpiresUnix: run.LeaseUntilUnix, MaxConcurrency: run.MaxConcurrency, Tasks: tasks}
}

type automaticReplicationPlan struct {
	Create        control.CreateRunBody
	FireKey       string
	ScheduledUnix int64
}

type dueReplicationFire struct {
	policy      control.ReplicationPolicy
	fire        time.Time
	key         string
	qualifier   string
	skipRunning bool
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
			continue
		}
		if owner < term || run.LeaseUntilUnix <= now.Unix() {
			takeovers = append(takeovers, id)
		}
	}
	return runs, takeovers
}

func listDueReplicationFires(state control.State, now time.Time) ([]dueReplicationFire, error) {
	ids := make([]string, 0, len(state.ReplicationPolicies))
	for id := range state.ReplicationPolicies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]dueReplicationFire, 0)
	for _, id := range ids {
		policy := state.ReplicationPolicies[id]
		if !policy.Enabled || strings.TrimSpace(policy.ScheduleCron) == "" {
			continue
		}
		if policy.PolicyID == "" {
			policy.PolicyID = id
		}
		fire, err := backup.PreviousOrEqualInTimezone(policy.ScheduleCron, policy.Timezone, now)
		if err != nil {
			return nil, err
		}
		if fire.IsZero() {
			continue
		}
		if fire.Unix() <= policy.ScheduleEpochUnix {
			continue
		}
		key := replicationFireKey(policy.PolicyID, fire.Unix())
		if _, exists := state.BackupFireLedger[key]; exists {
			continue
		}
		qualifier := strconv.FormatInt(fire.Unix(), 10)
		runID := automaticReplicationID("run", policy.PolicyID, qualifier)
		if _, exists := state.ReplicationRuns[runID]; exists {
			continue
		}
		out = append(out, dueReplicationFire{
			policy:      policy,
			fire:        fire,
			key:         key,
			qualifier:   qualifier,
			skipRunning: policyHasRunningReplication(state, policy.PolicyID),
		})
	}
	return out, nil
}

func policyHasRunningReplication(state control.State, policyID string) bool {
	for _, run := range state.ReplicationRuns {
		if run.PolicyID == policyID && run.Status == "RUNNING" {
			return true
		}
	}
	return false
}

func planAutomaticReplicationRuns(state control.State, term uint64, now time.Time) ([]automaticReplicationPlan, error) {
	fires, err := listDueReplicationFires(state, now)
	if err != nil {
		return nil, err
	}
	plans := make([]automaticReplicationPlan, 0)
	for _, fire := range fires {
		if fire.skipRunning {
			continue
		}
		plan, ok := buildAutomaticReplicationPlan(state, fire.policy, term, now, fire.qualifier)
		if !ok {
			continue
		}
		plan.FireKey = fire.key
		plan.ScheduledUnix = fire.fire.Unix()
		plans = append(plans, plan)
	}
	return plans, nil
}

func emptySourceTask(string) control.ClusterBackupTask { return control.ClusterBackupTask{} }

func buildAutomaticReplicationPlan(state control.State, policy control.ReplicationPolicy, term uint64, now time.Time, qualifier string) (automaticReplicationPlan, bool) {
	runID := automaticReplicationID("run", policy.PolicyID, qualifier)
	if _, exists := state.ReplicationRuns[runID]; exists {
		return automaticReplicationPlan{}, false
	}
	run := control.ClusterBackupRun{RunID: runID, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision, TargetNodeIDs: replicationSourcesForPolicy(policy), Status: "RUNNING", CreatedUnix: now.Unix(), StartedUnix: now.Unix(), MaxConcurrency: policy.MaxConcurrency, LeaseUntilUnix: now.Add(30 * time.Second).Unix()}
	plan := automaticReplicationPlan{Create: control.CreateRunBody{OperationID: "replication-auto-" + runID, LeaderTerm: term, Replication: true, Run: run}}
	for _, route := range policy.Routes {
		source := emptySourceTask(route.SourceNodeID)
		for _, target := range route.TargetNodeIDs {
			taskID := automaticReplicationID("task", policy.PolicyID, qualifier, route.SourceNodeID, target)
			task := control.ClusterBackupTask{RunID: runID, TaskID: taskID, SourceNodeID: route.SourceNodeID, NodeID: target, SnapshotID: source.SnapshotID, SHA256: source.SHA256, Status: "PENDING", UpdatedUnix: now.Unix()}
			plan.Create.Tasks = append(plan.Create.Tasks, task)
		}
	}
	return plan, true
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
		if task.RunID != run.RunID || !replicationRetryable(task.Status) {
			continue
		}
		out = append(out, backup.FrozenReplicationTask{TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.NodeID, SnapshotID: task.SnapshotID, SHA256: task.SHA256, Status: task.Status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

func replicationRetryable(status string) bool {
	return status == "PENDING" || status == "FAILED" || status == "TIMEOUT" || status == "UNAVAILABLE" || status == "CONFIG_MISSING" || status == "SKIPPED" || status == "RETENTION_FAILED"
}

func applyBeginReplicationTask(ctx context.Context, n replicationCommandApplier, update backup.ReplicationTaskUpdate, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd, err := control.EncodeCommand(control.CmdBackupTaskUpdate, control.UpdateTaskBody{OperationID: "replication-begin-" + update.RunID + ":" + update.TaskID, LeaderTerm: update.LeaderTerm, Replication: true, Task: control.ClusterBackupTask{RunID: update.RunID, TaskID: update.TaskID, SourceNodeID: update.SourceNodeID, NodeID: update.TargetNodeID, SnapshotID: update.SnapshotID, SHA256: update.SHA256, Status: "RUNNING", UpdatedUnix: now.Unix()}})
	if err != nil {
		return err
	}
	timeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	if err := n.Apply(cmd, timeout); err != nil {
		return err
	}
	return nil
}

func (a raftReplicationControl) BeginReplicationTask(ctx context.Context, update backup.ReplicationTaskUpdate) error {
	n := a.runtime.control()
	if n == nil {
		return fmt.Errorf("control plane unavailable")
	}
	return applyBeginReplicationTask(ctx, n, update, time.Now())
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
	before := n.View()
	if err := n.Apply(cmd, 5*time.Second); err != nil {
		return err
	}
	api.ObserveControlTransition(before, n.View(), update.RunID, true)
	return nil
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
	var last error
	for range 80 {
		addr := ""
		for _, member := range d.runtime.memberList() {
			if member.NodeID == task.SourceNodeID {
				addr = member.RPCAddress
				break
			}
		}
		if addr == "" || d.runtime.fwd == nil {
			last = errcode.E(errcode.UNAVAILABLE, "source agent rpc unavailable")
		} else {
			client, err := d.runtime.fwd.PeerReplication(ctx, api.Route{NodeID: task.SourceNodeID, RPC: addr})
			if err != nil {
				last = err
			} else {
				resp, err := client.ReplicateSnapshot(ctx, connect.NewRequest(&procmeshv1.ReplicateSnapshotRequest{RunId: task.RunID, TaskId: task.TaskID, PolicyId: task.PolicyID, PolicyRevision: task.PolicyRevision, SourceNodeId: task.SourceNodeID, TargetNodeId: task.TargetNodeID, SnapshotId: task.SnapshotID, Sha256: task.SHA256, LeaderTerm: task.LeaderTerm, LeaseExpiresUnix: task.LeaseExpiresUnix}))
				if err == nil {
					return (raftReplicationControl{runtime: d.runtime}).UpdateReplicationTask(ctx, backup.ReplicationTaskUpdate{RunID: task.RunID, TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.TargetNodeID, SnapshotID: task.SnapshotID, SHA256: task.SHA256, Status: "SUCCEEDED", Bytes: resp.Msg.GetBytes(), LeaderTerm: task.LeaderTerm})
				}
				last = err
				if !retryableBackupDispatch(err) {
					return err
				}
			}
		}
		select {
		case <-ctx.Done():
			if last != nil {
				return last
			}
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return last
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
	if !ok || !replicationTaskMatchesDispatch(task, msg, r.nodeID) {
		return errcode.E(errcode.CONFLICT, "replication task changed")
	}
	return nil
}

func replicationTaskMatchesDispatch(task control.ClusterBackupTask, msg *procmeshv1.ReplicateSnapshotRequest, sourceNodeID string) bool {
	return msg != nil && (task.Status == "PENDING" || task.Status == "RUNNING") && task.RunID == msg.GetRunId() && task.TaskID == msg.GetTaskId() &&
		task.SourceNodeID == sourceNodeID && task.SourceNodeID == msg.GetSourceNodeId() && task.NodeID == msg.GetTargetNodeId() &&
		task.SnapshotID == msg.GetSnapshotId() && task.SHA256 == msg.GetSha256()
}

func (r *rpcRuntime) authorizePeerOperation(peerNodeID string, operation api.PeerOperation) error {
	if r == nil || operation.ClusterID != r.clusterID || operation.TargetNodeID != r.nodeID {
		return errcode.E(errcode.DENIED, "peer operation identity mismatch")
	}
	n := r.control()
	if n == nil {
		return errcode.E(errcode.DENIED, "peer operation control unavailable")
	}
	state := n.View()
	member, admitted := state.Members[peerNodeID]
	if !admitted || member.Status != control.MemberAdmitted || operation.SourceNodeID != peerNodeID {
		return errcode.E(errcode.DENIED, "peer source not admitted")
	}
	if operation.Kind == "DELETE" {
		intent, ok := state.ReplicationDeleteIntents[operation.IntentID]
		if !ok || intent.Status != "PENDING" || intent.LeaderTerm != n.CurrentTerm() || intent.ExpiresUnix <= time.Now().Unix() ||
			intent.SourceNodeID != peerNodeID || intent.TargetNodeID != r.nodeID || intent.SnapshotID != operation.SnapshotID ||
			intent.PolicyID != operation.PolicyID || intent.PolicyRevision != operation.PolicyRevision {
			return errcode.E(errcode.DENIED, "peer delete requires local retention authorization")
		}
		return nil
	}
	for _, task := range state.ReplicationTasks {
		if task.SourceNodeID != peerNodeID || task.NodeID != r.nodeID || task.SnapshotID != operation.SnapshotID {
			continue
		}
		if operation.SHA256 != "" && task.SHA256 != operation.SHA256 {
			continue
		}
		if operation.Kind == "PUT" {
			run, ok := state.ReplicationRuns[operation.RunID]
			if !ok || run.PolicyID != operation.PolicyID || run.PolicyRevision != operation.PolicyRevision || task.RunID != operation.RunID || task.TaskID != operation.TaskID || run.Status != "RUNNING" || run.LeaseUntilUnix <= time.Now().Unix() || state.ReplicationRunTerms[run.RunID] != n.CurrentTerm() || (task.Status != "PENDING" && task.Status != "RUNNING") {
				continue
			}
		}
		return nil
	}
	return errcode.E(errcode.DENIED, "peer operation not authorized")
}

func (r *rpcRuntime) completeDeleteIntent(operation api.PeerOperation) error {
	if r == nil || operation.IntentID == "" {
		return errcode.E(errcode.INVALID, "delete intent id required")
	}
	n := r.control()
	if n == nil {
		return errcode.E(errcode.UNAVAILABLE, "peer delete intent control unavailable")
	}
	intent, ok := n.View().ReplicationDeleteIntents[operation.IntentID]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "delete intent not found")
	}
	intent.Status = "SUCCEEDED"
	cmd, err := control.EncodeCommand(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{
		OperationID: "delete-complete-" + operation.IntentID,
		Intent:      intent,
	})
	if err != nil {
		return err
	}
	return n.Apply(cmd, 5*time.Second)
}

var _ peerReplicationForwarder = (*agentForwarder)(nil)
