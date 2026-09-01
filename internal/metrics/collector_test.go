package metrics

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type manualTicker struct {
	c       chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newManualTicker() *manualTicker {
	return &manualTicker{c: make(chan time.Time), stopped: make(chan struct{})}
}

func (t *manualTicker) Chan() <-chan time.Time { return t.c }
func (t *manualTicker) Stop() {
	t.once.Do(func() { close(t.stopped) })
}

func newTestCollector(sampler nodeSampler, ticker *manualTicker) *Collector {
	c := New("/tmp", time.Hour)
	c.sampleNode = sampler
	c.newTicker = func(time.Duration) collectorTicker { return ticker }
	return c
}

func receiveSample(t *testing.T, samples <-chan int) int {
	t.Helper()
	select {
	case sample := <-samples:
		return sample
	case <-time.After(time.Second):
		t.Fatal("collector did not sample")
		return 0
	}
}

func waitNodeCPU(t *testing.T, c *Collector, want float64) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		node, err := c.NodeMetrics()
		if err == nil && node.CPUPercent == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("NodeMetrics never published CPU %.0f; last node=%+v err=%v", want, node, err)
		default:
			runtime.Gosched()
		}
	}
}

func TestCollector_New(t *testing.T) {
	c := New("/tmp", 5*time.Second)
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.interval != 5*time.Second {
		t.Errorf("wrong interval: got %v, want %v", c.interval, 5*time.Second)
	}
	if c.dataDir != "/tmp" {
		t.Errorf("wrong dataDir: got %s, want /tmp", c.dataDir)
	}
}

func TestCollector_StartDoesNotWaitForInitialSample(t *testing.T) {
	ticker := newManualTicker()
	started := make(chan struct{})
	release := make(chan struct{})
	c := newTestCollector(func(string) (*NodeMetrics, error) {
		close(started)
		<-release
		return &NodeMetrics{CPUPercent: 1}, nil
	}, ticker)
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		c.Stop()
	})

	returned := make(chan error, 1)
	go func() { returned <- c.Start(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("initial sample did not start")
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start blocked on initial sample")
	}
	if _, err := c.NodeMetrics(); err == nil || !strings.Contains(err.Error(), "no metrics available") {
		t.Fatalf("NodeMetrics before initial sample err=%v, want no metrics available", err)
	}
	close(release)
	released = true
}

func TestCollector_PublishesInitialSampleAndTickUpdates(t *testing.T) {
	ticker := newManualTicker()
	samples := make(chan int, 2)
	next := 0
	c := newTestCollector(func(string) (*NodeMetrics, error) {
		next++
		samples <- next
		return &NodeMetrics{CPUPercent: float64(next)}, nil
	}, ticker)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer c.Stop()

	if got := receiveSample(t, samples); got != 1 {
		t.Fatalf("initial sample=%d, want 1", got)
	}
	waitNodeCPU(t, c, 1)
	ticker.c <- time.Now()
	if got := receiveSample(t, samples); got != 2 {
		t.Fatalf("tick sample=%d, want 2", got)
	}
	waitNodeCPU(t, c, 2)
}

func TestCollector_StopPreventsFurtherSamples(t *testing.T) {
	ticker := newManualTicker()
	samples := make(chan int, 2)
	calls := 0
	c := newTestCollector(func(string) (*NodeMetrics, error) {
		calls++
		samples <- calls
		return &NodeMetrics{CPUPercent: float64(calls)}, nil
	}, ticker)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if got := receiveSample(t, samples); got != 1 {
		t.Fatalf("initial sample=%d, want 1", got)
	}
	waitNodeCPU(t, c, 1)
	c.Stop()
	select {
	case <-ticker.stopped:
	case <-time.After(time.Second):
		t.Fatal("ticker was not stopped")
	}
	select {
	case ticker.c <- time.Now():
		t.Fatal("collector accepted a tick after Stop")
	default:
	}
	node, err := c.NodeMetrics()
	if err != nil || node.CPUPercent != 1 {
		t.Fatalf("NodeMetrics after Stop node=%+v err=%v", node, err)
	}
}

func TestCollector_SampleErrorIsVisible(t *testing.T) {
	ticker := newManualTicker()
	want := errors.New("sample failed")
	sampled := make(chan int, 1)
	c := newTestCollector(func(string) (*NodeMetrics, error) {
		sampled <- 1
		return nil, want
	}, ticker)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer c.Stop()
	receiveSample(t, sampled)

	deadline := time.After(time.Second)
	for {
		_, err := c.NodeMetrics()
		if errors.Is(err, want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("NodeMetrics err=%v, want %v", err, want)
		default:
			runtime.Gosched()
		}
	}
}

func TestCollector_ConcurrentRead(t *testing.T) {
	ticker := newManualTicker()
	sampled := make(chan int, 1)
	c := newTestCollector(func(string) (*NodeMetrics, error) {
		sampled <- 1
		return &NodeMetrics{CPUPercent: 42}, nil
	}, ticker)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer c.Stop()
	receiveSample(t, sampled)
	waitNodeCPU(t, c, 42)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				node, err := c.NodeMetrics()
				if err != nil || node.CPUPercent != 42 {
					t.Errorf("NodeMetrics node=%+v err=%v", node, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestCollector_ProcessMetrics(t *testing.T) {
	c := New(t.TempDir(), 5*time.Second)
	pm, err := c.ProcessMetrics(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessMetrics failed: %v", err)
	}
	if pm == nil || pm.PID != os.Getpid() {
		t.Fatalf("ProcessMetrics=%+v, want PID %d", pm, os.Getpid())
	}
}

func TestCollector_NodeMetrics_BeforeStart(t *testing.T) {
	c := New(t.TempDir(), 5*time.Second)
	if _, err := c.NodeMetrics(); err == nil || !strings.Contains(err.Error(), "no metrics available") {
		t.Fatalf("NodeMetrics err=%v, want no metrics available", err)
	}
}
