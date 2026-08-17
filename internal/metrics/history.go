package metrics

import (
	"math"
	"time"
)

const (
	LayerRawMin = "raw_min"
	LayerDown5m = "down_5m"

	SeriesNodeCPU  = "node.cpu_percent"
	SeriesNodeMem  = "node.memory_percent"
	SeriesNodeDisk = "node.disk_percent"
	SeriesProcCPU  = "process.cpu_percent"
	SeriesProcMem  = "process.memory_bytes"

	RawMinRetention = 24 * time.Hour
	Down5mRetention = 7 * 24 * time.Hour
	FlushDeleteCap  = 256
)

func MinuteUnix(t time.Time) int64 { return t.UTC().Truncate(time.Minute).Unix() }

func FiveMinUnix(t time.Time) int64 { return t.UTC().Truncate(5 * time.Minute).Unix() }

func SelectLayer(since, until time.Time) string {
	if !until.After(since) {
		return LayerRawMin
	}
	if until.Sub(since) <= 24*time.Hour {
		return LayerRawMin
	}
	return LayerDown5m
}

type AggKind int

const (
	AggMean AggKind = iota
	AggMax
)

func KindOf(series string) AggKind {
	if series == SeriesNodeDisk {
		return AggMax
	}
	return AggMean
}

func ValidSample(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

func Aggregate(kind AggKind, values []float64) (float64, bool) {
	n := 0
	var sum, max float64
	for _, v := range values {
		if !ValidSample(v) {
			continue
		}
		if n == 0 || v > max {
			max = v
		}
		sum += v
		n++
	}
	if n == 0 {
		return 0, false
	}
	if kind == AggMax {
		return max, true
	}
	return sum / float64(n), true
}
