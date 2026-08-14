package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"

	_ "modernc.org/sqlite"
)

func TestOpen_CreatesFileAndStableNodeID(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id1, err := s.GetOrCreateNodeID(ctx)
	if err != nil || id1 == "" {
		t.Fatalf("id1 %q err %v", id1, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	id2, err := s2.GetOrCreateNodeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("node_id changed: %s vs %s", id1, id2)
	}
	boot1, err := s2.RotateBootID(ctx)
	if err != nil || boot1 == "" {
		t.Fatalf("boot %q err %v", boot1, err)
	}
	boot2, err := s2.RotateBootID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if boot1 == boot2 {
		t.Fatal("boot_id must change every rotate")
	}
}

func TestIntegrityCheck_OKOnFreshDB(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.IntegrityCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSetBootID_Persists(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SetBootID(ctx, "boot-from-os"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBootID(ctx)
	if err != nil || got != "boot-from-os" {
		t.Fatalf("got %q err %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err = s2.GetBootID(ctx)
	if err != nil || got != "boot-from-os" {
		t.Fatalf("reopen got %q err %v", got, err)
	}
}

func TestSetNodeID_OverwritesAndReadable(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	orig, err := s.GetOrCreateNodeID(ctx)
	if err != nil || orig == "" {
		t.Fatalf("orig %q err %v", orig, err)
	}
	const next = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if err := s.SetNodeID(ctx, next); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOrCreateNodeID(ctx)
	if err != nil || got != next {
		t.Fatalf("got %q want %q err %v", got, next, err)
	}
	if got == orig {
		t.Fatal("SetNodeID must overwrite existing node_id")
	}
}

func TestMeta_BootAndClusterPersist(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	boot, err := s.GetBootID(ctx)
	if err != nil || boot != "" {
		t.Fatalf("empty boot_id want \"\" got %q err %v", boot, err)
	}
	cluster, err := s.GetClusterID(ctx)
	if err != nil || cluster != "" {
		t.Fatalf("empty cluster_id want \"\" got %q err %v", cluster, err)
	}

	boot, err = s.RotateBootID(ctx)
	if err != nil || boot == "" {
		t.Fatalf("rotate boot %q err %v", boot, err)
	}
	gotBoot, err := s.GetBootID(ctx)
	if err != nil || gotBoot != boot {
		t.Fatalf("get boot %q want %q err %v", gotBoot, boot, err)
	}
	if err := s.SetClusterID(ctx, "cluster-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	gotBoot, err = s2.GetBootID(ctx)
	if err != nil || gotBoot != boot {
		t.Fatalf("reopen boot %q want %q err %v", gotBoot, boot, err)
	}
	gotCluster, err := s2.GetClusterID(ctx)
	if err != nil || gotCluster != "cluster-1" {
		t.Fatalf("reopen cluster %q err %v", gotCluster, err)
	}
}

func TestOpen_CreatesFileAndStubTables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	want := []string{
		"local_meta",
		"process_specs",
		"process_instances",
		"config_revisions",
		"operation_journal",
		"audit_events",
	}
	for _, name := range want {
		var got string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
		if err != nil || got != name {
			t.Fatalf("missing table %s: %q %v", name, got, err)
		}
	}

	var ver string
	err = db.QueryRow(`SELECT v FROM local_meta WHERE k='schema_version'`).Scan(&ver)
	if err != nil || ver != "1" {
		t.Fatalf("schema_version=%q err %v", ver, err)
	}
}

func TestOpen_ErrorWhenParentMissing(t *testing.T) {
	_, err := store.Open(filepath.Join(t.TempDir(), "missing", "store.db"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOpen_ErrorWhenPathIsDirectory(t *testing.T) {
	_, err := store.Open(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestClose_NilSafe(t *testing.T) {
	var s *store.Store
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStore_MethodsErrorAfterClose(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.IntegrityCheck(ctx); !errcode.Is(err, errcode.DEGRADED) {
		t.Fatalf("integrity: %v", err)
	}
	if _, err := s.GetOrCreateNodeID(ctx); err == nil {
		t.Fatal("GetOrCreateNodeID")
	}
	if _, err := s.RotateBootID(ctx); err == nil {
		t.Fatal("RotateBootID")
	}
	if _, err := s.GetBootID(ctx); err == nil {
		t.Fatal("GetBootID")
	}
	if err := s.SetClusterID(ctx, "x"); err == nil {
		t.Fatal("SetClusterID")
	}
	if _, err := s.GetClusterID(ctx); err == nil {
		t.Fatal("GetClusterID")
	}
}
