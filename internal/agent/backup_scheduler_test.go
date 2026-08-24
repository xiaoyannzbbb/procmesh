package agent

import (
	"context"
	"errors"
	"testing"
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

func TestAgentBackupSchedulerResolvesFrozenTargets(t *testing.T) {
	st := *control.NewState()
	st.Members["node-b"] = control.Member{NodeID: "node-b", Status: control.MemberAdmitted}
	st.Members["node-a"] = control.Member{NodeID: "node-a", Status: control.MemberAdmitted}
	got := resolveBackupTargets(st, control.BackupPolicy{TargetSelector: "ALL_ADMITTED"})
	if len(got) != 2 || got[0] != "node-a" || got[1] != "node-b" {
		t.Fatalf("targets=%v", got)
	}
	got = resolveBackupTargets(st, control.BackupPolicy{TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-b"}})
	if len(got) != 1 || got[0] != "node-b" {
		t.Fatalf("explicit targets=%v", got)
	}
}

func TestBackupDispatcherDispatchesRemoteAgentTaskAndPersistsResult(t *testing.T) {
	client := &fakeClusterBackupAgentClient{result: &procmeshv1.ClusterBackupTask{
		RunId: "run-1", TaskId: "task-node-b", NodeId: "node-b", Status: "SUCCESS",
		SnapshotId: "snap-1", Sha256: "abc", Bytes: 42, LeaderTerm: 7,
	}}
	var updated backup.TaskUpdate
	d := localBackupDispatcher{
		runtime: &rpcRuntime{nodeID: "node-a"},
		forward: fakeClusterBackupAgentForwarder{client: client},
		members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{NodeID: "node-b", RPCAddress: "127.0.0.1:18683"}}
		},
		update: func(_ context.Context, got backup.TaskUpdate) error { updated = got; return nil },
	}
	err := d.DispatchBackupTask(context.Background(), backup.BackupTaskRequest{
		RunID: "run-1", TaskID: "task-node-b", PolicyID: "policy-1", NodeID: "node-b",
		PolicyRevision: 3, Sink: "s3", DestinationProfile: "archive", LeaderTerm: 7, LeaseExpiresUnix: 123,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := client.request
	if req == nil || req.GetPolicyId() != "policy-1" || req.GetLeaderTerm() != 7 || req.GetLeaseExpiresUnix() != 123 || req.GetDestinationProfile() != "archive" {
		t.Fatalf("request=%+v", req)
	}
	if updated.Status != "SUCCESS" || updated.SnapshotID != "snap-1" || updated.Bytes != 42 || updated.LeaderTerm != 7 {
		t.Fatalf("updated=%+v", updated)
	}
}

func TestBackupDispatcherPreservesPersistedTerminalFailure(t *testing.T) {
	client := &fakeClusterBackupAgentClient{result: &procmeshv1.ClusterBackupTask{
		RunId: "run-1", TaskId: "task-node-b", NodeId: "node-b", Status: "FAILED", ErrorCode: "SNAPSHOT_FAILED", LeaderTerm: 7,
	}}
	var updated backup.TaskUpdate
	d := localBackupDispatcher{
		runtime: &rpcRuntime{nodeID: "node-a"},
		forward: fakeClusterBackupAgentForwarder{client: client},
		members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{NodeID: "node-b", RPCAddress: "127.0.0.1:18683"}}
		},
		update: func(_ context.Context, got backup.TaskUpdate) error { updated = got; return nil },
	}
	err := d.DispatchBackupTask(context.Background(), backup.BackupTaskRequest{
		RunID: "run-1", TaskID: "task-node-b", PolicyID: "policy-1", NodeID: "node-b",
		PolicyRevision: 3, Sink: "fs", LeaderTerm: 7, LeaseExpiresUnix: 123,
	})
	var outcome *backup.TaskOutcomeError
	if !errors.As(err, &outcome) || outcome.Status != "FAILED" {
		t.Fatalf("error=%v", err)
	}
	if updated.Status != "FAILED" || updated.ErrorCode != "SNAPSHOT_FAILED" {
		t.Fatalf("updated=%+v", updated)
	}
}

func TestBackupDispatcherRemoteRunTaskUsesParentDeadline(t *testing.T) {
	client := &fakeClusterBackupAgentClient{result: &procmeshv1.ClusterBackupTask{
		RunId: "run-1", TaskId: "task-node-b", NodeId: "node-b", Status: "SUCCESS", LeaderTerm: 7,
	}}
	d := localBackupDispatcher{
		runtime: &rpcRuntime{nodeID: "node-a"},
		forward: fakeClusterBackupAgentForwarder{client: client},
		members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{NodeID: "node-b", RPCAddress: "127.0.0.1:18683"}}
		},
		update: func(context.Context, backup.TaskUpdate) error { return nil },
	}
	parentDeadline := time.Now().Add(15 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), parentDeadline)
	defer cancel()
	if err := d.DispatchBackupTask(ctx, backup.BackupTaskRequest{
		RunID: "run-1", TaskID: "task-node-b", PolicyID: "policy-1", NodeID: "node-b",
		PolicyRevision: 3, Sink: "fs", LeaderTerm: 7, LeaseExpiresUnix: parentDeadline.Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := client.ctx.Deadline()
	if !ok {
		t.Fatal("remote RunTask context has no deadline")
	}
	if d := got.Sub(parentDeadline); d > 50*time.Millisecond || d < -50*time.Millisecond {
		t.Fatalf("RunTask deadline %s, want parent %s (delta %s)", got, parentDeadline, d)
	}
}

func TestRetryableBackupDispatch(t *testing.T) {
	t.Parallel()
	if !retryableBackupDispatch(errcode.E(errcode.UNAVAILABLE, "raft leader unknown")) {
		t.Fatal("raft leader unknown must retry")
	}
	if !retryableBackupDispatch(errcode.E(errcode.NOT_FOUND, "backup run not found")) {
		t.Fatal("backup run not found must retry")
	}
	if !retryableBackupDispatch(errcode.E(errcode.UNAVAILABLE, "target agent rpc unavailable")) {
		t.Fatal("rpc unavailable must retry")
	}
	if retryableBackupDispatch(errcode.E(errcode.CONFLICT, "stale leader term")) {
		t.Fatal("stale leader term must not retry")
	}
	if retryableBackupDispatch(context.DeadlineExceeded) {
		t.Fatal("parent deadline must not retry")
	}
	if retryableBackupDispatch(connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout"))) {
		t.Fatal("deadline exceeded must not retry")
	}
}

func TestUnfinishedBackupTargetsPreservesTerminalResults(t *testing.T) {
	state := *control.NewState()
	run := control.ClusterBackupRun{RunID: "run-1", TargetNodeIDs: []string{"a", "b", "c", "d"}}
	state.BackupTasks["run-1:task-a"] = control.ClusterBackupTask{RunID: run.RunID, NodeID: "a", Status: "SUCCESS"}
	state.BackupTasks["run-1:task-b"] = control.ClusterBackupTask{RunID: run.RunID, NodeID: "b", Status: "FAILED"}
	state.BackupTasks["run-1:task-c"] = control.ClusterBackupTask{RunID: run.RunID, NodeID: "c", Status: "RUNNING"}
	got := unfinishedBackupTargets(state, run)
	if len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Fatalf("targets=%v", got)
	}
}

