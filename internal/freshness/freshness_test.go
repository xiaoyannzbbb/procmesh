package freshness

import (
	"testing"
	"time"
)

var classifyNow = time.UnixMilli(1_700_000_010_000)

func TestClassify_LiveAliveRecent(t *testing.T) {
	if got := Classify(classifyNow, 1_700_000_005_000, "ALIVE"); got != LIVE {
		t.Fatalf("recent ALIVE: got %s want LIVE", got)
	}
	if got := Classify(classifyNow, 1_700_000_000_000, "ALIVE"); got != LIVE {
		t.Fatalf("ALIVE age==10s: got %s want LIVE", got)
	}
}

func TestClassify_StaleAliveOld(t *testing.T) {
	if got := Classify(classifyNow, 1_699_999_999_000, "ALIVE"); got != STALE {
		t.Fatalf("old ALIVE: got %s want STALE", got)
	}
}

func TestClassify_StaleFailedEvenIfRecent(t *testing.T) {
	if got := Classify(classifyNow, 1_700_000_009_000, "FAILED"); got != STALE {
		t.Fatalf("recent FAILED: got %s want STALE", got)
	}
}

func TestClassify_UnknownNoTimestamp(t *testing.T) {
	if got := Classify(classifyNow, 0, "ALIVE"); got != UNKNOWN {
		t.Fatalf("last=0: got %s want UNKNOWN", got)
	}
}

func TestClassify_UnknownRevoked(t *testing.T) {
	if got := Classify(classifyNow, 1_700_000_009_000, "REVOKED"); got != UNKNOWN {
		t.Fatalf("REVOKED: got %s want UNKNOWN", got)
	}
}

func TestClassify_StaleSuspect(t *testing.T) {
	if got := Classify(classifyNow, 1_700_000_009_000, "SUSPECT"); got != STALE {
		t.Fatalf("recent SUSPECT: got %s want STALE", got)
	}
}

func TestClassify_StaleLeft(t *testing.T) {
	if got := Classify(classifyNow, 1_700_000_009_000, "LEFT"); got != STALE {
		t.Fatalf("recent LEFT: got %s want STALE", got)
	}
}

func TestClassify_StaleJoiningWithTimestamp(t *testing.T) {
	if got := Classify(classifyNow, 1_700_000_009_000, "JOINING"); got != STALE {
		t.Fatalf("recent JOINING: got %s want STALE", got)
	}
}
