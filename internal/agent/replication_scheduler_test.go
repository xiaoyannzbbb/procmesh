package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

const replicationTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRunnableReplicationRunsIncludesCurrentTermLiveRun(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	state := control.NewState()
	state.ReplicationRuns["run-current"] = control.ClusterBackupRun{
		RunID: "run-current", PolicyID: "rp", PolicyRevision: 1, Status: "RUNNING",
		LeaseUntilUnix: now.Add(time.Minute).Unix(), MaxConcurrency: 2,
	}
	state.ReplicationRunTerms["run-current"] = 7
	state.ReplicationTasks["run-current:task-current"] = control.ClusterBackupTask{
		RunID: "run-current", TaskID: "task-current", SourceNodeID: "source", NodeID: "target",
		SnapshotID: "snapshot", SHA256: replicationTestSHA, Status: "PENDING",
	}

	runs, takeovers := runnableReplicationRuns(*state, 7, now)
	if len(takeovers) != 0 || len(runs) != 1 || runs[0].RunID != "run-current" || len(runs[0].Tasks) != 1 {
		t.Fatalf("runs=%+v takeovers=%v, want current-term live run ready now", runs, takeovers)
	}
}

func TestPlanAutomaticReplicationRunsAfterPrimaryUsesFrozenTaskResult(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	state := automaticReplicationState(now, "AFTER_PRIMARY_BACKUP")

	plans, err := planAutomaticReplicationRuns(*state, 9, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Create.Tasks) != 1 || len(plans[0].MissingTaskIDs) != 0 {
		t.Fatalf("plans=%+v, want one executable route", plans)
	}
	task := plans[0].Create.Tasks[0]
	if task.SourceNodeID != "source" || task.NodeID != "target" || task.SnapshotID != "snapshot-primary" || task.SHA256 != replicationTestSHA || task.TaskID == "" {
		t.Fatalf("task=%+v, want frozen primary task metadata", task)
	}
	if err := state.CreateRun(plans[0].Create); err != nil {
		t.Fatal(err)
	}
	plans, err = planAutomaticReplicationRuns(*state, 9, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("idempotent plans=%+v err=%v, want none", plans, err)
	}
}

func TestPlanAutomaticReplicationRunsAfterPrimaryPlansEveryUnreplicatedPrimaryRun(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	state := automaticReplicationState(now, "AFTER_PRIMARY_BACKUP")
	state.BackupRuns["backup-run-earlier"] = control.ClusterBackupRun{RunID: "backup-run-earlier", PolicyID: "bp", PolicyRevision: 1, Status: "SUCCEEDED", FinishedUnix: now.Add(-40 * time.Second).Unix()}
	state.BackupTasks["backup-run-earlier:task"] = control.ClusterBackupTask{RunID: "backup-run-earlier", TaskID: "task", NodeID: "source", SnapshotID: "snapshot-earlier", SHA256: replicationTestSHA, Status: "SUCCEEDED"}
	plans, err := planAutomaticReplicationRuns(*state, 9, now)
	if err != nil || len(plans) != 2 {
		t.Fatalf("plans=%+v err=%v, want both primary runs", plans, err)
	}
	if plans[0].Create.Run.RunID == plans[1].Create.Run.RunID {
		t.Fatalf("duplicate automatic run IDs")
	}
}

func TestPlanAutomaticReplicationRunsScheduleWithoutPrimaryMarksMissingRoute(t *testing.T) {
	now := time.Date(2027, 1, 15, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, Trigger: "SCHEDULE", ScheduleCron: "0 * * * *", Timezone: "UTC",
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, PrimaryPolicyIDs: []string{"bp"}, Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}

	plans, err := planAutomaticReplicationRuns(*state, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Create.Tasks) != 1 || len(plans[0].MissingTaskIDs) != 1 {
		t.Fatalf("plans=%+v, want one visible missing-source route", plans)
	}
	if task := plans[0].Create.Tasks[0]; task.SnapshotID != "" || task.SHA256 != "" || task.Status != "PENDING" {
		t.Fatalf("missing-source task=%+v", task)
	}
	if err := state.CreateRun(plans[0].Create); err != nil {
		t.Fatal(err)
	}
	plans, err = planAutomaticReplicationRuns(*state, 4, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("same schedule fire plans=%+v err=%v", plans, err)
	}
}

