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
	PeerStore  *backup.PeerStore
	ClusterID  string
	NodeID     string
	Replicator interface {
		ReplicateSnapshot(context.Context, backup.ReplicationTaskRequest) (int64, error)
	}
	AuthorizeReplication func(string, *procmeshv1.ReplicateSnapshotRequest) error
	AuthorizeOperation   func(string, PeerOperation) error
	CompleteDeleteIntent func(PeerOperation) error
}

type PeerOperation struct {
	Kind, ClusterID, SourceNodeID, TargetNodeID, SnapshotID, SHA256, RunID, TaskID string
	PolicyID, IntentID                                                             string
	PolicyRevision                                                                 int64
}

// PutSnapshot receives a snapshot from another agent.
func (p *PeerReplicationAPI) PutSnapshot(ctx context.Context, req *connect.Request[procmeshv1.PutSnapshotRequest]) (*connect.Response[procmeshv1.PutSnapshotResponse], error) {
	if p.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}

	peerNodeID, err := p.authorizeOperation(ctx, req.Msg.ClusterId, PeerOperation{Kind: "PUT", ClusterID: req.Msg.ClusterId, SourceNodeID: "", TargetNodeID: p.NodeID, SnapshotID: req.Msg.SnapshotId, SHA256: req.Msg.Sha256, RunID: req.Msg.RunId, TaskID: req.Msg.TaskId, PolicyID: req.Msg.PolicyId, PolicyRevision: req.Msg.PolicyRevision})
	if err != nil {
		return nil, ToConnect(err)
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

// ReplicateSnapshot authorizes the Leader to instruct this source Agent to
// reload an immutable indexed payload and push it under its own mTLS identity.
func (p *PeerReplicationAPI) ReplicateSnapshot(ctx context.Context, req *connect.Request[procmeshv1.ReplicateSnapshotRequest]) (*connect.Response[procmeshv1.ReplicateSnapshotResponse], error) {
	tlsState, err := rpc.TLSStateFromContext(ctx)
	if err != nil {
		return nil, ToConnect(errcode.E(errcode.DENIED, "mTLS required"))
	}
	clusterID, leaderNodeID, err := rpc.PeerIdentity(tlsState)
	if err != nil || clusterID != p.ClusterID {
		return nil, ToConnect(errcode.E(errcode.DENIED, "peer cluster mismatch"))
	}
	if p.AuthorizeReplication != nil {
		if err := p.AuthorizeReplication(leaderNodeID, req.Msg); err != nil {
			return nil, ToConnect(err)
		}
	}
	if p.Replicator == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "replication source unavailable"))
	}
	bytes, err := p.Replicator.ReplicateSnapshot(ctx, backup.ReplicationTaskRequest{RunID: req.Msg.RunId, TaskID: req.Msg.TaskId, PolicyID: req.Msg.PolicyId, PolicyRevision: req.Msg.PolicyRevision, SourceNodeID: req.Msg.SourceNodeId, TargetNodeID: req.Msg.TargetNodeId, SnapshotID: req.Msg.SnapshotId, SHA256: req.Msg.Sha256, LeaderTerm: req.Msg.LeaderTerm, LeaseExpiresUnix: req.Msg.LeaseExpiresUnix})
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.ReplicateSnapshotResponse{SnapshotId: req.Msg.SnapshotId, Sha256: req.Msg.Sha256, Bytes: bytes}), nil
}

// CheckSnapshot checks if a snapshot exists with the expected checksum.
func (p *PeerReplicationAPI) CheckSnapshot(ctx context.Context, req *connect.Request[procmeshv1.CheckSnapshotRequest]) (*connect.Response[procmeshv1.CheckSnapshotResponse], error) {
	if p.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}
	if _, err := p.authorizeOperation(ctx, req.Msg.ClusterId, PeerOperation{Kind: "CHECK", ClusterID: req.Msg.ClusterId, SourceNodeID: req.Msg.SourceNodeId, TargetNodeID: p.NodeID, SnapshotID: req.Msg.SnapshotId, SHA256: req.Msg.Sha256}); err != nil {
		return nil, ToConnect(err)
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
	operation := PeerOperation{
		Kind: "DELETE", ClusterID: req.Msg.ClusterId, SourceNodeID: req.Msg.SourceNodeId, TargetNodeID: p.NodeID,
		SnapshotID: req.Msg.SnapshotId, IntentID: req.Msg.IntentId, PolicyID: req.Msg.PolicyId, PolicyRevision: req.Msg.PolicyRevision,
	}
	peerNodeID, err := p.authorizeOperation(ctx, req.Msg.ClusterId, operation)
	if err != nil {
		return nil, ToConnect(err)
	}
	operation.SourceNodeID = peerNodeID

	err = p.PeerStore.DeleteSnapshot(ctx, req.Msg.SourceNodeId, req.Msg.ClusterId, req.Msg.SnapshotId)
	deleted := true
	if err != nil {
		if !errcode.Is(err, errcode.NOT_FOUND) {
			return nil, ToConnect(err)
		}
		deleted = false
	}
	if p.CompleteDeleteIntent != nil {
		if err := p.CompleteDeleteIntent(operation); err != nil {
			return nil, ToConnect(err)
		}
	}
	return connect.NewResponse(&procmeshv1.DeleteSnapshotResponse{Deleted: deleted}), nil
}

// GetReplicaMetadata retrieves metadata for a stored replica.
func (p *PeerReplicationAPI) GetReplicaMetadata(ctx context.Context, req *connect.Request[procmeshv1.GetReplicaMetadataRequest]) (*connect.Response[procmeshv1.GetReplicaMetadataResponse], error) {
	if p.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}
	if _, err := p.authorizeOperation(ctx, req.Msg.ClusterId, PeerOperation{Kind: "METADATA", ClusterID: req.Msg.ClusterId, SourceNodeID: req.Msg.SourceNodeId, TargetNodeID: p.NodeID, SnapshotID: req.Msg.SnapshotId}); err != nil {
		return nil, ToConnect(err)
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

func (p *PeerReplicationAPI) authorizeOperation(ctx context.Context, requestClusterID string, operation PeerOperation) (string, error) {
	tlsState, err := rpc.TLSStateFromContext(ctx)
	if err != nil {
		return "", errcode.E(errcode.DENIED, "mTLS required")
	}
	peerClusterID, peerNodeID, err := rpc.PeerIdentity(tlsState)
	if err != nil || peerClusterID != p.ClusterID || requestClusterID != p.ClusterID {
		return "", errcode.E(errcode.DENIED, "peer cluster mismatch")
	}
	if operation.SourceNodeID == "" {
		operation.SourceNodeID = peerNodeID
	} else if operation.SourceNodeID != peerNodeID {
		return "", errcode.E(errcode.DENIED, "peer source mismatch")
	}
	if p.AuthorizeOperation != nil {
		if err := p.AuthorizeOperation(peerNodeID, operation); err != nil {
			return "", err
		}
	}
	return peerNodeID, nil
}
