package alert

import (
	"context"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/store"
)

const (
	certExpiringWithin = 30 * 24 * time.Hour
	scanDeadline       = 12 * time.Second
)

type ClusterView struct {
	ClusterID          string
	Leader             bool
	Voter              bool
	HasQuorum          bool
	LeaderAddr         string
	LeaderMissingSince time.Time // 测试可设；生产由 Scanner 记住
	Members            []cluster.NodeSummary
	CertNotAfter       map[string]time.Time // node_id → NotAfter；缺省只填本机
}

type ProcessSnap struct {
	ProcessID string
	Desired   string // RUNNING|STOPPED
	Observed  string
	Health    string
}

type NodeSample struct {
	CPUPercent, MemoryPercent, DiskPercent float64
	MemoryTotalBytes                       int64
	HaveSnapshot                           bool
}

type Scanner struct {
	Engine    *Engine
	NodeID    string
	Hostname  string
	ListProcs func() []ProcessSnap
	Samples   func(ctx context.Context, series, subject, layer string, from, to int64) ([]store.MetricSample, error)
	Snapshot  func() NodeSample
	ProcCPU   func(processID string) float64 // 即时；未知 <0
	ProcMem   func(processID string) int64   // bytes；未知 <0
	Degraded  func() bool
	Now       func() time.Time

	leaderGoneAt time.Time
}

func (s *Scanner) ScanLocal(ctx context.Context) error {
	if s == nil || s.Engine == nil {
		return errcode.E(errcode.INVALID, "scanner engine")
	}
	ctx, cancel := context.WithTimeout(ctx, scanDeadline)
	defer cancel()
	now := s.now()
	if err := s.scanProcesses(ctx, now); err != nil {
		return err
	}
	if err := s.scanLocalDB(ctx, now); err != nil {
		return err
	}
	if err := s.scanNodeThresholds(ctx, now); err != nil {
		return err
	}
	return nil
}

