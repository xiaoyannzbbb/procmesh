package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/version"
)

// liveSource implements cluster.SummarySource from the agent process plane.
// cluster must not import process; this adapter lives in agent.
type liveSource struct {
	mu         sync.RWMutex
	nodeID     string
	hostname   string
	bootID     string
	apiAddr    string
	rpcAddr    string
	gossip     string
	store      *store.Store
	mgr        *process.Manager
	metrics    nodeMetricsSource
	diskPolicy logmgr.Policy
	process    agentcfg.Process

	workloadEpoch          string
	workloadVersion        uint64
	workloadObservationSeq uint64
	workloadHash           [sha256.Size]byte
	workloadHashSet        bool
	workloadProcesses      []cluster.ProcessSummary
	workloadVerifiedUnixMs int64
	now                    func() time.Time
	readWorkload           func(context.Context) ([]cluster.ProcessSummary, error)
}

type nodeMetricsSource interface {
	NodeMetrics() (*metrics.NodeMetrics, error)
}

func (s *liveSource) Snapshot() cluster.NodeSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	s.observeWorkloadLocked(now)
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
				CPUPercent:          int(math.Round(node.CPUPercent)),
				MemoryPercent:       int(math.Round(node.MemoryPercent)),
				DiskPercent:         int(math.Round(node.DiskPercent)),
				HistoryWritesPaused: historyWritesPaused(s.diskPolicy, node.DiskPercent),
			}
		}
	}
	res.HistoryPausePercent = s.diskPolicy.EmergencyPercent

	return cluster.NodeSummary{
		NodeID:                     s.nodeID,
		ClusterID:                  clusterID,
		Hostname:                   s.hostname,
		BootID:                     s.bootID,
		State:                      cluster.StateAlive,
		AgentVersion:               version.Agent,
		ProtocolVersion:            version.Protocol,
		OS:                         runtime.GOOS,
		Arch:                       runtime.GOARCH,
		APIAddress:                 s.apiAddr,
		RPCAddress:                 s.rpcAddr,
		GossipAddress:              s.gossip,
		Processes:                  append([]cluster.ProcessSummary(nil), s.workloadProcesses...),
		Resources:                  res,
		LastUpdatedUnixMs:          now.UnixMilli(),
		WorkloadSyncVersion:        workloadSyncVersion(s.workloadEpoch, s.workloadVersion),
		WorkloadEpoch:              s.workloadEpoch,
		WorkloadVersion:            s.workloadVersion,
		WorkloadObservationSeq:     s.workloadObservationSeq,
		WorkloadLastVerifiedUnixMs: s.workloadVerifiedUnixMs,
		DisableRemoteCreate:        s.process.DisableRemoteCreate,
		DisableRemoteUpdate:        s.process.DisableRemoteUpdate,
		DisableRemoteDelete:        s.process.DisableRemoteDelete,
	}
}

func workloadSyncVersion(epoch string, version uint64) int {
	if epoch == "" || version == 0 {
		return 0
	}
	return 1
}

func (s *liveSource) observeWorkloadLocked(now time.Time) {
	if s.mgr == nil && s.readWorkload == nil {
		return
	}
	read := s.readWorkload
	if read == nil {
		read = func(ctx context.Context) ([]cluster.ProcessSummary, error) {
			return processSummaries(ctx, s.mgr)
		}
	}
	processes, err := read(context.Background())
	if err != nil {
		return
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].ProcessID != processes[j].ProcessID {
			return processes[i].ProcessID < processes[j].ProcessID
		}
		return processes[i].Name < processes[j].Name
	})
	hash := workloadContentHash(processes)
	if !s.workloadHashSet || hash != s.workloadHash {
		s.workloadVersion++
		s.workloadHash = hash
		s.workloadHashSet = true
	}
	s.workloadObservationSeq++
	observedAt := now.UnixMilli()
	s.workloadVerifiedUnixMs = observedAt
	for i := range processes {
		processes[i].FreshnessUnixMs = observedAt
	}
	s.workloadProcesses = append(s.workloadProcesses[:0], processes...)
}

func workloadContentHash(processes []cluster.ProcessSummary) [sha256.Size]byte {
	content := append([]cluster.ProcessSummary(nil), processes...)
	for i := range content {
		content[i].FreshnessUnixMs = 0
	}
	raw, _ := json.Marshal(content)
	return sha256.Sum256(raw)
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

func processSummaries(ctx context.Context, mgr *process.Manager) ([]cluster.ProcessSummary, error) {
	if mgr == nil {
		return nil, nil
	}
	specs, err := mgr.ListSpecs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cluster.ProcessSummary, 0, len(specs))
	for _, spec := range specs {
		sum := cluster.ProcessSummary{
			ProcessID:      spec.ProcessID,
			Name:           spec.Name,
			Group:          spec.Group,
			LatestRevision: spec.LatestRevision,
		}
		insts, err := mgr.ListInstances(ctx, spec.ProcessID)
		if err != nil {
			return nil, err
		}
		if len(insts) > 0 {
			inst := insts[0]
			sum.Desired = string(inst.Desired)
			sum.Observed = string(inst.Observed)
			sum.Health = string(inst.Health)
			sum.ActiveRevision = inst.ActiveRevision
		}
		out = append(out, sum)
	}
	return out, nil
}
