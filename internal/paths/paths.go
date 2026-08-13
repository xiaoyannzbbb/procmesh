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
	}
}

// ShimSocket returns shim/<sanitized-instance-id>.sock with ':' replaced by '_'.
func (l Layout) ShimSocket(instanceID string) string {
	return filepath.Join(l.ShimDir, strings.ReplaceAll(instanceID, ":", "_")+".sock")
}

// Ensure creates layout directories with mode 0750.
func (l Layout) Ensure() error {
	for _, dir := range []string{l.Root, l.ShimDir, l.LogDir, l.RuntimeDir, l.ClusterDir} {
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
