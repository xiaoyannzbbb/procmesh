package agent

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/metrics"
)

func TestLiveSource_WorkloadObservationVersionsAndRetainsLastGoodSnapshot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	processes := []cluster.ProcessSummary{{ProcessID: "p1", Name: "worker", Observed: "RUNNING"}}
	var readErr error
	src := &liveSource{
		nodeID:        "n1",
		bootID:        "boot-1",
		workloadEpoch: "agent-start-1",
		now:           func() time.Time { return now },
		readWorkload: func(context.Context) ([]cluster.ProcessSummary, error) {
			return append([]cluster.ProcessSummary(nil), processes...), readErr
		},
	}

	first := src.Snapshot()
	if first.WorkloadVersion != 1 || first.WorkloadObservationSeq != 1 {
		t.Fatalf("first workload version/observation = %d/%d, want 1/1", first.WorkloadVersion, first.WorkloadObservationSeq)
	}
	if got := first.Processes[0].FreshnessUnixMs; got != now.UnixMilli() {
		t.Fatalf("first freshness = %d, want %d", got, now.UnixMilli())
	}

	now = now.Add(time.Second)
	second := src.Snapshot()
	if second.WorkloadVersion != 1 || second.WorkloadObservationSeq != 2 {
		t.Fatalf("unchanged workload version/observation = %d/%d, want 1/2", second.WorkloadVersion, second.WorkloadObservationSeq)
	}
	if got := second.Processes[0].FreshnessUnixMs; got != now.UnixMilli() {
		t.Fatalf("second freshness = %d, want %d", got, now.UnixMilli())
	}
	if second.WorkloadLastVerifiedUnixMs != now.UnixMilli() {
		t.Fatalf("second workload verification = %d, want %d", second.WorkloadLastVerifiedUnixMs, now.UnixMilli())
	}

	readErr = errors.New("store unavailable")
	now = now.Add(time.Minute)
	failed := src.Snapshot()
	if failed.WorkloadVersion != 1 || failed.WorkloadObservationSeq != 2 {
		t.Fatalf("failed observation advanced version/sequence: %d/%d", failed.WorkloadVersion, failed.WorkloadObservationSeq)
	}
	if got := failed.Processes[0].FreshnessUnixMs; got != second.Processes[0].FreshnessUnixMs {
		t.Fatalf("failed observation refreshed process: got %d want %d", got, second.Processes[0].FreshnessUnixMs)
	}
	if failed.WorkloadLastVerifiedUnixMs != second.WorkloadLastVerifiedUnixMs {
		t.Fatalf("failed observation refreshed workload verification: got %d want %d", failed.WorkloadLastVerifiedUnixMs, second.WorkloadLastVerifiedUnixMs)
	}

	readErr = nil
	processes[0].Observed = "STOPPED"
	changed := src.Snapshot()
	if changed.WorkloadVersion != 2 || changed.WorkloadObservationSeq != 3 {
		t.Fatalf("changed workload version/observation = %d/%d, want 2/3", changed.WorkloadVersion, changed.WorkloadObservationSeq)
	}
}

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
