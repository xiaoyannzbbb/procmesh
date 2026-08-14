package localhttp

import (
	"context"
	"encoding/json"
	"io"
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
		t.Fatalf("%v %v", err, statusOf(res))
	}
	start := `{"operation_id":"op-s","operator":"t"}`
	res, err = http.Post(srv+"/v1/processes/p1/start", "application/json", strings.NewReader(start))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("start %v %v", err, statusOf(res))
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

func TestLocalHTTP_ListHealthReadyReplayAdoptLogs(t *testing.T) {
	srv := startTestAgent(t)
	res, err := http.Get(srv + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz %v %v", err, statusOf(res))
	}
	res, err = http.Get(srv + "/readyz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("readyz %v %v", err, statusOf(res))
	}

	body := `{"operation_id":"op-c","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"true","command":"/bin/sleep","args":["2"],"instances":1}}`
	res, err = http.Post(srv+"/v1/processes", "application/json", strings.NewReader(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("create %v %v", err, statusOf(res))
	}
	res, err = http.Post(srv+"/v1/processes", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.Header.Get("X-Idempotent-Replay") != "1" {
		t.Fatalf("replay create status=%d header=%q", res.StatusCode, res.Header.Get("X-Idempotent-Replay"))
	}

	res, err = http.Get(srv + "/v1/processes")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("list %v %v", err, statusOf(res))
	}
	var listed ListProcessesResponse
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Processes) != 1 || listed.Processes[0].ProcessID != "p1" {
		t.Fatalf("list %+v", listed)
	}

	stop := `{"operation_id":"op-stop","operator":"t"}`
	res, err = http.Post(srv+"/v1/processes/p1/stop", "application/json", strings.NewReader(stop))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stop %v %v", err, statusOf(res))
	}

	res, err = http.Get(srv + "/v1/processes/p1/logs?lines=10")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("logs %v %v", err, statusOf(res))
	}

	adopt := `{"operation_id":"op-a","operator":"t","pid":1}`
	res, err = http.Post(srv+"/v1/instances/p1:0/adopt", "application/json", strings.NewReader(adopt))
	if err != nil {
		t.Fatal(err)
	}
	// pid 1 may or may not be alive; accept 200 or 404
	if res.StatusCode != 200 && res.StatusCode != 404 {
		t.Fatalf("adopt status %d", res.StatusCode)
	}

	missing := `{"operator":"t"}`
	res, err = http.Post(srv+"/v1/processes/p1/start", "application/json", strings.NewReader(missing))
	if err != nil || res.StatusCode != 400 {
		t.Fatalf("missing op id want 400 got %v %v", err, statusOf(res))
	}
}

func TestLocalHTTP_ReadyzDegraded(t *testing.T) {
	srv := startTestAgentDegraded(t)
	res, err := http.Get(srv + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 503 || strings.TrimSpace(string(body)) != "DEGRADED" {
		t.Fatalf("got %d %q", res.StatusCode, body)
	}
	start := `{"operation_id":"op-s","operator":"t"}`
	res, err = http.Post(srv+"/v1/processes/p1/start", "application/json", strings.NewReader(start))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != 503 || strings.TrimSpace(string(body)) != "DEGRADED" {
		t.Fatalf("mutation got %d %q", res.StatusCode, body)
	}
}

func startTestAgent(t *testing.T) string {
	t.Helper()
	return startTestAgentOpt(t, false)
}

func startTestAgentDegraded(t *testing.T) string {
	t.Helper()
	return startTestAgentOpt(t, true)
}

func startTestAgentOpt(t *testing.T, degraded bool) string {
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
	t.Cleanup(func() { _ = s.Close() })
	_, _ = s.RotateBootID(context.Background())
	mgr := process.NewManager(process.Deps{
		Store:   s,
		Layout:  layout,
		ShimBin: "procmesh-shim",
		Now:     time.Now,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServerOpts(mgr, nil, "127.0.0.1:0", degraded, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { _ = srv.Close() })
	time.Sleep(20 * time.Millisecond)
	return "http://" + ln.Addr().String()
}

func statusOf(res *http.Response) int {
	if res == nil {
		return 0
	}
	return res.StatusCode
}
