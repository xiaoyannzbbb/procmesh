package backup

import "context"

// ClusterTaskRequest contains parameters for running a cluster backup task on a local Agent.
type ClusterTaskRequest struct {
	RunID              string
	TaskID             string
	NodeID             string
	PolicyRevision     int64
	Sink               string
	DestinationProfile string
	PolicyID           string
	LeaderTerm         uint64
	LeaseExpiresUnix   int64
	ProcessIDs         []string
}

// TaskResult represents the outcome of a cluster backup task execution.
type TaskResult struct {
	RunID        string
	TaskID       string
	NodeID       string
	SnapshotID   string
	SHA256       string
	Status       string // SUCCESS, FAILED, IN_PROGRESS
	Bytes        int64
	ErrorCode    string
	ErrorSummary string
	LeaderTerm   uint64
	UpdatedUnix  int64
}

// AgentTaskExecutor defines the interface for executing cluster backup tasks locally.
type AgentTaskExecutor interface {
	RunClusterTask(ctx context.Context, req ClusterTaskRequest) (*TaskResult, error)
	GetClusterTask(ctx context.Context, runID, taskID string) (*TaskResult, error)
}
