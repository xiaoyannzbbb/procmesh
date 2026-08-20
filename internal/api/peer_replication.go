package api

import (
	"context"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

// PeerReplicationAPI implements PeerReplicationService for agent-to-agent snapshot transfer.
// Only accessible via Agent mTLS certificates, not Web sessions.
type PeerReplicationAPI struct {
	PeerStore *backup.PeerStore
	ClusterID string
	NodeID    string
}

// PutSnapshot receives a snapshot from another agent.
func (p *PeerReplicationAPI) PutSnapshot(ctx context.Context, req *connect.Request[procmeshv1.PutSnapshotRequest]) (*connect.Response[procmeshv1.PutSnapshotResponse], error) {
	if p.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}

	// Extract peer node ID from mTLS client certificate
	tlsState, err := rpc.TLSStateFromContext(ctx)
	if err != nil {
		return nil, ToConnect(err)
	}
	peerClusterID, peerNodeID, err := rpc.PeerIdentity(tlsState)
	if err != nil {
		return nil, ToConnect(err)
	}
	if peerClusterID != p.ClusterID {
		return nil, ToConnect(errcode.E(errcode.DENIED, "peer cluster mismatch"))
	}

	params := backup.ReceiveParams{
		SourceNodeID: peerNodeID, // Peer node ID from mTLS certificate
		ClusterID:    req.Msg.ClusterId,
		SnapshotID:   req.Msg.SnapshotId,
		SHA256:       req.Msg.Sha256,
		RunID:        req.Msg.RunId,
		TaskID:       req.Msg.TaskId,
		Payload:      req.Msg.Payload,
	}

	meta, err := p.PeerStore.ReceiveWithMetadata(ctx, params)
	if err != nil {
		return nil, ToConnect(err)
	}

	resp := &procmeshv1.PutSnapshotResponse{
		SnapshotId:   meta.SnapshotID,
		ClusterId:    meta.ClusterID,
		Sha256:       meta.SHA256,
		ProcessCount: int32(len(meta.ProcessIDs)),
		ProcessIds:   meta.ProcessIDs,
	}

	return connect.NewResponse(resp), nil
}

// CheckSnapshot checks if a snapshot exists with the expected checksum.
func (p *PeerReplicationAPI) CheckSnapshot(ctx context.Context, req *connect.Request[procmeshv1.CheckSnapshotRequest]) (*connect.Response[procmeshv1.CheckSnapshotResponse], error) {
	if p.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}

	exists, matches, err := p.PeerStore.CheckSnapshot(ctx, req.Msg.SourceNodeId, req.Msg.ClusterId, req.Msg.SnapshotId, req.Msg.Sha256)
	if err != nil {
		return nil, ToConnect(err)
	}

	resp := &procmeshv1.CheckSnapshotResponse{
		Exists:          exists,
		ChecksumMatches: matches,
	}

	return connect.NewResponse(resp), nil
}

// DeleteSnapshot removes a peer replica snapshot.
func (p *PeerReplicationAPI) DeleteSnapshot(ctx context.Context, req *connect.Request[procmeshv1.DeleteSnapshotRequest]) (*connect.Response[procmeshv1.DeleteSnapshotResponse], error) {
	if p.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}

	err := p.PeerStore.DeleteSnapshot(ctx, req.Msg.SourceNodeId, req.Msg.ClusterId, req.Msg.SnapshotId)
	if err != nil {
		if errcode.Is(err, errcode.NOT_FOUND) {
			// Idempotent - already deleted
			return connect.NewResponse(&procmeshv1.DeleteSnapshotResponse{Deleted: false}), nil
		}
		return nil, ToConnect(err)
	}

	return connect.NewResponse(&procmeshv1.DeleteSnapshotResponse{Deleted: true}), nil
}

// GetReplicaMetadata retrieves metadata for a stored replica.
func (p *PeerReplicationAPI) GetReplicaMetadata(ctx context.Context, req *connect.Request[procmeshv1.GetReplicaMetadataRequest]) (*connect.Response[procmeshv1.GetReplicaMetadataResponse], error) {
	if p.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}

	meta, err := p.PeerStore.GetReplicaMetadata(ctx, req.Msg.SourceNodeId, req.Msg.ClusterId, req.Msg.SnapshotId)
	if err != nil {
		return nil, ToConnect(err)
	}

	resp := &procmeshv1.GetReplicaMetadataResponse{
		SnapshotId:   meta.SnapshotID,
		ClusterId:    meta.ClusterID,
		NodeId:       meta.SourceNodeID, // Return the source node we received from
		Sha256:       meta.SHA256,
		CreatedAt:    meta.CreatedAt.Unix(),
		ProcessCount: int32(len(meta.ProcessIDs)),
		ProcessIds:   meta.ProcessIDs,
	}

	return connect.NewResponse(resp), nil
}
