package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/stretchr/testify/require"
)

func computeSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func TestPeerReplicationAPI_PutSnapshot(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes: []backup.ProcessDump{
			{ProcessID: "p1", Name: "proc1"},
			{ProcessID: "p2", Name: "proc2"},
		},
	}
	payload, err := json.Marshal(snap)
	require.NoError(t, err)

	req := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     computeSHA256(payload),
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload,
	})

	resp, err := api.PutSnapshot(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "snap-1", resp.Msg.SnapshotId)
	require.Equal(t, "cluster-1", resp.Msg.ClusterId)
	require.Equal(t, int32(2), resp.Msg.ProcessCount)
	require.ElementsMatch(t, []string{"p1", "p2"}, resp.Msg.ProcessIds)
}

func TestPeerReplicationAPI_PutSnapshotIdempotent(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1"}},
	}
	payload, _ := json.Marshal(snap)

	req := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     computeSHA256(payload),
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload,
	})

	// First call
	resp1, err := api.PutSnapshot(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "snap-1", resp1.Msg.SnapshotId)

	// Second call with same checksum - idempotent
	resp2, err := api.PutSnapshot(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "snap-1", resp2.Msg.SnapshotId)
}

func TestPeerReplicationAPI_PutSnapshotConflict(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1"}},
	}
	payload, _ := json.Marshal(snap)

	// First write
	req1 := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     computeSHA256(payload),
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload,
	})
	_, err := api.PutSnapshot(context.Background(), req1)
	require.NoError(t, err)

	// Second write with different checksum (but same payload - will fail)
	req2 := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     "b665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae4",
		RunId:      "run-2",
		TaskId:     "task-2",
		Payload:    payload,
	})
	_, err = api.PutSnapshot(context.Background(), req2)
	require.Error(t, err)
	require.True(t, errcode.Is(err, errcode.INVALID))
}

func TestPeerReplicationAPI_CheckSnapshot(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	// Write snapshot first
	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1"}},
	}
	payload, _ := json.Marshal(snap)

	// Create snapshot with known checksum
	params := backup.ReceiveParams{
		SourceNodeID: "node-2",
		ClusterID:    "cluster-1",
		SnapshotID:   "snap-1",
		SHA256:       computeSHA256(payload),
		RunID:        "run-1",
		TaskID:       "task-1",
		Payload:      payload,
	}
	_, err := peerStore.ReceiveWithMetadata(context.Background(), params)
	require.NoError(t, err)

	// Check with correct checksum
	req := connect.NewRequest(&procmeshv1.CheckSnapshotRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-1",
		Sha256:       computeSHA256(payload),
	})
	resp, err := api.CheckSnapshot(context.Background(), req)
	require.NoError(t, err)
	require.True(t, resp.Msg.Exists)
	require.True(t, resp.Msg.ChecksumMatches)

	// Check with wrong checksum
	req2 := connect.NewRequest(&procmeshv1.CheckSnapshotRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-1",
		Sha256:       "b665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae4",
	})
	resp2, err := api.CheckSnapshot(context.Background(), req2)
	require.NoError(t, err)
	require.True(t, resp2.Msg.Exists)
	require.False(t, resp2.Msg.ChecksumMatches)

	// Check non-existent
	req3 := connect.NewRequest(&procmeshv1.CheckSnapshotRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "missing",
		Sha256:       "c665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae5",
	})
	resp3, err := api.CheckSnapshot(context.Background(), req3)
	require.NoError(t, err)
	require.False(t, resp3.Msg.Exists)
	require.False(t, resp3.Msg.ChecksumMatches)
}

func TestPeerReplicationAPI_DeleteSnapshot(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	// Write snapshot first
	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1"}},
	}
	payload, _ := json.Marshal(snap)
	params := backup.ReceiveParams{
		SourceNodeID: "node-2",
		ClusterID:    "cluster-1",
		SnapshotID:   "snap-1",
		SHA256:       computeSHA256(payload),
		RunID:        "run-1",
		TaskID:       "task-1",
		Payload:      payload,
	}
	_, err := peerStore.ReceiveWithMetadata(context.Background(), params)
	require.NoError(t, err)

	// Delete
	req := connect.NewRequest(&procmeshv1.DeleteSnapshotRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-1",
	})
	resp, err := api.DeleteSnapshot(context.Background(), req)
	require.NoError(t, err)
	require.True(t, resp.Msg.Deleted)

	// Verify deleted
	path := filepath.Join(dir, "backup", "peer", "node-2", "cluster-1", "snap-1.json")
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))

	// Delete again - idempotent
	resp2, err := api.DeleteSnapshot(context.Background(), req)
	require.NoError(t, err)
	require.False(t, resp2.Msg.Deleted)
}

func TestPeerReplicationAPI_GetReplicaMetadata(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	// Write snapshot
	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes: []backup.ProcessDump{
			{ProcessID: "p1", Name: "proc1"},
			{ProcessID: "p2", Name: "proc2"},
		},
	}
	payload, _ := json.Marshal(snap)
	params := backup.ReceiveParams{
		SourceNodeID: "node-2",
		ClusterID:    "cluster-1",
		SnapshotID:   "snap-1",
		SHA256:       computeSHA256(payload),
		RunID:        "run-1",
		TaskID:       "task-1",
		Payload:      payload,
	}
	_, err := peerStore.ReceiveWithMetadata(context.Background(), params)
	require.NoError(t, err)

	// Get metadata
	req := connect.NewRequest(&procmeshv1.GetReplicaMetadataRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-1",
	})
	resp, err := api.GetReplicaMetadata(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "snap-1", resp.Msg.SnapshotId)
	require.Equal(t, "cluster-1", resp.Msg.ClusterId)
	require.Equal(t, "node-2", resp.Msg.NodeId)
	require.Equal(t, computeSHA256(payload), resp.Msg.Sha256)
	require.Equal(t, int32(2), resp.Msg.ProcessCount)
	require.ElementsMatch(t, []string{"p1", "p2"}, resp.Msg.ProcessIds)
}

func TestPeerReplicationAPI_UnavailableStore(t *testing.T) {
	api := &PeerReplicationAPI{
		PeerStore: nil,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	// PutSnapshot
	req1 := connect.NewRequest(&procmeshv1.PutSnapshotRequest{})
	_, err := api.PutSnapshot(context.Background(), req1)
	require.Error(t, err)
	require.True(t, errcode.Is(err, errcode.UNAVAILABLE))

	// CheckSnapshot
	req2 := connect.NewRequest(&procmeshv1.CheckSnapshotRequest{})
	_, err = api.CheckSnapshot(context.Background(), req2)
	require.Error(t, err)
	require.True(t, errcode.Is(err, errcode.UNAVAILABLE))

	// DeleteSnapshot
	req3 := connect.NewRequest(&procmeshv1.DeleteSnapshotRequest{})
	_, err = api.DeleteSnapshot(context.Background(), req3)
	require.Error(t, err)
	require.True(t, errcode.Is(err, errcode.UNAVAILABLE))

	// GetReplicaMetadata
	req4 := connect.NewRequest(&procmeshv1.GetReplicaMetadataRequest{})
	_, err = api.GetReplicaMetadata(context.Background(), req4)
	require.Error(t, err)
	require.True(t, errcode.Is(err, errcode.UNAVAILABLE))
}
