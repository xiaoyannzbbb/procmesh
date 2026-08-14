package agent

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
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
