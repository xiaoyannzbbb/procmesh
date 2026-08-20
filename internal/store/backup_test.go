package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestBackupIndex_PutGetListDelete(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	rec := store.BackupRecord{
		SnapshotID: "s1", ClusterID: "c", NodeID: "n",
		CreatedAt:  time.Unix(1_700_000_000, 0).UTC(),
		ProcessIDs: []string{"p1"}, SHA256: "abc", Sink: "fs",
		DestinationProfile: "archive",
		Location:           "/data/backup/fs/s1.json",
		RevisionRangesJSON: `[{"process_id":"p1","min_revision":1,"max_revision":2}]`,
	}
	if err := s.PutBackup(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBackup(ctx, "s1")
	if err != nil || got.Sink != "fs" || got.SHA256 != "abc" || got.DestinationProfile != "archive" {
		t.Fatalf("%+v %v", got, err)
	}
	list, err := s.ListBackups(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%d %v", len(list), err)
	}
	if err := s.DeleteBackup(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBackup(ctx, "s1"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("err %v", err)
	}
}

func TestBackupIndex_MissingIsNotFound(t *testing.T) {
	s, _ := store.Open(filepath.Join(t.TempDir(), "store.db"))
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.GetBackup(context.Background(), "nope"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("err %v", err)
	}
}
