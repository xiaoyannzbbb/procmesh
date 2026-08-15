package api

import (
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/process"
)

func TestViewOf_StartedAtUnixMs(t *testing.T) {
	started := time.UnixMilli(1_700_000_005_000).In(time.FixedZone("UTC+8", 8*3600))
	view := ViewOf(process.ProcessSpec{ProcessID: "p1"}, []process.Instance{{
		InstanceID: "i1",
		StartedAt:  &started,
	}})
	if len(view.Instances) != 1 {
		t.Fatalf("instances=%d", len(view.Instances))
	}
	if got := view.Instances[0].GetStartedUnixMs(); got != 1_700_000_005_000 {
		t.Fatalf("started_unix_ms=%d", got)
	}
}

func TestViewOf_NilStartedAtZero(t *testing.T) {
	view := ViewOf(process.ProcessSpec{ProcessID: "p1"}, []process.Instance{{
		InstanceID: "i1",
	}})
	if len(view.Instances) != 1 {
		t.Fatalf("instances=%d", len(view.Instances))
	}
	if got := view.Instances[0].GetStartedUnixMs(); got != 0 {
		t.Fatalf("started_unix_ms=%d want 0", got)
	}
}
