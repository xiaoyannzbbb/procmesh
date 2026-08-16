package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestBatch_InsertGetListAndUpdate(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	rec := store.BatchRecord{
		BatchID: "b1", Operator: "admin", SourceAgent: "n1",
		Type: "RESTART", SelectorJSON: `{"process_ids":["p1"]}`,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(), Status: "PENDING",
		SummaryJSON: `{"success":0,"failed":0,"timeout":0,"denied":0,"conflict":0,"unavailable":0,"invalid":0}`,
	}
	targets := []store.BatchTargetRecord{{
		BatchID: "b1", OperationID: "op-1", NodeID: "n1", ProcessID: "p1",
		ProcessName: "nginx", Status: "PENDING",
	}}
	if err := s.InsertBatch(ctx, rec, targets); err != nil {
		t.Fatal(err)
	}
	got, ts, err := s.GetBatch(ctx, "b1")
	if err != nil || got.Type != "RESTART" || len(ts) != 1 || ts[0].OperationID != "op-1" {
		t.Fatalf("got %+v %+v %v", got, ts, err)
	}
	if _, _, err := s.GetBatch(ctx, "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
	ts[0].Status = "SUCCESS"
	if err := s.UpdateTarget(ctx, "b1", "op-1", ts[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateBatchStatus(ctx, "b1", "COMPLETED", `{"success":1,"failed":0,"timeout":0,"denied":0,"conflict":0,"unavailable":0,"invalid":0}`); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListBatches(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Status != "COMPLETED" {
		t.Fatalf("list %+v %v", list, err)
	}
}

func TestBatch_ListIncompleteTargets(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rec := store.BatchRecord{BatchID: "b1", Operator: "a", SourceAgent: "n", Type: "START", SelectorJSON: `{}`, CreatedAt: time.Now().UTC(), Status: "RUNNING", SummaryJSON: `{}`}
	targets := []store.BatchTargetRecord{
		{BatchID: "b1", OperationID: "op-p", NodeID: "n", ProcessID: "p", Status: "PENDING"},
		{BatchID: "b1", OperationID: "op-s", NodeID: "n", ProcessID: "s", Status: "SUCCESS"},
	}
	if err := s.InsertBatch(ctx, rec, targets); err != nil {
		t.Fatal(err)
	}
	inc, err := s.ListIncompleteTargets(ctx)
	if err != nil || len(inc) != 1 || inc[0].OperationID != "op-p" {
		t.Fatalf("%+v %v", inc, err)
	}
}

func TestBatch_ReplaceTargetOp(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rec := store.BatchRecord{
		BatchID: "b1", Operator: "a", SourceAgent: "n", Type: "START",
		SelectorJSON: `{}`, CreatedAt: time.Now().UTC(), Status: "FAILED", SummaryJSON: `{}`,
	}
	targets := []store.BatchTargetRecord{{
		BatchID: "b1", OperationID: "op-old", NodeID: "n", ProcessID: "p",
		ProcessName: "x", Status: "FAILED", Error: "boom",
		FinishedAt: time.Now().UTC(),
	}}
	if err := s.InsertBatch(ctx, rec, targets); err != nil {
		t.Fatal(err)
	}
	newRec := store.BatchTargetRecord{
		BatchID: "b1", OperationID: "op-new", NodeID: "n", ProcessID: "p",
		ProcessName: "x", Status: "PENDING",
	}
	if err := s.ReplaceTargetOp(ctx, "b1", "op-old", newRec); err != nil {
		t.Fatal(err)
	}
	_, ts, err := s.GetBatch(ctx, "b1")
	if err != nil || len(ts) != 1 || ts[0].OperationID != "op-new" || ts[0].Status != "PENDING" {
		t.Fatalf("%+v %v", ts, err)
	}
	if err := s.ReplaceTargetOp(ctx, "b1", "missing", newRec); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
}
