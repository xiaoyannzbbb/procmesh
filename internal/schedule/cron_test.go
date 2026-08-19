package schedule_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/schedule"
)

func TestValidateCron(t *testing.T) {
	for _, cron := range []string{"", "0 2 * * *", "59 23 31 12 6"} {
		if err := schedule.ValidateCron(cron); err != nil {
			t.Fatalf("cron %q: %v", cron, err)
		}
	}
	for _, cron := range []string{"0 * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 7", "x * * * *"} {
		if err := schedule.ValidateCron(cron); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("cron %q: %v", cron, err)
		}
	}
}

func TestValidateTimezone(t *testing.T) {
	for _, timezone := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		if err := schedule.ValidateTimezone(timezone); err != nil {
			t.Fatalf("timezone %q: %v", timezone, err)
		}
	}
	for _, timezone := range []string{"", "Moon/Base"} {
		if err := schedule.ValidateTimezone(timezone); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("timezone %q: %v", timezone, err)
		}
	}
}
