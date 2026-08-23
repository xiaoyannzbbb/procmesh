package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRun_ServesPprofOnDedicatedListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dataDir := t.TempDir()
	pprofReady := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:       dataDir,
			Listen:        "127.0.0.1:0",
			PprofListen:   "127.0.0.1:0",
			GossipListen:  "127.0.0.1:0",
			RPCListen:     "127.0.0.1:0",
			ControlListen: "127.0.0.1:0",
			OnPprofListen: func(addr string) { pprofReady <- addr },
		})
	}()

	var addr string
	select {
	case addr = <-pprofReady:
	case err := <-errCh:
		t.Fatalf("Run exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for pprof listener")
	}

	res, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "profile") {
		t.Fatalf("pprof index status=%d body=%q", res.StatusCode, body)
	}

	res, err = http.Get("http://" + addr + "/debug/pprof/heap?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("heap profile status=%d err=%v", res.StatusCode, err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRun_RejectsNonLoopbackPprofByDefault(t *testing.T) {
	err := Run(context.Background(), Options{
		DataDir:       t.TempDir(),
		Listen:        "127.0.0.1:0",
		PprofListen:   "0.0.0.0:6060",
		GossipListen:  "127.0.0.1:0",
		RPCListen:     "127.0.0.1:0",
		ControlListen: "127.0.0.1:0",
	})
	if err == nil || !strings.Contains(err.Error(), "pprof") {
		t.Fatalf("expected pprof listen validation error, got %v", err)
	}
}
