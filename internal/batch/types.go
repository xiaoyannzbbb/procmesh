package batch

import "time"

// Type is a batch operation kind.
type Type string

const (
	TypeStart        Type = "START"
	TypeStop         Type = "STOP"
	TypeRestart      Type = "RESTART"
	TypeConfigUpdate Type = "CONFIG_UPDATE"
)

// Status is the aggregate batch status.
type Status string

const (
	StatusPending   Status = "PENDING"
	StatusRunning   Status = "RUNNING"
	StatusCompleted Status = "COMPLETED"
	StatusPartial   Status = "PARTIAL"
	StatusFailed    Status = "FAILED"
)

// TargetStatus is the per-target execution status.
type TargetStatus string

const (
	TargetPending     TargetStatus = "PENDING"
	TargetRunning     TargetStatus = "RUNNING"
	TargetSuccess     TargetStatus = "SUCCESS"
	TargetFailed      TargetStatus = "FAILED"
	TargetTimeout     TargetStatus = "TIMEOUT"
	TargetDenied      TargetStatus = "DENIED"
	TargetConflict    TargetStatus = "CONFLICT"
	TargetUnavailable TargetStatus = "UNAVAILABLE"
	TargetInvalid     TargetStatus = "INVALID"
)

// ProcessNameRef selects a process by node and name.
type ProcessNameRef struct {
	NodeID      string `json:"node_id"`
	ProcessName string `json:"process_name"`
}

// Selector chooses batch targets (union of non-empty fields).
type Selector struct {
	ProcessIDs   []string         `json:"process_ids,omitempty"`
	ProcessNames []ProcessNameRef `json:"process_names,omitempty"`
	AgentGroupID string           `json:"agent_group_id,omitempty"`
	ProcessGroup string           `json:"process_group,omitempty"`
}

// Summary counts terminal target statuses only (PENDING/RUNNING excluded).
type Summary struct {
	Success     int `json:"success"`
	Failed      int `json:"failed"`
	Timeout     int `json:"timeout"`
	Denied      int `json:"denied"`
	Conflict    int `json:"conflict"`
	Unavailable int `json:"unavailable"`
	Invalid     int `json:"invalid"`
}

// Target is one process-level unit within a batch.
type Target struct {
	OperationID      string
	NodeID           string
	ProcessID        string
	ProcessName      string
	Status           TargetStatus
	Error            string
	ExpectedRevision int64
	PayloadJSON      string
	StartedAt        time.Time
	FinishedAt       time.Time
}

// Batch is an entry-agent batch operation and its targets.
type Batch struct {
	BatchID     string
	Operator    string
	SourceAgent string
	Type        Type
	Selector    Selector
	CreatedAt   time.Time
	Status      Status
	Summary     Summary
	Targets     []Target
}

// Rollup computes aggregate Status from target statuses.
// Any PENDING/RUNNING keeps the batch RUNNING; otherwise:
// all SUCCESS → COMPLETED; success==0 && timeout==0 → FAILED; else PARTIAL.
func Rollup(targets []Target) Status {
	success := 0
	timeout := 0
	for _, t := range targets {
		switch t.Status {
		case TargetPending, TargetRunning:
			return StatusRunning
		case TargetSuccess:
			success++
		case TargetTimeout:
			timeout++
		}
	}
	if success == len(targets) {
		return StatusCompleted
	}
	if success == 0 && timeout == 0 {
		return StatusFailed
	}
	return StatusPartial
}

// CountSummary tallies terminal target statuses only.
func CountSummary(targets []Target) Summary {
	var s Summary
	for _, t := range targets {
		switch t.Status {
		case TargetSuccess:
			s.Success++
		case TargetFailed:
			s.Failed++
		case TargetTimeout:
			s.Timeout++
		case TargetDenied:
			s.Denied++
		case TargetConflict:
			s.Conflict++
		case TargetUnavailable:
			s.Unavailable++
		case TargetInvalid:
			s.Invalid++
		}
	}
	return s
}
