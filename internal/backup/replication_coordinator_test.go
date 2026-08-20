package backup_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type replicationControlFake struct {
	mu      sync.Mutex
	runs    []backup.FrozenReplicationRun
	updates []backup.ReplicationTaskUpdate
}

func TestReplicationCoordinator_ChecksumConflictIsTerminalFailed(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	control := &replicationControlFake{}
	dispatcher := replicationDispatcherFunc(func(context.Context, backup.ReplicationTaskRequest) error {
		return errcode.E(errcode.CONFLICT, "snapshot exists at /secret/path")
	})
	c := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{Control: control, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	c.DispatchRun(context.Background(), backup.FrozenReplicationRun{RunID: "run", PolicyID: "p", LeaderTerm: 1, LeaseExpiresUnix: now.Add(time.Minute).Unix(), Tasks: []backup.FrozenReplicationTask{{TaskID: "t", SourceNodeID: "s", TargetNodeID: "d", SnapshotID: "snap", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "PENDING"}}})
	if len(control.updates) != 1 || control.updates[0].Status != "FAILED" || control.updates[0].ErrorCode != "CONFLICT" || strings.Contains(control.updates[0].ErrorSummary, "/secret") {
		t.Fatalf("updates=%+v", control.updates)
	}
}

func TestReplicationCoordinator_ConnectConflictIsTerminalFailed(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	control := &replicationControlFake{}
	connectErr := connect.NewError(connect.CodeFailedPrecondition, nil)
	detail, err := connect.NewErrorDetail(&procmeshv1.ErrorInfo{Code: "CONFLICT", Message: "snapshot exists at /secret/path"})
	if err != nil {
		t.Fatal(err)
	}
	connectErr.AddDetail(detail)
	dispatcher := replicationDispatcherFunc(func(context.Context, backup.ReplicationTaskRequest) error {
		return connectErr
	})
	c := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{Control: control, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	c.DispatchRun(context.Background(), backup.FrozenReplicationRun{RunID: "run", PolicyID: "p", LeaderTerm: 1, LeaseExpiresUnix: now.Add(time.Minute).Unix(), Tasks: []backup.FrozenReplicationTask{{TaskID: "t", SourceNodeID: "s", TargetNodeID: "d", SnapshotID: "snap", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "PENDING"}}})
	if len(control.updates) != 1 || control.updates[0].Status != "FAILED" || control.updates[0].ErrorCode != "CONFLICT" || strings.Contains(control.updates[0].ErrorSummary, "/secret") {
		t.Fatalf("updates=%+v", control.updates)
	}
}

type replicationDispatcherFunc func(context.Context, backup.ReplicationTaskRequest) error

func (f replicationDispatcherFunc) DispatchReplicationTask(ctx context.Context, task backup.ReplicationTaskRequest) error {
	return f(ctx, task)
}

func (f *replicationControlFake) ClaimReplicationRuns(context.Context, uint64, time.Time) ([]backup.FrozenReplicationRun, error) {
	return append([]backup.FrozenReplicationRun(nil), f.runs...), nil
}

func (f *replicationControlFake) UpdateReplicationTask(_ context.Context, update backup.ReplicationTaskUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, update)
	return nil
}

type replicationDispatcherFake struct {
	mu       sync.Mutex
	dispatch []backup.ReplicationTaskRequest
}

func (f *replicationDispatcherFake) DispatchReplicationTask(_ context.Context, task backup.ReplicationTaskRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatch = append(f.dispatch, task)
	return nil
}

func TestReplicationCoordinator_LeaderOnlyDispatchesFrozenPendingRoutes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	control := &replicationControlFake{runs: []backup.FrozenReplicationRun{{
		RunID: "rep-run", PolicyID: "rep-policy", PolicyRevision: 4, LeaderTerm: 9,
		LeaseExpiresUnix: now.Add(time.Minute).Unix(), MaxConcurrency: 2,
		Tasks: []backup.FrozenReplicationTask{{
			TaskID: "task-a", SourceNodeID: "source-a", TargetNodeID: "target-b",
			SnapshotID: "snap-a", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "PENDING",
		}},
	}}}
	dispatcher := &replicationDispatcherFake{}
	leader := false
	coordinator := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{
		Control: control, Dispatcher: dispatcher, IsLeader: func() bool { return leader },
		CurrentTerm: func() uint64 { return 9 }, Now: func() time.Time { return now },
	})
	if err := coordinator.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.dispatch) != 0 {
		t.Fatalf("follower dispatched %+v", dispatcher.dispatch)
	}
	leader = true
	if err := coordinator.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.dispatch) != 1 {
		t.Fatalf("dispatches=%+v", dispatcher.dispatch)
	}
	got := dispatcher.dispatch[0]
	if got.SnapshotID != "snap-a" || got.SHA256 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || got.SourceNodeID != "source-a" {
		t.Fatalf("did not dispatch frozen source reference: %+v", got)
	}
}

func TestReplicationCoordinator_RetrySkipsSucceededRoutes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	control := &replicationControlFake{runs: []backup.FrozenReplicationRun{{
		RunID: "rep-run", PolicyID: "rep-policy", PolicyRevision: 4, LeaderTerm: 9,
		LeaseExpiresUnix: now.Add(time.Minute).Unix(),
		Tasks: []backup.FrozenReplicationTask{
			{TaskID: "task-succeeded", SourceNodeID: "source-a", TargetNodeID: "target-b", SnapshotID: "snap-a", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "SUCCEEDED"},
			{TaskID: "task-failed", SourceNodeID: "source-a", TargetNodeID: "target-c", SnapshotID: "snap-a", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "FAILED"},
		},
	}}}
	dispatcher := &replicationDispatcherFake{}
	coordinator := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{
		Control: control, Dispatcher: dispatcher, IsLeader: func() bool { return true },
		CurrentTerm: func() uint64 { return 9 }, Now: func() time.Time { return now },
	})
	if err := coordinator.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.dispatch) != 1 || dispatcher.dispatch[0].TaskID != "task-failed" {
		t.Fatalf("expected only failed route retry, got %+v", dispatcher.dispatch)
	}
}
