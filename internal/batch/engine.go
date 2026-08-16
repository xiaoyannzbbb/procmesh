package batch

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

const (
	defaultConcurrency   = 16
	maxConcurrency       = 64
	defaultTargetTimeout = 30 * time.Second
	jobQueueSize         = 128
)

// Expander resolves a selector into concrete targets.
type Expander interface {
	Expand(ctx context.Context, sel Selector, typ Type) ([]Target, error)
}

// Executor runs one target operation.
type Executor interface {
	Execute(ctx context.Context, t Target, typ Type) error
}

// Engine persists and serves entry-local batches. Start launches one background worker.
type Engine struct {
	DB            *store.Store
	Expand        Expander
	Exec          Executor
	Concurrency   int
	TargetTimeout time.Duration
	SourceAgent   string
	NewID         func() string
	// BindTargets is invoked after operation_ids are assigned and persisted,
	// before enqueue. Callers bind hop identity here.
	BindTargets func(ctx context.Context, targets []Target)

	startOnce sync.Once
	mu        sync.Mutex
	jobs      chan string
}

// Create expands the selector, assigns IDs, and inserts a PENDING batch.
func (e *Engine) Create(ctx context.Context, operator string, typ Type, sel Selector, comment string) (Batch, error) {
	return e.CreateWithExpand(ctx, operator, typ, sel, comment, e.Expand)
}

// CreateWithExpand is Create using a request-scoped expander. The expander is
// not stored on Engine.
func (e *Engine) CreateWithExpand(ctx context.Context, operator string, typ Type, sel Selector, comment string, expand Expander) (Batch, error) {
	_ = comment
	if strings.TrimSpace(operator) == "" {
		return Batch{}, errcode.E(errcode.INVALID, "operator")
	}
	if !validType(typ) {
		return Batch{}, errcode.E(errcode.INVALID, "type")
	}
	if selectorEmpty(sel) {
		return Batch{}, errcode.E(errcode.INVALID, "selector")
	}
	if expand == nil {
		return Batch{}, errcode.E(errcode.INVALID, "expander")
	}

	raw, err := expand.Expand(ctx, sel, typ)
	if err != nil {
		return Batch{}, err
	}
	targets := filterNonZeroTargets(raw)
	if len(targets) == 0 {
		return Batch{}, errcode.E(errcode.INVALID, "targets")
	}

	batchID := e.NewID()
	for i := range targets {
		if targets[i].OperationID == "" {
			targets[i].OperationID = e.NewID()
		}
		if targets[i].Status == "" {
			targets[i].Status = TargetPending
		}
	}

	now := time.Now().UTC()
	summary := CountSummary(targets)
	selJSON, err := json.Marshal(sel)
	if err != nil {
		return Batch{}, fmt.Errorf("marshal selector: %w", err)
	}
	sumJSON, err := json.Marshal(summary)
	if err != nil {
		return Batch{}, fmt.Errorf("marshal summary: %w", err)
	}

	rec := store.BatchRecord{
		BatchID:      batchID,
		Operator:     operator,
		SourceAgent:  e.SourceAgent,
		Type:         string(typ),
		SelectorJSON: string(selJSON),
		CreatedAt:    now,
		Status:       string(StatusPending),
		SummaryJSON:  string(sumJSON),
	}
	storeTargets := make([]store.BatchTargetRecord, len(targets))
	for i, t := range targets {
		storeTargets[i] = store.BatchTargetRecord{
			BatchID:          batchID,
			OperationID:      t.OperationID,
			NodeID:           t.NodeID,
			ProcessID:        t.ProcessID,
			ProcessName:      t.ProcessName,
			Status:           string(t.Status),
			Error:            t.Error,
			ExpectedRevision: t.ExpectedRevision,
			PayloadJSON:      t.PayloadJSON,
			StartedAt:        t.StartedAt,
			FinishedAt:       t.FinishedAt,
		}
	}
	if err := e.DB.InsertBatch(ctx, rec, storeTargets); err != nil {
		return Batch{}, err
	}
	e.bindTargets(ctx, targets)
	e.enqueue(batchID)
	return e.Get(ctx, batchID)
}

func (e *Engine) bindTargets(ctx context.Context, targets []Target) {
	if e == nil || e.BindTargets == nil || len(targets) == 0 {
		return
	}
	e.BindTargets(ctx, targets)
}

