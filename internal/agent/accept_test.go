package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/localhttp"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"golang.org/x/sys/unix"
)

var agentRoots sync.Map

func TestCase3_AgentCancelDoesNotKillChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base, pid := startSleepAgent(t, ctx)
	cancel()
	time.Sleep(200 * time.Millisecond)
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("child died after agent cancel: %v", err)
	}
	_ = base
}

func TestCase4_BootMismatchDoesNotAdoptOldPID(t *testing.T) {
	// Host reboot is systemd/manual; automate boot mismatch via SetBootID.
	ctx := context.Background()
	root, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	if err := st.SetBootID(ctx, "boot-1"); err != nil {
		t.Fatal(err)
	}
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
		Autostart: false,
	}
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
	oldShim := insts[0].ShimPID
	t.Cleanup(func() {
		if oldPID > 0 {
			_ = unix.Kill(oldPID, unix.SIGKILL)
		}
		if oldShim > 0 {
			_ = unix.Kill(oldShim, unix.SIGKILL)
		}
	})

	if err := st.SetBootID(ctx, "rebooted"); err != nil {
		t.Fatal(err)
	}
	m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, insts[0].InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed == process.ObservedUnknown {
		t.Fatalf("boot mismatch must not treat old pid as UNKNOWN: %+v", got)
	}
	if got.PID == oldPID {
		t.Fatalf("must not adopt old pid %d after boot mismatch: %+v", oldPID, got)
	}
}

func TestCase5_ConcurrentCAS(t *testing.T) {
	s := openStoreAt(t, filepath.Join(t.TempDir(), "store.db"))
	ctx := context.Background()
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "t", ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sp := spec
			sp.Command = fmt.Sprintf("v-%d", i)
			_, err := s.PutSpec(ctx, sp, 1, "t", "")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	var conflicts, oks int
	for err := range errs {
		if err == nil {
			oks++
			continue
		}
		if errcode.Is(err, errcode.CONFLICT) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected %v", err)
	}
	if oks != 1 || conflicts != 1 {
		t.Fatalf("oks=%d conflicts=%d", oks, conflicts)
	}
}

func TestCase10_DiskEmergencyNullsNewLogs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	_ = layout.Ensure()
	lm := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 96, nil }, Now: time.Now}
	if _, err := lm.Protect(ctx); err != nil {
		t.Fatal(err)
	}
	if lm.WritesAllowed() {
		t.Fatal("writes should be blocked")
	}
	t.Cleanup(func() { cleanupDataDir(root) })
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now, Logs: lm})
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
	stdout, _ := logmgr.InstancePaths(layout, "p1", process.MakeInstanceID("p1", 0))
	if b, _ := os.ReadFile(stdout); len(b) != 0 {
		t.Fatalf("expected no new log bytes, got %q", b)
	}
}

func TestStartSleepAgent_CleanupKillsShimAndChild(t *testing.T) {
	var child, shim int
	t.Run("start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		_, child = startSleepAgent(t, ctx)
		t.Cleanup(cancel)
		shim = runtimePID(t, testAgentRoot(t), "p1:0", true)
		if shim <= 0 {
			t.Fatal("expected shim pid in runtime file")
		}
	})
	if child > 0 && unix.Kill(child, 0) == nil {
		_ = unix.Kill(child, unix.SIGKILL)
		t.Fatalf("child pid %d leaked after startSleepAgent cleanup", child)
	}
	if shim > 0 && unix.Kill(shim, 0) == nil {
		_ = unix.Kill(shim, unix.SIGKILL)
		t.Fatalf("shim pid %d leaked after startSleepAgent cleanup", shim)
	}
}

func TestCase11_CorruptDBDoesNotKillProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base, pid := startSleepAgent(t, ctx)
	t.Cleanup(cancel)
	db := filepath.Join(testAgentRoot(t), "store.db")
	if err := os.WriteFile(db, []byte("not-a-sqlite-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(base+"/v1/processes/p1/start", "application/json", strings.NewReader(`{"operation_id":"op-x","operator":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 503 {
		t.Fatalf("want 503 got %d", res.StatusCode)
	}
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("corrupt db killed process: %v", err)
	}
}

func startSleepAgent(t *testing.T, ctx context.Context) (string, int) {
	t.Helper()
	// macOS unix socket path cap is ~104 bytes; t.TempDir() is too long.
	root, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	agentRoots.Store(t, root)
	t.Cleanup(func() { agentRoots.Delete(t) })
	t.Cleanup(func() { cleanupDataDir(root) })

	got := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:  root,
			Listen:   "127.0.0.1:0",
			ShimBin:  testShimBin,
			OnListen: func(addr string) { got <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-got:
	case err := <-errCh:
		t.Fatalf("agent run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("agent listen timeout")
	}
	base := "http://" + addr
	body := `{"operation_id":"op-create","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"sleep","command":"/bin/sleep","args":["60"],"instances":1,"autostart":true}}`
	res, err := http.Post(base+"/v1/processes", "application/json", strings.NewReader(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("create %v %v", err, res)
	}
	start := `{"operation_id":"op-start","operator":"t"}`
	res, err = http.Post(base+"/v1/processes/p1/start", "application/json", strings.NewReader(start))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("start %v %v", err, res)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		res, err = http.Get(base + "/v1/processes")
		if err == nil && res.StatusCode == 200 {
			var listed localhttp.ListProcessesResponse
			if err := json.NewDecoder(res.Body).Decode(&listed); err == nil {
				for _, p := range listed.Processes {
					for _, inst := range p.Instances {
						if inst.PID > 0 {
							return base, inst.PID
						}
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	res, _ = http.Get(base + "/v1/processes")
	var dump []byte
	if res != nil {
		dump, _ = io.ReadAll(res.Body)
	}
	t.Fatalf("no pid after start body=%s", dump)
	return base, 0
}

func testAgentRoot(t *testing.T) string {
	t.Helper()
	v, ok := agentRoots.Load(t)
	if !ok {
		t.Fatal("no agent root")
	}
	return v.(string)
}

func cleanupDataDir(root string) {
	if root == "" {
		return
	}
	layout := paths.New(root)
	entries, err := os.ReadDir(layout.RuntimeDir)
	if err == nil {
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
			killAgentPIDs(snap.PID, snap.ShimPID)
		}
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		return
	}
	defer func() { _ = st.Close() }()
	specs, err := st.ListSpecs(context.Background())
	if err != nil {
		return
	}
	for _, spec := range specs {
		insts, err := st.ListInstances(context.Background(), spec.ProcessID)
		if err != nil {
			continue
		}
		for _, inst := range insts {
			killAgentPIDs(inst.PID, inst.ShimPID)
		}
	}
}

func killAgentPIDs(pid, shimPID int) {
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

func runtimePID(t *testing.T, root, instanceID string, shim bool) int {
	t.Helper()
	name := strings.ReplaceAll(instanceID, ":", "_") + ".json"
	data, err := os.ReadFile(filepath.Join(root, "runtime", name))
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		PID     int `json:"pid"`
		ShimPID int `json:"shim_pid"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	if shim {
		return snap.ShimPID
	}
	return snap.PID
}

func openStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
