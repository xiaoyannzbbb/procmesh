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