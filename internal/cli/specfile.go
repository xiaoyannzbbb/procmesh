package cli

import (
	"os"

	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"gopkg.in/yaml.v3"
)

// ProcessSpec mirrors localhttp.ProcessSpec (snake_case YAML/JSON). Do not import localhttp.
type ProcessSpec struct {
	ProcessID        string            `json:"process_id" yaml:"process_id"`
	Name             string            `json:"name" yaml:"name"`
	OwnerAgentID     string            `json:"owner_agent_id,omitempty" yaml:"owner_agent_id,omitempty"`
	Group            string            `json:"group,omitempty" yaml:"group,omitempty"`
	Command          string            `json:"command" yaml:"command"`
	Args             []string          `json:"args" yaml:"args"`
	WorkingDirectory string            `json:"working_directory,omitempty" yaml:"working_directory,omitempty"`
	RunAsUser        string            `json:"run_as_user,omitempty" yaml:"run_as_user,omitempty"`
	Environment      map[string]string `json:"environment,omitempty" yaml:"environment,omitempty"`
	Instances        int               `json:"instances" yaml:"instances"`
	Autostart        bool              `json:"autostart,omitempty" yaml:"autostart,omitempty"`
	StopSignal       string            `json:"stop_signal,omitempty" yaml:"stop_signal,omitempty"`
	KillSignal       string            `json:"kill_signal,omitempty" yaml:"kill_signal,omitempty"`
	StopTimeoutMs    int64             `json:"stop_timeout_ms,omitempty" yaml:"stop_timeout_ms,omitempty"`
	StartupPriority  int               `json:"startup_priority,omitempty" yaml:"startup_priority,omitempty"`
	Restart          RestartPolicyDTO  `json:"restart,omitempty" yaml:"restart,omitempty"`
	Health           HealthCheckDTO    `json:"health,omitempty" yaml:"health,omitempty"`
	Log              LogPolicyDTO      `json:"log,omitempty" yaml:"log,omitempty"`
	Resources        ResourceLimitDTO  `json:"resources,omitempty" yaml:"resources,omitempty"`
	Dependencies     []DependencyDTO   `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	LatestRevision   int64             `json:"latest_revision,omitempty" yaml:"latest_revision,omitempty"`
}

type RestartPolicyDTO struct {
	Mode          string     `json:"mode,omitempty" yaml:"mode,omitempty"`
	MaxRetries    int        `json:"max_retries,omitempty" yaml:"max_retries,omitempty"`
	RetryWindowMs int64      `json:"retry_window_ms,omitempty" yaml:"retry_window_ms,omitempty"`
	Backoff       BackoffDTO `json:"backoff,omitempty" yaml:"backoff,omitempty"`
}

type BackoffDTO struct {
	InitialMs  int64   `json:"initial_ms,omitempty" yaml:"initial_ms,omitempty"`
	MaxMs      int64   `json:"max_ms,omitempty" yaml:"max_ms,omitempty"`
	Multiplier float64 `json:"multiplier,omitempty" yaml:"multiplier,omitempty"`
}

type HealthCheckDTO struct {
	Type              string   `json:"type,omitempty" yaml:"type,omitempty"`
	URL               string   `json:"url,omitempty" yaml:"url,omitempty"`
	Method            string   `json:"method,omitempty" yaml:"method,omitempty"`
	Address           string   `json:"address,omitempty" yaml:"address,omitempty"`
	Command           string   `json:"command,omitempty" yaml:"command,omitempty"`
	ExpectedStatus    int      `json:"expected_status,omitempty" yaml:"expected_status,omitempty"`
	Args              []string `json:"args,omitempty" yaml:"args,omitempty"`
	InitialDelayMs    int64    `json:"initial_delay_ms,omitempty" yaml:"initial_delay_ms,omitempty"`
	IntervalMs        int64    `json:"interval_ms,omitempty" yaml:"interval_ms,omitempty"`
	TimeoutMs         int64    `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
	FailureThreshold  int      `json:"failure_threshold,omitempty" yaml:"failure_threshold,omitempty"`
	SuccessThreshold  int      `json:"success_threshold,omitempty" yaml:"success_threshold,omitempty"`
	RestartOnFailure  bool     `json:"restart_on_failure,omitempty" yaml:"restart_on_failure,omitempty"`
	RestartCooldownMs int64    `json:"restart_cooldown_ms,omitempty" yaml:"restart_cooldown_ms,omitempty"`
}

type LogPolicyDTO struct {
	MaxSize       int64 `json:"max_size,omitempty" yaml:"max_size,omitempty"`
	MaxFiles      int   `json:"max_files,omitempty" yaml:"max_files,omitempty"`
	MaxAgeSeconds int64 `json:"max_age_seconds,omitempty" yaml:"max_age_seconds,omitempty"`
	Compress      bool  `json:"compress,omitempty" yaml:"compress,omitempty"`
}

