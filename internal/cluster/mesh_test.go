package cluster_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/qleelulu/procmesh/internal/cluster"
)

type staticSource struct {
	s cluster.NodeSummary
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
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

func TestMesh_StaleAlivePresentMemberMarkedSuspect(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	srcA := &staticSource{s: cluster.NodeSummary{
		NodeID: "na", BootID: "ba", Hostname: "a", State: cluster.StateAlive, ProtocolVersion: 1,
		LastUpdatedUnixMs: now.UnixMilli(),
	}}
	srcB := &staticSource{s: cluster.NodeSummary{
		NodeID: "nb", BootID: "bb", Hostname: "b", State: cluster.StateAlive, ProtocolVersion: 1,
		LastUpdatedUnixMs: now.Add(-3 * time.Second).UnixMilli(),
	}}
	a, err := cluster.Start(cluster.Config{
		NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: srcA, Protocol: 1, TestFast: true,
		SuspectAfter: 2 * time.Second,
		Now:          func() time.Time { return now },
	})
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

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if memberState(a, "nb") == cluster.StateSuspect {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("want nb SUSPECT via stale Members() overlay, got %q members=%+v", memberState(a, "nb"), a.Members())
}

func TestMesh_StaleOverlayLogsTransitionAndPushPullRecovery(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srcA := &staticSource{s: cluster.NodeSummary{
		NodeID: "na", BootID: "ba", State: cluster.StateAlive,
		LastUpdatedUnixMs: now.UnixMilli(),
	}}
	srcB := &staticSource{s: cluster.NodeSummary{
		NodeID: "nb", BootID: "bb", State: cluster.StateAlive,
		LastUpdatedUnixMs: now.Add(-3 * time.Second).UnixMilli(),
	}}
	a, err := cluster.Start(cluster.Config{
		NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: srcA,
		TestFast: true, SuspectAfter: 2 * time.Second,
		Now: func() time.Time { return now }, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Shutdown() })
	b, err := cluster.Start(cluster.Config{
		NodeID: "nb", BindAddr: "127.0.0.1", BindPort: 0, Source: srcB,
		TestFast: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown() })
	if _, err := b.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatal(err)
	}
	waitMembers(t, a, 2)
	_ = a.Members()
	_ = a.Members()

	got := logs.String()
	if count := strings.Count(got, `msg="gossip member marked suspect"`); count != 1 {
		t.Fatalf("suspect transition logs=%d want 1: %s", count, got)
	}
	for _, want := range []string{"reason=metadata_stale", "observer_node_id=na", "node_id=nb", "metadata_age_ms=3000", "threshold_ms=2000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("suspect log missing %q: %s", want, got)
		}
	}

	a.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "nb", BootID: "bb", State: cluster.StateAlive,
		LastUpdatedUnixMs: now.UnixMilli(),
	}))
	got = logs.String()
	for _, want := range []string{`msg="gossip member recovered"`, "source=push_pull", "previous_state=SUSPECT", "state=ALIVE"} {
		if !strings.Contains(got, want) {
			t.Fatalf("recovery log missing %q: %s", want, got)
		}
	}
}

func TestMesh_UpdateTimesOutAndLogsFailure(t *testing.T) {
	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srcA := &staticSource{s: cluster.NodeSummary{NodeID: "na", BootID: "ba", State: cluster.StateAlive}}
	srcB := &staticSource{s: cluster.NodeSummary{NodeID: "nb", BootID: "bb", State: cluster.StateAlive}}
	a, err := cluster.Start(cluster.Config{
		NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: srcA,
		TestFast: true, UpdateTimeout: 20 * time.Millisecond, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := cluster.Start(cluster.Config{
		NodeID: "nb", BindAddr: "127.0.0.1", BindPort: 0, Source: srcB,
		TestFast: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown() })
	if _, err := b.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatal(err)
	}
	waitMembers(t, a, 2)
	if err := a.Shutdown(); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	a.Update()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Update returned too slowly: %s", elapsed)
	}
	got := logs.String()
	for _, want := range []string{`msg="gossip metadata publish failed"`, "timeout_ms=20", "observer_node_id=na", `error="timeout waiting for update broadcast"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("publish failure log missing %q: %s", want, got)
		}
	}
}

func TestMesh_ZeroLastUpdatedNotSuspect(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	srcA := &staticSource{s: cluster.NodeSummary{NodeID: "na", BootID: "ba", Hostname: "a", State: cluster.StateAlive, ProtocolVersion: 1}}
	srcB := &staticSource{s: cluster.NodeSummary{NodeID: "nb", BootID: "bb", Hostname: "b", State: cluster.StateAlive, ProtocolVersion: 1}}
	a, err := cluster.Start(cluster.Config{
		NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: srcA, Protocol: 1, TestFast: true,
		SuspectAfter: 2 * time.Second,
		Now:          func() time.Time { return now },
	})
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
	if got := memberState(a, "nb"); got != cluster.StateAlive {
		t.Fatalf("LastUpdated=0 must stay ALIVE, got %q members=%+v", got, a.Members())
	}
}

func TestMesh_StaleDoesNotOverlayFailed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	srcA := &staticSource{s: cluster.NodeSummary{NodeID: "na", BootID: "ba", Hostname: "a", State: cluster.StateAlive, ProtocolVersion: 1}}
	srcB := &staticSource{s: cluster.NodeSummary{NodeID: "nb", BootID: "bb", Hostname: "b", State: cluster.StateAlive, ProtocolVersion: 1}}
	a, err := cluster.Start(cluster.Config{
		NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: srcA, Protocol: 1, TestFast: true,
		SuspectAfter: 2 * time.Second,
		Now:          func() time.Time { return now },
	})
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
	a.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "nb", BootID: "bb", Hostname: "b", State: cluster.StateFailed, ProtocolVersion: 1,
		LastUpdatedUnixMs: now.Add(-10 * time.Second).UnixMilli(),
	}))
	if got := memberState(a, "nb"); got != cluster.StateFailed {
		t.Fatalf("FAILED must not become SUSPECT, got %q", got)
	}
}

func TestMesh_ApplyMemberlistStateSuspect(t *testing.T) {
	src := &staticSource{s: cluster.NodeSummary{NodeID: "na", BootID: "ba", Hostname: "a", State: cluster.StateAlive, ProtocolVersion: 1}}
	m, err := cluster.Start(cluster.Config{NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: src, Protocol: 1, TestFast: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown() })

	put := func(id string, st cluster.State) {
		t.Helper()
		m.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
			NodeID: id, BootID: id, Hostname: id, State: st, ProtocolVersion: 1,
		}))
	}

	put("nb", cluster.StateAlive)
	m.ApplyMemberlistState("nb", memberlist.StateSuspect)
	if got := memberState(m, "nb"); got != cluster.StateSuspect {
		t.Fatalf("alive→suspect: got %q", got)
	}

	terminals := []cluster.State{cluster.StateFailed, cluster.StateLeft, cluster.StateRemoved, cluster.StateRevoked}
	for i, st := range terminals {
		id := string(rune('c' + i))
		put(id, st)
		m.ApplyMemberlistState(id, memberlist.StateSuspect)
		if got := memberState(m, id); got != st {
			t.Fatalf("%s: want %s held, got %q", id, st, got)
		}
	}
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
