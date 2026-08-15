package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type rbacEnv struct {
	*authnEnv
	user procmeshv1connect.UserServiceClient
	node procmeshv1connect.NodeServiceClient
}

func newRBACEnv(t *testing.T) *rbacEnv {
	t.Helper()
	e := newAuthnEnvInit(t, true, true)
	putViewerUser(t, e.svc)
	return &rbacEnv{
		authnEnv: e,
		user:     procmeshv1connect.NewUserServiceClient(e.http, e.url),
		node:     procmeshv1connect.NewNodeServiceClient(e.http, e.url),
	}
}

func putViewerUser(t *testing.T, svc *auth.Service) {
	t.Helper()
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: "user-view", Username: "viewer", PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, svc, control.CmdBindPut, control.BindPutBody{
		UserID: "user-view", RoleID: "viewer", Scope: control.ScopeCluster,
	})
}

func applyAuthCmd(t *testing.T, svc *auth.Service, typ string, body any) {
	t.Helper()
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.Apply(cmd, authApplyTimeout); err != nil {
		t.Fatal(err)
	}
}

func (e *rbacEnv) loginAs(t *testing.T, user, pass string) string {
	t.Helper()
	resp, err := e.authc.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: user,
		Password: pass,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.GetSessionId()
}

func bearerReq[T any](sid string, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+sid)
	return req
}

func TestRBAC_ViewerDeniedWrites(t *testing.T) {
	ctx := context.Background()
	e := newRBACEnv(t)
	sid := e.loginAs(t, "viewer", testAdminPass)

	if _, err := e.proc.ListProcesses(ctx, bearerReq(sid, &procmeshv1.ListProcessesRequest{})); err != nil {
		t.Fatal(err)
	}

	_, err := e.proc.RestartProcess(ctx, bearerReq(sid, &procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-view-restart", Operator: "viewer"},
		IdOrName: "missing",
	}))
	assertDenied(t, err)

	_, err = e.user.CreateUser(ctx, bearerReq(sid, &procmeshv1.CreateUserRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-view-user", Operator: "viewer"},
		Username: "eve",
		Password: "eve-pass-ok",
	}))
	assertDenied(t, err)

	_, err = e.node.CreateJoinToken(ctx, bearerReq(sid, &procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-view-tok", Operator: "viewer"},
	}))
	assertDenied(t, err)
}

func TestRBAC_AdminWritesNotDenied(t *testing.T) {
	ctx := context.Background()
	e := newRBACEnv(t)
	sid := e.loginAs(t, "admin", testAdminPass)

	created, err := e.user.CreateUser(ctx, bearerReq(sid, &procmeshv1.CreateUserRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-admin-user", Operator: "admin"},
		Username: "bob",
		Password: "bob-pass-ok",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if created.Msg.GetUser().GetUsername() != "bob" {
		t.Fatalf("created %+v", created.Msg.GetUser())
	}

	tok, err := e.node.CreateJoinToken(ctx, bearerReq(sid, &procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-admin-tok", Operator: "admin"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Msg.GetToken() == "" {
		t.Fatal("empty join token")
	}

	_, err = e.proc.RestartProcess(ctx, bearerReq(sid, &procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-admin-restart", Operator: "admin"},
		IdOrName: "missing",
	}))
	code, detail := connectDetail(t, err)
	if code == connect.CodePermissionDenied || detail == "DENIED" {
		t.Fatalf("admin restart must not be DENIED: code=%v detail=%s err=%v", code, detail, err)
	}
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("admin restart want NOT_FOUND: code=%v detail=%s err=%v", code, detail, err)
	}
}
