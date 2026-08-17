package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestAlert_UpsertGetListByFingerprint(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	first := time.Unix(1_700_000_000, 0).UTC()
	rec := store.AlertRecord{
		AlertID: "a1", Fingerprint: "PROCESS_EXIT:p1", Type: "PROCESS_EXIT",
		Severity: "WARNING", NodeID: "n1", ProcessID: "p1", PayloadJSON: `{}`,
		State: "FIRING", FirstAt: first, LastAt: first,
	}
	if err := s.UpsertAlert(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAlertByFingerprint(ctx, "PROCESS_EXIT:p1")
	if err != nil || got.AlertID != "a1" || got.State != "FIRING" {
		t.Fatalf("got %+v %v", got, err)
	}
	rec.LastAt = first.Add(time.Minute)
	rec.AlertID = "should-not-replace"
	rec.State = "FIRING"
	if err := s.UpsertAlert(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAlert(ctx, "a1")
	if err != nil || got.AlertID != "a1" || !got.LastAt.Equal(first.Add(time.Minute)) {
		t.Fatalf("reuse failed %+v %v", got, err)
	}
	if _, err := s.GetAlert(ctx, "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
	list, err := s.ListAlerts(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Fingerprint != "PROCESS_EXIT:p1" {
		t.Fatalf("list %+v %v", list, err)
	}
}
