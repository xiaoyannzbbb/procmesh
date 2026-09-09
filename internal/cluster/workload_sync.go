package cluster

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const (
	workloadSyncWorkers       = 4
	workloadSyncQueueCapacity = 256
	workloadFetchTimeout      = 5 * time.Second
	workloadFetchRetryBase    = time.Second
	workloadFetchRetryMax     = 30 * time.Second
)

type cachedWorkload struct {
	epoch          string
	version        uint64
	observationSeq uint64
	processes      []ProcessSummary
	verifiedUnixMs int64
}

type workloadFetchFailure struct {
	epoch            string
	version          uint64
	consecutive      uint32
	retryAfterUnixMs int64
}

func (f workloadFetchFailure) matches(summary NodeSummary) bool {
	return f.epoch != "" && f.epoch == summary.WorkloadEpoch && f.version == summary.WorkloadVersion
}

type workloadSyncer struct {
	mesh    *Mesh
	fetcher WorkloadFetcher
	ctx     context.Context
	cancel  context.CancelFunc
	queue   chan string

	mu       sync.Mutex
	pending  map[string]struct{}
	stopOnce sync.Once

	fetchSuccess        atomic.Uint64
	fetchError          atomic.Uint64
	fetchDiscarded      atomic.Uint64
	lastFetchDurationNs atomic.Int64
}

func newWorkloadSyncer(mesh *Mesh, fetcher WorkloadFetcher) *workloadSyncer {
	ctx, cancel := context.WithCancel(context.Background())
	s := &workloadSyncer{
		mesh: mesh, fetcher: fetcher, ctx: ctx, cancel: cancel,
		queue: make(chan string, workloadSyncQueueCapacity), pending: make(map[string]struct{}),
	}
	for range workloadSyncWorkers {
		go s.run()
	}
	return s
}

func (s *workloadSyncer) stop() {
	s.stopOnce.Do(s.cancel)
}

func (s *workloadSyncer) enqueue(nodeID string) {
	if nodeID == "" {
		return
	}
	s.mu.Lock()
	if _, ok := s.pending[nodeID]; ok {
		s.mu.Unlock()
		return
	}
	s.pending[nodeID] = struct{}{}
	s.mu.Unlock()
	select {
	case s.queue <- nodeID:
	case <-s.ctx.Done():
		s.finish(nodeID)
	default:
		s.finish(nodeID)
	}
}

func (s *workloadSyncer) run() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case nodeID := <-s.queue:
			s.fetch(nodeID)
		}
	}
}

