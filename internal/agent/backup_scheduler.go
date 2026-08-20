package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type raftBackupControl struct{ runtime *rpcRuntime }

func (a raftBackupControl) node() *control.Node {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.control()
}

func (a raftBackupControl) ListEnabledBackupPolicies(_ context.Context) ([]backup.PolicyView, error) {
	n := a.node()
	if n == nil {
		return nil, fmt.Errorf("control plane unavailable")
	}
	st := n.View()
	ids := make([]string, 0, len(st.BackupPolicies))
	for id, p := range st.BackupPolicies {
		if p.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]backup.PolicyView, 0, len(ids))
	for _, id := range ids {
		p := st.BackupPolicies[id]
		targets := resolveBackupTargets(st, p)
		out = append(out, backup.PolicyView{Policy: backup.PolicyFromRecord(backup.PolicyRecord{
			PolicyID: p.PolicyID, Name: p.Name, Enabled: p.Enabled, ScheduleCron: p.ScheduleCron, Timezone: p.Timezone,
			TargetSelector: p.TargetSelector, TargetIDs: p.TargetIDs, Sink: p.Sink, DestinationProfile: p.DestinationProfile,
			RetentionKeepLast: p.RetentionKeepLast, RetentionKeepDays: p.RetentionKeepDays, RetentionMaxBytes: p.RetentionMaxBytes,
			TimeoutSeconds: p.TimeoutSeconds, MaxConcurrency: p.MaxConcurrency, UnavailablePolicy: p.UnavailablePolicy, Revision: p.Revision,
		}), Revision: p.Revision, TargetNodeIDs: targets})
	}
	return out, nil
}

func (a raftBackupControl) ClaimFire(ctx context.Context, key, policyID string, term uint64, now time.Time) (string, bool, error) {
	n := a.node()
	if n == nil {
		return "", false, fmt.Errorf("control plane unavailable")
	}
	record, claimed, err := n.ClaimBackupFire(control.FireClaimBody{OperationID: "scheduler-" + key, FireKey: key, PolicyID: policyID, ScheduledUnix: scheduledUnix(key), LeaderTerm: term}, now)
	if err != nil {
		return "", false, err
	}
	return record.RunID, claimed, nil
}

func (a raftBackupControl) ClaimScheduledRun(_ context.Context, key string, view backup.PolicyView, term uint64, now time.Time) (backup.FrozenRun, bool, error) {
	n := a.node()
	if n == nil {
		return backup.FrozenRun{}, false, fmt.Errorf("control plane unavailable")
	}
	targets := append([]string(nil), view.TargetNodeIDs...)
	sort.Strings(targets)
	timeout := backupTimeout(view.Policy.TimeoutSeconds)
	leaseUntil := now.Add(time.Duration(timeout) * time.Second).Unix()
	record, run, acquired, err := n.ClaimScheduledBackupRun(control.ScheduledRunClaimBody{
		Fire: control.FireClaimBody{OperationID: "scheduler-" + key, FireKey: key, PolicyID: view.Policy.PolicyID, ScheduledUnix: scheduledUnix(key), LeaderTerm: term, LeaseUntilUnix: leaseUntil},
		Run:  control.ClusterBackupRun{RunID: "run-" + fireID(key), PolicyID: view.Policy.PolicyID, PolicyRevision: policyRevision(view), TargetNodeIDs: targets, Status: "RUNNING", CreatedUnix: now.Unix(), StartedUnix: now.Unix(), Sink: view.Policy.Sink, DestinationProfile: view.Policy.DestinationProfile, MaxConcurrency: view.Policy.MaxConcurrency, TimeoutSeconds: timeout, UnavailablePolicy: view.Policy.UnavailablePolicy, LeaseUntilUnix: leaseUntil},
	}, now)
	if err != nil {
		return backup.FrozenRun{}, false, err
	}
	return backup.FrozenRun{RunID: record.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, TargetNodeIDs: append([]string(nil), run.TargetNodeIDs...), Sink: run.Sink, DestinationProfile: run.DestinationProfile, MaxConcurrency: run.MaxConcurrency, LeaderTerm: record.LeaderTerm, LeaseExpiresUnix: run.LeaseUntilUnix, TimeoutSeconds: run.TimeoutSeconds, UnavailablePolicy: run.UnavailablePolicy}, acquired, nil
}

