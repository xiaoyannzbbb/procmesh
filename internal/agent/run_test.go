package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/localhttp"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"golang.org/x/sys/unix"
)

func TestLookUser_RejectsOtherUserWithoutRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	me := os.Getuid()
	var name string
	for _, cand := range []string{"nobody", "daemon", "www-data"} {
		u, err := user.Lookup(cand)
		if err != nil {
			continue
		}
		uid, err := strconv.Atoi(u.Uid)
		if err != nil || uid == me {
			continue
		}
		name = cand
		break
	}
	if name == "" {
		t.Skip("no other existing user")
	}
	err := lookupUser(name)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("LookUser(%q) want INVALID, got %v", name, err)
	}
}

func TestCheckListen_LoopbackOK(t *testing.T) {
	if err := CheckListen("127.0.0.1:9000", false); err != nil {
		t.Fatal(err)
	}
	if err := CheckListen("localhost:9000", false); err != nil {
		t.Fatal(err)
	}
}

func TestCheckListen_NonLoopbackRequiresFlag(t *testing.T) {
	err := CheckListen("0.0.0.0:9000", false)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
	if err := CheckListen("0.0.0.0:9000", true); err != nil {
		t.Fatal(err)
	}
}

func TestRun_CorruptDBAtOpenStillServesReadyz503(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "store.db"), []byte("not-a-sqlite-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	got := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:  root,
			Listen:   "127.0.0.1:0",
			OnListen: func(addr string) { got <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-got:
	case err := <-errCh:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for listen")
	}
	res, err := http.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 503 || string(body) != "DEGRADED" {
		t.Fatalf("readyz want 503 DEGRADED, got %d %q", res.StatusCode, body)
	}
	res, err = http.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz %v %v", err, res)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRun_ReconcilesImmediatelyAfterRecover(t *testing.T) {
	root, err := os.MkdirTemp("", "pm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	ctxSeed := context.Background()
	if _, err := st.GetOrCreateNodeID(ctxSeed); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBootID(ctxSeed, paths.CurrentBootID()); err != nil {
		t.Fatal(err)
	}
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Autostart: true,
	}
	if _, err := st.PutSpec(ctxSeed, spec, 0, "t", ""); err != nil {
		t.Fatal(err)
	}
	inst := process.Instance{
		InstanceID: process.MakeInstanceID("p1", 0),
		ProcessID:  "p1",
		Ordinal:    0,
		Desired:    process.DesiredRunning,
		Observed:   process.ObservedStopped,
		Health:     process.HealthUnknown,
		BootID:     paths.CurrentBootID(),
	}
	if err := st.PutInstance(ctxSeed, inst); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
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
		t.Fatalf("run exited early: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("timeout waiting for listen")
	}
	res, err := http.Get("http://" + addr + "/v1/processes")
	if err != nil {
		t.Fatal(err)
	}
	var listed localhttp.ListProcessesResponse
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	var pid int
	for _, p := range listed.Processes {
		for _, in := range p.Instances {
			if in.PID > 0 {
				pid = in.PID
			}
		}
	}
	if pid <= 0 {
		t.Fatalf("Run must Reconcile before ticker, got %+v", listed)
	}
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRun_BlocksUntilCancelAndServesHealthz(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	got := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:  dir,
			Listen:   "127.0.0.1:0",
			OnListen: func(addr string) { got <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-got:
	case err := <-errCh:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for listen")
	}
	res, err := http.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz %v %v", err, res)
	}
	res, err = http.Get("http://" + addr + "/readyz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("readyz %v %v", err, res)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
