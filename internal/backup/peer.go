package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/paths"
)

// ReceiveParams contains all parameters for receiving a peer snapshot with metadata.
type ReceiveParams struct {
	SourceNodeID string
	ClusterID    string
	SnapshotID   string
	SHA256       string
	RunID        string
	TaskID       string
	Payload      []byte
}

// PeerStore is a receive-only local store for snapshots pushed by peers.
type PeerStore struct {
	Root string // data_dir
}

func (p *PeerStore) validateID(id string) error {
	if id == "." || id == ".." || !snapshotIDRe.MatchString(id) {
		return errcode.E(errcode.INVALID, "invalid snapshot id")
	}
	return nil
}

func (p *PeerStore) validateSource(sourceNodeID string) error {
	if sourceNodeID == "." || sourceNodeID == ".." || !snapshotIDRe.MatchString(sourceNodeID) {
		return errcode.E(errcode.INVALID, "invalid source node id")
	}
	return nil
}

func (p *PeerStore) validateCluster(clusterID string) error {
	if clusterID == "." || clusterID == ".." || !snapshotIDRe.MatchString(clusterID) {
		return errcode.E(errcode.INVALID, "invalid cluster id")
	}
	return nil
}

func (p *PeerStore) dir(sourceNodeID, clusterID string) string {
	return filepath.Join(paths.New(p.Root).BackupRoot(), "peer", sourceNodeID, clusterID)
}

func (p *PeerStore) pathFor(sourceNodeID, clusterID, snapshotID string) string {
	return filepath.Join(p.dir(sourceNodeID, clusterID), snapshotID+".json")
}

// ReceiveWithMetadata receives a peer snapshot with extended metadata and validation.
// It verifies checksum, cluster ID, and implements atomic write with idempotency.
func (p *PeerStore) ReceiveWithMetadata(ctx context.Context, params ReceiveParams) (Meta, error) {
	if err := ctx.Err(); err != nil {
		return Meta{}, err
	}
	if err := p.validateSource(params.SourceNodeID); err != nil {
		return Meta{}, err
	}
	if err := p.validateID(params.SnapshotID); err != nil {
		return Meta{}, err
	}
	if err := p.validateCluster(params.ClusterID); err != nil {
		return Meta{}, err
	}
	if params.SHA256 == "" {
		return Meta{}, errcode.E(errcode.INVALID, "sha256 required")
	}

	// Validate SHA256 format (64 hex chars)
	if len(params.SHA256) != 64 {
		return Meta{}, errcode.E(errcode.INVALID, "sha256 must be 64 hex characters")
	}
	if _, err := hex.DecodeString(params.SHA256); err != nil {
		return Meta{}, errcode.E(errcode.INVALID, "sha256 must be valid hex")
	}

	// Verify payload checksum
	actualSum := sha256.Sum256(params.Payload)
	actualHex := hex.EncodeToString(actualSum[:])
	if actualHex != params.SHA256 {
		return Meta{}, errcode.E(errcode.INVALID, "payload checksum mismatch")
	}

	// Decode and validate snapshot
	snap, err := Decode(params.Payload)
	if err != nil {
		return Meta{}, err
	}

	// Verify cluster ID matches
	if snap.ClusterID != params.ClusterID {
		return Meta{}, errcode.E(errcode.INVALID, "cluster_id mismatch")
	}

	// Verify snapshot ID matches
	if snap.SnapshotID != params.SnapshotID {
		return Meta{}, errcode.E(errcode.INVALID, "snapshot_id mismatch")
	}

	// Check if snapshot already exists
	dir := p.dir(params.SourceNodeID, params.ClusterID)
	final := p.pathFor(params.SourceNodeID, params.ClusterID, params.SnapshotID)

	if existing, err := os.ReadFile(final); err == nil {
		// File exists - check if checksum matches (idempotency)
		existingSum := sha256.Sum256(existing)
		existingHex := hex.EncodeToString(existingSum[:])
		if existingHex != params.SHA256 {
			return Meta{}, errcode.E(errcode.CONFLICT, "snapshot exists with different checksum")
		}
		// Same checksum - idempotent, return existing meta
		meta := MetaFromSnapshot(snap)
		meta.SHA256 = params.SHA256
		meta.Bytes = int64(len(existing))
		meta.Sink = "peer"
		meta.Location = final
		meta.SourceNodeID = params.SourceNodeID
		return meta, nil
	}

	// Create directory
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Meta{}, fmt.Errorf("mkdir backup peer: %w", err)
	}

	// Atomic write: tmp file + rename
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, params.Payload, 0o600); err != nil {
		return Meta{}, fmt.Errorf("write backup peer tmp: %w", err)
	}

	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return Meta{}, fmt.Errorf("rename backup peer: %w", err)
	}

	if err := os.Chmod(final, 0o600); err != nil {
		return Meta{}, fmt.Errorf("chmod backup peer: %w", err)
	}

	meta := MetaFromSnapshot(snap)
	meta.SHA256 = params.SHA256
	meta.Bytes = int64(len(params.Payload))
	meta.Sink = "peer"
	meta.Location = final
	meta.SourceNodeID = params.SourceNodeID
	return meta, nil
}

// Receive decodes payload and writes backup/peer/<source>/<id>.json with mode 0600.
// Deprecated: Use ReceiveWithMetadata for new code.

