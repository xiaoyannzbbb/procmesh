package agent

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/metrics"
)

func TestCheckDiskUsage_Normal(t *testing.T) {
	node := &metrics.NodeMetrics{
		DiskPercent: 50.0,
	}

	action := determineDiskAction(node.DiskPercent)
	if action != diskActionNone {
		t.Errorf("expected no action for 50%%, got %v", action)
	}
}

func TestCheckDiskUsage_Warning(t *testing.T) {
	node := &metrics.NodeMetrics{
		DiskPercent: 86.0,
	}

	action := determineDiskAction(node.DiskPercent)
	if action != diskActionWarning {
		t.Errorf("expected warning for 86%%, got %v", action)
	}
}

func TestCheckDiskUsage_High(t *testing.T) {
	node := &metrics.NodeMetrics{
		DiskPercent: 92.0,
	}

	action := determineDiskAction(node.DiskPercent)
	if action != diskActionHigh {
		t.Errorf("expected high for 92%%, got %v", action)
	}
}

func TestCheckDiskUsage_Critical(t *testing.T) {
	node := &metrics.NodeMetrics{
		DiskPercent: 96.0,
	}

	action := determineDiskAction(node.DiskPercent)
	if action != diskActionCritical {
		t.Errorf("expected critical for 96%%, got %v", action)
	}
}
