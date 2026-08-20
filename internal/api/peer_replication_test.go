package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
	"github.com/stretchr/testify/require"
)

func newPeerReplicationClient(t *testing.T, api *PeerReplicationAPI, tlsState *tls.ConnectionState) procmeshv1connect.PeerReplicationServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	h, handlers := procmeshv1connect.NewPeerReplicationServiceHandler(api)
	mux.Handle(h, handlers)

	// Wrap handler to inject TLS state into context
	wrappedMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tlsState != nil {
			ctx := rpc.WithTLSState(r.Context(), *tlsState)
			r = r.WithContext(ctx)
		}
		mux.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(wrappedMux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewPeerReplicationServiceClient(srv.Client(), srv.URL)
}

func computeSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// CRITICAL #2: Test that PeerReplicationService requires mTLS
func TestPeerReplicationAPI_RequiresAgentMTLS(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	// No TLS state - should fail
	client := newPeerReplicationClient(t, api, nil)

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1", Name: "proc1"}},
	}
	payload, err := json.Marshal(snap)
	require.NoError(t, err)

	_, err = client.PutSnapshot(context.Background(), connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     computeSHA256(payload),
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload,
	}))
	if err == nil {
		t.Fatal("expected error without mTLS")
	}
}

func TestPeerReplicationAPI_RejectsRequestClusterAndSourceMismatch(t *testing.T) {
	creds := genAgentCreds(t, "cluster-1", "node-2")
	client := newPeerReplicationClient(t, &PeerReplicationAPI{PeerStore: &backup.PeerStore{Root: t.TempDir()}, ClusterID: "cluster-1", NodeID: "node-1"}, &tls.ConnectionState{PeerCertificates: []*x509.Certificate{creds.Cert}})
	_, err := client.CheckSnapshot(context.Background(), connect.NewRequest(&procmeshv1.CheckSnapshotRequest{SourceNodeId: "node-2", ClusterId: "cluster-other", SnapshotId: "snap", Sha256: strings.Repeat("a", 64)}))
	if err == nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("request cluster mismatch should deny, got %v", err)
	}
	_, err = client.GetReplicaMetadata(context.Background(), connect.NewRequest(&procmeshv1.GetReplicaMetadataRequest{SourceNodeId: "node-3", ClusterId: "cluster-1", SnapshotId: "snap"}))
	if err == nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("source identity mismatch should deny, got %v", err)
	}
}

// CRITICAL #1: Test that SourceNodeID is extracted from mTLS certificate
func TestPeerReplicationAPI_PutSnapshot(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	creds := genAgentCreds(t, "cluster-1", "node-2")

	var authorized PeerOperation
	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
		AuthorizeOperation: func(_ string, operation PeerOperation) error {
			authorized = operation
			return nil
		},
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newPeerReplicationClient(t, api, tlsState)

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

	resp, err := client.PutSnapshot(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "snap-1", resp.Msg.SnapshotId)
	require.Equal(t, "cluster-1", resp.Msg.ClusterId)
	require.Equal(t, int32(2), resp.Msg.ProcessCount)
	require.Equal(t, "PUT", authorized.Kind)
	require.Equal(t, "node-2", authorized.SourceNodeID)
	require.Equal(t, "node-1", authorized.TargetNodeID)
	require.Equal(t, "run-1", authorized.RunID)
	require.Equal(t, "task-1", authorized.TaskID)

	// CRITICAL #1: Verify file is stored under peer node ID from mTLS cert (node-2), not API's NodeID (node-1)
	expectedPath := filepath.Join(dir, "backup", "peer", "node-2", "cluster-1", "snap-1.json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatalf("expected file at %s", expectedPath)
	}

	// Verify wrong path does NOT exist
	wrongPath := filepath.Join(dir, "backup", "peer", "node-1", "cluster-1", "snap-1.json")
	if _, err := os.Stat(wrongPath); !os.IsNotExist(err) {
		t.Fatalf("file should not exist at %s", wrongPath)
	}
}

