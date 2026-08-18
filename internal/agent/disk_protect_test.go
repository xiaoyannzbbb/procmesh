package agent

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/logmgr"
)

func TestHistoryWritesPaused(t *testing.T) {
	tests := []struct {
		name   string
		policy logmgr.Policy
		used   float64
		want   bool
	}{
		{
			name:   "below threshold",
			policy: logmgr.Policy{EmergencyPercent: 93, EmergencyStopWrites: true},
			used:   92.9,
			want:   false,
		},
		{
			name:   "equal to threshold",
			policy: logmgr.Policy{EmergencyPercent: 93, EmergencyStopWrites: true},
			used:   93,
			want:   false,
		},
		{
			name:   "above threshold",
			policy: logmgr.Policy{EmergencyPercent: 93, EmergencyStopWrites: true},
			used:   93.1,
			want:   true,
		},
		{
			name:   "emergency stop disabled",
			policy: logmgr.Policy{EmergencyPercent: 93, EmergencyStopWrites: false},
			used:   99,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := historyWritesPaused(tt.policy, tt.used); got != tt.want {
				t.Fatalf("historyWritesPaused() = %t, want %t", got, tt.want)
			}
		})
	}
}
