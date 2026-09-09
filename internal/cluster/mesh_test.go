package cluster_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/qleelulu/procmesh/internal/cluster"
)

type staticSource struct {
	s cluster.NodeSummary
}

type staticWorkloadFetcher struct {
	snapshot cluster.WorkloadSnapshot
}

func (f staticWorkloadFetcher) Fetch(_ context.Context, _ cluster.NodeSummary, _ cluster.WorkloadVersion) (cluster.WorkloadSnapshot, error) {
	return f.snapshot, nil
}

type blockingWorkloadFetcher struct {
	started chan struct{}
}

func (f blockingWorkloadFetcher) Fetch(ctx context.Context, _ cluster.NodeSummary, _ cluster.WorkloadVersion) (cluster.WorkloadSnapshot, error) {
	select {
	case <-f.started:
	default:
		close(f.started)
	}
	<-ctx.Done()
	return cluster.WorkloadSnapshot{}, ctx.Err()
}

type workloadFetchResult struct {
	snapshot cluster.WorkloadSnapshot
	err      error
}

type controlledWorkloadFetch struct {
	peer     cluster.NodeSummary
	response chan workloadFetchResult
}

type controlledWorkloadFetcher struct {
	calls chan controlledWorkloadFetch
}

func (f controlledWorkloadFetcher) Fetch(ctx context.Context, peer cluster.NodeSummary, _ cluster.WorkloadVersion) (cluster.WorkloadSnapshot, error) {
	call := controlledWorkloadFetch{peer: peer, response: make(chan workloadFetchResult, 1)}
	select {
	case f.calls <- call:
	case <-ctx.Done():
		return cluster.WorkloadSnapshot{}, ctx.Err()
	}
	select {
	case result := <-call.response:
		return result.snapshot, result.err
	case <-ctx.Done():
		return cluster.WorkloadSnapshot{}, ctx.Err()
	}
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

func TestMesh_WorkloadHintRefreshesMatchingCachedSnapshot(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	now := base
	later := base.Add(45 * time.Second)
	src := &staticSource{s: cluster.NodeSummary{
		NodeID: "local", BootID: "boot-local", State: cluster.StateAlive,
		LastUpdatedUnixMs: now.UnixMilli(),
	}}
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source: src, TestFast: true, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })

	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive,
		LastUpdatedUnixMs:      base.UnixMilli(),
		WorkloadSyncVersion:    1,
		WorkloadEpoch:          "epoch-remote",
		WorkloadVersion:        7,
		WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{
			ProcessID: "p1", Name: "worker", FreshnessUnixMs: base.UnixMilli(),
		}},
	}))
	now = later
	mesh.NotifyUpdate(&memberlist.Node{
		Name: "remote#boot-remote",
		Meta: cluster.EncodeMeta(cluster.NodeSummary{
			NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive,
			LastUpdatedUnixMs:      later.UnixMilli(),
			WorkloadSyncVersion:    1,
			WorkloadEpoch:          "epoch-remote",
			WorkloadVersion:        7,
			WorkloadObservationSeq: 11,
		}),
	})

	for _, node := range mesh.Members() {
		if node.NodeID != "remote" {
			continue
		}
		if got := node.Processes[0].FreshnessUnixMs; got != later.UnixMilli() {
			t.Fatalf("process freshness = %d, want matching hint receive time %d", got, later.UnixMilli())
		}
		return
	}
	t.Fatal("remote node not found")
}

