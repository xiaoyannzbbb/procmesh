package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestBeginOperation_Idempotent(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	op := store.Operation{OperationID: "op-1", Type: "start", Target: "p1", Status: "PENDING"}
	if _, dup, err := s.BeginOperation(ctx, op); err != nil || dup {
		t.Fatalf("first: dup=%v err=%v", dup, err)
	}
	if err := s.FinishOperation(ctx, "op-1", "SUCCESS", []byte(`{"ok":true}`), ""); err != nil {
		t.Fatal(err)
	}
	got, dup, err := s.BeginOperation(ctx, op)
	if err != nil || !dup || got.Status != "SUCCESS" {
		t.Fatalf("second: %+v dup=%v err=%v", got, dup, err)
	}
}

func TestPutGetListInstance(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	started := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	exit := time.Date(2026, 8, 13, 10, 1, 0, 0, time.UTC)
	code := 1
	inst := process.Instance{
		InstanceID:     process.MakeInstanceID("p1", 0),
		ProcessID:      "p1",
		Ordinal:        0,
		PID:            42,
		ShimPID:        7,
		Desired:        process.DesiredRunning,
		Observed:       process.ObservedRunning,
		Health:         process.HealthHealthy,
		StartedAt:      &started,
		ExitAt:         &exit,
		ExitCode:       &code,
		RestartCount:   3,
		ActiveRevision: 2,
		BootID:         "boot-1",
	}
	if err := s.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	assertInstanceEqual(t, got, inst)

	other := process.Instance{
		InstanceID: process.MakeInstanceID("p1", 1),
		ProcessID:  "p1",
		Ordinal:    1,
		Desired:    process.DesiredStopped,
		Observed:   process.ObservedStopped,
		Health:     process.HealthUnknown,
	}
	if err := s.PutInstance(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := s.PutInstance(ctx, process.Instance{
		InstanceID: process.MakeInstanceID("p2", 0),
		ProcessID:  "p2",
		Desired:    process.DesiredRunning,
		Observed:   process.ObservedStarting,
		Health:     process.HealthUnknown,
	}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListInstances(ctx, "p1")
	if err != nil || len(list) != 2 {
		t.Fatalf("list=%d err=%v", len(list), err)
	}
	if list[0].Ordinal != 0 || list[1].Ordinal != 1 {
		t.Fatalf("order: %+v", list)
	}

	inst.Observed = process.ObservedExited
	inst.PID = 0
	if err := s.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetInstance(ctx, inst.InstanceID)
	if err != nil || got.Observed != process.ObservedExited || got.PID != 0 {
		t.Fatalf("upsert: %+v %v", got, err)
	}
}

func TestGetInstance_NotFound(t *testing.T) {
	s := openStore(t)
	_, err := s.GetInstance(context.Background(), "missing")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND got %v", err)
	}
}

func TestListInstances_Empty(t *testing.T) {
	list, err := openStore(t).ListInstances(context.Background(), "missing")
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("list=%v err=%v", list, err)
	}
}

func TestPutInstance_RejectsEmptyID(t *testing.T) {
	err := openStore(t).PutInstance(context.Background(), process.Instance{ProcessID: "p1"})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}

func TestInstance_PersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	inst := process.Instance{
		InstanceID:     "p1:0",
		ProcessID:      "p1",
		Ordinal:        0,
		Desired:        process.DesiredRunning,
		Observed:       process.ObservedBackoff,
		Health:         process.HealthUnhealthy,
		RestartCount:   4,
		ActiveRevision: 9,
		BootID:         "boot-x",
	}
	if err := s.PutInstance(ctx, inst); err != nil {
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
	got, err := s2.GetInstance(ctx, "p1:0")
	if err != nil {
		t.Fatal(err)
	}
	assertInstanceEqual(t, got, inst)
}

func TestBeginOperation_StoresAndGet(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	op := store.Operation{
		OperationID:    "op-store",
		Operator:       "admin",
		SourceAgent:    "agent-1",
		Target:         "p1",
		Type:           "start",
		RequestPayload: []byte(`{"n":1}`),
		CreatedAt:      created,
		Status:         store.OpPending,
	}
	got, dup, err := s.BeginOperation(ctx, op)
	if err != nil || dup {
		t.Fatalf("begin: dup=%v err=%v", dup, err)
	}
	if got.OperationID != "op-store" || got.Operator != "admin" || got.SourceAgent != "agent-1" {
		t.Fatalf("meta: %+v", got)
	}
	if got.Type != "start" || got.Target != "p1" || got.Status != store.OpPending {
		t.Fatalf("fields: %+v", got)
	}
	if string(got.RequestPayload) != `{"n":1}` || !got.CreatedAt.Equal(created) {
		t.Fatalf("payload/ts: %+v", got)
	}
	loaded, err := s.GetOperation(ctx, "op-store")
	if err != nil || loaded.Status != store.OpPending {
		t.Fatalf("get: %+v %v", loaded, err)
	}
}

