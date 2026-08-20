package backup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

// PolicyView is the immutable policy/target view used when a scheduled run is
// claimed. TargetNodeIDs are resolved by the control-plane adapter before the
// view is returned, and are copied into the run at claim time.
type PolicyView struct {
	Policy        Policy
	Revision      int64
	TargetNodeIDs []string
}

type BackupTaskRequest struct {
	RunID, TaskID, PolicyID, NodeID string
	PolicyRevision                  int64
	Sink, DestinationProfile        string
	LeaderTerm                      uint64
	LeaseExpiresUnix                int64
}

type TaskUpdate struct {
	RunID, TaskID, NodeID, Status, SnapshotID, SHA256 string
	Bytes                                             int64
	ErrorCode, ErrorSummary                           string
	LeaderTerm                                        uint64
}

type ControlPlane interface {
	ListEnabledBackupPolicies(context.Context) ([]PolicyView, error)
	ClaimFire(context.Context, string, string, uint64, time.Time) (runID string, claimed bool, err error)
	UpdateTask(context.Context, TaskUpdate) error
}

type AgentDispatcher interface {
	DispatchBackupTask(context.Context, BackupTaskRequest) error
}

// FrozenRun is the durable, non-secret run configuration returned by an
// atomic scheduler claim. It is the sole source for recovery dispatch.
type FrozenRun struct {
	RunID, PolicyID, Sink, DestinationProfile string
	PolicyRevision                            int64
	TargetNodeIDs                             []string
	MaxConcurrency                            int
	LeaderTerm                                uint64
	LeaseExpiresUnix                          int64
	TimeoutSeconds                            int
	UnavailablePolicy                         string
}

// ScheduledRunClaimer is an optional stronger control-plane capability. The
// production Raft adapter implements it to atomically commit fire+run state.
type ScheduledRunClaimer interface {
	ClaimScheduledRun(context.Context, string, PolicyView, uint64, time.Time) (FrozenRun, bool, error)
}

// RunRecovery claims expired running runs for the current Leader term and
// returns only targets that still need work.
type RunRecovery interface {
	ClaimRecoverableRuns(context.Context, uint64, time.Time) ([]FrozenRun, error)
}

// TaskOutcomeError means the dispatcher already persisted a terminal task
// result. The coordinator uses it to drive fail-fast without overwriting that
// result with a transport status.
type TaskOutcomeError struct{ Status string }

func (e *TaskOutcomeError) Error() string { return "backup task completed with status " + e.Status }

// BackupRunRequest is optional coordinator wiring for adapters that need the
// scheduler to persist a run record after ClaimFire.
type BackupRunRequest struct {
	RunID, PolicyID string
	PolicyRevision  int64
	TargetNodeIDs   []string
	Term            uint64
	CreatedAt       time.Time
}

type RunCreator interface {
	CreateBackupRun(context.Context, BackupRunRequest) error
}

type CoordinatorConfig struct {
	Control     ControlPlane
	Dispatcher  AgentDispatcher
	RunCreator  RunCreator
	IsLeader    func() bool
	CurrentTerm func() uint64
	Now         func() time.Time
}

// Coordinator schedules cluster backup fires. It is deliberately independent
// of Raft and control packages; the agent supplies those adapters.
type Coordinator struct {
	Control     ControlPlane
	Dispatcher  AgentDispatcher
	RunCreator  RunCreator
	IsLeader    func() bool
	CurrentTerm func() uint64
	Now         func() time.Time

	mu       sync.Mutex
	dispatch map[string]uint64 // fire key -> term last dispatched
}

func NewCoordinator(cfg CoordinatorConfig) *Coordinator {
	return &Coordinator{Control: cfg.Control, Dispatcher: cfg.Dispatcher, RunCreator: cfg.RunCreator,
		IsLeader: cfg.IsLeader, CurrentTerm: cfg.CurrentTerm, Now: cfg.Now, dispatch: map[string]uint64{}}
}

func (c *Coordinator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Coordinator) leader() bool { return c.IsLeader == nil || c.IsLeader() }
func (c *Coordinator) term() uint64 {
	if c.CurrentTerm == nil {
		return 1
	}
	return c.CurrentTerm()
}

