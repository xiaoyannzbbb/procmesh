package batch_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

type stubExec struct {
	fn func(ctx context.Context, t batch.Target) error
}

func (s stubExec) Execute(ctx context.Context, t batch.Target, _ batch.Type) error {
	return s.fn(ctx, t)
}

func waitBatch(t *testing.T, e *batch.Engine, id string, want batch.Status) batch.Batch {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last batch.Batch
	for time.Now().Before(deadline) {
		got, err := e.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		last = got
		if got.Status == want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wait status %s: last=%+v", want, last)
	return batch.Batch{}
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
		NewID:  func() string { id := ids[0]; ids = ids[1:]; return id },
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
		NewID:  func() string { id := ids[0]; ids = ids[1:]; return id },
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

func TestEngine_WorkerPartialTimeout(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-ok", "op-to"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1", Concurrency: 2, TargetTimeout: 50 * time.Millisecond,
		Expand: stubExpand{targets: []batch.Target{
			{NodeID: "n1", ProcessID: "p-ok", ProcessName: "ok"},
			{NodeID: "n2", ProcessID: "p-to", ProcessName: "to"},
		}},
		Exec: stubExec{fn: func(ctx context.Context, t batch.Target) error {
			if t.ProcessID == "p-to" {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p-ok", "p-to"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusPartial)
	got, _ := e.Get(ctx, b.BatchID)
	var sawTO, sawOK bool
	for _, tg := range got.Targets {
		if tg.Status == batch.TargetTimeout {
			sawTO = true
		}
		if tg.Status == batch.TargetSuccess {
			sawOK = true
		}
	}
	if !sawTO || !sawOK || got.Summary.Timeout != 1 || got.Summary.Success != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestMapExecError(t *testing.T) {
	if batch.MapExecError(errcode.E(errcode.TIMEOUT, "x")) != batch.TargetTimeout {
		t.Fatal("timeout")
	}
	if batch.MapExecError(errcode.E(errcode.UNAVAILABLE, "x")) != batch.TargetUnavailable {
		t.Fatal("unavailable")
	}
	if batch.MapExecError(errcode.E(errcode.DENIED, "x")) != batch.TargetDenied {
		t.Fatal("denied")
	}
	if batch.MapExecError(errcode.E(errcode.CONFLICT, "x")) != batch.TargetConflict {
		t.Fatal("conflict")
	}
}

func TestEngine_RetryFailedNewOperationID(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-old"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n", ProcessID: "p", ProcessName: "x"}}},
		Exec:   stubExec{fn: func(context.Context, batch.Target) error { return errcode.E(errcode.INVALID, "boom") }},
		NewID:  func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeStart, batch.Selector{ProcessIDs: []string{"p"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusFailed)
	ids = []string{"op-new"}
	e.Exec = stubExec{fn: func(context.Context, batch.Target) error { return nil }}
	got, err := e.RetryFailed(ctx, b.BatchID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	_ = got
	waitBatch(t, e, b.BatchID, batch.StatusCompleted)
	got, _ = e.Get(ctx, b.BatchID)
	if len(got.Targets) != 1 {
		t.Fatalf("want 1 target row, got %d", len(got.Targets))
	}
	if got.Targets[0].OperationID != "op-new" {
		t.Fatalf("want new op, got %s", got.Targets[0].OperationID)
	}
}

func TestEngine_ReplayTimeoutReusesOperationID(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-to"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1", TargetTimeout: 20 * time.Millisecond,
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n", ProcessID: "p", ProcessName: "x"}}},
		Exec: stubExec{fn: func(ctx context.Context, _ batch.Target) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, _ := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p"}}, "")
	waitBatch(t, e, b.BatchID, batch.StatusPartial)
	before, _ := e.Get(ctx, b.BatchID)
	old := before.Targets[0].OperationID
	e.Exec = stubExec{fn: func(context.Context, batch.Target) error { return nil }}
	if _, err := e.ReplayTimeout(ctx, b.BatchID, "admin"); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusCompleted)
	after, _ := e.Get(ctx, b.BatchID)
	if after.Targets[0].OperationID != old {
		t.Fatalf("replay must reuse %s, got %s", old, after.Targets[0].OperationID)
	}
}

func TestEngine_ResumeDoesNotReplaySuccessOrTimeout(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	// Manual insert: one SUCCESS, one TIMEOUT, one PENDING
	_ = st.InsertBatch(ctx, store.BatchRecord{
		BatchID: "b1", Operator: "a", SourceAgent: "n", Type: "RESTART",
		SelectorJSON: `{}`, CreatedAt: time.Now().UTC(), Status: "RUNNING", SummaryJSON: `{}`,
	}, []store.BatchTargetRecord{
		{BatchID: "b1", OperationID: "op-s", NodeID: "n", ProcessID: "s", Status: "SUCCESS"},
		{BatchID: "b1", OperationID: "op-t", NodeID: "n", ProcessID: "t", Status: "TIMEOUT"},
		{BatchID: "b1", OperationID: "op-p", NodeID: "n", ProcessID: "p", Status: "PENDING"},
	})
	var ran []string
	e := &batch.Engine{DB: st, SourceAgent: "n", Exec: stubExec{fn: func(_ context.Context, t batch.Target) error {
		ran = append(ran, t.OperationID)
		return nil
	}}}
	e.Start(ctx)
	if err := e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, "b1", batch.StatusPartial)
	if len(ran) != 1 || ran[0] != "op-p" {
		t.Fatalf("ran %v", ran)
	}
}

func TestEngine_RetryFailedNothingToRetry(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-1"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n", ProcessID: "p", ProcessName: "x"}}},
		Exec:   stubExec{fn: func(context.Context, batch.Target) error { return nil }},
		NewID:  func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeStart, batch.Selector{ProcessIDs: []string{"p"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusCompleted)
	_, err = e.RetryFailed(ctx, b.BatchID, "admin")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID, got %v", err)
	}
}

func TestEngine_BindTargetsBeforeEnqueue(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-1"}
	var bound []string
	execBeforeBind := false
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p1", ProcessName: "x"}}},
		BindTargets: func(_ context.Context, targets []batch.Target) {
			for _, tg := range targets {
				bound = append(bound, tg.OperationID)
			}
		},
		Exec: stubExec{fn: func(_ context.Context, t batch.Target) error {
			if len(bound) == 0 || bound[0] != t.OperationID {
				execBeforeBind = true
			}
			return nil
		}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	if _, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p1"}}, ""); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, "b1", batch.StatusCompleted)
	if execBeforeBind {
		t.Fatal("Execute ran before BindTargets")
	}
	if len(bound) != 1 || bound[0] != "op-1" {
		t.Fatalf("BindTargets=%v", bound)
	}
}

func TestEngine_RetryFailedBindsRemintsBeforeEnqueue(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-old"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n", ProcessID: "p", ProcessName: "x"}}},
		Exec:   stubExec{fn: func(context.Context, batch.Target) error { return errcode.E(errcode.INVALID, "boom") }},
		NewID:  func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeStart, batch.Selector{ProcessIDs: []string{"p"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusFailed)

	var bound []string
	e.BindTargets = func(_ context.Context, targets []batch.Target) {
		for _, tg := range targets {
			bound = append(bound, tg.OperationID)
		}
	}
	ids = []string{"op-new"}
	e.Exec = stubExec{fn: func(context.Context, batch.Target) error { return nil }}
	if _, err := e.RetryFailed(ctx, b.BatchID, "admin"); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusCompleted)
	if len(bound) != 1 || bound[0] != "op-new" {
		t.Fatalf("remint BindTargets=%v", bound)
	}
}

