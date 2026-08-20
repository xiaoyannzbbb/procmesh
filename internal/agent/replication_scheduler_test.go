package agent

import (
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
	now := time.Date(2027, 1, 15, 2, 2, 0, 0, time.UTC)
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
