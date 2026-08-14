package localhttp

// DTOs for HTTP JSON (snake_case). Do not leak store types.

type CreateProcessRequest struct {
	OperationID      string      `json:"operation_id"`
	Operator         string      `json:"operator"`
	ExpectedRevision int64       `json:"expected_revision"`
	Spec             ProcessSpec `json:"spec"`
}

type ProcessSpec struct {
	ProcessID        string            `json:"process_id"`
	Name             string            `json:"name"`
	OwnerAgentID     string            `json:"owner_agent_id,omitempty"`
	Group            string            `json:"group,omitempty"`
	Command          string            `json:"command"`
	Args             []string          `json:"args"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	RunAsUser        string            `json:"run_as_user,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Instances        int               `json:"instances"`
	Autostart        bool              `json:"autostart,omitempty"`
	StopSignal       string            `json:"stop_signal,omitempty"`
	KillSignal       string            `json:"kill_signal,omitempty"`
	StopTimeoutMs    int64             `json:"stop_timeout_ms,omitempty"`
	StartupPriority  int               `json:"startup_priority,omitempty"`
	Restart          RestartPolicyDTO  `json:"restart,omitempty"`
	Health           HealthCheckDTO    `json:"health,omitempty"`
	Log              LogPolicyDTO      `json:"log,omitempty"`
	Resources        ResourceLimitDTO  `json:"resources,omitempty"`
	Dependencies     []DependencyDTO   `json:"dependencies,omitempty"`
	LatestRevision   int64             `json:"latest_revision,omitempty"`
}

type RestartPolicyDTO struct {
	Mode          string     `json:"mode,omitempty"`
	MaxRetries    int        `json:"max_retries,omitempty"`
	RetryWindowMs int64      `json:"retry_window_ms,omitempty"`
	Backoff       BackoffDTO `json:"backoff,omitempty"`
}

type BackoffDTO struct {
	InitialMs  int64   `json:"initial_ms,omitempty"`
	MaxMs      int64   `json:"max_ms,omitempty"`
	Multiplier float64 `json:"multiplier,omitempty"`
}

type HealthCheckDTO struct {
	Type              string   `json:"type,omitempty"`
	URL               string   `json:"url,omitempty"`
	Method            string   `json:"method,omitempty"`
	Address           string   `json:"address,omitempty"`
	Command           string   `json:"command,omitempty"`
	ExpectedStatus    int      `json:"expected_status,omitempty"`
	Args              []string `json:"args,omitempty"`
	InitialDelayMs    int64    `json:"initial_delay_ms,omitempty"`
	IntervalMs        int64    `json:"interval_ms,omitempty"`
	TimeoutMs         int64    `json:"timeout_ms,omitempty"`
	FailureThreshold  int      `json:"failure_threshold,omitempty"`
	SuccessThreshold  int      `json:"success_threshold,omitempty"`
	RestartOnFailure  bool     `json:"restart_on_failure,omitempty"`
	RestartCooldownMs int64    `json:"restart_cooldown_ms,omitempty"`
}

type LogPolicyDTO struct {
	MaxSize       int64 `json:"max_size,omitempty"`
	MaxFiles      int   `json:"max_files,omitempty"`
	MaxAgeSeconds int64 `json:"max_age_seconds,omitempty"`
	Compress      bool  `json:"compress,omitempty"`
}

type ResourceLimitDTO struct {
	CPUQuotaMillis int64 `json:"cpu_quota_millis,omitempty"`
	MemoryBytes    int64 `json:"memory_bytes,omitempty"`
	OpenFiles      int64 `json:"open_files,omitempty"`
}

type DependencyDTO struct {
	ProcessName string `json:"process_name,omitempty"`
	Condition   string `json:"condition,omitempty"`
}

type MutationRequest struct {
	OperationID string `json:"operation_id"`
	Operator    string `json:"operator"`
}

type AdoptInstanceRequest struct {
	OperationID string `json:"operation_id"`
	Operator    string `json:"operator"`
	PID         int    `json:"pid"`
}

type ListProcessesResponse struct {
	Processes []ProcessResponse `json:"processes"`
}

type ProcessResponse struct {
	ProcessID string      `json:"process_id"`
	Spec      ProcessSpec `json:"spec"`
	Instances []Instance  `json:"instances"`
}

type Instance struct {
	InstanceID string `json:"instance_id"`
	Ordinal    int    `json:"ordinal"`
	Desired    string `json:"desired"`
	Observed   string `json:"observed"`
	Health     string `json:"health"`
	PID        int    `json:"pid"`
}

type TailLogsResponse struct {
	Lines []string `json:"lines"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
