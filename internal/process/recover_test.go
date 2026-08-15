package process_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/shim"
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
	if t != nil {
		t.Helper()
	}
	if st == nil {
		return
	}
	insts, err := st.ListInstances(context.Background(), processID)
	if err != nil {
		return
	}
	for _, inst := range insts {
		killPIDs(inst.PID, inst.ShimPID)
	}
}

func killTestLayout(st *store.Store, layout paths.Layout) {
	if st != nil {
		specs, err := st.ListSpecs(context.Background())
		if err == nil {
			for _, spec := range specs {
				killManaged(nil, st, spec.ProcessID)
			}
		}
	}
	killRuntimeFiles(layout)
}

func killRuntimeFiles(layout paths.Layout) {
	entries, err := os.ReadDir(layout.RuntimeDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(layout.RuntimeDir, e.Name()))
		if err != nil {
			continue
		}
		var snap struct {
			PID     int `json:"pid"`
			ShimPID int `json:"shim_pid"`
		}
		if json.Unmarshal(data, &snap) != nil {
			continue
		}
		killPIDs(snap.PID, snap.ShimPID)
	}
}

func killPIDs(pid, shimPID int) {
	self := os.Getpid()
	killOne := func(p int) {
		if p <= 1 || p == self {
			return
		}
		_ = unix.Kill(p, unix.SIGKILL)
	}
	killOne(pid)
	killOne(shimPID)
}

func TestReapTestShims_KillsMatchingBinary(t *testing.T) {
	dir := shortRoot(t)
	bin := filepath.Join(dir, "procmesh-shim")
	data, err := os.ReadFile(testShimBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "x.sock")
	pid, err := shim.Launch(context.Background(), bin, sock, "reap:0")
	if err != nil {
		t.Fatal(err)
	}
	reapTestShims(bin)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if unix.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = unix.Kill(pid, unix.SIGKILL)
	t.Fatalf("reaper left shim %d", pid)
}

