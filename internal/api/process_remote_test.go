package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func TestProcess_RemoteCreateDeniedWithoutLocalCLI(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	c := serveProcessAPI(t, &ProcessAPI{Mgr: m, Process: ProcessRemotePolicy{DisableCreate: true}})

	_, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-web", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	assertDenied(t, err)
	if err == nil || !strings.Contains(err.Error(), "remote process create is disabled") {
		t.Fatalf("err=%v", err)
	}
}

func TestProcess_RemoteCreateAllowsLoopbackBearer(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	c := serveProcessAPI(t, &ProcessAPI{Mgr: m, Process: ProcessRemotePolicy{DisableCreate: true}})

	got, err := c.ApplyProcess(ctx, bearerReq("tok", &procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-cli", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetSpec().GetName() != "web" {
		t.Fatalf("%+v", got.Msg.GetSpec())
	}
}

func TestProcess_RemoteCreateDeniedCookieEvenOnLoopback(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	c := serveProcessAPI(t, &ProcessAPI{Mgr: m, Process: ProcessRemotePolicy{DisableCreate: true}})

	req := connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-cookie", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	})
	req.Header().Set("Cookie", auth.CookieName+"=sid")
	_, err := c.ApplyProcess(ctx, req)
	assertDenied(t, err)
}

func TestProcess_RemoteCreateDeniedOnOwnerHop(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr: m, LocalOnly: true, LocalID: "ccc",
		Process: ProcessRemotePolicy{DisableCreate: true},
	})
	_, err := c.ApplyProcess(ctx, bearerReq("tok", &procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-hop", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	assertDenied(t, err)
}

func TestProcess_RemoteCreateStillForwardsToOwner(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	fakeCli := &fakeProcessClient{
		applyResp: connect.NewResponse(&procmeshv1.ApplyProcessResponse{
			Spec: &procmeshv1.ProcessSpec{Name: "nginx", ProcessId: "nginx-1", OwnerAgentId: "ccc"},
		}),
	}
	fwd := &fakeForwarder{proc: fakeCli}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr: m, LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", ""),
		Forward: fwd,
		Process: ProcessRemotePolicy{DisableCreate: true},
	})
	got, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-fwd", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "nginx", Command: "/bin/true", OwnerAgentId: "ccc"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetSpec().GetOwnerAgentId() != "ccc" {
		t.Fatalf("%+v", got.Msg.GetSpec())
	}
	if fwd.processCalls() != 1 {
		t.Fatalf("forward calls=%d", fwd.processCalls())
	}
}

func TestProcess_StartAllowedWhenRemoteCreateDisabled(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	c := serveProcessAPI(t, &ProcessAPI{Mgr: m, Process: ProcessRemotePolicy{DisableCreate: true}})
	if _, err := c.ApplyProcess(ctx, bearerReq("tok", &procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-c", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "true", Command: "/bin/true"},
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartProcess(ctx, connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-start", Operator: "t"},
		IdOrName: "true",
	})); err != nil {
		t.Fatal(err)
	}
}

func TestProcess_RemoteUpdateAndDelete(t *testing.T) {
	ctx := context.Background()
	m, revs, _ := newTestManager(t)
	proc := serveProcessAPI(t, &ProcessAPI{Mgr: m})
	created, err := proc.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-c", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	rev := created.Msg.GetSpec().GetLatestRevision()

	locked := serveProcessAPI(t, &ProcessAPI{Mgr: m, Process: ProcessRemotePolicy{DisableUpdate: true, DisableDelete: true}})
	_, err = locked.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-up", Operator: "t"},
		ExpectedRevision: rev,
		Spec:             &procmeshv1.ProcessSpec{ProcessId: created.Msg.GetSpec().GetProcessId(), Name: "web", Command: "/bin/false"},
	}))
	assertDenied(t, err)

	cfg := serveConfigAPI(t, &ConfigAPI{Mgr: m, Revs: revs, Process: ProcessRemotePolicy{DisableUpdate: true}})
	_, err = cfg.UpdateConfig(ctx, connect.NewRequest(&procmeshv1.UpdateConfigRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-cfg", Operator: "t"},
		IdOrName:         "web",
		ExpectedRevision: rev,
		Spec:             &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/false"},
	}))
	assertDenied(t, err)

	_, err = cfg.Rollback(ctx, connect.NewRequest(&procmeshv1.RollbackRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-rb", Operator: "t"},
		IdOrName:         "web",
		ToRevision:       1,
		ExpectedRevision: rev,
	}))
	assertDenied(t, err)

	_, err = locked.DeleteProcess(ctx, connect.NewRequest(&procmeshv1.DeleteProcessRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-del", Operator: "t"},
		IdOrName:         "web",
		ExpectedRevision: rev,
	}))
	assertDenied(t, err)

	if _, err := locked.DeleteProcess(ctx, bearerReq("tok", &procmeshv1.DeleteProcessRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-del-cli", Operator: "t"},
		IdOrName:         "web",
		ExpectedRevision: rev,
	})); err != nil {
		t.Fatal(err)
	}
}
