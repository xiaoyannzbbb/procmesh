package api

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type stubExpand struct {
	targets []batch.Target
	err     error
}

func (s stubExpand) Expand(context.Context, batch.Selector, batch.Type) ([]batch.Target, error) {
	return s.targets, s.err
}

type stubExec struct {
	fn func(ctx context.Context, t batch.Target, typ batch.Type) error
}

func (s stubExec) Execute(ctx context.Context, t batch.Target, typ batch.Type) error {
	if s.fn == nil {
		return nil
	}
	return s.fn(ctx, t, typ)
}

func putOperatorUser(t *testing.T, svc *auth.Service) {
	t.Helper()
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: "user-op", Username: "operator", PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, svc, control.CmdBindPut, control.BindPutBody{
		UserID: "user-op", RoleID: "operator", Scope: control.ScopeCluster,
	})
}

func newBatchEngine(t *testing.T, expand batch.Expander) (*batch.Engine, *store.Store) {
	t.Helper()
	_, st, _ := newTestManager(t)
	seq := 0
	return &batch.Engine{
		DB:          st,
		Expand:      expand,
		Exec:        stubExec{},
		SourceAgent: "n1",
		NewID: func() string {
			seq++
			return "id-" + strconv.Itoa(seq)
		},
	}, st
}

func newBatchAPI(t *testing.T, expand batch.Expander) (*BatchAPI, *batch.Engine, *store.Store, *auth.Service) {
	t.Helper()
	eng, st := newBatchEngine(t, expand)
	_, svc := newBootstrappedAuth(t)
	putOperatorUser(t, svc)
	putViewerUser(t, svc)
	return &BatchAPI{Auth: svc, Engine: eng, Store: st, LocalID: "n1"}, eng, st, svc
}

func operatorCtx() context.Context {
	return WithPrincipal(context.Background(), auth.Principal{UserID: "user-op", Username: "operator", SessionID: "sess-op"})
}

func viewerCtx() context.Context {
	return WithPrincipal(context.Background(), auth.Principal{UserID: "user-view", Username: "viewer", SessionID: "sess-view"})
}

func oneTargetExpand() stubExpand {
	return stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p1", ProcessName: "nginx"}}}
}

