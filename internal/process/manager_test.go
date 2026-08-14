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
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"golang.org/x/sys/unix"
)

func TestApplySpec_CreatesInstancesAndIdempotentOp(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 2}
	got, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", "add")
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestRevision != 1 {
		t.Fatalf("rev=%d", got.LatestRevision)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 2 {
		t.Fatalf("insts=%d err=%v", len(insts), err)
	}
	if insts[0].Desired != process.DesiredStopped || insts[0].Observed != process.ObservedStopped {
		t.Fatalf("want stopped %+v", insts[0])
	}
	again, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", "add")
	if err != nil {
		t.Fatal(err)
	}
	if again.LatestRevision != 1 {
		t.Fatalf("idempotent replay changed rev=%d", again.LatestRevision)
	}
}

func TestApplySpec_RejectsEmptyOperationID(t *testing.T) {
	m, _, _ := newTestManager(t)
	_, err := m.ApplySpec(context.Background(), process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true", Instances: 1}, 0, "", "t", "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestSetDesired_UpdatesAllInstances(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 2}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range insts {
		if inst.Desired != process.DesiredRunning {
			t.Fatalf("desired %+v", inst)
		}
	}
}

func TestDeleteSpec_RequiresStoppedNoLivePID(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 1}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSpec(ctx, "p1", got.LatestRevision, "op-d1", "t"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredStopped, "op-stop", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSpec(ctx, "p1", got.LatestRevision, "op-d2", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSpec(ctx, "p1"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want gone got %v", err)
	}
}

func TestAdopt_RecordsLivePIDWithoutLaunch(t *testing.T) {
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "self", Command: "/bin/true", Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	if err := m.Adopt(ctx, process.MakeInstanceID("p1", 0), pid, "op-a", "t"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != pid || got.Observed != process.ObservedRunning || got.Health != process.HealthUnknown {
		t.Fatalf("%+v", got)
	}
	raw, err := os.ReadFile(filepath.Join(layout.RuntimeDir, "p1_0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snap map[string]any
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if int(snap["pid"].(float64)) != pid {
		t.Fatalf("runtime %+v", snap)
	}
	if _, err := os.Stat(layout.ShimSocket(got.InstanceID)); !os.IsNotExist(err) {
		t.Fatal("adopt must not launch shim")
	}
}

func TestAdopt_DeadPIDNotFound(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "self", Command: "/bin/true", Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	err := m.Adopt(ctx, process.MakeInstanceID("p1", 0), 1<<30, "op-a", "t")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND got %v", err)
	}
}

func TestResetFailure_FatalToStopped(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	inst, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0))
	if err != nil {
		t.Fatal(err)
	}
	inst.Observed = process.ObservedFatal
	inst.Desired = process.DesiredRunning
	inst.RestartCount = 9
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetFailure(ctx, "p1", "op-r", "t"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedStopped {
		t.Fatalf("got %s", got.Observed)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("desired %s", got.Desired)
	}
}

func TestReconcile_StartsAndStops(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	t.Cleanup(func() { killManaged(t, st, "p1") })
	spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 || insts[0].PID <= 0 || insts[0].Observed != process.ObservedRunning {
		t.Fatalf("%+v %v", insts, err)
	}
	pid := insts[0].PID
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("not running: %v", err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredStopped, "op-stop", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, insts[0].InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedStopped {
		t.Fatalf("got %s", got.Observed)
	}
	if err := unix.Kill(pid, 0); err == nil {
		t.Fatal("child still alive after stop")
	}
}

func TestApplySpec_AppliesLogDefaults(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	got, err := m.ApplySpec(ctx, process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 1}, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Log.MaxSize != 100<<20 || got.Log.MaxFiles != 10 || got.Log.MaxAge != 7*24*time.Hour || !got.Log.Compress {
		t.Fatalf("log defaults %+v", got.Log)
	}
}

func TestReconcile_WritesStdoutToInstanceLog(t *testing.T) {
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	t.Cleanup(func() { killManaged(t, st, "p1") })
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "echo",
		Command:   "/bin/sh",
		Args:      []string{"-c", "printf 'hello-log\\n'; exec sleep 60"},
		Instances: 1,
	}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := logmgr.InstancePaths(layout, "p1", process.MakeInstanceID("p1", 0))
	waitFileContains(t, stdout, "hello-log")
	if _, err := os.Stat(stderr); err != nil {
		t.Fatalf("stderr not prepared: %v", err)
	}
}

func TestReconcile_EmergencyStdioDevNullAndAudit(t *testing.T) {
	ctx := context.Background()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	lm := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 96, nil }, Now: time.Now}
	if _, err := lm.Protect(ctx); err != nil {
		t.Fatal(err)
	}
	if lm.WritesAllowed() {
		t.Fatal("writes should be blocked")
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now, Logs: lm})
	t.Cleanup(func() { killManaged(t, st, "p1") })
	spec := process.ProcessSpec{ProcessID: "p1", Name: "echo", Command: "/bin/echo", Args: []string{"hi"}, Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	inst, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0))
	if err != nil || inst.PID <= 0 {
		t.Fatalf("process should still start: %+v %v", inst, err)
	}
	stdout, _ := logmgr.InstancePaths(layout, "p1", process.MakeInstanceID("p1", 0))
	if b, _ := os.ReadFile(stdout); len(b) != 0 {
		t.Fatalf("expected no new log bytes, got %q", b)
	}
	evs, err := st.ListAudit(ctx, "p1", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Action == "LOG_WRITES_DISABLED" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing LOG_WRITES_DISABLED audit: %+v", evs)
	}
}

func waitFileContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		last = b
		if err == nil && strings.Contains(string(b), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("log %s missing %q, got %q", path, want, last)
}

func newTestManager(t *testing.T) (*process.Manager, *store.Store, paths.Layout) {
	t.Helper()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	return process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now}), st, layout
}
