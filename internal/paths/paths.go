package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const dirMode = 0o750

// Layout is the on-disk directory layout for a procmesh data root.
type Layout struct {
	Root       string
	Store      string
	ShimDir    string
	LogDir     string
	RuntimeDir string
	ClusterDir string
	RaftDir    string
}

// New returns the standard layout under root.
func New(root string) Layout {
	return Layout{
		Root:       root,
		Store:      filepath.Join(root, "store.db"),
		ShimDir:    filepath.Join(root, "shim"),
		LogDir:     filepath.Join(root, "logs"),
		RuntimeDir: filepath.Join(root, "runtime"),
		ClusterDir: filepath.Join(root, "cluster"),
		RaftDir:    filepath.Join(root, "raft"),
	}
}

// ShimSocket returns shim/<sanitized-instance-id>.sock with ':' replaced by '_'.
func (l Layout) ShimSocket(instanceID string) string {
	return filepath.Join(l.ShimDir, strings.ReplaceAll(instanceID, ":", "_")+".sock")
}

// NodeIDFile is the durable node identity file under the data root.
func (l Layout) NodeIDFile() string { return filepath.Join(l.Root, "node_id") }

// BootIDFile is the last-seen host boot id file under the data root.
func (l Layout) BootIDFile() string { return filepath.Join(l.Root, "boot_id") }

// BackupRoot is the local backup directory under the data root.
// Not created by Ensure; sinks MkdirAll on Put.
func (l Layout) BackupRoot() string { return filepath.Join(l.Root, "backup") }

// BackupFSDir is the filesystem sink directory.
func (l Layout) BackupFSDir() string { return filepath.Join(l.BackupRoot(), "fs") }

// BackupReplicaDir is the disaster-replica capture sink directory.
func (l Layout) BackupReplicaDir() string { return filepath.Join(l.BackupRoot(), "replica") }

// BackupPeerDir is the peer-received snapshot directory for sourceNodeID.
func (l Layout) BackupPeerDir(sourceNodeID string) string {
	return filepath.Join(l.BackupRoot(), "peer", sourceNodeID)
}

// Ensure creates layout directories with mode 0750.
func (l Layout) Ensure() error {
	for _, dir := range []string{l.Root, l.ShimDir, l.LogDir, l.RuntimeDir, l.ClusterDir, l.RaftDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return fmt.Errorf("ensure %s: %w", dir, err)
		}
		if err := os.Chmod(dir, dirMode); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	return nil
}