func TestPeerReplicationAPI_PutSnapshotPolicyAuthorizationBeforeStore(t *testing.T) {
	dir := t.TempDir()
	creds := genAgentCreds(t, "cluster-1", "source")
	api := &PeerReplicationAPI{
		PeerStore: &backup.PeerStore{Root: dir}, ClusterID: "cluster-1", NodeID: "target",
		AuthorizeOperation: func(_ string, operation PeerOperation) error {
			if operation.PolicyID != "frozen-policy" || operation.PolicyRevision != 4 {
				return errcode.E(errcode.CONFLICT, "replication run changed")
			}
			return nil
		},
	}
	client := newPeerReplicationClient(t, api, &tls.ConnectionState{PeerCertificates: []*x509.Certificate{creds.Cert}})
	payload, _, err := backup.Encode(backup.Snapshot{FormatVersion: 1, SnapshotID: "snapshot", ClusterID: "cluster-1", NodeID: "source"})
	require.NoError(t, err)
	path := filepath.Join(dir, "backup", "peer", "source", "cluster-1", "snapshot.json")

	for _, request := range []*procmeshv1.PutSnapshotRequest{
		{ClusterId: "cluster-1", SnapshotId: "snapshot", Sha256: computeSHA256(payload), RunId: "run", TaskId: "task", PolicyId: "changed-policy", PolicyRevision: 4, Payload: payload},
		{ClusterId: "cluster-1", SnapshotId: "snapshot", Sha256: computeSHA256(payload), RunId: "run", TaskId: "task", PolicyId: "frozen-policy", PolicyRevision: 5, Payload: payload},
	} {
		_, err := client.PutSnapshot(context.Background(), connect.NewRequest(request))
		require.Error(t, err)
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		_, statErr := os.Stat(path)
		require.True(t, os.IsNotExist(statErr), "policy authorization must run before peer store")
	}

	_, err = client.PutSnapshot(context.Background(), connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId: "cluster-1", SnapshotId: "snapshot", Sha256: computeSHA256(payload), RunId: "run", TaskId: "task",
		PolicyId: "frozen-policy", PolicyRevision: 4, Payload: payload,
	}))
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestPeerReplicationAPI_PutSnapshot_Idempotent(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	creds := genAgentCreds(t, "cluster-1", "node-2")

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newPeerReplicationClient(t, api, tlsState)

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1", Name: "proc1"}},
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

	// First call
	_, err = client.PutSnapshot(context.Background(), req)
	require.NoError(t, err)

	// Second call with same data - should succeed (idempotent)
	_, err = client.PutSnapshot(context.Background(), req)
	require.NoError(t, err)
}

func TestPeerReplicationAPI_PutSnapshot_ChecksumConflict(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	creds := genAgentCreds(t, "cluster-1", "node-2")

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newPeerReplicationClient(t, api, tlsState)

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1", Name: "proc1"}},
	}
	payload, err := json.Marshal(snap)
	require.NoError(t, err)

	// First call
	req1 := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     computeSHA256(payload),
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload,
	})
	_, err = client.PutSnapshot(context.Background(), req1)
	require.NoError(t, err)

	// Second call with different payload but same snapshot ID
	snap2 := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p2", Name: "proc2"}},
	}
	payload2, err := json.Marshal(snap2)
	require.NoError(t, err)

	req2 := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     computeSHA256(payload2),
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload2,
	})
	_, err = client.PutSnapshot(context.Background(), req2)
	require.Error(t, err)
	var ce *connect.Error
	require.True(t, errors.As(err, &ce), "Expected connect.Error")
	require.Equal(t, connect.CodeFailedPrecondition, ce.Code())

	// Check ErrorInfo detail
	require.NotEmpty(t, ce.Details())
	msg, err := ce.Details()[0].Value()
	require.NoError(t, err)
	info, ok := msg.(*procmeshv1.ErrorInfo)
	require.True(t, ok, "Expected ErrorInfo detail")
	require.Equal(t, "CONFLICT", info.Code)
}

func TestPeerReplicationAPI_PutSnapshot_ClusterIDMismatch(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	creds := genAgentCreds(t, "cluster-2", "node-2")

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newPeerReplicationClient(t, api, tlsState)

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1", Name: "proc1"}},
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

	_, err = client.PutSnapshot(context.Background(), req)
	require.Error(t, err)
	var ce *connect.Error
	require.True(t, errors.As(err, &ce), "Expected connect.Error")
	require.Equal(t, connect.CodePermissionDenied, ce.Code())

	// Check ErrorInfo detail
	require.NotEmpty(t, ce.Details())
	msg, err := ce.Details()[0].Value()
	require.NoError(t, err)
	info, ok := msg.(*procmeshv1.ErrorInfo)
	require.True(t, ok, "Expected ErrorInfo detail")
	require.Equal(t, "DENIED", info.Code)
}

