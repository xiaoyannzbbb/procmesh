package process

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/health"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/shim"
	shimpb "github.com/qleelulu/procmesh/proto/shim/v1"
)

const adoptRequiredMsg = "adopt required"

func (m *Manager) Reconcile(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconcileLocked(ctx)
}

func (m *Manager) reconcileLocked(ctx context.Context) error {
	specs, err := m.deps.Store.ListSpecs(ctx)
	if err != nil {
		return err
	}
	order, err := StartupOrder(specs)
	if err != nil {
		return err
	}
	byName, err := m.instancesByName(ctx, specs)
	if err != nil {
		return err
	}
	for _, pid := range order {
		spec, ok := m.getSpecByID(specs, pid)
		if !ok {
			continue
		}
		insts, err := m.deps.Store.ListInstances(ctx, pid)
		if err != nil {
			return err
		}
		for _, inst := range insts {
			if inst.Ordinal >= spec.Instances {
				if err := m.stopInstance(ctx, spec, &inst); err != nil {
					// record and continue; do not abort the StartupOrder pass
					continue
				}
				if extraInstanceDeletable(inst) {
					if err := m.deps.Store.DeleteInstance(ctx, inst.InstanceID); err != nil {
						continue
					}
					m.forgetInstance(inst.InstanceID)
				}
				continue
			}
			if err := m.reconcileInstance(ctx, spec, &inst, byName); err != nil {
				// one startInstance failure must not abort later processes
				continue
			}
		}
	}
	m.closeAll()
	return nil
}

func isAdoptRequired(err error) bool {
	return err != nil && errcode.Is(err, errcode.INVALID) && strings.Contains(err.Error(), adoptRequiredMsg)
}

func (m *Manager) getSpecByID(specs []ProcessSpec, pid string) (ProcessSpec, bool) {
	for i := range specs {
		if specs[i].ProcessID == pid {
			return specs[i], true
		}
	}
	return ProcessSpec{}, false
}

// instancesByName snapshots current instance rows keyed by spec Name.
func (m *Manager) instancesByName(ctx context.Context, specs []ProcessSpec) (map[string][]Instance, error) {
	byName := make(map[string][]Instance, len(specs))
	for _, spec := range specs {
		insts, err := m.deps.Store.ListInstances(ctx, spec.ProcessID)
		if err != nil {
			return nil, err
		}
		byName[spec.Name] = insts
	}
	return byName, nil
}

func (m *Manager) reconcileInstance(ctx context.Context, spec ProcessSpec, inst *Instance, byName map[string][]Instance) error {
	if err := m.refresh(ctx, inst); err != nil {
		return err
	}

	// FATAL stays FATAL until ResetFailure. UNKNOWN stays UNKNOWN until Adopt
	// (or a later reconnect finds a live shim). Never auto-start either.
	// Stopping an UNKNOWN live pid must fail with adopt required — do not fake STOPPED.
	if inst.Observed == ObservedUnknown {
		if inst.Desired == DesiredStopped {
			if err := m.stopInstance(ctx, spec, inst); isAdoptRequired(err) {
				// Leave UNKNOWN; do not fail the whole Reconcile pass.
				return nil
			} else if err != nil {
				return err
			}
		}
		return nil
	}
	if inst.Observed == ObservedFatal {
		return nil
	}

	if inst.Desired == DesiredStopped {
		return m.stopInstance(ctx, spec, inst)
	}

	if inst.Desired == DesiredRunning {
		if inst.Observed == ObservedRunning {
			return m.applyHealth(ctx, spec, inst)
		}
		if inst.Observed == ObservedStarting {
			return nil
		}

		if inst.Observed == ObservedExited || inst.Observed == ObservedBackoff {
			// Failures are recorded only on real EvExit / EvStartFail / health-restart,
			// never on a skipped backoff tick.
			now := m.now()
			dec := DecideRestart(spec.Restart, inst.Desired, inst.Observed, exitCode(inst), m.failures[inst.InstanceID], now)
			if dec.Fatal {
				delete(m.nextTry, inst.InstanceID)
				if next, err := ApplyObserved(inst.Observed, EvRetriesExhausted); err == nil {
					inst.Observed = next
				} else {
					inst.Observed = ObservedFatal
				}
				return m.deps.Store.PutInstance(ctx, *inst)
			}
			if dec.Restart {
				// Set nextTry when deciding to restart; start only when now >= nextTry.
				if t, ok := m.nextTry[inst.InstanceID]; !ok || t.IsZero() {
					m.nextTry[inst.InstanceID] = now.Add(dec.Delay)
				}
				if now.Before(m.nextTry[inst.InstanceID]) {
					return nil
				}
				if !DepsReady(spec, byName) {
					return nil
				}
				delete(m.nextTry, inst.InstanceID)
				if next, err := ApplyObserved(inst.Observed, EvRetry); err == nil {
					inst.Observed = next
				} else {
					inst.Observed = ObservedStarting
				}
				return m.startInstance(ctx, spec, inst, m.bootID(ctx))
			}
			// DecideRestart said neither restart nor fatal (never / clean on-failure).
		}

		// From STOPPED (e.g. after ResetFailure) start once. Do not start
		// UNKNOWN/FATAL (already returned) or EXITED without a restart decision.
		if inst.Observed == ObservedStopped {
			if !DepsReady(spec, byName) {
				return nil
			}
			return m.startInstance(ctx, spec, inst, m.bootID(ctx))
		}
	}

	if inst.Observed != ObservedStopped {
		inst.Observed = ObservedStopped
		return m.deps.Store.PutInstance(ctx, *inst)
	}
	return nil
}

