package process_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"golang.org/x/sys/unix"
)

func openStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.GetOrCreateNodeID(context.Background()); err != nil {
		t.Fatal(err)
	}
	boot, err := st.GetBootID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if boot == "" {
		if _, err := st.RotateBootID(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func mustBoot(t *testing.T, st *store.Store) string {
	t.Helper()
	id, err := st.GetBootID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		id, err = st.RotateBootID(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func shortRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func killManaged(t *testing.T, st *store.Store, processID string) {
	t.Helper()
	insts, err := st.ListInstances(context.Background(), processID)
	if err != nil {
		return
	}
	for _, inst := range insts {
		if inst.PID > 0 {
			_ = unix.Kill(inst.PID, unix.SIGKILL)
		}
		if inst.ShimPID > 0 {
			_ = unix.Kill(inst.ShimPID, unix.SIGKILL)
		}
	}
}

func TestRecoverThenReconcile_OrphanPIDStaysUnknown(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	spec := process.ProcessSpec{ProcessID: "p1", Name: "self", Command: "/bin/true", Instances: 1}
	if _, err := st.PutSpec(ctx, spec, 0, "t", ""); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	inst := process.Instance{
		InstanceID: process.MakeInstanceID("p1", 0),
		ProcessID:  "p1",
		Ordinal:    0,
		PID:        pid,
		Desired:    process.DesiredRunning,
		Observed:   process.ObservedRunning,
		BootID:     mustBoot(t, st),
	}
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedUnknown {
		t.Fatalf("reconcile must leave orphan UNKNOWN, got %s", got.Observed)
	}
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("recover-then-reconcile must not kill orphan PID: %v", err)
	}
}

func TestRecover_DoesNotDoubleStartLiveShim(t *testing.T) {
	ctx := context.Background()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: true}
	t.Cleanup(func() { killManaged(t, st, "p1") })
	if _, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-start", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 || insts[0].PID <= 0 {
		t.Fatalf("%+v %v", insts, err)
	}
	pid1 := insts[0].PID
	m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err = st.ListInstances(ctx, "p1")
	if err != nil || insts[0].PID != pid1 {
		t.Fatalf("double start? %+v %v", insts, err)
	}
	if err := unix.Kill(pid1, 0); err != nil {
		t.Fatalf("child died: %v", err)
	}
}

func TestRecover_DeadShimLivePIDBecomesUnknown(t *testing.T) {
	ctx := context.Background()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: true}
	t.Cleanup(func() { killManaged(t, st, "p1") })
	if _, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-start", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 || insts[0].PID <= 0 || insts[0].ShimPID <= 0 {
		t.Fatalf("%+v %v", insts, err)
	}
	oldPID := insts[0].PID
	shimPID := insts[0].ShimPID
	boot, err := st.GetBootID(ctx)
	if err != nil || boot == "" {
		t.Fatalf("boot %q err %v", boot, err)
	}
	if err := unix.Kill(shimPID, unix.SIGKILL); err != nil {
		t.Fatalf("kill shim: %v", err)
	}
	// Wait for shim death and socket teardown.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(shimPID, 0); err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = os.Remove(layout.ShimSocket(insts[0].InstanceID))

	m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, insts[0].InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != oldPID {
		t.Fatalf("pid changed (double start?): %d -> %d", oldPID, got.PID)
	}
	if got.Observed != process.ObservedUnknown {
		t.Fatalf("observed %s want UNKNOWN", got.Observed)
	}
	if err := unix.Kill(oldPID, 0); err != nil {
		t.Fatalf("child must stay alive: %v", err)
	}
	boot2, err := st.GetBootID(ctx)
	if err != nil || boot2 != boot {
		t.Fatalf("boot must be unchanged: %q -> %q err %v", boot, boot2, err)
	}
}

