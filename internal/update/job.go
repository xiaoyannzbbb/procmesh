package update

import "time"

// JobStatus is the aggregate rolling-update job status.
type JobStatus string

const (
	JobPending   JobStatus = "PENDING"
	JobRunning   JobStatus = "RUNNING"
	JobCompleted JobStatus = "COMPLETED"
	JobPartial   JobStatus = "PARTIAL"
	JobFailed    JobStatus = "FAILED"
)

// TargetStatus is the per-node rolling-update status.
type TargetStatus string

const (
	TargetPending   TargetStatus = "PENDING"
	TargetRunning   TargetStatus = "RUNNING"
	TargetSuccess   TargetStatus = "SUCCESS"
	TargetFailed    TargetStatus = "FAILED"
	TargetTimeout   TargetStatus = "TIMEOUT"
	TargetConflict  TargetStatus = "CONFLICT"
	TargetSkipped   TargetStatus = "SKIPPED"
	TargetCancelled TargetStatus = "CANCELLED"
)

// TargetSpec is a node considered when creating a job.
type TargetSpec struct {
	NodeID     string
	Hostname   string
	SkipReason string
}

// JobSummary counts terminal target statuses (PENDING/RUNNING excluded).
type JobSummary struct {
	Success   int `json:"success"`
	Failed    int `json:"failed"`
	Timeout   int `json:"timeout"`
	Conflict  int `json:"conflict"`
	Skipped   int `json:"skipped"`
	Cancelled int `json:"cancelled"`
}

// Target is one node within a rolling update job.
type Target struct {
	OperationID string
	NodeID      string
	Hostname    string
	Status      TargetStatus
	SkipReason  string
	Error       string
	OrderIndex  int
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Job is an entry-local cluster rolling update.
type Job struct {
	JobID           string
	OperationID     string
	Operator        string
	SourceAgent     string
	Pin             Pin
	CreatedAt       time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
	Status          JobStatus
	Summary         JobSummary
	CancelRemaining bool
	Targets         []Target
}

// RollupJob computes aggregate status from targets after the worker has stopped.
// PENDING leftovers after halt are not treated as still running, but an in-flight
// RUNNING target must be reconciled before the job can become terminal.
// Skips and cancels are not failures. Attempted = SUCCESS/FAILED/TIMEOUT/CONFLICT.
func RollupJob(targets []Target, stopped bool) JobStatus {
	success, fail := 0, 0
	pending, running := false, false
	for _, t := range targets {
		switch t.Status {
		case TargetPending:
			pending = true
		case TargetRunning:
			running = true
		case TargetSuccess:
			success++
		case TargetFailed, TargetTimeout, TargetConflict:
			fail++
		}
	}
	if running {
		return JobRunning
	}
	if pending && !stopped {
		return JobRunning
	}
	if fail == 0 {
		return JobCompleted
	}
	if success == 0 {
		return JobFailed
	}
	return JobPartial
}

// CountJobSummary tallies terminal target statuses.
func CountJobSummary(targets []Target) JobSummary {
	var s JobSummary
	for _, t := range targets {
		switch t.Status {
		case TargetSuccess:
			s.Success++
		case TargetFailed:
			s.Failed++
		case TargetTimeout:
			s.Timeout++
		case TargetConflict:
			s.Conflict++
		case TargetSkipped:
			s.Skipped++
		case TargetCancelled:
			s.Cancelled++
		}
	}
	return s
}
