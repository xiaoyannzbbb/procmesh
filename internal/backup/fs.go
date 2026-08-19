package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
)

var snapshotIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// FSSink stores snapshot payloads as JSON files under a local directory.
type FSSink struct {
	dir string
}

// NewFSSink returns a filesystem Sink rooted at dir.
func NewFSSink(dir string) *FSSink {
	return &FSSink{dir: dir}
}

// Name returns the sink identifier "fs".
func (s *FSSink) Name() string { return "fs" }

func (s *FSSink) validateID(id string) error {
	if !snapshotIDRe.MatchString(id) {
		return errcode.E(errcode.INVALID, "invalid snapshot id")
	}
	return nil
}

func (s *FSSink) pathFor(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func (s *FSSink) clusterPathFor(clusterID, nodeID, id string) string {
	return filepath.Join(s.dir, clusterID, nodeID, id+".json")
}

// Put writes payload to {dir}/{id}.json via tmp+rename with mode 0600.
func (s *FSSink) Put(ctx context.Context, id string, payload []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.validateID(id); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir backup fs: %w", err)
	}
	final := s.pathFor(id)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return "", fmt.Errorf("write backup tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename backup: %w", err)
	}
	if err := os.Chmod(final, 0o600); err != nil {
		return "", fmt.Errorf("chmod backup: %w", err)
	}
	return final, nil
}

// PutCluster writes payload to {dir}/{clusterID}/{nodeID}/{id}.json via tmp+rename with mode 0600.
func (s *FSSink) PutCluster(ctx context.Context, clusterID, policyID, nodeID, id string, payload []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.validateID(id); err != nil {
		return "", err
	}
	dir := filepath.Join(s.dir, clusterID, nodeID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir backup cluster fs: %w", err)
	}
	final := s.clusterPathFor(clusterID, nodeID, id)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return "", fmt.Errorf("write backup tmp: %w", err)
	}
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0o600); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename backup: %w", err)
	}
	if err := os.Chmod(final, 0o600); err != nil {
		return "", fmt.Errorf("chmod backup: %w", err)
	}
	return final, nil
}

// Get reads a snapshot payload by id.
func (s *FSSink) Get(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.validateID(id); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.pathFor(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errcode.E(errcode.NOT_FOUND, "snapshot not found")
		}
		return nil, fmt.Errorf("read backup: %w", err)
	}
	return data, nil
}

// List enumerates *.json snapshot files in the sink directory.
func (s *FSSink) List(ctx context.Context) ([]Listed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Listed{}, nil
		}
		return nil, fmt.Errorf("list backup fs: %w", err)
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
		if err := s.validateID(id); err != nil {
			continue
		}
		out = append(out, Listed{
			SnapshotID: id,
			Location:   s.pathFor(id),
		})
	}
	return out, nil
}

// ListCluster enumerates snapshots under {dir}/{clusterID}/*/*.json.
// If policyID is provided, it's ignored (FS sink doesn't use policy-based filtering).
func (s *FSSink) ListCluster(ctx context.Context, clusterID, policyID string) ([]Listed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clusterDir := filepath.Join(s.dir, clusterID)
	if _, err := os.Stat(clusterDir); os.IsNotExist(err) {
		return []Listed{}, nil
	}

	var out []Listed
	nodeEntries, err := os.ReadDir(clusterDir)
	if err != nil {
		return nil, fmt.Errorf("list cluster backup fs: %w", err)
	}

	for _, nodeEntry := range nodeEntries {
		if !nodeEntry.IsDir() {
			continue
		}
		nodeID := nodeEntry.Name()
		nodeDir := filepath.Join(clusterDir, nodeID)
		snapEntries, err := os.ReadDir(nodeDir)
		if err != nil {
			continue
		}

		for _, snapEntry := range snapEntries {
			if snapEntry.IsDir() {
				continue
			}
			name := snapEntry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			if err := s.validateID(id); err != nil {
				continue
			}
			out = append(out, Listed{
				SnapshotID: id,
				Location:   s.clusterPathFor(clusterID, nodeID, id),
			})
		}
	}
	return out, nil
}

// Delete removes a snapshot file by id.
func (s *FSSink) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateID(id); err != nil {
		return err
	}
	err := os.Remove(s.pathFor(id))
	if err != nil {
		if os.IsNotExist(err) {
			return errcode.E(errcode.NOT_FOUND, "snapshot not found")
		}
		return fmt.Errorf("delete backup: %w", err)
	}
	return nil
}

var _ Sink = (*FSSink)(nil)
var _ ClusterSink = (*FSSink)(nil)
