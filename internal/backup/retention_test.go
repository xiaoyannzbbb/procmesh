package backup_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
)

func TestRetentionPlan_KeepLastIsPerPolicyAndSource(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	snapshots := []backup.RetentionSnapshot{
		retentionSnapshot("a-1", "bp", "node-a", now.Add(-4*time.Hour), 10),
		retentionSnapshot("a-2", "bp", "node-a", now.Add(-3*time.Hour), 10),
		retentionSnapshot("a-3", "bp", "node-a", now.Add(-2*time.Hour), 10),
		retentionSnapshot("b-1", "bp", "node-b", now.Add(-4*time.Hour), 10),
		retentionSnapshot("b-2", "bp", "node-b", now.Add(-3*time.Hour), 10),
		retentionSnapshot("other-policy", "other", "node-a", now.Add(-24*time.Hour), 10),
	}
	got, err := backup.PlanRetention(now, backup.Policy{PolicyID: "bp", Timezone: "UTC", RetentionKeepLast: 2}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	assertRetentionIDs(t, got, "a-1")
}

func TestRetentionPlan_KeepDaysUsesPolicyTimezoneCalendarBoundary(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 0, 30, 0, 0, loc)
	snapshots := []backup.RetentionSnapshot{
		retentionSnapshot("before-boundary", "bp", "node-a", time.Date(2026, 8, 18, 23, 59, 0, 0, loc), 10),
		retentionSnapshot("at-boundary", "bp", "node-a", time.Date(2026, 8, 19, 0, 0, 0, 0, loc), 10),
		retentionSnapshot("today", "bp", "node-a", time.Date(2026, 8, 20, 0, 1, 0, 0, loc), 10),
	}
	got, err := backup.PlanRetention(now, backup.Policy{PolicyID: "bp", Timezone: "Asia/Shanghai", RetentionKeepDays: 2}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	assertRetentionIDs(t, got, "before-boundary")
}

func TestRetentionPlan_MaxBytesDeletesOldestCompletedUntilWithinLimit(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	snapshots := []backup.RetentionSnapshot{
		retentionSnapshot("oldest", "bp", "node-a", now.Add(-4*time.Hour), 100),
		retentionSnapshot("active", "bp", "node-a", now.Add(-3*time.Hour), 100),
		retentionSnapshot("last-replica", "bp", "node-a", now.Add(-2*time.Hour), 100),
		retentionSnapshot("newest", "bp", "node-a", now.Add(-time.Hour), 100),
	}
	snapshots[1].Active = true
	snapshots[2].LastUsableReplica = true
	got, err := backup.PlanRetention(now, backup.Policy{PolicyID: "bp", Timezone: "UTC", RetentionMaxBytes: 250}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	// Protected snapshots still count toward usage; only the oldest deletable
	// snapshots may be selected, even if protection prevents reaching the cap.
	assertRetentionIDs(t, got, "oldest", "newest")
}

func TestRetentionPlan_CombinesDimensionsWithoutDeletingProtectedOrRunning(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	snapshots := []backup.RetentionSnapshot{
		retentionSnapshot("old", "bp", "node-a", now.AddDate(0, 0, -10), 100),
		retentionSnapshot("copying", "bp", "node-a", now.AddDate(0, 0, -9), 100),
		retentionSnapshot("restore", "bp", "node-a", now.AddDate(0, 0, -8), 100),
		retentionSnapshot("keep", "bp", "node-a", now.Add(-time.Hour), 100),
	}
	snapshots[1].Active = true
	snapshots[2].Active = true
	got, err := backup.PlanRetention(now, backup.Policy{PolicyID: "bp", Timezone: "UTC", RetentionKeepLast: 1, RetentionKeepDays: 2}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	assertRetentionIDs(t, got, "old")
}

func TestRetentionPlan_KeepLastProtectsNewestOutsideDayWindow(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	snapshots := []backup.RetentionSnapshot{
		retentionSnapshot("oldest", "bp", "node-a", now.AddDate(0, 0, -20), 10),
		retentionSnapshot("newest", "bp", "node-a", now.AddDate(0, 0, -10), 10),
	}
	got, err := backup.PlanRetention(now, backup.Policy{PolicyID: "bp", Timezone: "UTC", RetentionKeepLast: 1, RetentionKeepDays: 2}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	assertRetentionIDs(t, got, "oldest")
}

func TestRetentionPlan_RejectsInvalidTimezone(t *testing.T) {
	_, err := backup.PlanRetention(time.Now(), backup.Policy{PolicyID: "bp", Timezone: "Mars/Olympus", RetentionKeepDays: 1}, nil)
	if err == nil {
		t.Fatal("expected invalid timezone")
	}
}

func TestRetentionPlan_LastRemainingCopyPerSourceIsKept(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	snapshots := []backup.RetentionSnapshot{
		retentionSnapshot("old", "rp-1", "node-a", now.AddDate(0, 0, -10), 100),
		retentionSnapshot("newest", "rp-1", "node-a", now.AddDate(0, 0, -9), 100),
		retentionSnapshot("other-source", "rp-1", "node-b", now.AddDate(0, 0, -10), 100),
	}
	got, err := backup.PlanRetention(now, backup.Policy{PolicyID: "rp-1", Timezone: "UTC", RetentionKeepDays: 1}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	assertRetentionIDs(t, got, "old")
}

func TestRetentionPlan_PeerKeepLastIndependentOfSourcePresence(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	policy := backup.Policy{PolicyID: "rp-1", Timezone: "UTC", RetentionKeepLast: 1}
	peerCopies := []backup.RetentionSnapshot{
		retentionSnapshot("old", "rp-1", "node-a", now.Add(-2*time.Hour), 50),
		retentionSnapshot("newest", "rp-1", "node-a", now.Add(-time.Hour), 50),
	}
	got, err := backup.PlanRetention(now, policy, peerCopies)
	if err != nil {
		t.Fatal(err)
	}
	assertRetentionIDs(t, got, "old")

	sourceGone := []backup.RetentionSnapshot{
		retentionSnapshot("old", "rp-1", "node-a", now.Add(-2*time.Hour), 50),
	}
	got, err = backup.PlanRetention(now, policy, sourceGone)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("peer last remaining deleted while source is gone: %+v", got)
	}
}

func TestRetentionPlan_PeerMaxBytesIndependentOfSourceBytes(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	peerCopies := []backup.RetentionSnapshot{
		retentionSnapshot("old", "rp-1", "node-a", now.Add(-2*time.Hour), 80),
		retentionSnapshot("newest", "rp-1", "node-a", now.Add(-time.Hour), 80),
	}
	got, err := backup.PlanRetention(now, backup.Policy{PolicyID: "rp-1", Timezone: "UTC", RetentionMaxBytes: 100}, peerCopies)
	if err != nil {
		t.Fatal(err)
	}
	assertRetentionIDs(t, got, "old")
}

func TestEngineApplyRetentionReportsDelete(t *testing.T) {
	ctx := context.Background()
	e := seededEngine(t)
	ids := []string{"snap-1", "snap-2", "snap-3"}
	times := []time.Time{
		time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC),
	}
	index := 0
	e.NewID = func() (string, error) { id := ids[index]; return id, nil }
	e.Now = func() time.Time { return times[index] }
	for index < len(ids) {
		if _, err := e.CreateCluster(ctx, backup.ClusterCreateOpts{RunID: "run-" + ids[index], TaskID: "task-" + ids[index], PolicyID: "bp", ClusterID: "c1", NodeID: "n1", Sink: "fs"}); err != nil {
			t.Fatal(err)
		}
		index++
	}
	var got []backup.RetentionDeleteEvent
	e.OnRetentionDelete = func(_ context.Context, ev backup.RetentionDeleteEvent) {
		got = append(got, ev)
	}
	e.Now = func() time.Time { return time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC) }
	if _, err := e.ApplyRetention(ctx, backup.Policy{PolicyID: "bp", Sink: "fs", Timezone: "UTC", RetentionKeepLast: 2}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SnapshotID != "snap-1" || got[0].Sink != "fs" || got[0].PolicyID != "bp" || got[0].Status != "SUCCESS" {
		t.Fatalf("retention events=%+v", got)
	}
	if got[0].RunID != "run-snap-1" || got[0].TaskID != "task-snap-1" {
		t.Fatalf("expected run/task IDs, got %+v", got[0])
	}
}

func TestEngineApplyRetentionDeletesClusterObjectAndIndex(t *testing.T) {
	ctx := context.Background()
	e := seededEngine(t)
	ids := []string{"snap-1", "snap-2", "snap-3"}
	times := []time.Time{
		time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC),
	}
	index := 0
	e.NewID = func() (string, error) { id := ids[index]; return id, nil }
	e.Now = func() time.Time { return times[index] }
	locations := make([]string, 0, len(ids))
	for index < len(ids) {
		meta, err := e.CreateCluster(ctx, backup.ClusterCreateOpts{RunID: "run-" + ids[index], TaskID: "task-" + ids[index], PolicyID: "bp", ClusterID: "c1", NodeID: "n1", Sink: "fs"})
		if err != nil {
			t.Fatal(err)
		}
		locations = append(locations, meta.Location)
		index++
	}
	e.Now = func() time.Time { return time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC) }
	results, err := e.ApplyRetention(ctx, backup.Policy{PolicyID: "bp", Sink: "fs", Timezone: "UTC", RetentionKeepLast: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SnapshotID != "snap-1" || results[0].Status != "SUCCESS" {
		t.Fatalf("results=%+v", results)
	}
	if _, err := os.Stat(locations[0]); !os.IsNotExist(err) {
		t.Fatalf("deleted object still exists: %v", err)
	}
	listed, err := e.ListLocal(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	for _, meta := range listed {
		if meta.SnapshotID == "snap-1" {
			t.Fatalf("deleted snapshot retained in index: %+v", listed)
		}
	}
}

func TestEngineApplyRetentionPreservesProtectedSnapshot(t *testing.T) {
	ctx := context.Background()
	e := seededEngine(t)
	ids := []string{"snap-old", "snap-new"}
	index := 0
	e.NewID = func() (string, error) { return ids[index], nil }
	e.Now = func() time.Time { return time.Date(2026, 8, 18+index, 8, 0, 0, 0, time.UTC) }
	for index < len(ids) {
		if _, err := e.CreateCluster(ctx, backup.ClusterCreateOpts{RunID: "run-" + ids[index], TaskID: "task-" + ids[index], PolicyID: "bp", ClusterID: "c1", NodeID: "n1", Sink: "fs"}); err != nil {
			t.Fatal(err)
		}
		index++
	}
	e.Now = func() time.Time { return time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC) }
	release := e.ProtectSnapshot("snap-old")
	results, err := e.ApplyRetention(ctx, backup.Policy{PolicyID: "bp", Sink: "fs", Timezone: "UTC", RetentionKeepLast: 1})
	if err != nil || len(results) != 0 {
		t.Fatalf("protected results=%+v err=%v", results, err)
	}
	release()
	results, err = e.ApplyRetention(ctx, backup.Policy{PolicyID: "bp", Sink: "fs", Timezone: "UTC", RetentionKeepLast: 1})
	if err != nil || len(results) != 1 || results[0].SnapshotID != "snap-old" {
		t.Fatalf("released results=%+v err=%v", results, err)
	}
}

func retentionSnapshot(id, policyID, source string, created time.Time, bytes int64) backup.RetentionSnapshot {
	return backup.RetentionSnapshot{SnapshotID: id, PolicyID: policyID, SourceNodeID: source, CreatedAt: created, Bytes: bytes, Status: "SUCCEEDED"}
}

func assertRetentionIDs(t *testing.T, got []backup.RetentionSnapshot, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got=%+v want=%v", got, want)
	}
	for i := range want {
		if got[i].SnapshotID != want[i] {
			t.Fatalf("got=%+v want=%v", got, want)
		}
	}
}
