package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
