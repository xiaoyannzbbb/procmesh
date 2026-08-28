package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func testEngineWithProcess(t *testing.T) *backup.Engine {
	t.Helper()
	st, _ := seedProcess(t)
	root := t.TempDir()
	return &backup.Engine{
		Store:     st,
		NodeID:    "node-a",
		ClusterID: "c1",
		Sinks: map[string]backup.Sink{
			"fs":                   backup.NewFSSink(filepath.Join(root, "backup", "fs")),
			backup.ReplicaSinkName: backup.NewFSSink(filepath.Join(root, "backup", "replica")),
		},
		PeerStore: &backup.PeerStore{Root: root},
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
}

func testEngineWithProcessAndPeerPush(t *testing.T) *backup.Engine {
	t.Helper()
	e := testEngineWithProcess(t)
	e.ReplicationPush = backup.ReplicationPeerPushFunc(func(context.Context, backup.ReplicationPushRequest, []byte) error {
		return nil
	})
	return e
}

func TestEngine_CaptureReplicationSnapshot_IdempotentPerRunAndSource(t *testing.T) {
	eng := testEngineWithProcess(t) // 现有测试夹具：一个本地 spec
	id := backup.StableReplicationSnapshotID("run-1", "node-a")
	first, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: id,
	})
	if err != nil || first.SnapshotID != id || first.SHA256 == "" || first.Sink != "replica" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: id,
	})
	if err != nil || second.SHA256 != first.SHA256 || second.SnapshotID != first.SnapshotID {
		t.Fatalf("second=%+v", second)
	}
}

func TestEngine_CaptureReplicationSnapshot_FreezeOnceIgnoresLaterSpecChange(t *testing.T) {
	eng := testEngineWithProcess(t)
	id := backup.StableReplicationSnapshotID("run-1", eng.NodeID)
	ctx := context.Background()
	first, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := eng.Store.GetSpec(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	spec.Command = "/bin/changed"
	if _, err := eng.Store.PutSpec(ctx, spec, spec.LatestRevision, "t", "changed after capture"); err != nil {
		t.Fatal(err)
	}
	second, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: id,
	})
	if err != nil || second.SHA256 != first.SHA256 || second.SnapshotID != first.SnapshotID {
		t.Fatalf("second=%+v err=%v firstSHA=%s", second, err, first.SHA256)
	}
}

func TestEngine_ReplicateSnapshot_ReadsReplicaSink(t *testing.T) {
	eng := testEngineWithProcessAndPeerPush(t)
	cap, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		SnapshotID: backup.StableReplicationSnapshotID("run-1", eng.NodeID),
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := eng.ReplicateSnapshot(context.Background(), backup.ReplicationTaskRequest{
		RunID: "run-1", TaskID: "t1", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		TargetNodeID: "peer-b", SnapshotID: cap.SnapshotID, SHA256: cap.SHA256,
	})
	if err != nil || n <= 0 {
		t.Fatalf("bytes=%d err=%v", n, err)
	}
}

func TestEngine_CaptureReplicationSnapshot_RejectsForeignSource(t *testing.T) {
	eng := testEngineWithProcess(t)
	_, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: "other-node",
		SnapshotID: backup.StableReplicationSnapshotID("run-1", "other-node"),
	})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}

func TestEngine_CaptureReplicationSnapshot_Disk95Rejects(t *testing.T) {
	eng := testEngineWithProcess(t)
	eng.DiskPercent = func() float64 { return 95 }
	_, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		SnapshotID: backup.StableReplicationSnapshotID("run-1", eng.NodeID),
	})
	if !errcode.Is(err, errcode.DEGRADED) {
		t.Fatalf("err %v", err)
	}
	list, err := eng.ListLocal(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("must not write index: %+v %v", list, err)
	}
	matches, err := filepath.Glob(filepath.Join(eng.PeerStore.Root, "backup", "replica", "*", "*", "*.json"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("must not write replica file: %+v %v", matches, err)
	}
}

func TestEngine_CaptureReplicationSnapshot_WritesReplicaNamespacedPath(t *testing.T) {
	eng := testEngineWithProcess(t)
	id := backup.StableReplicationSnapshotID("run-1", eng.NodeID)
	meta, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.ToSlash(meta.Location), "/backup/replica/") {
		t.Fatalf("location %q is not under backup/replica", meta.Location)
	}
	want := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(meta.Location))), eng.ClusterID, eng.NodeID, id+".json")
	if meta.Location != want {
		t.Fatalf("location=%q want namespaced %q", meta.Location, want)
	}
	if _, err := os.Stat(meta.Location); err != nil {
		t.Fatalf("replica snapshot missing: %v", err)
	}
	fsMatches, err := filepath.Glob(filepath.Join(eng.PeerStore.Root, "backup", "fs", "*", "*", "*.json"))
	if err != nil || len(fsMatches) != 0 {
		t.Fatalf("must not write cluster FS profile: %+v %v", fsMatches, err)
	}
}

