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
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func seedProcess(t *testing.T) (*store.Store, process.ProcessSpec) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	spec := process.ProcessSpec{ProcessID: "p1", Name: "web", Command: "/bin/true"}
	got, err := st.PutSpec(context.Background(), spec, 0, "t", "create")
	if err != nil {
		t.Fatal(err)
	}
	got.Command = "/bin/web"
	got, err = st.PutSpec(context.Background(), got, got.LatestRevision, "t", "update")
	if err != nil {
		t.Fatal(err)
	}
	return st, got
}

func seededEngine(t *testing.T) *backup.Engine {
	t.Helper()
	st, _ := seedProcess(t)
	dir := filepath.Join(t.TempDir(), "fs")
	return &backup.Engine{
		Store:     st,
		NodeID:    "n1",
		ClusterID: "c1",
		Sinks:     map[string]backup.Sink{"fs": backup.NewFSSink(dir)},
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		NewID:     func() (string, error) { return "snap-1", nil },
	}
}

func TestEngine_CreateListsAndGetsFS(t *testing.T) {
	ctx := context.Background()
	st, spec := seedProcess(t) // name=web, two revisions
	dir := filepath.Join(t.TempDir(), "fs")
	e := &backup.Engine{
		Store: st, NodeID: "n1", ClusterID: "c1",
		Sinks: map[string]backup.Sink{"fs": backup.NewFSSink(dir)},
		Now:   func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		NewID: func() (string, error) { return "snap-1", nil },
	}
	meta, err := e.Create(ctx, nil, "fs")
	if err != nil {
		t.Fatal(err)
	}
	if meta.SnapshotID != "snap-1" || meta.Sink != "fs" || meta.SHA256 == "" {
		t.Fatalf("%+v", meta)
	}
	if len(meta.ProcessIDs) != 1 || meta.RevisionRanges[0].MaxRevision < 1 {
		t.Fatalf("%+v", meta)
	}
	if e.LastSuccessUnix.Load() != 1_700_000_000 {
		t.Fatalf("metric %d", e.LastSuccessUnix.Load())
	}
	m2, payload, err := e.Get(ctx, "snap-1", "fs")
	if err != nil || m2.SHA256 != meta.SHA256 || len(payload) == 0 {
		t.Fatalf("%+v %v", m2, err)
	}
	snap, err := backup.Decode(payload)
	if err != nil || snap.Processes[0].ProcessID != spec.ProcessID {
		t.Fatalf("%+v %v", snap, err)
	}
	if len(snap.Processes[0].Revisions) != 2 || snap.Processes[0].MaxRevision != 2 {
		t.Fatalf("history %+v", snap.Processes[0])
	}
	listed, err := e.ListLocal(ctx)
	if err != nil || len(listed) != 1 || listed[0].SnapshotID != "snap-1" {
		t.Fatalf("list %+v %v", listed, err)
	}
}

func TestEngine_UnknownSinkInvalid(t *testing.T) {
	e := &backup.Engine{Sinks: map[string]backup.Sink{}}
	_, err := e.Create(context.Background(), nil, "tape")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}

func TestEngine_MissingProcessNotFound(t *testing.T) {
	e := seededEngine(t)
	_, err := e.Create(context.Background(), []string{"nope"}, "fs")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("err %v", err)
	}
}

func TestEngine_Disk95RejectsCreate(t *testing.T) {
	e := seededEngine(t)
	e.DiskPercent = func() float64 { return 95 }
	_, err := e.Create(context.Background(), nil, "fs")
	if !errcode.Is(err, errcode.DEGRADED) {
		t.Fatalf("err %v", err)
	}
	list, _ := e.ListLocal(context.Background())
	if len(list) != 0 {
		t.Fatalf("must not write index: %d", len(list))
	}
	listed, err := e.Sinks["fs"].List(context.Background())
	if err != nil || len(listed) != 0 {
		t.Fatalf("must not write file: %+v %v", listed, err)
	}
	if e.LastSuccessUnix.Load() != 0 {
		t.Fatalf("metric %d", e.LastSuccessUnix.Load())
	}
}

func TestEngine_DeleteRemovesFileAndIndex(t *testing.T) {
	e := seededEngine(t)
	meta, err := e.Create(context.Background(), nil, "fs")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(context.Background(), meta.SnapshotID, "fs"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Get(context.Background(), meta.SnapshotID, "fs"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("err %v", err)
	}
}

func TestEngine_EmptyLocalProcessListInvalid(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &backup.Engine{
		Store:     st,
		NodeID:    "n1",
		ClusterID: "c1",
		Sinks:     map[string]backup.Sink{"fs": backup.NewFSSink(t.TempDir())},
	}
	_, err = e.Create(context.Background(), nil, "fs")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "no processes to backup") {
		t.Fatalf("msg %v", err)
	}
}

func TestEngine_GetSHA256MismatchInvalid(t *testing.T) {
	e := seededEngine(t)
	meta, err := e.Create(context.Background(), nil, "fs")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta.Location, []byte(`{"format_version":1,"snapshot_id":"snap-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.Get(context.Background(), meta.SnapshotID, "fs")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}
