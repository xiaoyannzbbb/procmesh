package metrics_test

import (
	"math"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/metrics"
)

func TestMinuteAndFiveMinUnix(t *testing.T) {
	tm := time.Date(2026, 8, 16, 12, 7, 42, 0, time.UTC)
	if metrics.MinuteUnix(tm) != tm.Truncate(time.Minute).Unix() {
		t.Fatal("minute")
	}
	if metrics.FiveMinUnix(tm) != time.Date(2026, 8, 16, 12, 5, 0, 0, time.UTC).Unix() {
		t.Fatal("five")
	}
}

func TestSelectLayer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	if metrics.SelectLayer(now.Add(-24*time.Hour), now) != metrics.LayerRawMin {
		t.Fatal("24h inclusive uses raw_min")
	}
	if metrics.SelectLayer(now.Add(-24*time.Hour-time.Second), now) != metrics.LayerDown5m {
		t.Fatal(">24h uses down_5m")
	}
}

func TestAggregate_MeanAndMaxAndEmpty(t *testing.T) {
	v, ok := metrics.Aggregate(metrics.AggMean, []float64{10, 20, 30})
	if !ok || v != 20 {
		t.Fatalf("mean %v %v", v, ok)
	}
	v, ok = metrics.Aggregate(metrics.AggMax, []float64{10, 40, 30})
	if !ok || v != 40 {
		t.Fatalf("max %v %v", v, ok)
	}
	if _, ok = metrics.Aggregate(metrics.AggMean, nil); ok {
		t.Fatal("empty")
	}
	if metrics.KindOf(metrics.SeriesNodeDisk) != metrics.AggMax {
		t.Fatal("disk max")
	}
	if metrics.KindOf(metrics.SeriesNodeCPU) != metrics.AggMean {
		t.Fatal("cpu mean")
	}
	if metrics.ValidSample(-1) || metrics.ValidSample(math.NaN()) {
		t.Fatal("invalid")
	}
}
