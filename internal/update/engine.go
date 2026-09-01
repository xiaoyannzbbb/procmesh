package update

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/store"
)

const (
	DefaultWaitTimeout  = 2 * time.Minute
	defaultPollInterval = 200 * time.Millisecond
	jobQueueSize        = 16
)

// NodeApplier applies a pinned update to one node. Tests inject fakes;
// production uses local Applier.Apply or a forwarded ApplyNode RPC.
type NodeApplier interface {
	Apply(ctx context.Context, nodeID string, pin Pin, operationID string) error
}

// Membership is a gossip/freshness clock used after each apply.
type Membership interface {
	Members() []cluster.NodeSummary
	Now() time.Time
}

// Engine persists and runs entry-local cluster rolling update jobs.
type Engine struct {
	DB           *store.Store
	Apply        NodeApplier
	Members      Membership
	SourceAgent  string
	WaitTimeout  time.Duration
	PollInterval time.Duration
	NewID        func() string
	// BindTargets is invoked after operation_ids are assigned and persisted,
	// before enqueue. Callers bind hop identity here.
	BindTargets func(ctx context.Context, targets []Target)

	startOnce sync.Once
	mu        sync.Mutex
	jobs      chan string
}

// Create inserts a job, ordering targets and starting the engine worker.
// At most one RUNNING job is allowed on this entry.
// A non-empty operationID is stored on the job; the same id returns the existing job.
func (e *Engine) Create(ctx context.Context, operator string, pin Pin, specs []TargetSpec, liveLeaderID, operationID string) (Job, error) {
	if strings.TrimSpace(operator) == "" {
		return Job{}, errcode.E(errcode.INVALID, "operator")
	}
	if err := ValidatePin(pin); err != nil {
		return Job{}, err
	}
	if e == nil || e.DB == nil {
		return Job{}, errcode.E(errcode.UNAVAILABLE, "update engine unavailable")
	}
	if job, ok, err := e.existingByOperationID(ctx, operationID); err != nil {
		return Job{}, err
	} else if ok {
		return job, nil
	}

	ordered := OrderTargets(append([]TargetSpec(nil), specs...), e.SourceAgent, liveLeaderID)
	now := time.Now().UTC()
	jobID := e.newID()
	targets := make([]Target, 0, len(ordered))
	pending := 0
	for i, spec := range ordered {
		t := Target{
			OperationID: e.newID(),
			NodeID:      spec.NodeID,
			Hostname:    spec.Hostname,
			OrderIndex:  i,
		}
		if strings.TrimSpace(spec.SkipReason) != "" {
			t.Status = TargetSkipped
			t.SkipReason = spec.SkipReason
			t.FinishedAt = now
		} else {
			t.Status = TargetPending
			pending++
		}
		targets = append(targets, t)
	}

	status := JobCompleted
	if pending > 0 {
		running, err := e.DB.HasRunningUpdateJob(ctx)
		if err != nil {
			return Job{}, err
		}
		if running {
			if job, ok, err := e.existingByOperationID(ctx, operationID); err != nil {
				return Job{}, err
			} else if ok {
				return job, nil
			}
			return Job{}, errcode.E(errcode.CONFLICT, "update job already running")
		}
		status = JobRunning
	}

	pinJSON, err := json.Marshal(clonePin(pin))
	if err != nil {
		return Job{}, fmt.Errorf("marshal pin: %w", err)
	}
	summary := CountJobSummary(targets)
	sumJSON, err := json.Marshal(summary)
	if err != nil {
		return Job{}, fmt.Errorf("marshal summary: %w", err)
	}
	rec := store.UpdateJobRecord{
		JobID:       jobID,
		Operator:    operator,
		SourceAgent: e.SourceAgent,
		PinJSON:     string(pinJSON),
		CreatedAt:   now,
		Status:      string(status),
		SummaryJSON: string(sumJSON),
		OperationID: strings.TrimSpace(operationID),
	}
	if status == JobRunning {
		rec.StartedAt = now
	} else {
		rec.FinishedAt = now
	}
	storeTargets := make([]store.UpdateJobTargetRecord, len(targets))
	for i, t := range targets {
		storeTargets[i] = toStoreJobTarget(jobID, t)
	}
	if err := e.DB.InsertUpdateJob(ctx, rec, storeTargets); err != nil {
		if job, ok, getErr := e.existingByOperationID(ctx, operationID); getErr != nil {
			return Job{}, getErr
		} else if ok {
			return job, nil
		}
		return Job{}, err
	}
	e.bindTargets(ctx, targets)
	if status == JobRunning {
		e.enqueue(jobID)
	}
	return e.Get(ctx, jobID)
}

