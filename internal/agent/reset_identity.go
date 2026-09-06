package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/identity"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/store"
)

// ResetNodeIdentity rotates an uninitialized Agent's local identity while
// preserving all process-plane data. The Agent must be stopped by the caller.
func ResetNodeIdentity(ctx context.Context, dataDir string) (string, error) {
	if dataDir == "" || dataDir != strings.TrimSpace(dataDir) || !filepath.IsAbs(dataDir) {
		return "", errcode.E(errcode.INVALID, "an absolute data directory is required")
	}
	layout := paths.New(filepath.Clean(dataDir))
	if err := requireExistingLocalIdentity(layout); err != nil {
		return "", err
	}
	if !dataDirLockSupported() {
		return "", errcode.E(errcode.UNAVAILABLE, "node identity reset requires data directory locking on this platform")
	}
	if err := layout.Ensure(); err != nil {
		return "", fmt.Errorf("ensure layout: %w", err)
	}
	dataLock, err := acquireDataDirLock(layout.Root)
	if err != nil {
		return "", err
	}
	defer dataLock.Close()
	if control.AlreadyInited(layout.ClusterDir) {
		return "", errcode.E(errcode.CONFLICT, "cluster already initialized; node identity reset refused")
	}
	if err := requireResettableIdentityState(layout); err != nil {
		return "", err
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		return "", fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := api.DiscardPendingJoin(layout.ClusterDir); err != nil {
		return "", err
	}
	return identity.Reset(ctx, layout, st)
}

func requireExistingLocalIdentity(layout paths.Layout) error {
	root, err := os.Lstat(layout.Root)
	if err != nil || !root.IsDir() {
		return errcode.E(errcode.INVALID, "existing data directory required")
	}
	for _, path := range []string{layout.NodeIDFile(), layout.Store} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errcode.E(errcode.INVALID, "existing node identity required")
		}
	}
	return nil
}

func requireResettableIdentityState(layout paths.Layout) error {
	clusterEntries, err := os.ReadDir(layout.ClusterDir)
	if err != nil {
		return fmt.Errorf("read cluster identity directory: %w", err)
	}
	for _, entry := range clusterEntries {
		if entry.Name() == "join.pending.json" || strings.HasPrefix(entry.Name(), ".join.pending-") {
			continue
		}
		return errcode.E(errcode.CONFLICT, "persisted cluster identity exists; node identity reset refused")
	}
	raftEntries, err := os.ReadDir(layout.RaftDir)
	if err != nil {
		return fmt.Errorf("read raft directory: %w", err)
	}
	if len(raftEntries) != 0 {
		return errcode.E(errcode.CONFLICT, "persisted raft state exists; node identity reset refused")
	}
	return nil
}
