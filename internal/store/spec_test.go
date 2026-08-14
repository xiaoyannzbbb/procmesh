package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPutSpec_CASConflict(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "nginx", Command: "/usr/sbin/nginx", Instances: 1}
	got, err := s.PutSpec(ctx, spec, 0, "admin", "create")
	if err != nil || got.LatestRevision != 1 {
		t.Fatalf("create: %+v %v", got, err)
	}
	spec.Command = "/bin/nginx"
	if _, err := s.PutSpec(ctx, spec, 1, "admin", "ok"); err != nil {
		t.Fatal(err)
	}
	spec.Command = "/opt/nginx"
	_, err = s.PutSpec(ctx, spec, 1, "admin", "stale")
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT got %v", err)
	}
	cur, err := s.GetSpec(ctx, "p1")
	if err != nil || cur.Command != "/bin/nginx" || cur.LatestRevision != 2 {
		t.Fatalf("lost update: %+v %v", cur, err)
	}
}

func TestPutSpec_ConcurrentCASNoBusy(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "t", ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sp := spec
			sp.Command = fmt.Sprintf("v-%d", i)
			_, err := s.PutSpec(ctx, sp, 1, "t", "")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	var conflicts, oks int
	for err := range errs {
		if err == nil {
			oks++
			continue
		}
		if errcode.Is(err, errcode.CONFLICT) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected %v", err)
	}
	if oks != 1 || conflicts != 1 {
		t.Fatalf("oks=%d conflicts=%d", oks, conflicts)
	}
}

func TestRollbackSpec_CreatesNewRevision(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	spec.Command = "v2"
	if _, err := s.PutSpec(ctx, spec, 1, "a", ""); err != nil {
		t.Fatal(err)
	}
	rolled, err := s.RollbackSpec(ctx, "p1", 1, 2, "a", "undo")
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Command != "v1" || rolled.LatestRevision != 3 {
		t.Fatalf("got %+v", rolled)
	}
	revs, err := s.ListRevisions(ctx, "p1")
	if err != nil || len(revs) != 3 {
		t.Fatalf("revs=%d err=%v", len(revs), err)
	}
}

func TestPutSpec_CreateRejectsDuplicateName(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "nginx", Command: "/bin/nginx", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	spec.ProcessID = "p2"
	_, err := s.PutSpec(ctx, spec, 0, "a", "")
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT got %v", err)
	}
}