func (e *Engine) existingByOperationID(ctx context.Context, operationID string) (Job, bool, error) {
	if e == nil || e.DB == nil || strings.TrimSpace(operationID) == "" {
		return Job{}, false, nil
	}
	rec, targets, err := e.DB.GetUpdateJobByOperationID(ctx, operationID)
	if err != nil {
		if errcode.Is(err, errcode.NOT_FOUND) {
			return Job{}, false, nil
		}
		return Job{}, false, err
	}
	job, err := mapJob(rec, targets)
	if err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (e *Engine) bindTargets(ctx context.Context, targets []Target) {
	if e == nil || e.BindTargets == nil || len(targets) == 0 {
		return
	}
	e.BindTargets(ctx, targets)
}

// Start launches a single background worker. Safe to call multiple times.
func (e *Engine) Start(ctx context.Context) {
	if e == nil {
		return
	}
	e.startOnce.Do(func() {
		jobs := make(chan string, jobQueueSize)
		e.mu.Lock()
		e.jobs = jobs
		e.mu.Unlock()
		go e.workerLoop(ctx, jobs)
	})
}

// Resume re-enqueues RUNNING jobs after process restart and repairs terminal
// jobs left with an in-flight target by older workers.
func (e *Engine) Resume(ctx context.Context) error {
	if e == nil || e.DB == nil {
		return nil
	}
	ids, err := e.DB.ListRunningUpdateJobIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		e.enqueue(id)
	}
	return nil
}

// Get returns a job with targets.
func (e *Engine) Get(ctx context.Context, id string) (Job, error) {
	rec, targets, err := e.DB.GetUpdateJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return mapJob(rec, targets)
}