func (m *Manager) refresh(ctx context.Context, inst *Instance) error {
	m.closeClient(inst.InstanceID)
	sock := m.deps.Layout.ShimSocket(inst.InstanceID)
	if !socketExists(sock) {
		return m.refreshNoSocket(ctx, inst)
	}
	client, st, err := shim.Reconnect(ctx, sock)
	if err != nil {
		if pidAlive(inst.PID) && sameBoot(inst.BootID, m.bootID(ctx)) {
			if err := m.markOrphan(ctx, *inst, inst.PID); err != nil {
				return err
			}
			inst.Observed = ObservedUnknown
			return nil
		}
		return nil
	}
	defer m.closeConn(client, inst.InstanceID)
	if st != nil && st.GetAlive() {
		if !sameBoot(inst.BootID, m.bootID(ctx)) {
			// Previous-boot leftover: do not adopt the old PID as RUNNING.
			return m.refreshNoSocket(ctx, inst)
		}
		inst.PID = int(st.GetPid())
		if inst.Observed != ObservedRunning {
			if next, err := ApplyObserved(inst.Observed, EvStartOK); err == nil {
				inst.Observed = next
			} else {
				inst.Observed = ObservedRunning
			}
		}
		return m.deps.Store.PutInstance(ctx, *inst)
	}
	m.handleExit(ctx, inst, st)
	return nil
}

// refreshNoSocket handles a missing shim socket without launching a second copy
// when the managed PID is still live on the same boot.
func (m *Manager) refreshNoSocket(ctx context.Context, inst *Instance) error {
	boot := m.bootID(ctx)
	livePID := 0
	if sameBoot(inst.BootID, boot) && pidAlive(inst.PID) {
		livePID = inst.PID
	}
	if livePID == 0 && sameBoot(inst.BootID, boot) {
		if snap, err := readRuntime(m.deps.Layout, inst.InstanceID); err == nil && pidAlive(snap.PID) {
			livePID = snap.PID
			inst.PID = snap.PID
			if snap.ShimPID > 0 {
				inst.ShimPID = snap.ShimPID
			}
		}
	}
	if livePID > 0 {
		if err := m.markOrphan(ctx, *inst, livePID); err != nil {
			return err
		}
		inst.PID = livePID
		inst.Observed = ObservedUnknown
		return nil
	}

	// Boot mismatch or dead child: never treat the stored PID as live.
	cleared := false
	if inst.PID != 0 {
		inst.PID = 0
		cleared = true
	}
	switch inst.Observed {
	case ObservedRunning, ObservedStarting, ObservedStopping:
		m.handleExit(ctx, inst, nil)
		return nil
	}
	if cleared {
		return m.deps.Store.PutInstance(ctx, *inst)
	}
	return nil
}