func TestBatchAPI_CreateRequiresOperationIDAndPerm(t *testing.T) {
	api, _, _, _ := newBatchAPI(t, oneTargetExpand())

	_, err := api.CreateBatch(operatorCtx(), connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Type:     "RESTART",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("no op: code=%v detail=%s err=%v", code, detail, err)
	}

	_, err = api.CreateBatch(viewerCtx(), connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-view", Operator: "viewer"},
		Type:     "RESTART",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	assertDenied(t, err)

	_, err = api.CreateBatch(operatorCtx(), connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-empty", Operator: "operator"},
		Type:     "RESTART",
		Selector: &procmeshv1.BatchSelector{},
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("empty selector: code=%v detail=%s err=%v", code, detail, err)
	}

	resp, err := api.CreateBatch(operatorCtx(), connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-ok", Operator: "operator"},
		Type:     "RESTART",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	b := resp.Msg.GetBatch()
	if b.GetBatchId() == "" {
		t.Fatal("empty batch_id")
	}
	if b.GetStatus() != string(batch.StatusPending) && b.GetStatus() != string(batch.StatusRunning) {
		t.Fatalf("status=%s", b.GetStatus())
	}
}

func TestBatchAPI_ListIsLocalOnly(t *testing.T) {
	api, _, _, _ := newBatchAPI(t, oneTargetExpand())
	ctx := operatorCtx()

	created, err := api.CreateBatch(ctx, connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-list", Operator: "operator"},
		Type:     "START",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetBatch().GetBatchId()

	listed, err := api.ListBatches(ctx, connect.NewRequest(&procmeshv1.ListBatchesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetBatches()) != 1 || listed.Msg.GetBatches()[0].GetBatchId() != id {
		t.Fatalf("list %+v", listed.Msg.GetBatches())
	}
	if len(listed.Msg.GetBatches()[0].GetTargets()) != 0 {
		t.Fatalf("list must omit targets: %+v", listed.Msg.GetBatches()[0].GetTargets())
	}

	got, err := api.GetBatch(ctx, connect.NewRequest(&procmeshv1.GetBatchRequest{BatchId: id}))
	if err != nil || got.Msg.GetBatch().GetBatchId() != id {
		t.Fatalf("get %+v %v", got, err)
	}
	if len(got.Msg.GetBatch().GetTargets()) != 1 {
		t.Fatalf("get targets %+v", got.Msg.GetBatch().GetTargets())
	}

	_, err = api.GetBatch(ctx, connect.NewRequest(&procmeshv1.GetBatchRequest{BatchId: "missing"}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("missing: code=%v detail=%s err=%v", code, detail, err)
	}

	_, err = api.GetBatch(ctx, connect.NewRequest(&procmeshv1.GetBatchRequest{}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("empty id: code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestBatchAPI_ViewerDeniedReads(t *testing.T) {
	api, _, _, _ := newBatchAPI(t, oneTargetExpand())
	created, err := api.CreateBatch(operatorCtx(), connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-rd", Operator: "operator"},
		Type:     "STOP",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetBatch().GetBatchId()
	ctx := viewerCtx()
	_, err = api.GetBatch(ctx, connect.NewRequest(&procmeshv1.GetBatchRequest{BatchId: id}))
	assertDenied(t, err)
	_, err = api.ListBatches(ctx, connect.NewRequest(&procmeshv1.ListBatchesRequest{}))
	assertDenied(t, err)
	_, err = api.ExportBatch(ctx, connect.NewRequest(&procmeshv1.ExportBatchRequest{BatchId: id}))
	assertDenied(t, err)
}

func TestBatchAPI_AuditOnMutations(t *testing.T) {
	api, _, st, _ := newBatchAPI(t, stubExpand{targets: []batch.Target{
		{NodeID: "n1", ProcessID: "p1", ProcessName: "nginx", Status: batch.TargetFailed},
		{NodeID: "n1", ProcessID: "p2", ProcessName: "web", Status: batch.TargetTimeout},
	}})
	ctx := operatorCtx()

	created, err := api.CreateBatch(ctx, connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-audit-c", Operator: "operator"},
		Type:     "RESTART",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1", "p2"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetBatch().GetBatchId()
	assertAudit(t, st, "batch:"+id, "batch.create", "op-audit-c")

	if _, err := api.RetryFailed(ctx, connect.NewRequest(&procmeshv1.RetryBatchRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-audit-r", Operator: "operator"},
		BatchId: id,
	})); err != nil {
		t.Fatal(err)
	}
	assertAudit(t, st, "batch:"+id, "batch.retry_failed", "op-audit-r")

	if _, err := api.ReplayTimeout(ctx, connect.NewRequest(&procmeshv1.RetryBatchRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-audit-p", Operator: "operator"},
		BatchId: id,
	})); err != nil {
		t.Fatal(err)
	}
	assertAudit(t, st, "batch:"+id, "batch.replay_timeout", "op-audit-p")
}

func TestBatchAPI_RetryReplayRequireOperationID(t *testing.T) {
	api, _, _, _ := newBatchAPI(t, stubExpand{targets: []batch.Target{
		{NodeID: "n1", ProcessID: "p1", Status: batch.TargetFailed},
	}})
	ctx := operatorCtx()
	created, err := api.CreateBatch(ctx, connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-rr", Operator: "operator"},
		Type:     "RESTART",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetBatch().GetBatchId()

	_, err = api.RetryFailed(ctx, connect.NewRequest(&procmeshv1.RetryBatchRequest{BatchId: id}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("retry no op: code=%v detail=%s err=%v", code, detail, err)
	}
	_, err = api.ReplayTimeout(ctx, connect.NewRequest(&procmeshv1.RetryBatchRequest{BatchId: id}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("replay no op: code=%v detail=%s err=%v", code, detail, err)
	}

	_, err = api.RetryFailed(viewerCtx(), connect.NewRequest(&procmeshv1.RetryBatchRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-v", Operator: "viewer"},
		BatchId: id,
	}))
	assertDenied(t, err)
}

func TestBatchAPI_DegradedCreate(t *testing.T) {
	api, _, _, _ := newBatchAPI(t, oneTargetExpand())
	api.Degraded = func() bool { return true }
	_, err := api.CreateBatch(operatorCtx(), connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-deg", Operator: "operator"},
		Type:     "RESTART",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestBatchAPI_ConfigUpdateRequiresConfig(t *testing.T) {
	api, _, _, _ := newBatchAPI(t, oneTargetExpand())
	ctx := operatorCtx()
	_, err := api.CreateBatch(ctx, connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-cu", Operator: "operator"},
		Type:     "CONFIG_UPDATE",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("missing config: code=%v detail=%s err=%v", code, detail, err)
	}

	_, err = api.CreateBatch(ctx, connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-extra", Operator: "operator"},
		Type:     "RESTART",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
		Config:   &procmeshv1.ProcessSpec{Command: "/bin/false"},
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("unexpected config: code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestBatchAPI_ExportJSON(t *testing.T) {
	api, _, _, _ := newBatchAPI(t, oneTargetExpand())
	ctx := operatorCtx()
	created, err := api.CreateBatch(ctx, connect.NewRequest(&procmeshv1.CreateBatchRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-ex", Operator: "operator"},
		Type:     "STOP",
		Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := api.ExportBatch(ctx, connect.NewRequest(&procmeshv1.ExportBatchRequest{
		BatchId: created.Msg.GetBatch().GetBatchId(),
		Format:  "json",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Msg.GetContentType() != "application/json" || !strings.Contains(string(out.Msg.GetContent()), "p1") {
		t.Fatalf("export %+v", out.Msg)
	}
}

func TestOverlayConfig_AppliesNonEmptyExceptIdentity(t *testing.T) {
	owner := batch.OwnerSpec{
		ProcessID:      "p1",
		Name:           "nginx",
		NodeID:         "n1",
		Group:          "web",
		LatestRevision: 3,
		SpecJSON: mustJSON(processSpecJSON{
			ProcessID: "p1", Name: "nginx", OwnerAgentID: "n1", Group: "web",
			Command: "/bin/old", Instances: 1, LatestRevision: 3,
		}),
	}
	payload, rev, err := overlayConfig(owner, &procmeshv1.ProcessSpec{
		ProcessId:      "stolen",
		Name:           "renamed",
		OwnerAgentId:   "other",
		LatestRevision: 99,
		Command:        "/bin/new",
		Group:          "api",
		Instances:      2,
	})
	if err != nil || rev != 3 {
		t.Fatalf("overlay %s %d %v", payload, rev, err)
	}
	var got configUpdatePayload
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatal(err)
	}
	if got.ExpectedRevision != 3 || got.Spec == nil {
		t.Fatalf("payload %+v", got)
	}
	if got.Spec.GetProcessId() != "p1" || got.Spec.GetName() != "nginx" || got.Spec.GetOwnerAgentId() != "n1" {
		t.Fatalf("identity overwritten: %+v", got.Spec)
	}
	if got.Spec.GetLatestRevision() != 3 {
		t.Fatalf("latest_revision=%d", got.Spec.GetLatestRevision())
	}
	if got.Spec.GetCommand() != "/bin/new" || got.Spec.GetGroup() != "api" || got.Spec.GetInstances() != 2 {
		t.Fatalf("overlay fields %+v", got.Spec)
	}
}

func TestBatchExec_RestartStampsTargetAndOp(t *testing.T) {
	fakeCli := &recordingProcessClient{}
	fwd := &fakeForwarder{proc: fakeCli}
	api := &BatchAPI{
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	}
	ex := &batchExecutor{api: api}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-op", Username: "operator", SessionID: "sess-op"})
	err := ex.Execute(ctx, batch.Target{
		OperationID: "op-tgt-1",
		NodeID:      "ccc",
		ProcessID:   "p1",
		ProcessName: "nginx",
	}, batch.TypeRestart)
	if err != nil {
		t.Fatal(err)
	}
	if fwd.processCalls() != 1 {
		t.Fatalf("forward Process calls=%d", fwd.processCalls())
	}
	reqs := fakeCli.restarts()
	if len(reqs) != 1 {
		t.Fatalf("restart calls=%d", len(reqs))
	}
	req := reqs[0]
	if req.Msg.GetMeta().GetOperationId() != "op-tgt-1" {
		t.Fatalf("op=%s", req.Msg.GetMeta().GetOperationId())
	}
	if rpc.TargetOf(req.Header()) != "ccc" {
		t.Fatalf("target=%s", rpc.TargetOf(req.Header()))
	}
	if rpc.SessionIDOf(req.Header()) != "sess-op" {
		t.Fatalf("session=%s", rpc.SessionIDOf(req.Header()))
	}
}

func TestBatchExec_ConfigUpdateUsesPayload(t *testing.T) {
	fakeCli := &fakeConfigClient{}
	fwd := &fakeForwarder{cfg: fakeCli}
	api := &BatchAPI{
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	}
	ex := &batchExecutor{api: api}
	payload, _ := json.Marshal(configUpdatePayload{
		ExpectedRevision: 3,
		Spec:             &procmeshv1.ProcessSpec{Command: "/bin/new", Group: "web"},
	})
	err := ex.Execute(operatorCtx(), batch.Target{
		OperationID:      "op-cfg-1",
		NodeID:           "ccc",
		ProcessID:        "p1",
		ExpectedRevision: 3,
		PayloadJSON:      string(payload),
	}, batch.TypeConfigUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if fwd.configCalls() != 1 {
		t.Fatalf("forward Config calls=%d", fwd.configCalls())
	}
	reqs := fakeCli.updateReqs()
	if len(reqs) != 1 {
		t.Fatalf("updates=%d", len(reqs))
	}
	req := reqs[0]
	if req.Msg.GetExpectedRevision() != 3 || req.Msg.GetSpec().GetCommand() != "/bin/new" {
		t.Fatalf("update %+v", req.Msg)
	}
	if req.Msg.GetMeta().GetOperationId() != "op-cfg-1" {
		t.Fatalf("op=%s", req.Msg.GetMeta().GetOperationId())
	}
	if rpc.TargetOf(req.Header()) != "ccc" {
		t.Fatalf("target=%s", rpc.TargetOf(req.Header()))
	}
}

func TestBatchExec_MapsOwnerConflict(t *testing.T) {
	fakeCli := &fakeConfigClient{err: ToConnect(errcode.E(errcode.CONFLICT, "revision mismatch"))}
	fwd := &fakeForwarder{cfg: fakeCli}
	api := &BatchAPI{
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	}
	ex := &batchExecutor{api: api}
	payload, _ := json.Marshal(configUpdatePayload{
		ExpectedRevision: 3,
		Spec:             &procmeshv1.ProcessSpec{Command: "/bin/new"},
	})
	err := ex.Execute(operatorCtx(), batch.Target{
		OperationID: "op-cfg-2", NodeID: "ccc", ProcessID: "p1",
		ExpectedRevision: 3, PayloadJSON: string(payload),
	}, batch.TypeConfigUpdate)
	if batch.MapExecError(err) != batch.TargetConflict {
		t.Fatalf("status=%s err=%v", batch.MapExecError(err), err)
	}
}

func TestBatchAPI_ConcurrentCreateDoesNotCrossOverlays(t *testing.T) {
	mgr, st, _ := newTestManager(t)
	_, svc := newBootstrappedAuth(t)
	putOperatorUser(t, svc)
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: "user-op2", Username: "operator2", PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, svc, control.CmdBindPut, control.BindPutBody{
		UserID: "user-op2", RoleID: "operator", Scope: control.ScopeCluster,
	})
	specA, err := mgr.ApplySpec(context.Background(), process.ProcessSpec{
		Name: "proc-a", Command: "/bin/old", OwnerAgentID: "n1",
	}, 0, "op-apply-a", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	specB, err := mgr.ApplySpec(context.Background(), process.ProcessSpec{
		Name: "proc-b", Command: "/bin/old", OwnerAgentID: "n1",
	}, 0, "op-apply-b", "t", "")
	if err != nil {
		t.Fatal(err)
	}

	var seq atomic.Int64
	eng := &batch.Engine{
		DB: st, SourceAgent: "n1",
		NewID: func() string { return "id-" + strconv.FormatInt(seq.Add(1), 10) },
	}
	membersHit := make(chan struct{})
	releaseMembers := make(chan struct{})
	var hitOnce sync.Once
	api := &BatchAPI{
		Auth: svc, Engine: eng, Store: st, LocalID: "n1", Mgr: mgr,
		Members: func() []cluster.NodeSummary {
			hitOnce.Do(func() { close(membersHit) })
			<-releaseMembers
			return []cluster.NodeSummary{{
				NodeID: "n1",
				Processes: []cluster.ProcessSummary{
					{ProcessID: specA.ProcessID, Name: specA.Name, LatestRevision: specA.LatestRevision},
					{ProcessID: specB.ProcessID, Name: specB.Name, LatestRevision: specB.LatestRevision},
				},
			}}
		},
	}

	ctxA := WithPrincipal(context.Background(), auth.Principal{UserID: "user-op", Username: "operator", SessionID: "sess-a"})
	ctxB := WithPrincipal(context.Background(), auth.Principal{UserID: "user-op2", Username: "operator2", SessionID: "sess-b"})

	type result struct {
		b   *procmeshv1.Batch
		err error
	}
	chA := make(chan result, 1)
	chB := make(chan result, 1)
	go func() {
		resp, err := api.CreateBatch(ctxA, connect.NewRequest(&procmeshv1.CreateBatchRequest{
			Meta:     &procmeshv1.MutationMeta{OperationId: "op-cu-a", Operator: "operator"},
			Type:     "CONFIG_UPDATE",
			Selector: &procmeshv1.BatchSelector{ProcessIds: []string{specA.ProcessID}},
			Config:   &procmeshv1.ProcessSpec{Command: "/bin/alpha"},
		}))
		if err != nil {
			chA <- result{err: err}
			return
		}
		chA <- result{b: resp.Msg.GetBatch()}
	}()
	select {
	case <-membersHit:
	case <-time.After(2 * time.Second):
		t.Fatal("first expand did not hit Members")
	}
	go func() {
		resp, err := api.CreateBatch(ctxB, connect.NewRequest(&procmeshv1.CreateBatchRequest{
			Meta:     &procmeshv1.MutationMeta{OperationId: "op-cu-b", Operator: "operator2"},
			Type:     "CONFIG_UPDATE",
			Selector: &procmeshv1.BatchSelector{ProcessIds: []string{specB.ProcessID}},
			Config:   &procmeshv1.ProcessSpec{Command: "/bin/bravo"},
		}))
		if err != nil {
			chB <- result{err: err}
			return
		}
		chB <- result{b: resp.Msg.GetBatch()}
	}()
	time.Sleep(30 * time.Millisecond)
	close(releaseMembers)

	ra := <-chA
	rb := <-chB
	if ra.err != nil {
		t.Fatalf("create A: %v", ra.err)
	}
	if rb.err != nil {
		t.Fatalf("create B: %v", rb.err)
	}
	if eng.Expand != nil {
		t.Fatal("request-scoped expander must not stay on Engine.Expand")
	}
	if len(ra.b.GetTargets()) != 1 || len(rb.b.GetTargets()) != 1 {
		t.Fatalf("targets A=%+v B=%+v", ra.b.GetTargets(), rb.b.GetTargets())
	}
	gotA, err := eng.Get(context.Background(), ra.b.GetBatchId())
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := eng.Get(context.Background(), rb.b.GetBatchId())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotA.Targets[0].PayloadJSON, "/bin/alpha") {
		t.Fatalf("A overlay crossed: %s", gotA.Targets[0].PayloadJSON)
	}
	if !strings.Contains(gotB.Targets[0].PayloadJSON, "/bin/bravo") {
		t.Fatalf("B overlay crossed: %s", gotB.Targets[0].PayloadJSON)
	}
	if strings.Contains(gotA.Targets[0].PayloadJSON, "/bin/bravo") || strings.Contains(gotB.Targets[0].PayloadJSON, "/bin/alpha") {
		t.Fatalf("overlays crossed A=%s B=%s", gotA.Targets[0].PayloadJSON, gotB.Targets[0].PayloadJSON)
	}
}

func TestBatchAPI_RemoteExecuteSeesHopIdentityBeforeCreateReturns(t *testing.T) {
	api, eng, _, _ := newBatchAPI(t, oneTargetExpand())
	api.LocalID = "aaa"
	api.Router = remoteOwnerRouter("aaa", "ccc", "nginx")
	saw := make(chan string, 1)
	hold := make(chan struct{})
	fakeCli := &blockingRestartClient{saw: saw, hold: hold}
	api.Forward = &fakeForwarder{proc: fakeCli}
	eng.Expand = stubExpand{targets: []batch.Target{
		{NodeID: "ccc", ProcessID: "p1", ProcessName: "nginx"},
	}}
	eng.Exec = nil
	api.ensureExec()
	eng.Start(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := api.CreateBatch(operatorCtx(), connect.NewRequest(&procmeshv1.CreateBatchRequest{
			Meta:     &procmeshv1.MutationMeta{OperationId: "op-hop", Operator: "operator"},
			Type:     "RESTART",
			Selector: &procmeshv1.BatchSelector{ProcessIds: []string{"p1"}},
		}))
		done <- err
	}()
	var sess string
	select {
	case sess = <-saw:
	case <-time.After(2 * time.Second):
		t.Fatal("remote Execute did not start")
	}
	if sess != "sess-op" {
		t.Fatalf("hop session=%q; identity must be bound before enqueue", sess)
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBatchExec_RemoteNoPrincipalSkipsOwner(t *testing.T) {
	fakeCli := &recordingProcessClient{}
	fwd := &fakeForwarder{proc: fakeCli}
	api := &BatchAPI{
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	}
	ex := &batchExecutor{api: api}
	err := ex.Execute(context.Background(), batch.Target{
		OperationID: "op-noprinc",
		NodeID:      "ccc",
		ProcessID:   "p1",
		ProcessName: "nginx",
	}, batch.TypeRestart)
	if batch.MapExecError(err) != batch.TargetTimeout {
		t.Fatalf("status=%s err=%v", batch.MapExecError(err), err)
	}
	if fwd.processCalls() != 0 {
		t.Fatalf("must not hop to Owner: calls=%d", fwd.processCalls())
	}
	if len(fakeCli.restarts()) != 0 {
		t.Fatalf("must not call Owner Restart: %d", len(fakeCli.restarts()))
	}
}

type processSpecJSON struct {
	ProcessID      string
	Name           string
	OwnerAgentID   string
	Group          string
	Command        string
	Instances      int
	LatestRevision int64
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func assertAudit(t *testing.T, st *store.Store, resource, action, opID string) {
	t.Helper()
	evs, err := st.ListAudit(context.Background(), resource, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Action == action && ev.OperationID == opID && ev.Resource == resource {
			return
		}
	}
	t.Fatalf("missing audit action=%s op=%s resource=%s evs=%+v", action, opID, resource, evs)
}

type blockingRestartClient struct {
	fakeProcessClient
	saw  chan string
	hold chan struct{}
}

func (f *blockingRestartClient) RestartProcess(_ context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	select {
	case f.saw <- rpc.SessionIDOf(req.Header()):
	default:
	}
	if f.hold != nil {
		<-f.hold
	}
	return connect.NewResponse(&procmeshv1.ProcessRefResponse{}), nil
}

type recordingProcessClient struct {
	fakeProcessClient
	restartN []*connect.Request[procmeshv1.ProcessRefRequest]
}

func (f *recordingProcessClient) RestartProcess(_ context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	f.fakeProcessClient.mu.Lock()
	defer f.fakeProcessClient.mu.Unlock()
	f.restartN = append(f.restartN, req)
	return connect.NewResponse(&procmeshv1.ProcessRefResponse{}), nil
}

func (f *recordingProcessClient) restarts() []*connect.Request[procmeshv1.ProcessRefRequest] {
	f.fakeProcessClient.mu.Lock()
	defer f.fakeProcessClient.mu.Unlock()
	out := make([]*connect.Request[procmeshv1.ProcessRefRequest], len(f.restartN))
	copy(out, f.restartN)
	return out
}