func (a raftBackupControl) ClaimRecoverableRuns(_ context.Context, term uint64, now time.Time) ([]backup.FrozenRun, error) {
	n := a.node()
	if n == nil {
		return nil, fmt.Errorf("control plane unavailable")
	}
	state := n.View()
	ids := make([]string, 0, len(state.BackupRuns))
	for id, run := range state.BackupRuns {
		if run.Status == "RUNNING" && run.LeaseUntilUnix <= now.Unix() && state.BackupRunTerms[id] < term {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]backup.FrozenRun, 0, len(ids))
	for _, id := range ids {
		run := state.BackupRuns[id]
		timeout := backupTimeout(run.TimeoutSeconds)
		leaseUntil := now.Add(time.Duration(timeout) * time.Second).Unix()
		cmd, err := control.EncodeCommand(control.CmdBackupRunClaim, control.RunClaimBody{OperationID: "scheduler-recover-" + id, RunID: id, LeaderTerm: term, UpdatedUnix: now.Unix(), LeaseUntilUnix: leaseUntil})
		if err != nil {
			return nil, err
		}
		if err := n.Apply(cmd, 5*time.Second); err != nil {
			return nil, err
		}
		claimed := n.View()
		run = claimed.BackupRuns[id]
		if claimed.BackupRunTerms[id] != term || run.LeaseUntilUnix != leaseUntil {
			continue
		}
		targets := unfinishedBackupTargets(claimed, run)
		if len(targets) == 0 {
			continue
		}
		out = append(out, backup.FrozenRun{RunID: run.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, TargetNodeIDs: targets, Sink: run.Sink, DestinationProfile: run.DestinationProfile, MaxConcurrency: run.MaxConcurrency, LeaderTerm: term, LeaseExpiresUnix: leaseUntil, TimeoutSeconds: timeout, UnavailablePolicy: run.UnavailablePolicy})
	}
	return out, nil
}

func unfinishedBackupTargets(state control.State, run control.ClusterBackupRun) []string {
	targets := make([]string, 0, len(run.TargetNodeIDs))
	for _, nodeID := range run.TargetNodeIDs {
		status := ""
		updated := int64(-1)
		for _, task := range state.BackupTasks {
			if task.RunID == run.RunID && task.NodeID == nodeID && task.UpdatedUnix >= updated {
				status = task.Status
				updated = task.UpdatedUnix
			}
		}
		if status == "" || status == "PENDING" || status == "RUNNING" {
			targets = append(targets, nodeID)
		}
	}
	return targets
}

func backupTimeout(seconds int) int {
	if seconds <= 0 {
		return 30
	}
	return seconds
}

func (a raftBackupControl) UpdateTask(ctx context.Context, u backup.TaskUpdate) error {
	n := a.node()
	if n == nil {
		return fmt.Errorf("control plane unavailable")
	}
	term := u.LeaderTerm
	if term == 0 {
		term = n.CurrentTerm()
	}
	cmd, err := control.EncodeCommand(control.CmdBackupTaskUpdate, control.UpdateTaskBody{OperationID: "scheduler-" + u.RunID + ":" + u.TaskID, LeaderTerm: term, Task: control.ClusterBackupTask{RunID: u.RunID, TaskID: u.TaskID, NodeID: u.NodeID, Status: u.Status, SnapshotID: u.SnapshotID, SHA256: u.SHA256, Bytes: u.Bytes, ErrorCode: u.ErrorCode, ErrorSummary: u.ErrorSummary, UpdatedUnix: time.Now().Unix()}})
	if err != nil {
		return err
	}
	return n.Apply(cmd, 5*time.Second)
}

type raftBackupRunCreator struct{ runtime *rpcRuntime }

func (a raftBackupRunCreator) CreateBackupRun(_ context.Context, r backup.BackupRunRequest) error {
	n := a.runtime.control()
	if n == nil {
		return fmt.Errorf("control plane unavailable")
	}
	cmd, err := control.EncodeCommand(control.CmdBackupRunCreate, control.CreateRunBody{OperationID: "scheduler-run-" + r.RunID, LeaderTerm: r.Term, Run: control.ClusterBackupRun{RunID: r.RunID, PolicyID: r.PolicyID, PolicyRevision: r.PolicyRevision, TargetNodeIDs: append([]string(nil), r.TargetNodeIDs...), Status: "RUNNING", CreatedUnix: r.CreatedAt.Unix(), StartedUnix: r.CreatedAt.Unix()}})
	if err != nil {
		return err
	}
	return n.Apply(cmd, 5*time.Second)
}

