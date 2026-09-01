package process_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"golang.org/x/sys/unix"
)

var processIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type countingStateStore struct {
	*store.Store
	mu                 sync.Mutex
	listInstancesCalls int
	bootIDCalls        int
}

func (s *countingStateStore) ListInstances(ctx context.Context, processID string) ([]process.Instance, error) {
	s.mu.Lock()
	s.listInstancesCalls++
	s.mu.Unlock()
	return s.Store.ListInstances(ctx, processID)
}

func (s *countingStateStore) GetBootID(ctx context.Context) (string, error) {
	s.mu.Lock()
	s.bootIDCalls++
	s.mu.Unlock()
	return s.Store.GetBootID(ctx)
}

func (s *countingStateStore) resetCounts() {
	s.mu.Lock()
	s.listInstancesCalls = 0
	s.bootIDCalls = 0
	s.mu.Unlock()
}

func (s *countingStateStore) counts() (listInstances, bootID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listInstancesCalls, s.bootIDCalls
}

func TestReconcile_ReadsOneStoreSnapshotPerPass(t *testing.T) {
	ctx := context.Background()
	root := shortRoot(t)
	base := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	counted := &countingStateStore{Store: base}
	m := process.NewManager(process.Deps{Store: counted, Layout: layout, Now: time.Now})
	if _, err := m.ApplySpec(ctx, process.ProcessSpec{
		ProcessID: "p1",
		Name:      "stopped",
		Command:   "/bin/true",
		Instances: 10,
	}, 0, "op-create", "t", ""); err != nil {
		t.Fatal(err)
	}

	counted.resetCounts()
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	listCalls, bootCalls := counted.counts()
	if listCalls != 1 || bootCalls != 1 {
		t.Fatalf("store reads per reconcile: ListInstances=%d GetBootID=%d, want 1 each", listCalls, bootCalls)
	}
}

func TestReconcile_ThrottlesHealthyShimStatusChecks(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	m, st, _ := newTestManagerNow(t, func() time.Time { return now })
	inst := startSleep(t, m, st, process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Health:    process.HealthCheckSpec{Type: "alive", Interval: time.Minute},
	})

	first := process.LastShimCheckForTest(m, inst.InstanceID)
	if first.IsZero() {
		t.Fatal("starting an instance must record a shim status verification")
	}
	now = now.Add(time.Second)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := process.LastShimCheckForTest(m, inst.InstanceID); !got.Equal(first) {
		t.Fatalf("healthy shim was checked again after 1s: first=%v got=%v", first, got)
	}
	now = now.Add(10 * time.Second)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := process.LastShimCheckForTest(m, inst.InstanceID); !got.After(first) {
		t.Fatalf("shim was not rechecked after verification interval: first=%v got=%v", first, got)
	}
}

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

func TestApplySpec_GeneratesProcessID(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	got, err := m.ApplySpec(ctx, process.ProcessSpec{Name: "n", Command: "/bin/true"}, 0, "op", "t", "")
	if err != nil || got.ProcessID == "" {
		t.Fatalf("%+v %v", got, err)
	}
	if !processIDRE.MatchString(got.ProcessID) {
		t.Fatalf("uuid form %q", got.ProcessID)
	}
}

func TestApplySpec_EmptyProcessIDReplayKeepsSameID(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{Name: "n", Command: "/bin/true"}
	first, err := m.ApplySpec(ctx, spec, 0, "op-empty", "t", "")
	if err != nil || first.ProcessID == "" {
		t.Fatalf("%+v %v", first, err)
	}
	if !processIDRE.MatchString(first.ProcessID) {
		t.Fatalf("uuid form %q", first.ProcessID)
	}
	again, err := m.ApplySpec(ctx, spec, 0, "op-empty", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ProcessID != first.ProcessID {
		t.Fatalf("replay generated new id %q vs %q", again.ProcessID, first.ProcessID)
	}
	if again.LatestRevision != first.LatestRevision {
		t.Fatalf("replay changed rev %d vs %d", again.LatestRevision, first.LatestRevision)
	}
	got, err := st.GetSpec(ctx, first.ProcessID)
	if err != nil || got.LatestRevision != first.LatestRevision {
		t.Fatalf("store %+v %v", got, err)
	}
}

func TestApplySpec_ScaleUpInheritsDesiredRunning(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true", Instances: 1}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	spec.Instances = 2
	if _, err := m.ApplySpec(ctx, spec, got.LatestRevision, "op-up", "t", ""); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 2 {
		t.Fatalf("insts=%d err=%v", len(insts), err)
	}
	for _, inst := range insts {
		if inst.Desired != process.DesiredRunning {
			t.Fatalf("scale-up must inherit RUNNING: %+v", inst)
		}
	}
}

func TestApplySpec_ScaleDownDeletesStoppedExtra(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true", Instances: 2}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 2 {
		t.Fatalf("create insts=%d err=%v", len(insts), err)
	}
	extra := insts[1]
	extra.Desired = process.DesiredStopped
	extra.Observed = process.ObservedStopped
	extra.PID = 0
	if err := st.PutInstance(ctx, extra); err != nil {
		t.Fatal(err)
	}
	spec.Instances = 1
	if _, err := m.ApplySpec(ctx, spec, got.LatestRevision, "op-scale", "t", ""); err != nil {
		t.Fatal(err)
	}
	insts, err = st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 {
		t.Fatalf("scale-down insts=%d err=%v", len(insts), err)
	}
	if insts[0].Ordinal != 0 {
		t.Fatalf("want ordinal 0 got %+v", insts[0])
	}
}

func TestApplySpec_ScaleDownClearsInstanceMemory(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true", Instances: 2}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	id := process.MakeInstanceID("p1", 1)
	process.SeedInstanceMemory(m, id)
	if !process.InstanceMemoryHeld(m, id) {
		t.Fatal("seed failed")
	}
	spec.Instances = 1
	if _, err := m.ApplySpec(ctx, spec, got.LatestRevision, "op-scale", "t", ""); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 {
		t.Fatalf("scale-down insts=%d err=%v", len(insts), err)
	}
	if process.InstanceMemoryHeld(m, id) {
		t.Fatal("scale-down must drop failures/nextTry/health/clients")
	}
}