func TestMesh_WorkloadHintFetchesAndAtomicallyReplacesChangedSnapshot(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	now := base
	later := base.Add(5 * time.Second)
	src := &staticSource{s: cluster.NodeSummary{NodeID: "local", BootID: "boot-local", State: cluster.StateAlive}}
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source: src, TestFast: true, Now: func() time.Time { return now },
		WorkloadFetcher: staticWorkloadFetcher{snapshot: cluster.WorkloadSnapshot{
			NodeID: "remote", Epoch: "epoch-remote", Version: 8, ObservationSeq: 11,
			Processes: []cluster.ProcessSummary{{ProcessID: "p2", Name: "replacement", Observed: "RUNNING"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })

	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 7, WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{ProcessID: "p1", Name: "old", FreshnessUnixMs: base.UnixMilli()}},
	}))
	now = later
	mesh.NotifyUpdate(&memberlist.Node{
		Name: "remote#boot-remote",
		Meta: cluster.EncodeMeta(cluster.NodeSummary{
			NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
			WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 8, WorkloadObservationSeq: 11,
		}),
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, node := range mesh.Members() {
			if node.NodeID == "remote" && len(node.Processes) == 1 && node.Processes[0].ProcessID == "p2" {
				if got := node.Processes[0].FreshnessUnixMs; got != later.UnixMilli() {
					t.Fatalf("fetched process freshness = %d, want %d", got, later.UnixMilli())
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("changed workload was not fetched: %+v", mesh.Members())
}

func TestMesh_WorkloadVersionMismatchIsImmediatelyStaleWhileFetchIsPending(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	now := base
	started := make(chan struct{})
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source:   &staticSource{s: cluster.NodeSummary{NodeID: "local", BootID: "boot-local", State: cluster.StateAlive}},
		TestFast: true, Now: func() time.Time { return now },
		WorkloadFetcher: blockingWorkloadFetcher{started: started},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 7, WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{ProcessID: "p1", Name: "worker", FreshnessUnixMs: base.UnixMilli()}},
	}))
	now = base.Add(time.Second)
	mesh.NotifyUpdate(&memberlist.Node{Name: "remote#boot-remote", Meta: cluster.EncodeMeta(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 8, WorkloadObservationSeq: 11,
	})})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("workload fetch did not start")
	}

	for _, node := range mesh.Members() {
		if node.NodeID != "remote" {
			continue
		}
		if node.WorkloadFreshness != "STALE" || node.WorkloadFreshnessReason != "SYNC_PENDING" {
			t.Fatalf("workload freshness/reason = %q/%q, want STALE/SYNC_PENDING", node.WorkloadFreshness, node.WorkloadFreshnessReason)
		}
		if node.WorkloadLastVerifiedUnixMs != base.UnixMilli() {
			t.Fatalf("last verified = %d, want %d", node.WorkloadLastVerifiedUnixMs, base.UnixMilli())
		}
		return
	}
	t.Fatal("remote node not found")
}

func TestMesh_DuplicateOrOlderWorkloadHintDoesNotRefreshSnapshot(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	now := base
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source:   &staticSource{s: cluster.NodeSummary{NodeID: "local", BootID: "boot-local", State: cluster.StateAlive}},
		TestFast: true, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive,
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 7, WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{ProcessID: "p1", Name: "worker"}},
	}))

	now = base.Add(45 * time.Second)
	for _, seq := range []uint64{10, 9} {
		mesh.NotifyUpdate(&memberlist.Node{Name: "remote#boot-remote", Meta: cluster.EncodeMeta(cluster.NodeSummary{
			NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive,
			WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 7, WorkloadObservationSeq: seq,
		})})
	}
	remote := findMember(t, mesh, "remote")
	if remote.WorkloadLastVerifiedUnixMs != base.UnixMilli() || remote.Processes[0].FreshnessUnixMs != base.UnixMilli() {
		t.Fatalf("duplicate/older hint refreshed snapshot: %+v", remote)
	}
	if remote.WorkloadFreshness != "STALE" || remote.WorkloadFreshnessReason != "EXPIRED" {
		t.Fatalf("freshness/reason = %q/%q, want STALE/EXPIRED", remote.WorkloadFreshness, remote.WorkloadFreshnessReason)
	}
	if age := mesh.WorkloadSyncStats().CacheMaxAgeSeconds; age != 45 {
		t.Fatalf("cache max age = %g, want 45", age)
	}
}

