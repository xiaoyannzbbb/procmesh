package agent

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/version"
)

// liveSource implements cluster.SummarySource from the agent process plane.
// cluster must not import process; this adapter lives in agent.
type liveSource struct {
	mu       sync.RWMutex
	nodeID   string
	hostname string
	bootID   string
	apiAddr  string
	rpcAddr  string
	gossip   string
	store    *store.Store
	mgr      *process.Manager
	metrics  *metrics.Collector
}

func (s *liveSource) Snapshot() cluster.NodeSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clusterID := ""
	if s.store != nil {
		if id, err := s.store.GetClusterID(context.Background()); err == nil {
			clusterID = id
		}
	}

	var res cluster.ResourceSummary
	if s.metrics == nil {
		// Collector 未初始化（降级模式）
		res = cluster.ResourceSummary{
			CPUPercent:    -1,
			MemoryPercent: -1,
			DiskPercent:   -1,
		}
	} else {
		node, err := s.metrics.NodeMetrics()
		if err != nil {
			// 采集失败
			res = cluster.ResourceSummary{
				CPUPercent:    -1,
				MemoryPercent: -1,
				DiskPercent:   -1,
			}
		} else {
			res = cluster.ResourceSummary{
				CPUPercent:    int(math.Round(node.CPUPercent)),
				MemoryPercent: int(math.Round(node.MemoryPercent)),
				DiskPercent:   int(math.Round(node.DiskPercent)),
			}
		}
	}

	return cluster.NodeSummary{
		NodeID:            s.nodeID,
		ClusterID:         clusterID,
		Hostname:          s.hostname,
		BootID:            s.bootID,
		State:             cluster.StateAlive,
		AgentVersion:      version.Agent,
		ProtocolVersion:   version.Protocol,
		APIAddress:        s.apiAddr,
		RPCAddress:        s.rpcAddr,
		GossipAddress:     s.gossip,
		Processes:         processSummaries(s.mgr),
		Resources:         res,
		LastUpdatedUnixMs: time.Now().UnixMilli(),
	}
}

func (s *liveSource) setAPI(addr string) {
	s.mu.Lock()
	s.apiAddr = addr
	s.mu.Unlock()
}

func (s *liveSource) setGossip(addr string) {
	s.mu.Lock()
	s.gossip = addr
	s.mu.Unlock()
}

func (s *liveSource) setRPC(addr string) {
	s.mu.Lock()
	s.rpcAddr = addr
	s.mu.Unlock()
}

func processSummaries(mgr *process.Manager) []cluster.ProcessSummary {
	if mgr == nil {
		return nil
	}
	ctx := context.Background()
	specs, err := mgr.ListSpecs(ctx)
	if err != nil {
		return nil
	}
	now := time.Now().UnixMilli()
	out := make([]cluster.ProcessSummary, 0, len(specs))
	for _, spec := range specs {
		sum := cluster.ProcessSummary{
			Name:            spec.Name,
			LatestRevision:  spec.LatestRevision,
			FreshnessUnixMs: now,
		}
		insts, err := mgr.ListInstances(ctx, spec.ProcessID)
		if err == nil && len(insts) > 0 {
			inst := insts[0]
			sum.Desired = string(inst.Desired)
			sum.Observed = string(inst.Observed)
			sum.Health = string(inst.Health)
			sum.ActiveRevision = inst.ActiveRevision
		}
		out = append(out, sum)
	}
	return out
}
