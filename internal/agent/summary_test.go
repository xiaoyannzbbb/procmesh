package agent

import (
	"runtime"
	"testing"

	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/metrics"
)

type staticNodeMetrics struct {
	node *metrics.NodeMetrics
}

func (s staticNodeMetrics) NodeMetrics() (*metrics.NodeMetrics, error) {
	return s.node, nil
}

func TestLiveSource_SnapshotResourcesUnknown(t *testing.T) {
	src := &liveSource{
		nodeID:   "n1",
		hostname: "h1",
		diskPolicy: logmgr.Policy{
			EmergencyPercent:    93,
			EmergencyStopWrites: true,
		},
	}
	sum := src.Snapshot()
	if sum.Resources.CPUPercent >= 0 || sum.Resources.MemoryPercent >= 0 || sum.Resources.DiskPercent >= 0 {
		t.Fatalf("uncollected resources must be unknown (negative), got %+v", sum.Resources)
	}
	if sum.Resources.HistoryWritesPaused || sum.Resources.HistoryPausePercent != 93 {
		t.Fatalf("uncollected resources must retain disk policy, got %+v", sum.Resources)
	}
}

func TestLiveSource_ReportsHistoryPauseFromExactDiskUsage(t *testing.T) {
	src := &liveSource{
		nodeID: "n1",
		metrics: staticNodeMetrics{node: &metrics.NodeMetrics{
			CPUPercent:    10,
			MemoryPercent: 20,
			DiskPercent:   93.1,
		}},
		diskPolicy: logmgr.Policy{
			EmergencyPercent:    93,
			EmergencyStopWrites: true,
		},
	}

	resources := src.Snapshot().Resources
	if resources.DiskPercent != 93 {
		t.Fatalf("rounded disk percent = %d, want 93", resources.DiskPercent)
	}
	if !resources.HistoryWritesPaused || resources.HistoryPausePercent != 93 {
		t.Fatalf("history pause resources = %+v", resources)
	}
}

func TestLiveSource_SnapshotOSArch(t *testing.T) {
	sum := (&liveSource{nodeID: "n1"}).Snapshot()
	if sum.OS != runtime.GOOS || sum.Arch != runtime.GOARCH {
		t.Fatalf("os/arch = %q/%q want %s/%s", sum.OS, sum.Arch, runtime.GOOS, runtime.GOARCH)
	}
}

func TestLiveSource_SnapshotRemoteProcessFlags(t *testing.T) {
	src := &liveSource{
		nodeID: "n1",
		process: agentcfg.Process{
			DisableRemoteCreate: true,
			DisableRemoteDelete: true,
		},
	}
	sum := src.Snapshot()
	if !sum.DisableRemoteCreate || sum.DisableRemoteUpdate || !sum.DisableRemoteDelete {
		t.Fatalf("remote flags %+v", sum)
	}
}
