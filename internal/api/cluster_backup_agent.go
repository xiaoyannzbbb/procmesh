package api

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.ClusterBackupAgentServiceHandler = (*ClusterBackupAgentAPI)(nil)

// ClusterBackupAgentAPI handles internal Agent-to-Agent backup task RPC.
// These endpoints MUST only be accessible via mTLS Agent certificates, not user tokens.
type ClusterBackupAgentAPI struct {
	Engine    backup.AgentTaskExecutor
	Auth      *auth.Service
	ClusterID string
	NodeID    string
	Now       func() time.Time
}

// RunTask executes a cluster backup task locally on this Agent.
// Only accepts mTLS Agent certificates with matching cluster ID and node ID.
func (s *ClusterBackupAgentAPI) RunTask(ctx context.Context, req *connect.Request[procmeshv1.RunClusterBackupTaskRequest]) (*connect.Response[procmeshv1.RunClusterBackupTaskResponse], error) {
	if err := s.requireAgentMTLS(ctx, req.Header()); err != nil {
		return nil, err
	}

	msg := req.Msg
	if msg.GetNodeId() != s.NodeID {
		return nil, ToConnect(errcode.E(errcode.DENIED, "node_id mismatch"))
	}

	if s.Engine == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "backup engine not initialized"))
	}

	result, err := s.Engine.RunClusterTask(ctx, backup.ClusterTaskRequest{
		RunID:              msg.GetRunId(),
		TaskID:             msg.GetTaskId(),
		NodeID:             msg.GetNodeId(),
		PolicyRevision:     msg.GetPolicyRevision(),
		Sink:               msg.GetSink(),
		DestinationProfile: msg.GetDestinationProfile(),
	})
	if err != nil {
		return nil, ToConnect(err)
	}

	return connect.NewResponse(&procmeshv1.RunClusterBackupTaskResponse{
		Task: toProtoTask(result),
	}), nil
}

// GetTask retrieves the status of a cluster backup task.
func (s *ClusterBackupAgentAPI) GetTask(ctx context.Context, req *connect.Request[procmeshv1.GetClusterBackupTaskRequest]) (*connect.Response[procmeshv1.GetClusterBackupTaskResponse], error) {
	if err := s.requireAgentMTLS(ctx, req.Header()); err != nil {
		return nil, err
	}

	if s.Engine == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "backup engine not initialized"))
	}

	msg := req.Msg
	result, err := s.Engine.GetClusterTask(ctx, msg.GetRunId(), msg.GetTaskId())
	if err != nil {
		return nil, ToConnect(err)
	}

	return connect.NewResponse(&procmeshv1.GetClusterBackupTaskResponse{
		Task: toProtoTask(result),
	}), nil
}

// requireAgentMTLS verifies the request comes from a valid Agent certificate
// with matching cluster ID. User tokens are rejected.
func (s *ClusterBackupAgentAPI) requireAgentMTLS(ctx context.Context, header http.Header) error {
	// Reject user authentication attempts
	if rpc.SessionIDOf(header) != "" || rpc.TokenIDOf(header) != "" {
		return ToConnect(errcode.E(errcode.DENIED, "user credentials not allowed for internal agent RPC"))
	}

	// Extract TLS connection state
	tlsState, err := rpc.TLSStateFromContext(ctx)
	if err != nil {
		return ToConnect(errcode.E(errcode.DENIED, "mTLS required"))
	}

	// Verify cluster ID from peer certificate
	clusterID, _, err := rpc.PeerIdentity(tlsState)
	if err != nil {
		return ToConnect(err)
	}

	if clusterID != s.ClusterID {
		return ToConnect(errcode.E(errcode.DENIED, "cluster_id mismatch"))
	}

	return nil
}

func toProtoTask(result *backup.TaskResult) *procmeshv1.ClusterBackupTask {
	if result == nil {
		return nil
	}
	return &procmeshv1.ClusterBackupTask{
		RunId:        result.RunID,
		TaskId:       result.TaskID,
		NodeId:       result.NodeID,
		SnapshotId:   result.SnapshotID,
		Sha256:       result.SHA256,
		Status:       result.Status,
		Bytes:        result.Bytes,
		ErrorCode:    result.ErrorCode,
		ErrorSummary: result.ErrorSummary,
		LeaderTerm:   result.LeaderTerm,
		UpdatedUnix:  result.UpdatedUnix,
	}
}