func TestApplySpec_StoreClosed(t *testing.T) {
	m, st, _ := newTestManager(t)
	_ = st.Close()
	_, err := m.ApplySpec(context.Background(), process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true"}, 0, "op", "t", "")
	if err == nil {
		t.Fatal("expected store error")
	}
}

func TestApplySpec_RejectsEmptyOperationID(t *testing.T) {
	m, _, _ := newTestManager(t)
	_, err := m.ApplySpec(context.Background(), process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true", Instances: 1}, 0, "", "t", "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestApplySpec_RejectsCircularDependency(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	a := process.ProcessSpec{
		ProcessID: "pa", Name: "a", Command: "/bin/true", Instances: 1,
		Dependencies: []process.Dependency{{ProcessName: "b"}},
	}
	if _, err := m.ApplySpec(ctx, a, 0, "op-a", "t", ""); err != nil {
		t.Fatal(err)
	}
	b := process.ProcessSpec{
		ProcessID: "pb", Name: "b", Command: "/bin/true", Instances: 1,
		Dependencies: []process.Dependency{{ProcessName: "a"}},
	}
	_, err := m.ApplySpec(ctx, b, 0, "op-b", "t", "")
	if !errcode.Is(err, errcode.INVALID) || !strings.Contains(err.Error(), "circular dependency") {
		t.Fatalf("want circular INVALID, got %v", err)
	}
	if _, err := st.GetSpec(ctx, "pb"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("cycle must not persist: %v", err)
	}
}

func TestApplySpec_AllowsMissingDependencyOnCreate(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	api := process.ProcessSpec{
		ProcessID: "api", Name: "api", Command: "/bin/true", Instances: 1,
		Dependencies: []process.Dependency{{ProcessName: "mysql"}},
	}
	if _, err := m.ApplySpec(ctx, api, 0, "op-api", "t", ""); err != nil {
		t.Fatalf("forward-ref dep must save: %v", err)
	}
}

func TestApplySpec_UpdateCreatesCycle(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	a := process.ProcessSpec{
		ProcessID: "pa", Name: "a", Command: "/bin/true", Instances: 1,
		Dependencies: []process.Dependency{{ProcessName: "b"}},
	}
	b := process.ProcessSpec{ProcessID: "pb", Name: "b", Command: "/bin/true", Instances: 1}
	gotA, err := m.ApplySpec(ctx, a, 0, "op-a", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := m.ApplySpec(ctx, b, 0, "op-b", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	b.Dependencies = []process.Dependency{{ProcessName: "a"}}
	_, err = m.ApplySpec(ctx, b, gotB.LatestRevision, "op-cycle", "t", "")
	if !errcode.Is(err, errcode.INVALID) || !strings.Contains(err.Error(), "circular dependency") {
		t.Fatalf("want circular INVALID, got %v", err)
	}
	cur, err := st.GetSpec(ctx, "pb")
	if err != nil || len(cur.Dependencies) != 0 || cur.LatestRevision != gotB.LatestRevision {
		t.Fatalf("update must not persist: %+v %v (a rev %d)", cur, err, gotA.LatestRevision)
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
	if _, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0)); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("instance row must be gone got %v", err)
	}
}

func TestDeleteSpec_RemovesInstanceRows(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 2}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	id0 := process.MakeInstanceID("p1", 0)
	id1 := process.MakeInstanceID("p1", 1)
	process.SeedInstanceMemory(m, id0)
	process.SeedInstanceMemory(m, id1)
	if err := m.DeleteSpec(ctx, "p1", got.LatestRevision, "op-del", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetInstance(ctx, id0); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("instance 0 want NOT_FOUND got %v", err)
	}
	if _, err := st.GetInstance(ctx, id1); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("instance 1 want NOT_FOUND got %v", err)
	}
	if process.InstanceMemoryHeld(m, id0) || process.InstanceMemoryHeld(m, id1) {
		t.Fatal("DeleteSpec must forget instance memory")
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

func TestApplySpec_RejectsLogDirInsideRaft(t *testing.T) {
	ctx := context.Background()
	m, _, layout := newTestManager(t)
	spec := process.ProcessSpec{
		Name: "n", Command: "/bin/true", Instances: 1,
		Log: process.LogPolicy{Directory: filepath.Join(layout.Root, "raft", "logs")},
	}
	_, err := m.ApplySpec(ctx, spec, 0, "op-log-bad", "t", "")
	if err == nil || !strings.Contains(err.Error(), "log path:") {
		t.Fatalf("got %v", err)
	}
}

func TestRotateLogs_UsesSpecPolicyAndLogNow(t *testing.T) {
	ctx := context.Background()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(2_000_000, 0)
	lm := &logmgr.Manager{Root: root, Now: func() time.Time { return fixed }}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now, Logs: lm})
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "n",
		Command:   "/bin/true",
		Instances: 1,
		Log:       process.LogPolicy{MaxSize: 32, MaxFiles: 2, MaxAge: time.Second, Compress: false},
	}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := logmgr.InstancePaths(layout, "p1", process.MakeInstanceID("p1", 0))
	if err := os.MkdirAll(filepath.Dir(stdout), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdout, bytes.Repeat([]byte("x"), 64), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderr, []byte("e"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := fixed.Add(-2 * time.Second)
	if err := os.WriteFile(stdout+".1", []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stdout+".1", old, old); err != nil {
		t.Fatal(err)
	}
	if err := m.RotateLogs(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(stdout)
	if err != nil || info.Size() != 0 {
		t.Fatalf("stdout should be truncated, size err=%v", err)
	}
	if _, err := os.Stat(stdout + ".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stdout + ".2"); !os.IsNotExist(err) {
		t.Fatal("aged archive should be deleted via Logs.Now")
	}
	if b, err := os.ReadFile(stderr); err != nil || string(b) != "e" {
		t.Fatalf("stderr under MaxSize must stay: %q %v", b, err)
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

func TestRotateLogs_RunningProcessContinuesInCurrentLog(t *testing.T) {
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	t.Cleanup(func() { killManaged(t, st, "p1") })
	continueFile := filepath.Join(t.TempDir(), "continue")
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "rotating-log",
		Command:   "/bin/sh",
		Args: []string{
			"-c",
			"printf 'before-rotation-before-rotation-before-rotation\\n'; while [ ! -f \"$1\" ]; do sleep 0.01; done; printf 'after-rotation\\n'; exec sleep 60",
			"sh",
			continueFile,
		},
		Instances: 1,
		Log:       process.LogPolicy{MaxSize: 32, MaxFiles: 2},
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

	stdout, _ := logmgr.InstancePaths(layout, "p1", process.MakeInstanceID("p1", 0))
	waitFileContains(t, stdout, "before-rotation")
	if err := m.RotateLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(continueFile, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFileContains(t, stdout, "after-rotation")

	lines, err := logmgr.Tail(stdout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, "\n") != "after-rotation" {
		t.Fatalf("current log lines=%q want post-rotation process output", lines)
	}
	archive, err := os.ReadFile(stdout + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(archive), "before-rotation") || strings.Contains(string(archive), "after-rotation") {
		t.Fatalf("archive must contain only pre-rotation output, got %q", archive)
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

func TestNewTestManager_CleanupKillsForgottenShim(t *testing.T) {
	if testing.Short() {
		t.Skip("shim")
	}
	var childPID, shimPID int
	t.Run("start_without_killManaged", func(t *testing.T) {
		ctx := context.Background()
		m, st, _ := newTestManager(t)
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
		inst, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0))
		if err != nil || inst.PID <= 0 || inst.ShimPID <= 0 {
			t.Fatalf("start %+v %v", inst, err)
		}
		childPID = inst.PID
		shimPID = inst.ShimPID
	})
	if pidAlive(shimPID) {
		_ = unix.Kill(shimPID, unix.SIGKILL)
		t.Fatalf("shim pid %d leaked after newTestManager cleanup", shimPID)
	}
	if pidAlive(childPID) {
		_ = unix.Kill(childPID, unix.SIGKILL)
		t.Fatalf("child pid %d leaked after newTestManager cleanup", childPID)
	}
}

func TestNewTestManager_CleanupKillsScaledDownShim(t *testing.T) {
	if testing.Short() {
		t.Skip("shim")
	}
	var extraShim int
	t.Run("scale_down", func(t *testing.T) {
		ctx := context.Background()
		m, st, _ := newTestManager(t)
		spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 2}
		got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
			t.Fatal(err)
		}
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		extra, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 1))
		if err != nil || extra.ShimPID <= 0 {
			t.Fatalf("extra %+v %v", extra, err)
		}
		extraShim = extra.ShimPID
		spec.Instances = 1
		if _, err := m.ApplySpec(ctx, spec, got.LatestRevision, "op-scale", "t", ""); err != nil {
			t.Fatal(err)
		}
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	})
	if pidAlive(extraShim) {
		_ = unix.Kill(extraShim, unix.SIGKILL)
		t.Fatalf("scaled-down shim pid %d leaked after newTestManager cleanup", extraShim)
	}
}

func newTestManager(t *testing.T) (*process.Manager, *store.Store, paths.Layout) {
	t.Helper()
	return newTestManagerNow(t, time.Now)
}

func newTestManagerNow(t *testing.T, now func() time.Time) (*process.Manager, *store.Store, paths.Layout) {
	t.Helper()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killTestLayout(st, layout) })
	return process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: now}), st, layout
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || err == unix.EPERM
}