type ResourceLimitDTO struct {
	CPUQuotaMillis int64 `json:"cpu_quota_millis,omitempty" yaml:"cpu_quota_millis,omitempty"`
	MemoryBytes    int64 `json:"memory_bytes,omitempty" yaml:"memory_bytes,omitempty"`
	OpenFiles      int64 `json:"open_files,omitempty" yaml:"open_files,omitempty"`
}

type DependencyDTO struct {
	ProcessName string `json:"process_name,omitempty" yaml:"process_name,omitempty"`
	Condition   string `json:"condition,omitempty" yaml:"condition,omitempty"`
}

// Load reads a YAML or JSON process spec (P0 DTO snake_case) into proto.
func Load(path string) (*procmeshv1.ProcessSpec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spec ProcessSpec
	if err := yaml.Unmarshal(b, &spec); err != nil {
		return nil, err
	}
	return specToProto(spec), nil
}

func specToProto(s ProcessSpec) *procmeshv1.ProcessSpec {
	out := &procmeshv1.ProcessSpec{
		ProcessId:        s.ProcessID,
		Name:             s.Name,
		OwnerAgentId:     s.OwnerAgentID,
		Group:            s.Group,
		Command:          s.Command,
		Args:             s.Args,
		WorkingDirectory: s.WorkingDirectory,
		RunAsUser:        s.RunAsUser,
		Environment:      s.Environment,
		Instances:        int32(s.Instances),
		Autostart:        s.Autostart,
		StopSignal:       s.StopSignal,
		KillSignal:       s.KillSignal,
		StopTimeoutMs:    s.StopTimeoutMs,
		StartupPriority:  int32(s.StartupPriority),
		LatestRevision:   s.LatestRevision,
	}
	if s.Restart != (RestartPolicyDTO{}) {
		out.Restart = &procmeshv1.RestartPolicy{
			Mode:          s.Restart.Mode,
			MaxRetries:    int32(s.Restart.MaxRetries),
			RetryWindowMs: s.Restart.RetryWindowMs,
		}
		if s.Restart.Backoff != (BackoffDTO{}) {
			out.Restart.Backoff = &procmeshv1.Backoff{
				InitialMs:  s.Restart.Backoff.InitialMs,
				MaxMs:      s.Restart.Backoff.MaxMs,
				Multiplier: s.Restart.Backoff.Multiplier,
			}
		}
	}
	if healthSet(s.Health) {
		out.Health = &procmeshv1.HealthCheck{
			Type:              s.Health.Type,
			Url:               s.Health.URL,
			Method:            s.Health.Method,
			Address:           s.Health.Address,
			Command:           s.Health.Command,
			ExpectedStatus:    int32(s.Health.ExpectedStatus),
			Args:              s.Health.Args,
			InitialDelayMs:    s.Health.InitialDelayMs,
			IntervalMs:        s.Health.IntervalMs,
			TimeoutMs:         s.Health.TimeoutMs,
			FailureThreshold:  int32(s.Health.FailureThreshold),
			SuccessThreshold:  int32(s.Health.SuccessThreshold),
			RestartOnFailure:  s.Health.RestartOnFailure,
			RestartCooldownMs: s.Health.RestartCooldownMs,
		}
	}
	if s.Log != (LogPolicyDTO{}) {
		out.Log = &procmeshv1.LogPolicy{
			MaxSize:       s.Log.MaxSize,
			MaxFiles:      int32(s.Log.MaxFiles),
			MaxAgeSeconds: s.Log.MaxAgeSeconds,
			Compress:      s.Log.Compress,
		}
	}
	if s.Resources != (ResourceLimitDTO{}) {
		out.Resources = &procmeshv1.ResourceLimit{
			CpuQuotaMillis: s.Resources.CPUQuotaMillis,
			MemoryBytes:    s.Resources.MemoryBytes,
			OpenFiles:      s.Resources.OpenFiles,
		}
	}
	if len(s.Dependencies) > 0 {
		out.Dependencies = make([]*procmeshv1.Dependency, 0, len(s.Dependencies))
		for _, d := range s.Dependencies {
			out.Dependencies = append(out.Dependencies, &procmeshv1.Dependency{
				ProcessName: d.ProcessName,
				Condition:   d.Condition,
			})
		}
	}
	return out
}

func healthSet(h HealthCheckDTO) bool {
	return h.Type != "" || h.URL != "" || h.Method != "" || h.Address != "" ||
		h.Command != "" || h.ExpectedStatus != 0 || len(h.Args) > 0 ||
		h.InitialDelayMs != 0 || h.IntervalMs != 0 || h.TimeoutMs != 0 ||
		h.FailureThreshold != 0 || h.SuccessThreshold != 0 ||
		h.RestartOnFailure || h.RestartCooldownMs != 0
}
