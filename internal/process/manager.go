package process

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/health"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/shim"
)

// StateStore is the persistence surface Manager needs.
// *store.Store implements it without a process→store import cycle.
type StateStore interface {
	PutSpec(ctx context.Context, spec ProcessSpec, expectedRevision int64, operator, comment string) (ProcessSpec, error)
	GetSpec(ctx context.Context, processID string) (ProcessSpec, error)
	ListSpecs(ctx context.Context) ([]ProcessSpec, error)
	DeleteSpec(ctx context.Context, processID string, expectedRevision int64) error
	PutInstance(ctx context.Context, inst Instance) error
	GetInstance(ctx context.Context, instanceID string) (Instance, error)
	ListInstances(ctx context.Context, processID string) ([]Instance, error)
	GetBootID(ctx context.Context) (string, error)
	StartOp(ctx context.Context, opID, operator, typ, target string, payload []byte) (duplicate bool, status, errMsg string, err error)
	FinishOperation(ctx context.Context, operationID, status string, result []byte, errMsg string) error
	GetOp(ctx context.Context, operationID string) (status string, result []byte, errMsg string, err error)
	WriteAudit(ctx context.Context, resource, action, opID, operator, result string) error
}

const (
	opSuccess = "SUCCESS"
	opFailed  = "FAILED"
	opRunning = "RUNNING"
)

// Deps wires Manager to store, layout, and optional log manager.
type Deps struct {
	Store    StateStore
	Layout   paths.Layout
	ShimBin  string
	Now      func() time.Time
	LookUser func(user string) error
	// Logs is optional disk-protection state. Nil is accepted (writes allowed).
	Logs *logmgr.Manager
}

// Manager reconciles desired process specs against observed instances.
type Manager struct {
	mu                sync.Mutex
	deps              Deps
	clients           map[string]*shim.Client
	failures          map[string][]time.Time
	nextTry           map[string]time.Time
	healthTrackers    map[string]*health.Tracker
	lastHealthCheck   map[string]time.Time
	lastHealthRestart map[string]time.Time
}

func NewManager(d Deps) *Manager {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Manager{
		deps:              d,
		clients:           make(map[string]*shim.Client),
		failures:          make(map[string][]time.Time),
		nextTry:           make(map[string]time.Time),
		healthTrackers:    make(map[string]*health.Tracker),
		lastHealthCheck:   make(map[string]time.Time),
		lastHealthRestart: make(map[string]time.Time),
	}
}

func (m *Manager) ApplySpec(ctx context.Context, spec ProcessSpec, expectedRevision int64, opID, operator, comment string) (ProcessSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	applyDefaults(&spec)
	if err := ValidateSpec(spec); err != nil {
		return ProcessSpec{}, err
	}
	done, existing, err := m.beginOp(ctx, opID, operator, "apply_spec", spec.ProcessID, nil)
	if err != nil {
		return ProcessSpec{}, err
	}
	if done {
		got, getErr := m.deps.Store.GetSpec(ctx, spec.ProcessID)
		if getErr != nil {
			if existing.Error != "" {
				return ProcessSpec{}, errcode.E(errcode.INVALID, existing.Error)
			}
			return ProcessSpec{}, getErr
		}
		return got, nil
	}
	out, err := m.deps.Store.PutSpec(ctx, spec, expectedRevision, operator, comment)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return ProcessSpec{}, err
	}
	if err := m.ensureInstances(ctx, out); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return ProcessSpec{}, err
	}
	action := "process.update"
	if expectedRevision == 0 {
		action = "process.create"
	}
	m.audit(ctx, out.ProcessID, action, opID, operator, "SUCCESS")
	payload, _ := json.Marshal(out)
	_ = m.finishOp(ctx, opID, opSuccess, payload, "")
	return out, nil
}