// List returns recent jobs without Targets. limit<=0 → 50; limit>200 → 200.
func (e *Engine) List(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	recs, err := e.DB.ListUpdateJobs(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(recs))
	for _, rec := range recs {
		j, err := mapJob(rec, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// CancelRemaining lets the in-flight target finish and marks other PENDING cancelled.
func (e *Engine) CancelRemaining(ctx context.Context, id, operator string) (Job, error) {
	_ = operator
	j, err := e.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if j.Status != JobRunning {
		return Job{}, errcode.E(errcode.INVALID, "job is not running")
	}
	if err := e.DB.SetUpdateJobCancelRemaining(ctx, id, true); err != nil {
		return Job{}, err
	}
	return e.Get(ctx, id)
}

// Retry re-runs FAILED/TIMEOUT/CONFLICT and not-yet-run targets with the same pin.
// Original skip set is kept. SUCCESS targets are left unchanged.
func (e *Engine) Retry(ctx context.Context, id, operator string) (Job, error) {
	_ = operator
	j, err := e.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if j.Status == JobRunning {
		return Job{}, errcode.E(errcode.CONFLICT, "update job already running")
	}
	running, err := e.DB.HasRunningUpdateJob(ctx)
	if err != nil {
		return Job{}, err
	}
	if running {
		return Job{}, errcode.E(errcode.CONFLICT, "update job already running")
	}
	n := 0
	reminted := make([]Target, 0, len(j.Targets))
	for _, t := range j.Targets {
		if !retryableTarget(t.Status) {
			continue
		}
		newOp := e.newID()
		rec := store.UpdateJobTargetRecord{
			JobID:       id,
			OperationID: newOp,
			NodeID:      t.NodeID,
			Hostname:    t.Hostname,
			Status:      string(TargetPending),
			OrderIndex:  t.OrderIndex,
		}
		if err := e.DB.ReplaceUpdateJobTargetOp(ctx, id, t.OperationID, rec); err != nil {
			return Job{}, err
		}
		t.OperationID = newOp
		t.Status = TargetPending
		t.Error = ""
		t.StartedAt = time.Time{}
		t.FinishedAt = time.Time{}
		reminted = append(reminted, t)
		n++
	}
	if n == 0 {
		return Job{}, errcode.E(errcode.INVALID, "nothing to retry")
	}
	e.bindTargets(ctx, reminted)
	if err := e.DB.SetUpdateJobCancelRemaining(ctx, id, false); err != nil {
		return Job{}, err
	}
	now := time.Now().UTC()
	j, err = e.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	sumJSON, err := json.Marshal(CountJobSummary(j.Targets))
	if err != nil {
		return Job{}, fmt.Errorf("marshal summary: %w", err)
	}
	if err := e.DB.UpdateUpdateJobStatus(ctx, id, string(JobRunning), string(sumJSON), startedAt(j, now), time.Time{}); err != nil {
		return Job{}, err
	}
	e.enqueue(id)
	return e.Get(ctx, id)
}

func retryableTarget(s TargetStatus) bool {
	switch s {
	case TargetFailed, TargetTimeout, TargetConflict, TargetPending, TargetCancelled:
		return true
	default:
		return false
	}
}

func startedAt(j Job, now time.Time) time.Time {
	if !j.StartedAt.IsZero() {
		return j.StartedAt
	}
	return now
}

func (e *Engine) enqueue(jobID string) {
	e.mu.Lock()
	jobs := e.jobs
	e.mu.Unlock()
	if jobs == nil {
		return
	}
	select {
	case jobs <- jobID:
	default:
		go func() { jobs <- jobID }()
	}
}

func (e *Engine) workerLoop(ctx context.Context, jobs <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-jobs:
			if !ok {
				return
			}
			e.runJob(ctx, id)
		}
	}
}

func (e *Engine) runJob(ctx context.Context, jobID string) {
	j, err := e.Get(ctx, jobID)
	if err != nil {
		return
	}
	if j.Status != JobRunning && !hasRunningTarget(j.Targets) {
		return
	}
	for _, t := range j.Targets {
		cur, err := e.Get(ctx, jobID)
		if err != nil {
			return
		}
		var live Target
		found := false
		for _, tg := range cur.Targets {
			if tg.OperationID == t.OperationID {
				live = tg
				found = true
				break
			}
		}
		if !found {
			continue
		}
		if live.Status == TargetRunning {
			if err := e.reconcileRunningTarget(ctx, jobID, cur.Pin, live); err != nil {
				return
			}
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if live.Status != TargetPending {
			continue
		}
		if cur.CancelRemaining {
			_ = e.cancelPending(ctx, jobID, cur.Targets)
			break
		}
		if haltRemaining(cur.Targets) {
			break
		}
		e.runTarget(ctx, jobID, cur.Pin, live)
		if ctx.Err() != nil {
			return
		}
	}
	e.finalize(ctx, jobID)
}

func hasRunningTarget(targets []Target) bool {
	for _, t := range targets {
		if t.Status == TargetRunning {
			return true
		}
	}
	return false
}

func (e *Engine) reconcileRunningTarget(ctx context.Context, jobID string, pin Pin, t Target) error {
	waitErr := e.waitReady(ctx, t.NodeID, pin)
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	if waitErr != nil {
		t.Status = TargetTimeout
		t.Error = waitErr.Error()
	} else {
		t.Status = TargetSuccess
		t.Error = ""
	}
	t.FinishedAt = time.Now().UTC()
	return e.DB.UpdateUpdateJobTarget(ctx, jobID, t.OperationID, toStoreJobTarget(jobID, t))
}

func haltRemaining(targets []Target) bool {
	for _, t := range targets {
		switch t.Status {
		case TargetFailed, TargetTimeout, TargetConflict:
			return true
		}
	}
	return false
}

func (e *Engine) cancelPending(ctx context.Context, jobID string, targets []Target) error {
	now := time.Now().UTC()
	for _, t := range targets {
		if t.Status != TargetPending {
			continue
		}
		t.Status = TargetCancelled
		t.FinishedAt = now
		t.Error = ""
		if err := e.DB.UpdateUpdateJobTarget(ctx, jobID, t.OperationID, toStoreJobTarget(jobID, t)); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runTarget(ctx context.Context, jobID string, pin Pin, t Target) {
	now := time.Now().UTC()
	t.Status = TargetRunning
	t.StartedAt = now
	t.Error = ""
	if err := e.DB.UpdateUpdateJobTarget(ctx, jobID, t.OperationID, toStoreJobTarget(jobID, t)); err != nil {
		return
	}

	var applyErr error
	if e.Apply == nil {
		applyErr = errcode.E(errcode.INVALID, "applier")
	} else {
		applyErr = e.Apply.Apply(ctx, t.NodeID, pin, t.OperationID)
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return
	}
	if applyErr != nil && !applyConfirmationLost(applyErr) {
		t.Status = mapApplyStatus(applyErr)
		t.Error = applyErr.Error()
		t.FinishedAt = time.Now().UTC()
		_ = e.DB.UpdateUpdateJobTarget(ctx, jobID, t.OperationID, toStoreJobTarget(jobID, t))
		return
	}

	waitErr := e.waitReady(ctx, t.NodeID, pin)
	if ctx.Err() != nil && !errcode.Is(waitErr, errcode.TIMEOUT) && !errors.Is(waitErr, context.DeadlineExceeded) {
		return
	}
	if waitErr != nil {
		if applyErr != nil {
			t.Status = mapApplyStatus(applyErr)
			t.Error = applyErr.Error()
		} else {
			t.Status = TargetTimeout
			t.Error = waitErr.Error()
		}
		t.FinishedAt = time.Now().UTC()
		_ = e.DB.UpdateUpdateJobTarget(ctx, jobID, t.OperationID, toStoreJobTarget(jobID, t))
		return
	}
	t.Status = TargetSuccess
	t.Error = ""
	t.FinishedAt = time.Now().UTC()
	_ = e.DB.UpdateUpdateJobTarget(ctx, jobID, t.OperationID, toStoreJobTarget(jobID, t))
}

func applyConfirmationLost(err error) bool {
	return errcode.Is(err, errcode.TIMEOUT) || errcode.Is(err, errcode.UNAVAILABLE) ||
		errors.Is(err, context.DeadlineExceeded)
}

func mapApplyStatus(err error) TargetStatus {
	if err == nil {
		return TargetSuccess
	}
	if errcode.Is(err, errcode.TIMEOUT) || errors.Is(err, context.DeadlineExceeded) {
		return TargetTimeout
	}
	if errcode.Is(err, errcode.CONFLICT) {
		return TargetConflict
	}
	return TargetFailed
}

func (e *Engine) waitReady(ctx context.Context, nodeID string, pin Pin) error {
	timeout := e.WaitTimeout
	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	poll := e.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	deadline := time.Now().Add(timeout)
	for {
		if e.memberReady(nodeID, pin) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errcode.E(errcode.TIMEOUT, "update wait timed out")
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errcode.E(errcode.TIMEOUT, "update wait timed out")
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *Engine) memberReady(nodeID string, pin Pin) bool {
	if e.Members == nil {
		return false
	}
	now := e.Members.Now()
	if now.IsZero() {
		now = time.Now()
	}
	for _, m := range e.Members.Members() {
		if m.NodeID != nodeID {
			continue
		}
		if m.State != cluster.StateAlive {
			return false
		}
		if freshness.Classify(now, m.LastUpdatedUnixMs, string(m.State)) != freshness.LIVE {
			return false
		}
		return VersionsEqual(m.AgentVersion, pin.Tag)
	}
	return false
}

func (e *Engine) finalize(ctx context.Context, jobID string) {
	j, err := e.Get(ctx, jobID)
	if err != nil {
		return
	}
	status := RollupJob(j.Targets, true)
	if status == JobRunning {
		return
	}
	summary := CountJobSummary(j.Targets)
	sumJSON, err := json.Marshal(summary)
	if err != nil {
		return
	}
	started := j.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	_ = e.DB.UpdateUpdateJobStatus(ctx, jobID, string(status), string(sumJSON), started, time.Now().UTC())
}

func (e *Engine) newID() string {
	if e != nil && e.NewID != nil {
		return e.NewID()
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func toStoreJobTarget(jobID string, t Target) store.UpdateJobTargetRecord {
	return store.UpdateJobTargetRecord{
		JobID:       jobID,
		OperationID: t.OperationID,
		NodeID:      t.NodeID,
		Hostname:    t.Hostname,
		Status:      string(t.Status),
		SkipReason:  t.SkipReason,
		Error:       t.Error,
		OrderIndex:  t.OrderIndex,
		StartedAt:   t.StartedAt,
		FinishedAt:  t.FinishedAt,
	}
}

func mapJob(rec store.UpdateJobRecord, targets []store.UpdateJobTargetRecord) (Job, error) {
	var pin Pin
	if rec.PinJSON != "" {
		if err := json.Unmarshal([]byte(rec.PinJSON), &pin); err != nil {
			return Job{}, fmt.Errorf("unmarshal pin: %w", err)
		}
	}
	var summary JobSummary
	if rec.SummaryJSON != "" {
		if err := json.Unmarshal([]byte(rec.SummaryJSON), &summary); err != nil {
			return Job{}, fmt.Errorf("unmarshal summary: %w", err)
		}
	}
	j := Job{
		JobID:           rec.JobID,
		OperationID:     rec.OperationID,
		Operator:        rec.Operator,
		SourceAgent:     rec.SourceAgent,
		Pin:             pin,
		CreatedAt:       rec.CreatedAt,
		StartedAt:       rec.StartedAt,
		FinishedAt:      rec.FinishedAt,
		Status:          JobStatus(rec.Status),
		Summary:         summary,
		CancelRemaining: rec.CancelRemaining,
	}
	if targets != nil {
		j.Targets = make([]Target, len(targets))
		for i, t := range targets {
			j.Targets[i] = Target{
				OperationID: t.OperationID,
				NodeID:      t.NodeID,
				Hostname:    t.Hostname,
				Status:      TargetStatus(t.Status),
				SkipReason:  t.SkipReason,
				Error:       t.Error,
				OrderIndex:  t.OrderIndex,
				StartedAt:   t.StartedAt,
				FinishedAt:  t.FinishedAt,
			}
		}
	}
	return j, nil
}