func (s *Scanner) ScanCluster(ctx context.Context, view ClusterView) error {
	if s == nil || s.Engine == nil {
		return errcode.E(errcode.INVALID, "scanner engine")
	}
	ctx, cancel := context.WithTimeout(ctx, scanDeadline)
	defer cancel()
	now := s.now()
	if err := s.scanControlQuorum(ctx, view, now); err != nil {
		return err
	}
	if view.Leader && view.HasQuorum {
		if err := s.scanClusterMembers(ctx, view, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scanner) scanControlQuorum(ctx context.Context, view ClusterView, now time.Time) error {
	if !view.Voter {
		return nil
	}
	missing := !view.HasQuorum || view.LeaderAddr == ""
	if !missing {
		s.leaderGoneAt = time.Time{}
		return s.observe(ctx, Event{
			Type: TypeControlNoQuorum, NodeID: s.NodeID, ClusterID: view.ClusterID, At: now, Firing: false,
		})
	}
	start := s.leaderGoneAt
	if start.IsZero() {
		if !view.LeaderMissingSince.IsZero() {
			start = view.LeaderMissingSince
		} else {
			start = now
		}
		s.leaderGoneAt = start
	}
	window := time.Duration(s.policy().SuspectTooLongSec) * time.Second
	if now.Sub(start) < window {
		return nil
	}
	return s.observe(ctx, Event{
		Type: TypeControlNoQuorum, NodeID: s.NodeID, ClusterID: view.ClusterID, At: now, Firing: true,
	})
}

func (s *Scanner) scanClusterMembers(ctx context.Context, view ClusterView, now time.Time) error {
	window := time.Duration(s.policy().SuspectTooLongSec) * time.Second
	for _, m := range view.Members {
		if err := s.scanMemberFailed(ctx, view.ClusterID, m, now); err != nil {
			return err
		}
		if err := s.scanMemberSuspect(ctx, view.ClusterID, m, now, window); err != nil {
			return err
		}
		if err := s.scanMemberVersion(ctx, view.ClusterID, m, now); err != nil {
			return err
		}
		if err := s.scanMemberCert(ctx, view.ClusterID, m, view.CertNotAfter, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scanner) scanMemberFailed(ctx context.Context, clusterID string, m cluster.NodeSummary, now time.Time) error {
	switch m.State {
	case cluster.StateFailed:
		return s.observe(ctx, Event{Type: TypeAgentFailed, NodeID: m.NodeID, Hostname: m.Hostname, ClusterID: clusterID, At: now, Firing: true})
	case cluster.StateAlive:
		return s.observe(ctx, Event{Type: TypeAgentFailed, NodeID: m.NodeID, Hostname: m.Hostname, ClusterID: clusterID, At: now, Firing: false})
	default:
		return nil
	}
}

func (s *Scanner) scanMemberSuspect(ctx context.Context, clusterID string, m cluster.NodeSummary, now time.Time, window time.Duration) error {
	switch m.State {
	case cluster.StateSuspect:
		last := time.UnixMilli(m.LastUpdatedUnixMs)
		if now.Sub(last) < window {
			return nil
		}
		return s.observe(ctx, Event{Type: TypeAgentSuspect, NodeID: m.NodeID, Hostname: m.Hostname, ClusterID: clusterID, At: now, Firing: true})
	case cluster.StateAlive:
		return s.observe(ctx, Event{Type: TypeAgentSuspect, NodeID: m.NodeID, Hostname: m.Hostname, ClusterID: clusterID, At: now, Firing: false})
	default:
		return nil
	}
}

func (s *Scanner) scanMemberVersion(ctx context.Context, clusterID string, m cluster.NodeSummary, now time.Time) error {
	if m.State != cluster.StateAlive {
		return nil
	}
	mismatch := m.ProtocolVersion != 0 && m.ProtocolVersion != 1
	return s.observe(ctx, Event{Type: TypeVersionMismatch, NodeID: m.NodeID, Hostname: m.Hostname, ClusterID: clusterID, At: now, Firing: mismatch})
}

func (s *Scanner) scanMemberCert(ctx context.Context, clusterID string, m cluster.NodeSummary, certs map[string]time.Time, now time.Time) error {
	notAfter, ok := certs[m.NodeID]
	if !ok || notAfter.IsZero() {
		return nil
	}
	firing := !notAfter.After(now.Add(certExpiringWithin))
	return s.observe(ctx, Event{Type: TypeCertExpiring, NodeID: m.NodeID, Hostname: m.Hostname, ClusterID: clusterID, At: now, Firing: firing})
}

func (s *Scanner) scanProcesses(ctx context.Context, now time.Time) error {
	if s.ListProcs == nil {
		return nil
	}
	pol := s.policy()
	snap := s.snapshot()
	for _, p := range s.ListProcs() {
		if err := s.scanProcessStates(ctx, p, now); err != nil {
			return err
		}
		if err := s.scanProcessThresholds(ctx, p, now, pol, snap); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scanner) scanProcessStates(ctx context.Context, p ProcessSnap, now time.Time) error {
	recovered := p.Desired == "RUNNING" && p.Observed == "RUNNING" && p.Health == "HEALTHY"
	conds := []struct {
		typ  Type
		fire bool
	}{
		{TypeProcessExit, p.Desired == "RUNNING" && p.Observed == "EXITED"},
		{TypeProcessCrashLoop, p.Desired == "RUNNING" && p.Observed == "BACKOFF"},
		{TypeProcessFatal, p.Observed == "FATAL"},
		{TypeHealthFailed, p.Desired == "RUNNING" && p.Observed == "RUNNING" && p.Health == "UNHEALTHY"},
	}
	for _, c := range conds {
		if c.fire {
			if err := s.observe(ctx, Event{
				Type: c.typ, NodeID: s.NodeID, ProcessID: p.ProcessID, At: now, Firing: true,
			}); err != nil {
				return fmt.Errorf("scan process %s %s: %w", p.ProcessID, c.typ, err)
			}
			continue
		}
		if recovered {
			if err := s.observe(ctx, Event{
				Type: c.typ, NodeID: s.NodeID, ProcessID: p.ProcessID, At: now, Firing: false,
			}); err != nil {
				return fmt.Errorf("resolve process %s %s: %w", p.ProcessID, c.typ, err)
			}
		}
	}
	return nil
}

func (s *Scanner) scanProcessThresholds(ctx context.Context, p ProcessSnap, now time.Time, pol control.AlertPolicy, snap NodeSample) error {
	cpuSnap := -1.0
	if s.ProcCPU != nil {
		cpuSnap = s.ProcCPU(p.ProcessID)
	}
	if err := s.evalThreshold(ctx, threshEval{
		typ: TypeCPUHigh, processID: p.ProcessID, series: metrics.SeriesProcCPU, subject: p.ProcessID,
		threshold: float64(pol.CPUHighPercent), need: pol.HighConsecutiveMins,
		snap: cpuSnap, haveSnap: cpuSnap >= 0, now: now,
	}); err != nil {
		return err
	}

	if snap.MemoryTotalBytes <= 0 {
		return nil
	}
	memSnap := -1.0
	haveMem := false
	if s.ProcMem != nil {
		if b := s.ProcMem(p.ProcessID); b >= 0 {
			memSnap = float64(b) / float64(snap.MemoryTotalBytes) * 100
			haveMem = true
		}
	}
	samples, err := s.loadSamples(ctx, metrics.SeriesProcMem, p.ProcessID, now, pol.HighConsecutiveMins)
	if err != nil {
		return err
	}
	for i := range samples {
		samples[i].Value = samples[i].Value / float64(snap.MemoryTotalBytes) * 100
	}
	return s.applyThreshold(ctx, TypeMemoryHigh, p.ProcessID, samples, memSnap, haveMem, float64(pol.MemoryHighPercent), pol.HighConsecutiveMins, now)
}

func (s *Scanner) scanLocalDB(ctx context.Context, now time.Time) error {
	firing := s.Degraded != nil && s.Degraded()
	if err := s.observe(ctx, Event{
		Type: TypeLocalDBError, NodeID: s.NodeID, At: now, Firing: firing,
	}); err != nil {
		return fmt.Errorf("scan local db: %w", err)
	}
	return nil
}

func (s *Scanner) scanNodeThresholds(ctx context.Context, now time.Time) error {
	pol := s.policy()
	snap := s.snapshot()
	evals := []threshEval{
		{typ: TypeCPUHigh, series: metrics.SeriesNodeCPU, subject: s.NodeID, threshold: float64(pol.CPUHighPercent), need: pol.HighConsecutiveMins, snap: snap.CPUPercent, haveSnap: snap.HaveSnapshot, now: now},
		{typ: TypeMemoryHigh, series: metrics.SeriesNodeMem, subject: s.NodeID, threshold: float64(pol.MemoryHighPercent), need: pol.HighConsecutiveMins, snap: snap.MemoryPercent, haveSnap: snap.HaveSnapshot, now: now},
		{typ: TypeDiskHigh, series: metrics.SeriesNodeDisk, subject: s.NodeID, threshold: float64(pol.DiskHighPercent), need: pol.HighConsecutiveMins, snap: snap.DiskPercent, haveSnap: snap.HaveSnapshot, now: now},
	}
	for _, ev := range evals {
		if err := s.evalThreshold(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

type threshEval struct {
	typ             Type
	processID       string
	series, subject string
	threshold       float64
	need            int
	snap            float64
	haveSnap        bool
	now             time.Time
}

func (s *Scanner) evalThreshold(ctx context.Context, ev threshEval) error {
	samples, err := s.loadSamples(ctx, ev.series, ev.subject, ev.now, ev.need)
	if err != nil {
		return err
	}
	return s.applyThreshold(ctx, ev.typ, ev.processID, samples, ev.snap, ev.haveSnap, ev.threshold, ev.need, ev.now)
}

func (s *Scanner) applyThreshold(ctx context.Context, typ Type, processID string, samples []store.MetricSample, snap float64, haveSnap bool, threshold float64, need int, now time.Time) error {
	payload := thresholdContext(samples, snap, haveSnap, threshold, need)
	switch thresholdDecision(samples, snap, haveSnap, threshold, need) {
	case threshHold:
		return nil
	case threshFire:
		if err := s.observe(ctx, Event{
			Type: typ, NodeID: s.NodeID, ProcessID: processID, Payload: payload, At: now, Firing: true,
		}); err != nil {
			return fmt.Errorf("scan %s %s: %w", typ, processID, err)
		}
	case threshResolve:
		if err := s.observe(ctx, Event{
			Type: typ, NodeID: s.NodeID, ProcessID: processID, Payload: payload, At: now, Firing: false,
		}); err != nil {
			return fmt.Errorf("scan %s %s: %w", typ, processID, err)
		}
	}
	return nil
}

func thresholdContext(samples []store.MetricSample, snap float64, haveSnap bool, threshold float64, need int) map[string]any {
	payload := map[string]any{
		"threshold_percent":   threshold,
		"consecutive_minutes": need,
	}
	if haveSnap {
		payload["current_value_percent"] = snap
		return payload
	}
	var latest store.MetricSample
	for _, sample := range samples {
		if sample.TSUnix > latest.TSUnix {
			latest = sample
		}
	}
	if latest.TSUnix != 0 {
		payload["current_value_percent"] = latest.Value
	}
	return payload
}

type threshDecision int

const (
	threshHold threshDecision = iota
	threshFire
	threshResolve
)

func thresholdDecision(samples []store.MetricSample, snap float64, haveSnap bool, threshold float64, need int) threshDecision {
	if thresholdMet(samples, snap, haveSnap, threshold, need) {
		return threshFire
	}
	if thresholdRecovered(samples, snap, haveSnap, threshold, need) {
		return threshResolve
	}
	return threshHold
}

func thresholdMet(samples []store.MetricSample, snap float64, haveSnap bool, threshold float64, need int) bool {
	if need < 1 {
		need = 1
	}
	if len(samples) == 0 {
		if !haveSnap {
			return false
		}
		return snap >= threshold && need <= 1
	}
	return consecutiveHigh(samples, threshold, need)
}

func thresholdRecovered(samples []store.MetricSample, snap float64, haveSnap bool, threshold float64, need int) bool {
	if need < 1 {
		need = 1
	}
	if haveSnap && snap < threshold {
		return true
	}
	return consecutiveCmp(samples, threshold, need, true)
}

func consecutiveHigh(samples []store.MetricSample, threshold float64, need int) bool {
	return consecutiveCmp(samples, threshold, need, false)
}

func consecutiveCmp(samples []store.MetricSample, threshold float64, need int, below bool) bool {
	count := 0
	var prev int64
	for i := len(samples) - 1; i >= 0; i-- {
		pt := samples[i]
		if below {
			if pt.Value >= threshold {
				break
			}
		} else if pt.Value < threshold {
			break
		}
		if count > 0 && pt.TSUnix != prev-60 {
			break
		}
		count++
		prev = pt.TSUnix
		if count >= need {
			return true
		}
	}
	return false
}

func (s *Scanner) loadSamples(ctx context.Context, series, subject string, now time.Time, need int) ([]store.MetricSample, error) {
	if s.Samples == nil || series == "" || subject == "" {
		return nil, nil
	}
	if need < 1 {
		need = 1
	}
	end := metrics.MinuteUnix(now)
	start := end - int64(need)*60
	samples, err := s.Samples(ctx, series, subject, metrics.LayerRawMin, start, end)
	if err != nil {
		return nil, fmt.Errorf("list metric samples %s %s: %w", series, subject, err)
	}
	return samples, nil
}

func (s *Scanner) observe(ctx context.Context, ev Event) error {
	if ev.Hostname == "" && ev.NodeID == s.NodeID {
		ev.Hostname = s.Hostname
	}
	if !ev.Firing {
		fp := Fingerprint(ev.Type, ev.NodeID, ev.ProcessID, ev.ClusterID)
		rec, err := s.Engine.Store.GetAlertByFingerprint(ctx, fp)
		if err != nil {
			if errcode.Is(err, errcode.NOT_FOUND) {
				return nil
			}
			return err
		}
		if rec.State == string(StateResolved) {
			return nil
		}
	}
	if _, err := s.Engine.Observe(ctx, ev); err != nil {
		return err
	}
	return nil
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	if s.Engine != nil && s.Engine.Now != nil {
		return s.Engine.Now()
	}
	return time.Now()
}

func (s *Scanner) snapshot() NodeSample {
	if s.Snapshot == nil {
		return NodeSample{}
	}
	return s.Snapshot()
}

func (s *Scanner) policy() control.AlertPolicy {
	if s.Engine != nil {
		return s.Engine.policy()
	}
	return control.DefaultAlertPolicy()
}
