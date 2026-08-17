package store_test

import (
	"context"
	"testing"

	"github.com/qleelulu/procmesh/internal/store"
)

func TestMetricSamples_InsertListRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	err := s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 100, Value: 10},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 160, Value: 20},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 220, Value: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListMetricSamples(ctx, "node.cpu_percent", "n1", "raw_min", 160, 220)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TSUnix != 160 || got[0].Value != 20 || got[1].TSUnix != 220 {
		t.Fatalf("%+v", got)
	}
}

func TestMetricSamples_ReplaceSamePrimaryKey(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.disk_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 60, Value: 40},
	})
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.disk_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 60, Value: 55},
	})
	got, _ := s.ListMetricSamples(ctx, "node.disk_percent", "n1", "raw_min", 0, 1000)
	if len(got) != 1 || got[0].Value != 55 {
		t.Fatalf("%+v", got)
	}
}

func TestMetricSamples_DoesNotInventMissingMinutes(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 0, Value: 1},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 120, Value: 2},
	})
	got, _ := s.ListMetricSamples(ctx, "node.cpu_percent", "n1", "raw_min", 0, 120)
	if len(got) != 2 {
		t.Fatalf("gap must remain a gap: %+v", got)
	}
	for _, p := range got {
		if p.TSUnix == 60 {
			t.Fatal("must not invent ts=60")
		}
		if p.Value == 0 && p.TSUnix != 0 {
			t.Fatal("must not insert 0 for gap")
		}
	}
}

func TestMetricSamples_DeleteBeforeAndOldest(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "down_5m", TSUnix: 10, Value: 1},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "down_5m", TSUnix: 20, Value: 2},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "down_5m", TSUnix: 30, Value: 3},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 10, Value: 9},
	})
	n, err := s.DeleteMetricSamplesBefore(ctx, "down_5m", 20)
	if err != nil || n != 1 {
		t.Fatalf("before n=%d err=%v", n, err)
	}
	n, err = s.DeleteOldestMetricSamples(ctx, "down_5m", 1)
	if err != nil || n != 1 {
		t.Fatalf("oldest n=%d err=%v", n, err)
	}
	got, _ := s.ListMetricSamples(ctx, "node.cpu_percent", "n1", "down_5m", 0, 100)
	if len(got) != 1 || got[0].TSUnix != 30 {
		t.Fatalf("%+v", got)
	}
	raw, _ := s.ListMetricSamples(ctx, "node.cpu_percent", "n1", "raw_min", 0, 100)
	if len(raw) != 1 {
		t.Fatalf("raw must be untouched: %+v", raw)
	}
}

func TestMetricSamples_CountAndEmptyInsert(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.InsertMetricSamples(ctx, nil); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountMetricSamples(ctx)
	if err != nil || n != 0 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "process.cpu_percent", SubjectID: "p1", Layer: "raw_min", TSUnix: 1, Value: 3},
	})
	n, _ = s.CountMetricSamples(ctx)
	if n != 1 {
		t.Fatalf("count=%d", n)
	}
}

func TestMetricSamples_ListRequiresKeys(t *testing.T) {
	s := openStore(t)
	if _, err := s.ListMetricSamples(context.Background(), "", "n1", "raw_min", 0, 1); err == nil {
		t.Fatal("empty series must error")
	}
}