// Start launches a single background worker loop. Safe to call multiple times.
func (e *Engine) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		jobs := make(chan string, jobQueueSize)
		e.mu.Lock()
		e.jobs = jobs
		e.mu.Unlock()
		go e.workerLoop(ctx, jobs)
	})
}

// MapExecError maps an Execute error to a terminal TargetStatus.
// TIMEOUT/DeadlineExceeded stay TIMEOUT (never rewritten as FAILED).
func MapExecError(err error) TargetStatus {
	if err == nil {
		return TargetSuccess
	}
	if errcode.Is(err, errcode.TIMEOUT) || errors.Is(err, context.DeadlineExceeded) {
		return TargetTimeout
	}
	if errcode.Is(err, errcode.DENIED) {
		return TargetDenied
	}
	if errcode.Is(err, errcode.CONFLICT) {
		return TargetConflict
	}
	if errcode.Is(err, errcode.INVALID) {
		return TargetInvalid
	}
	if errcode.Is(err, errcode.UNAVAILABLE) {
		return TargetUnavailable
	}
	return TargetFailed
}

func (e *Engine) enqueue(batchID string) {
	e.mu.Lock()
	jobs := e.jobs
	e.mu.Unlock()
	if jobs == nil {
		return
	}
	select {
	case jobs <- batchID:
	default:
		go func() { jobs <- batchID }()
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
			e.runBatch(ctx, id)
		}
	}
}

func (e *Engine) runBatch(ctx context.Context, batchID string) {
	b, err := e.Get(ctx, batchID)
	if err != nil {
		return
	}
	if b.Status == StatusCompleted || b.Status == StatusPartial || b.Status == StatusFailed {
		return
	}

	summaryJSON, err := json.Marshal(b.Summary)
	if err != nil {
		return
	}
	if err := e.DB.UpdateBatchStatus(ctx, batchID, string(StatusRunning), string(summaryJSON)); err != nil {
		return
	}

	conc := e.Concurrency
	if conc <= 0 {
		conc = defaultConcurrency
	}
	if conc > maxConcurrency {
		conc = maxConcurrency
	}
	timeout := e.TargetTimeout
	if timeout <= 0 {
		timeout = defaultTargetTimeout
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, t := range b.Targets {
		if isTerminalTarget(t.Status) {
			continue
		}
		if t.Status != TargetPending && t.Status != TargetRunning {
			continue
		}
		tg := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			e.runTarget(ctx, batchID, b.Type, tg, timeout)
		}()
	}
	wg.Wait()

	finished, err := e.Get(ctx, batchID)
	if err != nil {
		return
	}
	status := Rollup(finished.Targets)
	summary := CountSummary(finished.Targets)
	sumJSON, err := json.Marshal(summary)
	if err != nil {
		return
	}
	_ = e.DB.UpdateBatchStatus(ctx, batchID, string(status), string(sumJSON))
}

func (e *Engine) runTarget(ctx context.Context, batchID string, typ Type, t Target, timeout time.Duration) {
	now := time.Now().UTC()
	t.Status = TargetRunning
	t.StartedAt = now
	t.Error = ""
	if err := e.DB.UpdateTarget(ctx, batchID, t.OperationID, toStoreTarget(batchID, t)); err != nil {
		return
	}

	var execErr error
	deadlineExceeded := false
	if e.Exec == nil {
		execErr = errcode.E(errcode.INVALID, "executor")
	} else {
		tctx, cancel := context.WithTimeout(ctx, timeout)
		execErr = e.Exec.Execute(tctx, t, typ)
		deadlineExceeded = errors.Is(tctx.Err(), context.DeadlineExceeded)
		cancel()
	}

	// Parent cancel (agent shutdown) is not a per-target failure. Leave
	// PENDING/RUNNING so Resume can reuse the original operation_id.
	if ctx.Err() != nil && !deadlineExceeded {
		return
	}

	status := MapExecError(execErr)
	t.Status = status
	t.FinishedAt = time.Now().UTC()
	if execErr != nil {
		t.Error = execErr.Error()
	} else {
		t.Error = ""
	}
	writeCtx := ctx
	if ctx.Err() != nil {
		writeCtx = context.WithoutCancel(ctx)
	}
	_ = e.DB.UpdateTarget(writeCtx, batchID, t.OperationID, toStoreTarget(batchID, t))
}

func isTerminalTarget(s TargetStatus) bool {
	switch s {
	case TargetSuccess, TargetFailed, TargetTimeout, TargetDenied, TargetConflict, TargetUnavailable, TargetInvalid:
		return true
	default:
		return false
	}
}

