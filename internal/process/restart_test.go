package process_test

import (
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/process"
)

func TestDecideRestart_NegativeBackoffUsesMax(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{
		Mode:    process.RestartAlways,
		Backoff: process.Backoff{Initial: time.Second, Max: 3 * time.Second, Multiplier: -2},
	}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, []time.Time{now}, now)
	if !d.Restart || d.Delay != 3*time.Second {
		t.Fatalf("negative delay should clamp to max: %+v", d)
	}
}

func TestCountFailures_IgnoresFutureAndOutsideWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartAlways, MaxRetries: 2, RetryWindow: time.Second}
	fails := []time.Time{now.Add(time.Second), now.Add(-2 * time.Second), now.Add(-100 * time.Millisecond)}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, fails, now)
	if d.Fatal || !d.Restart {
		t.Fatalf("one in-window failure: %+v", d)
	}
}

func TestDecideRestart_CrashLoopBecomesFatal(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartOnFailure, MaxRetries: 3, RetryWindow: time.Minute, Backoff: process.Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2}}
	fails := []time.Time{now.Add(-3 * time.Second), now.Add(-2 * time.Second), now.Add(-1 * time.Second)}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, fails, now)
	if !d.Fatal || d.Restart {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_OnFailureIgnoresCleanExit(t *testing.T) {
	d := process.DecideRestart(process.RestartPolicy{Mode: process.RestartOnFailure}, process.DesiredRunning, process.ObservedExited, 0, nil, time.Now())
	if d.Restart || d.Fatal {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_DesiredStoppedNoRestart(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartAlways}
	d := process.DecideRestart(pol, process.DesiredStopped, process.ObservedExited, 1, nil, now)
	if d.Restart || d.Fatal {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_ObservedMustBeExitedOrBackoff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartAlways}
	for _, obs := range []process.ObservedState{
		process.ObservedStopped,
		process.ObservedStarting,
		process.ObservedRunning,
		process.ObservedStopping,
		process.ObservedFatal,
		process.ObservedUnknown,
	} {
		d := process.DecideRestart(pol, process.DesiredRunning, obs, 1, nil, now)
		if d.Restart || d.Fatal {
			t.Fatalf("observed %s: %+v", obs, d)
		}
	}
}

func TestDecideRestart_NeverModeNoRestart(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartNever}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, nil, now)
	if d.Restart || d.Fatal {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_AlwaysRestartsOnCleanExit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartAlways}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 0, nil, now)
	if !d.Restart || d.Fatal {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_BackoffObservedCanRestart(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartOnFailure}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedBackoff, 1, nil, now)
	if !d.Restart || d.Fatal {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_BackoffDelay(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{
		Mode:        process.RestartOnFailure,
		MaxRetries:  10,
		RetryWindow: time.Minute,
		Backoff:     process.Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2},
	}
	tests := []struct {
		n    int
		want time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
	}
	for _, tt := range tests {
		var fails []time.Time
		for i := 0; i < tt.n; i++ {
			fails = append(fails, now.Add(-time.Duration(i+1)*time.Second))
		}
		d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, fails, now)
		if !d.Restart || d.Fatal || d.Delay != tt.want {
			t.Fatalf("n=%d got %+v want delay %s", tt.n, d, tt.want)
		}
	}
}

func TestDecideRestart_BackoffCapsAtMax(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{
		Mode:        process.RestartAlways,
		RetryWindow: time.Hour,
		Backoff:     process.Backoff{Initial: time.Second, Max: 5 * time.Second, Multiplier: 2},
	}
	fails := []time.Time{
		now.Add(-4 * time.Second),
		now.Add(-3 * time.Second),
		now.Add(-2 * time.Second),
		now.Add(-1 * time.Second),
	}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, fails, now)
	if !d.Restart || d.Fatal || d.Delay != 5*time.Second {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_BackoffDefaults(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartAlways}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, nil, now)
	if !d.Restart || d.Fatal || d.Delay != time.Second {
		t.Fatalf("zero failures default delay: %+v", d)
	}
	fails := []time.Time{now.Add(-time.Second)}
	d = process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, fails, now)
	if !d.Restart || d.Fatal || d.Delay != 2*time.Second {
		t.Fatalf("one failure default delay: %+v", d)
	}
}

func TestDecideRestart_FailuresOutsideWindowIgnored(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{
		Mode:        process.RestartOnFailure,
		MaxRetries:  2,
		RetryWindow: 5 * time.Second,
		Backoff:     process.Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2},
	}
	fails := []time.Time{now.Add(-10 * time.Second), now.Add(-1 * time.Second)}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, fails, now)
	if !d.Restart || d.Fatal || d.Delay != 2*time.Second {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_MaxRetriesZeroNeverFatal(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{
		Mode:        process.RestartOnFailure,
		MaxRetries:  0,
		RetryWindow: time.Minute,
		Backoff:     process.Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2},
	}
	fails := []time.Time{now.Add(-3 * time.Second), now.Add(-2 * time.Second), now.Add(-1 * time.Second)}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, fails, now)
	if !d.Restart || d.Fatal {
		t.Fatalf("%+v", d)
	}
}