func startSleep(t *testing.T, m *process.Manager, st *store.Store, spec process.ProcessSpec) process.Instance {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() { killManaged(t, st, spec.ProcessID) })
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, spec.ProcessID, process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	inst, err := st.GetInstance(ctx, process.MakeInstanceID(spec.ProcessID, 0))
	if err != nil || inst.PID <= 0 || inst.Observed != process.ObservedRunning {
		t.Fatalf("start %+v %v", inst, err)
	}
	return inst
}

func TestReconcile_MissingWorkingDirRecordsLastError(t *testing.T) {
	ctx := context.Background()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killTestLayout(st, layout) })
	m := process.NewManager(process.Deps{
		Store:   st,
		Layout:  layout,
		ShimBin: testShimBin,
		Now:     time.Now,
		Logger:  logger,
	})
	missing := filepath.Join(t.TempDir(), "no-such-workdir")
	spec := process.ProcessSpec{
		ProcessID:        "p1",
		Name:             "missing-cwd",
		Command:          "/bin/true",
		WorkingDirectory: missing,
		Instances:        1,
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
	got, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedBackoff {
		t.Fatalf("observed=%s want BACKOFF %+v", got.Observed, got)
	}
	if got.LastError == "" {
		t.Fatalf("last_error empty, instance=%+v logs=%s", got, logBuf.String())
	}
	low := strings.ToLower(got.LastError)
	if !strings.Contains(low, "no such file") && !strings.Contains(low, "chdir") {
		t.Fatalf("last_error=%q want missing working directory", got.LastError)
	}
	if !strings.Contains(logBuf.String(), got.LastError) {
		t.Fatalf("agent log missing last_error %q: %s", got.LastError, logBuf.String())
	}
}

func TestReconcile_SuccessfulStartClearsLastError(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"30"},
		Instances: 1,
	}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	id := process.MakeInstanceID("p1", 0)
	inst, err := st.GetInstance(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	inst.LastError = "chdir /missing: no such file or directory"
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killManaged(t, st, spec.ProcessID) })
	if err := m.SetDesired(ctx, spec.ProcessID, process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, id)
	if err != nil || got.PID <= 0 || got.Observed != process.ObservedRunning {
		t.Fatalf("start %+v %v", got, err)
	}
	if got.LastError != "" {
		t.Fatalf("last_error=%q want empty after successful start", got.LastError)
	}
}

func TestReconcile_HealthHTTPMarksHealthy(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Health: process.HealthCheckSpec{
			Type:             "http",
			URL:              srv.URL,
			ExpectedStatus:   200,
			Timeout:          time.Second,
			FailureThreshold: 1,
			SuccessThreshold: 1,
		},
	}
	inst := startSleep(t, m, st, spec)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Health != process.HealthHealthy {
		t.Fatalf("health %s", got.Health)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("desired %s", got.Desired)
	}
}

func TestReconcile_UnhealthyRestartsWithoutChangingDesired(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Health: process.HealthCheckSpec{
			Type:             "http",
			URL:              srv.URL,
			ExpectedStatus:   200,
			Timeout:          time.Second,
			FailureThreshold: 1,
			SuccessThreshold: 1,
			RestartOnFailure: true,
		},
	}
	inst := startSleep(t, m, st, spec)
	oldPID := inst.PID
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("desired changed to %s", got.Desired)
	}
	if got.PID == oldPID || got.PID <= 0 {
		t.Fatalf("expected new pid, got %+v", got)
	}
	if got.Observed != process.ObservedRunning {
		t.Fatalf("observed %s", got.Observed)
	}
	if got.RestartCount < 1 {
		t.Fatalf("restart count %d", got.RestartCount)
	}
}

