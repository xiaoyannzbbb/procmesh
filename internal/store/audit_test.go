package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestAppendAudit_IsAppendOnly(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.AppendAudit(ctx, store.AuditEvent{Action: "process.start", Resource: "nginx", Result: "SUCCESS", OperationID: "op-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, store.AuditEvent{Action: "process.stop", Resource: "nginx", Result: "SUCCESS", OperationID: "op-2"}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAudit(ctx, "nginx", 10)
	if err != nil || len(evs) != 2 || evs[0].Action != "process.stop" {
		t.Fatalf("%+v %v", evs, err)
	}
}

func TestAppendAudit_GeneratesAuditIDWhenEmpty(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.AppendAudit(ctx, store.AuditEvent{Action: "process.start", Resource: "nginx", Result: "SUCCESS"}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAudit(ctx, "nginx", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs[0].AuditID == "" {
		t.Fatal("expected generated AuditID")
	}
	if !looksLikeUUID(evs[0].AuditID) {
		t.Fatalf("AuditID not UUID: %q", evs[0].AuditID)
	}
	if evs[0].Timestamp.IsZero() {
		t.Fatal("expected generated Timestamp")
	}
}

func TestAppendAudit_KeepsProvidedAuditID(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	ts := time.Date(2026, 8, 13, 15, 4, 5, 0, time.UTC)
	ev := store.AuditEvent{
		AuditID:     "audit-fixed",
		Timestamp:   ts,
		Action:      "process.start",
		Resource:    "nginx",
		Result:      "SUCCESS",
		OperationID: "op-1",
	}
	if err := s.AppendAudit(ctx, ev); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAudit(ctx, "nginx", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs[0].AuditID != "audit-fixed" || !evs[0].Timestamp.Equal(ts) {
		t.Fatalf("got %+v", evs[0])
	}
}

func TestAppendAudit_PersistsAllFields(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	ts := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	want := store.AuditEvent{
		AuditID:     "audit-full",
		Timestamp:   ts,
		UserID:      "u1",
		Username:    "admin",
		SourceIP:    "127.0.0.1",
		SourceAgent: "agent-a",
		TargetAgent: "agent-b",
		Resource:    "nginx",
		Action:      "process.update",
		OperationID: "op-9",
		Result:      "SUCCESS",
		Metadata:    []byte(`{"rev":2}`),
	}
	if err := s.AppendAudit(ctx, want); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAudit(ctx, "nginx", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%+v %v", evs, err)
	}
	assertAuditEqual(t, evs[0], want)
}

func TestAppendAudit_PersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	want := store.AuditEvent{
		AuditID:     "audit-reopen",
		Timestamp:   time.Date(2026, 8, 13, 17, 0, 0, 0, time.UTC),
		Resource:    "api",
		Action:      "process.create",
		Result:      "SUCCESS",
		OperationID: "op-re",
		Metadata:    []byte(`{"ok":true}`),
	}
	if err := s.AppendAudit(ctx, want); err != nil {
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
	evs, err := s2.ListAudit(ctx, "api", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%+v %v", evs, err)
	}
	assertAuditEqual(t, evs[0], want)
}

func TestListAudit_FiltersByResource(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.AppendAudit(ctx, store.AuditEvent{AuditID: "a1", Action: "process.start", Resource: "nginx", Result: "SUCCESS"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, store.AuditEvent{AuditID: "a2", Action: "process.start", Resource: "api", Result: "SUCCESS"}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAudit(ctx, "api", 10)
	if err != nil || len(evs) != 1 || evs[0].AuditID != "a2" {
		t.Fatalf("%+v %v", evs, err)
	}
}

func TestListAudit_Empty(t *testing.T) {
	evs, err := openStore(t).ListAudit(context.Background(), "missing", 10)
	if err != nil || evs == nil || len(evs) != 0 {
		t.Fatalf("list=%v err=%v", evs, err)
	}
}

func TestListAudit_RespectsLimit(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	for i, action := range []string{"process.start", "process.stop", "process.update"} {
		ev := store.AuditEvent{
			AuditID:   "lim-" + action,
			Timestamp: time.Date(2026, 8, 13, 18, i, 0, 0, time.UTC),
			Resource:  "nginx",
			Action:    action,
			Result:    "SUCCESS",
		}
		if err := s.AppendAudit(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.ListAudit(ctx, "nginx", 2)
	if err != nil || len(evs) != 2 {
		t.Fatalf("len=%d err=%v", len(evs), err)
	}
	if evs[0].Action != "process.update" || evs[1].Action != "process.stop" {
		t.Fatalf("order: %+v", evs)
	}
}

func TestAppendAudit_DuplicateIDDoesNotUpdate(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	first := store.AuditEvent{AuditID: "dup-1", Action: "process.start", Resource: "nginx", Result: "SUCCESS", OperationID: "op-1"}
	if err := s.AppendAudit(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := store.AuditEvent{AuditID: "dup-1", Action: "process.stop", Resource: "nginx", Result: "FAILED", OperationID: "op-2"}
	if err := s.AppendAudit(ctx, second); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT got %v", err)
	}
	evs, err := s.ListAudit(ctx, "nginx", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs[0].Action != "process.start" || evs[0].Result != "SUCCESS" || evs[0].OperationID != "op-1" {
		t.Fatalf("updated existing row: %+v", evs[0])
	}
}

func TestWriteAudit_WrapsAppendAudit(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.WriteAudit(ctx, "p1", "process.create", "op-1", "admin", "SUCCESS"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAudit(ctx, "p1", "process.start", "op-2", "admin", "SUCCESS"); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAudit(ctx, "p1", 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs[0].Action != "process.start" || evs[0].OperationID != "op-2" || evs[0].Username != "admin" || evs[0].Result != "SUCCESS" {
		t.Fatalf("newest: %+v", evs[0])
	}
	if evs[1].Action != "process.create" || evs[1].OperationID != "op-1" {
		t.Fatalf("older: %+v", evs[1])
	}
	if evs[0].AuditID == "" || evs[0].Timestamp.IsZero() {
		t.Fatal("expected generated AuditID and Timestamp")
	}
}

func TestListAuditAll_EmptyResourceReturnsAll(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.AppendAudit(ctx, store.AuditEvent{AuditID: "all-1", Action: "process.start", Resource: "nginx", Result: "SUCCESS"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, store.AuditEvent{AuditID: "all-2", Action: "process.start", Resource: "api", Result: "SUCCESS"}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAuditAll(ctx, "", 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("len=%d err=%v evs=%+v", len(evs), err, evs)
	}
	got := map[string]bool{}
	for _, ev := range evs {
		got[ev.Resource] = true
	}
	if !got["nginx"] || !got["api"] {
		t.Fatalf("resources=%v", got)
	}
}

func TestListAuditAll_CapsLimit(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	for i := 0; i < 5; i++ {
		ev := store.AuditEvent{
			AuditID:   "cap-" + string(rune('a'+i)),
			Timestamp: time.Date(2026, 8, 15, 12, 0, i, 0, time.UTC),
			Resource:  "nginx",
			Action:    "process.start",
			Result:    "SUCCESS",
		}
		if err := s.AppendAudit(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	got2, err := s.ListAuditAll(ctx, "nginx", 2)
	if err != nil || len(got2) != 2 {
		t.Fatalf("limit=2 len=%d err=%v", len(got2), err)
	}
	got0, err := s.ListAuditAll(ctx, "nginx", 0)
	if err != nil || len(got0) != 5 {
		t.Fatalf("limit=0 (as 50) len=%d err=%v", len(got0), err)
	}
	got500, err := s.ListAuditAll(ctx, "nginx", 500)
	if err != nil || len(got500) != 5 {
		t.Fatalf("limit=500 (as 200) len=%d err=%v", len(got500), err)
	}
}

func TestAudit_ErrorAfterClose(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, store.AuditEvent{Action: "process.start", Resource: "nginx"}); err == nil {
		t.Fatal("AppendAudit")
	}
	if _, err := s.ListAudit(ctx, "nginx", 10); err == nil {
		t.Fatal("ListAudit")
	}
	if _, err := s.ListAuditAll(ctx, "", 10); err == nil {
		t.Fatal("ListAuditAll")
	}
}

func looksLikeUUID(id string) bool {
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		return false
	}
	if len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		return false
	}
	return true
}

func assertAuditEqual(t *testing.T, got, want store.AuditEvent) {
	t.Helper()
	if got.AuditID != want.AuditID || got.UserID != want.UserID || got.Username != want.Username {
		t.Fatalf("ids: %+v want %+v", got, want)
	}
	if got.SourceIP != want.SourceIP || got.SourceAgent != want.SourceAgent || got.TargetAgent != want.TargetAgent {
		t.Fatalf("source: %+v want %+v", got, want)
	}
	if got.Resource != want.Resource || got.Action != want.Action || got.OperationID != want.OperationID || got.Result != want.Result {
		t.Fatalf("action: %+v want %+v", got, want)
	}
	if !got.Timestamp.Equal(want.Timestamp) {
		t.Fatalf("ts %s want %s", got.Timestamp, want.Timestamp)
	}
	if string(got.Metadata) != string(want.Metadata) {
		t.Fatalf("metadata %q want %q", got.Metadata, want.Metadata)
	}
}
