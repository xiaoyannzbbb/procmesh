package update

import (
	"sort"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/freshness"
)

// OrderTargets places LIVE control leader last, the current entry node
// second-to-last when it is not that leader, and remaining eligible nodes
// by hostname then node_id. Skipped nodes are appended after eligible ones.
func OrderTargets(nodes []TargetSpec, entryID, liveLeaderID string) []TargetSpec {
	var eligible, skipped []TargetSpec
	for _, n := range nodes {
		if strings.TrimSpace(n.SkipReason) != "" {
			skipped = append(skipped, n)
			continue
		}
		eligible = append(eligible, n)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		ri, rj := targetRank(eligible[i], entryID, liveLeaderID), targetRank(eligible[j], entryID, liveLeaderID)
		if ri != rj {
			return ri < rj
		}
		return lessHostID(eligible[i], eligible[j])
	})
	sort.SliceStable(skipped, func(i, j int) bool {
		return lessHostID(skipped[i], skipped[j])
	})
	out := make([]TargetSpec, 0, len(nodes))
	out = append(out, eligible...)
	out = append(out, skipped...)
	return out
}

func targetRank(n TargetSpec, entryID, liveLeaderID string) int {
	if liveLeaderID != "" && n.NodeID == liveLeaderID {
		return 2
	}
	if entryID != "" && n.NodeID == entryID {
		return 1
	}
	return 0
}

func lessHostID(a, b TargetSpec) bool {
	if a.Hostname != b.Hostname {
		return a.Hostname < b.Hostname
	}
	return a.NodeID < b.NodeID
}

// LiveLeaderID returns the control leader when that member's gossip
// freshness is LIVE. STALE/UNKNOWN/missing members are not special-cased.
func LiveLeaderID(leaderID string, now time.Time, members []cluster.NodeSummary) string {
	leaderID = strings.TrimSpace(leaderID)
	if leaderID == "" {
		return ""
	}
	for _, m := range members {
		if m.NodeID != leaderID {
			continue
		}
		if freshness.Classify(now, m.LastUpdatedUnixMs, string(m.State)) == freshness.LIVE {
			return leaderID
		}
		return ""
	}
	return ""
}
