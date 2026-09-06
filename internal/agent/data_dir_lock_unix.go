//go:build linux || darwin

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/qleelulu/procmesh/internal/errcode"
	"golang.org/x/sys/unix"
)

type dataDirLock struct {
	file *os.File
}

func dataDirLockSupported() bool { return true }

func acquireDataDirLock(dataDir string) (*dataDirLock, error) {
	path := filepath.Join(dataDir, "agent.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data directory lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errcode.E(errcode.CONFLICT, "data directory is in use by a running Agent")
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	return &dataDirLock{file: f}, nil
}

func (l *dataDirLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock data directory: %w", unlockErr)
	}
	return closeErr
}