func TestEngine_CaptureReplicationSnapshot_KeepLastDeletesOlderReplica(t *testing.T) {
	eng := testEngineWithProcess(t)
	eng.RetentionPolicy = func(policyID string) (backup.Policy, bool) {
		return backup.Policy{PolicyID: policyID, Timezone: "UTC", RetentionKeepLast: 1}, true
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	eng.Now = func() time.Time { return now }

	oldID := backup.StableReplicationSnapshotID("run-old", eng.NodeID)
	old, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-old", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: oldID,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(old.Location)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.PeerStore.ReceiveWithMetadata(ctx, backup.ReceiveParams{
		SourceNodeID: eng.NodeID, ClusterID: eng.ClusterID, SnapshotID: old.SnapshotID,
		SHA256: old.SHA256, RunID: "run-old", TaskID: "t1", Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Hour)
	newID := backup.StableReplicationSnapshotID("run-new", eng.NodeID)
	newer, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-new", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: newID,
	})
	if err != nil {
		t.Fatal(err)
	}
	newPayload, err := os.ReadFile(newer.Location)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.PeerStore.ReceiveWithMetadata(ctx, backup.ReceiveParams{
		SourceNodeID: eng.NodeID, ClusterID: eng.ClusterID, SnapshotID: newer.SnapshotID,
		SHA256: newer.SHA256, RunID: "run-new", TaskID: "t1", Payload: newPayload,
	}); err != nil {
		t.Fatal(err)
	}

	replica := replicaMetas(t, eng)
	if len(replica) != 1 || replica[0].SnapshotID != newer.SnapshotID {
		t.Fatalf("source replica after keep_last=1: %+v want only %s", replica, newer.SnapshotID)
	}
	if _, err := os.Stat(old.Location); !os.IsNotExist(err) {
		t.Fatalf("old replica object still present: %v", err)
	}
	peerOld := filepath.Join(eng.PeerStore.Root, "backup", "peer", eng.NodeID, eng.ClusterID, old.SnapshotID+".json")
	if _, err := os.Stat(peerOld); err != nil {
		t.Fatalf("source retention deleted peer copy: %v", err)
	}

	listed, err := eng.PeerStore.ListSnapshots(ctx, eng.NodeID, eng.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	peerSnaps := make([]backup.RetentionSnapshot, 0, len(listed))
	for _, item := range listed {
		peerSnaps = append(peerSnaps, backup.RetentionSnapshot{
			SnapshotID: item.SnapshotID, PolicyID: "rp-1", SourceNodeID: item.SourceNodeID,
			CreatedAt: item.CreatedAt, Bytes: item.Bytes, Status: "SUCCEEDED",
		})
	}
	planned, err := backup.PlanRetention(now, backup.Policy{PolicyID: "rp-1", Timezone: "UTC", RetentionKeepLast: 1}, peerSnaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 1 || planned[0].SnapshotID != old.SnapshotID {
		t.Fatalf("peer planner=%+v want old %s (source already gone is irrelevant)", planned, old.SnapshotID)
	}
}

func TestEngine_CaptureReplicationSnapshot_DoesNotDeleteClusterBackup(t *testing.T) {
	eng := testEngineWithProcess(t)
	eng.RetentionPolicy = func(policyID string) (backup.Policy, bool) {
		return backup.Policy{PolicyID: policyID, Timezone: "UTC", RetentionKeepLast: 1}, true
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	eng.Now = func() time.Time { return now }
	eng.NewID = func() (string, error) { return "fs-keep", nil }
	fsMeta, err := eng.CreateCluster(ctx, backup.ClusterCreateOpts{
		RunID: "backup-run", TaskID: "task-1", PolicyID: "rp-1", ClusterID: eng.ClusterID, NodeID: eng.NodeID, Sink: "fs",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		SnapshotID: backup.StableReplicationSnapshotID("run-1", eng.NodeID),
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-2", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		SnapshotID: backup.StableReplicationSnapshotID("run-2", eng.NodeID),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fsMeta.Location); err != nil {
		t.Fatalf("cluster FS backup deleted: %v", err)
	}
	got, err := eng.Store.GetBackup(ctx, fsMeta.SnapshotID)
	if err != nil || got.Sink != "fs" {
		t.Fatalf("cluster backup index: %+v err=%v", got, err)
	}
}

func TestEngine_CaptureReplicationSnapshot_PreservesProtectedReplica(t *testing.T) {
	eng := testEngineWithProcess(t)
	eng.RetentionPolicy = func(policyID string) (backup.Policy, bool) {
		return backup.Policy{PolicyID: policyID, Timezone: "UTC", RetentionKeepLast: 1}, true
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	eng.Now = func() time.Time { return now }
	oldID := backup.StableReplicationSnapshotID("run-old", eng.NodeID)
	if _, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-old", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: oldID,
	}); err != nil {
		t.Fatal(err)
	}
	release := eng.ProtectSnapshot(oldID)
	now = now.Add(time.Hour)
	if _, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-new", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		SnapshotID: backup.StableReplicationSnapshotID("run-new", eng.NodeID),
	}); err != nil {
		t.Fatal(err)
	}
	ids := replicaIDs(t, eng)
	if len(ids) != 2 || ids[oldID] == false {
		t.Fatalf("protected replica deleted: %v", ids)
	}
	release()
	if _, err := eng.ApplyRetention(ctx, backup.Policy{PolicyID: "rp-1", Sink: backup.ReplicaSinkName, Timezone: "UTC", RetentionKeepLast: 1}); err != nil {
		t.Fatal(err)
	}
	ids = replicaIDs(t, eng)
	if len(ids) != 1 || ids[oldID] {
		t.Fatalf("released replica still kept: %v", ids)
	}
}