func TestPlanAutomaticReplicationRunsScheduleWithoutPrimaryPolicyIDsUsesSourceSnapshot(t *testing.T) {
	now := time.Date(2027, 1, 15, 2, 0, 0, 0, time.UTC)
	state := automaticReplicationState(now, "SCHEDULE")
	policy := state.ReplicationPolicies["rp"]
	policy.ScheduleCron, policy.Timezone, policy.PrimaryPolicyIDs = "0 * * * *", "UTC", nil
	state.ReplicationPolicies["rp"] = policy
	plans, err := planAutomaticReplicationRuns(*state, 9, now)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	task := plans[0].Create.Tasks[0]
	if task.SnapshotID != "snapshot-primary" || task.SHA256 != replicationTestSHA {
		t.Fatalf("task=%+v, want source frozen primary", task)
	}
}

func TestPlanAutomaticReplicationRunsScheduleSelectsLatestSnapshotForEachSource(t *testing.T) {
	now := time.Date(2027, 1, 15, 2, 2, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, Trigger: "SCHEDULE", ScheduleCron: "0 * * * *", Timezone: "UTC", Revision: 1,
		Routes: []control.ReplicationRoute{
			{SourceNodeID: "source-a", TargetNodeIDs: []string{"target-a"}},
			{SourceNodeID: "source-b", TargetNodeIDs: []string{"target-b"}},
		},
	}
	state.BackupRuns["run-a"] = control.ClusterBackupRun{RunID: "run-a", PolicyID: "primary-a", Status: "SUCCEEDED", FinishedUnix: now.Add(-2 * time.Hour).Unix()}
	state.BackupRuns["run-b"] = control.ClusterBackupRun{RunID: "run-b", PolicyID: "primary-b", Status: "SUCCEEDED", FinishedUnix: now.Add(-time.Hour).Unix()}
	state.BackupTasks["run-a:task-a"] = control.ClusterBackupTask{RunID: "run-a", TaskID: "task-a", NodeID: "source-a", SnapshotID: "snapshot-a", SHA256: replicationTestSHA, Status: "SUCCEEDED", UpdatedUnix: now.Add(-2 * time.Hour).Unix()}
	state.BackupTasks["run-b:task-b"] = control.ClusterBackupTask{RunID: "run-b", TaskID: "task-b", NodeID: "source-b", SnapshotID: "snapshot-b", SHA256: strings.Repeat("b", 64), Status: "SUCCEEDED", UpdatedUnix: now.Add(-time.Hour).Unix()}

	plans, err := planAutomaticReplicationRuns(*state, 9, now)
	if err != nil || len(plans) != 1 || len(plans[0].Create.Tasks) != 2 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	tasks := plans[0].Create.Tasks
	if tasks[0].SourceNodeID != "source-a" || tasks[0].SnapshotID != "snapshot-a" || tasks[0].SHA256 != replicationTestSHA {
		t.Fatalf("first source task=%+v, want source-a frozen snapshot", tasks[0])
	}
	if tasks[1].SourceNodeID != "source-b" || tasks[1].SnapshotID != "snapshot-b" || tasks[1].SHA256 != strings.Repeat("b", 64) {
		t.Fatalf("second source task=%+v, want source-b frozen snapshot", tasks[1])
	}
}

func TestReplicationFrozenTasksStableByTaskID(t *testing.T) {
	state := control.NewState()
	run := control.ClusterBackupRun{RunID: "run", Status: "RUNNING"}
	state.ReplicationTasks["run:z"] = control.ClusterBackupTask{RunID: "run", TaskID: "z", SourceNodeID: "s", NodeID: "t", SnapshotID: "z", SHA256: replicationTestSHA, Status: "PENDING"}
	state.ReplicationTasks["run:a"] = control.ClusterBackupTask{RunID: "run", TaskID: "a", SourceNodeID: "s", NodeID: "t", SnapshotID: "a", SHA256: replicationTestSHA, Status: "PENDING"}
	tasks := replicationFrozenTasks(*state, run)
	if len(tasks) != 2 || tasks[0].TaskID != "a" || tasks[1].TaskID != "z" {
		t.Fatalf("tasks=%+v", tasks)
	}
}