func TestFinishOperation_UpdatesStatusResultError(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	op := store.Operation{OperationID: "op-fin", Type: "stop", Target: "p1", Status: store.OpRunning}
	if _, dup, err := s.BeginOperation(ctx, op); err != nil || dup {
		t.Fatalf("begin: dup=%v err=%v", dup, err)
	}
	if err := s.FinishOperation(ctx, "op-fin", store.OpFailed, []byte(`{"ok":false}`), "boom"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOperation(ctx, "op-fin")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.OpFailed || string(got.Result) != `{"ok":false}` || got.Error != "boom" {
		t.Fatalf("finish: %+v", got)
	}
	if got.FinishedAt.IsZero() {
		t.Fatal("finished_at not set")
	}
}

func TestFinishOperation_NotFoundAndInvalidStatus(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.FinishOperation(ctx, "missing", store.OpSuccess, nil, ""); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("missing: %v", err)
	}
	op := store.Operation{OperationID: "op-bad", Type: "start", Target: "p1", Status: store.OpPending}
	if _, _, err := s.BeginOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOperation(ctx, "op-bad", "DONE", nil, ""); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("status: %v", err)
	}
}

func TestGetOperation_NotFound(t *testing.T) {
	_, err := openStore(t).GetOperation(context.Background(), "missing")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND got %v", err)
	}
}

func TestBeginOperation_RejectsEmptyID(t *testing.T) {
	_, _, err := openStore(t).BeginOperation(context.Background(), store.Operation{Type: "start"})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}

func TestBeginOperation_DefaultsPending(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	got, dup, err := s.BeginOperation(ctx, store.Operation{OperationID: "op-def", Type: "start", Target: "p1"})
	if err != nil || dup || got.Status != store.OpPending || got.CreatedAt.IsZero() {
		t.Fatalf("got %+v dup=%v err=%v", got, dup, err)
	}
}

func TestBeginOperation_DoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	first := store.Operation{OperationID: "op-keep", Type: "start", Target: "p1", Operator: "a", Status: store.OpPending}
	if _, dup, err := s.BeginOperation(ctx, first); err != nil || dup {
		t.Fatalf("first: dup=%v err=%v", dup, err)
	}
	second := store.Operation{OperationID: "op-keep", Type: "stop", Target: "p2", Operator: "b", Status: store.OpRunning}
	got, dup, err := s.BeginOperation(ctx, second)
	if err != nil || !dup {
		t.Fatalf("second: dup=%v err=%v", dup, err)
	}
	if got.Type != "start" || got.Target != "p1" || got.Operator != "a" || got.Status != store.OpPending {
		t.Fatalf("overwrote: %+v", got)
	}
}

func TestRuntimeMethods_ErrorAfterClose(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	inst := process.Instance{InstanceID: "p1:0", ProcessID: "p1"}
	if err := s.PutInstance(ctx, inst); err == nil {
		t.Fatal("PutInstance")
	}
	if _, err := s.GetInstance(ctx, "p1:0"); err == nil {
		t.Fatal("GetInstance")
	}
	if _, err := s.ListInstances(ctx, "p1"); err == nil {
		t.Fatal("ListInstances")
	}
	op := store.Operation{OperationID: "op-1", Type: "start", Target: "p1", Status: store.OpPending}
	if _, _, err := s.BeginOperation(ctx, op); err == nil {
		t.Fatal("BeginOperation")
	}
	if err := s.FinishOperation(ctx, "op-1", store.OpSuccess, nil, ""); err == nil {
		t.Fatal("FinishOperation")
	}
	if _, err := s.GetOperation(ctx, "op-1"); err == nil {
		t.Fatal("GetOperation")
	}
}

func assertInstanceEqual(t *testing.T, got, want process.Instance) {
	t.Helper()
	if got.InstanceID != want.InstanceID || got.ProcessID != want.ProcessID || got.Ordinal != want.Ordinal {
		t.Fatalf("ids: %+v want %+v", got, want)
	}
	if got.PID != want.PID || got.ShimPID != want.ShimPID || got.RestartCount != want.RestartCount {
		t.Fatalf("pids: %+v want %+v", got, want)
	}
	if got.Desired != want.Desired || got.Observed != want.Observed || got.Health != want.Health {
		t.Fatalf("state: %+v want %+v", got, want)
	}
	if got.ActiveRevision != want.ActiveRevision || got.BootID != want.BootID {
		t.Fatalf("rev/boot: %+v want %+v", got, want)
	}
	assertTimePtr(t, "started", got.StartedAt, want.StartedAt)
	assertTimePtr(t, "exit", got.ExitAt, want.ExitAt)
	if (got.ExitCode == nil) != (want.ExitCode == nil) {
		t.Fatalf("exit_code: %+v want %+v", got.ExitCode, want.ExitCode)
	}
	if got.ExitCode != nil && *got.ExitCode != *want.ExitCode {
		t.Fatalf("exit_code %d want %d", *got.ExitCode, *want.ExitCode)
	}
}

func assertTimePtr(t *testing.T, name string, got, want *time.Time) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Fatalf("%s: %+v want %+v", name, got, want)
	}
	if got != nil && !got.Equal(*want) {
		t.Fatalf("%s: %s want %s", name, got, want)
	}
}
