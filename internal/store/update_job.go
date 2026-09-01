package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// UpdateJobRecord is one entry-local cluster rolling update job.
type UpdateJobRecord struct {
	JobID           string
	Operator        string
	SourceAgent     string
	PinJSON         string
	CreatedAt       time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
	Status          string
	SummaryJSON     string
	CancelRemaining bool
	OperationID     string
}

// UpdateJobTargetRecord is one per-node target within an update job.
type UpdateJobTargetRecord struct {
	JobID       string
	OperationID string
	NodeID      string
	Hostname    string
	Status      string
	SkipReason  string
	Error       string
	OrderIndex  int
	StartedAt   time.Time
	FinishedAt  time.Time
}

const updateJobCols = `job_id, operator, source_agent, pin_json, created_at, started_at, finished_at,
	status, summary_json, cancel_remaining, operation_id`

const updateJobTargetCols = `job_id, operation_id, node_id, hostname, status, skip_reason, error,
	order_index, started_at, finished_at`

// InsertUpdateJob inserts rec and targets in a single transaction.
func (s *Store) InsertUpdateJob(ctx context.Context, rec UpdateJobRecord, targets []UpdateJobTargetRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin insert update job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cancel := 0
	if rec.CancelRemaining {
		cancel = 1
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO update_jobs(
			job_id, operator, source_agent, pin_json, created_at, started_at, finished_at,
			status, summary_json, cancel_remaining, operation_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.JobID, rec.Operator, rec.SourceAgent, rec.PinJSON, formatTime(rec.CreatedAt),
		formatTime(rec.StartedAt), formatTime(rec.FinishedAt), rec.Status, rec.SummaryJSON, cancel, rec.OperationID)
	if err != nil {
		if isUniqueViolation(err) {
			if rec.Status == "RUNNING" {
				return errcode.E(errcode.CONFLICT, "update job already running")
			}
			return errcode.E(errcode.CONFLICT, "job_id")
		}
		return fmt.Errorf("insert update job: %w", err)
	}

	for _, t := range targets {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO update_job_targets(
				job_id, operation_id, node_id, hostname, status, skip_reason, error,
				order_index, started_at, finished_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, t.JobID, t.OperationID, t.NodeID, t.Hostname, t.Status, t.SkipReason, t.Error,
			t.OrderIndex, formatTime(t.StartedAt), formatTime(t.FinishedAt))
		if err != nil {
			if isUniqueViolation(err) {
				return errcode.E(errcode.CONFLICT, "update_job_target")
			}
			return fmt.Errorf("insert update job target: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit insert update job: %w", err)
	}
	return nil
}

// GetUpdateJob returns the job and its targets ordered by order_index. Missing job returns NOT_FOUND.
func (s *Store) GetUpdateJob(ctx context.Context, id string) (UpdateJobRecord, []UpdateJobTargetRecord, error) {
	rec, err := scanUpdateJob(s.db.QueryRowContext(ctx, `
		SELECT `+updateJobCols+` FROM update_jobs WHERE job_id = ?
	`, id))
	if err != nil {
		return UpdateJobRecord{}, nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+updateJobTargetCols+` FROM update_job_targets WHERE job_id = ? ORDER BY order_index ASC, operation_id ASC
	`, id)
	if err != nil {
		return UpdateJobRecord{}, nil, fmt.Errorf("list update job targets: %w", err)
	}
	defer rows.Close()

	targets, err := scanUpdateJobTargetRows(rows)
	if err != nil {
		return UpdateJobRecord{}, nil, err
	}
	return rec, targets, nil
}

// GetUpdateJobByOperationID returns the job created with operationID.
// Empty or missing operation_id returns NOT_FOUND.
func (s *Store) GetUpdateJobByOperationID(ctx context.Context, operationID string) (UpdateJobRecord, []UpdateJobTargetRecord, error) {
	if strings.TrimSpace(operationID) == "" {
		return UpdateJobRecord{}, nil, errcode.E(errcode.NOT_FOUND, "update_job")
	}
	rec, err := scanUpdateJob(s.db.QueryRowContext(ctx, `
		SELECT `+updateJobCols+` FROM update_jobs WHERE operation_id = ?
	`, operationID))
	if err != nil {
		return UpdateJobRecord{}, nil, err
	}
	return s.GetUpdateJob(ctx, rec.JobID)
}

// ListUpdateJobs returns jobs newest first, without targets.
func (s *Store) ListUpdateJobs(ctx context.Context, limit int) ([]UpdateJobRecord, error) {
	q := `SELECT ` + updateJobCols + ` FROM update_jobs ORDER BY created_at DESC, job_id DESC`
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
		return nil, fmt.Errorf("list update jobs: %w", err)
	}
	defer rows.Close()

	var out []UpdateJobRecord
	for rows.Next() {
		rec, err := scanUpdateJobRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list update jobs: %w", err)
	}
	if out == nil {
		out = []UpdateJobRecord{}
	}
	return out, nil
}

// HasRunningUpdateJob reports whether a RUNNING job or in-flight target exists
// on this entry.
func (s *Store) HasRunningUpdateJob(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM update_jobs j
		WHERE j.status = 'RUNNING' OR EXISTS (
			SELECT 1 FROM update_job_targets t
			WHERE t.job_id = j.job_id AND t.status = 'RUNNING'
		)
	`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has running update job: %w", err)
	}
	return n > 0, nil
}

// ListRunningUpdateJobIDs returns RUNNING jobs and terminal jobs left with an
// in-flight target by an older worker. The latter are repaired during Resume.
func (s *Store) ListRunningUpdateJobIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.job_id
		FROM update_jobs j
		WHERE j.status = 'RUNNING' OR EXISTS (
			SELECT 1 FROM update_job_targets t
			WHERE t.job_id = j.job_id AND t.status = 'RUNNING'
		)
		ORDER BY j.created_at ASC, j.job_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list running update jobs: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan running update job: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list running update jobs: %w", err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// UpdateUpdateJobStatus sets status, summary_json, and optional timestamps.
func (s *Store) UpdateUpdateJobStatus(ctx context.Context, id, status, summaryJSON string, startedAt, finishedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE update_jobs SET status = ?, summary_json = ?, started_at = ?, finished_at = ? WHERE job_id = ?
	`, status, summaryJSON, formatTime(startedAt), formatTime(finishedAt), id)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.E(errcode.CONFLICT, "update job already running")
		}
		return fmt.Errorf("update update job status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update update job status rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "update_job")
	}
	return nil
}

