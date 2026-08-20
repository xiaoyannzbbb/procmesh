package backup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/store"
)

func TestEngine_ReplicateSnapshotProtectsFrozenPayloadDuringPush(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sink := NewFSSink(filepath.Join(t.TempDir(), "snapshots"))
	payload, sha, err := Encode(Snapshot{FormatVersion: 1, SnapshotID: "snapshot-protected", ClusterID: "cluster", NodeID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	location, err := sink.Put(ctx, "snapshot-protected", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutBackup(ctx, store.BackupRecord{SnapshotID: "snapshot-protected", ClusterID: "cluster", NodeID: "source", CreatedAt: time.Now(), SHA256: sha, Bytes: int64(len(payload)), Sink: "fs", Location: location}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Store: st, NodeID: "source", ClusterID: "cluster", Sinks: map[string]Sink{"fs": sink}}
	engine.ReplicationPush = ReplicationPeerPushFunc(func(_ context.Context, req ReplicationPushRequest, _ []byte) error {
		if !engine.retentionActive(req.SnapshotID) {
			t.Fatal("snapshot was not protected during replication push")
		}
		return nil
	})

	if _, err := engine.ReplicateSnapshot(ctx, ReplicationTaskRequest{RunID: "run", TaskID: "task", SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot-protected", SHA256: sha}); err != nil {
		t.Fatal(err)
	}
	if engine.retentionActive("snapshot-protected") {
		t.Fatal("snapshot protection was not released after replication push")
	}
}
