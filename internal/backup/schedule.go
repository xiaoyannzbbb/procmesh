package backup

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/schedule"
)

// Next returns the next fire time strictly after from for a 5-field cron
// (min hour day month weekday). Tokens are "*" or a single integer.
// Empty expr returns a zero Time and nil (disabled).
func Next(cronExpr string, from time.Time) (time.Time, error) {
	cronExpr = strings.TrimSpace(cronExpr)
	if cronExpr == "" {
		return time.Time{}, nil
	}
	if err := schedule.ValidateCron(cronExpr); err != nil {
		return time.Time{}, err
	}
	fields := strings.Fields(cronExpr)
	if len(fields) != 5 {
		return time.Time{}, errcode.E(errcode.INVALID, "cron must have 5 fields")
	}
	min, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, err
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, err
	}
	dom, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, err
	}
	mon, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, err
	}
	dow, err := parseCronField(fields[4], 0, 6)
	if err != nil {
		return time.Time{}, err
	}
	loc := from.Location()
	t := from.Truncate(time.Minute).Add(time.Minute)
	deadline := t.Add(366 * 24 * time.Hour)
	for !t.After(deadline) {
		if cronMatch(t.In(loc), min, hour, dom, mon, dow) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, errcode.E(errcode.INVALID, "no matching cron time")
}

type cronField struct {
	any bool
	n   int
}

func parseCronField(tok string, min, max int) (cronField, error) {
	if tok == "*" {
		return cronField{any: true}, nil
	}
	n, err := strconv.Atoi(tok)
	if err != nil || n < min || n > max {
		return cronField{}, errcode.E(errcode.INVALID, "invalid cron field")
	}
	return cronField{n: n}, nil
}

func cronMatch(t time.Time, min, hour, dom, mon, dow cronField) bool {
	if !min.any && t.Minute() != min.n {
		return false
	}
	if !hour.any && t.Hour() != hour.n {
		return false
	}
	if !dom.any && t.Day() != dom.n {
		return false
	}
	if !mon.any && int(t.Month()) != mon.n {
		return false
	}
	if !dow.any && int(t.Weekday()) != dow.n {
		return false
	}
	return true
}

// TickSchedule creates an FS snapshot when the configured cron is due.
func (e *Engine) TickSchedule(ctx context.Context) error {
	if e == nil {
		return nil
	}
	next, err := Next(e.Schedule, e.now().Add(-time.Minute))
	if err != nil {
		return err
	}
	if next.IsZero() || next.After(e.now()) {
		return nil
	}
	_, err = e.Create(ctx, CreateOpts{Sink: "fs"})
	return err
}
