package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
)

const instanceCols = `instance_id, process_id, ordinal, pid, shim_pid, desired, observed, health,
	started_at, exit_at, exit_code, restart_count, active_revision, boot_id, last_error`

// PutInstance inserts or replaces a runtime instance row.
func (s *Store) PutInstance(ctx context.Context, inst process.Instance) error {
	if inst.InstanceID == "" {
		return errcode.E(errcode.INVALID, "instance_id required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO process_instances(
			instance_id, process_id, ordinal, pid, shim_pid,
			desired, observed, health, started_at, exit_at, exit_code,
			restart_count, active_revision, boot_id, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET
			process_id = excluded.process_id,
			ordinal = excluded.ordinal,
			pid = excluded.pid,
			shim_pid = excluded.shim_pid,
			desired = excluded.desired,
			observed = excluded.observed,
			health = excluded.health,
			started_at = excluded.started_at,
			exit_at = excluded.exit_at,
			exit_code = excluded.exit_code,
			restart_count = excluded.restart_count,
			active_revision = excluded.active_revision,
			boot_id = excluded.boot_id,
			last_error = excluded.last_error
	`, inst.InstanceID, inst.ProcessID, inst.Ordinal, inst.PID, inst.ShimPID,
		string(inst.Desired), string(inst.Observed), string(inst.Health),
		formatTimePtr(inst.StartedAt), formatTimePtr(inst.ExitAt), nullInt(inst.ExitCode),
		inst.RestartCount, inst.ActiveRevision, inst.BootID, inst.LastError)
	if err != nil {
		return fmt.Errorf("put instance: %w", err)
	}
	return nil
}

// GetInstance returns the runtime row for instanceID.
func (s *Store) GetInstance(ctx context.Context, instanceID string) (process.Instance, error) {
	return scanInstance(s.db.QueryRowContext(ctx, `
		SELECT `+instanceCols+` FROM process_instances WHERE instance_id = ?
	`, instanceID))
}

// DeleteInstance removes a runtime instance row by instance_id.
func (s *Store) DeleteInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return errcode.E(errcode.INVALID, "instance_id required")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM process_instances WHERE instance_id=?`, instanceID)
	if err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete instance rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "instance")
	}
	return nil
}

// ListInstances returns instances for processID ordered by ordinal.
func (s *Store) ListInstances(ctx context.Context, processID string) ([]process.Instance, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+instanceCols+` FROM process_instances WHERE process_id = ? ORDER BY ordinal ASC
	`, processID)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()

	var out []process.Instance
	for rows.Next() {
		inst, err := scanInstanceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	if out == nil {
		out = []process.Instance{}
	}
	return out, nil
}

func scanInstance(row *sql.Row) (process.Instance, error) {
	inst, err := scanInstanceDest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return process.Instance{}, errcode.E(errcode.NOT_FOUND, "instance")
	}
	if err != nil {
		return process.Instance{}, fmt.Errorf("get instance: %w", err)
	}
	return inst, nil
}

func scanInstanceRow(rows *sql.Rows) (process.Instance, error) {
	inst, err := scanInstanceDest(rows)
	if err != nil {
		return process.Instance{}, fmt.Errorf("scan instance: %w", err)
	}
	return inst, nil
}

type instanceScanner interface {
	Scan(dest ...any) error
}

func scanInstanceDest(row instanceScanner) (process.Instance, error) {
	var inst process.Instance
	var desired, observed, health string
	var startedAt, exitAt sql.NullString
	var exitCode sql.NullInt64
	err := row.Scan(
		&inst.InstanceID, &inst.ProcessID, &inst.Ordinal, &inst.PID, &inst.ShimPID,
		&desired, &observed, &health, &startedAt, &exitAt, &exitCode,
		&inst.RestartCount, &inst.ActiveRevision, &inst.BootID, &inst.LastError,
	)
	if err != nil {
		return process.Instance{}, err
	}
	inst.Desired = process.DesiredState(desired)
	inst.Observed = process.ObservedState(observed)
	inst.Health = process.HealthState(health)
	if t, err := parseNullTime(startedAt); err != nil {
		return process.Instance{}, err
	} else if t != nil {
		inst.StartedAt = t
	}
	if t, err := parseNullTime(exitAt); err != nil {
		return process.Instance{}, err
	} else if t != nil {
		inst.ExitAt = t
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		inst.ExitCode = &v
	}
	return inst, nil
}

func formatTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseNullTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil, fmt.Errorf("parse time: %w", err)
	}
	return &t, nil
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
