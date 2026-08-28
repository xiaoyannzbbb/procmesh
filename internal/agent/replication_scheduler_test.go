package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

const replicationTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRunnableReplicationRunsSkipsCurrentTermLiveRun(t *testing.T) {
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
	if len(takeovers) != 0 || len(runs) != 0 {
		t.Fatalf("runs=%+v takeovers=%v, want StartRun to dispatch current-term live work", runs, takeovers)
	}
}

func TestPlanAutomaticReplicationRuns_SkipsFireAtOrBeforeEpoch(t *testing.T) {
	now := time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC) // 15:00
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: now.Unix(), Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("caught-up plans=%+v err=%v", plans, err)
	}
}

func TestPlanAutomaticReplicationRuns_FiresAfterEpoch(t *testing.T) {
	epoch := time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC)
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: epoch.Unix(), Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	task := plans[0].Create.Tasks[0]
	if task.SnapshotID != "" || task.SHA256 != "" || task.Status != "PENDING" {
		t.Fatalf("capture-pending task=%+v", task)
	}
}

func TestPlanAutomaticReplicationRuns_SkipsDisabledAndEmptyCron(t *testing.T) {
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["off"] = control.ReplicationPolicy{
		PolicyID: "off", Enabled: false, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		Routes: []control.ReplicationRoute{{SourceNodeID: "s", TargetNodeIDs: []string{"t"}}},
	}
	state.ReplicationPolicies["manual"] = control.ReplicationPolicy{
		PolicyID: "manual", Enabled: true,
		Routes: []control.ReplicationRoute{{SourceNodeID: "s", TargetNodeIDs: []string{"t"}}},
	}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("plans=%+v", plans)
	}
}

func TestPlanAutomaticReplicationRuns_SkipFireWhenPolicyRunning(t *testing.T) {
	epoch := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: epoch.Unix(),
		Routes:            []control.ReplicationRoute{{SourceNodeID: "s", TargetNodeIDs: []string{"t"}}},
	}
	state.ReplicationRuns["run-live"] = control.ClusterBackupRun{RunID: "run-live", PolicyID: "rp", Status: "RUNNING"}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("plans=%+v, want skip", plans)
	}
	// 实现后：ClaimReplicationRuns 应写入 SKIPPED fire，随后 Tick 不再建 run
}

func TestPlanAutomaticReplicationRuns_IgnoresLegacyTriggerAndPrimaryPolicyIDs(t *testing.T) {
	epoch := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, Trigger: "AFTER_PRIMARY_BACKUP", ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: epoch.Unix(), Revision: 1, PrimaryPolicyIDs: []string{"bp"},
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	state.BackupRuns["backup-run"] = control.ClusterBackupRun{RunID: "backup-run", PolicyID: "bp", Status: "SUCCEEDED", FinishedUnix: now.Unix()}
	state.BackupTasks["backup-run:task"] = control.ClusterBackupTask{
		RunID: "backup-run", TaskID: "task", NodeID: "source", SnapshotID: "snapshot-primary",
		SHA256: replicationTestSHA, Status: "SUCCEEDED",
	}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v, want cron fire ignoring Trigger", plans, err)
	}
	task := plans[0].Create.Tasks[0]
	if task.SnapshotID != "" || task.SHA256 != "" || task.Status != "PENDING" {
		t.Fatalf("capture-pending task=%+v, must not bind BackupRuns", task)
	}
}

func TestPlanAutomaticReplicationRuns_SkipsExistingLedgerFire(t *testing.T) {
	epoch := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: epoch.Unix(), Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	key := replicationFireKey("rp", now.Unix())
	state.BackupFireLedger[key] = control.FireRecord{FireKey: key, PolicyID: "rp", Status: "SKIPPED"}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("ledger plans=%+v err=%v", plans, err)
	}
}

func TestPlanAutomaticReplicationRuns_IdempotentWhenRunExists(t *testing.T) {
	epoch := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: epoch.Unix(), Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	state.ReplicationRuns[plans[0].Create.Run.RunID] = plans[0].Create.Run
	plans, err = planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("idempotent plans=%+v err=%v", plans, err)
	}
}