// SetUpdateJobCancelRemaining records CancelRemaining for a job.
func (s *Store) SetUpdateJobCancelRemaining(ctx context.Context, id string, cancel bool) error {
	v := 0
	if cancel {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE update_jobs SET cancel_remaining = ? WHERE job_id = ?`, v, id)
	if err != nil {
		return fmt.Errorf("set update job cancel: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set update job cancel rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "update_job")
	}
	return nil
}

// UpdateUpdateJobTarget replaces fields on one target row.
func (s *Store) UpdateUpdateJobTarget(ctx context.Context, jobID, opID string, rec UpdateJobTargetRecord) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE update_job_targets SET
			node_id = ?, hostname = ?, status = ?, skip_reason = ?, error = ?,
			order_index = ?, started_at = ?, finished_at = ?
		WHERE job_id = ? AND operation_id = ?
	`, rec.NodeID, rec.Hostname, rec.Status, rec.SkipReason, rec.Error,
		rec.OrderIndex, formatTime(rec.StartedAt), formatTime(rec.FinishedAt),
		jobID, opID)
	if err != nil {
		return fmt.Errorf("update update job target: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update update job target rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "update_job_target")
	}
	return nil
}

// ReplaceUpdateJobTargetOp deletes the target row keyed by (jobID, oldOp) and inserts rec.
func (s *Store) ReplaceUpdateJobTargetOp(ctx context.Context, jobID, oldOp string, rec UpdateJobTargetRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace update job target: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		DELETE FROM update_job_targets WHERE job_id = ? AND operation_id = ?
	`, jobID, oldOp)
	if err != nil {
		return fmt.Errorf("delete update job target: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete update job target rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "update_job_target")
	}

	if rec.JobID == "" {
		rec.JobID = jobID
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO update_job_targets(
			job_id, operation_id, node_id, hostname, status, skip_reason, error,
			order_index, started_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.JobID, rec.OperationID, rec.NodeID, rec.Hostname, rec.Status, rec.SkipReason, rec.Error,
		rec.OrderIndex, formatTime(rec.StartedAt), formatTime(rec.FinishedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.E(errcode.CONFLICT, "update_job_target")
		}
		return fmt.Errorf("insert update job target: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace update job target: %w", err)
	}
	return nil
}

func scanUpdateJob(row *sql.Row) (UpdateJobRecord, error) {
	rec, err := scanUpdateJobValues(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return UpdateJobRecord{}, errcode.E(errcode.NOT_FOUND, "update_job")
	}
	if err != nil {
		return UpdateJobRecord{}, fmt.Errorf("get update job: %w", err)
	}
	return rec, nil
}

func scanUpdateJobRow(rows *sql.Rows) (UpdateJobRecord, error) {
	rec, err := scanUpdateJobValues(rows.Scan)
	if err != nil {
		return UpdateJobRecord{}, fmt.Errorf("scan update job: %w", err)
	}
	return rec, nil
}

func scanUpdateJobValues(scan func(dest ...any) error) (UpdateJobRecord, error) {
	var rec UpdateJobRecord
	var createdAt string
	var startedAt, finishedAt sql.NullString
	var cancel int
	err := scan(
		&rec.JobID, &rec.Operator, &rec.SourceAgent, &rec.PinJSON, &createdAt,
		&startedAt, &finishedAt, &rec.Status, &rec.SummaryJSON, &cancel, &rec.OperationID,
	)
	if err != nil {
		return UpdateJobRecord{}, err
	}
	rec.CancelRemaining = cancel != 0
	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return UpdateJobRecord{}, fmt.Errorf("parse update job created_at: %w", err)
	}
	if pt, err := parseNullTime(startedAt); err != nil {
		return UpdateJobRecord{}, err
	} else if pt != nil {
		rec.StartedAt = *pt
	}
	if pt, err := parseNullTime(finishedAt); err != nil {
		return UpdateJobRecord{}, err
	} else if pt != nil {
		rec.FinishedAt = *pt
	}
	return rec, nil
}

func scanUpdateJobTargetRows(rows *sql.Rows) ([]UpdateJobTargetRecord, error) {
	var out []UpdateJobTargetRecord
	for rows.Next() {
		t, err := scanUpdateJobTargetRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list update job targets: %w", err)
	}
	if out == nil {
		out = []UpdateJobTargetRecord{}
	}
	return out, nil
}

func scanUpdateJobTargetRow(rows *sql.Rows) (UpdateJobTargetRecord, error) {
	var t UpdateJobTargetRecord
	var startedAt, finishedAt sql.NullString
	err := rows.Scan(
		&t.JobID, &t.OperationID, &t.NodeID, &t.Hostname, &t.Status, &t.SkipReason, &t.Error,
		&t.OrderIndex, &startedAt, &finishedAt,
	)
	if err != nil {
		return UpdateJobTargetRecord{}, fmt.Errorf("scan update job target: %w", err)
	}
	if pt, err := parseNullTime(startedAt); err != nil {
		return UpdateJobTargetRecord{}, err
	} else if pt != nil {
		t.StartedAt = *pt
	}
	if pt, err := parseNullTime(finishedAt); err != nil {
		return UpdateJobTargetRecord{}, err
	} else if pt != nil {
		t.FinishedAt = *pt
	}
	return t, nil
}
