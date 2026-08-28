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