func TestPeerReplicationAPI_PutSnapshot_Unavailable(t *testing.T) {
	api := &PeerReplicationAPI{
		PeerStore: nil,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	creds := genAgentCreds(t, "cluster-1", "node-2")
	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newPeerReplicationClient(t, api, tlsState)

	req := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     "abc123",
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    []byte("{}"),
	})

	_, err := client.PutSnapshot(context.Background(), req)
	require.Error(t, err)
	var ce *connect.Error
	require.True(t, errors.As(err, &ce), "Expected connect.Error")
	require.Equal(t, connect.CodeUnavailable, ce.Code())

	// Check ErrorInfo detail
	require.NotEmpty(t, ce.Details())
	msg, err := ce.Details()[0].Value()
	require.NoError(t, err)
	info, ok := msg.(*procmeshv1.ErrorInfo)
	require.True(t, ok, "Expected ErrorInfo detail")
	require.Equal(t, "UNAVAILABLE", info.Code)
}

func TestPeerReplicationAPI_CheckSnapshot(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	creds := genAgentCreds(t, "cluster-1", "node-2")

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newPeerReplicationClient(t, api, tlsState)

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1", Name: "proc1"}},
	}
	payload, err := json.Marshal(snap)
	require.NoError(t, err)
	sha := computeSHA256(payload)

	// Store snapshot first
	putReq := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     sha,
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload,
	})
	_, err = client.PutSnapshot(context.Background(), putReq)
	require.NoError(t, err)

	// Check with correct checksum
	checkReq := connect.NewRequest(&procmeshv1.CheckSnapshotRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-1",
		Sha256:       sha,
	})
	resp, err := client.CheckSnapshot(context.Background(), checkReq)
	require.NoError(t, err)
	require.True(t, resp.Msg.Exists)
	require.True(t, resp.Msg.ChecksumMatches)

	// Check with wrong checksum
	checkReq2 := connect.NewRequest(&procmeshv1.CheckSnapshotRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-1",
		Sha256:       "wrongsha",
	})
	resp2, err := client.CheckSnapshot(context.Background(), checkReq2)
	require.NoError(t, err)
	require.True(t, resp2.Msg.Exists)
	require.False(t, resp2.Msg.ChecksumMatches)

	// Check non-existent snapshot
	checkReq3 := connect.NewRequest(&procmeshv1.CheckSnapshotRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-999",
		Sha256:       sha,
	})
	resp3, err := client.CheckSnapshot(context.Background(), checkReq3)
	require.NoError(t, err)
	require.False(t, resp3.Msg.Exists)
	require.False(t, resp3.Msg.ChecksumMatches)
}

func TestPeerReplicationAPI_DeleteSnapshot(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	creds := genAgentCreds(t, "cluster-1", "node-2")

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newPeerReplicationClient(t, api, tlsState)

	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "cluster-1",
		Processes:     []backup.ProcessDump{{ProcessID: "p1", Name: "proc1"}},
	}
	payload, err := json.Marshal(snap)
	require.NoError(t, err)

	// Store snapshot first
	putReq := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     computeSHA256(payload),
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload,
	})
	_, err = client.PutSnapshot(context.Background(), putReq)
	require.NoError(t, err)

	// Delete it
	delReq := connect.NewRequest(&procmeshv1.DeleteSnapshotRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-1",
	})
	resp, err := client.DeleteSnapshot(context.Background(), delReq)
	require.NoError(t, err)
	require.True(t, resp.Msg.Deleted)

	// Delete again (idempotent)
	resp2, err := client.DeleteSnapshot(context.Background(), delReq)
	require.NoError(t, err)
	require.False(t, resp2.Msg.Deleted)
}

func TestPeerReplicationAPI_GetReplicaMetadata(t *testing.T) {
	dir := t.TempDir()
	peerStore := &backup.PeerStore{Root: dir}

	creds := genAgentCreds(t, "cluster-1", "node-2")

	api := &PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: "cluster-1",
		NodeID:    "node-1",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newPeerReplicationClient(t, api, tlsState)

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

	// Store snapshot first
	putReq := connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId:  "cluster-1",
		SnapshotId: "snap-1",
		Sha256:     computeSHA256(payload),
		RunId:      "run-1",
		TaskId:     "task-1",
		Payload:    payload,
	})
	_, err = client.PutSnapshot(context.Background(), putReq)
	require.NoError(t, err)

	// Get metadata
	metaReq := connect.NewRequest(&procmeshv1.GetReplicaMetadataRequest{
		SourceNodeId: "node-2",
		ClusterId:    "cluster-1",
		SnapshotId:   "snap-1",
	})
	resp, err := client.GetReplicaMetadata(context.Background(), metaReq)
	require.NoError(t, err)
	require.Equal(t, "snap-1", resp.Msg.SnapshotId)
	require.Equal(t, "cluster-1", resp.Msg.ClusterId)
	require.Equal(t, "node-2", resp.Msg.NodeId)
	require.Equal(t, int32(2), resp.Msg.ProcessCount)
	require.Len(t, resp.Msg.ProcessIds, 2)
}