func TestReconcile_HealthCooldownSkipsRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	m, st, _ := newTestManagerNow(t, func() time.Time { return now })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Health: process.HealthCheckSpec{
			Type:             "http",
			URL:              srv.URL,
			ExpectedStatus:   200,
			Timeout:          time.Second,
			FailureThreshold: 1,
			SuccessThreshold: 1,
			RestartOnFailure: true,
			RestartCooldown:  time.Hour,
		},
	}
	inst := startSleep(t, m, st, spec)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PID == inst.PID {
		t.Fatal("expected first health restart")
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if again.PID != after.PID {
		t.Fatalf("cooldown should skip restart %d -> %d", after.PID, again.PID)
	}
	if again.Desired != process.DesiredRunning {
		t.Fatalf("desired %s", again.Desired)
	}
}

func TestReconcile_HealthInitialDelayAndInterval(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	m, st, _ := newTestManagerNow(t, func() time.Time { return now })
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Health: process.HealthCheckSpec{
			Type:             "http",
			URL:              srv.URL,
			ExpectedStatus:   200,
			Timeout:          time.Second,
			InitialDelay:     10 * time.Second,
			Interval:         10 * time.Second,
			FailureThreshold: 1,
			SuccessThreshold: 1,
		},
	}
	inst := startSleep(t, m, st, spec)
	now = now.Add(5 * time.Second)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 || got.Health != process.HealthUnknown {
		t.Fatalf("delay: hits=%d health=%s", hits, got.Health)
	}
	now = now.Add(6 * time.Second)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 || got.Health != process.HealthHealthy {
		t.Fatalf("first check: hits=%d health=%s", hits, got.Health)
	}
	now = now.Add(5 * time.Second)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("interval not elapsed: hits=%d", hits)
	}
	now = now.Add(6 * time.Second)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("second check: hits=%d", hits)
	}
}

func TestRecover_HealthUnknownUntilFirstCheck(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Health: process.HealthCheckSpec{
			Type:             "http",
			URL:              srv.URL,
			ExpectedStatus:   200,
			Timeout:          time.Second,
			FailureThreshold: 1,
			SuccessThreshold: 1,
		},
	}
	inst := startSleep(t, m, st, spec)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil || got.Health != process.HealthHealthy {
		t.Fatalf("pre-recover %+v %v", got, err)
	}
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Health != process.HealthUnknown {
		t.Fatalf("recover health %s", got.Health)
	}
	if got.Desired != process.DesiredRunning || got.Observed != process.ObservedRunning {
		t.Fatalf("recover state %+v", got)
	}
}

func TestReconcile_HealthRestartCountsTowardCrashLoop(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Restart: process.RestartPolicy{
			Mode:        process.RestartOnFailure,
			MaxRetries:  2,
			RetryWindow: time.Minute,
		},
		Health: process.HealthCheckSpec{
			Type:             "http",
			URL:              srv.URL,
			ExpectedStatus:   200,
			Timeout:          time.Second,
			FailureThreshold: 1,
			SuccessThreshold: 1,
			RestartOnFailure: true,
		},
	}
	inst := startSleep(t, m, st, spec)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedRunning || got.Desired != process.DesiredRunning {
		t.Fatalf("first restart %+v", got)
	}
	if got.PID == inst.PID {
		t.Fatal("expected health restart")
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedFatal {
		t.Fatalf("want FATAL got %s", got.Observed)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("desired %s", got.Desired)
	}
}

func TestReconcile_UnhealthyWithoutRestartKeepsProcess(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Health: process.HealthCheckSpec{
			Type:             "http",
			URL:              srv.URL,
			ExpectedStatus:   200,
			Timeout:          time.Second,
			FailureThreshold: 1,
			SuccessThreshold: 1,
			RestartOnFailure: false,
		},
	}
	inst := startSleep(t, m, st, spec)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Health != process.HealthUnhealthy {
		t.Fatalf("health %s", got.Health)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("desired %s", got.Desired)
	}
	if got.PID != inst.PID {
		t.Fatalf("pid changed %d -> %d", inst.PID, got.PID)
	}
}