type clusterBackupAgentForwarder interface {
	ClusterBackupAgent(context.Context, api.Route) (procmeshv1connect.ClusterBackupAgentServiceClient, error)
}

type localBackupDispatcher struct {
	runtime *rpcRuntime
	local   backup.AgentTaskExecutor
	forward clusterBackupAgentForwarder
	members func() []cluster.NodeSummary
	update  func(context.Context, backup.TaskUpdate) error
}

func (d localBackupDispatcher) DispatchBackupTask(ctx context.Context, task backup.BackupTaskRequest) error {
	if d.runtime == nil {
		return fmt.Errorf("backup runtime unavailable")
	}
	if n := d.runtime.control(); n != nil {
		if existing, ok := n.View().BackupTasks[task.RunID+":"+task.TaskID]; ok && terminalBackupTaskStatus(existing.Status) {
			if successfulBackupTaskStatus(existing.Status) {
				return nil
			}
			return &backup.TaskOutcomeError{Status: existing.Status}
		}
	}
	if task.NodeID == d.runtime.nodeID {
		executor := d.local
		if executor == nil {
			executor = d.runtime.backup
		}
		if executor == nil {
			return fmt.Errorf("backup engine unavailable")
		}
		result, err := executor.RunClusterTask(ctx, backup.ClusterTaskRequest{
			RunID: task.RunID, TaskID: task.TaskID, PolicyID: task.PolicyID, NodeID: task.NodeID,
			PolicyRevision: task.PolicyRevision, Sink: task.Sink, DestinationProfile: task.DestinationProfile,
			LeaderTerm: task.LeaderTerm, LeaseExpiresUnix: task.LeaseExpiresUnix,
		})
		if err != nil {
			return err
		}
		return d.persistResult(ctx, task, taskResultUpdate(result, task))
	}

	addr := ""
	members := d.members
	if members == nil {
		members = d.runtime.memberList
	}
	for _, member := range members() {
		if member.NodeID == task.NodeID {
			addr = member.RPCAddress
			break
		}
	}
	if addr == "" {
		return fmt.Errorf("target agent rpc unavailable")
	}
	forward := d.forward
	if forward == nil {
		forward = d.runtime.fwd
	}
	if forward == nil {
		return fmt.Errorf("agent forwarder unavailable")
	}
	client, err := forward.ClusterBackupAgent(ctx, api.Route{NodeID: task.NodeID, RPC: addr})
	if err != nil {
		return err
	}
	resp, err := client.RunTask(ctx, connect.NewRequest(&procmeshv1.RunClusterBackupTaskRequest{
		RunId: task.RunID, TaskId: task.TaskID, PolicyId: task.PolicyID, NodeId: task.NodeID,
		PolicyRevision: task.PolicyRevision, Sink: task.Sink, DestinationProfile: task.DestinationProfile,
		LeaderTerm: task.LeaderTerm, LeaseExpiresUnix: task.LeaseExpiresUnix,
	}))
	if err != nil {
		return err
	}
	return d.persistResult(ctx, task, taskResultUpdateFromProto(resp.Msg.GetTask(), task))
}

func (d localBackupDispatcher) persistResult(ctx context.Context, task backup.BackupTaskRequest, update backup.TaskUpdate) error {
	if err := d.persist(ctx, task, update); err != nil {
		return err
	}
	if terminalBackupTaskStatus(update.Status) && !successfulBackupTaskStatus(update.Status) {
		return &backup.TaskOutcomeError{Status: update.Status}
	}
	return nil
}

func successfulBackupTaskStatus(status string) bool {
	return status == "SUCCESS" || status == "SUCCEEDED"
}

func terminalBackupTaskStatus(status string) bool {
	return successfulBackupTaskStatus(status) || status == "FAILED" || status == "TIMEOUT" || status == "UNAVAILABLE" || status == "CONFIG_MISSING" || status == "RETENTION_FAILED" || status == "SKIPPED"
}

func (d localBackupDispatcher) persist(ctx context.Context, task backup.BackupTaskRequest, update backup.TaskUpdate) error {
	if d.update != nil {
		return d.update(ctx, update)
	}
	return (raftBackupControl{runtime: d.runtime}).UpdateTask(ctx, update)
}

