package metrics

import (
	"testing"
	"time"
)

func TestNodeMetrics_ValidValues(t *testing.T) {
	nm := &NodeMetrics{
		CPUPercent:    45.5,
		MemoryPercent: 67.2,
		MemoryUsed:    8 * 1024 * 1024 * 1024,  // 8GB
		MemoryTotal:   16 * 1024 * 1024 * 1024, // 16GB
		DiskPercent:   30.0,
		DiskUsed:      100 * 1024 * 1024 * 1024,  // 100GB
		DiskTotal:     500 * 1024 * 1024 * 1024,  // 500GB
		Timestamp:     time.Now(),
	}

	if nm.CPUPercent < 0 || nm.CPUPercent > 100 {
		t.Errorf("invalid CPU percent: %f", nm.CPUPercent)
	}
	if nm.MemoryPercent < 0 || nm.MemoryPercent > 100 {
		t.Errorf("invalid memory percent: %f", nm.MemoryPercent)
	}
	if nm.DiskPercent < 0 || nm.DiskPercent > 100 {
		t.Errorf("invalid disk percent: %f", nm.DiskPercent)
	}
}

func TestProcessMetrics_ValidValues(t *testing.T) {
	pm := &ProcessMetrics{
		PID:         12345,
		CPUPercent:  25.5,
		MemoryBytes: 512 * 1024 * 1024, // 512MB
		Timestamp:   time.Now(),
	}

	if pm.PID <= 0 {
		t.Errorf("invalid PID: %d", pm.PID)
	}
	if pm.CPUPercent < 0 {
		t.Errorf("invalid CPU percent: %f", pm.CPUPercent)
	}
	if pm.MemoryBytes == 0 {
		t.Error("memory bytes should not be zero")
	}
}
