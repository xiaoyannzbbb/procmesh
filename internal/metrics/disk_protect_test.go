package metrics

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckDiskUsage(t *testing.T) {
	tests := []struct {
		name        string
		diskPercent float64
		wantCalled  bool
	}{
		{"below threshold", 80.0, false},
		{"at 85% warn", 85.0, true},
		{"at 90% aggressive", 90.0, true},
		{"at 95% critical", 95.0, true},
		{"above critical", 98.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			called := false
			protectFn := func() { called = true }

			checkDiskUsage(tmpDir, tt.diskPercent, protectFn)

			if called != tt.wantCalled {
				t.Errorf("protectFn called = %v, want %v", called, tt.wantCalled)
			}
		})
	}
}

func TestCollectorDiskProtection(t *testing.T) {
	tmpDir := t.TempDir()
	logDir := filepath.Join(tmpDir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	c := New(tmpDir, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer c.Stop()

	time.Sleep(200 * time.Millisecond)

	node, err := c.NodeMetrics()
	if err != nil {
		t.Logf("NodeMetrics error (expected on some systems): %v", err)
		return
	}

	if node.DiskPercent >= 85.0 {
		t.Logf("High disk usage detected: %.1f%%", node.DiskPercent)
	} else {
		t.Logf("Normal disk usage: %.1f%%", node.DiskPercent)
	}
}