// Tick evaluates all enabled policies once. It is safe to call repeatedly for
// the same minute; claims are durable and dispatch is term-scoped in memory.
func (c *Coordinator) Tick(ctx context.Context) error {
	if c == nil || c.Control == nil || c.Dispatcher == nil || !c.leader() {
		return nil
	}
	term := c.term()
	if term == 0 {
		return nil
	}
	now := c.now()
	if recovery, ok := c.Control.(RunRecovery); ok {
		runs, err := recovery.ClaimRecoverableRuns(ctx, term, now)
		if err != nil {
			return err
		}
		for _, run := range runs {
			c.DispatchRun(ctx, run)
		}
	}
	policies, err := c.Control.ListEnabledBackupPolicies(ctx)
	if err != nil {
		return err
	}
	for _, view := range policies {
		if !view.Policy.Enabled || view.Policy.PolicyID == "" || view.Policy.ScheduleCron == "" {
			continue
		}
		fire, err := PreviousOrEqualInTimezone(view.Policy.ScheduleCron, view.Policy.Timezone, now)
		if err != nil {
			return err
		}
		if fire.IsZero() || fire.After(now) {
			continue
		}
		key := fmt.Sprintf("%s:%d", view.Policy.PolicyID, fire.Unix())
		if c.wasDispatched(key, term) {
			continue
		}
		if atomic, ok := c.Control.(ScheduledRunClaimer); ok {
			run, acquired, err := atomic.ClaimScheduledRun(ctx, key, view, term, now)
			if err != nil {
				return err
			}
			if !acquired {
				continue
			}
			c.markDispatched(key, term)
			c.DispatchRun(ctx, run)
			continue
		}
		runID, claimed, err := c.Control.ClaimFire(ctx, key, view.Policy.PolicyID, term, now)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		targets := append([]string(nil), view.TargetNodeIDs...)
		sort.Strings(targets)
		// A reused fire already has a durable run. Re-creating it can return a
		// conflict because the original timestamps are immutable; recovery must
		// continue with the existing run/task IDs instead of dropping dispatch.
		if claimed && c.RunCreator != nil {
			if err := c.RunCreator.CreateBackupRun(ctx, BackupRunRequest{RunID: runID, PolicyID: view.Policy.PolicyID, PolicyRevision: effectiveRevision(view), TargetNodeIDs: targets, Term: term, CreatedAt: now}); err != nil {
				continue
			}
		}
		c.markDispatched(key, term)
		timeout := effectiveTimeout(view.Policy.TimeoutSeconds)
		c.DispatchRun(ctx, FrozenRun{RunID: runID, PolicyID: view.Policy.PolicyID, PolicyRevision: effectiveRevision(view), TargetNodeIDs: targets, Sink: view.Policy.Sink, DestinationProfile: view.Policy.DestinationProfile, MaxConcurrency: view.Policy.MaxConcurrency, LeaderTerm: term, TimeoutSeconds: timeout, UnavailablePolicy: view.Policy.UnavailablePolicy, LeaseExpiresUnix: now.Add(time.Duration(timeout) * time.Second).Unix()})
	}
	return nil
}

func effectiveRevision(v PolicyView) int64 {
	if v.Revision != 0 {
		return v.Revision
	}
	return v.Policy.Revision
}

func effectiveTimeout(seconds int) int {
	if seconds <= 0 {
		return 30
	}
	return seconds
}

func (c *Coordinator) wasDispatched(key string, term uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dispatch == nil {
		c.dispatch = map[string]uint64{}
	}
	return c.dispatch[key] == term
}
func (c *Coordinator) markDispatched(key string, term uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dispatch == nil {
		c.dispatch = map[string]uint64{}
	}
	c.dispatch[key] = term
}