func (m *Manager) handleExit(ctx context.Context, inst *Instance, st *shimpb.StatusResponse) {
	// Count only the transition into EXITED, not repeated refresh of a dead child.
	shouldCount := inst.Observed == ObservedRunning || inst.Observed == ObservedStarting || inst.Observed == ObservedStopping
	now := m.now()
	inst.ExitAt = &now
	inst.PID = 0
	if st != nil {
		if ec := st.GetExitCode(); ec != 0 {
			code := int(ec)
			inst.ExitCode = &code
		}
	}
	if next, err := ApplyObserved(inst.Observed, EvExit); err == nil {
		inst.Observed = next
	} else {
		inst.Observed = ObservedExited
	}
	inst.Health = HealthUnknown
	if shouldCount {
		m.recordFailure(inst.InstanceID)
		// Clear any prior nextTry so the next DecideRestart arms a fresh Delay.
		delete(m.nextTry, inst.InstanceID)
	}
	_ = m.deps.Store.PutInstance(ctx, *inst)
}

func (m *Manager) startInstance(ctx context.Context, spec ProcessSpec, inst *Instance, boot string) error {
	if spec.RunAsUser != "" {
		if m.deps.LookUser == nil {
			return errcode.E(errcode.INVALID, "run_as_user")
		}
		if err := m.deps.LookUser(spec.RunAsUser); err != nil {
			return err
		}
	}

	m.closeClient(inst.InstanceID)
	sock := m.deps.Layout.ShimSocket(inst.InstanceID)
	var client *shim.Client
	var st *shimpb.StatusResponse
	if socketExists(sock) {
		c, status, err := shim.Reconnect(ctx, sock)
		if err == nil {
			client, st = c, status
			if status != nil && status.GetAlive() {
				if sameBoot(inst.BootID, boot) || inst.BootID == "" {
					defer m.closeConn(c, inst.InstanceID)
					return m.applyRunning(ctx, inst, int(status.GetPid()), boot)
				}
				// Previous-boot leftover: do not adopt the old PID.
				if c != nil {
					_ = c.Close()
				}
				return nil
			}
		}
	}
	if client == nil {
		bin := m.deps.ShimBin
		if bin == "" {
			var err error
			bin, err = shim.LookPath()
			if err != nil {
				return err
			}
		}
		shimPID, err := shim.Launch(ctx, bin, sock, inst.InstanceID)
		if err != nil {
			return fmt.Errorf("launch shim: %w", err)
		}
		inst.ShimPID = shimPID
		c, status, err := shim.Reconnect(ctx, sock)
		if err != nil {
			return err
		}
		client, st = c, status
	}
	defer m.closeConn(client, inst.InstanceID)

	if st != nil && st.GetAlive() {
		return m.applyRunning(ctx, inst, int(st.GetPid()), boot)
	}

	if next, err := ApplyObserved(inst.Observed, EvStart); err == nil {
		inst.Observed = next
	} else if inst.Observed != ObservedStarting {
		inst.Observed = ObservedStarting
	}

	stdout, stderr := logmgr.InstancePaths(m.deps.Layout, spec.ProcessID, inst.InstanceID)
	if m.deps.Logs != nil && !m.deps.Logs.WritesAllowed() {
		stdout, stderr = os.DevNull, os.DevNull
		m.audit(ctx, inst.ProcessID, "LOG_WRITES_DISABLED", "", "", "")
	} else if err := logmgr.Prepare(stdout, stderr); err != nil {
		return err
	}

	resp, err := client.Start(ctx, &shimpb.StartRequest{
		Command:    spec.Command,
		Args:       spec.Args,
		Env:        spec.Environment,
		Cwd:        spec.WorkingDirectory,
		RunAsUser:  spec.RunAsUser,
		StdoutPath: stdout,
		StderrPath: stderr,
	})
	if err != nil || (resp != nil && resp.GetError() != "") {
		msg := "start failed"
		if err != nil {
			msg = err.Error()
		} else {
			msg = resp.GetError()
		}
		if next, aerr := ApplyObserved(inst.Observed, EvStartFail); aerr == nil {
			inst.Observed = next
		} else {
			inst.Observed = ObservedBackoff
		}
		m.recordFailure(inst.InstanceID)
		_ = m.deps.Store.PutInstance(ctx, *inst)
		return fmt.Errorf("start: %s", msg)
	}
	now := m.now()
	inst.PID = int(resp.GetPid())
	inst.StartedAt = &now
	inst.ExitAt = nil
	inst.ExitCode = nil
	inst.ActiveRevision = spec.LatestRevision
	inst.BootID = boot
	inst.Health = HealthUnknown
	if next, aerr := ApplyObserved(inst.Observed, EvStartOK); aerr == nil {
		inst.Observed = next
	} else {
		inst.Observed = ObservedRunning
	}
	if ResourceLimitSet(spec.Resources) && (runtime.GOOS != "linux" || ApplyResourceLimit(inst.PID, spec.Resources) != nil) {
		m.audit(ctx, inst.ProcessID, "RESOURCE_LIMIT_UNSUPPORTED", "", "", "")
	}
	if err := writeRuntime(m.deps.Layout, *inst); err != nil {
		return err
	}
	return m.deps.Store.PutInstance(ctx, *inst)
}

