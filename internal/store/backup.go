package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// BackupRecord is one row in the local backup_index.
type BackupRecord struct {
	SnapshotID         string
	ClusterID          string
	NodeID             string
	CreatedAt          time.Time
	ProcessIDs         []string
	RevisionRangesJSON string
	SHA256             string
	Sink               string
	Location           string
	SourceNodeID       string
}

const backupCols = `snapshot_id, cluster_id, node_id, created_at, process_ids_json,
	revision_range_json, sha256, sink, location, source_node_id`

// PutBackup inserts or replaces a backup_index row by snapshot_id.
func (s *Store) PutBackup(ctx context.Context, rec BackupRecord) error {
	if rec.SnapshotID == "" {
		return errcode.E(errcode.INVALID, "snapshot_id required")
	}
	ids := rec.ProcessIDs
	if ids == nil {
		ids = []string{}
	}
	pids, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("marshal process_ids: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO backup_index(
			snapshot_id, cluster_id, node_id, created_at, process_ids_json,
			revision_range_json, sha256, sink, location, source_node_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(snapshot_id) DO UPDATE SET
			cluster_id = excluded.cluster_id,
			node_id = excluded.node_id,
			created_at = excluded.created_at,
			process_ids_json = excluded.process_ids_json,
			revision_range_json = excluded.revision_range_json,
			sha256 = excluded.sha256,
			sink = excluded.sink,
			location = excluded.location,
			source_node_id = excluded.source_node_id
	`, rec.SnapshotID, rec.ClusterID, rec.NodeID, rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		string(pids), rec.RevisionRangesJSON, rec.SHA256, rec.Sink, rec.Location, rec.SourceNodeID)
	if err != nil {
		return fmt.Errorf("put backup: %w", err)
	}
	return nil
}

// GetBackup returns a backup_index row. Missing returns NOT_FOUND.
func (s *Store) GetBackup(ctx context.Context, snapshotID string) (BackupRecord, error) {
	return scanBackup(s.db.QueryRowContext(ctx, `
		SELECT `+backupCols+` FROM backup_index WHERE snapshot_id = ?
	`, snapshotID))
}

// ListBackups returns all backup_index rows ordered by created_at DESC.
func (s *Store) ListBackups(ctx context.Context) ([]BackupRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+backupCols+` FROM backup_index ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()

	var out []BackupRecord
	for rows.Next() {
		rec, err := scanBackupRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	if out == nil {
		out = []BackupRecord{}
	}
	return out, nil
}

// DeleteBackup removes a backup_index row. Missing returns NOT_FOUND.
func (s *Store) DeleteBackup(ctx context.Context, snapshotID string) error {
	if snapshotID == "" {
		return errcode.E(errcode.INVALID, "snapshot_id required")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM backup_index WHERE snapshot_id = ?`, snapshotID)
	if err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete backup rows: %w", err)
	}
	if n == 0 {
		return errcode.E(errcode.NOT_FOUND, "backup")
	}
	return nil
}

func scanBackup(row *sql.Row) (BackupRecord, error) {
	var rec BackupRecord
	var createdAt, processIDsJSON string
	err := row.Scan(
		&rec.SnapshotID, &rec.ClusterID, &rec.NodeID, &createdAt, &processIDsJSON,
		&rec.RevisionRangesJSON, &rec.SHA256, &rec.Sink, &rec.Location, &rec.SourceNodeID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupRecord{}, errcode.E(errcode.NOT_FOUND, "backup")
	}
	if err != nil {
		return BackupRecord{}, fmt.Errorf("get backup: %w", err)
	}
	if err := fillBackupFields(&rec, createdAt, processIDsJSON); err != nil {
		return BackupRecord{}, err
	}
	return rec, nil
}

func scanBackupRow(rows *sql.Rows) (BackupRecord, error) {
	var rec BackupRecord
	var createdAt, processIDsJSON string
	err := rows.Scan(
		&rec.SnapshotID, &rec.ClusterID, &rec.NodeID, &createdAt, &processIDsJSON,
		&rec.RevisionRangesJSON, &rec.SHA256, &rec.Sink, &rec.Location, &rec.SourceNodeID,
	)
	if err != nil {
		return BackupRecord{}, fmt.Errorf("scan backup: %w", err)
	}
	if err := fillBackupFields(&rec, createdAt, processIDsJSON); err != nil {
		return BackupRecord{}, err
	}
	return rec, nil
}

func fillBackupFields(rec *BackupRecord, createdAt, processIDsJSON string) error {
	var err error
	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return fmt.Errorf("parse backup created_at: %w", err)
	}
	if processIDsJSON == "" || processIDsJSON == "null" {
		rec.ProcessIDs = []string{}
		return nil
	}
	if err := json.Unmarshal([]byte(processIDsJSON), &rec.ProcessIDs); err != nil {
		return fmt.Errorf("parse backup process_ids: %w", err)
	}
	if rec.ProcessIDs == nil {
		rec.ProcessIDs = []string{}
	}
	return nil
}