func (c *Coordinator) dispatchFrozenTargets(ctx context.Context, run FrozenRun) {
	limit := run.MaxConcurrency
	if limit <= 0 {
		limit = 1
	}
	leaseDuration := time.Unix(run.LeaseExpiresUnix, 0).Sub(c.now())
	leaseCtx, leaseCancel := context.WithTimeout(ctx, leaseDuration)
	defer leaseCancel()
	runCtx, failFastCancel := context.WithCancel(leaseCtx)
	defer failFastCancel()
	failFast := run.UnavailablePolicy == "FAIL_FAST"

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var failureMu sync.Mutex
	failFastTriggered := false
	markFailFast := func() {
		failureMu.Lock()
		failFastTriggered = true
		failureMu.Unlock()
		failFastCancel()
	}
	wasFailFast := func() bool {
		failureMu.Lock()
		defer failureMu.Unlock()
		return failFastTriggered
	}
	statusAfterCancel := func() string {
		if errors.Is(leaseCtx.Err(), context.DeadlineExceeded) {
			return "TIMEOUT"
		}
		if wasFailFast() {
			return "SKIPPED"
		}
		return "UNAVAILABLE"
	}
	updateCanceled := func(nodeID string) {
		status := statusAfterCancel()
		_ = c.Control.UpdateTask(ctx, TaskUpdate{RunID: run.RunID, TaskID: "task-" + nodeID, NodeID: nodeID, Status: status, ErrorCode: status, ErrorSummary: contextStatusSummary(status), LeaderTerm: run.LeaderTerm})
	}

dispatchLoop:
	for i, nodeID := range run.TargetNodeIDs {
		select {
		case sem <- struct{}{}:
			if runCtx.Err() != nil {
				<-sem
				for _, remaining := range run.TargetNodeIDs[i:] {
					updateCanceled(remaining)
				}
				break dispatchLoop
			}
		case <-runCtx.Done():
			for _, remaining := range run.TargetNodeIDs[i:] {
				updateCanceled(remaining)
			}
			break dispatchLoop
		}
		nodeID := nodeID
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			task := BackupTaskRequest{RunID: run.RunID, TaskID: "task-" + nodeID, PolicyID: run.PolicyID, NodeID: nodeID, PolicyRevision: run.PolicyRevision, Sink: run.Sink, DestinationProfile: run.DestinationProfile, LeaderTerm: run.LeaderTerm, LeaseExpiresUnix: run.LeaseExpiresUnix}
			if err := c.Dispatcher.DispatchBackupTask(runCtx, task); err != nil {
				var outcome *TaskOutcomeError
				if errors.As(err, &outcome) {
					if failFast && outcome.Status != "SUCCESS" && outcome.Status != "SUCCEEDED" {
						markFailFast()
					}
					return
				}
				status := "UNAVAILABLE"
				if runCtx.Err() != nil {
					status = statusAfterCancel()
				}
				_ = c.Control.UpdateTask(ctx, TaskUpdate{RunID: run.RunID, TaskID: task.TaskID, NodeID: nodeID, Status: status, ErrorCode: status, ErrorSummary: boundedTaskError(err), LeaderTerm: run.LeaderTerm})
				if failFast && status != "TIMEOUT" {
					markFailFast()
				}
			}
		}()
	}
	wg.Wait()
}

func boundedTaskError(err error) string {
	if err == nil {
		return ""
	}
	const maxBytes = 2048
	summary := err.Error()
	if len(summary) <= maxBytes {
		return summary
	}
	summary = summary[:maxBytes]
	for !utf8.ValidString(summary) {
		summary = summary[:len(summary)-1]
	}
	return summary
}

func contextStatusSummary(status string) string {
	if status == "TIMEOUT" {
		return "backup task lease expired"
	}
	if status == "SKIPPED" {
		return "skipped after fail-fast failure"
	}
	return "backup task unavailable"
}

func (c *Coordinator) DispatchRun(ctx context.Context, run FrozenRun) {
	if run.LeaderTerm == 0 {
		run.LeaderTerm = c.term()
	}
	if run.LeaseExpiresUnix == 0 {
		run.TimeoutSeconds = effectiveTimeout(run.TimeoutSeconds)
		run.LeaseExpiresUnix = c.now().Add(time.Duration(run.TimeoutSeconds) * time.Second).Unix()
	}
	c.dispatchFrozenTargets(ctx, run)
}