func TestRecover_BootMismatchIgnoresOldPID(t *testing.T) {
	ctx := context.Background()

	t.Run("autostart_restarts", func(t *testing.T) {
		root := shortRoot(t)
		st := openStoreAt(t, filepath.Join(root, "store.db"))
		layout := paths.New(root)
		if err := layout.Ensure(); err != nil {
			t.Fatal(err)
		}
		m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
		spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: true}
		t.Cleanup(func() { killManaged(t, st, "p1") })
		if _, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", ""); err != nil {
			t.Fatal(err)
		}
		if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-start", "t"); err != nil {
			t.Fatal(err)
		}
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		insts, err := st.ListInstances(ctx, "p1")
		if err != nil || len(insts) != 1 || insts[0].PID <= 0 {
			t.Fatalf("%+v %v", insts, err)
		}
		oldPID := insts[0].PID
		if err := st.SetBootID(ctx, "other-boot"); err != nil {
			t.Fatal(err)
		}
		killManaged(t, st, "p1")
		// Wait for reaping so recover does not see old PIDs as live under wrong boot.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if unix.Kill(oldPID, 0) != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.Remove(layout.ShimSocket(insts[0].InstanceID))

		m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
		if err := m2.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetInstance(ctx, insts[0].InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PID <= 0 {
			t.Fatalf("autostart should start new child: %+v", got)
		}
		if got.PID == oldPID {
			t.Fatalf("expected new pid after reboot, still %d", oldPID)
		}
		if got.Observed != process.ObservedRunning {
			t.Fatalf("observed %s want RUNNING", got.Observed)
		}
	})

	t.Run("no_autostart_stops", func(t *testing.T) {
		root := shortRoot(t)
		st := openStoreAt(t, filepath.Join(root, "store.db"))
		layout := paths.New(root)
		if err := layout.Ensure(); err != nil {
			t.Fatal(err)
		}
		m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
		spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: false}
		t.Cleanup(func() { killManaged(t, st, "p1") })
		if _, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", ""); err != nil {
			t.Fatal(err)
		}
		if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-start", "t"); err != nil {
			t.Fatal(err)
		}
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		insts, err := st.ListInstances(ctx, "p1")
		if err != nil || len(insts) != 1 || insts[0].PID <= 0 {
			t.Fatalf("%+v %v", insts, err)
		}
		oldPID := insts[0].PID
		if err := st.SetBootID(ctx, "other-boot"); err != nil {
			t.Fatal(err)
		}
		killManaged(t, st, "p1")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if unix.Kill(oldPID, 0) != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.Remove(layout.ShimSocket(insts[0].InstanceID))

		m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
		if err := m2.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetInstance(ctx, insts[0].InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PID != 0 {
			t.Fatalf("must not start without autostart: %+v", got)
		}
		if got.Observed != process.ObservedStopped {
			t.Fatalf("observed %s want STOPPED", got.Observed)
		}
	})
}

func TestReconcile_DeadShimLivePIDBecomesUnknown(t *testing.T) {
	ctx := context.Background()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: true}
	t.Cleanup(func() { killManaged(t, st, "p1") })
	if _, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-start", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 || insts[0].PID <= 0 || insts[0].ShimPID <= 0 {
		t.Fatalf("%+v %v", insts, err)
	}
	oldPID := insts[0].PID
	shimPID := insts[0].ShimPID
	if err := unix.Kill(shimPID, unix.SIGKILL); err != nil {
		t.Fatalf("kill shim: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(shimPID, 0); err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = os.Remove(layout.ShimSocket(insts[0].InstanceID))

	// Do not Recover — only Reconcile must orphan a live child without a socket.
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, insts[0].InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != oldPID {
		t.Fatalf("pid changed (double start?): %d -> %d", oldPID, got.PID)
	}
	if got.Observed != process.ObservedUnknown {
		t.Fatalf("observed %s want UNKNOWN", got.Observed)
	}
	if err := unix.Kill(oldPID, 0); err != nil {
		t.Fatalf("child must stay alive: %v", err)
	}
}

func TestReconcile_DeadChildWithoutSocketRestarts(t *testing.T) {
	ctx := context.Background()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Restart:   process.RestartPolicy{Mode: process.RestartAlways},
	}
	t.Cleanup(func() { killManaged(t, st, "p1") })
	if _, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-start", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 || insts[0].PID <= 0 {
		t.Fatalf("%+v %v", insts, err)
	}
	oldPID := insts[0].PID
	shimPID := insts[0].ShimPID
	if oldPID > 0 {
		_ = unix.Kill(oldPID, unix.SIGKILL)
	}
	if shimPID > 0 {
		_ = unix.Kill(shimPID, unix.SIGKILL)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		childDead := oldPID <= 0 || unix.Kill(oldPID, 0) != nil
		shimDead := shimPID <= 0 || unix.Kill(shimPID, 0) != nil
		if childDead && shimDead {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = os.Remove(layout.ShimSocket(insts[0].InstanceID))

	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, insts[0].InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID <= 0 {
		t.Fatalf("expected restart with new PID: %+v", got)
	}
	if got.PID == oldPID {
		t.Fatalf("expected new pid after dead child, still %d", oldPID)
	}
	if got.Observed != process.ObservedRunning {
		t.Fatalf("observed %s want RUNNING", got.Observed)
	}
}