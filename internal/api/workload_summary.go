package api

import (
	"context"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

const workloadSnapshotChunkSize = 256

type WorkloadSummarySource interface {
	Snapshot() cluster.NodeSummary
}

// WorkloadSummaryAPI serves sanitized Owner-produced workload snapshots to
// peers authenticated by the Agent mTLS listener.
type WorkloadSummaryAPI struct {
	Source    WorkloadSummarySource
	ClusterID string
	NodeID    string
}

func (s *WorkloadSummaryAPI) FetchWorkloadSnapshot(ctx context.Context, _ *connect.Request[procmeshv1.FetchWorkloadSnapshotRequest], stream *connect.ServerStream[procmeshv1.WorkloadSnapshotChunk]) error {
	tlsState, err := rpc.TLSStateFromContext(ctx)
	if err != nil {
		return ToConnect(errcode.E(errcode.DENIED, "mTLS required"))
	}
	clusterID, _, err := rpc.PeerIdentity(tlsState)
	if err != nil || clusterID == "" || clusterID != s.ClusterID {
		return ToConnect(errcode.E(errcode.DENIED, "peer cluster mismatch"))
	}
	if s.Source == nil {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "workload summary unavailable"))
	}
	summary := s.Source.Snapshot()
	if summary.NodeID == "" {
		summary.NodeID = s.NodeID
	}
	if summary.NodeID == "" || (s.NodeID != "" && summary.NodeID != s.NodeID) {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "workload summary identity mismatch"))
	}

	if len(summary.Processes) == 0 {
		return stream.Send(workloadChunk(summary, nil))
	}
	for start := 0; start < len(summary.Processes); start += workloadSnapshotChunkSize {
		end := min(start+workloadSnapshotChunkSize, len(summary.Processes))
		if err := stream.Send(workloadChunk(summary, summary.Processes[start:end])); err != nil {
			return err
		}
	}
	return nil
}

func workloadChunk(summary cluster.NodeSummary, processes []cluster.ProcessSummary) *procmeshv1.WorkloadSnapshotChunk {
	out := &procmeshv1.WorkloadSnapshotChunk{
		NodeId: summary.NodeID, Epoch: summary.WorkloadEpoch,
		Version: summary.WorkloadVersion, ObservationSeq: summary.WorkloadObservationSeq,
		Processes: make([]*procmeshv1.ProcessSummary, 0, len(processes)),
	}
	for _, process := range processes {
		out.Processes = append(out.Processes, &procmeshv1.ProcessSummary{
			Name: process.Name, ProcessId: process.ProcessID, Group: process.Group,
			Desired: process.Desired, Observed: process.Observed, Health: process.Health,
			LatestRevision: process.LatestRevision, ActiveRevision: process.ActiveRevision,
			FreshnessUnixMs: process.FreshnessUnixMs,
		})
	}
	return out
}