func TestPutSpec_RejectsInvalid(t *testing.T) {
	s := openStore(t)
	_, err := s.PutSpec(context.Background(), process.ProcessSpec{ProcessID: "p1", Command: "/bin/true"}, 0, "a", "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}

func TestGetSpec_NotFound(t *testing.T) {
	s := openStore(t)
	_, err := s.GetSpec(context.Background(), "missing")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND got %v", err)
	}
}

func TestGetSpecByNameAndList(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	a := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	b := process.ProcessSpec{ProcessID: "p2", Name: "web", Command: "v2", Instances: 1}
	if _, err := s.PutSpec(ctx, a, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSpec(ctx, b, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSpecByName(ctx, "web")
	if err != nil || got.ProcessID != "p2" || got.LatestRevision != 1 {
		t.Fatalf("by name: %+v %v", got, err)
	}
	list, err := s.ListSpecs(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list=%d err=%v", len(list), err)
	}
	if list[0].Name != "api" || list[1].Name != "web" {
		t.Fatalf("order: %+v", list)
	}
}

func TestDeleteSpec_CAS(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSpec(ctx, "p1", 0); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := s.DeleteSpec(ctx, "p1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSpec(ctx, "p1"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteSpec(ctx, "p1", 1); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("missing delete: %v", err)
	}
}

func TestRollbackSpec_StaleLatestConflicts(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	spec.Command = "v2"
	if _, err := s.PutSpec(ctx, spec, 1, "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RollbackSpec(ctx, "p1", 1, 1, "a", "stale"); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT got %v", err)
	}
}

func TestPutSpec_UpdateMissingNotFound(t *testing.T) {
	spec := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	_, err := openStore(t).PutSpec(context.Background(), spec, 1, "a", "")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND got %v", err)
	}
}

func TestPutSpec_UpdateNameConflict(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	a := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	b := process.ProcessSpec{ProcessID: "p2", Name: "web", Command: "v2", Instances: 1}
	if _, err := s.PutSpec(ctx, a, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSpec(ctx, b, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	b.Name = "api"
	_, err := s.PutSpec(ctx, b, 1, "a", "")
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT got %v", err)
	}
}

func TestPutSpec_RecordsDiffAndPersists(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	spec := process.ProcessSpec{
		ProcessID:   "p1",
		Name:        "api",
		Command:     "v1",
		Instances:   1,
		Args:        []string{"a"},
		Environment: map[string]string{"K": "1"},
	}
	if _, err := s.PutSpec(ctx, spec, 0, "admin", "create"); err != nil {
		t.Fatal(err)
	}
	spec.Args = []string{"b"}
	spec.Environment = map[string]string{"K": "2", "Z": "9"}
	if _, err := s.PutSpec(ctx, spec, 1, "admin", "tune"); err != nil {
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
	got, err := s2.GetSpec(ctx, "p1")
	if err != nil || got.Command != "v1" || got.LatestRevision != 2 || got.Args[0] != "b" || got.Environment["Z"] != "9" {
		t.Fatalf("reopen: %+v %v", got, err)
	}
	revs, err := s2.ListRevisions(ctx, "p1")
	if err != nil || len(revs) != 2 {
		t.Fatalf("revs=%d err=%v", len(revs), err)
	}
	if revs[1].Operator != "admin" || revs[1].Comment != "tune" || revs[1].Revision != 2 {
		t.Fatalf("meta: %+v", revs[1])
	}
	if !strings.Contains(revs[1].Diff, "-args a") || !strings.Contains(revs[1].Diff, "+env") {
		t.Fatalf("diff: %q", revs[1].Diff)
	}
	payload, err := s2.GetRevisionSpecJSON(ctx, "p1", revs[1].Revision)
	if err != nil || len(payload) == 0 || revs[1].Timestamp.IsZero() {
		t.Fatalf("payload/ts: %+v %v", revs[1], err)
	}
}

func TestListSpecsAndRevisions_Empty(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	list, err := s.ListSpecs(ctx)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	revs, err := s.ListRevisions(ctx, "missing")
	if err != nil || revs == nil || len(revs) != 0 {
		t.Fatalf("revs=%v err=%v", revs, err)
	}
	if _, err := s.GetSpecByName(ctx, "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND got %v", err)
	}
}

func TestRollbackSpec_MissingAndNameConflict(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	a := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, a, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RollbackSpec(ctx, "p1", 9, 1, "a", ""); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("missing rev: %v", err)
	}
	if _, err := s.RollbackSpec(ctx, "missing", 1, 1, "a", ""); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("missing spec: %v", err)
	}
	a.Name = "api2"
	if _, err := s.PutSpec(ctx, a, 1, "a", ""); err != nil {
		t.Fatal(err)
	}
	b := process.ProcessSpec{ProcessID: "p2", Name: "api", Command: "x", Instances: 1}
	if _, err := s.PutSpec(ctx, b, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RollbackSpec(ctx, "p1", 1, 2, "a", "undo"); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT got %v", err)
	}
}

func TestSpecMethods_ErrorAfterClose(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	spec := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "a", ""); err == nil {
		t.Fatal("PutSpec")
	}
	if _, err := s.GetSpec(ctx, "p1"); err == nil {
		t.Fatal("GetSpec")
	}
	if _, err := s.GetSpecByName(ctx, "api"); err == nil {
		t.Fatal("GetSpecByName")
	}
	if _, err := s.ListSpecs(ctx); err == nil {
		t.Fatal("ListSpecs")
	}
	if err := s.DeleteSpec(ctx, "p1", 1); err == nil {
		t.Fatal("DeleteSpec")
	}
	if _, err := s.ListRevisions(ctx, "p1"); err == nil {
		t.Fatal("ListRevisions")
	}
	if _, err := s.RollbackSpec(ctx, "p1", 1, 1, "a", ""); err == nil {
		t.Fatal("RollbackSpec")
	}
}
