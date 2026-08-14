package agent

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

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
