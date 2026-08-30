package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestServer_DebugLogsHTTPAccessWithoutQuery(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, err := NewServer(Options{Started: time.Now(), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz?token=secret", nil)
	req.RemoteAddr = "192.0.2.1:4321"
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["msg"] != "http request" || got["method"] != "GET" || got["path"] != "/healthz" || got["status"] != float64(200) || got["remote_addr"] != "192.0.2.1:4321" {
		t.Fatalf("access log = %#v", got)
	}
	if _, ok := got["duration_ms"].(float64); !ok {
		t.Fatalf("duration_ms = %#v", got["duration_ms"])
	}
	if strings.Contains(out.String(), "secret") {
		t.Fatalf("query leaked: %q", out.String())
	}
}

func TestServer_InfoSuppressesHTTPAccess(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv, err := NewServer(Options{Started: time.Now(), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	srv.Engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if out.Len() != 0 {
		t.Fatalf("INFO access output = %q", out.String())
	}
}

func TestServer_Root(t *testing.T) {
	srv, err := NewServer(Options{Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `<div id="app">`) {
		t.Fatalf("GET / body %q", rec.Body.String())
	}
}

func TestServerMountsDisasterReplicationService(t *testing.T) {
	srv, err := NewServer(Options{Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(srv.Engine)
	t.Cleanup(httpSrv.Close)

	client := procmeshv1connect.NewDisasterReplicationServiceClient(httpSrv.Client(), httpSrv.URL)
	resp, err := client.ListPolicies(context.Background(), connect.NewRequest(&procmeshv1.ListPoliciesRequest{}))
	if err != nil {
		t.Fatalf("ListPolicies through public server: %v", err)
	}
	if len(resp.Msg.GetPolicies()) != 0 {
		t.Fatalf("policies = %+v, want empty", resp.Msg.GetPolicies())
	}
}

func TestServerDisasterReplicationServiceRequiresAuthentication(t *testing.T) {
	_, store, _ := newTestManager(t)
	_, authSvc := newBootstrappedAuth(t)
	if err := store.SetClusterID(context.Background(), "cluster-authenticated"); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Options{
		Started: time.Now(),
		Auth:    authSvc,
		Cluster: ClusterDeps{Store: store},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(srv.Engine)
	t.Cleanup(httpSrv.Close)

	client := procmeshv1connect.NewDisasterReplicationServiceClient(httpSrv.Client(), httpSrv.URL)
	_, err = client.ListPolicies(context.Background(), connect.NewRequest(&procmeshv1.ListPoliciesRequest{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListPolicies without credentials error = %v, want permission denied", err)
	}
}

func TestServerDisasterReplicationUsesCurrentClusterID(t *testing.T) {
	_, store, _ := newTestManager(t)
	srv, err := NewServer(Options{
		Started: time.Now(),
		Cluster: ClusterDeps{Store: store},
		Members: func() []cluster.NodeSummary { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetClusterID(context.Background(), "cluster-after-init"); err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(srv.Engine)
	t.Cleanup(httpSrv.Close)
	client := procmeshv1connect.NewDisasterReplicationServiceClient(httpSrv.Client(), httpSrv.URL)
	resp, err := client.GetTopology(context.Background(), connect.NewRequest(&procmeshv1.GetTopologyRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetClusterId() != "cluster-after-init" {
		t.Fatalf("cluster id = %q, want cluster-after-init", resp.Msg.GetClusterId())
	}
}

func TestServer_Healthz(t *testing.T) {
	srv, err := NewServer(Options{Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz %d %q", rec.Code, rec.Body.String())
	}
}

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

func TestServer_UpdateHealthReportsVersionStoreAndShimRecovery(t *testing.T) {
	m, _, _ := newTestManager(t)
	shimReady := false
	srv, err := NewServer(Options{
		Mgr: m, AgentVersion: "v1.2.1",
		UpdateReady: func() error {
			if !shimReady {
				return errors.New("recovery incomplete")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/updatez", nil))
	var status struct {
		Version              string `json:"version"`
		StoreReady           bool   `json:"store_ready"`
		ShimRecoveryComplete bool   `json:"shim_recovery_complete"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable || status.Version != "v1.2.1" || !status.StoreReady || status.ShimRecoveryComplete {
		t.Fatalf("updatez %d %q", rec.Code, rec.Body.String())
	}

	shimReady = true
	rec = httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/updatez", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || status.Version != "v1.2.1" || !status.StoreReady || !status.ShimRecoveryComplete {
		t.Fatalf("updatez %d %q", rec.Code, rec.Body.String())
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

func TestServer_RootReturnsIndex(t *testing.T) {
	s, err := NewServer(Options{Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	s.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET / status=%d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "ProcMesh") {
		t.Fatalf("GET / body missing 'ProcMesh': %q", body)
	}
	if !strings.Contains(body, "<!doctype html>") {
		t.Fatalf("GET / body missing '<!doctype html>': %q", body)
	}
}

func TestServer_HealthzNotAffectedBySPA(t *testing.T) {
	s, err := NewServer(Options{Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	s.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /healthz status=%d, want 200", w.Code)
	}
	body := w.Body.String()
	if body != "ok" {
		t.Fatalf("GET /healthz body=%q, want 'ok'", body)
	}
	// 应该是纯文本 "ok"，不是 index.html
	if strings.Contains(body, "ProcMesh") || strings.Contains(body, "<div id=\"app\">") {
		t.Fatalf("GET /healthz returned HTML instead of plain text: %q", body)
	}
}

func TestServer_MetricsNotAffectedBySPA(t *testing.T) {
	s, err := NewServer(Options{Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	s.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /metrics status=%d, want 200", w.Code)
	}
	body := w.Body.String()
	// 应该是 Prometheus 格式，不是 index.html
	if strings.Contains(body, "ProcMesh</title>") || strings.Contains(body, "<div id=\"app\">") {
		t.Fatalf("GET /metrics returned HTML instead of metrics: %q", body)
	}
	// Prometheus metrics 应该包含 # HELP 或 # TYPE
	if !strings.Contains(body, "# HELP") && !strings.Contains(body, "# TYPE") {
		t.Fatalf("GET /metrics doesn't look like Prometheus format: %q", body)
	}
}
