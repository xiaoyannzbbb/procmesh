package backup_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestPeerStore_ReceiveWritesFile0600AndNeverNeedsApplier(t *testing.T) {
	root := t.TempDir()
	p := &backup.PeerStore{Root: root}
	snap := backup.Snapshot{FormatVersion: 1, SnapshotID: "s1", ClusterID: "c", NodeID: "src",
		CreatedAt: time.Unix(1, 0).UTC(),
		Processes: []backup.ProcessDump{{ProcessID: "p-remote", Name: "other", MaxRevision: 1,
			Revisions: []backup.RevisionDump{{Revision: 1, Spec: json.RawMessage(`{"Name":"other","ProcessID":"p-remote"}`)}}}}}
	payload, _, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := p.Receive(context.Background(), "src", payload)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "backup", "peer", "src", "s1.json")
	if meta.Location != want {
		t.Fatal(meta.Location)
	}
	st, err := os.Stat(want)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestPeerStore_RejectsPathEscape(t *testing.T) {
	p := &backup.PeerStore{Root: t.TempDir()}
	snap := backup.Snapshot{FormatVersion: 1, SnapshotID: "s1", ClusterID: "c", NodeID: "src",
		CreatedAt: time.Unix(1, 0).UTC()}
	payload, _, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Receive(context.Background(), "../x", payload); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("source err %v", err)
	}
	if _, err := p.Get(context.Background(), "src/../x", "s1"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("get source err %v", err)
	}
	if _, err := p.Get(context.Background(), "src", "../s1"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("get id err %v", err)
	}
	if err := p.Delete(context.Background(), "../x", "s1"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("delete source err %v", err)
	}
	if _, err := p.List(context.Background(), "a/b"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("list source err %v", err)
	}
}

func TestPeerStore_ReceiveRejectsDotAndDotDotSource(t *testing.T) {
	root := t.TempDir()
	p := &backup.PeerStore{Root: root}
	snap := backup.Snapshot{FormatVersion: 1, SnapshotID: "s1", ClusterID: "c", NodeID: "src",
		CreatedAt: time.Unix(1, 0).UTC()}
	payload, _, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"..", "."} {
		if _, err := p.Receive(context.Background(), src, payload); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("source %q err %v", src, err)
		}
	}
	escaped := filepath.Join(root, "backup", "s1.json")
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("must not write %s: %v", escaped, err)
	}
}

func TestPeerStore_GetListDelete(t *testing.T) {
	root := t.TempDir()
	p := &backup.PeerStore{Root: root}
	snap := backup.Snapshot{FormatVersion: 1, SnapshotID: "s1", ClusterID: "c", NodeID: "src",
		CreatedAt: time.Unix(1, 0).UTC()}
	payload, _, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Receive(context.Background(), "src", payload); err != nil {
		t.Fatal(err)
	}
	got, err := p.Get(context.Background(), "src", "s1")
	if err != nil || string(got) != string(payload) {
		t.Fatalf("%s %v", got, err)
	}
	listed, err := p.List(context.Background(), "src")
	if err != nil || len(listed) != 1 || listed[0].SnapshotID != "s1" {
		t.Fatalf("%+v %v", listed, err)
	}
	if listed[0].Location != filepath.Join(root, "backup", "peer", "src", "s1.json") {
		t.Fatalf("loc %s", listed[0].Location)
	}
	if err := p.Delete(context.Background(), "src", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(context.Background(), "src", "s1"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("err %v", err)
	}
	if err := p.Delete(context.Background(), "src", "s1"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("delete missing %v", err)
	}
}
