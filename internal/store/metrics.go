package store

import (
	"context"
	"fmt"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// MetricSample is one historical metrics point on the owner node.
type MetricSample struct {
	Series    string
	SubjectID string
	Layer     string
	TSUnix    int64
	Value     float64
}

// InsertMetricSamples inserts samples in one transaction using INSERT OR REPLACE.
// An empty slice is a no-op.
func (s *Store) InsertMetricSamples(ctx context.Context, samples []MetricSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin insert metric samples: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO metric_samples(series, subject_id, layer, ts_unix, value)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare insert metric samples: %w", err)
	}
	defer stmt.Close()

	for _, sample := range samples {
		if _, err := stmt.ExecContext(ctx, sample.Series, sample.SubjectID, sample.Layer, sample.TSUnix, sample.Value); err != nil {
			return fmt.Errorf("insert metric sample: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit insert metric samples: %w", err)
	}
	return nil
}

// ListMetricSamples returns points for the series in [fromUnix, toUnix] inclusive, ASC by ts_unix.
// series, subjectID, and layer must all be non-empty.
func (s *Store) ListMetricSamples(ctx context.Context, series, subjectID, layer string, fromUnix, toUnix int64) ([]MetricSample, error) {
	if series == "" || subjectID == "" || layer == "" {
		return nil, errcode.E(errcode.INVALID, "series, subject_id, and layer required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT series, subject_id, layer, ts_unix, value
		FROM metric_samples
		WHERE series = ? AND subject_id = ? AND layer = ?
		  AND ts_unix >= ? AND ts_unix <= ?
		ORDER BY ts_unix ASC
	`, series, subjectID, layer, fromUnix, toUnix)
	if err != nil {
		return nil, fmt.Errorf("list metric samples: %w", err)
	}
	defer rows.Close()

	var out []MetricSample
	for rows.Next() {
		var m MetricSample
		if err := rows.Scan(&m.Series, &m.SubjectID, &m.Layer, &m.TSUnix, &m.Value); err != nil {
			return nil, fmt.Errorf("scan metric sample: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list metric samples rows: %w", err)
	}
	return out, nil
}

// DeleteMetricSamplesBefore deletes rows with the given layer and ts_unix < tsUnix.
func (s *Store) DeleteMetricSamplesBefore(ctx context.Context, layer string, tsUnix int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM metric_samples WHERE layer = ? AND ts_unix < ?
	`, layer, tsUnix)
	if err != nil {
		return 0, fmt.Errorf("delete metric samples before: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete metric samples before rows: %w", err)
	}
	return n, nil
}

// DeleteOldestMetricSamples deletes up to limit oldest rows for layer by ts_unix ASC.
// limit <= 0 defaults to 256.
func (s *Store) DeleteOldestMetricSamples(ctx context.Context, layer string, limit int) (int64, error) {
	if limit <= 0 {
		limit = 256
	}
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM metric_samples WHERE rowid IN (
			SELECT rowid FROM metric_samples WHERE layer = ? ORDER BY ts_unix ASC LIMIT ?
		)
	`, layer, limit)
	if err != nil {
		return 0, fmt.Errorf("delete oldest metric samples: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete oldest metric samples rows: %w", err)
	}
	return n, nil
}

// CountMetricSamples returns the total number of metric sample rows.
func (s *Store) CountMetricSamples(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_samples`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count metric samples: %w", err)
	}
	return n, nil
}
