package cluster_test

import (
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
)

type staticSource struct {
	s cluster.NodeSummary
}

func (s *staticSource) Snapshot() cluster.NodeSummary {
	return s.s
}

func TestMesh_TwoNodesSeeEachOther(t *testing.T) {
	srcA := &staticSource{s: cluster.NodeSummary{NodeID: "na", BootID: "ba", Hostname: "a", State: cluster.StateAlive, ProtocolVersion: 1}}
	srcB := &staticSource{s: cluster.NodeSummary{NodeID: "nb", BootID: "bb", Hostname: "b", State: cluster.StateAlive, ProtocolVersion: 1}}
	a, err := cluster.Start(cluster.Config{NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: srcA, Protocol: 1, TestFast: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Shutdown() })
	b, err := cluster.Start(cluster.Config{NodeID: "nb", BindAddr: "127.0.0.1", BindPort: 0, Source: srcB, Protocol: 1, TestFast: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown() })
	if _, err := b.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatal(err)
	}

	waitMembers(t, a, 2)
	waitMembers(t, b, 2)
	ids := map[string]bool{}
	for _, m := range a.Members() {
		ids[m.NodeID] = true
	}
	if !ids["na"] || !ids["nb"] {
		t.Fatalf("%v", ids)
	}
}

func TestMesh_GracefulLeaveMarksLeft(t *testing.T) {
	srcA := &staticSource{s: cluster.NodeSummary{NodeID: "na", BootID: "ba", Hostname: "a", State: cluster.StateAlive, ProtocolVersion: 1}}
	srcB := &staticSource{s: cluster.NodeSummary{NodeID: "nb", BootID: "bb", Hostname: "b", State: cluster.StateAlive, ProtocolVersion: 1}}
	a, err := cluster.Start(cluster.Config{NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: srcA, Protocol: 1, TestFast: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Shutdown() })
	b, err := cluster.Start(cluster.Config{NodeID: "nb", BindAddr: "127.0.0.1", BindPort: 0, Source: srcB, Protocol: 1, TestFast: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown() })
	if _, err := b.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatal(err)
	}
	waitMembers(t, a, 2)

	if err := b.Leave(time.Second); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := memberState(a, "nb"); st == cluster.StateLeft {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("want nb LEFT, got %q members=%+v", memberState(a, "nb"), a.Members())
}

func memberState(m *cluster.Mesh, id string) cluster.State {
	for _, n := range m.Members() {
		if n.NodeID == id {
			return n.State
		}
	}
	return ""
}

func waitMembers(t *testing.T, m *cluster.Mesh, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.Members()) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("want %d members, got %d: %+v", n, len(m.Members()), m.Members())
}
