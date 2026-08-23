package cluster

import (
	"sync/atomic"
	"testing"
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