func TestAuthorizeClusterBackupTaskUsesRuntimeClock(t *testing.T) {
	const (
		clusterID = "cluster-backup-clock"
		sourceID  = "source"
		targetID  = "target"
		policyID  = "policy"
		runID     = "run"
	)
	fakeNow := time.Now().Add(-time.Hour).Truncate(time.Second)
	node, apply := startBackupSchedulerControl(t, clusterID, targetID)
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: sourceID, RaftAddr: node.LeaderAddr(), Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: targetID, Status: control.MemberAdmitted})
	apply(control.CmdBackupPolicyPut, control.BackupPolicyPutBody{
		OperationID: "policy", PolicyID: policyID, Name: policyID, Enabled: true,
		ScheduleCron: "0 2 * * *", Timezone: "UTC",
		TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{targetID}, Sink: "fs",
		TimeoutSeconds: 8, MaxConcurrency: 1, UnavailablePolicy: "RECORD_AND_CONTINUE",
	})
	term := node.CurrentTerm()
	apply(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: "run", LeaderTerm: term,
		Run: control.ClusterBackupRun{
			RunID: runID, PolicyID: policyID, PolicyRevision: 1, TargetNodeIDs: []string{targetID},
			Status: "RUNNING", TimeoutSeconds: 8, LeaseUntilUnix: fakeNow.Add(time.Minute).Unix(),
		},
	})

	runtime := &rpcRuntime{nodeID: targetID, node: node, opt: Options{Now: func() time.Time { return fakeNow }}}
	err := runtime.authorizeClusterBackupTask(sourceID, &procmeshv1.RunClusterBackupTaskRequest{
		RunId: runID, TaskId: "task-target", NodeId: targetID, PolicyId: policyID,
		PolicyRevision: 1, LeaderTerm: term, LeaseExpiresUnix: fakeNow.Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("runtime-clock-live task rejected: %v", err)
	}
}

