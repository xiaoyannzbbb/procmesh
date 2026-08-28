package backup_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/hashicorp/raft"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type replicationControlFake struct {
	mu       sync.Mutex
	runs     []backup.FrozenReplicationRun
	updates  []backup.ReplicationTaskUpdate
	beginErr error
	begin    func(context.Context, backup.ReplicationTaskUpdate) error
}

type replicationFSMControl struct {
	fsm *control.FSM
	now time.Time
}

func (c *replicationFSMControl) ClaimReplicationRuns(context.Context, uint64, time.Time) ([]backup.FrozenReplicationRun, error) {
	return nil, nil
}

func (c *replicationFSMControl) BeginReplicationTask(_ context.Context, update backup.ReplicationTaskUpdate) error {
	update.Status = "RUNNING"
	return c.apply(update)
}

func (c *replicationFSMControl) UpdateReplicationTask(_ context.Context, update backup.ReplicationTaskUpdate) error {
	return c.apply(update)
}

type beginThenExpireControl struct {
	inner *replicationFSMControl
	now   *time.Time
}

func (c *beginThenExpireControl) ClaimReplicationRuns(ctx context.Context, term uint64, now time.Time) ([]backup.FrozenReplicationRun, error) {
	return c.inner.ClaimReplicationRuns(ctx, term, now)
}

func (c *beginThenExpireControl) BeginReplicationTask(ctx context.Context, update backup.ReplicationTaskUpdate) error {
	if err := c.inner.BeginReplicationTask(ctx, update); err != nil {
		return err
	}
	*c.now = c.now.Add(2 * time.Second)
	c.inner.now = *c.now
	return nil
}

func (c *beginThenExpireControl) UpdateReplicationTask(ctx context.Context, update backup.ReplicationTaskUpdate) error {
	return c.inner.UpdateReplicationTask(ctx, update)
}

func (c *replicationFSMControl) apply(update backup.ReplicationTaskUpdate) error {
	cmd, err := control.EncodeCommand(control.CmdBackupTaskUpdate, control.UpdateTaskBody{
		OperationID: "test-replication-" + update.RunID + ":" + update.TaskID,
		LeaderTerm:  update.LeaderTerm,
		Replication: true,
		Task: control.ClusterBackupTask{
			RunID: update.RunID, TaskID: update.TaskID, SourceNodeID: update.SourceNodeID, NodeID: update.TargetNodeID,
			SnapshotID: update.SnapshotID, SHA256: update.SHA256, Status: update.Status, Bytes: update.Bytes,
			ErrorCode: update.ErrorCode, ErrorSummary: update.ErrorSummary, UpdatedUnix: c.now.Unix(),
		},
	})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	if result := c.fsm.Apply(&raft.Log{Data: raw, AppendedAt: c.now}); result != nil {
		if applyErr, ok := result.(error); ok {
			return applyErr
		}
		return fmt.Errorf("unexpected raft apply result")
	}
	return nil
}

func applyReplicationFSMCommand(t *testing.T, fsm *control.FSM, now time.Time, typ string, body any) {
	t.Helper()
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result := fsm.Apply(&raft.Log{Data: raw, AppendedAt: now}); result != nil {
		t.Fatalf("apply %s: %v", typ, result)
	}
}

