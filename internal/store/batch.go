package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// BatchRecord is one batch operation stored on the entry agent.
type BatchRecord struct {
	BatchID      string
	Operator     string
	SourceAgent  string
	Type         string
	SelectorJSON string
	CreatedAt    time.Time
	Status       string
	SummaryJSON  string
}

// BatchTargetRecord is one per-process target within a batch.
type BatchTargetRecord struct {
	BatchID          string
	OperationID      string
	NodeID           string
	ProcessID        string
	ProcessName      string
	Status           string
	Error            string
	ExpectedRevision int64
	PayloadJSON      string
	StartedAt        time.Time
	FinishedAt       time.Time
}

const batchCols = `batch_id, operator, source_agent, type, selector_json, created_at, status, summary_json`

const batchTargetCols = `batch_id, operation_id, node_id, process_id, process_name, status, error,
	expected_revision, payload_json, started_at, finished_at`

// InsertBatch inserts rec and targets in a single transaction.
func (s *Store) InsertBatch(ctx context.Context, rec BatchRecord, targets []BatchTargetRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin insert batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO batches(
			batch_id, operator, source_agent, type, selector_json, created_at, status, summary_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.BatchID, rec.Operator, rec.SourceAgent, rec.Type, rec.SelectorJSON,
		formatTime(rec.CreatedAt), rec.Status, rec.SummaryJSON)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.E(errcode.CONFLICT, "batch_id")
		}
		return fmt.Errorf("insert batch: %w", err)
	}

	for _, t := range targets {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO batch_targets(
				batch_id, operation_id, node_id, process_id, process_name, status, error,
				expected_revision, payload_json, started_at, finished_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, t.BatchID, t.OperationID, t.NodeID, t.ProcessID, t.ProcessName, t.Status, t.Error,
			t.ExpectedRevision, t.PayloadJSON, formatTime(t.StartedAt), formatTime(t.FinishedAt))
		if err != nil {
			if isUniqueViolation(err) {
				return errcode.E(errcode.CONFLICT, "batch_target")
			}
			return fmt.Errorf("insert batch target: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit insert batch: %w", err)
	}
	return nil
}

// GetBatch returns the batch and its targets. Missing batch returns NOT_FOUND.
func (s *Store) GetBatch(ctx context.Context, id string) (BatchRecord, []BatchTargetRecord, error) {
	rec, err := scanBatch(s.db.QueryRowContext(ctx, `
		SELECT `+batchCols+` FROM batches WHERE batch_id = ?
	`, id))
	if err != nil {
		return BatchRecord{}, nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+batchTargetCols+` FROM batch_targets WHERE batch_id = ? ORDER BY operation_id ASC
	`, id)
	if err != nil {
		return BatchRecord{}, nil, fmt.Errorf("list batch targets: %w", err)
	}
	defer rows.Close()

	targets, err := scanBatchTargetRows(rows)
	if err != nil {
		return BatchRecord{}, nil, err
	}
	return rec, targets, nil
}

// ListBatches returns batches newest first.
func (s *Store) ListBatches(ctx context.Context, limit int) ([]BatchRecord, error) {
	q := `SELECT ` + batchCols + ` FROM batches ORDER BY created_at DESC, batch_id DESC`
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		rows, err = s.db.QueryContext(ctx, q+` LIMIT ?`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, fmt.Errorf("list batches: %w", err)
	}
	defer rows.Close()

	var out []BatchRecord
	for rows.Next() {
		rec, err := scanBatchRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list batches: %w", err)
	}
	if out == nil {
		out = []BatchRecord{}
	}
	return out, nil
}

// UpdateBatchStatus sets status and summary_json for a batch.
func (s *Store) UpdateBatchStatus(ctx context.Context, id, status, summaryJSON string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE batches SET status = ?, summary_json = ? WHERE batch_id = ?
	`, status, summaryJSON, id)
	if err != nil {
		return fmt.Errorf("update batch status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update batch status rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "batch")
	}
	return nil
}

// UpdateTarget replaces fields on one batch target row.
func (s *Store) UpdateTarget(ctx context.Context, batchID, opID string, rec BatchTargetRecord) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE batch_targets SET
			node_id = ?, process_id = ?, process_name = ?, status = ?, error = ?,
			expected_revision = ?, payload_json = ?, started_at = ?, finished_at = ?
		WHERE batch_id = ? AND operation_id = ?
	`, rec.NodeID, rec.ProcessID, rec.ProcessName, rec.Status, rec.Error,
		rec.ExpectedRevision, rec.PayloadJSON, formatTime(rec.StartedAt), formatTime(rec.FinishedAt),
		batchID, opID)
	if err != nil {
		return fmt.Errorf("update batch target: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update batch target rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "batch_target")
	}
	return nil
}