func (p *PeerStore) Receive(ctx context.Context, sourceNodeID string, payload []byte) (Meta, error) {
	if err := ctx.Err(); err != nil {
		return Meta{}, err
	}
	if err := p.validateSource(sourceNodeID); err != nil {
		return Meta{}, err
	}
	snap, err := Decode(payload)
	if err != nil {
		return Meta{}, err
	}
	if err := p.validateID(snap.SnapshotID); err != nil {
		return Meta{}, err
	}
	dir := filepath.Join(paths.New(p.Root).BackupRoot(), "peer", sourceNodeID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Meta{}, fmt.Errorf("mkdir backup peer: %w", err)
	}
	final := filepath.Join(dir, snap.SnapshotID+".json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return Meta{}, fmt.Errorf("write backup peer tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return Meta{}, fmt.Errorf("rename backup peer: %w", err)
	}
	if err := os.Chmod(final, 0o600); err != nil {
		return Meta{}, fmt.Errorf("chmod backup peer: %w", err)
	}
	meta := MetaFromSnapshot(snap)
	sum := sha256.Sum256(payload)
	meta.SHA256 = hex.EncodeToString(sum[:])
	meta.Bytes = int64(len(payload))
	meta.Sink = "peer"
	meta.Location = final
	meta.SourceNodeID = sourceNodeID
	return meta, nil
}

// GetReplicaMetadata reads and returns metadata for a stored peer replica.
func (p *PeerStore) GetReplicaMetadata(ctx context.Context, sourceNodeID, clusterID, snapshotID string) (Meta, error) {
	if err := ctx.Err(); err != nil {
		return Meta{}, err
	}
	if err := p.validateSource(sourceNodeID); err != nil {
		return Meta{}, err
	}
	if err := p.validateID(snapshotID); err != nil {
		return Meta{}, err
	}
	if err := p.validateCluster(clusterID); err != nil {
		return Meta{}, err
	}

	path := p.pathFor(sourceNodeID, clusterID, snapshotID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, errcode.E(errcode.NOT_FOUND, "snapshot not found")
		}
		return Meta{}, fmt.Errorf("read backup peer: %w", err)
	}

	snap, err := Decode(data)
	if err != nil {
		return Meta{}, err
	}

	meta := MetaFromSnapshot(snap)
	sum := sha256.Sum256(data)
	meta.SHA256 = hex.EncodeToString(sum[:])
	meta.Bytes = int64(len(data))
	meta.Sink = "peer"
	meta.Location = path
	meta.SourceNodeID = sourceNodeID
	return meta, nil
}

// CheckSnapshot checks if a snapshot exists and whether its checksum matches.
// Returns (exists, matches, error).
func (p *PeerStore) CheckSnapshot(ctx context.Context, sourceNodeID, clusterID, snapshotID, expectedSHA256 string) (bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	if err := p.validateSource(sourceNodeID); err != nil {
		return false, false, err
	}
	if err := p.validateID(snapshotID); err != nil {
		return false, false, err
	}
	if err := p.validateCluster(clusterID); err != nil {
		return false, false, err
	}

	path := p.pathFor(sourceNodeID, clusterID, snapshotID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("read backup peer: %w", err)
	}

	sum := sha256.Sum256(data)
	actualSHA256 := hex.EncodeToString(sum[:])
	matches := actualSHA256 == expectedSHA256
	return true, matches, nil
}

// DeleteSnapshot removes a peer replica snapshot.
func (p *PeerStore) DeleteSnapshot(ctx context.Context, sourceNodeID, clusterID, snapshotID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.validateSource(sourceNodeID); err != nil {
		return err
	}
	if err := p.validateID(snapshotID); err != nil {
		return err
	}
	if err := p.validateCluster(clusterID); err != nil {
		return err
	}

	path := p.pathFor(sourceNodeID, clusterID, snapshotID)
	err := os.Remove(path)
	if err != nil {
		if os.IsNotExist(err) {
			return errcode.E(errcode.NOT_FOUND, "snapshot not found")
		}
		return fmt.Errorf("delete backup peer: %w", err)
	}
	return nil
}

// Get reads a peer-received snapshot payload.
func (p *PeerStore) Get(ctx context.Context, sourceNodeID, snapshotID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.validateSource(sourceNodeID); err != nil {
		return nil, err
	}
	if err := p.validateID(snapshotID); err != nil {
		return nil, err
	}
	// Legacy method uses old path structure
	legacyPath := filepath.Join(paths.New(p.Root).BackupRoot(), "peer", sourceNodeID, snapshotID+".json")
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errcode.E(errcode.NOT_FOUND, "snapshot not found")
		}
		return nil, fmt.Errorf("read backup peer: %w", err)
	}
	return data, nil
}

// List enumerates peer-received snapshots from one source node.
// Deprecated: Does not support new cluster-based directory structure.
func (p *PeerStore) List(ctx context.Context, sourceNodeID string) ([]Listed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.validateSource(sourceNodeID); err != nil {
		return nil, err
	}
	// Legacy method uses old path structure
	dir := filepath.Join(paths.New(p.Root).BackupRoot(), "peer", sourceNodeID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Listed{}, nil
		}
		return nil, fmt.Errorf("list backup peer: %w", err)
	}
	out := make([]Listed, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if err := p.validateID(id); err != nil {
			continue
		}
		out = append(out, Listed{
			SnapshotID: id,
			Location:   filepath.Join(dir, name),
		})
	}
	return out, nil
}

// Delete removes a peer-received snapshot file.
// Deprecated: Does not support new cluster-based directory structure.
func (p *PeerStore) Delete(ctx context.Context, sourceNodeID, snapshotID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.validateSource(sourceNodeID); err != nil {
		return err
	}
	if err := p.validateID(snapshotID); err != nil {
		return err
	}
	// Legacy method uses old path structure
	legacyPath := filepath.Join(paths.New(p.Root).BackupRoot(), "peer", sourceNodeID, snapshotID+".json")
	err := os.Remove(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return errcode.E(errcode.NOT_FOUND, "snapshot not found")
		}
		return fmt.Errorf("delete backup peer: %w", err)
	}
	return nil
}