func taskResultUpdate(result *backup.TaskResult, task backup.BackupTaskRequest) backup.TaskUpdate {
	if result == nil {
		return backup.TaskUpdate{RunID: task.RunID, TaskID: task.TaskID, NodeID: task.NodeID, Status: "FAILED", ErrorCode: "UNKNOWN", ErrorSummary: "empty task result", LeaderTerm: task.LeaderTerm}
	}
	return backup.TaskUpdate{RunID: result.RunID, TaskID: result.TaskID, NodeID: result.NodeID, Status: result.Status, SnapshotID: result.SnapshotID, SHA256: result.SHA256, Bytes: result.Bytes, ErrorCode: result.ErrorCode, ErrorSummary: result.ErrorSummary, LeaderTerm: result.LeaderTerm}
}

func taskResultUpdateFromProto(result *procmeshv1.ClusterBackupTask, task backup.BackupTaskRequest) backup.TaskUpdate {
	if result == nil {
		return taskResultUpdate(nil, task)
	}
	return backup.TaskUpdate{RunID: result.GetRunId(), TaskID: result.GetTaskId(), NodeID: result.GetNodeId(), Status: result.GetStatus(), SnapshotID: result.GetSnapshotId(), SHA256: result.GetSha256(), Bytes: result.GetBytes(), ErrorCode: result.GetErrorCode(), ErrorSummary: result.GetErrorSummary(), LeaderTerm: result.GetLeaderTerm()}
}

func resolveBackupTargets(st control.State, p control.BackupPolicy) []string {
	if p.TargetSelector == "EXPLICIT_NODES" {
		return append([]string(nil), p.TargetIDs...)
	}
	if p.TargetSelector == "AGENT_GROUP" {
		seen := map[string]bool{}
		for _, gid := range p.TargetIDs {
			for _, id := range st.AgentGroups[gid].MemberIDs {
				if m, ok := st.Members[id]; ok && m.Status == control.MemberAdmitted {
					seen[id] = true
				}
			}
		}
		return sortedKeys(seen)
	}
	seen := map[string]bool{}
	for id, m := range st.Members {
		if m.Status == control.MemberAdmitted {
			seen[id] = true
		}
	}
	return sortedKeys(seen)
}
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func scheduledUnix(key string) int64 {
	var n int64
	_, _ = fmt.Sscanf(key[strings.LastIndex(key, ":")+1:], "%d", &n)
	return n
}

func policyRevision(view backup.PolicyView) int64 {
	if view.Revision != 0 {
		return view.Revision
	}
	return view.Policy.Revision
}
func fireID(key string) string { sum := sha256.Sum256([]byte(key)); return fmt.Sprintf("%x", sum[:12]) }

func stableTaskSnapshotID(runID, nodeID string) string {
	sum := sha256.Sum256([]byte(runID + ":" + nodeID))
	return "snap-" + fmt.Sprintf("%x", sum[:12])
}

func (r *rpcRuntime) authorizeClusterBackupTask(sourceNodeID string, msg *procmeshv1.RunClusterBackupTaskRequest) error {
	if r == nil || msg == nil {
		return errcode.E(errcode.UNAVAILABLE, "backup runtime unavailable")
	}
	n := r.control()
	if n == nil {
		return errcode.E(errcode.UNAVAILABLE, "control plane unavailable")
	}
	if msg.GetLeaderTerm() == 0 || msg.GetLeaderTerm() != n.CurrentTerm() {
		return errcode.E(errcode.CONFLICT, "stale leader term")
	}
	if msg.GetLeaseExpiresUnix() <= time.Now().Unix() {
		return errcode.E(errcode.TIMEOUT, "task lease expired")
	}
	state := n.View()
	source, ok := state.Members[sourceNodeID]
	if !ok || source.Status != control.MemberAdmitted || source.RaftAddr == "" || source.RaftAddr != n.LeaderAddr() {
		return errcode.E(errcode.DENIED, "task source is not current leader")
	}
	target, ok := state.Members[r.nodeID]
	if !ok || target.Status != control.MemberAdmitted || msg.GetNodeId() != r.nodeID {
		return errcode.E(errcode.DENIED, "target agent not admitted")
	}
	policy, ok := state.BackupPolicies[msg.GetPolicyId()]
	if !ok || policy.Revision != msg.GetPolicyRevision() {
		return errcode.E(errcode.CONFLICT, "backup policy revision changed")
	}
	run, ok := state.BackupRuns[msg.GetRunId()]
	if !ok || run.PolicyID != policy.PolicyID || run.PolicyRevision != policy.Revision {
		return errcode.E(errcode.NOT_FOUND, "backup run not found")
	}
	return nil
}