func TestEngine_CaptureReplicationSnapshot_MaxBytesCountsReplicaOnly(t *testing.T) {
	eng := testEngineWithProcess(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	eng.Now = func() time.Time { return now }
	eng.NewID = func() (string, error) { return "fs-huge", nil }
	fsMeta, err := eng.CreateCluster(ctx, backup.ClusterCreateOpts{
		RunID: "backup-run", TaskID: "task-1", PolicyID: "rp-1", ClusterID: eng.ClusterID, NodeID: eng.NodeID, Sink: "fs",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	old, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-old", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		SnapshotID: backup.StableReplicationSnapshotID("run-old", eng.NodeID),
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	newer, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-new", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		SnapshotID: backup.StableReplicationSnapshotID("run-new", eng.NodeID),
	})
	if err != nil {
		t.Fatal(err)
	}
	limit := old.Bytes + newer.Bytes - 1
	if limit <= newer.Bytes {
		t.Fatalf("replica bytes too small to distinguish: old=%d new=%d", old.Bytes, newer.Bytes)
	}
	eng.RetentionPolicy = func(policyID string) (backup.Policy, bool) {
		return backup.Policy{PolicyID: policyID, Timezone: "UTC", RetentionMaxBytes: limit}, true
	}
	if _, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-new", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: newer.SnapshotID,
	}); err != nil {
		t.Fatal(err)
	}
	ids := replicaIDs(t, eng)
	if len(ids) != 1 || !ids[newer.SnapshotID] {
		t.Fatalf("max_bytes replica=%v want only %s", ids, newer.SnapshotID)
	}
	if _, err := os.Stat(fsMeta.Location); err != nil {
		t.Fatalf("cluster FS counted toward replica max_bytes: %v", err)
	}
}

func TestEngine_CaptureReplicationSnapshot_KeepsLastRemainingReplica(t *testing.T) {
	eng := testEngineWithProcess(t)
	eng.RetentionPolicy = func(policyID string) (backup.Policy, bool) {
		return backup.Policy{PolicyID: policyID, Timezone: "UTC", RetentionKeepDays: 1}, true
	}
	ctx := context.Background()
	created := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	eng.Now = func() time.Time { return created }
	id := backup.StableReplicationSnapshotID("run-old", eng.NodeID)
	if _, err := eng.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-old", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: id,
	}); err != nil {
		t.Fatal(err)
	}
	eng.Now = func() time.Time { return time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC) }
	if _, err := eng.ApplyRetention(ctx, backup.Policy{PolicyID: "rp-1", Sink: backup.ReplicaSinkName, Timezone: "UTC", RetentionKeepDays: 1}); err != nil {
		t.Fatal(err)
	}
	ids := replicaIDs(t, eng)
	if len(ids) != 1 || !ids[id] {
		t.Fatalf("last remaining replica deleted: %v", ids)
	}
}

func replicaMetas(t *testing.T, eng *backup.Engine) []backup.Meta {
	t.Helper()
	listed, err := eng.ListLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]backup.Meta, 0, len(listed))
	for _, meta := range listed {
		if meta.Sink == backup.ReplicaSinkName {
			out = append(out, meta)
		}
	}
	return out
}

func replicaIDs(t *testing.T, eng *backup.Engine) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, meta := range replicaMetas(t, eng) {
		ids[meta.SnapshotID] = true
	}
	return ids
}
