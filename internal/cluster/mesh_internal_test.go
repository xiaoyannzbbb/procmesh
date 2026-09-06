package cluster

import (
	"sync/atomic"
	"testing"
	"time"
)

type countingSummarySource struct {
	calls   atomic.Int64
	summary NodeSummary
}

func (s *countingSummarySource) Snapshot() NodeSummary {
	s.calls.Add(1)
	return s.summary
}

func TestMesh_RejectRemoteMetadataUsesCachedIdentity(t *testing.T) {
	source := &countingSummarySource{summary: NodeSummary{
		NodeID: "local", BootID: "boot-local", State: StateAlive,
	}}
	mesh, err := Start(Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source: source, TestFast: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })

	source.calls.Store(0)
	for range 10 {
		if mesh.rejectLocalCloneLocked(NodeSummary{NodeID: "remote", BootID: "boot-remote"}) {
			t.Fatal("remote node rejected as local clone")
		}
	}
	if calls := source.calls.Load(); calls != 0 {
		t.Fatalf("remote identity checks called full Snapshot %d times", calls)
	}
}

func TestMesh_MembersExpiresLeftTombstone(t *testing.T) {
	var nowUnixMs atomic.Int64
	nowUnixMs.Store(time.Unix(1_700_000_000, 0).UTC().UnixMilli())
	source := &countingSummarySource{summary: NodeSummary{
		NodeID: "local", BootID: "boot-local", State: StateAlive,
	}}
	mesh, err := Start(Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source: source, TestFast: true,
		Now: func() time.Time { return time.UnixMilli(nowUnixMs.Load()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })

	mesh.MergeForTest(EncodeState(NodeSummary{
		NodeID: "departed", BootID: "boot-departed", State: StateLeft, ProtocolVersion: 7,
	}))
	if got := len(mesh.Members()); got != 2 {
		t.Fatalf("fresh LEFT tombstone must remain visible: got %d members", got)
	}

	nowUnixMs.Add((500 * time.Millisecond).Milliseconds())
	mesh.MergeForTest(EncodeState(NodeSummary{
		NodeID: "departed", BootID: "boot-departed", State: StateLeft,
	}))
	nowUnixMs.Add((500 * time.Millisecond).Milliseconds())
	if got := mesh.Members(); len(got) != 1 || got[0].NodeID != "local" {
		t.Fatalf("repeated LEFT must not extend tombstone retention: %+v", got)
	}
	if protocol, ok := mesh.KnownProtocolVersion("departed"); !ok || protocol != 7 {
		t.Fatalf("expired Raft peer protocol=%d known=%v want 7, true", protocol, ok)
	}
}