func TestReplicationCoordinator_BeginsEmptySnapshotCapturePending(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fsm := control.NewFSM()
	for _, nodeID := range []string{"source", "target"} {
		applyReplicationFSMCommand(t, fsm, now, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted})
	}
	applyReplicationFSMCommand(t, fsm, now, control.CmdReplicationPolicyPut, control.ReplicationPolicyPutBody{
		OperationID: "policy", PolicyID: "policy", Name: "policy", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, ReplicaFactor: 1,
		Routes:  []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
		Trigger: "MANUAL", ExpectedRevision: -1,
	})
	run := control.ClusterBackupRun{
		RunID: "run", PolicyID: "policy", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING",
		CreatedUnix: now.Unix(), StartedUnix: now.Unix(), LeaseUntilUnix: now.Add(time.Minute).Unix(),
	}
	task := control.ClusterBackupTask{RunID: run.RunID, TaskID: "capture", SourceNodeID: "source", NodeID: "target", Status: "PENDING"}
	applyReplicationFSMCommand(t, fsm, now, control.CmdBackupRunCreate, control.CreateRunBody{OperationID: "create", Run: run, Tasks: []control.ClusterBackupTask{task}, LeaderTerm: 7, Replication: true})

	controlPlane := &replicationFSMControl{fsm: fsm, now: now}
	var begun backup.ReplicationTaskRequest
	dispatcher := replicationDispatcherFunc(func(_ context.Context, request backup.ReplicationTaskRequest) error {
		begun = request
		got := fsm.View().ReplicationTasks[request.RunID+":"+request.TaskID]
		if got.Status != "RUNNING" || got.SnapshotID != "" || got.SHA256 != "" {
			return errcode.E(errcode.CONFLICT, "empty snapshot was not begun")
		}
		return controlPlane.UpdateReplicationTask(context.Background(), backup.ReplicationTaskUpdate{
			RunID: request.RunID, TaskID: request.TaskID, SourceNodeID: request.SourceNodeID, TargetNodeID: request.TargetNodeID,
			SnapshotID: "snap-captured", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "SUCCEEDED", LeaderTerm: request.LeaderTerm,
		})
	})
	coordinator := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{Control: controlPlane, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	coordinator.DispatchRun(context.Background(), backup.FrozenReplicationRun{
		RunID: run.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, LeaderTerm: 7,
		LeaseExpiresUnix: run.LeaseUntilUnix,
		Tasks:            []backup.FrozenReplicationTask{{TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.NodeID, Status: task.Status}},
	})
	if begun.TaskID != "capture" || begun.SnapshotID != "" {
		t.Fatalf("dispatched %+v, want empty snapshot capture-pending", begun)
	}
	got := fsm.View().ReplicationTasks[run.RunID+":"+task.TaskID]
	if got.Status != "SUCCEEDED" || got.SnapshotID != "snap-captured" {
		t.Fatalf("task=%+v", got)
	}
}

func TestReplicationCoordinator_DispatchFallbackKeepsCapturedSnapshot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fsm := control.NewFSM()
	for _, nodeID := range []string{"source", "target"} {
		applyReplicationFSMCommand(t, fsm, now, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted})
	}
	applyReplicationFSMCommand(t, fsm, now, control.CmdReplicationPolicyPut, control.ReplicationPolicyPutBody{
		OperationID: "policy", PolicyID: "policy", Name: "policy", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, ReplicaFactor: 1,
		Routes:  []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
		Trigger: "MANUAL", ExpectedRevision: -1,
	})
	run := control.ClusterBackupRun{
		RunID: "run", PolicyID: "policy", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING",
		CreatedUnix: now.Unix(), StartedUnix: now.Unix(), LeaseUntilUnix: now.Add(time.Minute).Unix(),
	}
	task := control.ClusterBackupTask{RunID: run.RunID, TaskID: "task", SourceNodeID: "source", NodeID: "target", Status: "PENDING"}
	applyReplicationFSMCommand(t, fsm, now, control.CmdBackupRunCreate, control.CreateRunBody{OperationID: "create", Run: run, Tasks: []control.ClusterBackupTask{task}, LeaderTerm: 7, Replication: true})

	controlPlane := &replicationFSMControl{fsm: fsm, now: now}
	dispatcher := replicationDispatcherFunc(func(_ context.Context, request backup.ReplicationTaskRequest) error {
		if err := controlPlane.BeginReplicationTask(context.Background(), backup.ReplicationTaskUpdate{
			RunID: request.RunID, TaskID: request.TaskID, SourceNodeID: request.SourceNodeID, TargetNodeID: request.TargetNodeID,
			SnapshotID: "snap-captured", SHA256: sha, Status: "RUNNING", LeaderTerm: request.LeaderTerm,
		}); err != nil {
			return err
		}
		got := fsm.View().ReplicationTasks[request.RunID+":"+request.TaskID]
		if got.SnapshotID != "snap-captured" || got.SHA256 != sha {
			return fmt.Errorf("dispatcher did not store snapshot: %+v", got)
		}
		return errcode.E(errcode.UNAVAILABLE, "copy failed after capture")
	})
	coordinator := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{Control: controlPlane, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	coordinator.DispatchRun(context.Background(), backup.FrozenReplicationRun{
		RunID: run.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, LeaderTerm: 7,
		LeaseExpiresUnix: run.LeaseUntilUnix,
		Tasks:            []backup.FrozenReplicationTask{{TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.NodeID, Status: task.Status}},
	})
	got := fsm.View().ReplicationTasks[run.RunID+":"+task.TaskID]
	if got.SnapshotID != "snap-captured" || got.SHA256 != sha {
		t.Fatalf("coordinator cleared captured snapshot: %+v", got)
	}
	if got.Status != "UNAVAILABLE" && got.Status != "FAILED" {
		t.Fatalf("status=%s, want UNAVAILABLE or FAILED", got.Status)
	}
}