func TestMesh_WorkloadVerificationUsesObserverClock(t *testing.T) {
	observerNow := time.Unix(1_700_000_000, 0).UTC()
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source:   &staticSource{s: cluster.NodeSummary{NodeID: "local", BootID: "boot-local", State: cluster.StateAlive}},
		TestFast: true, Now: func() time.Time { return observerNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive,
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 7, WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{
			ProcessID: "p1", Name: "worker", FreshnessUnixMs: observerNow.Add(24 * time.Hour).UnixMilli(),
		}},
	}))
	remote := findMember(t, mesh, "remote")
	if remote.WorkloadLastVerifiedUnixMs != observerNow.UnixMilli() || remote.Processes[0].FreshnessUnixMs != observerNow.UnixMilli() {
		t.Fatalf("source clock leaked into observer freshness: %+v", remote)
	}
}

func TestMesh_LocalMetadataHeartbeatDoesNotRefreshWorkload(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	now := base.Add(45 * time.Second)
	source := &staticSource{s: cluster.NodeSummary{
		NodeID: "local", BootID: "boot-local", State: cluster.StateAlive,
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-local", WorkloadVersion: 1, WorkloadObservationSeq: 1,
		WorkloadLastVerifiedUnixMs: base.UnixMilli(), LastUpdatedUnixMs: now.UnixMilli(),
		Processes: []cluster.ProcessSummary{{ProcessID: "p1", Name: "last-good", FreshnessUnixMs: base.UnixMilli()}},
	}}
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source: source, TestFast: true, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	local := findMember(t, mesh, "local")
	if local.WorkloadFreshness != "STALE" || local.WorkloadFreshnessReason != "EXPIRED" {
		t.Fatalf("metadata heartbeat refreshed local workload: %+v", local)
	}
}

func TestMesh_StaleFetchedResponseDoesNotReplaceNewerSnapshot(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	calls := make(chan controlledWorkloadFetch, 2)
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source:   &staticSource{s: cluster.NodeSummary{NodeID: "local", BootID: "boot-local", State: cluster.StateAlive}},
		TestFast: true, Now: func() time.Time { return base }, WorkloadFetcher: controlledWorkloadFetcher{calls: calls},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 7, WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{ProcessID: "p1", Name: "old"}},
	}))
	mesh.NotifyUpdate(&memberlist.Node{Name: "remote#boot-remote", Meta: cluster.EncodeMeta(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 8, WorkloadObservationSeq: 11,
	})})
	call := waitWorkloadFetch(t, calls)
	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 8, WorkloadObservationSeq: 12,
		Processes: []cluster.ProcessSummary{{ProcessID: "p2", Name: "newer"}},
	}))
	call.response <- workloadFetchResult{snapshot: cluster.WorkloadSnapshot{
		NodeID: "remote", Epoch: "epoch-remote", Version: 8, ObservationSeq: 11,
		Processes: []cluster.ProcessSummary{{ProcessID: "p-stale", Name: "stale-response"}},
	}}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		remote := findMember(t, mesh, "remote")
		if len(remote.Processes) == 1 && remote.Processes[0].ProcessID == "p2" && mesh.WorkloadSyncStats().FetchDiscardedTotal == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stale fetch replaced newer snapshot: %+v", findMember(t, mesh, "remote"))
}

func TestMesh_EmptyFetchedSnapshotAtomicallyClearsProcesses(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source:   &staticSource{s: cluster.NodeSummary{NodeID: "local", BootID: "boot-local", State: cluster.StateAlive}},
		TestFast: true, Now: func() time.Time { return base },
		WorkloadFetcher: staticWorkloadFetcher{snapshot: cluster.WorkloadSnapshot{
			NodeID: "remote", Epoch: "epoch-remote", Version: 8, ObservationSeq: 11,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 7, WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{ProcessID: "p1", Name: "old"}},
	}))
	mesh.NotifyUpdate(&memberlist.Node{Name: "remote#boot-remote", Meta: cluster.EncodeMeta(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 8, WorkloadObservationSeq: 11,
	})})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		remote := findMember(t, mesh, "remote")
		if len(remote.Processes) == 0 && remote.WorkloadFreshness == "LIVE" && remote.WorkloadFreshnessReason == "CURRENT" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("empty snapshot was not applied atomically: %+v", findMember(t, mesh, "remote"))
}

