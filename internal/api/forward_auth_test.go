package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func TestForward_SetsSessionHeaders(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	svc := newTestAuthService(t)
	sid, _, uid, _, err := svc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	fakeCli := &fakeProcessClient{
		restartResp: connect.NewResponse(&procmeshv1.ProcessRefResponse{
			Process: &procmeshv1.ProcessView{
				ProcessId: "nginx-1",
				Spec:      &procmeshv1.ProcessSpec{Name: "nginx"},
			},
		}),
	}
	fwd := &fakeForwarder{proc: fakeCli}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:     m,
		Auth:    svc,
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	}, AuthInterceptor(svc, func() bool { return true }))

	got, err := c.RestartProcess(ctx, bearerReq(sid, &procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-fwd-sess", Operator: "admin"},
		IdOrName: "nginx",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetProcess().GetSpec().GetName() != "nginx" {
		t.Fatalf("view %+v", got.Msg.GetProcess())
	}
	restarts := fakeCli.restartReqs()
	if len(restarts) != 1 {
		t.Fatalf("RestartProcess calls=%d", len(restarts))
	}
	if rpc.UserIDOf(restarts[0].Header()) != uid {
		t.Fatalf("user=%q want %q", rpc.UserIDOf(restarts[0].Header()), uid)
	}
	if rpc.SessionIDOf(restarts[0].Header()) != sid {
		t.Fatalf("session=%q want %q", rpc.SessionIDOf(restarts[0].Header()), sid)
	}
}

func TestOwner_RejectsHopWithoutSession(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	svc := newTestAuthService(t)
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:       m,
		Auth:      svc,
		LocalOnly: true,
		LocalID:   "owner-1",
	}, OwnerAuthInterceptor(svc, "owner-1"))

	_, err := c.RestartProcess(ctx, connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-anon", Operator: "t"},
		IdOrName: "nginx",
	}))
	assertDenied(t, err)
	if !strings.Contains(err.Error(), "missing session") {
		t.Fatalf("want missing session: %v", err)
	}
}

func TestOwner_RechecksViewerRestart(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	svc := newTestAuthService(t)
	putViewerUser(t, svc)
	sid, _, uid, _, err := svc.Login("viewer", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:       m,
		Auth:      svc,
		LocalOnly: true,
		LocalID:   "owner-1",
	}, OwnerAuthInterceptor(svc, "owner-1"))

	req := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-view-restart", Operator: "viewer"},
		IdOrName: "nginx",
	})
	rpc.SetUserID(req.Header(), uid)
	rpc.SetSessionID(req.Header(), sid)
	_, err = c.RestartProcess(ctx, req)
	assertDenied(t, err)
}
