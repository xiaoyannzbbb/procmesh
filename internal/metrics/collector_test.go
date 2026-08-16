package metrics

import (
	"context"
	"os"
	"testing"
	"time"
)

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

func TestCollector_StartStop(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 100*time.Millisecond)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 等待至少一次采集
	time.Sleep(50 * time.Millisecond)

	// 验证已采集数据
	node, err := c.NodeMetrics()
	if err != nil {
		t.Fatalf("NodeMetrics failed: %v", err)
	}
	if node == nil {
		t.Fatal("node metrics is nil")
	}

	// 停止
	c.Stop()

	// 再次读取应该仍然返回缓存数据
	node2, err := c.NodeMetrics()
	if err != nil {
		t.Errorf("NodeMetrics after Stop failed: %v", err)
	}
	if node2 == nil {
		t.Error("node metrics is nil after Stop")
	}
}

func TestCollector_BackgroundUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	// 使用较长的 interval 来避免采集重叠（collectNode 阻塞 1 秒）
	c := New(tmpDir, 1500*time.Millisecond)

	ctx := context.Background()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer c.Stop()

	// 等待第一次采集完成（Start 立即调用一次）
	time.Sleep(1100 * time.Millisecond)

	c.mu.RLock()
	firstUpdate := c.lastUpdate
	c.mu.RUnlock()

	if firstUpdate.IsZero() {
		t.Fatal("first update timestamp is zero")
	}

	// 等待第二次采集（ticker 触发 + 采集完成）
	time.Sleep(1700 * time.Millisecond)

	c.mu.RLock()
	secondUpdate := c.lastUpdate
	c.mu.RUnlock()

	if !secondUpdate.After(firstUpdate) {
		t.Error("metrics not updating in background")
	}

	t.Logf("updates: first=%v, second=%v, diff=%v",
		firstUpdate, secondUpdate, secondUpdate.Sub(firstUpdate))
}

func TestCollector_ConcurrentRead(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 50*time.Millisecond)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer c.Stop()

	// 等待初始采集
	time.Sleep(100 * time.Millisecond)

	// 并发读取
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 20; j++ {
				node, err := c.NodeMetrics()
				if err != nil {
					t.Errorf("goroutine %d: NodeMetrics failed: %v", id, err)
					break
				}
				if node == nil {
					t.Errorf("goroutine %d: node is nil", id)
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			done <- true
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestCollector_ProcessMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 5*time.Second)

	// ProcessMetrics 不需要 Start
	pm, err := c.ProcessMetrics(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessMetrics failed: %v", err)
	}
	if pm == nil {
		t.Fatal("process metrics is nil")
	}
	if pm.PID != os.Getpid() {
		t.Errorf("wrong PID: got %d, want %d", pm.PID, os.Getpid())
	}
}

func TestCollector_NodeMetrics_BeforeStart(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 5*time.Second)

	// 未启动前读取应该返回 error
	_, err := c.NodeMetrics()
	if err == nil {
		t.Fatal("expected error before Start")
	}
	t.Logf("got expected error: %v", err)
}

func TestCollector_InvalidDataDir(t *testing.T) {
	c := New("/nonexistent/impossible/path/12345", 5*time.Second)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		// Start 不应该失败，只是采集会失败
		t.Fatalf("Start should not fail: %v", err)
	}
	defer c.Stop()

	// 等待采集尝试
	time.Sleep(100 * time.Millisecond)

	// 读取应该返回错误
	_, err := c.NodeMetrics()
	if err == nil {
		t.Fatal("expected error for invalid dataDir")
	}
	t.Logf("got expected error: %v", err)
}
