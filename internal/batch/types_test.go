package batch_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/batch"
)

func TestRollup_AllSuccessCompleted(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetSuccess}, {Status: batch.TargetSuccess}}); g != batch.StatusCompleted {
		t.Fatalf("%s", g)
	}
}

func TestRollup_AllFailedNoTimeout(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetFailed}, {Status: batch.TargetDenied}}); g != batch.StatusFailed {
		t.Fatalf("%s", g)
	}
}

func TestRollup_TimeoutIsPartialEvenIfOthersFailed(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetFailed}, {Status: batch.TargetTimeout}}); g != batch.StatusPartial {
		t.Fatalf("%s", g)
	}
}

func TestRollup_MixedSuccessFailurePartial(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetSuccess}, {Status: batch.TargetFailed}}); g != batch.StatusPartial {
		t.Fatalf("%s", g)
	}
}

func TestRollup_StillRunning(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetSuccess}, {Status: batch.TargetPending}}); g != batch.StatusRunning {
		t.Fatalf("%s", g)
	}
}

func TestCountSummary_IgnoresInFlight(t *testing.T) {
	s := batch.CountSummary([]batch.Target{
		{Status: batch.TargetSuccess},
		{Status: batch.TargetTimeout},
		{Status: batch.TargetPending},
	})
	if s.Success != 1 || s.Timeout != 1 || s.Failed != 0 {
		t.Fatalf("%+v", s)
	}
}
