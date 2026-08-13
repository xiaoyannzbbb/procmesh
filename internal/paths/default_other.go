//go:build !linux && !darwin

package paths

import (
	"os"
	"path/filepath"
)

func DefaultRoot() string {
	return filepath.Join(os.TempDir(), "procmesh")
}