func TestReconcile_BackoffTicksDoNotCountAsFailures(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	m, st, _ := newTestManagerNow(t, func() time.Time { return now })
	t.Cleanup(func() { killManaged(t, st, "p1") })
	const delay = 5 * time.Second
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "fail",
		Command:   "/bin/sh",
		Args:      []string{"-c", "exit 1"},
		Instances: 1,
		Restart: process.RestartPolicy{
			Mode:        process.RestartOnFailure,
			MaxRetries:  3,
			RetryWindow: time.Minute,
			Backoff:     process.Backoff{Initial: delay, Max: time.Minute, Multiplier: 2},
		},
	}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	// Drive until first crash is observed (EXITED/BACKOFF), without advancing fake time.
	deadline := time.Now().Add(3 * time.Second)
	id := process.MakeInstanceID("p1", 0)
	for {
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetInstance(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Observed == process.ObservedExited || got.Observed == process.ObservedBackoff {
			break
		}
		if got.Observed == process.ObservedFatal {
			t.Fatalf("FATAL before backoff ticks: %+v", got)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for first crash: %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Advance 1s per Reconcile, 4 times — still inside Initial backoff; must not FATAL.
	var got process.Instance
	for i := 0; i < 4; i++ {
		now = now.Add(time.Second)
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		var err error
		got, err = st.GetInstance(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Observed == process.ObservedFatal {
			t.Fatalf("tick %d: backoff ticks must not exhaust MaxRetries, got FATAL", i)
		}
	}
	if got.Observed != process.ObservedExited && got.Observed != process.ObservedBackoff &&
		got.Observed != process.ObservedStarting {
		t.Fatalf("want BACKOFF/EXITED/STARTING, got %s", got.Observed)
	}
}

func TestReconcile_OnFailureCleanExitClearsPriorExitCode(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "once",
		Command:   "/bin/sleep",
		Args:      []string{"0.3"},
		Instances: 1,
		Restart: process.RestartPolicy{
			Mode:    process.RestartOnFailure,
			Backoff: process.Backoff{Initial: time.Hour, Max: time.Hour, Multiplier: 1},
		},
	}
	inst := startSleep(t, m, st, spec)
	prior := 1
	inst.ExitCode = &prior
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var clean process.Instance
	for {
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		var err error
		clean, err = st.GetInstance(ctx, inst.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		if clean.PID == 0 && clean.Observed != process.ObservedRunning && clean.Observed != process.ObservedStarting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for sleep to exit: %+v", clean)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if clean.ExitCode == nil || *clean.ExitCode != 0 {
		code := any(nil)
		if clean.ExitCode != nil {
			code = *clean.ExitCode
		}
		t.Fatalf("clean exit must store 0, got %v inst=%+v", code, clean)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PID != 0 || after.Observed == process.ObservedRunning || after.Observed == process.ObservedStarting {
		t.Fatalf("on-failure must not restart clean exit: %+v", after)
	}
}

func TestReconcile_HonorsBackoffBeforeRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	m, st, _ := newTestManagerNow(t, func() time.Time { return now })
	const delay = 5 * time.Second
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Restart: process.RestartPolicy{
			Mode:        process.RestartOnFailure,
			MaxRetries:  10,
			RetryWindow: time.Minute,
			// Multiplier 1 keeps Delay == Initial after the first recorded failure.
			Backoff: process.Backoff{Initial: delay, Max: time.Minute, Multiplier: 1},
		},
	}
	inst := startSleep(t, m, st, spec)
	oldPID := inst.PID
	if err := unix.Kill(oldPID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, oldPID)

	// Crash observation at T+0 must not start a replacement yet.
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed == process.ObservedRunning || got.PID > 0 {
		t.Fatalf("must not restart before Delay: %+v", got)
	}
	if got.Observed != process.ObservedExited && got.Observed != process.ObservedBackoff {
		t.Fatalf("want EXITED/BACKOFF after crash, got %+v", got)
	}

	// T+Delay: restart is due.
	now = now.Add(delay)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedRunning || got.PID <= 0 || got.PID == oldPID {
		t.Fatalf("want new running pid after Delay, got %+v", got)
	}
}

func waitPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}

func TestAdopt_RespectsInitialDelay(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	m, st, _ := newTestManagerNow(t, func() time.Time { return now })
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Health: process.HealthCheckSpec{
			Type:             "http",
			URL:              srv.URL,
			ExpectedStatus:   200,
			Timeout:          time.Second,
			InitialDelay:     time.Hour,
			FailureThreshold: 1,
			SuccessThreshold: 1,
		},
	}
	inst := startSleep(t, m, st, spec)
	// Simulate pre-fix Adopt: clear StartedAt then re-Adopt the live PID.
	inst.StartedAt = nil
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := m.Adopt(ctx, inst.InstanceID, inst.PID, "op-a", "t"); err != nil {
		t.Fatal(err)
	}
	adopted, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.StartedAt == nil {
		t.Fatal("Adopt must set StartedAt when nil")
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 || got.Health != process.HealthUnknown {
		t.Fatalf("InitialDelay must skip Check: hits=%d health=%s", hits, got.Health)
	}
}

// TestApplyHealth_SkipsWhenDesiredStoppedDuringProbe proves post-unlock
// revalidation: if Desired becomes STOPPED while Check runs unlocked, the
// probe result must not Observe or health-restart.
func TestApplyHealth_SkipsWhenDesiredStoppedDuringProbe(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		// Would be UNHEALTHY + restart if Observe were applied.
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)

	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
	}
	inst := startSleep(t, m, st, spec)
	// Attach health after start so startSleep's Reconcile does not block on probe.
	cur, err := st.GetSpec(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	spec.Health = process.HealthCheckSpec{
		Type:             "http",
		URL:              srv.URL,
		ExpectedStatus:   200,
		Timeout:          5 * time.Second,
		FailureThreshold: 1,
		SuccessThreshold: 1,
		RestartOnFailure: true,
	}
	if _, err := m.ApplySpec(ctx, spec, cur.LatestRevision, "op-h", "t", "health"); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- m.Reconcile(ctx) }()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("health probe did not start")
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredStopped, "op-stop-mid-probe", "t"); err != nil {
		close(release)
		<-errCh
		t.Fatal(err)
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Health != process.HealthUnknown {
		t.Fatalf("must not Observe after Desired=STOPPED during probe: health=%s", got.Health)
	}
	if got.RestartCount != 0 {
		t.Fatalf("must not health-restart after Desired=STOPPED: restart=%d", got.RestartCount)
	}
	if got.Desired != process.DesiredStopped {
		t.Fatalf("desired %s", got.Desired)
	}
	// Health path must not replace the process; stop is deferred to a later pass.
	if got.PID != inst.PID {
		t.Fatalf("unexpected pid change from health path: old=%d new=%d", inst.PID, got.PID)
	}
}

func TestReconcile_WaitsForDepHealthy(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
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
	if err := m.SetDesired(ctx, "pid-mysql", process.DesiredRunning, "op-s-mysql", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "pid-api", process.DesiredRunning, "op-s-api", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	gotMySQL, err := st.GetInstance(ctx, process.MakeInstanceID("pid-mysql", 0))
	if err != nil {
		t.Fatal(err)
	}
	if gotMySQL.Observed != process.ObservedRunning || gotMySQL.PID <= 0 {
		t.Fatalf("mysql want RUNNING, got %+v", gotMySQL)
	}
	if gotMySQL.Health == process.HealthHealthy {
		t.Fatalf("mysql health should stay UNKNOWN without a live HTTP probe, got %s", gotMySQL.Health)
	}
	gotAPI, err := st.GetInstance(ctx, process.MakeInstanceID("pid-api", 0))
	if err != nil {
		t.Fatal(err)
	}
	if gotAPI.Observed != process.ObservedStopped || gotAPI.PID != 0 {
		t.Fatalf("api must wait for mysql HEALTHY, got %+v", gotAPI)
	}
}

func TestStart_ResourceLimitUnsupportedAudits(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Resources: process.ResourceLimit{MemoryBytes: 1 << 20},
	}
	inst := startSleep(t, m, st, spec)
	if inst.PID <= 0 || inst.Observed != process.ObservedRunning {
		t.Fatalf("start must not fail: %+v", inst)
	}
	evs, err := st.ListAudit(ctx, "p1", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Action == "RESOURCE_LIMIT_UNSUPPORTED" {
			found = true
			break
		}
	}
	if runtime.GOOS != "linux" {
		if !found {
			t.Fatalf("missing RESOURCE_LIMIT_UNSUPPORTED audit: %+v", evs)
		}
		return
	}
	if !found {
		// Linux: apply succeeded, or the kernel accepted the cgroup write.
		dir := filepath.Join("/sys/fs/cgroup/procmesh", strconv.Itoa(inst.PID))
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("linux: want apply or audit, no cgroup at %s, audit=%+v", dir, evs)
		}
	}
}

