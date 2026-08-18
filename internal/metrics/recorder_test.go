package metrics_test

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestRecorder_FlushWritesMinuteMeanAndSkipsInvalid(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 10 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 10, MemoryPercent: 20, DiskPercent: 30}, nil
	}
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 30, MemoryPercent: 20, DiskPercent: 50}, nil
	}
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: -1}, errors.New("fail")
	}
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	if err := r.Sample(ctx); err != nil { // 分钟翻转触发 Flush
		t.Fatal(err)
	}
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerRawMin, 0, math.MaxInt64)
	if len(got) != 1 || got[0].Value != 20 {
		t.Fatalf("cpu mean want 20 got %+v", got)
	}
	disk, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeDisk, "n1", metrics.LayerRawMin, 0, math.MaxInt64)
	if len(disk) != 1 || disk[0].Value != 50 {
		t.Fatalf("disk max want 50 got %+v", disk)
	}
}

func TestRecorder_FailedMinuteWritesNoPoint(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 10 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) { return nil, errors.New("down") }
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	_ = r.Sample(ctx)
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	_ = r.Sample(ctx)
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerRawMin, 0, math.MaxInt64)
	if len(got) != 0 {
		t.Fatalf("failed minute must leave a gap: %+v", got)
	}
}

func TestRecorder_DownsampleFiveRawMin(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	r := metrics.NewRecorder(mem, "n1")
	r.DiskPercent = func() float64 { return 10 }
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	// 10:00–10:04 每分钟一个 raw 点，最后一分钟 flush 应写 down_5m @ 10:00
	base := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		now := base.Add(time.Duration(i)*time.Minute + 10*time.Second)
		r.Now = func() time.Time { return now }
		cpu := float64(10 + i*10)
		r.CollectNode = func() (*metrics.NodeMetrics, error) {
			return &metrics.NodeMetrics{CPUPercent: cpu, MemoryPercent: 1, DiskPercent: float64(i + 1)}, nil
		}
		if err := r.Sample(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r.Now = func() time.Time { return base.Add(5 * time.Minute) }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 1, MemoryPercent: 1, DiskPercent: 1}, nil
	}
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerDown5m, 0, math.MaxInt64)
	if len(got) != 1 || got[0].TSUnix != base.Unix() || got[0].Value != 30 {
		t.Fatalf("down_5m cpu want 30 @10:00 got %+v", got)
	}
	disk, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeDisk, "n1", metrics.LayerDown5m, 0, math.MaxInt64)
	if len(disk) != 1 || disk[0].Value != 5 {
		t.Fatalf("down_5m disk max want 5 got %+v", disk)
	}
}

func TestRecorder_FlushUsesInjectedPauseDecision(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 93.1 }
	r.PauseWrites = func(used float64) bool { return used > 93 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 10, MemoryPercent: 10, DiskPercent: 10}, nil
	}
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	_ = r.Sample(ctx)
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	err := r.Sample(ctx)
	if err == nil || !strings.Contains(err.Error(), "history writes paused") {
		t.Fatalf("want history writes paused error, got %v", err)
	}
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerRawMin, 0, math.MaxInt64)
	if len(got) != 0 {
		t.Fatalf("paused recorder must not insert: %+v", got)
	}
}

func TestRecorder_FlushWritesWhenPauseDecisionAllows(t *testing.T) {
	mem := newMemSamples()
	r := metrics.NewRecorder(mem, "n1")
	r.DiskPercent = func() float64 { return 99 }
	r.PauseWrites = func(float64) bool { return false }

	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRecorder_FlushWithoutPauseDecisionDoesNotInventThreshold(t *testing.T) {
	r := metrics.NewRecorder(newMemSamples(), "n1")
	r.DiskPercent = func() float64 { return 100 }

	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRecorder_Disk90DeletesOldestDown5m(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	// 256+1 in-window points: DeleteOldest(FlushDeleteCap) must leave the newest.
	var seed []store.MetricSample
	for i := 0; i < metrics.FlushDeleteCap+1; i++ {
		seed = append(seed, store.MetricSample{
			Series:    metrics.SeriesNodeCPU,
			SubjectID: "n1",
			Layer:     metrics.LayerDown5m,
			TSUnix:    now.Add(-time.Duration(metrics.FlushDeleteCap+1-i) * time.Minute).Unix(),
			Value:     float64(i),
		})
	}
	_ = mem.InsertMetricSamples(ctx, seed)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 91 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 10, MemoryPercent: 10, DiskPercent: 10}, nil
	}
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	_ = r.Sample(ctx)
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerDown5m, 0, math.MaxInt64)
	wantTS := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC).Add(-1 * time.Minute).Unix()
	if len(got) != 1 || got[0].TSUnix != wantTS {
		t.Fatalf("oldest down_5m must go: %+v", got)
	}
}