func (m *Manager) applyRunning(ctx context.Context, inst *Instance, pid int, boot string) error {
	inst.PID = pid
	inst.Health = HealthUnknown
	inst.BootID = boot
	if inst.Observed != ObservedRunning {
		if next, err := ApplyObserved(inst.Observed, EvStartOK); err == nil {
			inst.Observed = next
		} else {
			inst.Observed = ObservedRunning
		}
	}
	if err := writeRuntime(m.deps.Layout, *inst); err != nil {
		return err
	}
	return m.deps.Store.PutInstance(ctx, *inst)
}

func (m *Manager) stopInstance(ctx context.Context, spec ProcessSpec, inst *Instance) error {
	if inst.Observed == ObservedUnknown && pidAlive(inst.PID) && sameBoot(inst.BootID, m.bootID(ctx)) {
		return errcode.E(errcode.INVALID, adoptRequiredMsg)
	}
	m.closeClient(inst.InstanceID)
	sock := m.deps.Layout.ShimSocket(inst.InstanceID)
	if !socketExists(sock) {
		if !pidAlive(inst.PID) || !sameBoot(inst.BootID, m.bootID(ctx)) {
			inst.PID = 0
			inst.Observed = ObservedStopped
			return m.deps.Store.PutInstance(ctx, *inst)
		}
		return nil
	}
	client, st, err := shim.Reconnect(ctx, sock)
	if err != nil {
		inst.PID = 0
		inst.Observed = ObservedStopped
		return m.deps.Store.PutInstance(ctx, *inst)
	}
	defer m.closeConn(client, inst.InstanceID)
	if st == nil || !st.GetAlive() {
		inst.PID = 0
		inst.Observed = ObservedStopped
		return m.deps.Store.PutInstance(ctx, *inst)
	}

	if next, err := ApplyObserved(inst.Observed, EvStop); err == nil {
		inst.Observed = next
		_ = m.deps.Store.PutInstance(ctx, *inst)
	}

	timeout := spec.StopTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	resp, err := client.Stop(ctx, &shimpb.StopRequest{
		Signal:     spec.StopSignal,
		TimeoutMs:  int32(timeout / time.Millisecond),
		KillSignal: spec.KillSignal,
	})
	if err != nil {
		return err
	}
	inst.PID = 0
	now := m.now()
	inst.ExitAt = &now
	if resp != nil {
		code := int(resp.GetExitCode())
		inst.ExitCode = &code
	}
	if next, err := ApplyObserved(inst.Observed, EvStopped); err == nil {
		inst.Observed = next
	} else {
		inst.Observed = ObservedStopped
	}
	inst.Health = HealthUnknown
	return m.deps.Store.PutInstance(ctx, *inst)
}

func (m *Manager) closeClient(id string) {
	if c := m.clients[id]; c != nil {
		_ = c.Close()
		delete(m.clients, id)
	}
}

func (m *Manager) closeConn(c *shim.Client, id string) {
	if c != nil {
		_ = c.Close()
	}
	delete(m.clients, id)
}

