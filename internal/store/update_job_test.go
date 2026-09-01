package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestUpdateJob_InsertGetListAndUpdate(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	created := time.Unix(1_700_000_000, 0).UTC()
	rec := store.UpdateJobRecord{
		JobID: "j1", Operator: "admin", SourceAgent: "n1",
		PinJSON: `{"tag":"v0.2.0"}`, CreatedAt: created, Status: "PENDING",
		SummaryJSON: `{"success":0,"failed":0,"timeout":0,"conflict":0,"skipped":1,"cancelled":0}`,
	}
	targets := []store.UpdateJobTargetRecord{{
		JobID: "j1", OperationID: "op-1", NodeID: "n1", Hostname: "host-a",
		Status: "SKIPPED", SkipReason: "MACOS", OrderIndex: 0,
	}, {
		JobID: "j1", OperationID: "op-2", NodeID: "n2", Hostname: "host-b",
		Status: "PENDING", OrderIndex: 1,
	}}
	if err := s.InsertUpdateJob(ctx, rec, targets); err != nil {
		t.Fatal(err)
	}
	got, ts, err := s.GetUpdateJob(ctx, "j1")
	if err != nil || got.Operator != "admin" || got.PinJSON != rec.PinJSON || len(ts) != 2 {
		t.Fatalf("got %+v %+v %v", got, ts, err)
	}
	if ts[0].OperationID != "op-1" || ts[1].OperationID != "op-2" {
		t.Fatalf("order %+v", ts)
	}
	if _, _, err := s.GetUpdateJob(ctx, "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}

	ts[1].Status = "SUCCESS"
	ts[1].FinishedAt = created.Add(time.Second)
	if err := s.UpdateUpdateJobTarget(ctx, "j1", "op-2", ts[1]); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUpdateJobStatus(ctx, "j1", "COMPLETED", `{"success":1,"skipped":1}`, created, created.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListUpdateJobs(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Status != "COMPLETED" {
		t.Fatalf("list %+v %v", list, err)
	}
}

func TestUpdateJob_OneRunning(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	a := store.UpdateJobRecord{
		JobID: "j-run", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now, Status: "RUNNING", SummaryJSON: `{}`,
	}
	if err := s.InsertUpdateJob(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	running, err := s.HasRunningUpdateJob(ctx)
	if err != nil || !running {
		t.Fatalf("has running=%v err=%v", running, err)
	}
	b := store.UpdateJobRecord{
		JobID: "j-run-2", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now, Status: "RUNNING", SummaryJSON: `{}`,
	}
	if err := s.InsertUpdateJob(ctx, b, nil); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT, got %v", err)
	}
	if err := s.UpdateUpdateJobStatus(ctx, "j-run", "COMPLETED", `{}`, now, now); err != nil {
		t.Fatal(err)
	}
	if running, err = s.HasRunningUpdateJob(ctx); err != nil || running {
		t.Fatalf("after complete has running=%v err=%v", running, err)
	}
	if err := s.InsertUpdateJob(ctx, b, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateJob_CancelRemainingAndReplaceOp(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	rec := store.UpdateJobRecord{
		JobID: "j1", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now, Status: "RUNNING", SummaryJSON: `{}`,
	}
	targets := []store.UpdateJobTargetRecord{
		{JobID: "j1", OperationID: "op-old", NodeID: "n1", Status: "FAILED", Error: "boom", OrderIndex: 0, FinishedAt: now},
		{JobID: "j1", OperationID: "op-p", NodeID: "n2", Status: "PENDING", OrderIndex: 1},
	}
	if err := s.InsertUpdateJob(ctx, rec, targets); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUpdateJobCancelRemaining(ctx, "j1", true); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetUpdateJob(ctx, "j1")
	if err != nil || !got.CancelRemaining {
		t.Fatalf("cancel flag %+v %v", got, err)
	}
	newRec := store.UpdateJobTargetRecord{
		JobID: "j1", OperationID: "op-new", NodeID: "n1", Status: "PENDING", OrderIndex: 0,
	}
	if err := s.ReplaceUpdateJobTargetOp(ctx, "j1", "op-old", newRec); err != nil {
		t.Fatal(err)
	}
	_, ts, err := s.GetUpdateJob(ctx, "j1")
	if err != nil || len(ts) != 2 {
		t.Fatalf("%+v %v", ts, err)
	}
	found := false
	for _, trow := range ts {
		if trow.OperationID == "op-new" && trow.Status == "PENDING" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing reminted target %+v", ts)
	}
	if err := s.ReplaceUpdateJobTargetOp(ctx, "j1", "missing", newRec); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
}

func TestUpdateJob_OperationIDIdempotent(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	first := store.UpdateJobRecord{
		JobID: "j1", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now, Status: "COMPLETED", SummaryJSON: `{}`, OperationID: "op-create",
	}
	if err := s.InsertUpdateJob(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetUpdateJobByOperationID(ctx, "op-create")
	if err != nil || got.JobID != "j1" {
		t.Fatalf("by op %+v %v", got, err)
	}
	dup := store.UpdateJobRecord{
		JobID: "j2", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now, Status: "COMPLETED", SummaryJSON: `{}`, OperationID: "op-create",
	}
	if err := s.InsertUpdateJob(ctx, dup, nil); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT, got %v", err)
	}
	emptyA := store.UpdateJobRecord{
		JobID: "empty-a", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now, Status: "COMPLETED", SummaryJSON: `{}`,
	}
	emptyB := store.UpdateJobRecord{
		JobID: "empty-b", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now, Status: "COMPLETED", SummaryJSON: `{}`,
	}
	if err := s.InsertUpdateJob(ctx, emptyA, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertUpdateJob(ctx, emptyB, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetUpdateJobByOperationID(ctx, ""); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("empty op want NOT_FOUND, got %v", err)
	}
}

func TestUpdateJob_ListRunning(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	if err := s.InsertUpdateJob(ctx, store.UpdateJobRecord{
		JobID: "done", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now, Status: "COMPLETED", SummaryJSON: `{}`,
	}, []store.UpdateJobTargetRecord{{
		JobID: "done", OperationID: "op-stuck", NodeID: "n", Status: "RUNNING",
	}}); err != nil {
		t.Fatal(err)
	}
	if running, err := s.HasRunningUpdateJob(ctx); err != nil || !running {
		t.Fatalf("terminal job with RUNNING target: running=%v err=%v", running, err)
	}
	if err := s.InsertUpdateJob(ctx, store.UpdateJobRecord{
		JobID: "run", Operator: "a", SourceAgent: "n", PinJSON: `{}`,
		CreatedAt: now.Add(time.Second), Status: "RUNNING", SummaryJSON: `{}`,
	}, nil); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ListRunningUpdateJobIDs(ctx)
	if err != nil || len(ids) != 2 || ids[0] != "done" || ids[1] != "run" {
		t.Fatalf("%v %v", ids, err)
	}
	list, err := s.ListUpdateJobs(ctx, 1)
	if err != nil || len(list) != 1 || list[0].JobID != "run" {
		t.Fatalf("newest first %+v %v", list, err)
	}
}
