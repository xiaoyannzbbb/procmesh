package cluster_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/version"
)

func TestEncodeMeta_OmitsProcessesAndFits512(t *testing.T) {
	s := cluster.NodeSummary{
		NodeID: "n1", ClusterID: "c1", Hostname: "h", BootID: "b",
		State: cluster.StateAlive, AgentVersion: version.Agent,
		ProtocolVersion: version.Protocol, APIAddress: "127.0.0.1:18680",
		GossipAddress: "127.0.0.1:18689",
		Processes:     []cluster.ProcessSummary{{Name: "web", Desired: "RUNNING"}},
	}
	raw := cluster.EncodeMeta(s)
	if len(raw) >= 512 {
		t.Fatalf("meta too large: %d", len(raw))
	}
	got, err := cluster.DecodeMeta(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Processes) != 0 {
		t.Fatalf("meta must omit processes: %+v", got.Processes)
	}
	if got.NodeID != "n1" || got.ProtocolVersion != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestEncodeState_KeepsProcessSummary(t *testing.T) {
	s := cluster.NodeSummary{
		NodeID: "n1", Processes: []cluster.ProcessSummary{{
			ProcessID: "pid-1", Name: "web", Group: "finance",
			Desired: "RUNNING", Observed: "RUNNING",
			Health: "HEALTHY", LatestRevision: 3, ActiveRevision: 3,
			FreshnessUnixMs: 100,
		}},
	}
	got, err := cluster.DecodeState(cluster.EncodeState(s))
	if err != nil {
		t.Fatal(err)
	}
	if p := got.Processes[0]; p.ProcessID != "pid-1" || p.Group != "finance" || p.Name != "web" {
		t.Fatalf("%+v", p)
	}
}

func TestEncodeState_KeepsHistoryPause(t *testing.T) {
	s := cluster.NodeSummary{
		NodeID: "n1",
		Resources: cluster.ResourceSummary{
			CPUPercent:          10,
			MemoryPercent:       20,
			DiskPercent:         93,
			HistoryWritesPaused: true,
			HistoryPausePercent: 93,
		},
	}

	got, err := cluster.DecodeState(cluster.EncodeState(s))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Resources.HistoryWritesPaused || got.Resources.HistoryPausePercent != 93 {
		t.Fatalf("history pause resources = %+v", got.Resources)
	}
}

func TestDecodeState_IgnoresUnknownProcessFields(t *testing.T) {
	raw := []byte(`{"node_id":"n1","processes":[{"name":"web","process_id":"p1","group":"finance","extra":1}]}`)
	got, err := cluster.DecodeState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Processes[0].ProcessID != "p1" || got.Processes[0].Group != "finance" {
		t.Fatalf("%+v", got.Processes[0])
	}
}
