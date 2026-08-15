package agent

import "testing"

func TestLiveSource_SnapshotResourcesUnknown(t *testing.T) {
	src := &liveSource{nodeID: "n1", hostname: "h1"}
	sum := src.Snapshot()
	if sum.Resources.CPUPercent >= 0 || sum.Resources.MemoryPercent >= 0 || sum.Resources.DiskPercent >= 0 {
		t.Fatalf("uncollected resources must be unknown (negative), got %+v", sum.Resources)
	}
}
