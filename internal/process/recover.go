package process

import (
	"context"
	"errors"
	"os"
	"time"

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
	m.lastShimCheck = make(map[string]time.Time)
	boot, err := m.deps.Store.GetBootID(ctx)
	if err != nil {
		return err
	}
	specs, err := m.deps.Store.ListSpecs(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{})
	for i := range specs {
		if err := m.ensureInstances(ctx, specs[i]); err != nil {
			return err
		}
		insts, err := m.deps.Store.ListInstances(ctx, specs[i].ProcessID)
		if err != nil {
			return err
		}
		for _, inst := range insts {
			known[sanitizedID(inst.InstanceID)] = struct{}{}
			if err := m.recoverInstance(ctx, specs[i], inst, boot); err != nil {
				return err
			}
		}
	}
	if err := m.recoverLeftoverSockets(ctx, known); err != nil {
		return err
	}
	m.closeAll()
	return nil
}

func (m *Manager) recoverLeftoverSockets(ctx context.Context, known map[string]struct{}) error {
	found, err := shim.Discover(m.deps.Layout.ShimDir)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for id := range found {
		if _, ok := known[id]; ok {
			continue
		}
		// Unknown shims are not owned by this manager. Leave them untouched.
	}
	return nil
}

func (m *Manager) recoverInstance(ctx context.Context, spec ProcessSpec, inst Instance, boot string) error {
	sock := m.deps.Layout.ShimSocket(inst.InstanceID)
	if socketExists(sock) {
		client, st, err := shim.Reconnect(ctx, sock)
		if err == nil && st != nil && st.GetAlive() {
			if sameBoot(inst.BootID, boot) {
				return m.takeOver(ctx, inst, client, st, boot)
			}
			if client != nil {
				_ = client.Close()
			}
		} else if client != nil {
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

	// Recover never launches. Reconcile owns start so StartupOrder and DepsReady apply.
	inst.PID = 0
	inst.ShimPID = 0
	if hostRebooted(inst.BootID, boot) && !spec.Autostart {
		inst.Desired = DesiredStopped
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
