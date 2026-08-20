package agent

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
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
			return []cluster.NodeSummary{{NodeID: "node-b", RPCAddress: "127.0.0.1:9001"}}
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
			return []cluster.NodeSummary{{NodeID: "node-b", RPCAddress: "127.0.0.1:9001"}}
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

type fakeClusterBackupAgentForwarder struct {
	client procmeshv1connect.ClusterBackupAgentServiceClient
}

func (f fakeClusterBackupAgentForwarder) ClusterBackupAgent(context.Context, api.Route) (procmeshv1connect.ClusterBackupAgentServiceClient, error) {
	return f.client, nil
}

type fakeClusterBackupAgentClient struct {
	request *procmeshv1.RunClusterBackupTaskRequest
	result  *procmeshv1.ClusterBackupTask
}

func (f *fakeClusterBackupAgentClient) RunTask(_ context.Context, req *connect.Request[procmeshv1.RunClusterBackupTaskRequest]) (*connect.Response[procmeshv1.RunClusterBackupTaskResponse], error) {
	f.request = req.Msg
	return connect.NewResponse(&procmeshv1.RunClusterBackupTaskResponse{Task: f.result}), nil
}

func (f *fakeClusterBackupAgentClient) GetTask(context.Context, *connect.Request[procmeshv1.GetClusterBackupTaskRequest]) (*connect.Response[procmeshv1.GetClusterBackupTaskResponse], error) {
	return connect.NewResponse(&procmeshv1.GetClusterBackupTaskResponse{Task: f.result}), nil
}
