package metrics

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestCollectNode(t *testing.T) {
	tmpDir := t.TempDir()

	node, err := collectNode(tmpDir)
	if err != nil {
		t.Fatalf("collectNode failed: %v", err)
	}

	// 验证 CPU 范围
	if node.CPUPercent < 0 || node.CPUPercent > 100 {
		t.Errorf("invalid CPU percent: %f", node.CPUPercent)
	}

	// 验证内存范围
	if node.MemoryPercent < 0 || node.MemoryPercent > 100 {
		t.Errorf("invalid memory percent: %f", node.MemoryPercent)
	}
	if node.MemoryUsed == 0 || node.MemoryTotal == 0 {
		t.Error("memory values should not be zero")
	}
	if node.MemoryUsed > node.MemoryTotal {
		t.Errorf("memory used (%d) > total (%d)", node.MemoryUsed, node.MemoryTotal)
	}

	// 验证磁盘范围
	if node.DiskPercent < 0 || node.DiskPercent > 100 {
		t.Errorf("invalid disk percent: %f", node.DiskPercent)
	}
	if node.DiskUsed == 0 || node.DiskTotal == 0 {
		t.Error("disk values should not be zero")
	}
	if node.DiskUsed > node.DiskTotal {
		t.Errorf("disk used (%d) > total (%d)", node.DiskUsed, node.DiskTotal)
	}

	// 验证时间戳
	if time.Since(node.Timestamp) > 2*time.Second {
		t.Errorf("stale timestamp: %v", node.Timestamp)
	}

	t.Logf("%s metrics: CPU=%.1f%% Mem=%.1f%% (%.1fGB/%.1fGB) Disk=%.1f%% (%.1fGB/%.1fGB)",
		runtime.GOOS,
		node.CPUPercent,
		node.MemoryPercent,
		float64(node.MemoryUsed)/(1024*1024*1024),
		float64(node.MemoryTotal)/(1024*1024*1024),
		node.DiskPercent,
		float64(node.DiskUsed)/(1024*1024*1024),
		float64(node.DiskTotal)/(1024*1024*1024))
}

func TestCollectNode_InvalidPath(t *testing.T) {
	_, err := collectNode("/nonexistent/path/12345/impossible")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
	t.Logf("got expected error: %v", err)
}

func TestCollectNode_CrossPlatform(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping platform test in short mode")
	}

	// 使用当前目录测试
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}

	node, err := collectNode(wd)
	if err != nil {
		t.Fatalf("%s: collectNode failed: %v", runtime.GOOS, err)
	}

	t.Logf("%s: CPU=%.1f%% Memory=%.1f%% Disk=%.1f%%",
		runtime.GOOS, node.CPUPercent, node.MemoryPercent, node.DiskPercent)
}
