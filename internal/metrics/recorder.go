package metrics

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

// ProcessRef identifies a local process to sample (stable process_id, not name).
type ProcessRef struct {
	ProcessID string
	PID       int
}

// SampleStore is the persistence surface Recorder needs from store.Store.
type SampleStore interface {
	InsertMetricSamples(ctx context.Context, samples []store.MetricSample) error
	ListMetricSamples(ctx context.Context, series, subjectID, layer string, fromUnix, toUnix int64) ([]store.MetricSample, error)
	DeleteMetricSamplesBefore(ctx context.Context, layer string, tsUnix int64) (int64, error)
	DeleteOldestMetricSamples(ctx context.Context, layer string, limit int) (int64, error)
	CountMetricSamples(ctx context.Context) (int64, error)
}

type Recorder struct {
	Store          SampleStore
	NodeID         string
	CollectNode    func() (*NodeMetrics, error)
	CollectProcess func(pid int) (*ProcessMetrics, error)
	ListProcesses  func() []ProcessRef
	DiskPercent    func() float64
	Now            func() time.Time

	mu      sync.Mutex
	buckets map[string][]float64
	curMin  int64
	seen    map[string]struct{}
	cancel  context.CancelFunc
}

func NewRecorder(st SampleStore, nodeID string) *Recorder {
	return &Recorder{
		Store:   st,
		NodeID:  nodeID,
		Now:     time.Now,
		buckets: make(map[string][]float64),
		seen:    make(map[string]struct{}),
	}
}

func (r *Recorder) Sample(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sampleLocked(ctx)
}

func (r *Recorder) Flush(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushLocked(ctx)
}

func (r *Recorder) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = r.Sample(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

func (r *Recorder) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Recorder) Rows() int64 {
	if r.Store == nil {
		return 0
	}
	n, err := r.Store.CountMetricSamples(context.Background())
	if err != nil {
		return 0
	}
	return n
}

func (r *Recorder) sampleLocked(ctx context.Context) error {
	now := r.now()
	min := MinuteUnix(now)
	if r.curMin != 0 && min != r.curMin {
		if err := r.flushLocked(ctx); err != nil {
			return err
		}
	}
	r.curMin = min

	if r.CollectNode != nil {
		nm, err := r.CollectNode()
		if err == nil && nm != nil {
			r.push(SeriesNodeCPU, r.NodeID, nm.CPUPercent)
			r.push(SeriesNodeMem, r.NodeID, nm.MemoryPercent)
			r.push(SeriesNodeDisk, r.NodeID, nm.DiskPercent)
		}
	}
	if r.CollectProcess == nil || r.ListProcesses == nil {
		return nil
	}
	for _, ref := range r.ListProcesses() {
		if ref.PID <= 0 || ref.ProcessID == "" {
			continue
		}
		pm, err := r.CollectProcess(ref.PID)
		if err != nil || pm == nil {
			continue
		}
		r.push(SeriesProcCPU, ref.ProcessID, pm.CPUPercent)
		r.push(SeriesProcMem, ref.ProcessID, float64(pm.MemoryBytes))
	}
	return nil
}

func (r *Recorder) flushLocked(ctx context.Context) error {
	now := r.now()
	disk := r.disk()
	flushedMin := r.curMin

	var writeErr error
	if disk >= 95 {
		writeErr = errcode.E(errcode.DEGRADED, "disk usage at or above 95 percent; history writes paused")
	} else {
		if disk >= 90 {
			_, _ = r.Store.DeleteOldestMetricSamples(ctx, LayerDown5m, FlushDeleteCap)
		}
		var raw []store.MetricSample
		for key, vals := range r.buckets {
			series, subject := splitBucketKey(key)
			v, ok := Aggregate(KindOf(series), vals)
			if !ok {
				continue
			}
			raw = append(raw, store.MetricSample{
				Series:    series,
				SubjectID: subject,
				Layer:     LayerRawMin,
				TSUnix:    flushedMin,
				Value:     v,
			})
			r.seen[key] = struct{}{}
		}
		if len(raw) > 0 {
			if err := r.Store.InsertMetricSamples(ctx, raw); err != nil {
				writeErr = err
			}
		}
		if writeErr == nil && flushedMin != 0 && flushedMin%300 == 240 {
			if err := r.downsampleLocked(ctx, flushedMin); err != nil {
				writeErr = err
			}
		}
	}

	_, _ = r.Store.DeleteMetricSamplesBefore(ctx, LayerRawMin, now.Add(-RawMinRetention).Unix())
	_, _ = r.Store.DeleteMetricSamplesBefore(ctx, LayerDown5m, now.Add(-Down5mRetention).Unix())
	r.clearBuckets()
	return writeErr
}

func (r *Recorder) downsampleLocked(ctx context.Context, flushedMin int64) error {
	windowStart := FiveMinUnix(time.Unix(flushedMin, 0).UTC())
	windowEnd := windowStart + 299

	keys := make(map[string]struct{}, len(r.seen)+len(r.buckets)+3)
	for k := range r.seen {
		keys[k] = struct{}{}
	}
	for k := range r.buckets {
		keys[k] = struct{}{}
	}
	if r.NodeID != "" {
		keys[bucketKey(SeriesNodeCPU, r.NodeID)] = struct{}{}
		keys[bucketKey(SeriesNodeMem, r.NodeID)] = struct{}{}
		keys[bucketKey(SeriesNodeDisk, r.NodeID)] = struct{}{}
	}

	var down []store.MetricSample
	for key := range keys {
		series, subject := splitBucketKey(key)
		if series == "" || subject == "" {
			continue
		}
		samples, err := r.Store.ListMetricSamples(ctx, series, subject, LayerRawMin, windowStart, windowEnd)
		if err != nil {
			return err
		}
		vals := make([]float64, 0, len(samples))
		for _, s := range samples {
			vals = append(vals, s.Value)
		}
		v, ok := Aggregate(KindOf(series), vals)
		if !ok {
			continue
		}
		down = append(down, store.MetricSample{
			Series:    series,
			SubjectID: subject,
			Layer:     LayerDown5m,
			TSUnix:    windowStart,
			Value:     v,
		})
	}
	return r.Store.InsertMetricSamples(ctx, down)
}

func (r *Recorder) push(series, subject string, v float64) {
	if subject == "" || !ValidSample(v) {
		return
	}
	if r.buckets == nil {
		r.buckets = make(map[string][]float64)
	}
	key := bucketKey(series, subject)
	r.buckets[key] = append(r.buckets[key], v)
}

func (r *Recorder) clearBuckets() {
	r.buckets = make(map[string][]float64)
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Recorder) disk() float64 {
	if r.DiskPercent != nil {
		return r.DiskPercent()
	}
	return 0
}

func bucketKey(series, subject string) string {
	return series + "\x00" + subject
}

func splitBucketKey(key string) (series, subject string) {
	i := strings.IndexByte(key, 0)
	if i < 0 {
		return key, ""
	}
	return key[:i], key[i+1:]
}
