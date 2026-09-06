//go:build !linux && !darwin

package agent

type dataDirLock struct{}

// Preserve the existing Agent startup behavior; offline identity reset checks
// dataDirLockSupported and refuses to run without an advisory lock.
func acquireDataDirLock(string) (*dataDirLock, error) {
	return &dataDirLock{}, nil
}

func dataDirLockSupported() bool { return false }

func (l *dataDirLock) Close() error { return nil }
