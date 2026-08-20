package agent

import (
	"context"
	"fmt"
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
	out := make([]backup.FrozenReplicationRun, 0)
	for id, run := range state.ReplicationRuns {
		if run.Status != "RUNNING" || run.LeaseUntilUnix > now.Unix() || state.ReplicationRunTerms[id] >= term {
			continue
		}
		leaseUntil := now.Add(30 * time.Second).Unix()
		cmd, err := control.EncodeCommand(control.CmdBackupRunClaim, control.RunClaimBody{OperationID: "replication-recover-" + id, RunID: id, LeaderTerm: term, UpdatedUnix: now.Unix(), LeaseUntilUnix: leaseUntil, Replication: true})
		if err != nil || n.Apply(cmd, 5*time.Second) != nil {
			continue
		}
		state = n.View()
		claimed := state.ReplicationRuns[id]
		tasks := replicationFrozenTasks(state, claimed)
		if len(tasks) != 0 {
			out = append(out, backup.FrozenReplicationRun{RunID: claimed.RunID, PolicyID: claimed.PolicyID, PolicyRevision: claimed.PolicyRevision, LeaderTerm: term, LeaseExpiresUnix: claimed.LeaseUntilUnix, MaxConcurrency: claimed.MaxConcurrency, Tasks: tasks})
		}
	}
	return out, nil
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
