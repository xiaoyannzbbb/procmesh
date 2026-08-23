package agent

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const (
	storeHealthQuickInterval = 15 * time.Second
	storeHealthFullInterval  = 10 * time.Minute
	storeHealthCheckTimeout  = 5 * time.Second
	sqliteHeaderSize         = 16
)

type storeHealthMonitor struct {
	fullCheck   func(context.Context) error
	quickCheck  func(context.Context) error
	headerCheck func() error

	mu        sync.RWMutex
	fullErr   error
	quickErr  error
	headerErr error
}

func newStoreHealthMonitor(
	fullCheck func(context.Context) error,
	quickCheck func(context.Context) error,
	headerCheck func() error,
) *storeHealthMonitor {
	return &storeHealthMonitor{
		fullCheck:   fullCheck,
		quickCheck:  quickCheck,
		headerCheck: headerCheck,
	}
}

// initialize records the startup full-check and header state. Its return value
// intentionally reflects only the full check, whose startup failure is latched
// by the Agent's existing degraded flag.
func (m *storeHealthMonitor) initialize(ctx context.Context) error {
	m.checkFull(ctx)
	m.checkHeader()
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fullErr
}

func (m *storeHealthMonitor) ready() error {
	headerErr := m.headerCheck()
	m.mu.Lock()
	m.headerErr = headerErr
	m.mu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fullErr != nil {
		return m.fullErr
	}
	if m.quickErr != nil {
		return m.quickErr
	}
	return m.headerErr
}

func (m *storeHealthMonitor) checkFull(ctx context.Context) {
	err := m.fullCheck(ctx)
	m.mu.Lock()
	m.fullErr = err
	m.mu.Unlock()
}

func (m *storeHealthMonitor) checkQuick(ctx context.Context) {
	err := m.quickCheck(ctx)
	headerErr := m.headerCheck()
	m.mu.Lock()
	m.quickErr = err
	m.headerErr = headerErr
	m.mu.Unlock()
}

func (m *storeHealthMonitor) checkHeader() {
	err := m.headerCheck()
	m.mu.Lock()
	m.headerErr = err
	m.mu.Unlock()
}

func (m *storeHealthMonitor) runPeriodic(ctx context.Context) {
	quickTicker := time.NewTicker(storeHealthQuickInterval)
	defer quickTicker.Stop()
	fullTicker := time.NewTicker(storeHealthFullInterval)
	defer fullTicker.Stop()
	m.run(ctx, quickTicker.C, fullTicker.C)
}

func (m *storeHealthMonitor) run(ctx context.Context, quickTicks, fullTicks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-quickTicks:
			checkCtx, cancel := context.WithTimeout(ctx, storeHealthCheckTimeout)
			m.checkQuick(checkCtx)
			cancel()
		case <-fullTicks:
			checkCtx, cancel := context.WithTimeout(ctx, storeHealthCheckTimeout)
			m.checkFull(checkCtx)
			cancel()
		}
	}
}

func checkSQLiteFileHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errcode.E(errcode.DEGRADED, err.Error())
	}
	defer f.Close()
	return checkSQLiteHeader(f)
}

func checkSQLiteHeader(r io.Reader) error {
	var header [sqliteHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return errcode.E(errcode.DEGRADED, err.Error())
	}
	if string(header[:sqliteHeaderSize-1]) != "SQLite format 3" {
		return errcode.E(errcode.DEGRADED, "store file corrupted")
	}
	return nil
}
