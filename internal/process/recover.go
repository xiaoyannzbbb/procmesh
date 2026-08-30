package process

import (
	"context"
	"errors"
	"fmt"
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
	m.closeAll()
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
	for id, sock := range found {
		if _, ok := known[id]; ok {
			continue
		}
		// Leftover socket: reconnect and leave it. Never start a second child.
		client, st, recErr := shim.Reconnect(ctx, sock)
		if recErr == nil && st != nil && st.GetAlive() {
			if client != nil {
				_ = client.Close()
			}
			continue
		}
		if client != nil {
			_ = client.Close()
		}
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
	m.closeClient(inst.InstanceID)
	m.clients[inst.InstanceID] = client
	return m.applyRunning(ctx, &inst, int(st.GetPid()), boot)
}

// ShimTakeoverReady verifies that every active managed instance on this boot
// is owned through a live Shim connection and still refers to the stored PID.
func (m *Manager) ShimTakeoverReady(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	boot, err := m.deps.Store.GetBootID(ctx)
	if err != nil {
		return fmt.Errorf("read boot id: %w", err)
	}
	specs, err := m.deps.Store.ListSpecs(ctx)
	if err != nil {
		return fmt.Errorf("list process specs: %w", err)
	}
	for _, spec := range specs {
		instances, err := m.deps.Store.ListInstances(ctx, spec.ProcessID)
		if err != nil {
			return fmt.Errorf("list instances for %s: %w", spec.ProcessID, err)
		}
		for _, inst := range instances {
			active := sameBoot(inst.BootID, boot) && pidAlive(inst.PID)
			if !active && inst.Observed != ObservedRunning {
				continue
			}
			if !active || inst.Observed != ObservedRunning {
				return fmt.Errorf("instance %s is not a running process owned on this boot", inst.InstanceID)
			}
			if inst.ShimPID <= 0 || !pidAlive(inst.ShimPID) {
				return fmt.Errorf("instance %s shim is not alive", inst.InstanceID)
			}
			socket := m.deps.Layout.ShimSocket(inst.InstanceID)
			if !socketExists(socket) {
				return fmt.Errorf("instance %s shim socket is missing", inst.InstanceID)
			}
			client, status, err := shim.Reconnect(ctx, socket)
			if err != nil {
				return fmt.Errorf("instance %s shim takeover: %w", inst.InstanceID, err)
			}
			peerPID, peerErr := client.PeerPID()
			_ = client.Close()
			if peerErr != nil {
				return fmt.Errorf("instance %s shim peer identity: %w", inst.InstanceID, peerErr)
			}
			if peerPID != inst.ShimPID {
				return fmt.Errorf("instance %s shim peer pid %d does not match stored pid %d", inst.InstanceID, peerPID, inst.ShimPID)
			}
			if status == nil || !status.GetAlive() || int(status.GetPid()) != inst.PID {
				return fmt.Errorf("instance %s shim status does not match pid %d", inst.InstanceID, inst.PID)
			}
		}
	}
	return nil
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
