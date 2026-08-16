package metrics

import (
	"os"
	"testing"
	"time"
)

func TestCollectProcess_Self(t *testing.T) {
	// 测试采集当前进程
	pid := os.Getpid()

	pm, err := collectProcess(pid)
	if err != nil {
		t.Fatalf("collectProcess failed: %v", err)
	}

	if pm.PID != pid {
		t.Errorf("wrong PID: got %d, want %d", pm.PID, pid)
	}

	// CPU 可能是 0（瞬时值），但不应该是负数
	if pm.CPUPercent < 0 {
		t.Errorf("invalid CPU percent: %f", pm.CPUPercent)
	}

	// 内存不应该为 0
	if pm.MemoryBytes == 0 {
		t.Error("memory bytes should not be zero")
	}

	// 验证时间戳
	if time.Since(pm.Timestamp) > time.Second {
		t.Errorf("stale timestamp: %v", pm.Timestamp)
	}

	t.Logf("process %d: CPU=%.1f%% Memory=%.1fMB",
		pid, pm.CPUPercent, float64(pm.MemoryBytes)/(1024*1024))
}

func TestCollectProcess_NotFound(t *testing.T) {
	// 使用不存在的 PID
	_, err := collectProcess(999999)
	if err == nil {
		t.Fatal("expected error for non-existent process")
	}
	t.Logf("got expected error: %v", err)
}

func TestCollectProcess_InvalidPID(t *testing.T) {
	testCases := []struct {
		name string
		pid  int
	}{
		{"zero", 0},
		{"negative", -1},
		{"large negative", -999},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := collectProcess(tc.pid)
			if err == nil {
				t.Fatalf("expected error for PID %d", tc.pid)
			}
			t.Logf("PID %d: got expected error: %v", tc.pid, err)
		})
	}
}
