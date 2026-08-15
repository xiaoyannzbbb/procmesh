package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestServer_HealthReadyMetrics(t *testing.T) {
	m, st, _ := newTestManager(t)
	srv, err := NewServer(Options{
		Addr:    "127.0.0.1:0",
		Mgr:     m,
		Store:   st,
		Started: time.Now().Add(-2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("readyz %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics %d %q", rec.Code, body)
	}
	if !strings.Contains(body, "procmesh_agent_uptime") {
		t.Fatalf("missing uptime: %q", body)
	}
	if !strings.Contains(body, "procmesh_process_running") {
		t.Fatalf("missing running: %q", body)
	}
	if !strings.Contains(body, "procmesh_cluster_members") {
		t.Fatalf("missing cluster members: %q", body)
	}
	if !strings.Contains(body, "procmesh_cluster_alive_members") {
		t.Fatalf("missing cluster alive members: %q", body)
	}

	bare, err := NewServer(Options{Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	bare.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("bare metrics %d %q", rec.Code, body)
	}
	if !strings.Contains(body, "procmesh_agent_uptime") {
		t.Fatalf("bare missing uptime: %q", body)
	}
	if !strings.Contains(body, "procmesh_process_running 0") {
		t.Fatalf("bare running want 0: %q", body)
	}
}

func TestServer_ReadyDegraded(t *testing.T) {
	m, _, _ := newTestManager(t)
	cases := []struct {
		name string
		opts Options
	}{
		{name: "flag", opts: Options{Mgr: m, Degraded: true}},
		{name: "nil-mgr", opts: Options{}},
		{name: "ready-err", opts: Options{Mgr: m, Ready: func() error { return errors.New("store") }}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, err := NewServer(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "DEGRADED" {
				t.Fatalf("readyz %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestServer_ConnectAndLegacyJSON(t *testing.T) {
	m, st, _ := newTestManager(t)
	srv, err := NewServer(Options{Mgr: m, Store: st, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)

	client := procmeshv1connect.NewProcessServiceClient(hs.Client(), hs.URL)
	listed, err := client.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if listed.Msg == nil {
		t.Fatal("list nil")
	}

	body := `{"operation_id":"op-c","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"true","command":"/bin/sleep","args":["5"],"instances":1}}`
	res, err := http.Post(hs.URL+"/v1/processes", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/processes %d %s", res.StatusCode, got)
	}
}

func TestServer_LegacyMutationRejectedWhenRouterWired(t *testing.T) {
	m, st, _ := newTestManager(t)
	srv, err := NewServer(Options{Mgr: m, Store: st, Router: &Router{LocalID: "n1"}})
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyMutationRejected(t, srv)
	assertLegacyGETAllowed(t, srv)
}

func TestServer_LegacyMutationRejectedWhenClusterInited(t *testing.T) {
	m, st, _ := newTestManager(t)
	if err := st.SetClusterID(context.Background(), "cid"); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Options{Mgr: m, Store: st, Cluster: ClusterDeps{Store: st}})
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyV1Denied(t, srv, http.MethodPost, "/v1/processes")
	assertLegacyV1Denied(t, srv, http.MethodGet, "/v1/processes")
	assertLegacyV1Denied(t, srv, http.MethodGet, "/v1/instances/x")
}

func TestServer_LegacyMutationAllowedWhenRouterWiredUnclustered(t *testing.T) {
	m, st, _ := newTestManager(t)
	srv, err := NewServer(Options{
		Mgr: m, Store: st,
		Router:  &Router{LocalID: "n1"},
		Cluster: ClusterDeps{Store: st, Dir: t.TempDir(), NodeID: "n1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)
	body := `{"operation_id":"op-c","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"true","command":"/bin/sleep","args":["5"],"instances":1}}`
	res, err := http.Post(hs.URL+"/v1/processes", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unclustered POST /v1/processes %d %s", res.StatusCode, got)
	}
}

func assertLegacyMutationRejected(t *testing.T, srv *Server) {
	t.Helper()
	rec := httptest.NewRecorder()
	body := `{"operation_id":"op-c","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"true","command":"/bin/true","instances":1}}`
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/processes", strings.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /v1/processes %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "use connect rpc for remote mutations") {
		t.Fatalf("body %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "UNAVAILABLE") {
		t.Fatalf("missing UNAVAILABLE: %q", rec.Body.String())
	}
}

func assertLegacyGETAllowed(t *testing.T, srv *Server) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/processes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/processes %d %s", rec.Code, rec.Body.String())
	}
}

func assertLegacyV1Denied(t *testing.T, srv *Server, method, path string) {
	t.Helper()
	rec := httptest.NewRecorder()
	var body *strings.Reader
	if method != http.MethodGet && method != http.MethodHead {
		body = strings.NewReader(`{"operation_id":"op-c","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"true","command":"/bin/true","instances":1}}`)
		srv.Engine.ServeHTTP(rec, httptest.NewRequest(method, path, body))
	} else {
		srv.Engine.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("%s %s %d %s", method, path, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "DENIED") {
		t.Fatalf("missing DENIED: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "use connect rpc") {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestServer_ConnectNilMgrReturnsDegraded(t *testing.T) {
	srv, err := NewServer(Options{Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)
	ctx := context.Background()

	proc := procmeshv1connect.NewProcessServiceClient(hs.Client(), hs.URL)
	_, err = proc.ListProcesses(ctx, connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("ListProcesses code=%v detail=%s err=%v", code, detail, err)
	}

	cfg := procmeshv1connect.NewConfigServiceClient(hs.Client(), hs.URL)
	_, err = cfg.GetConfig(ctx, connect.NewRequest(&procmeshv1.GetConfigRequest{IdOrName: "web"}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("GetConfig code=%v detail=%s err=%v", code, detail, err)
	}

	logs := procmeshv1connect.NewLogServiceClient(hs.Client(), hs.URL)
	_, err = logs.TailLogs(ctx, connect.NewRequest(&procmeshv1.TailLogsRequest{IdOrName: "web"}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("TailLogs code=%v detail=%s err=%v", code, detail, err)
	}
}
