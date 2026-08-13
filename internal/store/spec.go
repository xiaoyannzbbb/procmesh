package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"

	sqlite3 "modernc.org/sqlite"
)

// Revision is one historical snapshot of a process spec.
type Revision struct {
	Revision  int64
	Operator  string
	Timestamp time.Time
	Diff      string
	Comment   string
	SpecJSON  []byte
}

// PutSpec creates (expectedRevision==0) or updates a spec with CAS on LatestRevision.
func (s *Store) PutSpec(ctx context.Context, spec process.ProcessSpec, expectedRevision int64, operator, comment string) (process.ProcessSpec, error) {
	if err := process.ValidateSpec(spec); err != nil {
		return process.ProcessSpec{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return process.ProcessSpec{}, fmt.Errorf("begin put spec: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	out := spec
	out.UpdatedAt = now

	if expectedRevision == 0 {
		out.LatestRevision = 1
		if out.CreatedAt.IsZero() {
			out.CreatedAt = now
		}
		payload, err := json.Marshal(out)
		if err != nil {
			return process.ProcessSpec{}, fmt.Errorf("marshal spec: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO process_specs(process_id, name, latest_revision, spec_json)
			VALUES (?, ?, 1, ?)
		`, out.ProcessID, out.Name, string(payload))
		if err != nil {
			if isUniqueViolation(err) {
				return process.ProcessSpec{}, errcode.E(errcode.CONFLICT, "process already exists")
			}
			return process.ProcessSpec{}, fmt.Errorf("insert spec: %w", err)
		}
		if err := insertRevision(ctx, tx, out.ProcessID, 1, operator, now, specDiff(process.ProcessSpec{}, out), comment, payload); err != nil {
			return process.ProcessSpec{}, err
		}
	} else {
		current, err := getSpecTx(ctx, tx, spec.ProcessID)
		if err != nil {
			return process.ProcessSpec{}, err
		}
		if current.LatestRevision != expectedRevision {
			return process.ProcessSpec{}, errcode.E(errcode.CONFLICT, "revision mismatch")
		}
		out.LatestRevision = current.LatestRevision + 1
		if out.CreatedAt.IsZero() {
			out.CreatedAt = current.CreatedAt
		}
		payload, err := json.Marshal(out)
		if err != nil {
			return process.ProcessSpec{}, fmt.Errorf("marshal spec: %w", err)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE process_specs
			SET name = ?, latest_revision = ?, spec_json = ?
			WHERE process_id = ? AND latest_revision = ?
		`, out.Name, out.LatestRevision, string(payload), out.ProcessID, expectedRevision)
		if err != nil {
			if isUniqueViolation(err) {
				return process.ProcessSpec{}, errcode.E(errcode.CONFLICT, "name already exists")
			}
			return process.ProcessSpec{}, fmt.Errorf("update spec: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return process.ProcessSpec{}, fmt.Errorf("update spec rows: %w", err)
		}
		if n == 0 {
			return process.ProcessSpec{}, errcode.E(errcode.CONFLICT, "revision mismatch")
		}
		if err := insertRevision(ctx, tx, out.ProcessID, out.LatestRevision, operator, now, specDiff(current, out), comment, payload); err != nil {
			return process.ProcessSpec{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return process.ProcessSpec{}, fmt.Errorf("commit put spec: %w", err)
	}
	return out, nil
}

// GetSpec returns the current spec for processID.
func (s *Store) GetSpec(ctx context.Context, processID string) (process.ProcessSpec, error) {
	return scanSpec(s.db.QueryRowContext(ctx, `
		SELECT spec_json, latest_revision FROM process_specs WHERE process_id = ?
	`, processID))
}

// GetSpecByName returns the current spec with the given unique name.
func (s *Store) GetSpecByName(ctx context.Context, name string) (process.ProcessSpec, error) {
	return scanSpec(s.db.QueryRowContext(ctx, `
		SELECT spec_json, latest_revision FROM process_specs WHERE name = ?
	`, name))
}

// ListSpecs returns all current process specs ordered by name.
func (s *Store) ListSpecs(ctx context.Context) ([]process.ProcessSpec, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT spec_json, latest_revision FROM process_specs ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list specs: %w", err)
	}
	defer rows.Close()

	var out []process.ProcessSpec
	for rows.Next() {
		var specJSON string
		var latest int64
		if err := rows.Scan(&specJSON, &latest); err != nil {
			return nil, fmt.Errorf("scan spec: %w", err)
		}
		spec, err := decodeSpec([]byte(specJSON), latest)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list specs: %w", err)
	}
	if out == nil {
		out = []process.ProcessSpec{}
	}
	return out, nil
}

// DeleteSpec removes a spec when expectedRevision matches LatestRevision.
func (s *Store) DeleteSpec(ctx context.Context, processID string, expectedRevision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete spec: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		DELETE FROM process_specs WHERE process_id = ? AND latest_revision = ?
	`, processID, expectedRevision)
	if err != nil {
		return fmt.Errorf("delete spec: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete spec rows: %w", err)
	}
	if n == 0 {
		var latest int64
		err = tx.QueryRowContext(ctx, `
			SELECT latest_revision FROM process_specs WHERE process_id = ?
		`, processID).Scan(&latest)
		if errors.Is(err, sql.ErrNoRows) {
			return errcode.E(errcode.NOT_FOUND, "process spec")
		}
		if err != nil {
			return fmt.Errorf("get spec revision: %w", err)
		}
		return errcode.E(errcode.CONFLICT, "revision mismatch")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM config_revisions WHERE process_id = ?`, processID); err != nil {
		return fmt.Errorf("delete revisions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete spec: %w", err)
	}
	return nil
}

// ListRevisions returns the revision history for a process, oldest first.
func (s *Store) ListRevisions(ctx context.Context, processID string) ([]Revision, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT revision, operator, ts, diff, comment, spec_json
		FROM config_revisions
		WHERE process_id = ?
		ORDER BY revision ASC
	`, processID)
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	defer rows.Close()

	var out []Revision
	for rows.Next() {
		var rev Revision
		var ts string
		var specJSON string
		if err := rows.Scan(&rev.Revision, &rev.Operator, &ts, &rev.Diff, &rev.Comment, &specJSON); err != nil {
			return nil, fmt.Errorf("scan revision: %w", err)
		}
		rev.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("parse revision ts: %w", err)
		}
		rev.SpecJSON = []byte(specJSON)
		out = append(out, rev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	if out == nil {
		out = []Revision{}
	}
	return out, nil
}

// RollbackSpec copies toRevision's payload into a new revision.
func (s *Store) RollbackSpec(ctx context.Context, processID string, toRevision, expectedLatest int64, operator, comment string) (process.ProcessSpec, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return process.ProcessSpec{}, fmt.Errorf("begin rollback spec: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current, err := getSpecTx(ctx, tx, processID)
	if err != nil {
		return process.ProcessSpec{}, err
	}
	if current.LatestRevision != expectedLatest {
		return process.ProcessSpec{}, errcode.E(errcode.CONFLICT, "revision mismatch")
	}

	var payload string
	err = tx.QueryRowContext(ctx, `
		SELECT spec_json FROM config_revisions WHERE process_id = ? AND revision = ?
	`, processID, toRevision).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return process.ProcessSpec{}, errcode.E(errcode.NOT_FOUND, "revision")
	}
	if err != nil {
		return process.ProcessSpec{}, fmt.Errorf("get revision: %w", err)
	}

	now := time.Now().UTC()
	out, err := decodeSpec([]byte(payload), current.LatestRevision+1)
	if err != nil {
		return process.ProcessSpec{}, err
	}
	out.UpdatedAt = now
	newPayload, err := json.Marshal(out)
	if err != nil {
		return process.ProcessSpec{}, fmt.Errorf("marshal spec: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE process_specs
		SET name = ?, latest_revision = ?, spec_json = ?
		WHERE process_id = ? AND latest_revision = ?
	`, out.Name, out.LatestRevision, string(newPayload), processID, expectedLatest)
	if err != nil {
		if isUniqueViolation(err) {
			return process.ProcessSpec{}, errcode.E(errcode.CONFLICT, "name already exists")
		}
		return process.ProcessSpec{}, fmt.Errorf("rollback spec: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return process.ProcessSpec{}, fmt.Errorf("rollback spec rows: %w", err)
	}
	if n == 0 {
		return process.ProcessSpec{}, errcode.E(errcode.CONFLICT, "revision mismatch")
	}
	if err := insertRevision(ctx, tx, processID, out.LatestRevision, operator, now, specDiff(current, out), comment, newPayload); err != nil {
		return process.ProcessSpec{}, err
	}
	if err := tx.Commit(); err != nil {
		return process.ProcessSpec{}, fmt.Errorf("commit rollback spec: %w", err)
	}
	return out, nil
}

func getSpecTx(ctx context.Context, tx *sql.Tx, processID string) (process.ProcessSpec, error) {
	return scanSpec(tx.QueryRowContext(ctx, `
		SELECT spec_json, latest_revision FROM process_specs WHERE process_id = ?
	`, processID))
}

func scanSpec(row *sql.Row) (process.ProcessSpec, error) {
	var specJSON string
	var latest int64
	err := row.Scan(&specJSON, &latest)
	if errors.Is(err, sql.ErrNoRows) {
		return process.ProcessSpec{}, errcode.E(errcode.NOT_FOUND, "process spec")
	}
	if err != nil {
		return process.ProcessSpec{}, fmt.Errorf("get spec: %w", err)
	}
	return decodeSpec([]byte(specJSON), latest)
}

func decodeSpec(specJSON []byte, latest int64) (process.ProcessSpec, error) {
	var spec process.ProcessSpec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return process.ProcessSpec{}, fmt.Errorf("decode spec: %w", err)
	}
	spec.LatestRevision = latest
	return spec, nil
}

func insertRevision(ctx context.Context, tx *sql.Tx, processID string, rev int64, operator string, ts time.Time, diff, comment string, specJSON []byte) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO config_revisions(process_id, revision, operator, ts, diff, comment, spec_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, processID, rev, operator, ts.UTC().Format(time.RFC3339Nano), diff, comment, string(specJSON))
	if err != nil {
		return fmt.Errorf("insert revision: %w", err)
	}
	return nil
}

func specDiff(oldSpec, newSpec process.ProcessSpec) string {
	var b strings.Builder
	if oldSpec.Command != newSpec.Command {
		fmt.Fprintf(&b, "-command %s\n+command %s\n", oldSpec.Command, newSpec.Command)
	}
	oldArgs := strings.Join(oldSpec.Args, " ")
	newArgs := strings.Join(newSpec.Args, " ")
	if oldArgs != newArgs {
		fmt.Fprintf(&b, "-args %s\n+args %s\n", oldArgs, newArgs)
	}
	oldEnv := formatEnv(oldSpec.Environment)
	newEnv := formatEnv(newSpec.Environment)
	if oldEnv != newEnv {
		fmt.Fprintf(&b, "-env %s\n+env %s\n", oldEnv, newEnv)
	}
	return b.String()
}

func formatEnv(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+env[k])
	}
	return strings.Join(parts, " ")
}

func isUniqueViolation(err error) bool {
	var se *sqlite3.Error
	if errors.As(err, &se) && se.Code()&0xff == 19 {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY constraint failed")
}
