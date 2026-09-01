package update_test

import (
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/update"
)

func TestOrderTargets_EntryLastLeaderSecondLast(t *testing.T) {
	t.Parallel()
	nodes := []update.TargetSpec{
		{NodeID: "leader", Hostname: "aaa-leader"},
		{NodeID: "entry", Hostname: "zzz-entry"},
		{NodeID: "b", Hostname: "host-b"},
		{NodeID: "a", Hostname: "host-a"},
		{NodeID: "skip", Hostname: "host-skip", SkipReason: update.SkipMACOS},
	}
	got := update.OrderTargets(nodes, "entry", "leader")
	want := []string{"a", "b", "leader", "entry", "skip"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].NodeID != id {
			t.Fatalf("i=%d got=%s want=%s full=%v", i, got[i].NodeID, id, ids(got))
		}
	}
	if got[4].SkipReason != update.SkipMACOS {
		t.Fatalf("skip reason=%q", got[4].SkipReason)
	}
}

func TestOrderTargets_EntryIsLiveLeaderGoesLast(t *testing.T) {
	t.Parallel()
	nodes := []update.TargetSpec{
		{NodeID: "entry", Hostname: "host-entry"},
		{NodeID: "z", Hostname: "host-z"},
		{NodeID: "a", Hostname: "host-a"},
	}
	got := update.OrderTargets(nodes, "entry", "entry")
	want := []string{"a", "z", "entry"}
	for i, id := range want {
		if got[i].NodeID != id {
			t.Fatalf("i=%d got=%s want=%s", i, got[i].NodeID, id)
		}
	}
}

func TestOrderTargets_NoLiveLeaderPutsEntryLast(t *testing.T) {
	t.Parallel()
	nodes := []update.TargetSpec{
		{NodeID: "entry", Hostname: "host-entry"},
		{NodeID: "z", Hostname: "host-z"},
		{NodeID: "a", Hostname: "host-a"},
	}
	got := update.OrderTargets(nodes, "entry", "")
	want := []string{"a", "z", "entry"}
	for i, id := range want {
		if got[i].NodeID != id {
			t.Fatalf("i=%d got=%s want=%s", i, got[i].NodeID, id)
		}
	}
}

func TestOrderTargets_HostnameThenNodeID(t *testing.T) {
	t.Parallel()
	nodes := []update.TargetSpec{
		{NodeID: "n2", Hostname: "same"},
		{NodeID: "n1", Hostname: "same"},
	}
	got := update.OrderTargets(nodes, "", "")
	if got[0].NodeID != "n1" || got[1].NodeID != "n2" {
		t.Fatalf("%v", ids(got))
	}
}

func TestLiveLeaderID_RequiresGossipLIVE(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	live := cluster.NodeSummary{
		NodeID:            "leader",
		Hostname:          "host-leader",
		State:             cluster.StateAlive,
		LastUpdatedUnixMs: now.UnixMilli(),
	}
	stale := live
	stale.LastUpdatedUnixMs = now.Add(-time.Minute).UnixMilli()
	unknown := live
	unknown.LastUpdatedUnixMs = 0
	failed := live
	failed.State = cluster.StateFailed

	if got := update.LiveLeaderID("leader", now, []cluster.NodeSummary{stale}); got != "" {
		t.Fatalf("stale gossip: got %q", got)
	}
	if got := update.LiveLeaderID("leader", now, []cluster.NodeSummary{unknown}); got != "" {
		t.Fatalf("unknown gossip: got %q", got)
	}
	if got := update.LiveLeaderID("leader", now, []cluster.NodeSummary{failed}); got != "" {
		t.Fatalf("failed gossip: got %q", got)
	}
	if got := update.LiveLeaderID("leader", now, nil); got != "" {
		t.Fatalf("missing member: got %q", got)
	}
	if got := update.LiveLeaderID("leader", now, []cluster.NodeSummary{live}); got != "leader" {
		t.Fatalf("live gossip: got %q", got)
	}
}

func ids(nodes []update.TargetSpec) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.NodeID
	}
	return out
}
