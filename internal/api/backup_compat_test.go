package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/process"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

// TestBackupAPI_OldCreateBackupStillLocal verifies that the old CreateBackup
// API remains local-only and does NOT use cluster namespacing.
func TestBackupAPI_OldCreateBackupStillLocal(t *testing.T) {
	ctx := context.Background()
	e, _ := testBackupEngine(t, "node-local")
	api := &BackupAPI{Engine: e, LocalID: "node-local"}

	created, err := api.CreateBackup(ctx, connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-old", Operator: "test"},
		Sink: "fs",
	}))
	if err != nil {
		t.Fatal(err)
	}

	snap := created.Msg.GetSnapshot()
	if snap.GetNodeId() != "node-local" {
		t.Fatalf("old API should use local node: %s", snap.GetNodeId())
	}

	// Verify snapshot accessible via old Engine.ListLocal
	list, err := e.ListLocal(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("old API snapshot not in local list: %+v %v", list, err)
	}
	if list[0].SnapshotID != snap.GetSnapshotId() {
		t.Fatalf("snapshot id mismatch")
	}
}

// TestBackupAPI_OldPeerSinkStillCallable verifies that the old "peer" sink
// parameter still works for backward compatibility.
func TestBackupAPI_OldPeerSinkStillCallable(t *testing.T) {
	ctx := context.Background()
	e, _ := testBackupEngine(t, "node-peer")

	peerPushed := false
	e.PeerPush = backup.PeerPushFunc(func(ctx context.Context, nodeID, sourceNodeID string, payload []byte) error {
		peerPushed = true
		if nodeID != "node-target" {
			t.Fatalf("wrong target: %s", nodeID)
		}
		return nil
	})
	e.Admitted = func(nodeID string) bool { return nodeID == "node-target" }

	api := &BackupAPI{Engine: e, LocalID: "node-peer"}

	created, err := api.CreateBackup(ctx, connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Meta:          &procmeshv1.MutationMeta{OperationId: "op-peer", Operator: "test"},
		Sink:          "peer",
		TargetNodeIds: []string{"node-target"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !peerPushed {
		t.Fatal("peer sink should call PeerPush")
	}

	snap := created.Msg.GetSnapshot()
	if snap.GetSink() != "peer" {
		t.Fatalf("sink should be peer: %s", snap.GetSink())
	}
}

// TestBackupAPI_OldFSKeysReadable verifies that snapshots created with old
// (non-namespaced) FS keys are still readable.
func TestBackupAPI_OldFSKeysReadable(t *testing.T) {
	ctx := context.Background()
	e, mgr := testBackupEngine(t, "node-old-fs")

	// Seed a process
	spec := process.ProcessSpec{ProcessID: "p1", Name: "old", Command: "/bin/true"}
	if _, err := mgr.ApplySpec(ctx, spec, 0, "op-seed", "test", ""); err != nil {
		t.Fatal(err)
	}

	// Create via old Engine.Create (non-cluster)
	meta, err := e.Create(ctx, backup.CreateOpts{Sink: "fs"})
	if err != nil {
		t.Fatal(err)
	}

	api := &BackupAPI{Engine: e, LocalID: "node-old-fs"}

	// List should include old snapshot
	listed, err := api.ListBackups(ctx, connect.NewRequest(&procmeshv1.ListBackupsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetEntries()) != 1 {
		t.Fatalf("old snapshot not listed: %d entries", len(listed.Msg.GetEntries()))
	}
	ent := listed.Msg.GetEntries()[0]
	if ent.GetSnapshot().GetSnapshotId() != meta.SnapshotID {
		t.Fatalf("snapshot id mismatch: got %s want %s", ent.GetSnapshot().GetSnapshotId(), meta.SnapshotID)
	}

	// Get should return old snapshot
	got, err := api.GetBackup(ctx, connect.NewRequest(&procmeshv1.GetBackupRequest{
		SnapshotId:     meta.SnapshotID,
		Sink:           "fs",
		IncludePayload: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetSnapshot().GetSnapshotId() != meta.SnapshotID {
		t.Fatalf("get snapshot id mismatch")
	}
	if len(got.Msg.GetPayload()) == 0 {
		t.Fatal("payload should be included")
	}
}

// TestBackupAPI_OldPermissionsUnchanged verifies that old backup API
// permissions remain the same (PermBackupManage for Create, PermBackupRead for List/Get).
func TestBackupAPI_OldPermissionsUnchanged(t *testing.T) {
	// This is already covered by TestBackupAPI_DeniedWithoutPerm in backup_test.go
	// which verifies that viewer (without PermBackupManage) cannot CreateBackup
	// and cannot ListBackups.
	// No additional test needed here.
}
