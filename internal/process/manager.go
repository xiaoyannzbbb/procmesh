package process

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
	GetSpecByName(ctx context.Context, name string) (ProcessSpec, error)
	ListSpecs(ctx context.Context) ([]ProcessSpec, error)
	DeleteSpec(ctx context.Context, processID string, expectedRevision int64) error
	ListRevisions(ctx context.Context, processID string) ([]Revision, error)
	GetRevisionSpec(ctx context.Context, processID string, rev int64) (ProcessSpec, error)
	RollbackSpec(ctx context.Context, processID string, toRevision, expectedLatest int64, operator, comment string) (ProcessSpec, error)
	PutInstance(ctx context.Context, inst Instance) error
	GetInstance(ctx context.Context, instanceID string) (Instance, error)
	ListInstances(ctx context.Context, processID string) ([]Instance, error)
	DeleteInstance(ctx context.Context, instanceID string) error
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
	ApplyDefaults(&spec)
	spec.Group = strings.TrimSpace(spec.Group)
	if err := ValidateSpec(spec); err != nil {
		return ProcessSpec{}, err
	}
	if err := logmgr.ValidateDirectory(spec.Log.Directory, m.deps.Layout.Root); err != nil {
		return ProcessSpec{}, err
	}
	done, existing, err := m.beginOp(ctx, opID, operator, "apply_spec", spec.ProcessID, nil)
	if err != nil {
		return ProcessSpec{}, err
	}
	if done {
		return m.replayApplySpec(ctx, spec.ProcessID, opID, existing)
	}
	if err := m.rejectDependencyCycle(ctx, spec); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return ProcessSpec{}, err
	}
	if expectedRevision == 0 && spec.ProcessID == "" {
		id, err := newProcessID()
		if err != nil {
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return ProcessSpec{}, err
		}
		spec.ProcessID = id
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
	for _, inst := range insts {
		if err := m.deps.Store.DeleteInstance(ctx, inst.InstanceID); err != nil && !errcode.Is(err, errcode.NOT_FOUND) {
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
		m.forgetInstance(inst.InstanceID)
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
	if desired == DesiredStopped {
		if err := m.rejectLiveOrphanStop(ctx, insts); err != nil {
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
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

// Resolve looks up a spec by process_id, then by unique name.
func (m *Manager) Resolve(ctx context.Context, idOrName string) (ProcessSpec, error) {
	if idOrName == "" {
		return ProcessSpec{}, errcode.E(errcode.INVALID, "id or name required")
	}
	spec, err := m.deps.Store.GetSpec(ctx, idOrName)
	if err == nil {
		return spec, nil
	}
	if !errcode.Is(err, errcode.NOT_FOUND) {
		return ProcessSpec{}, err
	}
	return m.deps.Store.GetSpecByName(ctx, idOrName)
}

// Kill immediately terminates instances, then sets desired STOPPED and reconciles.
func (m *Manager) Kill(ctx context.Context, processID, opID, operator string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	done, existing, err := m.beginOp(ctx, opID, operator, "kill", processID, nil)
	if err != nil {
		return err
	}
	if done {
		if existing.Status == opFailed {
			return errcode.E(errcode.INVALID, existing.Error)
		}
		return nil
	}
	spec, err := m.deps.Store.GetSpec(ctx, processID)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	insts, err := m.deps.Store.ListInstances(ctx, processID)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	if err := m.rejectLiveOrphanStop(ctx, insts); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return err
	}
	for i := range insts {
		if err := m.killInstance(ctx, spec, &insts[i]); err != nil {
			_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
			return err
		}
	}
	insts, err = m.deps.Store.ListInstances(ctx, processID)
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
	m.audit(ctx, processID, "process.kill", opID, operator, "SUCCESS")
	return m.finishOp(ctx, opID, opSuccess, nil, "")
}

// ListRevisions returns stored revision history (no journal).
func (m *Manager) ListRevisions(ctx context.Context, processID string) ([]Revision, error) {
	return m.deps.Store.ListRevisions(ctx, processID)
}

// Rollback copies toRevision into a new latest revision via the journal.
func (m *Manager) Rollback(ctx context.Context, processID string, toRevision, expectedLatest int64, opID, operator, comment string) (ProcessSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	done, existing, err := m.beginOp(ctx, opID, operator, "rollback", processID, nil)
	if err != nil {
		return ProcessSpec{}, err
	}
	if done {
		return m.replayApplySpec(ctx, processID, opID, existing)
	}
	out, err := m.deps.Store.RollbackSpec(ctx, processID, toRevision, expectedLatest, operator, comment)
	if err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return ProcessSpec{}, err
	}
	if err := m.ensureInstances(ctx, out); err != nil {
		_ = m.finishOp(ctx, opID, opFailed, nil, err.Error())
		return ProcessSpec{}, err
	}
	m.audit(ctx, processID, "process.rollback", opID, operator, "SUCCESS")
	payload, _ := json.Marshal(out)
	_ = m.finishOp(ctx, opID, opSuccess, payload, "")
	return out, nil
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

// RotateLogs applies each spec's Log policy to that process's stdout/stderr files.
// Age checks use logmgr.Manager.Now when set.
func (m *Manager) RotateLogs(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	specs, err := m.deps.Store.ListSpecs(ctx)
	if err != nil {
		return err
	}
	now := m.now()
	if m.deps.Logs != nil && m.deps.Logs.Now != nil {
		now = m.deps.Logs.Now()
	}
	var first error
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return err
		}
		insts, err := m.deps.Store.ListInstances(ctx, spec.ProcessID)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		for _, inst := range insts {
			pol := m.EffectiveLog(ctx, spec, inst)
			lp := pol.WithDefaults()
			rpol := logmgr.RotatePolicy{MaxSize: lp.MaxSize, MaxFiles: lp.MaxFiles, MaxAge: lp.MaxAge, Compress: lp.Compress}
			stdout, stderr := logmgr.Resolve(m.deps.Layout, pol.Directory, spec.ProcessID, inst.InstanceID, inst.Ordinal)
			if err := logmgr.Rotate(stdout, rpol, now); err != nil && first == nil {
				first = err
			}
			if stderr != stdout {
				if err := logmgr.Rotate(stderr, rpol, now); err != nil && first == nil {
					first = err
				}
			}
		}
	}
	return first
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
	if err := m.rejectLiveOrphanStop(ctx, insts); err != nil {
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

func (m *Manager) rejectDependencyCycle(ctx context.Context, spec ProcessSpec) error {
	specs, err := m.deps.Store.ListSpecs(ctx)
	if err != nil {
		return err
	}
	merged := false
	for i, s := range specs {
		if spec.ProcessID != "" && s.ProcessID == spec.ProcessID {
			specs[i] = spec
			merged = true
			break
		}
	}
	if !merged {
		specs = append(specs, spec)
	}
	return RejectCycle(specs)
}

func (m *Manager) ensureInstances(ctx context.Context, spec ProcessSpec) error {
	insts, err := m.deps.Store.ListInstances(ctx, spec.ProcessID)
	if err != nil {
		return err
	}
	desired := inheritProcessDesired(insts, spec.Instances)
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
			Desired:    desired,
			Observed:   ObservedStopped,
			Health:     HealthUnknown,
		}
		if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
			return err
		}
	}
	insts, err = m.deps.Store.ListInstances(ctx, spec.ProcessID)
	if err != nil {
		return err
	}
	for _, inst := range insts {
		if inst.Ordinal < spec.Instances {
			continue
		}
		if inst.Desired != DesiredStopped {
			inst.Desired = DesiredStopped
			if err := m.deps.Store.PutInstance(ctx, inst); err != nil {
				return err
			}
		}
		if extraInstanceDeletable(inst) {
			if err := m.deps.Store.DeleteInstance(ctx, inst.InstanceID); err != nil {
				return err
			}
			m.forgetInstance(inst.InstanceID)
		}
	}
	return nil
}

func (m *Manager) replayApplySpec(ctx context.Context, processID, opID string, existing opResult) (ProcessSpec, error) {
	if existing.Status == opFailed {
		return ProcessSpec{}, errcode.E(errcode.INVALID, existing.Error)
	}
	_, result, _, err := m.deps.Store.GetOp(ctx, opID)
	if err == nil && len(result) > 0 {
		var out ProcessSpec
		if json.Unmarshal(result, &out) == nil && out.ProcessID != "" {
			return out, nil
		}
	}
	if processID == "" {
		return ProcessSpec{}, errcode.E(errcode.NOT_FOUND, "process")
	}
	return m.deps.Store.GetSpec(ctx, processID)
}

func inheritProcessDesired(insts []Instance, instances int) DesiredState {
	for _, inst := range insts {
		if inst.Ordinal < instances && inst.Desired == DesiredRunning {
			return DesiredRunning
		}
	}
	return DesiredStopped
}

func extraInstanceDeletable(inst Instance) bool {
	switch inst.Observed {
	case ObservedStopped, ObservedFatal, ObservedUnknown:
	default:
		return false
	}
	return !pidAlive(inst.PID)
}

// newProcessID returns a UUID in the same format as store.newUUID:
// xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
func newProcessID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate process_id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
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

func hostRebooted(instBoot, current string) bool {
	return instBoot != "" && instBoot != current
}

func (m *Manager) rejectLiveOrphanStop(ctx context.Context, insts []Instance) error {
	boot, _ := m.deps.Store.GetBootID(ctx)
	for _, inst := range insts {
		if inst.Observed == ObservedUnknown && pidAlive(inst.PID) && sameBoot(inst.BootID, boot) {
			return errcode.E(errcode.INVALID, adoptRequiredMsg)
		}
	}
	return nil
}

func socketExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
