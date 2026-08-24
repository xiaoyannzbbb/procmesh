package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
)

type backupPauseStage int

const (
	backupPauseAfterRunClaim backupPauseStage = iota
	backupPauseBeforeTaskDispatch
)

type manualBackupPause struct {
	want        int
	reached     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	count       int
	reachedOnce sync.Once
	releaseOnce sync.Once
}

func newManualBackupPause(want int) *manualBackupPause {
	return &manualBackupPause{want: want, reached: make(chan struct{}), release: make(chan struct{})}
}

func (p *manualBackupPause) block() {
	p.mu.Lock()
	p.count++
	if p.count >= p.want {
		p.reachedOnce.Do(func() { close(p.reached) })
	}
	p.mu.Unlock()
	<-p.release
}

func waitBackupPauseOrTick(t *testing.T, pause *manualBackupPause, tick <-chan error) {
	t.Helper()
	select {
	case <-pause.reached:
	case err := <-tick:
		t.Fatalf("backup coordinator tick finished before pause: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for backup coordinator pause")
	}
}

func (p *manualBackupPause) resume() {
	p.releaseOnce.Do(func() { close(p.release) })
}

type manualRunTriggers struct {
	hooks         *runTriggers
	agentLoop     chan struct{}
	agentLoopDone chan struct{}
	alertScan     chan struct{}
	alertScanDone chan struct{}
	runtimeMu     sync.RWMutex
	runtime       *rpcRuntime
	backupPauseMu sync.RWMutex
	backupPauses  map[backupPauseStage]*manualBackupPause
	dispatchMu    sync.Mutex
	dispatches    []backup.BackupTaskRequest
}

func newManualRunTriggers() *manualRunTriggers {
	m := &manualRunTriggers{
		agentLoop:     make(chan struct{}),
		agentLoopDone: make(chan struct{}, 1),
		alertScan:     make(chan struct{}),
		alertScanDone: make(chan struct{}, 1),
		backupPauses:  make(map[backupPauseStage]*manualBackupPause),
	}
	m.hooks = &runTriggers{
		agentLoop: m.agentLoop,
		afterAgentLoop: func() {
			m.agentLoopDone <- struct{}{}
		},
		alertScan: m.alertScan,
		afterAlertScan: func() {
			m.alertScanDone <- struct{}{}
		},
		runtimeReady: func(runtime *rpcRuntime) {
			m.runtimeMu.Lock()
			m.runtime = runtime
			m.runtimeMu.Unlock()
		},
		afterBackupRunClaim: func() { m.blockBackup(backupPauseAfterRunClaim) },
		beforeBackupTaskDispatch: func(task backup.BackupTaskRequest) {
			m.recordBackupDispatch(task)
			m.blockBackup(backupPauseBeforeTaskDispatch)
		},
	}
	return m
}

func (m *manualRunTriggers) rpcRuntime() *rpcRuntime {
	m.runtimeMu.RLock()
	defer m.runtimeMu.RUnlock()
	return m.runtime
}

func (m *manualRunTriggers) pauseBackup(stage backupPauseStage, want int) *manualBackupPause {
	pause := newManualBackupPause(want)
	m.backupPauseMu.Lock()
	m.backupPauses[stage] = pause
	m.backupPauseMu.Unlock()
	return pause
}

func (m *manualRunTriggers) blockBackup(stage backupPauseStage) {
	m.backupPauseMu.RLock()
	pause := m.backupPauses[stage]
	m.backupPauseMu.RUnlock()
	if pause != nil {
		pause.block()
	}
}

func (m *manualRunTriggers) triggerAlertScan(t *testing.T) {
	t.Helper()
	triggerAndWait(t, "alert scan", m.alertScan, m.alertScanDone)
}

func (m *manualRunTriggers) triggerAgentLoop(t *testing.T) {
	t.Helper()
	triggerAndWait(t, "agent loop", m.agentLoop, m.agentLoopDone)
}

func (m *manualRunTriggers) triggerBackupCoordinatorAsync(t *testing.T) <-chan error {
	t.Helper()
	runtime := m.rpcRuntime()
	if runtime == nil || runtime.backupCoord == nil {
		t.Fatal("backup coordinator unavailable")
	}
	done := make(chan error, 1)
	go func() {
		done <- runtime.backupCoord.Tick(runtime.ctx)
	}()
	return done
}

func (m *manualRunTriggers) triggerBackupCoordinator(t *testing.T) error {
	t.Helper()
	runtime := m.rpcRuntime()
	if runtime == nil || runtime.backupCoord == nil {
		t.Fatal("backup coordinator unavailable")
	}
	return runtime.backupCoord.Tick(runtime.ctx)
}

func waitBackupCoordinatorTick(t *testing.T, tick <-chan error) error {
	t.Helper()
	select {
	case err := <-tick:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("backup coordinator tick did not stop")
		return nil
	}
}

func (m *manualRunTriggers) recordBackupDispatch(task backup.BackupTaskRequest) {
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	m.dispatches = append(m.dispatches, task)
}

func (m *manualRunTriggers) takeBackupDispatches() []backup.BackupTaskRequest {
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	dispatches := append([]backup.BackupTaskRequest(nil), m.dispatches...)
	m.dispatches = nil
	return dispatches
}

func triggerAndWait(t *testing.T, name string, trigger chan<- struct{}, done <-chan struct{}) {
	t.Helper()
	select {
	case trigger <- struct{}{}:
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout triggering %s", name)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s", name)
	}
}
