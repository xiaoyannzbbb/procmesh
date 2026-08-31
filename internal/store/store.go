package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const (
	keySchemaVersion = "schema_version"
	schemaVersion    = "1"
)

// Store is the local SQLite authority for process-plane state.
type Store struct {
	db *sql.DB
}

// Open creates or opens a SQLite database at path, applies schema, and
// records schema_version in local_meta when missing.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_busy_timeout=5000&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("busy_timeout: %w", err)
	}
	// One connection: concurrent writers queue instead of SQLITE_BUSY on
	// deferred-tx upgrade (CAS PutSpec). Agent is local-first; this is fine.
	db.SetMaxOpenConns(1)
	if err := ensureUpdateJobOperationID(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureBackupIndexBytes(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureInstanceLastError(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureSchemaVersion(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func ensureBackupIndexBytes(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(backup_index)`)
	if err != nil {
		return fmt.Errorf("inspect backup_index: %w", err)
	}
	foundBytes := false
	foundDestinationProfile := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan backup_index schema: %w", err)
		}
		if name == "bytes" {
			foundBytes = true
		}
		if name == "destination_profile" {
			foundDestinationProfile = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("inspect backup_index rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close backup_index schema: %w", err)
	}
	if !foundBytes {
		if _, err := db.Exec(`ALTER TABLE backup_index ADD COLUMN bytes INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate backup_index bytes: %w", err)
		}
	}
	if !foundDestinationProfile {
		if _, err := db.Exec(`ALTER TABLE backup_index ADD COLUMN destination_profile TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate backup_index destination profile: %w", err)
		}
	}
	return nil
}

func ensureInstanceLastError(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(process_instances)`)
	if err != nil {
		return fmt.Errorf("inspect process_instances: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan process_instances schema: %w", err)
		}
		if name == "last_error" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("inspect process_instances rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close process_instances schema: %w", err)
	}
	if found {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE process_instances ADD COLUMN last_error TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrate process_instances last_error: %w", err)
	}
	return nil
}

func ensureUpdateJobOperationID(db *sql.DB) error {
	var table string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='update_jobs'`).Scan(&table)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("inspect update_jobs table: %w", err)
	}
	rows, err := db.Query(`PRAGMA table_info(update_jobs)`)
	if err != nil {
		return fmt.Errorf("inspect update_jobs: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan update_jobs schema: %w", err)
		}
		if name == "operation_id" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("inspect update_jobs rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close update_jobs schema: %w", err)
	}
	if !found {
		if _, err := db.Exec(`ALTER TABLE update_jobs ADD COLUMN operation_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate update_jobs operation_id: %w", err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS update_jobs_operation_id ON update_jobs(operation_id) WHERE operation_id != ''`); err != nil {
		return fmt.Errorf("migrate update_jobs operation_id index: %w", err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// IntegrityCheck runs PRAGMA integrity_check and returns DEGRADED if not ok.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	return s.runIntegrityCheck(ctx, "PRAGMA integrity_check")
}

// QuickCheck runs PRAGMA quick_check(1) and returns DEGRADED if not ok.
func (s *Store) QuickCheck(ctx context.Context) error {
	return s.runIntegrityCheck(ctx, "PRAGMA quick_check(1)")
}

func (s *Store) runIntegrityCheck(ctx context.Context, query string) error {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return errcode.E(errcode.DEGRADED, err.Error())
	}
	defer rows.Close()

	var msgs []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return errcode.E(errcode.DEGRADED, err.Error())
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return errcode.E(errcode.DEGRADED, err.Error())
	}
	return checkIntegrityMessages(msgs)
}

func checkIntegrityMessages(msgs []string) error {
	if len(msgs) == 1 && msgs[0] == "ok" {
		return nil
	}
	if len(msgs) == 0 {
		return errcode.E(errcode.DEGRADED, "integrity_check returned no rows")
	}
	return errcode.E(errcode.DEGRADED, strings.Join(msgs, "; "))
}

func applySchema(db *sql.DB) error {
	for _, raw := range strings.Split(schemaSQL, ";") {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	return nil
}

func ensureSchemaVersion(db *sql.DB) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO local_meta(k, v) VALUES(?, ?)`,
		keySchemaVersion, schemaVersion,
	)
	if err != nil {
		return fmt.Errorf("set schema_version: %w", err)
	}
	return nil
}
