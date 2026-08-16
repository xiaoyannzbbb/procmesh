package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/version"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func TestProcessAPI_ProcessGroupScope(t *testing.T) {
	t.Run("local", testProcessAPIProcessGroupScopeLocal)
	t.Run("remoteEntry", testProcessAPIProcessGroupScopeRemote)
	t.Run("ownerHop", testProcessAPIProcessGroupScopeHop)
}

func testProcessAPIProcessGroupScopeLocal(t *testing.T) {
	ctx := context.Background()
	e := newRBACEnv(t)
	putProcessGroupOperator(t, e.svc, "u-fin", "finop", "finance")

	adminSID := e.loginAs(t, "admin", testAdminPass)
	applyNamedProcess(t, e.proc, adminSID, "op-api", "api", "finance")
	applyNamedProcess(t, e.proc, adminSID, "op-ads", "ads", "adsys")

	sid := e.loginAs(t, "finop", testAdminPass)

	listed, err := e.proc.ListProcesses(ctx, bearerReq(sid, &procmeshv1.ListProcessesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if names := processNames(listed.Msg.GetProcesses()); len(names) != 1 || names[0] != "api" {
		t.Fatalf("list %+v", names)
	}

	filtered, err := e.proc.ListProcesses(ctx, bearerReq(sid, &procmeshv1.ListProcessesRequest{Group: "adsys"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Msg.GetProcesses()) != 0 {
		t.Fatalf("list group=adsys want empty, got %+v", processNames(filtered.Msg.GetProcesses()))
	}

	_, err = e.proc.GetProcess(ctx, bearerReq(sid, &procmeshv1.GetProcessRequest{IdOrName: "ads"}))
	assertDenied(t, err)

	_, err = e.proc.RestartProcess(ctx, bearerReq(sid, &procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-rst-ads", Operator: "finop"},
		IdOrName: "ads",
	}))
	assertDenied(t, err)

	got, err := e.proc.GetProcess(ctx, bearerReq(sid, &procmeshv1.GetProcessRequest{IdOrName: "api"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetProcess().GetSpec().GetName() != "api" || got.Msg.GetProcess().GetSpec().GetGroup() != "finance" {
		t.Fatalf("get api %+v", got.Msg.GetProcess())
	}

	restarted, err := e.proc.RestartProcess(ctx, bearerReq(sid, &procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-rst-api", Operator: "finop"},
		IdOrName: "api",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Msg.GetProcess().GetSpec().GetName() != "api" {
		t.Fatalf("restart api %+v", restarted.Msg.GetProcess())
	}
}

func testProcessAPIProcessGroupScopeRemote(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	svc := newTestAuthService(t)
	putProcessGroupOperator(t, svc, "u-fin", "finop", "finance")
	sid, _, _, _, err := svc.Login("finop", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	fakeCli := &fakeProcessClient{
		restartResp: connect.NewResponse(&procmeshv1.ProcessRefResponse{
			Process: &procmeshv1.ProcessView{Spec: &procmeshv1.ProcessSpec{Name: "api", Group: "finance"}},
		}),
	}
	fwd := &fakeForwarder{proc: fakeCli}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:     m,
		Auth:    svc,
		LocalID: "aaa",
		Router:  routerWithProcessGroups("aaa", "ccc", []cluster.ProcessSummary{
			{Name: "api", Group: "finance"},
			{Name: "ads", Group: "adsys"},
		}),
		Forward: fwd,
	}, AuthInterceptor(svc, func() bool { return true }))

	_, err = c.GetProcess(ctx, bearerReq(sid, &procmeshv1.GetProcessRequest{IdOrName: "ads"}))
	assertDenied(t, err)
	_, err = c.RestartProcess(ctx, bearerReq(sid, &procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-rst-ads", Operator: "finop"},
		IdOrName: "ads",
	}))
	assertDenied(t, err)
	if fwd.processCalls() != 0 {
		t.Fatalf("denied remote must not forward, calls=%d", fwd.processCalls())
	}

	if _, err := c.GetProcess(ctx, bearerReq(sid, &procmeshv1.GetProcessRequest{IdOrName: "api"})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RestartProcess(ctx, bearerReq(sid, &procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-rst-api", Operator: "finop"},
		IdOrName: "api",
	})); err != nil {
		t.Fatal(err)
	}
	if fwd.processCalls() != 2 {
		t.Fatalf("finance remote must forward, calls=%d", fwd.processCalls())
	}
}

func testProcessAPIProcessGroupScopeHop(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	svc := newTestAuthService(t)
	putProcessGroupOperator(t, svc, "u-fin", "finop", "finance")
	if _, err := m.ApplySpec(ctx, process.ProcessSpec{Name: "api", Group: "finance", Command: "/bin/true"}, 0, "op-api", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplySpec(ctx, process.ProcessSpec{Name: "ads", Group: "adsys", Command: "/bin/true"}, 0, "op-ads", "t", ""); err != nil {
		t.Fatal(err)
	}
	sid, _, uid, _, err := svc.Login("finop", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:       m,
		Auth:      svc,
		LocalOnly: true,
		LocalID:   "owner-1",
	}, OwnerAuthInterceptor(svc, "owner-1"))

	ads := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-hop-ads", Operator: "finop"},
		IdOrName: "ads",
	})
	rpc.SetUserID(ads.Header(), uid)
	rpc.SetSessionID(ads.Header(), sid)
	_, err = c.RestartProcess(ctx, ads)
	assertDenied(t, err)

	apiReq := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-hop-api", Operator: "finop"},
		IdOrName: "api",
	})
	rpc.SetUserID(apiReq.Header(), uid)
	rpc.SetSessionID(apiReq.Header(), sid)
	got, err := c.RestartProcess(ctx, apiReq)
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetProcess().GetSpec().GetName() != "api" {
		t.Fatalf("hop restart %+v", got.Msg.GetProcess())
	}
}

func putProcessGroupOperator(t *testing.T, svc *auth.Service, userID, username, group string) {
	t.Helper()
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: userID, Username: username, PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, svc, control.CmdBindPut, control.BindPutBody{
		UserID: userID, RoleID: "operator", Scope: control.ScopeProcessGroup, ScopeID: group,
	})
}

func applyNamedProcess(t *testing.T, proc interface {
	ApplyProcess(context.Context, *connect.Request[procmeshv1.ApplyProcessRequest]) (*connect.Response[procmeshv1.ApplyProcessResponse], error)
}, sid, opID, name, group string) {
	t.Helper()
	if _, err := proc.ApplyProcess(context.Background(), bearerReq(sid, &procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: opID, Operator: "admin"},
		Spec: &procmeshv1.ProcessSpec{Name: name, Group: group, Command: "/bin/true"},
	})); err != nil {
		t.Fatal(err)
	}
}

func processNames(views []*procmeshv1.ProcessView) []string {
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.GetSpec().GetName())
	}
	return out
}

func routerWithProcessGroups(localID, ownerID string, procs []cluster.ProcessSummary) *Router {
	return &Router{
		LocalID: localID,
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{
				NodeID: ownerID, Hostname: "host-" + ownerID, State: cluster.StateAlive,
				RPCAddress: "127.0.0.1:9003", ProtocolVersion: version.Protocol,
				Processes: procs,
			}}
		},
		LocalHasName: func(context.Context, string) bool { return false },
	}
}