func TestMesh_WorkloadFetchFailureRetainsSnapshotAndBacksOff(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	var nowUnixMs atomic.Int64
	nowUnixMs.Store(base.UnixMilli())
	calls := make(chan controlledWorkloadFetch, 4)
	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source:   &staticSource{s: cluster.NodeSummary{NodeID: "local", BootID: "boot-local", State: cluster.StateAlive}},
		TestFast: true, Now: func() time.Time { return time.UnixMilli(nowUnixMs.Load()) }, Logger: logger,
		WorkloadFetcher: controlledWorkloadFetcher{calls: calls},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 7, WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{ProcessID: "p1", Name: "last-good"}},
	}))
	update := func(seq uint64) {
		mesh.NotifyUpdate(&memberlist.Node{Name: "remote#boot-remote", Meta: cluster.EncodeMeta(cluster.NodeSummary{
			NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
			WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-remote", WorkloadVersion: 8, WorkloadObservationSeq: seq,
		})})
	}
	update(11)
	first := waitWorkloadFetch(t, calls)
	first.response <- workloadFetchResult{err: errors.New("credential=must-not-appear")}
	waitForWorkloadReason(t, mesh, "remote", "FETCH_FAILED")
	remote := findMember(t, mesh, "remote")
	if len(remote.Processes) != 1 || remote.Processes[0].ProcessID != "p1" {
		t.Fatalf("fetch error discarded last good snapshot: %+v", remote)
	}

	for seq := uint64(12); seq < 20; seq++ {
		update(seq)
	}
	select {
	case call := <-calls:
		call.response <- workloadFetchResult{err: errors.New("unexpected call during backoff")}
		t.Fatal("workload fetch ignored backoff")
	case <-time.After(100 * time.Millisecond):
	}

	nowUnixMs.Store(base.Add(time.Second).UnixMilli())
	update(20)
	second := waitWorkloadFetch(t, calls)
	second.response <- workloadFetchResult{err: errors.New("second injected failure")}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && mesh.WorkloadSyncStats().FetchErrorTotal < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if mesh.WorkloadSyncStats().FetchErrorTotal != 2 {
		t.Fatalf("second fetch failure was not recorded: %+v", mesh.WorkloadSyncStats())
	}

	nowUnixMs.Store(base.Add(2 * time.Second).UnixMilli())
	update(21)
	select {
	case call := <-calls:
		call.response <- workloadFetchResult{err: errors.New("unexpected call before exponential backoff elapsed")}
		t.Fatal("second workload retry did not use exponential backoff")
	case <-time.After(100 * time.Millisecond):
	}

	nowUnixMs.Store(base.Add(3 * time.Second).UnixMilli())
	update(22)
	third := waitWorkloadFetch(t, calls)
	third.response <- workloadFetchResult{snapshot: cluster.WorkloadSnapshot{
		NodeID: "remote", Epoch: "epoch-remote", Version: 8, ObservationSeq: 22,
		Processes: []cluster.ProcessSummary{{ProcessID: "p2", Name: "recovered"}},
	}}
	waitForWorkloadReason(t, mesh, "remote", "CURRENT")
	remote = findMember(t, mesh, "remote")
	if len(remote.Processes) != 1 || remote.Processes[0].ProcessID != "p2" {
		t.Fatalf("successful retry did not replace snapshot: %+v", remote)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := mesh.WorkloadSyncStats()
		gotLogs := logs.String()
		if stats.FetchErrorTotal == 2 && stats.FetchSuccessTotal == 1 && stats.FailedNodes == 0 &&
			strings.Contains(gotLogs, "workload snapshot synchronized") {
			if strings.Count(gotLogs, "workload snapshot fetch failed") != 1 {
				t.Fatalf("failure transition logged more than once: %s", gotLogs)
			}
			if strings.Contains(gotLogs, "must-not-appear") {
				t.Fatalf("raw fetch error leaked into logs: %s", gotLogs)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workload observability did not settle: stats=%+v logs=%s", mesh.WorkloadSyncStats(), logs.String())
}

func TestMesh_WorkloadEpochChangeFencesOldFetch(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	calls := make(chan controlledWorkloadFetch, 3)
	mesh, err := cluster.Start(cluster.Config{
		NodeID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		Source:   &staticSource{s: cluster.NodeSummary{NodeID: "local", BootID: "boot-local", State: cluster.StateAlive}},
		TestFast: true, Now: func() time.Time { return base }, WorkloadFetcher: controlledWorkloadFetcher{calls: calls},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mesh.Shutdown() })
	mesh.MergeForTest(cluster.EncodeState(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-old", WorkloadVersion: 7, WorkloadObservationSeq: 10,
		Processes: []cluster.ProcessSummary{{ProcessID: "p1", Name: "last-good"}},
	}))
	mesh.NotifyUpdate(&memberlist.Node{Name: "remote#boot-remote", Meta: cluster.EncodeMeta(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-old", WorkloadVersion: 8, WorkloadObservationSeq: 11,
	})})
	oldCall := waitWorkloadFetch(t, calls)
	mesh.NotifyUpdate(&memberlist.Node{Name: "remote#boot-remote", Meta: cluster.EncodeMeta(cluster.NodeSummary{
		NodeID: "remote", BootID: "boot-remote", State: cluster.StateAlive, RPCAddress: "127.0.0.1:18683",
		WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-new", WorkloadVersion: 1, WorkloadObservationSeq: 1,
	})})
	oldCall.response <- workloadFetchResult{snapshot: cluster.WorkloadSnapshot{
		NodeID: "remote", Epoch: "epoch-old", Version: 8, ObservationSeq: 11,
		Processes: []cluster.ProcessSummary{{ProcessID: "p-old-response", Name: "must-not-apply"}},
	}}
	newCall := waitWorkloadFetch(t, calls)
	if newCall.peer.WorkloadEpoch != "epoch-new" || newCall.peer.WorkloadVersion != 1 {
		t.Fatalf("retry target = %q/%d, want epoch-new/1", newCall.peer.WorkloadEpoch, newCall.peer.WorkloadVersion)
	}
	newCall.response <- workloadFetchResult{snapshot: cluster.WorkloadSnapshot{
		NodeID: "remote", Epoch: "epoch-new", Version: 1, ObservationSeq: 1,
		Processes: []cluster.ProcessSummary{{ProcessID: "p-new", Name: "new-epoch"}},
	}}
	waitForWorkloadReason(t, mesh, "remote", "CURRENT")
	remote := findMember(t, mesh, "remote")
	if len(remote.Processes) != 1 || remote.Processes[0].ProcessID != "p-new" {
		t.Fatalf("old epoch response crossed fence: %+v", remote)
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
	sawLeft := false
	for time.Now().Before(deadline) {
		if st := memberState(a, "nb"); st == cluster.StateLeft {
			sawLeft = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawLeft {
		t.Fatalf("want nb LEFT, got %q members=%+v", memberState(a, "nb"), a.Members())
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := memberState(a, "nb"); st == "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("want expired nb tombstone removed, got members=%+v", a.Members())
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

func findMember(t *testing.T, m *cluster.Mesh, id string) cluster.NodeSummary {
	t.Helper()
	for _, member := range m.Members() {
		if member.NodeID == id {
			return member
		}
	}
	t.Fatalf("member %q not found: %+v", id, m.Members())
	return cluster.NodeSummary{}
}

func waitWorkloadFetch(t *testing.T, calls <-chan controlledWorkloadFetch) controlledWorkloadFetch {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("workload fetch did not start")
		return controlledWorkloadFetch{}
	}
}

func waitForWorkloadReason(t *testing.T, m *cluster.Mesh, nodeID, reason string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if node := findMember(t, m, nodeID); node.WorkloadFreshnessReason == reason {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	node := findMember(t, m, nodeID)
	t.Fatalf("workload freshness reason = %q, want %q (node=%+v)", node.WorkloadFreshnessReason, reason, node)
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
