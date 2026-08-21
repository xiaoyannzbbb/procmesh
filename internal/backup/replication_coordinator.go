package backup

import (
	"context"
	"errors"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

// FrozenReplicationTask is a durable route with its immutable source payload
// reference. It deliberately contains no payload or sink location.
type FrozenReplicationTask struct {
	TaskID, SourceNodeID, TargetNodeID string
	SnapshotID, SHA256                 string
	Status                             string
}

// FrozenReplicationRun is the metadata-only replication work claimed by the
// current Leader.
type FrozenReplicationRun struct {
	RunID, PolicyID  string
	PolicyRevision   int64
	LeaderTerm       uint64
	LeaseExpiresUnix int64
	MaxConcurrency   int
	Tasks            []FrozenReplicationTask
}

// ReplicationTaskRequest is sent to the source Agent. The source Agent loads
// and verifies the frozen payload before it opens the target Peer RPC.
type ReplicationTaskRequest struct {
	RunID, TaskID, PolicyID    string
	PolicyRevision             int64
	SourceNodeID, TargetNodeID string
	SnapshotID, SHA256         string
	LeaderTerm                 uint64
	LeaseExpiresUnix           int64
}

type ReplicationTaskUpdate struct {
	RunID, TaskID, SourceNodeID, TargetNodeID string
	SnapshotID, SHA256                        string
	Status                                    string
	Bytes                                     int64
	ErrorCode, ErrorSummary                   string
	LeaderTerm                                uint64
}

type ReplicationControlPlane interface {
	ClaimReplicationRuns(context.Context, uint64, time.Time) ([]FrozenReplicationRun, error)
	BeginReplicationTask(context.Context, ReplicationTaskUpdate) error
	UpdateReplicationTask(context.Context, ReplicationTaskUpdate) error
}

type ReplicationDispatcher interface {
	DispatchReplicationTask(context.Context, ReplicationTaskRequest) error
}

type ReplicationCoordinatorConfig struct {
	Control     ReplicationControlPlane
	Dispatcher  ReplicationDispatcher
	IsLeader    func() bool
	CurrentTerm func() uint64
	Now         func() time.Time
}

// ReplicationCoordinator dispatches metadata-only route work on the Leader.
// A retry reuses the original task and immutable payload reference.
type ReplicationCoordinator struct {
	Control     ReplicationControlPlane
	Dispatcher  ReplicationDispatcher
	IsLeader    func() bool
	CurrentTerm func() uint64
	Now         func() time.Time
}

func NewReplicationCoordinator(cfg ReplicationCoordinatorConfig) *ReplicationCoordinator {
	return &ReplicationCoordinator{Control: cfg.Control, Dispatcher: cfg.Dispatcher, IsLeader: cfg.IsLeader, CurrentTerm: cfg.CurrentTerm, Now: cfg.Now}
}

func (c *ReplicationCoordinator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *ReplicationCoordinator) leader() bool { return c.IsLeader == nil || c.IsLeader() }

func (c *ReplicationCoordinator) term() uint64 {
	if c.CurrentTerm == nil {
		return 1
	}
	return c.CurrentTerm()
}

func (c *ReplicationCoordinator) Tick(ctx context.Context) error {
	if c == nil || c.Control == nil || c.Dispatcher == nil || !c.leader() {
		return nil
	}
	term := c.term()
	if term == 0 {
		return nil
	}
	runs, err := c.Control.ClaimReplicationRuns(ctx, term, c.now())
	if err != nil {
		return err
	}
	for _, run := range runs {
		c.DispatchRun(ctx, run)
	}
	return nil
}

func retryableReplicationTask(status string) bool {
	switch status {
	case "PENDING", "FAILED", "TIMEOUT", "UNAVAILABLE", "CONFIG_MISSING", "SKIPPED", "RETENTION_FAILED":
		return true
	default:
		return false
	}
}

func (c *ReplicationCoordinator) DispatchRun(ctx context.Context, run FrozenReplicationRun) {
	if c == nil || c.Control == nil || c.Dispatcher == nil {
		return
	}
	if run.LeaderTerm == 0 {
		run.LeaderTerm = c.term()
	}
	if run.LeaseExpiresUnix == 0 {
		run.LeaseExpiresUnix = c.now().Add(30 * time.Second).Unix()
	}
	limit := run.MaxConcurrency
	if limit <= 0 {
		limit = 1
	}
	lease := time.Until(time.Unix(run.LeaseExpiresUnix, 0))
	if c.Now != nil {
		lease = time.Unix(run.LeaseExpiresUnix, 0).Sub(c.now())
	}
	if lease <= 0 {
		return
	}
	leaseCtx, cancel := context.WithTimeout(ctx, lease)
	defer cancel()
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, task := range run.Tasks {
		if !retryableReplicationTask(task.Status) {
			continue
		}
		task := task
		select {
		case sem <- struct{}{}:
		case <-leaseCtx.Done():
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			req := ReplicationTaskRequest{RunID: run.RunID, TaskID: task.TaskID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, SourceNodeID: task.SourceNodeID, TargetNodeID: task.TargetNodeID, SnapshotID: task.SnapshotID, SHA256: task.SHA256, LeaderTerm: run.LeaderTerm, LeaseExpiresUnix: run.LeaseExpiresUnix}
			if err := c.Control.BeginReplicationTask(leaseCtx, ReplicationTaskUpdate{RunID: req.RunID, TaskID: req.TaskID, SourceNodeID: req.SourceNodeID, TargetNodeID: req.TargetNodeID, SnapshotID: req.SnapshotID, SHA256: req.SHA256, Status: "RUNNING", LeaderTerm: req.LeaderTerm}); err != nil {
				return
			}
			if leaseCtx.Err() != nil || c.now().Unix() >= req.LeaseExpiresUnix {
				abortErr := leaseCtx.Err()
				if abortErr == nil {
					abortErr = context.DeadlineExceeded
				}
				status, code, summary := classifyReplicationFailure(abortErr, abortErr)
				_ = c.Control.UpdateReplicationTask(ctx, ReplicationTaskUpdate{RunID: req.RunID, TaskID: req.TaskID, SourceNodeID: req.SourceNodeID, TargetNodeID: req.TargetNodeID, SnapshotID: req.SnapshotID, SHA256: req.SHA256, Status: status, ErrorCode: code, ErrorSummary: summary, LeaderTerm: req.LeaderTerm})
				return
			}
			if err := c.Dispatcher.DispatchReplicationTask(leaseCtx, req); err != nil {
				status, code, summary := classifyReplicationFailure(err, leaseCtx.Err())
				_ = c.Control.UpdateReplicationTask(ctx, ReplicationTaskUpdate{RunID: req.RunID, TaskID: req.TaskID, SourceNodeID: req.SourceNodeID, TargetNodeID: req.TargetNodeID, SnapshotID: req.SnapshotID, SHA256: req.SHA256, Status: status, ErrorCode: code, ErrorSummary: summary, LeaderTerm: req.LeaderTerm})
			}
		}()
	}
	wg.Wait()
}

func classifyReplicationFailure(err, contextErr error) (status, code, summary string) {
	if errors.Is(contextErr, context.DeadlineExceeded) || errcode.Is(err, errcode.TIMEOUT) {
		return "TIMEOUT", "TIMEOUT", "replication route timed out"
	}
	if errcode.Is(err, errcode.CONFLICT) || connectErrorInfoCode(err) == string(errcode.CONFLICT) {
		return "FAILED", "CONFLICT", "frozen snapshot conflict"
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeDeadlineExceeded {
		return "TIMEOUT", "TIMEOUT", "replication route timed out"
	}
	return "UNAVAILABLE", "UNAVAILABLE", "replication route unavailable"
}

func connectErrorInfoCode(err error) string {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return ""
	}
	for _, detail := range connectErr.Details() {
		value, detailErr := detail.Value()
		if detailErr != nil {
			continue
		}
		if info, ok := value.(*procmeshv1.ErrorInfo); ok {
			return info.GetCode()
		}
	}
	return ""
}
