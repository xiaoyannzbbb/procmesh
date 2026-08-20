package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestFSSink_PutGetListDelete_Mode0600(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fs")
	s := backup.NewFSSink(dir)
	payload := []byte(`{"format_version":1,"snapshot_id":"s1"}`)
	loc, err := s.Put(context.Background(), "s1", payload)
	if err != nil {
		t.Fatal(err)
	}
	if loc != filepath.Join(dir, "s1.json") {
		t.Fatal(loc)
	}
	st, err := os.Stat(loc)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st.Mode(), err)
	}
	got, err := s.Get(context.Background(), "s1")
	if err != nil || string(got) != string(payload) {
		t.Fatalf("%s %v", got, err)
	}
	list, err := s.List(context.Background())
	if err != nil || len(list) != 1 || list[0].SnapshotID != "s1" {
		t.Fatalf("%+v %v", list, err)
	}
	if err := s.Delete(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "s1"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("err %v", err)
	}
}

func TestFSSink_RejectsPathEscape(t *testing.T) {
	s := backup.NewFSSink(t.TempDir())
	if _, err := s.Put(context.Background(), "../x", []byte("{}")); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}

func TestFSSink_Name(t *testing.T) {
	if backup.NewFSSink("/tmp").Name() != "fs" {
		t.Fatal("name")
	}
}

func TestFSSink_GetDeleteMissing_NOT_FOUND(t *testing.T) {
	s := backup.NewFSSink(t.TempDir())
	if _, err := s.Get(context.Background(), "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("get err %v", err)
	}
	if err := s.Delete(context.Background(), "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("delete err %v", err)
	}
}

func TestFSSink_ListEmptyAndSkipsTmp(t *testing.T) {
	dir := t.TempDir()
	s := backup.NewFSSink(filepath.Join(dir, "missing"))
	list, err := s.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("%+v %v", list, err)
	}

	root := filepath.Join(dir, "fs")
	s = backup.NewFSSink(root)
	if _, err := s.Put(context.Background(), "keep", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep.json.tmp"), []byte(`tmp`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(`x`), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err = s.List(context.Background())
	if err != nil || len(list) != 1 || list[0].SnapshotID != "keep" || list[0].Location != filepath.Join(root, "keep.json") {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestFSSink_InvalidIDOnGetDelete(t *testing.T) {
	s := backup.NewFSSink(t.TempDir())
	if _, err := s.Get(context.Background(), "../x"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("get err %v", err)
	}
	if err := s.Delete(context.Background(), "a/b"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("delete err %v", err)
	}
}

func TestFSSink_CanceledContext(t *testing.T) {
	s := backup.NewFSSink(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, "s1", []byte(`{}`)); err == nil {
		t.Fatal("put expected canceled")
	}
	if _, err := s.Get(ctx, "s1"); err == nil {
		t.Fatal("get expected canceled")
	}
	if _, err := s.List(ctx); err == nil {
		t.Fatal("list expected canceled")
	}
	if err := s.Delete(ctx, "s1"); err == nil {
		t.Fatal("delete expected canceled")
	}
}

func TestFSSink_ClusterDeleteIsPolicyScoped(t *testing.T) {
	ctx := context.Background()
	s := backup.NewFSSink(t.TempDir())
	created := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	payload, _, err := backup.Encode(backup.Snapshot{FormatVersion: 1, SnapshotID: "snap-a", ClusterID: "cluster-a", NodeID: "node-a", PolicyID: "policy-a", CreatedAt: created})
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.PutCluster(ctx, "cluster-a", "policy-a", "node-a", "snap-a", payload)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListCluster(ctx, "cluster-a", "policy-a")
	if err != nil || len(listed) != 1 || listed[0].NodeID != "node-a" || listed[0].PolicyID != "policy-a" || listed[0].Bytes != int64(len(payload)) || !listed[0].CreatedAt.Equal(created) {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	if err := s.DeleteCluster(ctx, "cluster-a", "wrong-policy", "node-a", "snap-a"); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("wrong policy delete err=%v", err)
	}
	if _, err := os.Stat(location); err != nil {
		t.Fatalf("wrong policy removed snapshot: %v", err)
	}
	if err := s.DeleteCluster(ctx, "cluster-a", "policy-a", "node-a", "snap-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(location); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists: %v", err)
	}
}

func TestFSSink_ClusterOperationsRejectNamespaceEscape(t *testing.T) {
	s := backup.NewFSSink(t.TempDir())
	for _, ids := range [][3]string{{"../cluster", "policy", "node"}, {"cluster", "../policy", "node"}, {"cluster", "policy", "../node"}, {".", "policy", "node"}, {"cluster", "policy", ".."}} {
		if _, err := s.PutCluster(context.Background(), ids[0], ids[1], ids[2], "snap", []byte(`{}`)); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("ids=%v err=%v", ids, err)
		}
	}
}
