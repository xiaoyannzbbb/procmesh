package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// AuditEvent is one append-only local audit record.
type AuditEvent struct {
	AuditID     string
	Timestamp   time.Time
	UserID      string
	Username    string
	SourceIP    string
	SourceAgent string
	TargetAgent string
	Resource    string
	Action      string
	OperationID string
	Result      string
	Metadata    []byte
}

const auditCols = `audit_id, timestamp, user_id, username, source_ip, source_agent,
	target_agent, resource, action, operation_id, result, metadata`

// AppendAudit inserts ev. An empty AuditID is generated. Existing IDs are never updated.
func (s *Store) AppendAudit(ctx context.Context, ev AuditEvent) error {
	if ev.AuditID == "" {
		id, err := newUUID()
		if err != nil {
			return err
		}
		ev.AuditID = id
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events(
			audit_id, timestamp, user_id, username, source_ip, source_agent,
			target_agent, resource, action, operation_id, result, metadata
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ev.AuditID, ev.Timestamp.UTC().Format(time.RFC3339Nano),
		ev.UserID, ev.Username, ev.SourceIP, ev.SourceAgent, ev.TargetAgent,
		ev.Resource, ev.Action, ev.OperationID, ev.Result, ev.Metadata)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.E(errcode.CONFLICT, "audit_id")
		}
		return fmt.Errorf("append audit: %w", err)
	}
	return nil
}

// WriteAudit wraps AppendAudit for process.StateStore.
func (s *Store) WriteAudit(ctx context.Context, resource, action, opID, operator, result string) error {
	return s.AppendAudit(ctx, AuditEvent{
		Resource:    resource,
		Action:      action,
		OperationID: opID,
		Username:    operator,
		Result:      result,
	})
}

// ListAudit returns events for resource, newest first.
func (s *Store) ListAudit(ctx context.Context, resource string, limit int) ([]AuditEvent, error) {
	q := `SELECT ` + auditCols + ` FROM audit_events WHERE resource = ? ORDER BY timestamp DESC, rowid DESC`
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		rows, err = s.db.QueryContext(ctx, q+` LIMIT ?`, resource, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, q, resource)
	}
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()

	var out []AuditEvent
	for rows.Next() {
		ev, err := scanAuditRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	if out == nil {
		out = []AuditEvent{}
	}
	return out, nil
}

func scanAuditRow(rows *sql.Rows) (AuditEvent, error) {
	var ev AuditEvent
	var ts string
	err := rows.Scan(
		&ev.AuditID, &ts, &ev.UserID, &ev.Username, &ev.SourceIP, &ev.SourceAgent,
		&ev.TargetAgent, &ev.Resource, &ev.Action, &ev.OperationID, &ev.Result, &ev.Metadata,
	)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("scan audit: %w", err)
	}
	ev.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("parse audit timestamp: %w", err)
	}
	return ev, nil
}
