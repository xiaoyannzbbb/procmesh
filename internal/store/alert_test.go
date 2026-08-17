package store_test

import (
	"context"
	"fmt"
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
	list, err := s.ListAlerts(ctx, 10, "")
	if err != nil || len(list) != 1 || list[0].Fingerprint != "PROCESS_EXIT:p1" {
		t.Fatalf("list %+v %v", list, err)
	}
}

func TestAlert_ListAlertsStateFilterKeepsOlderFiring(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	base := time.Unix(1_700_000_000, 0).UTC()
	firingAt := base
	if err := s.UpsertAlert(ctx, store.AlertRecord{
		AlertID: "f1", Fingerprint: "PROCESS_FATAL:old", Type: "PROCESS_FATAL",
		Severity: "CRITICAL", NodeID: "n1", ProcessID: "old", PayloadJSON: `{}`,
		State: "FIRING", FirstAt: firingAt, LastAt: firingAt,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		at := base.Add(time.Duration(i+1) * time.Minute)
		if err := s.UpsertAlert(ctx, store.AlertRecord{
			AlertID: fmt.Sprintf("r%02d", i), Fingerprint: fmt.Sprintf("PROCESS_EXIT:p%02d", i),
			Type: "PROCESS_EXIT", Severity: "WARNING", NodeID: "n1",
			ProcessID: fmt.Sprintf("p%02d", i), PayloadJSON: `{}`,
			State: "RESOLVED", FirstAt: at, LastAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}

	unfiltered, err := s.ListAlerts(ctx, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(unfiltered) != 50 {
		t.Fatalf("unfiltered=%d want 50", len(unfiltered))
	}
	for _, rec := range unfiltered {
		if rec.State == "FIRING" {
			t.Fatal("uncapped newest-50 already includes FIRING; test setup wrong")
		}
	}

	got, err := s.ListAlerts(ctx, 50, "FIRING")
	if err != nil || len(got) != 1 || got[0].AlertID != "f1" || got[0].State != "FIRING" {
		t.Fatalf("state filter %+v %v", got, err)
	}
}