// ReplaceTargetOp deletes the target row keyed by (batchID, oldOp) and inserts rec
// in one transaction. Used when RetryFailed assigns a new operation_id.
func (s *Store) ReplaceTargetOp(ctx context.Context, batchID, oldOp string, rec BatchTargetRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace batch target: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		DELETE FROM batch_targets WHERE batch_id = ? AND operation_id = ?
	`, batchID, oldOp)
	if err != nil {
		return fmt.Errorf("delete batch target: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete batch target rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "batch_target")
	}

	if rec.BatchID == "" {
		rec.BatchID = batchID
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO batch_targets(
			batch_id, operation_id, node_id, process_id, process_name, status, error,
			expected_revision, payload_json, started_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.BatchID, rec.OperationID, rec.NodeID, rec.ProcessID, rec.ProcessName, rec.Status, rec.Error,
		rec.ExpectedRevision, rec.PayloadJSON, formatTime(rec.StartedAt), formatTime(rec.FinishedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.E(errcode.CONFLICT, "batch_target")
		}
		return fmt.Errorf("insert batch target: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace batch target: %w", err)
	}
	return nil
}

// ListIncompleteTargets returns targets still PENDING or RUNNING.
func (s *Store) ListIncompleteTargets(ctx context.Context) ([]BatchTargetRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+batchTargetCols+` FROM batch_targets
		WHERE status IN ('PENDING', 'RUNNING')
		ORDER BY batch_id ASC, operation_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list incomplete batch targets: %w", err)
	}
	defer rows.Close()

	return scanBatchTargetRows(rows)
}

func scanBatch(row *sql.Row) (BatchRecord, error) {
	var rec BatchRecord
	var createdAt string
	err := row.Scan(
		&rec.BatchID, &rec.Operator, &rec.SourceAgent, &rec.Type, &rec.SelectorJSON,
		&createdAt, &rec.Status, &rec.SummaryJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return BatchRecord{}, errcode.E(errcode.NOT_FOUND, "batch")
	}
	if err != nil {
		return BatchRecord{}, fmt.Errorf("get batch: %w", err)
	}
	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return BatchRecord{}, fmt.Errorf("parse batch created_at: %w", err)
	}
	return rec, nil
}

func scanBatchRow(rows *sql.Rows) (BatchRecord, error) {
	var rec BatchRecord
	var createdAt string
	err := rows.Scan(
		&rec.BatchID, &rec.Operator, &rec.SourceAgent, &rec.Type, &rec.SelectorJSON,
		&createdAt, &rec.Status, &rec.SummaryJSON,
	)
	if err != nil {
		return BatchRecord{}, fmt.Errorf("scan batch: %w", err)
	}
	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return BatchRecord{}, fmt.Errorf("parse batch created_at: %w", err)
	}
	return rec, nil
}

func scanBatchTargetRows(rows *sql.Rows) ([]BatchTargetRecord, error) {
	var out []BatchTargetRecord
	for rows.Next() {
		t, err := scanBatchTargetRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list batch targets: %w", err)
	}
	if out == nil {
		out = []BatchTargetRecord{}
	}
	return out, nil
}

func scanBatchTargetRow(rows *sql.Rows) (BatchTargetRecord, error) {
	var t BatchTargetRecord
	var startedAt, finishedAt sql.NullString
	err := rows.Scan(
		&t.BatchID, &t.OperationID, &t.NodeID, &t.ProcessID, &t.ProcessName, &t.Status, &t.Error,
		&t.ExpectedRevision, &t.PayloadJSON, &startedAt, &finishedAt,
	)
	if err != nil {
		return BatchTargetRecord{}, fmt.Errorf("scan batch target: %w", err)
	}
	if pt, err := parseNullTime(startedAt); err != nil {
		return BatchTargetRecord{}, err
	} else if pt != nil {
		t.StartedAt = *pt
	}
	if pt, err := parseNullTime(finishedAt); err != nil {
		return BatchTargetRecord{}, err
	} else if pt != nil {
		t.FinishedAt = *pt
	}
	return t, nil
}
