package process

import (
	"strconv"
	"time"

	"github.com/qleelulu/procmesh/internal/health"
)

// ProcessSpec is the desired configuration for a managed process.
// Empty-field defaults (applied by callers, not ValidateSpec):
// Instances=1, StopSignal=SIGTERM, KillSignal=SIGKILL, StopTimeout=10s, Restart.Mode=on-failure.
type ProcessSpec struct {
	ProcessID        string
	Name             string
	OwnerAgentID     string
	Group            string
	Command          string
	Args             []string
	WorkingDirectory string
	RunAsUser        string
	Environment      map[string]string
	Instances        int
	Restart          RestartPolicy
	Health           HealthCheckSpec
	Log              LogPolicy
	Resources        ResourceLimit
	StartupPriority  int
	Dependencies     []Dependency
	Autostart        bool
	StopSignal       string
	StopTimeout      time.Duration
	KillSignal       string
	LatestRevision   int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ApplyDefaults fills empty spec fields. ValidateSpec still rejects explicit Instances=0
// if this is not called first.
func ApplyDefaults(s *ProcessSpec) {
	if s == nil {
		return
	}
	if s.Instances == 0 {
		s.Instances = 1
	}
	if s.StopSignal == "" {
		s.StopSignal = "SIGTERM"
	}
	if s.KillSignal == "" {
		s.KillSignal = "SIGKILL"
	}
	if s.StopTimeout == 0 {
		s.StopTimeout = 10 * time.Second
	}
	if s.Restart.Mode == "" {
		s.Restart.Mode = RestartOnFailure
	}
	s.Log = s.Log.WithDefaults()
}

type RestartPolicy struct {
	Mode        RestartMode
	MaxRetries  int
	RetryWindow time.Duration
	Backoff     Backoff
}

type Backoff struct {
	Initial    time.Duration
	Max        time.Duration
	Multiplier float64
}

type RestartMode string

const (
	RestartNever     RestartMode = "never"
	RestartAlways    RestartMode = "always"
	RestartOnFailure RestartMode = "on-failure"
)

type DesiredState string

const (
	DesiredRunning DesiredState = "RUNNING"
	DesiredStopped DesiredState = "STOPPED"
)

type ObservedState string

const (
	ObservedStopped  ObservedState = "STOPPED"
	ObservedStarting ObservedState = "STARTING"
	ObservedRunning  ObservedState = "RUNNING"
	ObservedStopping ObservedState = "STOPPING"
	ObservedExited   ObservedState = "EXITED"
	ObservedBackoff  ObservedState = "BACKOFF"
	ObservedFatal    ObservedState = "FATAL"
	ObservedUnknown  ObservedState = "UNKNOWN"
)

type HealthState = health.HealthState

const (
	HealthHealthy   = health.HealthHealthy
	HealthUnhealthy = health.HealthUnhealthy
	HealthUnknown   = health.HealthUnknown
)

type Instance struct {
	InstanceID     string
	ProcessID      string
	Ordinal        int
	PID            int
	ShimPID        int
	Desired        DesiredState
	Observed       ObservedState
	Health         HealthState
	StartedAt      *time.Time
	ExitAt         *time.Time
	ExitCode       *int
	RestartCount   int
	ActiveRevision int64
	BootID         string
}

func MakeInstanceID(processID string, ordinal int) string {
	return processID + ":" + strconv.Itoa(ordinal)
}

type HealthCheckSpec = health.HealthCheckSpec

type LogPolicy struct {
	MaxSize  int64
	MaxFiles int
	MaxAge   time.Duration
	Compress bool
}

// WithDefaults fills empty log policy fields: 100MiB, 10 files, 7 days, compress.
func (p LogPolicy) WithDefaults() LogPolicy {
	empty := p == LogPolicy{}
	if p.MaxSize == 0 {
		p.MaxSize = 100 << 20
	}
	if p.MaxFiles == 0 {
		p.MaxFiles = 10
	}
	if p.MaxAge == 0 {
		p.MaxAge = 7 * 24 * time.Hour
	}
	if empty {
		p.Compress = true
	}
	return p
}

type ResourceLimit struct {
	CPUQuotaMillis int64
	MemoryBytes    int64
	OpenFiles      int64
}

type Dependency struct {
	ProcessName string
	Condition   DepCondition
}