func TestReconcile_StartsAfterDepStarted(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
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
	}
	api := process.ProcessSpec{
		ProcessID: "pid-api",
		Name:      "api",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Dependencies: []process.Dependency{{
			ProcessName: "mysql",
			Condition:   process.DepStarted,
		}},
	}
	if _, err := m.ApplySpec(ctx, mysql, 0, "op-mysql", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplySpec(ctx, api, 0, "op-api", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "pid-mysql", process.DesiredRunning, "op-s-mysql", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "pid-api", process.DesiredRunning, "op-s-api", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	gotMySQL, err := st.GetInstance(ctx, process.MakeInstanceID("pid-mysql", 0))
	if err != nil {
		t.Fatal(err)
	}
	if gotMySQL.Observed != process.ObservedRunning || gotMySQL.PID <= 0 {
		t.Fatalf("mysql want RUNNING, got %+v", gotMySQL)
	}
	gotAPI, err := st.GetInstance(ctx, process.MakeInstanceID("pid-api", 0))
	if err != nil {
		t.Fatal(err)
	}
	if gotAPI.Observed != process.ObservedStopped || gotAPI.PID != 0 {
		t.Fatalf("api must not start in the same pass, got %+v", gotAPI)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	gotAPI, err = st.GetInstance(ctx, process.MakeInstanceID("pid-api", 0))
	if err != nil {
		t.Fatal(err)
	}
	if gotAPI.Observed != process.ObservedRunning || gotAPI.PID <= 0 {
		t.Fatalf("api should start after mysql STARTED, got %+v", gotAPI)
	}
}

func TestManager_ReadAccessors(t *testing.T) {
	ctx := context.Background()
	m, _, layout := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetSpec(ctx, "p1")
	if err != nil || got.ProcessID != "p1" {
		t.Fatalf("%+v %v", got, err)
	}
	specs, err := m.ListSpecs(ctx)
	if err != nil || len(specs) != 1 {
		t.Fatalf("%v %v", specs, err)
	}
	insts, err := m.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 {
		t.Fatalf("%v %v", insts, err)
	}
	inst, err := m.GetInstance(ctx, insts[0].InstanceID)
	if err != nil || inst.InstanceID != insts[0].InstanceID {
		t.Fatalf("%+v %v", inst, err)
	}
	status, result, _, err := m.PeekOp(ctx, "op-c")
	if err != nil || status != "SUCCESS" || len(result) == 0 {
		t.Fatalf("peek %s %q %v", status, result, err)
	}
	if m.Layout().Root != layout.Root {
		t.Fatalf("layout root %q", m.Layout().Root)
	}
}

func TestApplySpec_RejectsInvalidName(t *testing.T) {
	m, _, _ := newTestManager(t)
	_, err := m.ApplySpec(context.Background(), process.ProcessSpec{ProcessID: "p1", Name: "!!!", Command: "/bin/true"}, 0, "op", "t", "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestApplySpec_ReplayFailedRevision(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	_, err := m.ApplySpec(ctx, process.ProcessSpec{ProcessID: "missing", Name: "n", Command: "/bin/true"}, 5, "op-fail", "t", "")
	if err == nil {
		t.Fatal("expected revision error")
	}
	_, err = m.ApplySpec(ctx, process.ProcessSpec{ProcessID: "missing", Name: "n", Command: "/bin/true"}, 5, "op-fail", "t", "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("replay want INVALID got %v", err)
	}
}

func TestRestart_StopsThenSetsDesiredRunning(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Restart:   process.RestartPolicy{Mode: process.RestartAlways},
	}
	inst := startSleep(t, m, st, spec)
	oldPID := inst.PID
	if err := m.Restart(ctx, "p1", "op-rst", "t"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("desired %s", got.Desired)
	}
	if got.Observed != process.ObservedStopped {
		t.Fatalf("observed after inner stop %s", got.Observed)
	}
	if err := unix.Kill(oldPID, 0); err == nil {
		t.Fatal("old pid still alive")
	}
	if err := m.Restart(ctx, "p1", "op-rst", "t"); err != nil {
		t.Fatal(err)
	}
}

func TestRestart_ReconcileStartsNewPID(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
	}
	inst := startSleep(t, m, st, spec)
	oldPID := inst.PID
	if err := m.Restart(ctx, "p1", "op-rst", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedRunning || got.PID <= 0 {
		t.Fatalf("after restart+reconcile %+v", got)
	}
	if got.PID == oldPID {
		t.Fatalf("pid not replaced: %d", oldPID)
	}
}

func TestRestart_MissingSpec(t *testing.T) {
	m, _, _ := newTestManager(t)
	err := m.Restart(context.Background(), "nope", "op-r", "t")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("got %v", err)
	}
	err = m.Restart(context.Background(), "nope", "op-r", "t")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("replay want INVALID got %v", err)
	}
}

func TestNewManager_DefaultNow(t *testing.T) {
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout})
	if err := m.RotateLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestApplySpec_ReplayFallsBackToGetSpec(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true"}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.StartOp(ctx, "op-bad-json", "t", "apply_spec", "p1", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperation(ctx, "op-bad-json", "SUCCESS", []byte("not-json"), ""); err != nil {
		t.Fatal(err)
	}
	got, err := m.ApplySpec(ctx, spec, 0, "op-bad-json", "t", "")
	if err != nil || got.ProcessID != "p1" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, _, _, err := st.StartOp(ctx, "op-blank", "t", "apply_spec", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperation(ctx, "op-blank", "SUCCESS", nil, ""); err != nil {
		t.Fatal(err)
	}
	_, err = m.ApplySpec(ctx, process.ProcessSpec{Name: "n", Command: "/bin/true"}, 0, "op-blank", "t", "")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("empty id replay want NOT_FOUND got %v", err)
	}
}

func TestSetDesired_ReplayAndMissing(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	err := m.SetDesired(ctx, "missing", process.DesiredRunning, "op-miss", "t")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("got %v", err)
	}
	err = m.SetDesired(ctx, "missing", process.DesiredRunning, "op-miss", "t")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("replay want INVALID got %v", err)
	}
}

