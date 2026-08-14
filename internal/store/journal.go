package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// Operation is one idempotent command recorded in the local journal.
type Operation struct {
	OperationID    string
	Operator       string
	SourceAgent    string
	Target         string
	Type           string
	RequestPayload []byte
	CreatedAt      time.Time
	StartedAt      time.Time
	FinishedAt     time.Time
	Status         string
	Result         []byte
	Error          string
}

const (
	OpPending = "PENDING"
	OpRunning = "RUNNING"
	OpSuccess = "SUCCESS"
	OpFailed  = "FAILED"
	OpTimeout = "TIMEOUT"
	OpUnknown = "UNKNOWN"
)

const operationCols = `operation_id, operator, source_agent, target, type, request_payload,
	created_at, started_at, finished_at, status, result, error`

// BeginOperation inserts op unless operation_id already exists.
// A duplicate returns the stored row unchanged.
func (s *Store) BeginOperation(ctx context.Context, op Operation) (Operation, bool, error) {
	if op.OperationID == "" {
		return Operation{}, false, errcode.E(errcode.INVALID, "operation_id required")
	}
	if op.Status == "" {
		op.Status = OpPending
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = time.Now().UTC()
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO operation_journal(
			operation_id, operator, source_agent, target, type, request_payload,
			created_at, started_at, finished_at, status, result, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, op.OperationID, op.Operator, op.SourceAgent, op.Target, op.Type, op.RequestPayload,
		formatTime(op.CreatedAt), formatTime(op.StartedAt), formatTime(op.FinishedAt),
		op.Status, op.Result, op.Error)
	if err != nil {
		return Operation{}, false, fmt.Errorf("begin operation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Operation{}, false, fmt.Errorf("begin operation rows: %w", err)
	}
	existing, err := s.GetOperation(ctx, op.OperationID)
	if err != nil {
		return Operation{}, false, err
	}
	return existing, n == 0, nil
}

// StartOp wraps BeginOperation for process.StateStore.
func (s *Store) StartOp(ctx context.Context, opID, operator, typ, target string, payload []byte) (duplicate bool, status, errMsg string, err error) {
	op, dup, err := s.BeginOperation(ctx, Operation{
		OperationID:    opID,
		Operator:       operator,
		Type:           typ,
		Target:         target,
		RequestPayload: payload,
	})
	if err != nil {
		return false, "", "", err
	}
	return dup, op.Status, op.Error, nil
}

// FinishOperation sets status, result, and error on an existing journal row.
func (s *Store) FinishOperation(ctx context.Context, operationID, status string, result []byte, errMsg string) error {
	if !validOpStatus(status) {
		return errcode.E(errcode.INVALID, "invalid operation status")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE operation_journal
		SET status = ?, result = ?, error = ?, finished_at = ?
		WHERE operation_id = ?
	`, status, result, errMsg, time.Now().UTC().Format(time.RFC3339Nano), operationID)
	if err != nil {
		return fmt.Errorf("finish operation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish operation rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "operation")
	}
	return nil
}

// GetOperation returns the journal row for operationID.
func (s *Store) GetOperation(ctx context.Context, operationID string) (Operation, error) {
	return scanOperation(s.db.QueryRowContext(ctx, `
		SELECT `+operationCols+` FROM operation_journal WHERE operation_id = ?
	`, operationID))
}

func scanOperation(row *sql.Row) (Operation, error) {
	var op Operation
	var createdAt string
	var startedAt, finishedAt sql.NullString
	err := row.Scan(
		&op.OperationID, &op.Operator, &op.SourceAgent, &op.Target, &op.Type, &op.RequestPayload,
		&createdAt, &startedAt, &finishedAt, &op.Status, &op.Result, &op.Error,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, errcode.E(errcode.NOT_FOUND, "operation")
	}
	if err != nil {
		return Operation{}, fmt.Errorf("get operation: %w", err)
	}
	op.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Operation{}, fmt.Errorf("parse operation created_at: %w", err)
	}
	if t, err := parseNullTime(startedAt); err != nil {
		return Operation{}, err
	} else if t != nil {
		op.StartedAt = *t
	}
	if t, err := parseNullTime(finishedAt); err != nil {
		return Operation{}, err
	} else if t != nil {
		op.FinishedAt = *t
	}
	return op, nil
}

func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func validOpStatus(status string) bool {
	switch status {
	case OpPending, OpRunning, OpSuccess, OpFailed, OpTimeout, OpUnknown:
		return true
	default:
		return false
	}
}
