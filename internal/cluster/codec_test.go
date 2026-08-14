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
		ProtocolVersion: version.Protocol, APIAddress: "127.0.0.1:9000",
		GossipAddress: "127.0.0.1:7946",
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
			Name: "web", Desired: "RUNNING", Observed: "RUNNING",
			Health: "HEALTHY", LatestRevision: 3, ActiveRevision: 3,
			FreshnessUnixMs: 100,
		}},
	}
	got, err := cluster.DecodeState(cluster.EncodeState(s))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Processes) != 1 || got.Processes[0].Name != "web" {
		t.Fatalf("%+v", got)
	}
}