func TestDeleteSpec_ReplayAndLivePID(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 1}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSpec(ctx, "p1", got.LatestRevision, "op-d", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSpec(ctx, "p1", got.LatestRevision, "op-d", "t"); err != nil {
		t.Fatal(err)
	}
	spec = process.ProcessSpec{ProcessID: "p2", Name: "live", Command: "/bin/true", Instances: 1}
	got, err = m.ApplySpec(ctx, spec, 0, "op-c2", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := st.GetInstance(ctx, process.MakeInstanceID("p2", 0))
	if err != nil {
		t.Fatal(err)
	}
	boot, err := st.GetBootID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inst.PID = os.Getpid()
	inst.BootID = boot
	inst.Desired = process.DesiredStopped
	inst.Observed = process.ObservedStopped
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSpec(ctx, "p2", got.LatestRevision, "op-dlive", "t"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("live pid want INVALID got %v", err)
	}
	err = m.DeleteSpec(ctx, "p2", got.LatestRevision, "op-dlive", "t")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("replay want INVALID got %v", err)
	}
}

func TestDeleteSpec_NonTerminalAndBadRevision(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true", Instances: 1}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0))
	if err != nil {
		t.Fatal(err)
	}
	inst.Desired = process.DesiredStopped
	inst.Observed = process.ObservedRunning
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSpec(ctx, "p1", got.LatestRevision, "op-nt", "t"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("non-terminal want INVALID got %v", err)
	}
	inst.Observed = process.ObservedStopped
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSpec(ctx, "p1", got.LatestRevision+9, "op-rev", "t"); err == nil {
		t.Fatal("bad revision")
	}
}

func TestResetFailure_Replay(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "true", Command: "/bin/true", Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetFailure(ctx, "p1", "op-r", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetFailure(ctx, "p1", "op-r", "t"); err != nil {
		t.Fatal(err)
	}
}

func TestAdopt_ReplayAndMissingInstance(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "self", Command: "/bin/true", Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	if err := m.Adopt(ctx, process.MakeInstanceID("p1", 0), pid, "op-a", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Adopt(ctx, process.MakeInstanceID("p1", 0), pid, "op-a", "t"); err != nil {
		t.Fatal(err)
	}
	err := m.Adopt(ctx, "nope:0", pid, "op-miss", "t")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("got %v", err)
	}
	err = m.Adopt(ctx, "nope:0", pid, "op-miss", "t")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("replay want INVALID got %v", err)
	}
}

func TestRotateLogs_NilManagerAndCanceled(t *testing.T) {
	var m *process.Manager
	if err := m.RotateLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	m2, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m2.RotateLogs(ctx); err == nil {
		t.Fatal("canceled ctx")
	}
}

func TestApplySpec_ScaleDownKeepsLiveExtra(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "/bin/true", Instances: 2}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 2 {
		t.Fatalf("insts=%d err=%v", len(insts), err)
	}
	extra := insts[1]
	extra.Desired = process.DesiredStopped
	extra.Observed = process.ObservedStopped
	extra.PID = os.Getpid()
	if err := st.PutInstance(ctx, extra); err != nil {
		t.Fatal(err)
	}
	spec.Instances = 1
	if _, err := m.ApplySpec(ctx, spec, got.LatestRevision, "op-scale", "t", ""); err != nil {
		t.Fatal(err)
	}
	insts, err = st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 2 {
		t.Fatalf("live extra must remain insts=%d err=%v", len(insts), err)
	}
}

func TestReconcile_ScaleDownDeletesStoppedExtra(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	t.Cleanup(func() { killManaged(t, st, "p1") })
	spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 2}
	got, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 2 {
		t.Fatalf("insts=%d err=%v", len(insts), err)
	}
	id1 := process.MakeInstanceID("p1", 1)
	process.SeedInstanceMemory(m, id1)
	spec.Instances = 1
	if _, err := m.ApplySpec(ctx, spec, got.LatestRevision, "op-scale", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err = st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 {
		t.Fatalf("reconcile scale-down insts=%d err=%v", len(insts), err)
	}
	if process.InstanceMemoryHeld(m, id1) {
		t.Fatal("reconcile delete must forget extra instance")
	}
}

func TestReconcile_RunAsUserWithoutLookUser(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "n",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		RunAsUser: "nobody",
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
	got, err := st.GetInstance(ctx, process.MakeInstanceID("p1", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != 0 {
		t.Fatalf("must not start without LookUser: %+v", got)
	}
}

func TestResolve_ByNameAndID(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	got, err := m.ApplySpec(ctx, process.ProcessSpec{Name: "nginx", Command: "/bin/true"}, 0, "op-a", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	byID, err := m.Resolve(ctx, got.ProcessID)
	if err != nil || byID.Name != "nginx" {
		t.Fatalf("%+v %v", byID, err)
	}
	byName, err := m.Resolve(ctx, "nginx")
	if err != nil || byName.ProcessID != got.ProcessID {
		t.Fatalf("%+v %v", byName, err)
	}
	if _, err := m.Resolve(ctx, "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("%v", err)
	}
	if _, err := m.Resolve(ctx, ""); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("%v", err)
	}
}

func TestKill_StopsRunningProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("shim")
	}
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "k1", Name: "k1", Command: "/bin/sleep", Args: []string{"60"}}
	inst := startSleep(t, m, st, spec)
	if err := m.Kill(ctx, "k1", "op-kill", "t"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != process.DesiredStopped {
		t.Fatalf("desired=%s", got.Desired)
	}
	if pidAlive(got.PID) && got.Observed != process.ObservedStopped {
		// after kill+reconcile, pid should be dead or observed STOPPED
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		got, _ = st.GetInstance(ctx, inst.InstanceID)
	}
	if got.Observed != process.ObservedStopped && got.Observed != process.ObservedStopping {
		t.Fatalf("observed=%s pid=%d", got.Observed, got.PID)
	}
}

