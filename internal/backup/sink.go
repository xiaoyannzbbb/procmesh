package backup

import (
	"context"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// Sink stores and retrieves backup snapshot payloads.
type Sink interface {
	Name() string
	Put(ctx context.Context, id string, payload []byte) (location string, err error)
	List(ctx context.Context) ([]Listed, error)
	Get(ctx context.Context, id string) ([]byte, error)
	Delete(ctx context.Context, id string) error
}

// ClusterSink extends Sink with namespace-aware operations for cluster backups.
type ClusterSink interface {
	Sink
	PutCluster(ctx context.Context, clusterID, policyID, nodeID, id string, payload []byte) (location string, err error)
	ListCluster(ctx context.Context, clusterID, policyID string) ([]Listed, error)
	DeleteCluster(ctx context.Context, clusterID, policyID, nodeID, id string) error
}

// Listed is one snapshot entry returned by Sink.List.
type Listed struct {
	SnapshotID   string
	Location     string
	ClusterID    string
	PolicyID     string
	NodeID       string
	SourceNodeID string
	CreatedAt    time.Time
	Bytes        int64
}

func validateNamespaceID(id string) error {
	if id == "." || id == ".." || !snapshotIDRe.MatchString(id) {
		return errcode.E(errcode.INVALID, "invalid backup namespace id")
	}
	return nil
}