func TestClaimReplicationRuns_CreatesEmptySnapshotRunAndClaimsFire(t *testing.T) {
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	node, apply := startScheduledReplicationControl(t)
	apply(control.CmdReplicationPolicyPut, control.ReplicationPolicyPutBody{
		OperationID: "policy", PolicyID: "rp", Name: "rp", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, ReplicaFactor: 1,
		Routes:       []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
		ScheduleCron: "0 2 * * *", Timezone: "UTC", ExpectedRevision: -1,
	})
	ctrl := raftReplicationControl{runtime: &rpcRuntime{node: node}}
	term := node.CurrentTerm()
	runs, err := ctrl.ClaimReplicationRuns(context.Background(), term, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || len(runs[0].Tasks) != 1 {
		t.Fatalf("claimed=%+v, want one capture-pending run", runs)
	}
	task := runs[0].Tasks[0]
	if task.SnapshotID != "" || task.SHA256 != "" || task.Status != "PENDING" {
		t.Fatalf("task=%+v, want empty snapshot PENDING", task)
	}
	stored := node.View().ReplicationTasks[runs[0].RunID+":"+task.TaskID]
	if stored.SnapshotID != "" || stored.SHA256 != "" || stored.Status != "PENDING" {
		t.Fatalf("stored task=%+v", stored)
	}
	key := replicationFireKey("rp", now.Unix())
	record, ok := node.View().BackupFireLedger[key]
	if !ok || record.Status != "CLAIMED" || record.PolicyID != "rp" {
		t.Fatalf("ledger=%+v ok=%v, want CLAIMED replication fire", record, ok)
	}
	if _, ok := node.View().BackupFireLedger["rp:"+strconv.FormatInt(now.Unix(), 10)]; ok {
		t.Fatal("replication fire collided with cluster backup fire key")
	}

	again, err := ctrl.ClaimReplicationRuns(context.Background(), term, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(node.View().ReplicationRuns) != 1 {
		t.Fatalf("runs=%v, want idempotent single automatic run", node.View().ReplicationRuns)
	}
	for _, run := range again {
		if run.RunID != runs[0].RunID {
			t.Fatalf("second claim created extra run %+v", run)
		}
	}
}

func TestClaimReplicationRuns_WritesSkippedFireWhenPolicyRunning(t *testing.T) {
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	node, apply := startScheduledReplicationControl(t)
	apply(control.CmdReplicationPolicyPut, control.ReplicationPolicyPutBody{
		OperationID: "policy", PolicyID: "rp", Name: "rp", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, ReplicaFactor: 1,
		Routes:       []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
		ScheduleCron: "0 2 * * *", Timezone: "UTC", ExpectedRevision: -1,
	})
	term := node.CurrentTerm()
	apply(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: "run-live", LeaderTerm: term, Replication: true,
		Run: control.ClusterBackupRun{
			RunID: "run-live", PolicyID: "rp", PolicyRevision: 1, TargetNodeIDs: []string{"source"},
			Status: "RUNNING", LeaseUntilUnix: now.Add(time.Hour).Unix(),
		},
		Tasks: []control.ClusterBackupTask{{
			RunID: "run-live", TaskID: "task-live", SourceNodeID: "source", NodeID: "target", Status: "PENDING",
		}},
	})
	ctrl := raftReplicationControl{runtime: &rpcRuntime{node: node}}
	claimed, err := ctrl.ClaimReplicationRuns(context.Background(), term, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range claimed {
		if run.RunID != "run-live" {
			t.Fatalf("created automatic run during RUNNING: %+v", run)
		}
	}
	key := replicationFireKey("rp", now.Unix())
	record, ok := node.View().BackupFireLedger[key]
	if !ok || record.Status != "SKIPPED" || record.PolicyID != "rp" {
		t.Fatalf("ledger=%+v ok=%v, want SKIPPED fire", record, ok)
	}

	apply(control.CmdBackupRunFinish, control.FinishRunBody{
		OperationID: "finish-live", RunID: "run-live", Status: "SUCCEEDED", FinishedUnix: now.Unix(), LeaderTerm: term, Replication: true,
	})
	after, err := ctrl.ClaimReplicationRuns(context.Background(), term, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := node.View().ReplicationRuns["run-live"]; !ok {
		t.Fatal("finished live run missing")
	}
	for _, run := range after {
		if run.RunID != "run-live" {
			t.Fatalf("SKIPPED slot created run after RUNNING ended: %+v", run)
		}
	}
	if len(node.View().ReplicationRuns) != 1 {
		t.Fatalf("runs=%v, want only finished live run", node.View().ReplicationRuns)
	}
}

func startScheduledReplicationControl(t *testing.T) (*control.Node, func(string, any)) {
	t.Helper()
	node, apply := startBackupSchedulerControl(t, "cluster-replication-schedule", "leader")
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "source", Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "target", Status: control.MemberAdmitted})
	return node, apply
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

func TestReplicationFrozenTasksIncludesCapturePending(t *testing.T) {
	state := control.NewState()
	run := control.ClusterBackupRun{RunID: "run", Status: "RUNNING"}
	state.ReplicationTasks["run:a"] = control.ClusterBackupTask{RunID: "run", TaskID: "a", SourceNodeID: "s", NodeID: "t", Status: "PENDING"}
	tasks := replicationFrozenTasks(*state, run)
	if len(tasks) != 1 || tasks[0].TaskID != "a" || tasks[0].SnapshotID != "" || tasks[0].SHA256 != "" {
		t.Fatalf("tasks=%+v, want capture-pending", tasks)
	}
}

type recordingReplicationApplier struct {
	commands   []control.Command
	err        error
	afterApply func()
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

func TestReplicationRetryBeginTreatsCommittedApplyAsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	applier := &recordingReplicationApplier{afterApply: cancel}
	err := applyBeginReplicationTask(ctx, applier, backup.ReplicationTaskUpdate{
		RunID: "run", TaskID: "task", SourceNodeID: "source", TargetNodeID: "target",
		SnapshotID: "snapshot", SHA256: replicationTestSHA, LeaderTerm: 7,
	}, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatalf("begin after committed apply error=%v, want nil", err)
	}
	if len(applier.commands) != 1 {
		t.Fatalf("committed begin commands=%+v", applier.commands)
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
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "other-source", Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "target", Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "other-target", Status: control.MemberAdmitted})
	term := node.CurrentTerm()
	intent := control.ReplicationDeleteIntent{
		IntentID: "intent-1", PolicyID: "rp", PolicyRevision: 2,
		SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot",
		LeaderTerm: term, ExpiresUnix: now.Add(time.Minute).Unix(), Status: "PENDING",
	}
	apply(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{OperationID: "op-intent", Intent: intent})
	apply(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{
		OperationID: "op-intent-other-target", Intent: control.ReplicationDeleteIntent{
			IntentID: "intent-other-target", PolicyID: "rp", PolicyRevision: 2,
			SourceNodeID: "source", TargetNodeID: "other-target", SnapshotID: "snapshot",
			LeaderTerm: term, ExpiresUnix: now.Add(time.Minute).Unix(), Status: "PENDING",
		},
	})
	apply(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{
		OperationID: "op-intent-other-source", Intent: control.ReplicationDeleteIntent{
			IntentID: "intent-other-source", PolicyID: "rp", PolicyRevision: 2,
			SourceNodeID: "other-source", TargetNodeID: "target", SnapshotID: "snapshot",
			LeaderTerm: term, ExpiresUnix: now.Add(time.Minute).Unix(), Status: "PENDING",
		},
	})
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
		// Operation identity matches local/mTLS; only the durable intent target differs.
		{name: "intent target mismatch", op: api.PeerOperation{Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot", IntentID: "intent-other-target", PolicyID: "rp", PolicyRevision: 2}},
		// Operation identity matches local/mTLS admitted source; only the durable intent source differs.
		{name: "intent source mismatch", op: api.PeerOperation{Kind: "DELETE", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot", IntentID: "intent-other-source", PolicyID: "rp", PolicyRevision: 2}},
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

func TestAuthorizePeerOperationPutFencing(t *testing.T) {
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
		OperationID: "policy", PolicyID: "rp", Name: "policy", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"}, ReplicaFactor: 1,
		Routes:  []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
		Trigger: "MANUAL", ExpectedRevision: -1,
	})
	term := node.CurrentTerm()
	liveOp := api.PeerOperation{
		Kind: "PUT", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target",
		SnapshotID: "snapshot", SHA256: replicationTestSHA, RunID: "run-live", TaskID: "task-live",
		PolicyID: "rp", PolicyRevision: 1,
	}
	apply(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: "run-live", LeaderTerm: term, Replication: true,
		Run:   control.ClusterBackupRun{RunID: "run-live", PolicyID: "rp", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING", LeaseUntilUnix: now.Add(time.Hour).Unix()},
		Tasks: []control.ClusterBackupTask{{RunID: "run-live", TaskID: "task-live", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot", SHA256: replicationTestSHA, Status: "PENDING"}},
	})
	apply(control.CmdBackupTaskUpdate, control.UpdateTaskBody{
		OperationID: "begin-live", LeaderTerm: term, Replication: true,
		Task: control.ClusterBackupTask{RunID: "run-live", TaskID: "task-live", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot", SHA256: replicationTestSHA, Status: "RUNNING", UpdatedUnix: now.Unix()},
	})
	apply(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: "run-expired", LeaderTerm: term, Replication: true,
		Run:   control.ClusterBackupRun{RunID: "run-expired", PolicyID: "rp", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING", LeaseUntilUnix: now.Add(-time.Minute).Unix()},
		Tasks: []control.ClusterBackupTask{{RunID: "run-expired", TaskID: "task-expired", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot-expired", SHA256: replicationTestSHA, Status: "PENDING"}},
	})
	apply(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: "run-stale", LeaderTerm: term + 10, Replication: true,
		Run:   control.ClusterBackupRun{RunID: "run-stale", PolicyID: "rp", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING", LeaseUntilUnix: now.Add(time.Hour).Unix()},
		Tasks: []control.ClusterBackupTask{{RunID: "run-stale", TaskID: "task-stale", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot-stale", SHA256: replicationTestSHA, Status: "PENDING"}},
	})
	runtime := &rpcRuntime{nodeID: "target", clusterID: "cluster", node: node}
	if err := runtime.authorizePeerOperation("source", liveOp); err != nil {
		t.Fatalf("live put rejected: %v", err)
	}

	for _, tc := range []struct {
		name string
		op   api.PeerOperation
	}{
		{name: "expired lease", op: api.PeerOperation{Kind: "PUT", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot-expired", SHA256: replicationTestSHA, RunID: "run-expired", TaskID: "task-expired", PolicyID: "rp", PolicyRevision: 1}},
		{name: "stale term", op: api.PeerOperation{Kind: "PUT", ClusterID: "cluster", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot-stale", SHA256: replicationTestSHA, RunID: "run-stale", TaskID: "task-stale", PolicyID: "rp", PolicyRevision: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runtime.authorizePeerOperation("source", tc.op)
			if !errcode.Is(err, errcode.DENIED) && !errcode.Is(err, errcode.CONFLICT) {
				t.Fatalf("error=%v want DENIED or CONFLICT", err)
			}
		})
	}

	apply(control.CmdBackupTaskUpdate, control.UpdateTaskBody{
		OperationID: "fail-live", LeaderTerm: term, Replication: true,
		Task: control.ClusterBackupTask{RunID: "run-live", TaskID: "task-live", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot", SHA256: replicationTestSHA, Status: "FAILED", UpdatedUnix: now.Unix()},
	})
	if err := runtime.authorizePeerOperation("source", liveOp); !errcode.Is(err, errcode.DENIED) && !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("terminal task still authorized: %v", err)
	}
}

func (a *recordingReplicationApplier) Apply(command control.Command, _ time.Duration) error {
	a.commands = append(a.commands, command)
	if a.afterApply != nil {
		a.afterApply()
	}
	return a.err
}
