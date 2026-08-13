package localhttp

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestLocalHTTP_CreateStartAndConflict(t *testing.T) {
	srv := startTestAgent(t)
	body := `{"operation_id":"op-c","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"true","command":"/bin/sleep","args":["5"],"instances":1}}`
	res, err := http.Post(srv+"/v1/processes", "application/json", strings.NewReader(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("%v %v", err, res)
	}
	start := `{"operation_id":"op-s","operator":"t"}`
	res, err = http.Post(srv+"/v1/processes/p1/start", "application/json", strings.NewReader(start))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("start %v %v", err, res)
	}
	body2 := `{"operation_id":"op-c2","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"true","command":"/bin/sleep","args":["5"],"instances":1}}`
	res, err = http.Post(srv+"/v1/processes", "application/json", strings.NewReader(body2))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 409 {
		t.Fatalf("want 409 got %d", res.StatusCode)
	}
}

func startTestAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	layout := paths.New(dataDir)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.RotateBootID(context.Background())
	mgr := process.NewManager(process.Deps{
		Store:    s,
		Layout:   layout,
		ShimBin:  "procmesh-shim",
		Now:      time.Now,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(mgr, nil, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Server.Addr = ln.Addr().String() // override
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	// wait for port
	time.Sleep(50 * time.Millisecond)
	return "http://" + ln.Addr().String()
}