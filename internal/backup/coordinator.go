package backup

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
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
}

// ScheduledRunClaimer is an optional stronger control-plane capability. The
// production Raft adapter implements it to atomically commit fire+run state.
type ScheduledRunClaimer interface {
	ClaimScheduledRun(context.Context, string, PolicyView, uint64, time.Time) (FrozenRun, bool, error)
}

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
		c.DispatchRun(ctx, FrozenRun{RunID: runID, PolicyID: view.Policy.PolicyID, PolicyRevision: effectiveRevision(view), TargetNodeIDs: targets, Sink: view.Policy.Sink, DestinationProfile: view.Policy.DestinationProfile, MaxConcurrency: view.Policy.MaxConcurrency, LeaderTerm: term})
	}
	return nil
}

func effectiveRevision(v PolicyView) int64 {
	if v.Revision != 0 {
		return v.Revision
	}
	return v.Policy.Revision
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
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, nodeID := range run.TargetNodeIDs {
		nodeID := nodeID
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			task := BackupTaskRequest{RunID: run.RunID, TaskID: "task-" + nodeID, PolicyID: run.PolicyID, NodeID: nodeID, PolicyRevision: run.PolicyRevision, Sink: run.Sink, DestinationProfile: run.DestinationProfile, LeaderTerm: run.LeaderTerm, LeaseExpiresUnix: run.LeaseExpiresUnix}
			if err := c.Dispatcher.DispatchBackupTask(ctx, task); err != nil {
				_ = c.Control.UpdateTask(ctx, TaskUpdate{RunID: run.RunID, TaskID: task.TaskID, NodeID: nodeID, Status: "UNAVAILABLE", ErrorCode: "UNAVAILABLE", ErrorSummary: err.Error(), LeaderTerm: run.LeaderTerm})
			}
		}()
	}
	wg.Wait()
}

func (c *Coordinator) DispatchRun(ctx context.Context, run FrozenRun) {
	if run.LeaderTerm == 0 {
		run.LeaderTerm = c.term()
	}
	if run.LeaseExpiresUnix == 0 {
		run.LeaseExpiresUnix = c.now().Add(30 * time.Second).Unix()
	}
	c.dispatchFrozenTargets(ctx, run)
}
