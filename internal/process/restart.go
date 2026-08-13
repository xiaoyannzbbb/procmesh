package process

import (
	"math"
	"time"
)

// RestartDecision is the outcome of DecideRestart.
type RestartDecision struct {
	Restart bool
	Fatal   bool
	Delay   time.Duration
}

// DecideRestart is a pure policy function. Callers pass now; this never reads the clock.
func DecideRestart(pol RestartPolicy, desired DesiredState, observed ObservedState, exitCode int, failures []time.Time, now time.Time) RestartDecision {
	if desired == DesiredStopped {
		return RestartDecision{}
	}
	if observed != ObservedExited && observed != ObservedBackoff {
		return RestartDecision{}
	}
	if pol.Mode == RestartNever {
		return RestartDecision{}
	}
	if pol.Mode == RestartOnFailure && exitCode == 0 {
		return RestartDecision{}
	}

	n := countFailuresInWindow(failures, now, pol.RetryWindow)
	if pol.MaxRetries > 0 && n >= pol.MaxRetries {
		return RestartDecision{Fatal: true}
	}
	return RestartDecision{Restart: true, Delay: backoffDelay(pol.Backoff, n)}
}

func countFailuresInWindow(failures []time.Time, now time.Time, window time.Duration) int {
	n := 0
	for _, t := range failures {
		if t.After(now) {
			continue
		}
		if window <= 0 || !t.Before(now.Add(-window)) {
			n++
		}
	}
	return n
}

func backoffDelay(b Backoff, n int) time.Duration {
	initial := b.Initial
	if initial <= 0 {
		initial = time.Second
	}
	max := b.Max
	if max <= 0 {
		max = time.Minute
	}
	mult := b.Multiplier
	if mult == 0 {
		mult = 2
	}
	delay := float64(initial) * math.Pow(mult, float64(n))
	if math.IsInf(delay, 0) || delay > float64(max) {
		return max
	}
	if delay < 0 {
		return max
	}
	return time.Duration(delay)
}
