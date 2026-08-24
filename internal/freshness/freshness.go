package freshness

import "time"

const (
	LIVE          = "LIVE"
	STALE         = "STALE"
	UNKNOWN       = "UNKNOWN"
	MaxAge        = 10 * time.Second
	ProcessMaxAge = 40 * time.Second
)

func Classify(now time.Time, lastUpdatedUnixMs int64, nodeState string) string {
	return classifyWithAge(now, lastUpdatedUnixMs, nodeState, MaxAge)
}

func ClassifyProcess(now time.Time, lastUpdatedUnixMs int64, nodeState string) string {
	return classifyWithAge(now, lastUpdatedUnixMs, nodeState, ProcessMaxAge)
}

func classifyWithAge(now time.Time, lastUpdatedUnixMs int64, nodeState string, maxAge time.Duration) string {
	if lastUpdatedUnixMs <= 0 {
		return UNKNOWN
	}
	switch nodeState {
	case "REMOVED", "REVOKED":
		return UNKNOWN
	}
	age := now.Sub(time.UnixMilli(lastUpdatedUnixMs))
	if nodeState == "ALIVE" && age <= maxAge {
		return LIVE
	}
	return STALE
}