func TestReconcileMissingReplicationTasksReturnsApplyFailureThenRetriesOnNextTick(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{PolicyID: "rp", Enabled: true, Trigger: "SCHEDULE"}
	state.ReplicationRuns["run-missing"] = control.ClusterBackupRun{RunID: "run-missing", PolicyID: "rp", Status: "RUNNING"}
	task := control.ClusterBackupTask{RunID: "run-missing", TaskID: "route-missing", SourceNodeID: "source", NodeID: "target", Status: "PENDING"}
	state.ReplicationTasks["run-missing:route-missing"] = task
	applyErr := errors.New("raft apply failed")
	failed := &recordingReplicationApplier{err: applyErr}
	if err := reconcileMissingReplicationTasks(failed, *state, 7, now); !errors.Is(err, applyErr) {
		t.Fatalf("first reconciliation error = %v, want apply failure", err)
	}
	if len(failed.commands) != 1 {
		t.Fatalf("first reconciliation commands = %d, want 1", len(failed.commands))
	}

	retried := &recordingReplicationApplier{}
	if err := reconcileMissingReplicationTasks(retried, *state, 7, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(retried.commands) != 1 {
		t.Fatalf("second reconciliation commands = %d, want 1", len(retried.commands))
	}
	var update control.UpdateTaskBody
	if err := json.Unmarshal(retried.commands[0].Body, &update); err != nil {
		t.Fatal(err)
	}
	if update.Task.Status != "FAILED" || update.Task.ErrorCode != "SOURCE_SNAPSHOT_MISSING" || update.Task.SnapshotID != "" || update.Task.SHA256 != "" {
		t.Fatalf("second reconciliation update = %+v", update.Task)
	}
}

func TestReconcileMissingReplicationTasksDoesNotDependOnMutablePolicy(t *testing.T) {
	now := time.Unix(1_800_000_100, 0)
	for _, tc := range []struct {
		name   string
		policy *control.ReplicationPolicy
	}{
		{name: "policy deleted"},
		{name: "trigger changed", policy: &control.ReplicationPolicy{PolicyID: "rp", Trigger: "MANUAL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := control.NewState()
			if tc.policy != nil {
				state.ReplicationPolicies[tc.policy.PolicyID] = *tc.policy
			}
			state.ReplicationRuns["run-missing"] = control.ClusterBackupRun{RunID: "run-missing", PolicyID: "rp", Status: "RUNNING"}
			state.ReplicationTasks["run-missing:route-missing"] = control.ClusterBackupTask{RunID: "run-missing", TaskID: "route-missing", SourceNodeID: "source", NodeID: "target", Status: "PENDING"}
			applier := &recordingReplicationApplier{}
			if err := reconcileMissingReplicationTasks(applier, *state, 7, now); err != nil {
				t.Fatal(err)
			}
			if len(applier.commands) != 1 {
				t.Fatalf("commands = %d, want durable task reconciliation", len(applier.commands))
			}
		})
	}
}

type recordingReplicationApplier struct {
	commands []control.Command
	err      error
}

func TestReplicationRetryBeginCommandContainsOnlySafeFrozenMetadata(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	applier := &recordingReplicationApplier{}
	update := backup.ReplicationTaskUpdate{
		RunID: "run", TaskID: "task", SourceNodeID: "source", TargetNodeID: "target",
		SnapshotID: "snapshot", SHA256: replicationTestSHA, Bytes: 42,
		ErrorCode: "UNAVAILABLE", ErrorSummary: "safe summary", LeaderTerm: 7,
	}
	if err := applyBeginReplicationTask(context.Background(), applier, update, now); err != nil {
		t.Fatal(err)
	}
	if len(applier.commands) != 1 || applier.commands[0].Type != control.CmdBackupTaskUpdate {
		t.Fatalf("commands=%+v", applier.commands)
	}
	var body control.UpdateTaskBody
	if err := json.Unmarshal(applier.commands[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if !body.Replication || body.LeaderTerm != 7 || body.Task.Status != "RUNNING" || body.Task.UpdatedUnix != now.Unix() {
		t.Fatalf("begin body=%+v", body)
	}
	if body.Task.RunID != "run" || body.Task.TaskID != "task" || body.Task.SourceNodeID != "source" || body.Task.NodeID != "target" || body.Task.SnapshotID != "snapshot" || body.Task.SHA256 != replicationTestSHA {
		t.Fatalf("frozen identity changed: %+v", body.Task)
	}
	if body.Task.Bytes != 0 || body.Task.ErrorCode != "" || body.Task.ErrorSummary != "" {
		t.Fatalf("prior result metadata retained: %+v", body.Task)
	}
}

func TestReplicationRetryBeginHonorsCanceledContextBeforeRaftApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	applier := &recordingReplicationApplier{}
	err := applyBeginReplicationTask(ctx, applier, backup.ReplicationTaskUpdate{
		RunID: "run", TaskID: "task", SourceNodeID: "source", TargetNodeID: "target",
		SnapshotID: "snapshot", SHA256: replicationTestSHA, LeaderTerm: 7,
	}, time.Unix(1_800_000_000, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("begin error=%v, want context canceled", err)
	}
	if len(applier.commands) != 0 {
		t.Fatalf("raft apply after canceled begin: %+v", applier.commands)
	}
}

func TestReplicationRetryAuthorizationRequiresRunningTask(t *testing.T) {
	task := control.ClusterBackupTask{
		RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target",
		SnapshotID: "snapshot", SHA256: replicationTestSHA, Status: "UNAVAILABLE",
	}
	request := &procmeshv1.ReplicateSnapshotRequest{
		RunId: "run", TaskId: "task", SourceNodeId: "source", TargetNodeId: "target",
		SnapshotId: "snapshot", Sha256: replicationTestSHA,
	}
	if replicationTaskMatchesDispatch(task, request, "source") {
		t.Fatal("failure-state task authorized before durable begin")
	}
	task.Status = "RUNNING"
	if !replicationTaskMatchesDispatch(task, request, "source") {
		t.Fatal("running task rejected after durable begin")
	}
	request.SnapshotId = "changed"
	if replicationTaskMatchesDispatch(task, request, "source") {
		t.Fatal("changed frozen identity authorized")
	}
}

func TestPutSnapshotPolicyAuthorizationMatchesFrozenReplicationRun(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	node, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: "target", ClusterID: "cluster"})
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
		command, encodeErr := control.EncodeCommand(commandType, body)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if applyErr := node.Apply(command, raftApplyTO); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "source", Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "target", Status: control.MemberAdmitted})
	apply(control.CmdReplicationPolicyPut, control.ReplicationPolicyPutBody{
		OperationID: "policy", PolicyID: "frozen-policy", Name: "policy", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, ReplicaFactor: 1,
		Routes:  []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
		Trigger: "MANUAL", ExpectedRevision: -1,
	})
	term := node.CurrentTerm()
	apply(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: "run", LeaderTerm: term, Replication: true,
		Run:   control.ClusterBackupRun{RunID: "run", PolicyID: "frozen-policy", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING", LeaseUntilUnix: now.Add(time.Minute).Unix()},
		Tasks: []control.ClusterBackupTask{{RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot", SHA256: replicationTestSHA, Status: "PENDING"}},
	})
	apply(control.CmdBackupTaskUpdate, control.UpdateTaskBody{
		OperationID: "begin", LeaderTerm: term, Replication: true,
		Task: control.ClusterBackupTask{RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot", SHA256: replicationTestSHA, Status: "RUNNING", UpdatedUnix: now.Unix()},
	})
	runtime := &rpcRuntime{nodeID: "target", clusterID: "cluster", node: node}

	tests := []struct {
		name           string
		policyID       string
		policyRevision int64
		wantErr        bool
	}{
		{name: "exact frozen identity", policyID: "frozen-policy", policyRevision: 1},
		{name: "mismatched policy ID", policyID: "changed-policy", policyRevision: 1, wantErr: true},
		{name: "mismatched policy revision", policyID: "frozen-policy", policyRevision: 2, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runtime.authorizePeerOperation("source", api.PeerOperation{
				Kind: "PUT", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target",
				SnapshotID: "snapshot", SHA256: replicationTestSHA, RunID: "run", TaskID: "task",
				PolicyID: test.policyID, PolicyRevision: test.policyRevision,
			})
			if test.wantErr && err == nil {
				t.Fatal("mismatched frozen policy identity authorized")
			}
			if test.wantErr && !errcode.Is(err, errcode.CONFLICT) && !errcode.Is(err, errcode.DENIED) {
				t.Fatalf("mismatched frozen policy identity error = %v, want CONFLICT or DENIED", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("exact frozen policy identity rejected: %v", err)
			}
		})
	}
}

func TestAuthorizePeerOperationDeleteIntent(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	node, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: "target", ClusterID: "cluster"})
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
		command, encodeErr := control.EncodeCommand(commandType, body)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if applyErr := node.Apply(command, raftApplyTO); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "source", Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "target", Status: control.MemberAdmitted})
	term := node.CurrentTerm()
	intent := control.ReplicationDeleteIntent{
		IntentID: "intent-1", PolicyID: "rp", PolicyRevision: 2,
		SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot",
		LeaderTerm: term, ExpiresUnix: now.Add(time.Minute).Unix(), Status: "PENDING",
	}
	apply(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{OperationID: "op-intent", Intent: intent})
	runtime := &rpcRuntime{nodeID: "target", clusterID: "cluster", node: node}

	exact := api.PeerOperation{
		Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target",
		SnapshotID: "snapshot", IntentID: "intent-1", PolicyID: "rp", PolicyRevision: 2,
	}
	if err := runtime.authorizePeerOperation("source", exact); err != nil {
		t.Fatalf("exact pending intent rejected: %v", err)
	}

	for _, tc := range []struct {
		name string
		op   api.PeerOperation
	}{
		{name: "missing intent", op: api.PeerOperation{Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot", IntentID: "missing", PolicyID: "rp", PolicyRevision: 2}},
		{name: "policy mismatch", op: api.PeerOperation{Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot", IntentID: "intent-1", PolicyID: "other", PolicyRevision: 2}},
		{name: "revision mismatch", op: api.PeerOperation{Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot", IntentID: "intent-1", PolicyID: "rp", PolicyRevision: 3}},
		{name: "snapshot mismatch", op: api.PeerOperation{Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "other", IntentID: "intent-1", PolicyID: "rp", PolicyRevision: 2}},
		{name: "target mismatch", op: api.PeerOperation{Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "other", SnapshotID: "snapshot", IntentID: "intent-1", PolicyID: "rp", PolicyRevision: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runtime.authorizePeerOperation("source", tc.op)
			if !errcode.Is(err, errcode.DENIED) {
				t.Fatalf("error=%v want DENIED", err)
			}
		})
	}

	t.Run("expired and stale term", func(t *testing.T) {
		apply(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{
			OperationID: "op-stale", Intent: control.ReplicationDeleteIntent{
				IntentID: "intent-stale", PolicyID: "rp", PolicyRevision: 2,
				SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot",
				LeaderTerm: term + 10, ExpiresUnix: now.Add(time.Minute).Unix(), Status: "PENDING",
			},
		})
		err := runtime.authorizePeerOperation("source", api.PeerOperation{
			Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target",
			SnapshotID: "snapshot", IntentID: "intent-stale", PolicyID: "rp", PolicyRevision: 2,
		})
		if !errcode.Is(err, errcode.DENIED) {
			t.Fatalf("stale-term error=%v want DENIED", err)
		}

		apply(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{
			OperationID: "op-live-expire", Intent: control.ReplicationDeleteIntent{
				IntentID: "intent-live-expire", PolicyID: "rp", PolicyRevision: 2,
				SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot",
				LeaderTerm: term, ExpiresUnix: time.Now().Add(1500 * time.Millisecond).Unix(), Status: "PENDING",
			},
		})
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			err = runtime.authorizePeerOperation("source", api.PeerOperation{
				Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target",
				SnapshotID: "snapshot", IntentID: "intent-live-expire", PolicyID: "rp", PolicyRevision: 2,
			})
			if errcode.Is(err, errcode.DENIED) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("expired intent still authorized: %v", err)
	})

	t.Run("complete marks succeeded", func(t *testing.T) {
		if err := runtime.completeDeleteIntent(exact); err != nil {
			t.Fatal(err)
		}
		got := node.View().ReplicationDeleteIntents["intent-1"]
		if got.Status != "SUCCEEDED" {
			t.Fatalf("status=%q", got.Status)
		}
		if err := runtime.authorizePeerOperation("source", exact); !errcode.Is(err, errcode.DENIED) {
			t.Fatalf("succeeded intent still authorized: %v", err)
		}
	})
}

func (a *recordingReplicationApplier) Apply(command control.Command, _ time.Duration) error {
	a.commands = append(a.commands, command)
	return a.err
}

func automaticReplicationState(now time.Time, trigger string) *control.State {
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, Trigger: trigger, SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"},
		PrimaryPolicyIDs: []string{"bp"}, Revision: 1, MaxConcurrency: 2,
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	state.BackupRuns["backup-run"] = control.ClusterBackupRun{
		RunID: "backup-run", PolicyID: "bp", PolicyRevision: 1, Status: "SUCCEEDED",
		CreatedUnix: now.Add(-time.Minute).Unix(), FinishedUnix: now.Add(-30 * time.Second).Unix(),
	}
	state.BackupTasks["backup-run:backup-task"] = control.ClusterBackupTask{
		RunID: "backup-run", TaskID: "backup-task", NodeID: "source", SnapshotID: "snapshot-primary",
		SHA256: replicationTestSHA, Status: "SUCCEEDED",
	}
	return state
}
