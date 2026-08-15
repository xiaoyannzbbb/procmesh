package freshness

import "time"

const (
	LIVE    = "LIVE"
	STALE   = "STALE"
	UNKNOWN = "UNKNOWN"
	MaxAge  = 10 * time.Second
)

func Classify(now time.Time, lastUpdatedUnixMs int64, nodeState string) string {
	if lastUpdatedUnixMs <= 0 {
		return UNKNOWN
	}
	switch nodeState {
	case "REMOVED", "REVOKED":
		return UNKNOWN
	}
	age := now.Sub(time.UnixMilli(lastUpdatedUnixMs))
	if nodeState == "ALIVE" && age <= MaxAge {
		return LIVE
	}
	return STALE
}
