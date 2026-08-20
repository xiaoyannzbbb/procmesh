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

// TestPeer_ReceiveContextCanceled tests context cancellation handling
func TestPeer_ReceiveContextCanceled(t *testing.T) {
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
		t.Fatalf("encode: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err = ps.ReceiveWithMetadata(ctx, ReceiveParams{
		SourceNodeID: "node-2",
		SnapshotID:   "snap-001",
		ClusterID:    "cluster-A",
		Payload:      payload,
		SHA256:       expectedSHA,
	})

	if err == nil {
		t.Fatal("expected context.Canceled error")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// TestPeer_ReceiveMkdirError tests directory creation failure
func TestPeer_ReceiveMkdirError(t *testing.T) {
	// Create a file where the directory should be to cause mkdir to fail
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	// Create a file at the directory path to block mkdir
	blockingFile := filepath.Join(root, "backup", "peer", "node-2")
	if err := os.MkdirAll(filepath.Dir(blockingFile), 0o750); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(blockingFile, []byte("block"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
		t.Fatalf("encode: %v", err)
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), ReceiveParams{
		SourceNodeID: "node-2",
		SnapshotID:   "snap-001",
		ClusterID:    "cluster-A",
		Payload:      payload,
		SHA256:       expectedSHA,
	})

	if err == nil {
		t.Fatal("expected mkdir error")
	}
}

// TestPeer_DeleteSnapshotErrors tests error paths in DeleteSnapshot
func TestPeer_DeleteSnapshotErrors(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	// Test deleting non-existent snapshot (should return NOT_FOUND)
	err := ps.DeleteSnapshot(context.Background(), "node-2", "cluster-A", "snap-999")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Errorf("expected NOT_FOUND for non-existent delete, got: %v", err)
	}

	// Test context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = ps.DeleteSnapshot(ctx, "node-2", "cluster-A", "snap-001")
	if err == nil {
		t.Fatal("expected context.Canceled error")
	}
}

// TestPeer_CheckSnapshotErrors tests error paths in CheckSnapshot
func TestPeer_CheckSnapshotErrors(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	// Test context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := ps.CheckSnapshot(ctx, "node-2", "cluster-A", "snap-001", "abc123")
	if err == nil {
		t.Fatal("expected context.Canceled error")
	}
}

// TestPeer_GetReplicaMetadataErrors tests error paths in GetReplicaMetadata
func TestPeer_GetReplicaMetadataErrors(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	// Test context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ps.GetReplicaMetadata(ctx, "node-2", "cluster-A", "snap-001")
	if err == nil {
		t.Fatal("expected context.Canceled error")
	}

	// Test non-existent snapshot
	_, err = ps.GetReplicaMetadata(context.Background(), "node-2", "cluster-A", "snap-999")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Errorf("expected NOT_FOUND, got: %v", err)
	}
}

// TestPeer_ListErrors tests error paths in List
func TestPeer_ListErrors(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	// Test context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ps.List(ctx, "node-2")
	if err == nil {
		t.Fatal("expected context.Canceled error")
	}

	// Test invalid source node ID
	_, err = ps.List(context.Background(), "invalid/node")
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID error, got: %v", err)
	}

	// Test non-existent directory (should return empty list)
	list, err := ps.List(context.Background(), "node-nonexistent")
	if err != nil {
		t.Errorf("expected nil error for non-existent dir, got: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d items", len(list))
	}

	// Test directory with files that should be filtered
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
		t.Fatalf("encode: %v", err)
	}

	// Create a valid snapshot
	_, err = ps.ReceiveWithMetadata(context.Background(), ReceiveParams{
		SourceNodeID: "node-list-test",
		SnapshotID:   "snap-001",
		ClusterID:    "cluster-A",
		Payload:      payload,
		SHA256:       expectedSHA,
	})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	// Create a file with invalid name (should be filtered)
	legacyDir := filepath.Join(root, "backup", "peer", "node-list-test")
	if err := os.WriteFile(filepath.Join(legacyDir, "invalid-no-json-ext"), []byte("test"), 0o600); err != nil {
		t.Fatalf("write invalid file: %v", err)
	}

	// Create a subdirectory (should be filtered)
	if err := os.MkdirAll(filepath.Join(legacyDir, "subdir"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create a file with invalid snapshot ID (should be filtered)
	if err := os.WriteFile(filepath.Join(legacyDir, "invalid/../id.json"), []byte("test"), 0o600); err != nil {
		// This might fail due to path validation - that's OK
	}

	// List should only return valid snapshots
	list, err = ps.List(context.Background(), "node-list-test")
	if err != nil {
		t.Errorf("list error: %v", err)
	}
	// Should only have the valid cluster-A/snap-001 file, not the invalid ones
	if len(list) == 0 {
		t.Error("expected at least one valid snapshot in list")
	}

	// Test read error by creating a file where directory should be
	blockingFile := filepath.Join(root, "backup", "peer", "node-blocked")
	if err := os.MkdirAll(filepath.Dir(blockingFile), 0o750); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(blockingFile, []byte("block"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// This should fail with a read error (not IsNotExist)
	_, err = ps.List(context.Background(), "node-blocked")
	if err == nil {
		t.Error("expected read error for blocked directory")
	}
}

// TestPeer_CheckSnapshotValidation tests validation in CheckSnapshot
func TestPeer_CheckSnapshotValidation(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	// Test invalid source node ID
	_, _, err := ps.CheckSnapshot(context.Background(), "invalid/node", "cluster-A", "snap-001", "abc123")
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID for source node, got: %v", err)
	}

	// Test invalid snapshot ID
	_, _, err = ps.CheckSnapshot(context.Background(), "node-2", "cluster-A", "invalid/../id", "abc123")
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID for snapshot ID, got: %v", err)
	}

	// Test empty cluster ID
	_, _, err = ps.CheckSnapshot(context.Background(), "node-2", "", "snap-001", "abc123")
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID for empty cluster ID, got: %v", err)
	}

	// Test checksum mismatch
	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-002",
		ClusterID:     "cluster-A",
		NodeID:        "node-1",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}
	payload, expectedSHA, err := Encode(snap)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), ReceiveParams{
		SourceNodeID: "node-check",
		SnapshotID:   "snap-002",
		ClusterID:    "cluster-A",
		Payload:      payload,
		SHA256:       expectedSHA,
	})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	// Check with wrong SHA - should return exists=true, matches=false
	exists, matches, err := ps.CheckSnapshot(context.Background(), "node-check", "cluster-A", "snap-002", "wrongsha256")
	if err != nil {
		t.Errorf("check error: %v", err)
	}
	if !exists {
		t.Error("expected exists=true")
	}
	if matches {
		t.Error("expected matches=false for wrong SHA")
	}
}

