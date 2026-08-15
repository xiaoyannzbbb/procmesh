package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestMetrics_ForwardTotal(t *testing.T) {
	m, st, _ := newTestManager(t)
	fakeCli := &fakeProcessClient{
		restartResp: connect.NewResponse(&procmeshv1.ProcessRefResponse{
			Process: &procmeshv1.ProcessView{
				ProcessId: "nginx-1",
				Spec:      &procmeshv1.ProcessSpec{Name: "nginx"},
			},
		}),
	}
	fwd := &fakeForwarder{proc: fakeCli}
	srv, err := NewServer(Options{
		Mgr:     m,
		Store:   st,
		Started: time.Now(),
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	})
	if err != nil {
		t.Fatal(err)
	}

	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)

	client := procmeshv1connect.NewProcessServiceClient(hs.Client(), hs.URL)
	_, err = client.RestartProcess(context.Background(), connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-fwd-metric", Operator: "t"},
		IdOrName: "nginx",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.processCalls() != 1 {
		t.Fatalf("forward Process calls=%d", fwd.processCalls())
	}

	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics %d %q", rec.Code, body)
	}
	if !strings.Contains(body, "procmesh_rpc_forward_total 1") {
		t.Fatalf("want procmesh_rpc_forward_total 1, got:\n%s", body)
	}
	if !strings.Contains(body, "# HELP procmesh_rpc_forward_total Remote owner RPC forward attempts.") {
		t.Fatalf("missing HELP: %q", body)
	}
	if !strings.Contains(body, "# TYPE procmesh_rpc_forward_total counter") {
		t.Fatalf("missing TYPE: %q", body)
	}
}
