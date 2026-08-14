package process

import (
	"context"
	"os"

	"github.com/qleelulu/procmesh/internal/shim"
	shimpb "github.com/qleelulu/procmesh/proto/shim/v1"
)

func (m *Manager) Recover(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoverLocked(ctx)
}

func (m *Manager) recoverLocked(ctx context.Context) error {
	m.resetAllHealth()
	boot, err := m.deps.Store.GetBootID(ctx)
	if err != nil {
		return err
	}
	specs, err := m.deps.Store.ListSpecs(ctx)
	if err != nil {
		return err
	}
	if _, err := shim.Discover(m.deps.Layout.ShimDir); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := range specs {
		if err := m.ensureInstances(ctx, specs[i]); err != nil {
			return err
		}
		insts, err := m.deps.Store.ListInstances(ctx, specs[i].ProcessID)
		if err != nil {
			return err
		}
		for _, inst := range insts {
			if err := m.recoverInstance(ctx, specs[i], inst, boot); err != nil {
				return err
			}
		}
	}
	m.closeAll()
	return nil
}

func (m *Manager) recoverInstance(ctx context.Context, spec ProcessSpec, inst Instance, boot string) error {
	sock := m.deps.Layout.ShimSocket(inst.InstanceID)
	if socketExists(sock) {
		client, st, err := shim.Reconnect(ctx, sock)
		if err == nil && st != nil && st.GetAlive() {
			return m.takeOver(ctx, inst, client, st, boot)
		}
		if client != nil {
			_ = client.Close()
		}
	}

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
		return m.markOrphan(ctx, inst, livePID)
	}

	inst.PID = 0
	inst.ShimPID = 0
	hostReboot := !sameBoot(inst.BootID, boot)
	if inst.Desired == DesiredRunning && (!hostReboot || spec.Autostart) {
		return m.startInstance(ctx, spec, &inst, boot)
	}
	if inst.Observed != ObservedStopped && inst.Observed != ObservedFatal {
		inst.Observed = ObservedStopped
	}
	inst.Health = HealthUnknown
	return m.deps.Store.PutInstance(ctx, inst)
}

func (m *Manager) takeOver(ctx context.Context, inst Instance, client *shim.Client, st *shimpb.StatusResponse, boot string) error {
	if client != nil {
		_ = client.Close()
	}
	m.closeClient(inst.InstanceID)
	return m.applyRunning(ctx, &inst, int(st.GetPid()), boot)
}

func (m *Manager) markOrphan(ctx context.Context, inst Instance, pid int) error {
	inst.PID = pid
	if next, err := ApplyObserved(inst.Observed, EvLost); err == nil {
		inst.Observed = next
	} else {
		inst.Observed = ObservedUnknown
	}
	inst.Health = HealthUnknown
	m.audit(ctx, inst.ProcessID, "ORPHAN_PROCESS", "", "", "SUCCESS")
	_ = writeRuntime(m.deps.Layout, inst)
	return m.deps.Store.PutInstance(ctx, inst)
}