func (m *Manager) bootID(ctx context.Context) string {
	id, _ := m.deps.Store.GetBootID(ctx)
	return id
}

func (m *Manager) recordFailure(id string) {
	m.failures[id] = append(m.failures[id], m.now())
}

func (m *Manager) resetHealth(id string) {
	delete(m.healthTrackers, id)
	delete(m.lastHealthCheck, id)
	delete(m.lastHealthRestart, id)
}

func (m *Manager) forgetInstance(id string) {
	m.closeClient(id)
	delete(m.failures, id)
	delete(m.nextTry, id)
	m.resetHealth(id)
}

func (m *Manager) resetAllHealth() {
	m.healthTrackers = make(map[string]*health.Tracker)
	m.lastHealthCheck = make(map[string]time.Time)
	m.lastHealthRestart = make(map[string]time.Time)
}

func (m *Manager) tracker(id string, spec HealthCheckSpec) *health.Tracker {
	if tr := m.healthTrackers[id]; tr != nil && tr.SameThresholds(spec) {
		return tr
	}
	tr := health.NewTracker(spec)
	m.healthTrackers[id] = tr
	return tr
}

func (m *Manager) applyHealth(ctx context.Context, spec ProcessSpec, inst *Instance) error {
	now := m.now()
	if inst.StartedAt != nil && now.Before(inst.StartedAt.Add(spec.Health.InitialDelay)) {
		return nil
	}
	if last, ok := m.lastHealthCheck[inst.InstanceID]; ok && spec.Health.Interval > 0 && now.Before(last.Add(spec.Health.Interval)) {
		return nil
	}

	// Snapshot and run Check without holding m.mu (HTTP/TCP probes can block).
	hspec := spec.Health
	pid := inst.PID
	id := inst.InstanceID
	var checkErr error
	func() {
		m.mu.Unlock()
		defer m.mu.Lock()
		checkErr = health.Check(ctx, hspec, pid)
	}()

	// Re-validate after re-lock: another pass may have stopped the instance,
	// changed PID, or already recorded a probe in this Interval.
	fresh, err := m.deps.Store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	*inst = fresh
	if inst.Desired != DesiredRunning || inst.Observed != ObservedRunning || inst.PID != pid {
		return nil
	}
	if last, ok := m.lastHealthCheck[inst.InstanceID]; ok && spec.Health.Interval > 0 && now.Before(last.Add(spec.Health.Interval)) {
		return nil
	}

	state := m.tracker(inst.InstanceID, spec.Health).Observe(checkErr, now)
	m.lastHealthCheck[inst.InstanceID] = now

	if inst.Health != state {
		inst.Health = state
		if putErr := m.deps.Store.PutInstance(ctx, *inst); putErr != nil {
			return putErr
		}
	}

	if state != HealthUnhealthy || !spec.Health.RestartOnFailure {
		return nil
	}
	if prev, ok := m.lastHealthRestart[inst.InstanceID]; ok && spec.Health.RestartCooldown > 0 && now.Before(prev.Add(spec.Health.RestartCooldown)) {
		return nil
	}

	desired := inst.Desired
	m.failures[inst.InstanceID] = append(m.failures[inst.InstanceID], now)
	if spec.Restart.MaxRetries > 0 && countFailuresInWindow(m.failures[inst.InstanceID], now, spec.Restart.RetryWindow) >= spec.Restart.MaxRetries {
		if err := m.stopInstance(ctx, spec, inst); err != nil {
			return err
		}
		inst.Desired = desired
		inst.Observed = ObservedFatal
		inst.Health = HealthUnhealthy
		return m.deps.Store.PutInstance(ctx, *inst)
	}

	inst.RestartCount++
	m.lastHealthRestart[inst.InstanceID] = now
	delete(m.healthTrackers, inst.InstanceID)
	delete(m.lastHealthCheck, inst.InstanceID)
	if err := m.stopInstance(ctx, spec, inst); err != nil {
		return err
	}
	inst.Desired = desired
	return m.startInstance(ctx, spec, inst, m.bootID(ctx))
}

func exitCode(inst *Instance) int {
	if inst != nil && inst.ExitCode != nil {
		return *inst.ExitCode
	}
	return 0
}
