package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
)

// TestTickSchedule_LegacyLocalScheduleStillWorks verifies that the old
// agent.yaml backup.schedule field still triggers local FS snapshots when
// no cluster backup policies exist.
func TestTickSchedule_LegacyLocalScheduleStillWorks(t *testing.T) {
	e := seededEngine(t)
	e.Schedule = "0 * * * *" // hourly
	e.Now = func() time.Time { return time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC) }

	// Tick should fire
	if err := e.TickSchedule(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify snapshot created via old path
	list, err := e.ListLocal(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("legacy schedule should create snapshot: %+v %v", list, err)
	}
	if list[0].Sink != "fs" {
		t.Fatalf("wrong sink: %+v", list[0])
	}
}

// TestTickSchedule_OldPathStillReadable verifies that snapshots created
// before Task 1 namespacing are still readable.
func TestTickSchedule_OldPathStillReadable(t *testing.T) {
	ctx := context.Background()
	st, spec := seedProcess(t)
	fsDir := filepath.Join(t.TempDir(), "fs")
	e := &backup.Engine{
		Store:     st,
		NodeID:    "node-old",
		ClusterID: "c1",
		Sinks:     map[string]backup.Sink{"fs": backup.NewFSSink(fsDir)},
		PeerStore: &backup.PeerStore{Root: t.TempDir()},
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		NewID:     func() (string, error) { return "snap-old", nil },
	}

	// Create via old Engine.Create (not CreateCluster)
	meta, err := e.Create(ctx, backup.CreateOpts{Sink: "fs"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify readable via ListLocal
	list, err := e.ListLocal(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("old snapshot unreadable: %+v %v", list, err)
	}
	if list[0].SnapshotID != meta.SnapshotID {
		t.Fatalf("snapshot id mismatch: got %s want %s", list[0].SnapshotID, meta.SnapshotID)
	}

	// Verify Get still works
	getMeta, payload, err := e.Get(ctx, meta.SnapshotID, "fs")
	if err != nil || len(payload) == 0 {
		t.Fatalf("old snapshot not gettable: %v", err)
	}
	if getMeta.SnapshotID != meta.SnapshotID {
		t.Fatalf("get meta mismatch")
	}

	// Verify old path layout: {fs_dir}/{snapshot_id}.json (no namespace)
	oldPath := filepath.Join(fsDir, meta.SnapshotID+".json")
	if _, err := os.ReadFile(oldPath); err != nil {
		t.Fatalf("old path not accessible: %s: %v", oldPath, err)
	}

	// Process spec should match original
	snap, err := backup.Decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Processes) != 1 || snap.Processes[0].ProcessID != spec.ProcessID {
		t.Fatalf("process mismatch: %+v", snap.Processes)
	}
}
