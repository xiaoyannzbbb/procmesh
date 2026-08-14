//go:build darwin

package paths

import (
	"os"
	"path/filepath"
)

func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "procmesh")
	}
	return filepath.Join(home, "Library", "Application Support", "procmesh")
}
