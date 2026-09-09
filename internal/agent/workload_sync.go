package agent

import (
	"context"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type agentWorkloadFetcher struct {
	forwarder *agentForwarder
}

func (f agentWorkloadFetcher) Fetch(ctx context.Context, peer cluster.NodeSummary, known cluster.WorkloadVersion) (cluster.WorkloadSnapshot, error) {
	if f.forwarder == nil || peer.NodeID == "" || peer.RPCAddress == "" {
		return cluster.WorkloadSnapshot{}, errcode.E(errcode.UNAVAILABLE, "workload owner unavailable")
	}
	client, err := f.forwarder.WorkloadSummary(ctx, api.Route{NodeID: peer.NodeID, RPC: peer.RPCAddress})
	if err != nil {
		return cluster.WorkloadSnapshot{}, err
	}
	stream, err := client.FetchWorkloadSnapshot(ctx, connect.NewRequest(&procmeshv1.FetchWorkloadSnapshotRequest{
		KnownEpoch: known.Epoch, KnownVersion: known.Version,
	}))
	if err != nil {
		return cluster.WorkloadSnapshot{}, rpc.MapCallError(err)
	}

	var snapshot cluster.WorkloadSnapshot
	sawChunk := false
	for stream.Receive() {
		chunk := stream.Msg()
		if !sawChunk {
			snapshot.NodeID = chunk.GetNodeId()
			snapshot.Epoch = chunk.GetEpoch()
			snapshot.Version = chunk.GetVersion()
			snapshot.ObservationSeq = chunk.GetObservationSeq()
			sawChunk = true
		} else if chunk.GetNodeId() != snapshot.NodeID || chunk.GetEpoch() != snapshot.Epoch || chunk.GetVersion() != snapshot.Version || chunk.GetObservationSeq() != snapshot.ObservationSeq {
			return cluster.WorkloadSnapshot{}, errcode.E(errcode.UNAVAILABLE, "workload snapshot changed during transfer")
		}
		for _, process := range chunk.GetProcesses() {
			snapshot.Processes = append(snapshot.Processes, workloadProcessFromProto(process))
		}
	}
	if err := stream.Err(); err != nil {
		return cluster.WorkloadSnapshot{}, rpc.MapCallError(err)
	}
	if !sawChunk || snapshot.NodeID != peer.NodeID || snapshot.Epoch == "" || snapshot.Version == 0 {
		return cluster.WorkloadSnapshot{}, errcode.E(errcode.UNAVAILABLE, "invalid workload snapshot")
	}
	return snapshot, nil
}

func workloadProcessFromProto(process *procmeshv1.ProcessSummary) cluster.ProcessSummary {
	if process == nil {
		return cluster.ProcessSummary{}
	}
	return cluster.ProcessSummary{
		ProcessID: process.GetProcessId(), Name: process.GetName(), Group: process.GetGroup(),
		Desired: process.GetDesired(), Observed: process.GetObserved(), Health: process.GetHealth(),
		LatestRevision: process.GetLatestRevision(), ActiveRevision: process.GetActiveRevision(),
		FreshnessUnixMs: process.GetFreshnessUnixMs(),
	}
}

var _ cluster.WorkloadFetcher = agentWorkloadFetcher{}
