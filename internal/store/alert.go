package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// AlertRecord is one local alert instance on the sender agent.
type AlertRecord struct {
	AlertID, Fingerprint, Type, Severity string
	NodeID, ProcessID, PayloadJSON, State string
	FirstAt, LastAt, NotifiedAt, ResolvedAt time.Time
	LastError                               string
}

const alertCols = `alert_id, fingerprint, type, severity, node_id, process_id, payload_json,
	state, first_at, last_at, notified_at, resolved_at, last_error`

// UpsertAlert inserts or updates an alert by fingerprint.
// On conflict, alert_id and first_at are preserved; other fields are updated.
func (s *Store) UpsertAlert(ctx context.Context, rec AlertRecord) error {
	if rec.AlertID == "" || rec.Fingerprint == "" {
		return errcode.E(errcode.INVALID, "alert_id and fingerprint required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alerts(
			alert_id, fingerprint, type, severity, node_id, process_id, payload_json,
			state, first_at, last_at, notified_at, resolved_at, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			type = excluded.type,
			severity = excluded.severity,
			node_id = excluded.node_id,
			process_id = excluded.process_id,
			payload_json = excluded.payload_json,
			state = excluded.state,
			last_at = excluded.last_at,
			notified_at = excluded.notified_at,
			resolved_at = excluded.resolved_at,
			last_error = excluded.last_error
	`, rec.AlertID, rec.Fingerprint, rec.Type, rec.Severity, rec.NodeID, rec.ProcessID, rec.PayloadJSON,
		rec.State, formatTime(rec.FirstAt), formatTime(rec.LastAt),
		formatTime(rec.NotifiedAt), formatTime(rec.ResolvedAt), rec.LastError)
	if err != nil {
		return fmt.Errorf("upsert alert: %w", err)
	}
	return nil
}

// GetAlert returns the alert by alert_id. Missing returns NOT_FOUND.
func (s *Store) GetAlert(ctx context.Context, alertID string) (AlertRecord, error) {
	return scanAlert(s.db.QueryRowContext(ctx, `
		SELECT `+alertCols+` FROM alerts WHERE alert_id = ?
	`, alertID))
}

// GetAlertByFingerprint returns the alert by fingerprint. Missing returns NOT_FOUND.
func (s *Store) GetAlertByFingerprint(ctx context.Context, fp string) (AlertRecord, error) {
	return scanAlert(s.db.QueryRowContext(ctx, `
		SELECT `+alertCols+` FROM alerts WHERE fingerprint = ?
	`, fp))
}

// ListAlerts returns alerts ordered by last_at DESC.
// limit <= 0 defaults to 50; limit is capped at 200.
func (s *Store) ListAlerts(ctx context.Context, limit int) ([]AlertRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+alertCols+` FROM alerts ORDER BY last_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()

	var out []AlertRecord
	for rows.Next() {
		rec, err := scanAlertRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	if out == nil {
		out = []AlertRecord{}
	}
	return out, nil
}

func scanAlert(row *sql.Row) (AlertRecord, error) {
	var rec AlertRecord
	var firstAt, lastAt string
	var notifiedAt, resolvedAt sql.NullString
	err := row.Scan(
		&rec.AlertID, &rec.Fingerprint, &rec.Type, &rec.Severity, &rec.NodeID, &rec.ProcessID, &rec.PayloadJSON,
		&rec.State, &firstAt, &lastAt, &notifiedAt, &resolvedAt, &rec.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AlertRecord{}, errcode.E(errcode.NOT_FOUND, "alert")
	}
	if err != nil {
		return AlertRecord{}, fmt.Errorf("get alert: %w", err)
	}
	if err := fillAlertTimes(&rec, firstAt, lastAt, notifiedAt, resolvedAt); err != nil {
		return AlertRecord{}, err
	}
	return rec, nil
}

func scanAlertRow(rows *sql.Rows) (AlertRecord, error) {
	var rec AlertRecord
	var firstAt, lastAt string
	var notifiedAt, resolvedAt sql.NullString
	err := rows.Scan(
		&rec.AlertID, &rec.Fingerprint, &rec.Type, &rec.Severity, &rec.NodeID, &rec.ProcessID, &rec.PayloadJSON,
		&rec.State, &firstAt, &lastAt, &notifiedAt, &resolvedAt, &rec.LastError,
	)
	if err != nil {
		return AlertRecord{}, fmt.Errorf("scan alert: %w", err)
	}
	if err := fillAlertTimes(&rec, firstAt, lastAt, notifiedAt, resolvedAt); err != nil {
		return AlertRecord{}, err
	}
	return rec, nil
}

func fillAlertTimes(rec *AlertRecord, firstAt, lastAt string, notifiedAt, resolvedAt sql.NullString) error {
	var err error
	rec.FirstAt, err = time.Parse(time.RFC3339Nano, firstAt)
	if err != nil {
		return fmt.Errorf("parse alert first_at: %w", err)
	}
	rec.LastAt, err = time.Parse(time.RFC3339Nano, lastAt)
	if err != nil {
		return fmt.Errorf("parse alert last_at: %w", err)
	}
	if t, err := parseNullTime(notifiedAt); err != nil {
		return err
	} else if t != nil {
		rec.NotifiedAt = *t
	}
	if t, err := parseNullTime(resolvedAt); err != nil {
		return err
	} else if t != nil {
		rec.ResolvedAt = *t
	}
	return nil
}
