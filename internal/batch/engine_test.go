package batch_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

type stubExpand struct {
	targets []batch.Target
	err     error
}

func (s stubExpand) Expand(context.Context, batch.Selector, batch.Type) ([]batch.Target, error) {
	return s.targets, s.err
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestEngine_CreateRejectsEmptySelectorAndZeroTargets(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e := &batch.Engine{DB: st, Expand: stubExpand{}, NewID: func() string { return "id" }, SourceAgent: "n1"}
	_, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{}, "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty selector: %v", err)
	}
	e.Expand = stubExpand{targets: nil}
	_, err = e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p1"}}, "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("zero targets: %v", err)
	}
	if list, _ := e.List(ctx, 10); len(list) != 0 {
		t.Fatalf("must not insert: %+v", list)
	}
}

func TestEngine_CreateGetListExport(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-1"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p1", ProcessName: "nginx"}}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	b, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p1"}}, "")
	if err != nil || b.BatchID != "b1" || b.Status != batch.StatusPending || len(b.Targets) != 1 || b.Targets[0].OperationID != "op-1" {
		t.Fatalf("%+v %v", b, err)
	}
	got, err := e.Get(ctx, "b1")
	if err != nil || got.Targets[0].ProcessName != "nginx" {
		t.Fatalf("%+v %v", got, err)
	}
	raw, ct, name, err := e.Export(ctx, "b1", "csv")
	if err != nil || ct != "text/csv" || !strings.Contains(string(raw), "op-1") || name == "" {
		t.Fatalf("%s %s %s %v", raw, ct, name, err)
	}
}

func TestEngine_CreateRejectsEmptyOperatorAndIllegalType(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p1"}}},
		NewID:  func() string { return "id" },
	}
	_, err := e.Create(ctx, "", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p1"}}, "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty operator: %v", err)
	}
	_, err = e.Create(ctx, "admin", batch.Type("NOPE"), batch.Selector{ProcessIDs: []string{"p1"}}, "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("illegal type: %v", err)
	}
}

func TestEngine_CreateRejectsZeroValueTargetsOnly(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{}}},
		NewID:  func() string { return "id" },
	}
	_, err := e.Create(ctx, "admin", batch.TypeStart, batch.Selector{ProcessIDs: []string{"p1"}}, "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("zero-value targets: %v", err)
	}
	if list, _ := e.List(ctx, 10); len(list) != 0 {
		t.Fatalf("must not insert: %+v", list)
	}
}

func TestEngine_ListClampsLimitAndOmitsTargets(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op1", "b2", "op2"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p1", ProcessName: "a"}}},
		NewID:  func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	if _, err := e.Create(ctx, "admin", batch.TypeStop, batch.Selector{ProcessIDs: []string{"p1"}}, ""); err != nil {
		t.Fatal(err)
	}
	e.Expand = stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p2", ProcessName: "b"}}}
	if _, err := e.Create(ctx, "admin", batch.TypeStop, batch.Selector{ProcessIDs: []string{"p2"}}, ""); err != nil {
		t.Fatal(err)
	}
	list, err := e.List(ctx, 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("limit 0 default: %+v %v", list, err)
	}
	for _, b := range list {
		if len(b.Targets) != 0 {
			t.Fatalf("list must omit targets: %+v", b)
		}
	}
	list, err = e.List(ctx, 1)
	if err != nil || len(list) != 1 {
		t.Fatalf("limit 1: %+v %v", list, err)
	}
}

func TestEngine_ExportJSONAndInvalidFormat(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-1"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p1", ProcessName: "nginx"}}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	if _, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p1"}}, ""); err != nil {
		t.Fatal(err)
	}
	raw, ct, name, err := e.Export(ctx, "b1", "json")
	if err != nil || ct != "application/json" || !strings.Contains(string(raw), "b1") || name == "" {
		t.Fatalf("%s %s %s %v", raw, ct, name, err)
	}
	raw, ct, name, err = e.Export(ctx, "b1", "")
	if err != nil || ct != "application/json" || name == "" {
		t.Fatalf("default json: %s %s %s %v", raw, ct, name, err)
	}
	_, _, _, err = e.Export(ctx, "b1", "xml")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID, got %v", err)
	}
}
