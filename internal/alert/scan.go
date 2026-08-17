package alert

import (
	"context"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/store"
)

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
	ListProcs func() []ProcessSnap
	Samples   func(ctx context.Context, series, subject, layer string, from, to int64) ([]store.MetricSample, error)
	Snapshot  func() NodeSample
	ProcCPU   func(processID string) float64 // 即时；未知 <0
	ProcMem   func(processID string) int64   // bytes；未知 <0
	Degraded  func() bool
	Now       func() time.Time
}

func (s *Scanner) ScanLocal(ctx context.Context) error {
	if s == nil || s.Engine == nil {
		return errcode.E(errcode.INVALID, "scanner engine")
	}
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
	firing := thresholdMet(samples, snap, haveSnap, threshold, need)
	if err := s.observe(ctx, Event{
		Type: typ, NodeID: s.NodeID, ProcessID: processID, At: now, Firing: firing,
	}); err != nil {
		return fmt.Errorf("scan %s %s: %w", typ, processID, err)
	}
	return nil
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

func consecutiveHigh(samples []store.MetricSample, threshold float64, need int) bool {
	count := 0
	var prev int64
	for i := len(samples) - 1; i >= 0; i-- {
		pt := samples[i]
		if pt.Value < threshold {
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
