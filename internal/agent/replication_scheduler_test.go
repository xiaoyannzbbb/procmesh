package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
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