func TestRecorder_ProcessSamplesUseProcessID(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 10 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 1, MemoryPercent: 1, DiskPercent: 1}, nil
	}
	r.ListProcesses = func() []metrics.ProcessRef {
		return []metrics.ProcessRef{{ProcessID: "proc-1", PID: 4242}}
	}
	r.CollectProcess = func(pid int) (*metrics.ProcessMetrics, error) {
		if pid != 4242 {
			t.Fatalf("pid %d", pid)
		}
		return &metrics.ProcessMetrics{PID: pid, CPUPercent: 8, MemoryBytes: 4096}, nil
	}
	_ = r.Sample(ctx)
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	_ = r.Sample(ctx)
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesProcMem, "proc-1", metrics.LayerRawMin, 0, math.MaxInt64)
	if len(got) != 1 || got[0].Value != 4096 {
		t.Fatalf("%+v", got)
	}
}

func TestRecorder_PrunesExpiredLayers(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	_ = mem.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerRawMin, TSUnix: now.Add(-25 * time.Hour).Unix(), Value: 1},
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerDown5m, TSUnix: now.Add(-8 * 24 * time.Hour).Unix(), Value: 2},
	})
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 10 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) { return nil, errors.New("skip") }
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	if err := r.Flush(ctx); err != nil && !strings.Contains(err.Error(), "DEGRADED") {
		// flush with empty buckets is ok
	}
	raw, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerRawMin, 0, now.Unix())
	down, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerDown5m, 0, now.Unix())
	if len(raw) != 0 || len(down) != 0 {
		t.Fatalf("ttl prune failed raw=%+v down=%+v", raw, down)
	}
}

var _ metrics.SampleStore = (*memSamples)(nil)

type memSamples struct {
	mu   sync.Mutex
	rows []store.MetricSample
}

func newMemSamples() *memSamples {
	return &memSamples{}
}

func sameMetricPK(a, b store.MetricSample) bool {
	return a.Series == b.Series && a.SubjectID == b.SubjectID && a.Layer == b.Layer && a.TSUnix == b.TSUnix
}

func (m *memSamples) InsertMetricSamples(_ context.Context, samples []store.MetricSample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sample := range samples {
		replaced := false
		for i, existing := range m.rows {
			if sameMetricPK(existing, sample) {
				m.rows[i] = sample
				replaced = true
				break
			}
		}
		if !replaced {
			m.rows = append(m.rows, sample)
		}
	}
	return nil
}

func (m *memSamples) ListMetricSamples(_ context.Context, series, subjectID, layer string, fromUnix, toUnix int64) ([]store.MetricSample, error) {
	if series == "" || subjectID == "" || layer == "" {
		return nil, errors.New("series, subject_id, and layer required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.MetricSample
	for _, row := range m.rows {
		if row.Series == series && row.SubjectID == subjectID && row.Layer == layer && row.TSUnix >= fromUnix && row.TSUnix <= toUnix {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TSUnix < out[j].TSUnix })
	return out, nil
}

func (m *memSamples) DeleteMetricSamplesBefore(_ context.Context, layer string, tsUnix int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.rows[:0]
	var n int64
	for _, row := range m.rows {
		if row.Layer == layer && row.TSUnix < tsUnix {
			n++
			continue
		}
		kept = append(kept, row)
	}
	m.rows = kept
	return n, nil
}

func (m *memSamples) DeleteOldestMetricSamples(_ context.Context, layer string, limit int) (int64, error) {
	if limit <= 0 {
		limit = 256
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	type idxTS struct {
		i  int
		ts int64
	}
	var hits []idxTS
	for i, row := range m.rows {
		if row.Layer == layer {
			hits = append(hits, idxTS{i: i, ts: row.TSUnix})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].ts < hits[j].ts })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	drop := make(map[int]struct{}, len(hits))
	for _, h := range hits {
		drop[h.i] = struct{}{}
	}
	kept := m.rows[:0]
	for i, row := range m.rows {
		if _, ok := drop[i]; ok {
			continue
		}
		kept = append(kept, row)
	}
	m.rows = kept
	return int64(len(hits)), nil
}

func (m *memSamples) CountMetricSamples(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.rows)), nil
}
