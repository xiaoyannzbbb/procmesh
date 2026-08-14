package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
)

const (
	keyNodeID    = "node_id"
	keyBootID    = "boot_id"
	keyClusterID = "cluster_id"
)

// GetOrCreateNodeID returns the persisted node UUID, creating one if missing.
func (s *Store) GetOrCreateNodeID(ctx context.Context) (string, error) {
	id, err := s.getMeta(ctx, keyNodeID)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("get node_id: %w", err)
	}
	id, err = newUUID()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO local_meta(k, v) VALUES(?, ?)`, keyNodeID, id)
	if err != nil {
		return "", fmt.Errorf("insert node_id: %w", err)
	}
	id, err = s.getMeta(ctx, keyNodeID)
	if err != nil {
		return "", fmt.Errorf("reread node_id: %w", err)
	}
	return id, nil
}

// RotateBootID always writes a new boot UUID and returns it.
// Kept for store tests; agent startup uses SetBootID with the OS boot id.
func (s *Store) RotateBootID(ctx context.Context) (string, error) {
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	if err := s.putMeta(ctx, keyBootID, id); err != nil {
		return "", fmt.Errorf("rotate boot_id: %w", err)
	}
	return id, nil
}

// SetBootID persists the OS boot identity (or any explicit boot id string).
func (s *Store) SetBootID(ctx context.Context, id string) error {
	if err := s.putMeta(ctx, keyBootID, id); err != nil {
		return fmt.Errorf("set boot_id: %w", err)
	}
	return nil
}

// GetBootID returns the current boot id, or "" if none has been set yet.
func (s *Store) GetBootID(ctx context.Context) (string, error) {
	id, err := s.getMeta(ctx, keyBootID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get boot_id: %w", err)
	}
	return id, nil
}

// SetClusterID persists the cluster UUID.
func (s *Store) SetClusterID(ctx context.Context, id string) error {
	if err := s.putMeta(ctx, keyClusterID, id); err != nil {
		return fmt.Errorf("set cluster_id: %w", err)
	}
	return nil
}

// GetClusterID returns the persisted cluster UUID, or "" if unset.
func (s *Store) GetClusterID(ctx context.Context) (string, error) {
	id, err := s.getMeta(ctx, keyClusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get cluster_id: %w", err)
	}
	return id, nil
}

func (s *Store) getMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM local_meta WHERE k = ?`, key).Scan(&v)
	if err != nil {
		return "", err
	}
	return v, nil
}

func (s *Store) putMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO local_meta(k, v) VALUES(?, ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v
	`, key, value)
	return err
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
