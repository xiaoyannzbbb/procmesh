package process

import (
	"strconv"
	"time"
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

type HealthState string

const (
	HealthHealthy   HealthState = "HEALTHY"
	HealthUnhealthy HealthState = "UNHEALTHY"
	HealthUnknown   HealthState = "UNKNOWN"
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

type HealthCheckSpec struct {
	Type             string
	URL              string
	Method           string
	ExpectedStatus   int
	Address          string
	Command          string
	Args             []string
	InitialDelay     time.Duration
	Interval         time.Duration
	Timeout          time.Duration
	FailureThreshold int
	SuccessThreshold int
	RestartOnFailure bool
	RestartCooldown  time.Duration
}

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

type ResourceLimit struct{}

type Dependency struct {
	ProcessName string
	Condition   DepCondition
}