func (m *Manager) DeleteSpec(ctx context.Context, processID string, expectedRevision int64, opID, operator string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	done, existing, err := m.beginOp(ctx, opID, operator, "delete_spec", processID, nil)
	if err != nil {
		return err
	}
	if done {
		if existing.Status == opFailed {
			return errcode.E(errcode.INVALID, existing.Error)
		}
		return nil
	}
	boot, _ := m.deps.Store.GetBootID(ctx)
	insts, err := m.deps.Store.ListInstances(ctx, processID)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	for _, inst := range insts {
		if inst.Desired != DesiredStopped {
			err := errcode.E(errcode.INVALID, "desired must be STOPPED")
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
		switch inst.Observed {
		case ObservedStopped, ObservedFatal, ObservedUnknown:
		default:
			err := errcode.E(errcode.INVALID, "instance not terminal")
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
		if sameBoot(inst.BootID, boot) && pidAlive(inst.PID) {
			err := errcode.E(errcode.INVALID, "live pid")
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
	}
	if err := m.deps.Store.DeleteSpec(ctx, processID, expectedRevision); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	m.audit(ctx, processID, "process.delete", opID, operator, "SUCCESS")
	return m.finishOp(ctx, opID, opSuccess, nil, "")
}

func (m *Manager) SetDesired(ctx context.Context, processID string, desired DesiredState, opID, operator string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	done, existing, err := m.beginOp(ctx, opID, operator, "set_desired", processID, []byte(desired))
	if err != nil {
		return err
	}
	if done {
		if existing.Status == opFailed {
			return errcode.E(errcode.INVALID, existing.Error)
		}
		return nil
	}
	if _, err := m.deps.Store.GetSpec(ctx, processID); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	insts, err := m.deps.Store.ListInstances(ctx, processID)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	for _, inst := range insts {
		inst.Desired = desired
		if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
	}
	action := "process.stop"
	if desired == DesiredRunning {
		action = "process.start"
	}
	m.audit(ctx, processID, action, opID, operator, "SUCCESS")
	return m.finishOp(ctx, opID, opSuccess, nil, "")
}

// GetSpec returns the stored spec.
func (m *Manager) GetSpec(ctx context.Context, processID string) (ProcessSpec, error) {
	return m.deps.Store.GetSpec(ctx, processID)
}

// ListSpecs returns all specs.
func (m *Manager) ListSpecs(ctx context.Context) ([]ProcessSpec, error) {
	return m.deps.Store.ListSpecs(ctx)
}

// ListInstances returns instances for processID.
func (m *Manager) ListInstances(ctx context.Context, processID string) ([]Instance, error) {
	return m.deps.Store.ListInstances(ctx, processID)
}

// GetInstance returns one instance row.
func (m *Manager) GetInstance(ctx context.Context, instanceID string) (Instance, error) {
	return m.deps.Store.GetInstance(ctx, instanceID)
}

// PeekOp returns a journal row for HTTP idempotent replay.
func (m *Manager) PeekOp(ctx context.Context, operationID string) (status string, result []byte, errMsg string, err error) {
	return m.deps.Store.GetOp(ctx, operationID)
}

// Layout returns the manager data layout (for log paths).
func (m *Manager) Layout() paths.Layout {
	return m.deps.Layout
}

// Restart stops then sets desired RUNNING under a single operation_id.
func (m *Manager) Restart(ctx context.Context, processID, opID, operator string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	done, existing, err := m.beginOp(ctx, opID, operator, "restart", processID, nil)
	if err != nil {
		return err
	}
	if done {
		if existing.Status == opFailed {
			return errcode.E(errcode.INVALID, existing.Error)
		}
		return nil
	}
	if _, err := m.deps.Store.GetSpec(ctx, processID); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	insts, err := m.deps.Store.ListInstances(ctx, processID)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	for _, inst := range insts {
		inst.Desired = DesiredStopped
		if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
	}
	if err := m.reconcileLocked(ctx); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	insts, err = m.deps.Store.ListInstances(ctx, processID)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	for _, inst := range insts {
		inst.Desired = DesiredRunning
		if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
	}
	m.audit(ctx, processID, "process.restart", opID, operator, "SUCCESS")
	return m.finishOp(ctx, opID, opSuccess, nil, "")
}

func (m *Manager) ResetFailure(ctx context.Context, processID, opID, operator string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	done, existing, err := m.beginOp(ctx, opID, operator, "reset_failure", processID, nil)
	if err != nil {
		return err
	}
	if done {
		if existing.Status == opFailed {
			return errcode.E(errcode.INVALID, existing.Error)
		}
		return nil
	}
	insts, err := m.deps.Store.ListInstances(ctx, processID)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	for _, inst := range insts {
		delete(m.failures, inst.InstanceID)
		delete(m.nextTry, inst.InstanceID)
		m.resetHealth(inst.InstanceID)
		if inst.Observed == ObservedFatal {
			inst.Observed = ObservedStopped
			inst.RestartCount = 0
			if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
				_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
				return err
			}
		}
	}
	m.audit(ctx, processID, "process.reset_failure", opID, operator, "SUCCESS")
	return m.finishOp(ctx, opID, opSuccess, nil, "")
}

func (m *Manager) Adopt(ctx context.Context, instanceID string, pid int, opID, operator string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	done, existing, err := m.beginOp(ctx, opID, operator, "adopt", instanceID, nil)
	if err != nil {
		return err
	}
	if done {
		if existing.Status == opFailed {
			return errcode.E(errcode.INVALID, existing.Error)
		}
		return nil
	}
	if !pidAlive(pid) {
		err := errcode.E(errcode.NOT_FOUND, "pid")
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	inst, err := m.deps.Store.GetInstance(ctx, instanceID)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	boot, _ := m.deps.Store.GetBootID(ctx)
	inst.PID = pid
	inst.Observed = ObservedRunning
	inst.Health = HealthUnknown
	inst.BootID = boot
	// So InitialDelay applies on the next health check pass.
	if inst.StartedAt == nil {
		now := m.now()
		inst.StartedAt = &now
	}
	if err := writeRuntime(m.deps.Layout, inst); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	m.audit(ctx, inst.ProcessID, "process.adopt", opID, operator, "SUCCESS")
	return m.finishOp(ctx, opID, opSuccess, nil, "")
}

func (m *Manager) ensureInstances(ctx context.Context, spec ProcessSpec) error {
	for i := 0; i < spec.Instances; i++ {
		id := MakeInstanceID(spec.ProcessID, i)
		if _, err := m.deps.Store.GetInstance(ctx, id); err == nil {
			continue
		} else if !errcode.Is(err, errcode.NOT_FOUND) {
			return err
		}
		inst := Instance{
			InstanceID: id,
			ProcessID:  spec.ProcessID,
			Ordinal:    i,
			Desired:    DesiredStopped,
			Observed:   ObservedStopped,
			Health:     HealthUnknown,
		}
		if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
			return err
		}
	}
	insts, err := m.deps.Store.ListInstances(ctx, spec.ProcessID)
	if err != nil {
		return err
	}
	for _, inst := range insts {
		if inst.Ordinal >= spec.Instances && inst.Desired != DesiredStopped {
			inst.Desired = DesiredStopped
			if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) beginOp(ctx context.Context, opID, operator, typ, target string, payload []byte) (bool, opResult, error) {
	if opID == "" {
		return false, opResult{}, errcode.E(errcode.INVALID, "operation_id required")
	}
	dup, status, errMsg, err := m.deps.Store.StartOp(ctx, opID, operator, typ, target, payload)
	if err != nil {
		return false, opResult{}, err
	}
	res := opResult{Status: status, Error: errMsg}
	if !dup {
		return false, res, nil
	}
	switch status {
	case opSuccess, opFailed:
		return true, res, nil
	default:
		return false, res, nil
	}
}

func (m *Manager) finishOp(ctx context.Context, opID, status string, result []byte, errMsg string) error {
	return m.deps.Store.FinishOperation(ctx, opID, status, result, errMsg)
}

func (m *Manager) audit(ctx context.Context, resource, action, opID, operator, result string) {
	_ = m.deps.Store.WriteAudit(ctx, resource, action, opID, operator, result)
}

type opResult struct {
	Status string
	Error  string
}

func (m *Manager) closeAll() {
	for id := range m.clients {
		m.closeClient(id)
	}
}

func (m *Manager) now() time.Time {
	return m.deps.Now()
}

func sameBoot(instBoot, current string) bool {
	return instBoot != "" && instBoot == current
}

func applyDefaults(spec *ProcessSpec) {
	if spec.StopSignal == "" {
		spec.StopSignal = "SIGTERM"
	}
	if spec.KillSignal == "" {
		spec.KillSignal = "SIGKILL"
	}
	if spec.StopTimeout == 0 {
		spec.StopTimeout = 10 * time.Second
	}
	if spec.Restart.Mode == "" {
		spec.Restart.Mode = RestartOnFailure
	}
	spec.Log = spec.Log.WithDefaults()
}

func socketExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
