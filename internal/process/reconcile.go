package process

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/health"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/shim"
	shimpb "github.com/qleelulu/procmesh/proto/shim/v1"
)

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
				continue
			}
			if err := m.reconcileInstance(ctx, spec, &inst); err != nil {
				// one startInstance failure must not abort later processes
				continue
			}
		}
	}
	m.closeAll()
	return nil
}

func (m *Manager) getSpecByID(specs []ProcessSpec, pid string) (ProcessSpec, bool) {
	for i := range specs {
		if specs[i].ProcessID == pid {
			return specs[i], true
		}
	}
	return ProcessSpec{}, false
}

func (m *Manager) reconcileInstance(ctx context.Context, spec ProcessSpec, inst *Instance) error {
	if err := m.refresh(ctx, inst); err != nil {
		return err
	}

	// FATAL stays FATAL until ResetFailure. UNKNOWN stays UNKNOWN until Adopt
	// (or a later reconnect finds a live shim). Never auto-start either.
	if inst.Observed == ObservedUnknown || inst.Observed == ObservedFatal {
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
			m.recordFailure(inst.InstanceID)
			dec := DecideRestart(spec.Restart, inst.Desired, inst.Observed, exitCode(inst), m.failures[inst.InstanceID], m.now())
			if dec.Fatal {
				if next, err := ApplyObserved(inst.Observed, EvRetriesExhausted); err == nil {
					inst.Observed = next
				} else {
					inst.Observed = ObservedFatal
				}
				return m.deps.Store.PutInstance(ctx, *inst)
			}
			if dec.Restart {
				// Honor backoff Delay via nextTry; skip start until due.
				if t, ok := m.nextTry[inst.InstanceID]; ok && !t.IsZero() && m.now().Before(t) {
					return nil
				}
				if next, err := ApplyObserved(inst.Observed, EvRetry); err == nil {
					inst.Observed = next
				} else {
					inst.Observed = ObservedStarting
				}
				m.nextTry[inst.InstanceID] = m.now().Add(dec.Delay)
				return m.startInstance(ctx, spec, inst, m.bootID(ctx))
			}
			// DecideRestart said neither restart nor fatal (never / clean on-failure).
		}

		// From STOPPED (e.g. after ResetFailure) start once. Do not start
		// UNKNOWN/FATAL (already returned) or EXITED without a restart decision.
		if inst.Observed == ObservedStopped {
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
		return nil
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

func (m *Manager) handleExit(ctx context.Context, inst *Instance, st *shimpb.StatusResponse) {
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
				defer m.closeConn(c, inst.InstanceID)
				return m.applyRunning(ctx, inst, int(status.GetPid()), boot)
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
		return nil
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

func (m *Manager) resetAllHealth() {
	m.healthTrackers = make(map[string]*health.Tracker)
	m.lastHealthCheck = make(map[string]time.Time)
	m.lastHealthRestart = make(map[string]time.Time)
}

func (m *Manager) tracker(id string, spec HealthCheckSpec) *health.Tracker {
	if tr := m.healthTrackers[id]; tr != nil {
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

	err := health.Check(ctx, spec.Health, inst.PID)
	state := m.tracker(inst.InstanceID, spec.Health).Observe(err, now)
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