func TestRecover_LeftoverUnknownSocketLeftAlone(t *testing.T) {
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	sock := layout.ShimSocket("orphan:0")
	pid, err := shim.Launch(ctx, testShimBin, sock, "orphan:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	dead := filepath.Join(layout.ShimDir, "dead_0.sock")
	if err := os.WriteFile(dead, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("leftover shim must stay: %v", err)
	}
	specs, err := st.ListSpecs(ctx)
	if err != nil || len(specs) != 0 {
		t.Fatalf("must not invent spec: %v %v", specs, err)
	}
	c, status, err := shim.Reconnect(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if status == nil {
		t.Fatal("missing status")
	}
}

func TestRecover_MissingShimDir(t *testing.T) {
	ctx := context.Background()
	m, _, layout := newTestManager(t)
	if err := os.RemoveAll(layout.ShimDir); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
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
		if err := m2.Reconcile(ctx); err != nil {
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
	now := time.Now()
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: func() time.Time { return now }})
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

	// Crash observation arms backoff nextTry; restart only after Delay.
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
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

func startSleepAt(t *testing.T, spec process.ProcessSpec) (context.Context, *process.Manager, *store.Store, paths.Layout, process.Instance) {
	t.Helper()
	ctx := context.Background()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if spec.Instances == 0 {
		spec.Instances = 1
	}
	t.Cleanup(func() { killManaged(t, st, spec.ProcessID) })
	if _, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, spec.ProcessID, process.DesiredRunning, "op-start", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, spec.ProcessID)
	if err != nil || len(insts) != 1 || insts[0].PID <= 0 {
		t.Fatalf("%+v %v", insts, err)
	}
	return ctx, m, st, layout, insts[0]
}

func TestRecoverThenReconcile_AutostartHonored(t *testing.T) {
	t.Run("no_autostart_stays_stopped", func(t *testing.T) {
		ctx, _, st, layout, inst := startSleepAt(t, process.ProcessSpec{
			ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: false,
		})
		oldPID := inst.PID
		if err := st.SetBootID(ctx, "other-boot"); err != nil {
			t.Fatal(err)
		}
		killManaged(t, st, "p1")
		waitPIDGone(t, oldPID)
		_ = os.Remove(layout.ShimSocket(inst.InstanceID))

		m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
		if err := m2.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetInstance(ctx, inst.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Desired != process.DesiredStopped {
			t.Fatalf("reboot without autostart must set desired STOPPED: %+v", got)
		}
		if got.PID != 0 {
			t.Fatalf("Recover must not start: %+v", got)
		}
		if err := m2.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		got, err = st.GetInstance(ctx, inst.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PID != 0 || got.Observed != process.ObservedStopped || got.Desired != process.DesiredStopped {
			t.Fatalf("Reconcile must not start Autostart=false: %+v", got)
		}
	})

	t.Run("autostart_starts_on_reconcile", func(t *testing.T) {
		ctx, _, st, layout, inst := startSleepAt(t, process.ProcessSpec{
			ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: true,
		})
		oldPID := inst.PID
		if err := st.SetBootID(ctx, "other-boot"); err != nil {
			t.Fatal(err)
		}
		killManaged(t, st, "p1")
		waitPIDGone(t, oldPID)
		_ = os.Remove(layout.ShimSocket(inst.InstanceID))

		m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
		if err := m2.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetInstance(ctx, inst.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PID != 0 {
			t.Fatalf("Recover must not start: %+v", got)
		}
		if got.Desired != process.DesiredRunning {
			t.Fatalf("autostart must keep desired RUNNING: %+v", got)
		}
		if err := m2.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		got, err = st.GetInstance(ctx, inst.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PID <= 0 || got.PID == oldPID || got.Observed != process.ObservedRunning {
			t.Fatalf("Reconcile should start Autostart=true: %+v", got)
		}
	})
}

func TestRecoverThenReconcile_DependentWaitsForHealthy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reboot bool
		auto   bool
	}{
		{name: "same_boot_dead_pid", reboot: false, auto: false},
		{name: "reboot_autostart", reboot: true, auto: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := shortRoot(t)
			st := openStoreAt(t, filepath.Join(root, "store.db"))
			layout := paths.New(root)
			if err := layout.Ensure(); err != nil {
				t.Fatal(err)
			}
			m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
			t.Cleanup(func() {
				killManaged(t, st, "pid-mysql")
				killManaged(t, st, "pid-api")
			})
			mysql := process.ProcessSpec{
				ProcessID: "pid-mysql",
				Name:      "mysql",
				Command:   "/bin/sleep",
				Args:      []string{"60"},
				Instances: 1,
				Autostart: tc.auto,
				Health: process.HealthCheckSpec{
					Type:             "http",
					URL:              "http://127.0.0.1:1",
					Timeout:          50 * time.Millisecond,
					FailureThreshold: 1,
					SuccessThreshold: 1,
				},
			}
			api := process.ProcessSpec{
				ProcessID: "pid-api",
				Name:      "api",
				Command:   "/bin/sleep",
				Args:      []string{"60"},
				Instances: 1,
				Autostart: tc.auto,
				Dependencies: []process.Dependency{{
					ProcessName: "mysql",
					Condition:   process.DepHealthy,
				}},
			}
			if _, err := m.ApplySpec(ctx, mysql, 0, "op-mysql", "t", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := m.ApplySpec(ctx, api, 0, "op-api", "t", ""); err != nil {
				t.Fatal(err)
			}
			boot := mustBoot(t, st)
			for _, id := range []string{"pid-mysql", "pid-api"} {
				inst, err := st.GetInstance(ctx, process.MakeInstanceID(id, 0))
				if err != nil {
					t.Fatal(err)
				}
				inst.Desired = process.DesiredRunning
				inst.Observed = process.ObservedRunning
				inst.PID = 1 << 30
				inst.BootID = boot
				if err := st.PutInstance(ctx, inst); err != nil {
					t.Fatal(err)
				}
			}
			if tc.reboot {
				if err := st.SetBootID(ctx, "other-boot"); err != nil {
					t.Fatal(err)
				}
			}
			m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
			if err := m2.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"pid-mysql", "pid-api"} {
				got, err := st.GetInstance(ctx, process.MakeInstanceID(id, 0))
				if err != nil {
					t.Fatal(err)
				}
				if got.PID > 0 && unix.Kill(got.PID, 0) == nil && got.PID != 1<<30 {
					t.Fatalf("Recover must not start %s: %+v", id, got)
				}
			}
			if err := m2.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			gotMySQL, err := st.GetInstance(ctx, process.MakeInstanceID("pid-mysql", 0))
			if err != nil {
				t.Fatal(err)
			}
			if gotMySQL.Observed != process.ObservedRunning || gotMySQL.PID <= 0 {
				t.Fatalf("mysql should start on Reconcile, got %+v", gotMySQL)
			}
			if gotMySQL.Health == process.HealthHealthy {
				t.Fatalf("mysql must not be HEALTHY, got %s", gotMySQL.Health)
			}
			gotAPI, err := st.GetInstance(ctx, process.MakeInstanceID("pid-api", 0))
			if err != nil {
				t.Fatal(err)
			}
			if gotAPI.PID != 0 || gotAPI.Observed == process.ObservedRunning {
				t.Fatalf("api must not start before mysql HEALTHY, got %+v", gotAPI)
			}
		})
	}
}

func TestReconcile_LiveSocketBootMismatchDoesNotAdopt(t *testing.T) {
	ctx, m, st, _, inst := startSleepAt(t, process.ProcessSpec{
		ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: false,
	})
	oldPID := inst.PID
	if err := st.SetBootID(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(oldPID, 0); err != nil {
		t.Fatalf("child must stay alive for this test: %v", err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed == process.ObservedRunning && got.PID == oldPID {
		t.Fatalf("must not treat old pid as valid RUNNING after boot mismatch: %+v", got)
	}
	if got.Desired == process.DesiredStopped && got.Observed == process.ObservedRunning && got.PID == oldPID {
		t.Fatalf("must not take over old pid when desired STOPPED: %+v", got)
	}
}

func TestRecoverThenReconcile_EmptyBootIDKeepsDesiredRunning(t *testing.T) {
	ctx := context.Background()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	t.Cleanup(func() { killManaged(t, st, "p1") })
	spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got.BootID != "" {
		t.Fatalf("precondition: empty BootID, got %q", got.BootID)
	}

	m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetInstance(ctx, got.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("empty BootID is not a host reboot, desired must stay RUNNING: %+v", got)
	}
	if err := m2.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetInstance(ctx, got.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID <= 0 || got.Observed != process.ObservedRunning {
		t.Fatalf("agent-crash before first start must still launch: %+v", got)
	}
}

func TestReconcile_EmptyBootIDDoesNotAdoptLiveLeftover(t *testing.T) {
	ctx, _, st, layout, inst := startSleepAt(t, process.ProcessSpec{
		ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1,
	})
	leftover := inst.PID
	shimPID := inst.ShimPID
	t.Cleanup(func() {
		if leftover > 0 {
			_ = unix.Kill(leftover, unix.SIGKILL)
		}
		if shimPID > 0 {
			_ = unix.Kill(shimPID, unix.SIGKILL)
		}
	})

	inst.BootID = ""
	inst.PID = 0
	inst.ShimPID = 0
	inst.Observed = process.ObservedStopped
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m2.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID == leftover {
		t.Fatalf("empty boot_id must not adopt leftover pid: %+v", got)
	}
	if got.PID > 0 {
		t.Fatalf("must not start a second copy while leftover is alive: %+v", got)
	}
	if err := unix.Kill(leftover, 0); err != nil {
		t.Fatalf("leftover child must stay alive: %v", err)
	}
}

func TestSetDesired_OrphanStopRequiresAdopt(t *testing.T) {
	ctx, _, st, layout, inst := startSleepAt(t, process.ProcessSpec{
		ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: true,
	})
	oldPID := inst.PID
	shimPID := inst.ShimPID
	if err := unix.Kill(shimPID, unix.SIGKILL); err != nil {
		t.Fatalf("kill shim: %v", err)
	}
	waitPIDGone(t, shimPID)
	_ = os.Remove(layout.ShimSocket(inst.InstanceID))

	m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	err := m2.SetDesired(ctx, "p1", process.DesiredStopped, "op-stop-orphan", "t")
	if !errcode.Is(err, errcode.INVALID) || !strings.Contains(err.Error(), "adopt required") {
		t.Fatalf("want INVALID adopt required, got %v", err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("refused stop must not flip desired: %+v", got)
	}
	if got.Observed != process.ObservedUnknown {
		t.Fatalf("observed %s want UNKNOWN", got.Observed)
	}
	if got.PID != oldPID {
		t.Fatalf("pid changed: %d -> %d", oldPID, got.PID)
	}
	if err := unix.Kill(oldPID, 0); err != nil {
		t.Fatalf("orphan pid must stay alive: %v", err)
	}
}

func TestReconcile_AdoptRequiredDoesNotBlockOtherProcess(t *testing.T) {
	ctx, _, st, layout, orphan := startSleepAt(t, process.ProcessSpec{
		ProcessID: "orphan", Name: "orphan", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1,
	})
	shimPID := orphan.ShimPID
	if err := unix.Kill(shimPID, unix.SIGKILL); err != nil {
		t.Fatalf("kill shim: %v", err)
	}
	waitPIDGone(t, shimPID)
	_ = os.Remove(layout.ShimSocket(orphan.InstanceID))

	okSpec := process.ProcessSpec{ProcessID: "ok", Name: "ok", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	t.Cleanup(func() { killManaged(t, st, "ok") })
	if _, err := m.ApplySpec(ctx, okSpec, 0, "op-ok", "t", ""); err != nil {
		t.Fatal(err)
	}

	m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	// Leftover desired STOPPED on the orphan must not fail the whole pass.
	row, err := st.GetInstance(ctx, orphan.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	row.Desired = process.DesiredStopped
	if err := st.PutInstance(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := m2.SetDesired(ctx, "ok", process.DesiredRunning, "op-ok-start", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m2.Reconcile(ctx); err != nil {
		t.Fatalf("orphan adopt-required must not fail Reconcile: %v", err)
	}
	got, err := st.GetInstance(ctx, process.MakeInstanceID("ok", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got.PID <= 0 || got.Observed != process.ObservedRunning {
		t.Fatalf("ok process should start: %+v", got)
	}
	still, err := st.GetInstance(ctx, orphan.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Observed != process.ObservedUnknown {
		t.Fatalf("orphan must stay UNKNOWN: %+v", still)
	}
	if err := unix.Kill(orphan.PID, 0); err != nil {
		t.Fatalf("orphan must stay alive: %v", err)
	}
}