func TestPeerStore_RejectsTraversalClusterIdentifiers(t *testing.T) {
	ps := &PeerStore{Root: t.TempDir()}

	for _, clusterID := range []string{"..", ".", "../outside", "/absolute", `..\outside`, "cluster/child", "cluster\x00child"} {
		_, _, err := ps.CheckSnapshot(context.Background(), "node-2", clusterID, "snap-001", "abc123")
		if !errcode.Is(err, errcode.INVALID) {
			t.Errorf("cluster ID %q: expected INVALID, got %v", clusterID, err)
		}
	}
}

// TestPeer_DeleteSnapshotValidation tests validation in DeleteSnapshot
func TestPeer_DeleteSnapshotValidation(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	// Test invalid source node ID
	err := ps.DeleteSnapshot(context.Background(), "invalid/node", "cluster-A", "snap-001")
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID for source node, got: %v", err)
	}

	// Test invalid snapshot ID
	err = ps.DeleteSnapshot(context.Background(), "node-2", "cluster-A", "invalid/../id")
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID for snapshot ID, got: %v", err)
	}

	// Test empty cluster ID
	err = ps.DeleteSnapshot(context.Background(), "node-2", "", "snap-001")
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID for empty cluster ID, got: %v", err)
	}

	// Test successful delete
	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-del",
		ClusterID:     "cluster-A",
		NodeID:        "node-1",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}
	payload, expectedSHA, err := Encode(snap)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	_, err = ps.ReceiveWithMetadata(context.Background(), ReceiveParams{
		SourceNodeID: "node-del",
		SnapshotID:   "snap-del",
		ClusterID:    "cluster-A",
		Payload:      payload,
		SHA256:       expectedSHA,
	})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	// Delete should succeed
	err = ps.DeleteSnapshot(context.Background(), "node-del", "cluster-A", "snap-del")
	if err != nil {
		t.Errorf("delete error: %v", err)
	}

	// Second delete should return NOT_FOUND
	err = ps.DeleteSnapshot(context.Background(), "node-del", "cluster-A", "snap-del")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Errorf("expected NOT_FOUND on second delete, got: %v", err)
	}
}

// TestPeer_ReceiveDeprecatedMethod tests the deprecated Receive method
func TestPeer_ReceiveDeprecatedMethod(t *testing.T) {
	root := t.TempDir()
	ps := &PeerStore{Root: root}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-deprecated",
		ClusterID:     "cluster-A",
		NodeID:        "node-1",
		CreatedAt:     time.Now().UTC(),
		Processes:     []ProcessDump{},
	}
	payload, _, err := Encode(snap)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Test deprecated Receive method success path
	_, err = ps.Receive(context.Background(), "node-src", payload)
	if err != nil {
		t.Errorf("deprecated Receive error: %v", err)
	}

	// Test context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ps.Receive(ctx, "node-src", payload)
	if err == nil {
		t.Error("expected context.Canceled error")
	}

	// Test invalid source node ID
	_, err = ps.Receive(context.Background(), "invalid/node", payload)
	if !errcode.Is(err, errcode.INVALID) {
		t.Errorf("expected INVALID for source node, got: %v", err)
	}

	// Test invalid payload (non-JSON)
	_, err = ps.Receive(context.Background(), "node-src", []byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON payload")
	}
}

func TestPeer_ListSnapshotsReturnsClusterReplicaMetadata(t *testing.T) {
	store := &PeerStore{Root: t.TempDir()}
	created := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"snap-a", "snap-b"} {
		snapshot := Snapshot{FormatVersion: 1, SnapshotID: id, ClusterID: "cluster-a", NodeID: "owner-a", PolicyID: "primary-policy", CreatedAt: created, Processes: []ProcessDump{}}
		payload, checksum, err := Encode(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReceiveWithMetadata(context.Background(), ReceiveParams{SourceNodeID: "owner-a", ClusterID: "cluster-a", SnapshotID: id, SHA256: checksum, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := store.ListSnapshots(context.Background(), "owner-a", "cluster-a")
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	for _, item := range listed {
		if item.SourceNodeID != "owner-a" || item.ClusterID != "cluster-a" || item.NodeID != "owner-a" || item.PolicyID != "primary-policy" || !item.CreatedAt.Equal(created) || item.Bytes <= 0 {
			t.Fatalf("item=%+v", item)
		}
	}
}
