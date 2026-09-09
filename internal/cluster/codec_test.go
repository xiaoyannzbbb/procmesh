package cluster_test

import (
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/version"
)

type oversizedMetaSource struct {
	s cluster.NodeSummary
}

func (s oversizedMetaSource) Snapshot() cluster.NodeSummary { return s.s }

func TestEncodeMeta_OmitsProcessesAndFits512(t *testing.T) {
	s := cluster.NodeSummary{
		NodeID: "n1", ClusterID: "c1", Hostname: "h", BootID: "b",
		State: cluster.StateAlive, AgentVersion: version.Agent,
		ProtocolVersion: version.Protocol, APIAddress: "127.0.0.1:18680",
		GossipAddress:       "127.0.0.1:18689",
		OS:                  "linux",
		Arch:                "amd64",
		Processes:           []cluster.ProcessSummary{{Name: "web", Desired: "RUNNING"}},
		DisableRemoteCreate: true,
		DisableRemoteUpdate: true,
		DisableRemoteDelete: true,
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
	if got.NodeID != "n1" || got.ProtocolVersion != version.Protocol {
		t.Fatalf("%+v", got)
	}
	if !got.DisableRemoteCreate || !got.DisableRemoteUpdate || !got.DisableRemoteDelete {
		t.Fatalf("remote flags %+v", got)
	}
	if got.OS != "linux" || got.Arch != "amd64" {
		t.Fatalf("os/arch %+v", got)
	}
}

func TestMesh_NodeMetaAlwaysHonorsLimit(t *testing.T) {
	labels := make(map[string]string, 32)
	for i := range 32 {
		labels[strings.Repeat("k", 20)+string(rune('a'+i))] = strings.Repeat("v", 80)
	}
	source := oversizedMetaSource{s: cluster.NodeSummary{
		NodeID: "node-1", ClusterID: "cluster-1", Hostname: "host-1", BootID: "boot-1",
		State: cluster.StateAlive, AgentVersion: version.Agent, ProtocolVersion: version.Protocol,
		APIAddress: "127.0.0.1:18680", RPCAddress: "127.0.0.1:18683", Labels: labels,
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-1", WorkloadVersion: 9, WorkloadObservationSeq: 14,
	}}
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "node-1", BindAddr: "127.0.0.1", BindPort: 0, Source: source, TestFast: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	for _, limit := range []int{512, 128, 64, 1} {
		if raw := mesh.NodeMeta(limit); len(raw) > limit {
			t.Fatalf("NodeMeta(%d) returned %d bytes", limit, len(raw))
		}
	}
	got, err := cluster.DecodeMeta(mesh.NodeMeta(512))
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkloadEpoch != "epoch-1" || got.WorkloadVersion != 9 || got.WorkloadObservationSeq != 14 {
		t.Fatalf("512-byte metadata lost workload hint: %+v", got)
	}
}

func TestDecodeMeta_OldPayloadLeavesOSArchEmpty(t *testing.T) {
	got, err := cluster.DecodeMeta([]byte(`{"node_id":"n1","cluster_id":"c1","hostname":"h","boot_id":"b","state":"ALIVE","agent_version":"0.1.0","protocol_version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != "n1" || got.OS != "" || got.Arch != "" {
		t.Fatalf("old member must keep empty os/arch, got %+v", got)
	}
	if got.OS == "darwin" || got.OS == "linux" {
		t.Fatal("missing os must not be treated as a known platform")
	}
}

func TestEncodeState_KeepsOSArch(t *testing.T) {
	s := cluster.NodeSummary{NodeID: "n1", OS: "linux", Arch: "arm64"}
	got, err := cluster.DecodeState(cluster.EncodeState(s))
	if err != nil {
		t.Fatal(err)
	}
	if got.OS != "linux" || got.Arch != "arm64" {
		t.Fatalf("os/arch %+v", got)
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
