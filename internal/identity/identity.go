package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/paths"
)

const idFileMode = 0o640

var nodeIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Meta is the store surface needed to keep node_id in sync with the file.
type Meta interface {
	GetOrCreateNodeID(ctx context.Context) (string, error)
	SetNodeID(ctx context.Context, id string) error
}

// Ensure aligns $data_dir/node_id and $data_dir/boot_id with the store.
// File node_id wins when present and valid; otherwise store GetOrCreateNodeID
// is used and written to the file. boot_id file is always overwritten with hostBoot.
// Does not rotate or rewrite store boot_id.
func Ensure(ctx context.Context, layout paths.Layout, meta Meta, hostBoot string) (nodeID string, err error) {
	if err := layout.Ensure(); err != nil {
		return "", fmt.Errorf("ensure layout: %w", err)
	}

	raw, err := os.ReadFile(layout.NodeIDFile())
	switch {
	case err == nil:
		id := strings.TrimSpace(string(raw))
		if !nodeIDRE.MatchString(id) {
			return "", errcode.E(errcode.INVALID, "node_id must be a UUID")
		}
		if err := meta.SetNodeID(ctx, id); err != nil {
			return "", err
		}
		nodeID = id
	case os.IsNotExist(err):
		id, err := meta.GetOrCreateNodeID(ctx)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(layout.NodeIDFile(), []byte(id+"\n"), idFileMode); err != nil {
			return "", fmt.Errorf("write node_id: %w", err)
		}
		nodeID = id
	default:
		return "", fmt.Errorf("read node_id: %w", err)
	}

	if err := os.WriteFile(layout.BootIDFile(), []byte(hostBoot+"\n"), idFileMode); err != nil {
		return "", fmt.Errorf("write boot_id: %w", err)
	}
	return nodeID, nil
}

// Reset replaces the durable local node identity without touching process,
// log, backup, or metrics data. Callers must first verify that the node has
// not completed cluster initialization and that no Agent is using the data
// directory.
func Reset(ctx context.Context, layout paths.Layout, meta Meta) (string, error) {
	return reset(ctx, layout, meta, writeAtomicNodeID)
}

func reset(ctx context.Context, layout paths.Layout, meta Meta, writeNodeID func(string, string) (bool, error)) (string, error) {
	oldID, err := meta.GetOrCreateNodeID(ctx)
	if err != nil {
		return "", err
	}
	newID, err := newNodeID()
	if err != nil {
		return "", err
	}
	if err := meta.SetNodeID(ctx, newID); err != nil {
		return "", err
	}
	committed, err := writeNodeID(layout.NodeIDFile(), newID)
	if err != nil {
		if !committed {
			if rollbackErr := meta.SetNodeID(ctx, oldID); rollbackErr != nil {
				return "", errors.Join(err, fmt.Errorf("restore node id in store: %w", rollbackErr))
			}
		}
		return "", err
	}
	return newID, nil
}

func newNodeID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate node id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexID := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexID[:8], hexID[8:12], hexID[12:16], hexID[16:20], hexID[20:]), nil
}

func writeAtomicNodeID(path, nodeID string) (committed bool, retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".node_id-*")
	if err != nil {
		return false, fmt.Errorf("create node_id: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(idFileMode); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("chmod node_id: %w", err)
	}
	if _, err := tmp.WriteString(nodeID + "\n"); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write node_id: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("sync node_id: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close node_id: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("commit node_id: %w", err)
	}
	committed = true
	d, err := os.Open(dir)
	if err != nil {
		return true, fmt.Errorf("open node_id directory after commit: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return true, fmt.Errorf("sync node_id directory after commit: %w", err)
	}
	return true, nil
}