func toStoreTarget(batchID string, t Target) store.BatchTargetRecord {
	return store.BatchTargetRecord{
		BatchID:          batchID,
		OperationID:      t.OperationID,
		NodeID:           t.NodeID,
		ProcessID:        t.ProcessID,
		ProcessName:      t.ProcessName,
		Status:           string(t.Status),
		Error:            t.Error,
		ExpectedRevision: t.ExpectedRevision,
		PayloadJSON:      t.PayloadJSON,
		StartedAt:        t.StartedAt,
		FinishedAt:       t.FinishedAt,
	}
}

// RetryFailed resets FAILED/DENIED/CONFLICT/UNAVAILABLE/INVALID targets to PENDING
// with new operation_ids, sets the batch RUNNING, and enqueues it.
// SUCCESS and TIMEOUT targets are left unchanged.
func (e *Engine) RetryFailed(ctx context.Context, id, operator string) (Batch, error) {
	_ = operator
	b, err := e.Get(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	n := 0
	reminted := make([]Target, 0, len(b.Targets))
	for _, t := range b.Targets {
		if !isRetryableTarget(t.Status) {
			continue
		}
		newOp := e.NewID()
		rec := store.BatchTargetRecord{
			BatchID:          id,
			OperationID:      newOp,
			NodeID:           t.NodeID,
			ProcessID:        t.ProcessID,
			ProcessName:      t.ProcessName,
			Status:           string(TargetPending),
			Error:            "",
			ExpectedRevision: t.ExpectedRevision,
			PayloadJSON:      t.PayloadJSON,
		}
		if err := e.DB.ReplaceTargetOp(ctx, id, t.OperationID, rec); err != nil {
			return Batch{}, err
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
		return Batch{}, errcode.E(errcode.INVALID, "nothing to retry")
	}
	e.bindTargets(ctx, reminted)
	return e.requeueBatch(ctx, id)
}

// ReplayTimeout resets TIMEOUT targets to PENDING reusing the original operation_id,
// sets the batch RUNNING, and enqueues it. SUCCESS targets are left unchanged.
func (e *Engine) ReplayTimeout(ctx context.Context, id, operator string) (Batch, error) {
	_ = operator
	b, err := e.Get(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	n := 0
	replayed := make([]Target, 0, len(b.Targets))
	for _, t := range b.Targets {
		if t.Status != TargetTimeout {
			continue
		}
		rec := store.BatchTargetRecord{
			BatchID:          id,
			OperationID:      t.OperationID,
			NodeID:           t.NodeID,
			ProcessID:        t.ProcessID,
			ProcessName:      t.ProcessName,
			Status:           string(TargetPending),
			Error:            "",
			ExpectedRevision: t.ExpectedRevision,
			PayloadJSON:      t.PayloadJSON,
		}
		if err := e.DB.UpdateTarget(ctx, id, t.OperationID, rec); err != nil {
			return Batch{}, err
		}
		t.Status = TargetPending
		t.Error = ""
		t.StartedAt = time.Time{}
		t.FinishedAt = time.Time{}
		replayed = append(replayed, t)
		n++
	}
	if n == 0 {
		return Batch{}, errcode.E(errcode.INVALID, "nothing to replay")
	}
	e.bindTargets(ctx, replayed)
	return e.requeueBatch(ctx, id)
}

// Resume re-enqueues batches that still have PENDING/RUNNING targets.
// Does not change operation_ids and does not replay TIMEOUT or SUCCESS.
func (e *Engine) Resume(ctx context.Context) error {
	targets, err := e.DB.ListIncompleteTargets(ctx)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{})
	for _, t := range targets {
		if _, ok := seen[t.BatchID]; ok {
			continue
		}
		seen[t.BatchID] = struct{}{}
		e.enqueue(t.BatchID)
	}
	return nil
}

func (e *Engine) requeueBatch(ctx context.Context, id string) (Batch, error) {
	b, err := e.Get(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	summary := CountSummary(b.Targets)
	sumJSON, err := json.Marshal(summary)
	if err != nil {
		return Batch{}, fmt.Errorf("marshal summary: %w", err)
	}
	if err := e.DB.UpdateBatchStatus(ctx, id, string(StatusRunning), string(sumJSON)); err != nil {
		return Batch{}, err
	}
	e.enqueue(id)
	return e.Get(ctx, id)
}

func isRetryableTarget(s TargetStatus) bool {
	switch s {
	case TargetFailed, TargetDenied, TargetConflict, TargetUnavailable, TargetInvalid:
		return true
	default:
		return false
	}
}

// Get returns a batch with targets.
func (e *Engine) Get(ctx context.Context, id string) (Batch, error) {
	rec, targets, err := e.DB.GetBatch(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	return mapBatch(rec, targets)
}

// List returns recent batches without Targets. limit<=0 → 50; limit>200 → 200.
func (e *Engine) List(ctx context.Context, limit int) ([]Batch, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	recs, err := e.DB.ListBatches(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Batch, 0, len(recs))
	for _, rec := range recs {
		b, err := mapBatch(rec, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// Export returns batch content as json (default) or csv.
func (e *Engine) Export(ctx context.Context, id, format string) (content []byte, contentType, filename string, err error) {
	b, err := e.Get(ctx, id)
	if err != nil {
		return nil, "", "", err
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json":
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, "", "", fmt.Errorf("marshal batch: %w", err)
		}
		return raw, "application/json", id + ".json", nil
	case "csv":
		var buf strings.Builder
		w := csv.NewWriter(&buf)
		if err := w.Write([]string{"operation_id", "node_id", "process_id", "process_name", "status", "error"}); err != nil {
			return nil, "", "", fmt.Errorf("csv header: %w", err)
		}
		for _, t := range b.Targets {
			if err := w.Write([]string{
				t.OperationID, t.NodeID, t.ProcessID, t.ProcessName, string(t.Status), t.Error,
			}); err != nil {
				return nil, "", "", fmt.Errorf("csv row: %w", err)
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return nil, "", "", fmt.Errorf("csv flush: %w", err)
		}
		return []byte(buf.String()), "text/csv", id + ".csv", nil
	default:
		return nil, "", "", errcode.E(errcode.INVALID, "format")
	}
}

func validType(typ Type) bool {
	switch typ {
	case TypeStart, TypeStop, TypeRestart, TypeConfigUpdate:
		return true
	default:
		return false
	}
}

func selectorEmpty(sel Selector) bool {
	return len(sel.ProcessIDs) == 0 &&
		len(sel.ProcessNames) == 0 &&
		sel.AgentGroupID == "" &&
		sel.ProcessGroup == ""
}

func filterNonZeroTargets(in []Target) []Target {
	out := make([]Target, 0, len(in))
	for _, t := range in {
		if t.OperationID == "" && t.NodeID == "" && t.ProcessID == "" && t.ProcessName == "" &&
			t.Status == "" && t.Error == "" && t.ExpectedRevision == 0 && t.PayloadJSON == "" &&
			t.StartedAt.IsZero() && t.FinishedAt.IsZero() {
			continue
		}
		out = append(out, t)
	}
	return out
}

func mapBatch(rec store.BatchRecord, targets []store.BatchTargetRecord) (Batch, error) {
	var sel Selector
	if rec.SelectorJSON != "" {
		if err := json.Unmarshal([]byte(rec.SelectorJSON), &sel); err != nil {
			return Batch{}, fmt.Errorf("unmarshal selector: %w", err)
		}
	}
	var summary Summary
	if rec.SummaryJSON != "" {
		if err := json.Unmarshal([]byte(rec.SummaryJSON), &summary); err != nil {
			return Batch{}, fmt.Errorf("unmarshal summary: %w", err)
		}
	}
	b := Batch{
		BatchID:     rec.BatchID,
		Operator:    rec.Operator,
		SourceAgent: rec.SourceAgent,
		Type:        Type(rec.Type),
		Selector:    sel,
		CreatedAt:   rec.CreatedAt,
		Status:      Status(rec.Status),
		Summary:     summary,
	}
	if targets != nil {
		b.Targets = make([]Target, len(targets))
		for i, t := range targets {
			b.Targets[i] = Target{
				OperationID:      t.OperationID,
				NodeID:           t.NodeID,
				ProcessID:        t.ProcessID,
				ProcessName:      t.ProcessName,
				Status:           TargetStatus(t.Status),
				Error:            t.Error,
				ExpectedRevision: t.ExpectedRevision,
				PayloadJSON:      t.PayloadJSON,
				StartedAt:        t.StartedAt,
				FinishedAt:       t.FinishedAt,
			}
		}
	}
	return b, nil
}