func TestEngine_ReplayTimeoutBindsBeforeEnqueue(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-to"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1", TargetTimeout: 20 * time.Millisecond,
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n", ProcessID: "p", ProcessName: "x"}}},
		Exec: stubExec{fn: func(ctx context.Context, _ batch.Target) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusPartial)

	var bound []string
	e.BindTargets = func(_ context.Context, targets []batch.Target) {
		for _, tg := range targets {
			bound = append(bound, tg.OperationID)
		}
	}
	e.Exec = stubExec{fn: func(context.Context, batch.Target) error { return nil }}
	if _, err := e.ReplayTimeout(ctx, b.BatchID, "admin"); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusCompleted)
	if len(bound) != 1 || bound[0] != "op-to" {
		t.Fatalf("replay BindTargets=%v", bound)
	}
}

func TestEngine_ParentCancelDoesNotFailRunningTarget(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openStore(t)
	ids := []string{"b1", "op-1"}
	started := make(chan struct{})
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p1", ProcessName: "x"}}},
		Exec: stubExec{fn: func(ctx context.Context, _ batch.Target) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(parent)
	b, err := e.Create(context.Background(), "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p1"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("execute did not start")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	var got batch.Batch
	for time.Now().Before(deadline) {
		got, err = e.Get(context.Background(), b.BatchID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Targets[0].Status == batch.TargetFailed {
			t.Fatalf("parent cancel must not persist FAILED: %+v", got.Targets[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Targets[0].Status != batch.TargetPending && got.Targets[0].Status != batch.TargetRunning {
		t.Fatalf("want PENDING/RUNNING after parent cancel, got %s", got.Targets[0].Status)
	}
}

func TestEngine_ReplayTimeoutNothingToReplay(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-1"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n", ProcessID: "p", ProcessName: "x"}}},
		Exec:   stubExec{fn: func(context.Context, batch.Target) error { return nil }},
		NewID:  func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeStart, batch.Selector{ProcessIDs: []string{"p"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusCompleted)
	_, err = e.ReplayTimeout(ctx, b.BatchID, "admin")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID, got %v", err)
	}
}