func TestKill_Idempotent(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	if _, err := m.ApplySpec(ctx, process.ProcessSpec{ProcessID: "k2", Name: "k2", Command: "/bin/true"}, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(ctx, "k2", "op-k", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(ctx, "k2", "op-k", "t"); err != nil {
		t.Fatal(err)
	}
}

func TestRollback_CreatesNewRevision(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	s1 := process.ProcessSpec{ProcessID: "r1", Name: "r1", Command: "/bin/true", Args: []string{"a"}}
	if _, err := m.ApplySpec(ctx, s1, 0, "op-1", "t", "c1"); err != nil {
		t.Fatal(err)
	}
	s1.Args = []string{"b"}
	if _, err := m.ApplySpec(ctx, s1, 1, "op-2", "t", "c2"); err != nil {
		t.Fatal(err)
	}
	out, err := m.Rollback(ctx, "r1", 1, 2, "op-rb", "t", "back")
	if err != nil {
		t.Fatal(err)
	}
	if out.LatestRevision != 3 || len(out.Args) != 1 || out.Args[0] != "a" {
		t.Fatalf("%+v", out)
	}
	revs, err := m.ListRevisions(ctx, "r1")
	if err != nil || len(revs) != 3 {
		t.Fatalf("n=%d err=%v", len(revs), err)
	}
}

func TestRollback_Conflict(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	s1 := process.ProcessSpec{ProcessID: "r2", Name: "r2", Command: "/bin/true"}
	if _, err := m.ApplySpec(ctx, s1, 0, "op-1", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rollback(ctx, "r2", 1, 99, "op-rb", "t", ""); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("%v", err)
	}
}

func TestResolve_StoreClosed(t *testing.T) {
	m, st, _ := newTestManager(t)
	_ = st.Close()
	if _, err := m.Resolve(context.Background(), "x"); err == nil {
		t.Fatal("expected store error")
	}
}

func TestKill_MissingSpec(t *testing.T) {
	m, _, _ := newTestManager(t)
	err := m.Kill(context.Background(), "nope", "op-kmiss", "t")
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("got %v", err)
	}
	err = m.Kill(context.Background(), "nope", "op-kmiss", "t")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("replay want INVALID got %v", err)
	}
}

func TestKill_EmptyOpID(t *testing.T) {
	m, _, _ := newTestManager(t)
	if err := m.Kill(context.Background(), "k", "", "t"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestKill_OrphanRequiresAdopt(t *testing.T) {
	if testing.Short() {
		t.Skip("shim")
	}
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "k3", Name: "k3", Command: "/bin/sleep", Args: []string{"60"}}
	inst := startSleep(t, m, st, spec)
	oldPID := inst.PID
	if err := unix.Kill(inst.ShimPID, unix.SIGKILL); err != nil {
		t.Fatalf("kill shim: %v", err)
	}
	waitPIDGone(t, inst.ShimPID)
	_ = os.Remove(layout.ShimSocket(inst.InstanceID))
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	err := m.Kill(ctx, "k3", "op-kill-orphan", "t")
	if !errcode.Is(err, errcode.INVALID) || !strings.Contains(err.Error(), "adopt required") {
		t.Fatalf("want INVALID adopt required, got %v", err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != process.DesiredRunning {
		t.Fatalf("refused kill must not flip desired: %+v", got)
	}
	if got.Observed != process.ObservedUnknown {
		t.Fatalf("observed %s want UNKNOWN", got.Observed)
	}
	if err := unix.Kill(oldPID, 0); err != nil {
		t.Fatalf("orphan pid must stay alive: %v", err)
	}
	err = m.Kill(ctx, "k3", "op-kill-orphan", "t")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("replay want INVALID got %v", err)
	}
}

func TestRollback_Idempotent(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	s1 := process.ProcessSpec{ProcessID: "r3", Name: "r3", Command: "/bin/true", Args: []string{"a"}}
	if _, err := m.ApplySpec(ctx, s1, 0, "op-1", "t", ""); err != nil {
		t.Fatal(err)
	}
	s1.Args = []string{"b"}
	if _, err := m.ApplySpec(ctx, s1, 1, "op-2", "t", ""); err != nil {
		t.Fatal(err)
	}
	out, err := m.Rollback(ctx, "r3", 1, 2, "op-rb2", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.Rollback(ctx, "r3", 1, 2, "op-rb2", "t", "")
	if err != nil || again.LatestRevision != out.LatestRevision || again.Args[0] != "a" {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestRollback_EmptyOpID(t *testing.T) {
	m, _, _ := newTestManager(t)
	if _, err := m.Rollback(context.Background(), "r", 1, 1, "", "t", ""); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestReconcile_CustomDirectoryAndRedirect(t *testing.T) {
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	t.Cleanup(func() { killManaged(t, st, "p1") })
	dir := filepath.Join(t.TempDir(), "app")
	spec := process.ProcessSpec{
		ProcessID: "p1", Name: "echo", Command: "/bin/sh",
		Args:      []string{"-c", "printf 'out\\n'; printf 'err\\n' >&2; exec sleep 60"},
		Instances: 1,
		Log:       process.LogPolicy{Directory: dir, RedirectStderr: true},
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
	stdout := filepath.Join(dir, "0", "stdout.log")
	stderr := filepath.Join(dir, "0", "stderr.log")
	waitFileContains(t, stdout, "out")
	if _, err := os.Stat(stderr); !os.IsNotExist(err) {
		t.Fatal("stderr.log must not exist when redirected")
	}
	body, err := os.ReadFile(stdout)
	if err != nil || !strings.Contains(string(body), "err") {
		t.Fatalf("merged stderr missing: %q %v", body, err)
	}
	_ = layout
}

func TestRotateLogs_UsesActiveRevisionPath(t *testing.T) {
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	t.Cleanup(func() { killManaged(t, st, "p1") })
	oldDir := filepath.Join(t.TempDir(), "old")
	spec := process.ProcessSpec{
		ProcessID: "p1", Name: "n", Command: "/bin/sleep", Args: []string{"60"},
		Instances: 1, Log: process.LogPolicy{Directory: oldDir, MaxSize: 32, MaxFiles: 2},
	}
	got, err := m.ApplySpec(ctx, spec, 0, "op1", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	oldStdout := filepath.Join(oldDir, "0", "stdout.log")
	if err := os.WriteFile(oldStdout, bytes.Repeat([]byte("x"), 64), 0o640); err != nil {
		t.Fatal(err)
	}
	newDir := filepath.Join(t.TempDir(), "new")
	got.Log.Directory = newDir
	if _, err := m.ApplySpec(ctx, got, got.LatestRevision, "op2", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.RotateLogs(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(oldStdout)
	if err != nil || info.Size() != 0 {
		t.Fatalf("should rotate old file, size err=%v info=%v", err, info)
	}
	if _, err := os.Stat(filepath.Join(newDir, "0", "stdout.log")); !os.IsNotExist(err) {
		t.Fatal("must not create new path before restart")
	}
	_ = layout
	_ = st
}
