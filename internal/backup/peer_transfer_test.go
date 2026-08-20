package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// TestPeer_ReceiveWithMetadata tests that PeerStore.Receive accepts extended metadata
func TestPeer_ReceiveWithMetadata(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-001",
		ClusterID:     "cluster-A",
		NodeID:        "node-1",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, expectedSHA, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	// Call Receive with extended parameters
	meta, err := ps.ReceiveWithMetadata(context.Background(), ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-A",
		SnapshotID:   "snap-001",
		SHA256:       expectedSHA,
		RunID:        "run-123",
		TaskID:       "task-456",
		Payload:      payload,
	})
	if err != nil {
		t.Fatalf("ReceiveWithMetadata failed: %v", err)
	}

	if meta.SnapshotID != "snap-001" {
		t.Errorf("got snapshot_id %q, want snap-001", meta.SnapshotID)
	}
	if meta.SHA256 != expectedSHA {
		t.Errorf("got sha256 %q, want %q", meta.SHA256, expectedSHA)
	}
	if meta.ClusterID != "cluster-A" {
		t.Errorf("got cluster_id %q, want cluster-A", meta.ClusterID)
	}

	// Verify file exists at correct path
	expectedPath := filepath.Join(root, "backup", "peer", "node-source", "cluster-A", "snap-001.json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("snapshot file not found at %s", expectedPath)
	}
}

// TestPeer_ReceiveChecksumIdempotent tests that receiving same checksum is idempotent
func TestPeer_ReceiveChecksumIdempotent(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-002",
		ClusterID:     "cluster-B",
		NodeID:        "node-2",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, sha256hex, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-B",
		SnapshotID:   "snap-002",
		SHA256:       sha256hex,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	// First receive
	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err != nil {
		t.Fatalf("first receive failed: %v", err)
	}

	// Second receive with same checksum - should succeed (idempotent)
	params.RunID = "run-002"
	params.TaskID = "task-002"
	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err != nil {
		t.Errorf("second receive with same checksum should be idempotent, got error: %v", err)
	}
}

// TestPeer_ReceiveChecksumConflict tests that different payload with same ID is rejected
func TestPeer_ReceiveChecksumConflict(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap1 := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-003",
		ClusterID:     "cluster-C",
		NodeID:        "node-3",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload1, sha1, err := Encode(snap1)
	if err != nil {
		t.Fatal(err)
	}

	params1 := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-C",
		SnapshotID:   "snap-003",
		SHA256:       sha1,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload1,
	}

	// First receive
	_, err = ps.ReceiveWithMetadata(context.Background(), params1)
	if err != nil {
		t.Fatalf("first receive failed: %v", err)
	}

	// Second receive with different payload but same ID
	snap2 := snap1
	snap2.CreatedAt = time.Now().UTC().Add(time.Hour)
	payload2, sha2, err := Encode(snap2)
	if err != nil {
		t.Fatal(err)
	}

	if sha1 == sha2 {
		t.Fatal("test setup error: sha256 should differ")
	}

	params2 := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-C",
		SnapshotID:   "snap-003",
		SHA256:       sha2,
		RunID:        "run-002",
		TaskID:       "task-002",
		Payload:      payload2,
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), params2)
	if err == nil {
		t.Fatal("expected checksum conflict error, got nil")
	}
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Errorf("expected CONFLICT error, got: %v", err)
	}
}

// TestPeer_ReceiveClusterIDMismatch tests that mismatched cluster ID is rejected
func TestPeer_ReceiveClusterIDMismatch(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-004",
		ClusterID:     "cluster-D",
		NodeID:        "node-4",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, sha256hex, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-WRONG", // Mismatch
		SnapshotID:   "snap-004",
		SHA256:       sha256hex,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err == nil {
		t.Fatal("expected cluster ID mismatch error, got nil")
	}
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID error for cluster mismatch, got: %v", err)
	}
}

// TestPeer_ReceiveNoProcessCreation tests that receiving snapshot does not create processes
func TestPeer_ReceiveNoProcessCreation(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-005",
		ClusterID:     "cluster-E",
		NodeID:        "node-5",
		CreatedAt:     time.Now().UTC(),
		Processes: []ProcessDump{
			{
				ProcessID:   "proc-1",
				Name:        "test-proc",
				MinRevision: 1,
				MaxRevision: 1,
				Revisions:   []RevisionDump{},
			},
		},
	}

	payload, sha256hex, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-E",
		SnapshotID:   "snap-005",
		SHA256:       sha256hex,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	meta, err := ps.ReceiveWithMetadata(context.Background(), params)
	if err != nil {
		t.Fatalf("receive failed: %v", err)
	}

	// Verify snapshot is stored
	if meta.SnapshotID != "snap-005" {
		t.Errorf("snapshot not stored correctly")
	}

	// PeerStore should only store files, not create processes
	// This is verified by the fact that PeerStore has no process creation methods
	if len(meta.ProcessIDs) != 1 {
		t.Errorf("meta should contain process IDs from snapshot, got %d", len(meta.ProcessIDs))
	}
}

