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

func (p *PeerStore) dir(sourceNodeID string) string {
	return paths.New(p.Root).BackupPeerDir(sourceNodeID)
}

func (p *PeerStore) pathFor(sourceNodeID, snapshotID string) string {
	return filepath.Join(p.dir(sourceNodeID), snapshotID+".json")
}

// Receive decodes payload and writes backup/peer/<source>/<id>.json with mode 0600.
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
	dir := p.dir(sourceNodeID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Meta{}, fmt.Errorf("mkdir backup peer: %w", err)
	}
	final := p.pathFor(sourceNodeID, snap.SnapshotID)
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
	meta.Sink = "peer"
	meta.Location = final
	meta.SourceNodeID = sourceNodeID
	return meta, nil
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
	data, err := os.ReadFile(p.pathFor(sourceNodeID, snapshotID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errcode.E(errcode.NOT_FOUND, "snapshot not found")
		}
		return nil, fmt.Errorf("read backup peer: %w", err)
	}
	return data, nil
}

// List enumerates peer-received snapshots from one source node.
func (p *PeerStore) List(ctx context.Context, sourceNodeID string) ([]Listed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.validateSource(sourceNodeID); err != nil {
		return nil, err
	}
	dir := p.dir(sourceNodeID)
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
			Location:   p.pathFor(sourceNodeID, id),
		})
	}
	return out, nil
}

// Delete removes a peer-received snapshot file.
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
	err := os.Remove(p.pathFor(sourceNodeID, snapshotID))
	if err != nil {
		if os.IsNotExist(err) {
			return errcode.E(errcode.NOT_FOUND, "snapshot not found")
		}
		return fmt.Errorf("delete backup peer: %w", err)
	}
	return nil
}