func TestReplicationCoordinator_DispatchFallbackUsesCapturedErrorIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	controlPlane := &replicationControlFake{}
	dispatcher := replicationDispatcherFunc(func(context.Context, backup.ReplicationTaskRequest) error {
		return &backup.CapturedReplicationError{
			SnapshotID: "snap-captured", SHA256: sha,
			Err: errcode.E(errcode.UNAVAILABLE, "update after copy failed"),
		}
	})
	c := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{Control: controlPlane, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	c.DispatchRun(context.Background(), backup.FrozenReplicationRun{
		RunID: "run", PolicyID: "p", LeaderTerm: 1, LeaseExpiresUnix: now.Add(time.Minute).Unix(),
		Tasks: []backup.FrozenReplicationTask{{TaskID: "t", SourceNodeID: "s", TargetNodeID: "d", Status: "PENDING"}},
	})
	if len(controlPlane.updates) != 1 || controlPlane.updates[0].SnapshotID != "snap-captured" || controlPlane.updates[0].SHA256 != sha {
		t.Fatalf("fallback updates=%+v, want captured snapshot", controlPlane.updates)
	}
}

func TestReplicationCoordinator_BeginsRetryBeforeDispatch(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fsm := control.NewFSM()
	for _, nodeID := range []string{"source", "target", "other-target"} {
		applyReplicationFSMCommand(t, fsm, now, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted})
	}
	applyReplicationFSMCommand(t, fsm, now, control.CmdReplicationPolicyPut, control.ReplicationPolicyPutBody{
		OperationID: "policy", PolicyID: "policy", Name: "policy", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, ReplicaFactor: 2,
		Routes:  []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target", "other-target"}}},
		Trigger: "MANUAL", ExpectedRevision: -1,
	})
	run := control.ClusterBackupRun{
		RunID: "run", PolicyID: "policy", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING",
		CreatedUnix: now.Unix(), StartedUnix: now.Unix(), LeaseUntilUnix: now.Add(time.Minute).Unix(),
	}
	retryTask := control.ClusterBackupTask{RunID: run.RunID, TaskID: "retry", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot", SHA256: sha, Status: "PENDING"}
	otherTask := control.ClusterBackupTask{RunID: run.RunID, TaskID: "other", SourceNodeID: "source", NodeID: "other-target", SnapshotID: "other-snapshot", SHA256: sha, Status: "PENDING"}
	applyReplicationFSMCommand(t, fsm, now, control.CmdBackupRunCreate, control.CreateRunBody{OperationID: "create", Run: run, Tasks: []control.ClusterBackupTask{retryTask, otherTask}, LeaderTerm: 7, Replication: true})
	retryTask.Status, retryTask.ErrorCode, retryTask.ErrorSummary = "UNAVAILABLE", "UNAVAILABLE", "safe summary"
	applyReplicationFSMCommand(t, fsm, now, control.CmdBackupTaskUpdate, control.UpdateTaskBody{OperationID: "fail", Task: retryTask, LeaderTerm: 7, Replication: true})

	controlPlane := &replicationFSMControl{fsm: fsm, now: now}
	dispatches := 0
	dispatcher := replicationDispatcherFunc(func(_ context.Context, request backup.ReplicationTaskRequest) error {
		dispatches++
		got := fsm.View().ReplicationTasks[request.RunID+":"+request.TaskID]
		if got.Status != "RUNNING" {
			return errcode.E(errcode.CONFLICT, "task was not begun")
		}
		return controlPlane.UpdateReplicationTask(context.Background(), backup.ReplicationTaskUpdate{
			RunID: request.RunID, TaskID: request.TaskID, SourceNodeID: request.SourceNodeID, TargetNodeID: request.TargetNodeID,
			SnapshotID: request.SnapshotID, SHA256: request.SHA256, Status: "SUCCEEDED", LeaderTerm: request.LeaderTerm,
		})
	})
	coordinator := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{Control: controlPlane, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	coordinator.DispatchRun(context.Background(), backup.FrozenReplicationRun{
		RunID: run.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, LeaderTerm: 7,
		LeaseExpiresUnix: run.LeaseUntilUnix,
		Tasks:            []backup.FrozenReplicationTask{{TaskID: retryTask.TaskID, SourceNodeID: retryTask.SourceNodeID, TargetNodeID: retryTask.NodeID, SnapshotID: retryTask.SnapshotID, SHA256: retryTask.SHA256, Status: retryTask.Status}},
	})

	got := fsm.View().ReplicationTasks[run.RunID+":"+retryTask.TaskID]
	if dispatches != 1 || got.Status != "SUCCEEDED" {
		t.Fatalf("dispatches=%d task=%+v", dispatches, got)
	}
	if got.RunID != "run" || got.TaskID != "retry" || got.SourceNodeID != "source" || got.NodeID != "target" || got.SnapshotID != "snapshot" || got.SHA256 != sha {
		t.Fatalf("immutable retry identity changed: %+v", got)
	}
}

func TestReplicationCoordinator_BeginRejectionPreventsDispatch(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	controlPlane := &replicationControlFake{beginErr: errcode.E(errcode.CONFLICT, "stale retry")}
	dispatcher := &replicationDispatcherFake{}
	coordinator := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{Control: controlPlane, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	coordinator.DispatchRun(context.Background(), backup.FrozenReplicationRun{
		RunID: "run", PolicyID: "policy", LeaderTerm: 7, LeaseExpiresUnix: now.Add(time.Minute).Unix(),
		Tasks: []backup.FrozenReplicationTask{{TaskID: "task", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "UNAVAILABLE"}},
	})
	if len(dispatcher.dispatch) != 0 {
		t.Fatalf("dispatch after rejected begin: %+v", dispatcher.dispatch)
	}
}

func TestReplicationCoordinator_LeaseExpiryDuringBeginPreventsDispatch(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fsm := control.NewFSM()
	for _, nodeID := range []string{"source", "target", "other-target"} {
		applyReplicationFSMCommand(t, fsm, now, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted})
	}
	applyReplicationFSMCommand(t, fsm, now, control.CmdReplicationPolicyPut, control.ReplicationPolicyPutBody{
		OperationID: "policy", PolicyID: "policy", Name: "policy", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, ReplicaFactor: 2,
		Routes:  []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target", "other-target"}}},
		Trigger: "MANUAL", ExpectedRevision: -1,
	})
	run := control.ClusterBackupRun{
		RunID: "run", PolicyID: "policy", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING",
		CreatedUnix: now.Unix(), StartedUnix: now.Unix(), LeaseUntilUnix: now.Add(time.Second).Unix(),
	}
	task := control.ClusterBackupTask{RunID: run.RunID, TaskID: "task", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot", SHA256: sha, Status: "PENDING"}
	other := control.ClusterBackupTask{RunID: run.RunID, TaskID: "other", SourceNodeID: "source", NodeID: "other-target", SnapshotID: "other-snapshot", SHA256: sha, Status: "PENDING"}
	applyReplicationFSMCommand(t, fsm, now, control.CmdBackupRunCreate, control.CreateRunBody{OperationID: "create", Run: run, Tasks: []control.ClusterBackupTask{task, other}, LeaderTerm: 7, Replication: true})
	task.Status, task.ErrorCode, task.ErrorSummary = "UNAVAILABLE", "UNAVAILABLE", "safe summary"
	applyReplicationFSMCommand(t, fsm, now, control.CmdBackupTaskUpdate, control.UpdateTaskBody{OperationID: "fail", Task: task, LeaderTerm: 7, Replication: true})

	controlPlane := &beginThenExpireControl{inner: &replicationFSMControl{fsm: fsm, now: now}, now: &now}
	dispatcher := &replicationDispatcherFake{}
	coordinator := backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{Control: controlPlane, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	coordinator.DispatchRun(context.Background(), backup.FrozenReplicationRun{
		RunID: run.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, LeaderTerm: 7,
		LeaseExpiresUnix: run.LeaseUntilUnix,
		Tasks:            []backup.FrozenReplicationTask{{TaskID: task.TaskID, SourceNodeID: task.SourceNodeID, TargetNodeID: task.NodeID, SnapshotID: task.SnapshotID, SHA256: task.SHA256, Status: task.Status}},
	})
	if len(dispatcher.dispatch) != 0 {
		t.Fatalf("dispatch after lease expired during begin: %+v", dispatcher.dispatch)
	}
	got := fsm.View().ReplicationTasks[run.RunID+":"+task.TaskID]
	if got.Status == "RUNNING" {
		t.Fatalf("begin abort left task stuck running: %+v", got)
	}
	if got.Status != "TIMEOUT" && got.Status != "UNAVAILABLE" {
		t.Fatalf("post-abort status=%s, want retryable TIMEOUT or UNAVAILABLE", got.Status)
	}
	if got.RunID != run.RunID || got.TaskID != task.TaskID || got.SourceNodeID != "source" || got.NodeID != "target" || got.SnapshotID != "snapshot" || got.SHA256 != sha {
		t.Fatalf("immutable identity changed after begin abort: %+v", got)
	}
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

func TestReplicationPushPropagatesFrozenPolicyIdentity(t *testing.T) {
	ctx := context.Background()
	st, _ := seedProcess(t)
	fsRoot := filepath.Join(t.TempDir(), "fs")
	var pushed backup.ReplicationPushRequest
	engine := &backup.Engine{
		Store: st, NodeID: "source", ClusterID: "cluster", Sinks: map[string]backup.Sink{"fs": backup.NewFSSink(fsRoot)},
		NewID: func() (string, error) { return "snapshot", nil },
		ReplicationPush: backup.ReplicationPeerPushFunc(func(_ context.Context, request backup.ReplicationPushRequest, _ []byte) error {
			pushed = request
			return nil
		}),
	}
	meta, err := engine.CreateCluster(ctx, backup.ClusterCreateOpts{RunID: "primary-run", TaskID: "primary-task", PolicyID: "primary-policy", ClusterID: "cluster", NodeID: "source", Sink: "fs"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.ReplicateSnapshot(ctx, backup.ReplicationTaskRequest{
		RunID: "replication-run", TaskID: "replication-task", PolicyID: "frozen-policy", PolicyRevision: 4,
		SourceNodeID: "source", TargetNodeID: "target", SnapshotID: meta.SnapshotID, SHA256: meta.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pushed.PolicyID != "frozen-policy" || pushed.PolicyRevision != 4 {
		t.Fatalf("replication push policy identity = %q/%d, want frozen-policy/4", pushed.PolicyID, pushed.PolicyRevision)
	}
}

type replicationDispatcherFunc func(context.Context, backup.ReplicationTaskRequest) error

func (f replicationDispatcherFunc) DispatchReplicationTask(ctx context.Context, task backup.ReplicationTaskRequest) error {
	return f(ctx, task)
}

func (f *replicationControlFake) ClaimReplicationRuns(context.Context, uint64, time.Time) ([]backup.FrozenReplicationRun, error) {
	return append([]backup.FrozenReplicationRun(nil), f.runs...), nil
}

func (f *replicationControlFake) BeginReplicationTask(ctx context.Context, update backup.ReplicationTaskUpdate) error {
	if f.begin != nil {
		return f.begin(ctx, update)
	}
	return f.beginErr
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