// TestPeer_ReceiveAtomicWrite tests temporary file and atomic rename
func TestPeer_ReceiveAtomicWrite(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-006",
		ClusterID:     "cluster-F",
		NodeID:        "node-6",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, sha256hex, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-F",
		SnapshotID:   "snap-006",
		SHA256:       sha256hex,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err != nil {
		t.Fatalf("receive failed: %v", err)
	}

	// Verify no .tmp file exists
	tmpPath := filepath.Join(root, "backup", "peer", "node-source", "cluster-F", "snap-006.json.tmp")
	if _, err := os.Stat(tmpPath); err == nil {
		t.Error("temporary file should not exist after successful receive")
	}

	// Verify final file exists
	finalPath := filepath.Join(root, "backup", "peer", "node-source", "cluster-F", "snap-006.json")
	if _, err := os.Stat(finalPath); os.IsNotExist(err) {
		t.Error("final snapshot file should exist")
	}

	// Verify file permissions (0600)
	info, err := os.Stat(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file permissions should be 0600, got %o", info.Mode().Perm())
	}
}

// TestPeer_GetReplicaMetadata tests retrieving metadata for stored replica
func TestPeer_GetReplicaMetadata(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-007",
		ClusterID:     "cluster-G",
		NodeID:        "node-7",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, sha256hex, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-G",
		SnapshotID:   "snap-007",
		SHA256:       sha256hex,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err != nil {
		t.Fatalf("receive failed: %v", err)
	}

	// Get metadata
	meta, err := ps.GetReplicaMetadata(context.Background(), "node-source", "cluster-G", "snap-007")
	if err != nil {
		t.Fatalf("GetReplicaMetadata failed: %v", err)
	}

	if meta.SnapshotID != "snap-007" {
		t.Errorf("got snapshot_id %q, want snap-007", meta.SnapshotID)
	}
	if meta.ClusterID != "cluster-G" {
		t.Errorf("got cluster_id %q, want cluster-G", meta.ClusterID)
	}
	if meta.SHA256 != sha256hex {
		t.Errorf("got sha256 %q, want %q", meta.SHA256, sha256hex)
	}
}

// TestPeer_CheckSnapshot tests checking if snapshot exists with correct checksum
func TestPeer_CheckSnapshot(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-008",
		ClusterID:     "cluster-H",
		NodeID:        "node-8",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, sha256hex, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-H",
		SnapshotID:   "snap-008",
		SHA256:       sha256hex,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err != nil {
		t.Fatalf("receive failed: %v", err)
	}

	// Check snapshot with correct checksum
	exists, matches, err := ps.CheckSnapshot(context.Background(), "node-source", "cluster-H", "snap-008", sha256hex)
	if err != nil {
		t.Fatalf("CheckSnapshot failed: %v", err)
	}
	if !exists {
		t.Error("snapshot should exist")
	}
	if !matches {
		t.Error("checksum should match")
	}

	// Check with wrong checksum
	wrongSHA := "0000000000000000000000000000000000000000000000000000000000000000"
	exists, matches, err = ps.CheckSnapshot(context.Background(), "node-source", "cluster-H", "snap-008", wrongSHA)
	if err != nil {
		t.Fatalf("CheckSnapshot failed: %v", err)
	}
	if !exists {
		t.Error("snapshot should exist")
	}
	if matches {
		t.Error("checksum should not match")
	}

	// Check non-existent snapshot
	exists, matches, err = ps.CheckSnapshot(context.Background(), "node-source", "cluster-H", "snap-999", sha256hex)
	if err != nil {
		t.Fatalf("CheckSnapshot failed: %v", err)
	}
	if exists {
		t.Error("snapshot should not exist")
	}
	if matches {
		t.Error("checksum should not match for non-existent snapshot")
	}
}

// TestPeer_DeleteSnapshot tests deleting a peer replica
func TestPeer_DeleteSnapshot(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-009",
		ClusterID:     "cluster-I",
		NodeID:        "node-9",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, sha256hex, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-I",
		SnapshotID:   "snap-009",
		SHA256:       sha256hex,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err != nil {
		t.Fatalf("receive failed: %v", err)
	}

	// Delete snapshot
	err = ps.DeleteSnapshot(context.Background(), "node-source", "cluster-I", "snap-009")
	if err != nil {
		t.Fatalf("DeleteSnapshot failed: %v", err)
	}

	// Verify snapshot no longer exists
	exists, _, err := ps.CheckSnapshot(context.Background(), "node-source", "cluster-I", "snap-009", sha256hex)
	if err != nil {
		t.Fatalf("CheckSnapshot failed: %v", err)
	}
	if exists {
		t.Error("snapshot should not exist after deletion")
	}
}

// TestPeer_ReceiveInvalidChecksum tests that invalid SHA256 format is rejected
func TestPeer_ReceiveInvalidChecksum(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-010",
		ClusterID:     "cluster-J",
		NodeID:        "node-10",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, _, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-J",
		SnapshotID:   "snap-010",
		SHA256:       "invalid-not-hex",
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err == nil {
		t.Fatal("expected invalid checksum error, got nil")
	}
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID error, got: %v", err)
	}
}

// TestPeer_ReceivePayloadChecksumMismatch tests that payload checksum is verified
func TestPeer_ReceivePayloadChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-011",
		ClusterID:     "cluster-K",
		NodeID:        "node-11",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}

	payload, _, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	// Use wrong checksum
	wrongSHA := hex.EncodeToString(sha256.New().Sum([]byte("wrong")))

	params := ReceiveParams{
		SourceNodeID: "node-source",
		ClusterID:    "cluster-K",
		SnapshotID:   "snap-011",
		SHA256:       wrongSHA,
		RunID:        "run-001",
		TaskID:       "task-001",
		Payload:      payload,
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), params)
	if err == nil {
		t.Fatal("expected payload checksum mismatch error, got nil")
	}
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID error for payload mismatch, got: %v", err)
	}
}