func (s *workloadSyncer) fetch(nodeID string) {
	defer func() {
		s.finish(nodeID)
		if s.mesh.workloadFetchReady(nodeID) {
			s.enqueue(nodeID)
		}
	}()
	peer, known, target, ok := s.mesh.workloadFetchTarget(nodeID)
	if !ok {
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(s.ctx, workloadFetchTimeout)
	defer cancel()
	snapshot, err := s.fetcher.Fetch(ctx, peer, known)
	duration := time.Since(started)
	s.lastFetchDurationNs.Store(duration.Nanoseconds())
	if err != nil {
		s.fetchError.Add(1)
		failure, first := s.mesh.recordWorkloadFetchFailure(nodeID, target)
		if first && s.mesh.cfg.Logger != nil {
			s.mesh.cfg.Logger.Warn("workload snapshot fetch failed",
				"observer_node_id", s.mesh.cfg.NodeID,
				"node_id", nodeID,
				"workload_version", target.Version,
				"result", workloadFetchErrorResult(err),
				"retry_ms", workloadFetchRetryDelay(failure.consecutive).Milliseconds(),
			)
		}
		return
	}
	if !s.mesh.applyFetchedWorkload(snapshot) {
		s.fetchDiscarded.Add(1)
		s.mesh.recordWorkloadFetchFailure(nodeID, target)
		return
	}
	s.fetchSuccess.Add(1)
	if s.mesh.cfg.Logger != nil {
		s.mesh.cfg.Logger.Info("workload snapshot synchronized",
			"observer_node_id", s.mesh.cfg.NodeID,
			"node_id", nodeID,
			"workload_version", snapshot.Version,
			"process_count", len(snapshot.Processes),
			"duration_ms", duration.Milliseconds(),
		)
	}
}

func (s *workloadSyncer) finish(nodeID string) {
	s.mu.Lock()
	delete(s.pending, nodeID)
	s.mu.Unlock()
}

func (m *Mesh) applyFullWorkloadLocked(summary NodeSummary) {
	if summary.WorkloadSyncVersion < WorkloadSyncV1 || summary.WorkloadEpoch == "" || summary.WorkloadVersion == 0 {
		if summary.Processes != nil {
			m.workloads[summary.NodeID] = cachedWorkload{processes: cloneProcessSummaries(summary.Processes)}
		}
		return
	}
	if cached, ok := m.workloads[summary.NodeID]; ok &&
		cached.epoch == summary.WorkloadEpoch && cached.version == summary.WorkloadVersion &&
		summary.WorkloadObservationSeq <= cached.observationSeq {
		return
	}
	verified := m.now().UnixMilli()
	processes := cloneProcessSummaries(summary.Processes)
	setProcessFreshness(processes, verified)
	m.workloads[summary.NodeID] = cachedWorkload{
		epoch: summary.WorkloadEpoch, version: summary.WorkloadVersion,
		observationSeq: summary.WorkloadObservationSeq,
		processes:      processes, verifiedUnixMs: verified,
	}
	delete(m.workloadFailures, summary.NodeID)
}

func (m *Mesh) applyWorkloadHintLocked(hint NodeSummary) {
	if hint.WorkloadSyncVersion < WorkloadSyncV1 || hint.WorkloadEpoch == "" || hint.WorkloadVersion == 0 {
		return
	}
	cached, ok := m.workloads[hint.NodeID]
	if !ok || cached.epoch != hint.WorkloadEpoch || cached.version != hint.WorkloadVersion {
		return
	}
	if hint.WorkloadObservationSeq <= cached.observationSeq {
		return
	}
	cached.observationSeq = hint.WorkloadObservationSeq
	cached.verifiedUnixMs = m.now().UnixMilli()
	setProcessFreshness(cached.processes, cached.verifiedUnixMs)
	m.workloads[hint.NodeID] = cached
	delete(m.workloadFailures, hint.NodeID)
}

func (m *Mesh) workloadNeedsFetchLocked(nodeID string) bool {
	peer, ok := m.view[nodeID]
	if !ok || peer.State != StateAlive || peer.RPCAddress == "" || peer.WorkloadSyncVersion < WorkloadSyncV1 || peer.WorkloadEpoch == "" || peer.WorkloadVersion == 0 {
		return false
	}
	cached, ok := m.workloads[nodeID]
	return !ok || cached.epoch != peer.WorkloadEpoch || cached.version != peer.WorkloadVersion
}

func (m *Mesh) workloadFetchReadyLocked(nodeID string) bool {
	if !m.workloadNeedsFetchLocked(nodeID) {
		return false
	}
	peer := m.view[nodeID]
	failure, ok := m.workloadFailures[nodeID]
	return !ok || !failure.matches(peer) || m.now().UnixMilli() >= failure.retryAfterUnixMs
}

func (m *Mesh) workloadFetchReady(nodeID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.workloadFetchReadyLocked(nodeID)
}

func (m *Mesh) workloadFetchTarget(nodeID string) (NodeSummary, WorkloadVersion, WorkloadVersion, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	peer, ok := m.view[nodeID]
	if !ok || !m.workloadFetchReadyLocked(nodeID) {
		return NodeSummary{}, WorkloadVersion{}, WorkloadVersion{}, false
	}
	cached := m.workloads[nodeID]
	return peer,
		WorkloadVersion{Epoch: cached.epoch, Version: cached.version},
		WorkloadVersion{Epoch: peer.WorkloadEpoch, Version: peer.WorkloadVersion},
		true
}

func (m *Mesh) applyFetchedWorkload(snapshot WorkloadSnapshot) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	peer, ok := m.view[snapshot.NodeID]
	if !ok || peer.WorkloadSyncVersion < WorkloadSyncV1 || peer.WorkloadEpoch != snapshot.Epoch || peer.WorkloadVersion != snapshot.Version {
		return false
	}
	if cached, ok := m.workloads[snapshot.NodeID]; ok &&
		cached.epoch == snapshot.Epoch && cached.version == snapshot.Version &&
		snapshot.ObservationSeq <= cached.observationSeq {
		return false
	}
	verified := m.now().UnixMilli()
	processes := cloneProcessSummaries(snapshot.Processes)
	setProcessFreshness(processes, verified)
	m.workloads[snapshot.NodeID] = cachedWorkload{
		epoch: snapshot.Epoch, version: snapshot.Version,
		observationSeq: snapshot.ObservationSeq,
		processes:      processes, verifiedUnixMs: verified,
	}
	delete(m.workloadFailures, snapshot.NodeID)
	return true
}

