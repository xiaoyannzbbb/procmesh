package schedule

import (
	"strconv"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

func ValidateCron(expr string) error {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) == 0 {
		return nil
	}
	if len(fields) != 5 {
		return errcode.E(errcode.INVALID, "cron must have 5 fields")
	}
	mins := [...]int{0, 0, 1, 1, 0}
	maxs := [...]int{59, 23, 31, 12, 6}
	for i, field := range fields {
		if field == "*" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil || n < mins[i] || n > maxs[i] {
			return errcode.E(errcode.INVALID, "invalid cron field")
		}
	}
	return nil
}

func ValidateTimezone(name string) error {
	if strings.TrimSpace(name) == "" {
		return errcode.E(errcode.INVALID, "timezone required")
	}
	if _, err := time.LoadLocation(name); err != nil {
		return errcode.E(errcode.INVALID, "invalid timezone")
	}
	return nil
}