func TestClaimRecoverableRunsRenewsMinimumLeaseWithoutChangingTimeout(t *testing.T) {
	const (
		clusterID = "cluster-backup-recovery"
		nodeID    = "target"
		policyID  = "policy"
		runID     = "run"
	)
	now := time.Now().Truncate(time.Second)
	node, apply := startBackupSchedulerControl(t, clusterID, nodeID)
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted})
	apply(control.CmdBackupPolicyPut, control.BackupPolicyPutBody{
		OperationID: "policy", PolicyID: policyID, Name: policyID, Enabled: true,
		ScheduleCron: "0 2 * * *", Timezone: "UTC",
		TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{nodeID}, Sink: "fs",
		TimeoutSeconds: 8, MaxConcurrency: 1, UnavailablePolicy: "RECORD_AND_CONTINUE",
	})
	oldTerm := node.CurrentTerm()
	apply(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: "run", LeaderTerm: oldTerm,
		Run: control.ClusterBackupRun{
			RunID: runID, PolicyID: policyID, PolicyRevision: 1, TargetNodeIDs: []string{nodeID},
			Status: "RUNNING", TimeoutSeconds: 8, LeaseUntilUnix: now.Add(-time.Second).Unix(),
		},
	})

	runs, err := (raftBackupControl{runtime: &rpcRuntime{node: node}}).ClaimRecoverableRuns(context.Background(), oldTerm+1, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("recovered runs=%d want 1", len(runs))
	}
	wantLease := now.Add(30 * time.Second).Unix()
	if runs[0].LeaseExpiresUnix < wantLease {
		t.Fatalf("recovered lease=%d want at least %d", runs[0].LeaseExpiresUnix, wantLease)
	}
	if runs[0].TimeoutSeconds != 8 {
		t.Fatalf("recovered timeout=%d want original 8", runs[0].TimeoutSeconds)
	}
	committed := node.View().BackupRuns[runID]
	if committed.LeaseUntilUnix != runs[0].LeaseExpiresUnix {
		t.Fatalf("committed lease=%d returned lease=%d", committed.LeaseUntilUnix, runs[0].LeaseExpiresUnix)
	}
	if committed.TimeoutSeconds != 8 {
		t.Fatalf("committed timeout=%d want original 8", committed.TimeoutSeconds)
	}
}

func startBackupSchedulerControl(t *testing.T, clusterID, nodeID string) (*control.Node, func(string, any)) {
	t.Helper()
	node, err := control.Start(control.RaftConfig{
		Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: nodeID, ClusterID: clusterID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Shutdown() })
	if err := node.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := waitRaftLeader(node, raftStartTO); err != nil {
		t.Fatal(err)
	}
	apply := func(commandType string, body any) {
		t.Helper()
		command, err := control.EncodeCommand(commandType, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := node.Apply(command, raftApplyTO); err != nil {
			t.Fatal(err)
		}
	}
	return node, apply
}

type fakeClusterBackupAgentForwarder struct {
	client procmeshv1connect.ClusterBackupAgentServiceClient
}

func (f fakeClusterBackupAgentForwarder) ClusterBackupAgent(context.Context, api.Route) (procmeshv1connect.ClusterBackupAgentServiceClient, error) {
	return f.client, nil
}

type fakeClusterBackupAgentClient struct {
	request *procmeshv1.RunClusterBackupTaskRequest
	result  *procmeshv1.ClusterBackupTask
	ctx     context.Context
}

func (f *fakeClusterBackupAgentClient) RunTask(ctx context.Context, req *connect.Request[procmeshv1.RunClusterBackupTaskRequest]) (*connect.Response[procmeshv1.RunClusterBackupTaskResponse], error) {
	f.ctx = ctx
	f.request = req.Msg
	return connect.NewResponse(&procmeshv1.RunClusterBackupTaskResponse{Task: f.result}), nil
}

func (f *fakeClusterBackupAgentClient) GetTask(context.Context, *connect.Request[procmeshv1.GetClusterBackupTaskRequest]) (*connect.Response[procmeshv1.GetClusterBackupTaskResponse], error) {
	return connect.NewResponse(&procmeshv1.GetClusterBackupTaskResponse{Task: f.result}), nil
}