func (m *Mesh) recordWorkloadFetchFailure(nodeID string, target WorkloadVersion) (workloadFetchFailure, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	peer, ok := m.view[nodeID]
	if !ok || peer.WorkloadEpoch != target.Epoch || peer.WorkloadVersion != target.Version || !m.workloadNeedsFetchLocked(nodeID) {
		return workloadFetchFailure{}, false
	}
	previous, existed := m.workloadFailures[nodeID]
	failure := workloadFetchFailure{epoch: target.Epoch, version: target.Version, consecutive: 1}
	if existed && previous.epoch == target.Epoch && previous.version == target.Version {
		failure.consecutive = previous.consecutive + 1
	}
	delay := workloadFetchRetryDelay(failure.consecutive)
	failure.retryAfterUnixMs = m.now().Add(delay).UnixMilli()
	m.workloadFailures[nodeID] = failure
	return failure, !existed || previous.epoch != target.Epoch || previous.version != target.Version
}

func workloadFetchRetryDelay(consecutive uint32) time.Duration {
	if consecutive == 0 {
		return 0
	}
	shift := min(consecutive-1, uint32(5))
	delay := workloadFetchRetryBase * time.Duration(1<<shift)
	return min(delay, workloadFetchRetryMax)
}

func workloadFetchErrorResult(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errcode.Is(err, errcode.TIMEOUT):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errcode.Is(err, errcode.DENIED):
		return "denied"
	case errcode.Is(err, errcode.UNAVAILABLE):
		return "unavailable"
	default:
		return "error"
	}
}

func (m *Mesh) WorkloadSyncStats() WorkloadSyncStats {
	stats := WorkloadSyncStats{}
	if syncer := m.workloadSync; syncer != nil {
		stats.FetchSuccessTotal = syncer.fetchSuccess.Load()
		stats.FetchErrorTotal = syncer.fetchError.Load()
		stats.FetchDiscardedTotal = syncer.fetchDiscarded.Load()
		stats.LastFetchDurationSeconds = float64(syncer.lastFetchDurationNs.Load()) / float64(time.Second)
		stats.QueueDepth = len(syncer.queue)
	}
	now := m.now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for nodeID, failure := range m.workloadFailures {
		if peer, ok := m.view[nodeID]; ok && failure.matches(peer) {
			stats.FailedNodes++
		}
	}
	for _, cached := range m.workloads {
		if cached.verifiedUnixMs <= 0 {
			continue
		}
		age := now.Sub(time.UnixMilli(cached.verifiedUnixMs)).Seconds()
		if age > stats.CacheMaxAgeSeconds {
			stats.CacheMaxAgeSeconds = age
		}
	}
	return stats
}

func cloneProcessSummaries(in []ProcessSummary) []ProcessSummary {
	return append([]ProcessSummary(nil), in...)
}

func setProcessFreshness(processes []ProcessSummary, unixMs int64) {
	for i := range processes {
		processes[i].FreshnessUnixMs = unixMs
	}
}
