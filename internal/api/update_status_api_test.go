package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/update"
	"github.com/qleelulu/procmesh/internal/version"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type recordingChecker struct {
	mu      sync.Mutex
	refresh []bool
	res     update.Result
	err     error
}

func (s *recordingChecker) CheckLatest(_ context.Context, refresh bool) (update.Result, error) {
	s.mu.Lock()
	s.refresh = append(s.refresh, refresh)
	s.mu.Unlock()
	return s.res, s.err
}

func (s *recordingChecker) refreshes() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bool, len(s.refresh))
	copy(out, s.refresh)
	return out
}

type stubLocalInfo struct {
	info update.LocalInfo
}

func (s stubLocalInfo) LocalInfo() update.LocalInfo { return s.info }

type fakeUpdateClient struct {
	mu        sync.Mutex
	calls     int
	info      *procmeshv1.GetLocalUpdateInfoResponse
	err       error
	byID      map[string]*procmeshv1.GetLocalUpdateInfoResponse
	errID     map[string]error
	last      Route
	mesh      *staticMesh
	now       time.Time
	applySaw  chan string
	applyHold chan struct{}
}

func (f *fakeUpdateClient) CheckLatest(context.Context, *connect.Request[procmeshv1.CheckLatestRequest]) (*connect.Response[procmeshv1.CheckLatestResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unused"))
}

func (f *fakeUpdateClient) GetLocalUpdateInfo(context.Context, *connect.Request[procmeshv1.GetLocalUpdateInfoRequest]) (*connect.Response[procmeshv1.GetLocalUpdateInfoResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	nodeID := f.last.NodeID
	if f.errID != nil {
		if err, ok := f.errID[nodeID]; ok {
			return nil, err
		}
	}
	if f.byID != nil {
		if info, ok := f.byID[nodeID]; ok {
			return connect.NewResponse(info), nil
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.info != nil {
		return connect.NewResponse(f.info), nil
	}
	return connect.NewResponse(&procmeshv1.GetLocalUpdateInfoResponse{
		Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true,
	}), nil
}

func (f *fakeUpdateClient) ListNodeUpdateStatus(context.Context, *connect.Request[procmeshv1.ListNodeUpdateStatusRequest]) (*connect.Response[procmeshv1.ListNodeUpdateStatusResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unused"))
}

func (f *fakeUpdateClient) CreateClusterUpdate(context.Context, *connect.Request[procmeshv1.CreateClusterUpdateRequest]) (*connect.Response[procmeshv1.CreateClusterUpdateResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unused"))
}

func (f *fakeUpdateClient) GetUpdateJob(context.Context, *connect.Request[procmeshv1.GetUpdateJobRequest]) (*connect.Response[procmeshv1.GetUpdateJobResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unused"))
}

func (f *fakeUpdateClient) ListUpdateJobs(context.Context, *connect.Request[procmeshv1.ListUpdateJobsRequest]) (*connect.Response[procmeshv1.ListUpdateJobsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unused"))
}

func (f *fakeUpdateClient) CancelRemaining(context.Context, *connect.Request[procmeshv1.CancelRemainingRequest]) (*connect.Response[procmeshv1.CancelRemainingResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unused"))
}

func (f *fakeUpdateClient) RetryUpdateJob(context.Context, *connect.Request[procmeshv1.RetryUpdateJobRequest]) (*connect.Response[procmeshv1.RetryUpdateJobResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unused"))
}

func (f *fakeUpdateClient) ApplyNode(_ context.Context, req *connect.Request[procmeshv1.ApplyNodeRequest]) (*connect.Response[procmeshv1.ApplyNodeResponse], error) {
	f.mu.Lock()
	f.calls++
	if f.err != nil {
		f.mu.Unlock()
		return nil, f.err
	}
	if f.mesh != nil {
		nodeID := req.Msg.GetNodeId()
		tag := req.Msg.GetPin().GetTag()
		f.mesh.mu.Lock()
		for i := range f.mesh.members {
			if f.mesh.members[i].NodeID == nodeID {
				f.mesh.members[i].AgentVersion = tag
				f.mesh.members[i].State = cluster.StateAlive
				f.mesh.members[i].LastUpdatedUnixMs = f.now.UnixMilli()
			}
		}
		f.mesh.mu.Unlock()
	}
	saw, hold := f.applySaw, f.applyHold
	session := rpc.SessionIDOf(req.Header())
	f.mu.Unlock()
	if saw != nil {
		select {
		case saw <- session:
		default:
		}
	}
	if hold != nil {
		<-hold
	}
	return connect.NewResponse(&procmeshv1.ApplyNodeResponse{}), nil
}

func (f *fakeUpdateClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var _ procmeshv1connect.UpdateServiceClient = (*fakeUpdateClient)(nil)

type boundUpdateClient struct {
	parent *fakeUpdateClient
	nodeID string
}

func (c *boundUpdateClient) CheckLatest(ctx context.Context, req *connect.Request[procmeshv1.CheckLatestRequest]) (*connect.Response[procmeshv1.CheckLatestResponse], error) {
	return c.parent.CheckLatest(ctx, req)
}

func (c *boundUpdateClient) GetLocalUpdateInfo(context.Context, *connect.Request[procmeshv1.GetLocalUpdateInfoRequest]) (*connect.Response[procmeshv1.GetLocalUpdateInfoResponse], error) {
	f := c.parent
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = Route{NodeID: c.nodeID}
	if f.errID != nil {
		if err, ok := f.errID[c.nodeID]; ok {
			return nil, err
		}
	}
	if f.byID != nil {
		if info, ok := f.byID[c.nodeID]; ok {
			return connect.NewResponse(info), nil
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.info != nil {
		return connect.NewResponse(f.info), nil
	}
	return connect.NewResponse(&procmeshv1.GetLocalUpdateInfoResponse{
		Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true,
	}), nil
}

func (c *boundUpdateClient) ListNodeUpdateStatus(ctx context.Context, req *connect.Request[procmeshv1.ListNodeUpdateStatusRequest]) (*connect.Response[procmeshv1.ListNodeUpdateStatusResponse], error) {
	return c.parent.ListNodeUpdateStatus(ctx, req)
}

func (c *boundUpdateClient) CreateClusterUpdate(ctx context.Context, req *connect.Request[procmeshv1.CreateClusterUpdateRequest]) (*connect.Response[procmeshv1.CreateClusterUpdateResponse], error) {
	return c.parent.CreateClusterUpdate(ctx, req)
}

func (c *boundUpdateClient) GetUpdateJob(ctx context.Context, req *connect.Request[procmeshv1.GetUpdateJobRequest]) (*connect.Response[procmeshv1.GetUpdateJobResponse], error) {
	return c.parent.GetUpdateJob(ctx, req)
}

func (c *boundUpdateClient) ListUpdateJobs(ctx context.Context, req *connect.Request[procmeshv1.ListUpdateJobsRequest]) (*connect.Response[procmeshv1.ListUpdateJobsResponse], error) {
	return c.parent.ListUpdateJobs(ctx, req)
}

func (c *boundUpdateClient) CancelRemaining(ctx context.Context, req *connect.Request[procmeshv1.CancelRemainingRequest]) (*connect.Response[procmeshv1.CancelRemainingResponse], error) {
	return c.parent.CancelRemaining(ctx, req)
}

func (c *boundUpdateClient) RetryUpdateJob(ctx context.Context, req *connect.Request[procmeshv1.RetryUpdateJobRequest]) (*connect.Response[procmeshv1.RetryUpdateJobResponse], error) {
	return c.parent.RetryUpdateJob(ctx, req)
}

func (c *boundUpdateClient) ApplyNode(ctx context.Context, req *connect.Request[procmeshv1.ApplyNodeRequest]) (*connect.Response[procmeshv1.ApplyNodeResponse], error) {
	f := c.parent
	f.mu.Lock()
	f.last = Route{NodeID: c.nodeID}
	f.mu.Unlock()
	return f.ApplyNode(ctx, req)
}

var _ procmeshv1connect.UpdateServiceClient = (*boundUpdateClient)(nil)

func pinResult(tag string) update.Result {
	return update.Result{
		Pin: update.Pin{
			Repository: "owner/procmesh",
			Tag:        tag,
			Checksums: map[string]string{
				"linux/amd64": "a", "linux/arm64": "b", "linux/armv7": "c",
			},
		},
		CheckedUnixMs: 1_700_000_000_000,
	}
}

func listNow() time.Time {
	return time.Unix(1_700_000_010, 0)
}

func liveMember(id, host, os, arch, ver string) cluster.NodeSummary {
	now := listNow()
	return cluster.NodeSummary{
		NodeID:            id,
		Hostname:          host,
		State:             cluster.StateAlive,
		OS:                os,
		Arch:              arch,
		AgentVersion:      ver,
		LastUpdatedUnixMs: now.UnixMilli(),
		RPCAddress:        "127.0.0.1:9003",
		ProtocolVersion:   version.Protocol,
	}
}

func updateAPIForMembers(t *testing.T, members []cluster.NodeSummary, local update.LocalInfo, remote procmeshv1connect.UpdateServiceClient) (*UpdateAPI, *recordingChecker, *fakeForwarder) {
	t.Helper()
	checker := &recordingChecker{res: pinResult("v0.2.0")}
	fwd := &fakeForwarder{update: remote}
	localID := "local"
	api := &UpdateAPI{
		Checker: checker,
		Local:   stubLocalInfo{info: local},
		LocalID: localID,
		Cluster: ClusterDeps{
			Now:  listNow,
			Mesh: &staticMesh{members: members},
		},
		Router: &Router{
			LocalID: localID,
			Members: func() []cluster.NodeSummary { return members },
		},
		Forward: fwd,
	}
	return api, checker, fwd
}

func TestUpdateAPI_GetLocalUpdateInfoDeniedWithoutClusterRead(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: "user-noperm", Username: "noperm", PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, svc, control.CmdRolePut, control.RolePutBody{
		ID: "no-cluster", Name: "no-cluster", Perms: []string{auth.PermProcessRead},
	})
	applyAuthCmd(t, svc, control.CmdBindPut, control.BindPutBody{
		UserID: "user-noperm", RoleID: "no-cluster", Scope: control.ScopeCluster,
	})
	api := &UpdateAPI{
		Auth:  svc,
		Local: stubLocalInfo{info: update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true}},
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-noperm", Username: "noperm"})
	_, err := api.GetLocalUpdateInfo(ctx, connect.NewRequest(&procmeshv1.GetLocalUpdateInfoRequest{}))
	assertDenied(t, err)
}

func TestUpdateAPI_ListNodeUpdateStatusDeniedWithoutClusterRead(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: "user-noperm", Username: "noperm", PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, svc, control.CmdRolePut, control.RolePutBody{
		ID: "no-cluster", Name: "no-cluster", Perms: []string{auth.PermProcessRead},
	})
	applyAuthCmd(t, svc, control.CmdBindPut, control.BindPutBody{
		UserID: "user-noperm", RoleID: "no-cluster", Scope: control.ScopeCluster,
	})
	api := &UpdateAPI{Auth: svc, Checker: &recordingChecker{res: pinResult("v0.2.0")}}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-noperm", Username: "noperm"})
	_, err := api.ListNodeUpdateStatus(ctx, connect.NewRequest(&procmeshv1.ListNodeUpdateStatusRequest{}))
	assertDenied(t, err)
}

func TestUpdateAPI_GetLocalUpdateInfoReturnsLocalFields(t *testing.T) {
	api := &UpdateAPI{
		Local: stubLocalInfo{info: update.LocalInfo{
			OS: "linux", Arch: "arm64", Version: "0.1.0", Enabled: true, Busy: true,
		}},
		Router:  remoteOwnerRouter("local", "ccc", "nginx"),
		LocalID: "local",
		Forward: &fakeForwarder{update: &fakeUpdateClient{}},
	}
	got, err := api.GetLocalUpdateInfo(context.Background(), connect.NewRequest(&procmeshv1.GetLocalUpdateInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	msg := got.Msg
	if msg.GetOs() != "linux" || msg.GetArch() != "arm64" || msg.GetVersion() != "0.1.0" || !msg.GetEnabled() || !msg.GetBusy() {
		t.Fatalf("%+v", msg)
	}
	fwd := api.Forward.(*fakeForwarder)
	if fwd.updateCalls() != 0 {
		t.Fatalf("GetLocalUpdateInfo must stay local, hops=%d", fwd.updateCalls())
	}
}

func TestUpdateAPI_ListNodeUpdateStatusSkipReasonsAndEligible(t *testing.T) {
	now := listNow()
	stale := liveMember("stale", "host-stale", "linux", "amd64", "0.1.0")
	stale.LastUpdatedUnixMs = now.Add(-time.Minute).UnixMilli()
	unknown := liveMember("unknown", "host-unknown", "linux", "amd64", "0.1.0")
	unknown.LastUpdatedUnixMs = 0
	failed := liveMember("failed", "host-failed", "linux", "amd64", "0.1.0")
	failed.State = cluster.StateFailed
	suspect := liveMember("suspect", "host-suspect", "linux", "amd64", "0.1.0")
	suspect.State = cluster.StateSuspect
	macos := liveMember("macos", "host-mac", "darwin", "arm64", "0.1.0")
	emptyOS := liveMember("empty-os", "host-empty", "", "", "0.1.0")
	disabled := liveMember("disabled", "host-disabled", "linux", "amd64", "0.1.0")
	busy := liveMember("busy", "host-busy", "linux", "amd64", "0.1.0")
	current := liveMember("current", "host-current", "linux", "amd64", "0.1.0")
	unavail := liveMember("unavail", "host-unavail", "linux", "amd64", "0.1.0")
	timeout := liveMember("timeout", "host-timeout", "linux", "amd64", "0.1.0")
	unimpl := liveMember("unimpl", "host-unimpl", "linux", "amd64", "0.1.0")
	eligible := liveMember("eligible", "host-eligible", "linux", "amd64", "0.1.0")
	local := liveMember("local", "host-local", "linux", "amd64", "0.1.0")

	remote := &fakeUpdateClient{
		byID: map[string]*procmeshv1.GetLocalUpdateInfoResponse{
			"empty-os": {Os: "", Arch: "", Version: "0.1.0", Enabled: true},
			"disabled": {Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: false},
			"busy":     {Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true, Busy: true},
			"current":  {Os: "linux", Arch: "amd64", Version: "v0.2.0", Enabled: true},
			"eligible": {Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
			"unavail":  nil,
			"timeout":  nil,
			"unimpl":   nil,
		},
		errID: map[string]error{
			"unavail": errcodeUnavailable(),
			"timeout": connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded),
			"unimpl":  connect.NewError(connect.CodeUnimplemented, errors.New("procmesh.v1.UpdateService.GetLocalUpdateInfo is not implemented")),
		},
	}
	members := []cluster.NodeSummary{
		stale, unknown, failed, suspect, macos, emptyOS, disabled, busy, current, unavail, timeout, unimpl, eligible, local,
	}
	api, checker, fwd := updateAPIForMembers(t, members, update.LocalInfo{
		OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true,
	}, remote)
	got, err := api.ListNodeUpdateStatus(context.Background(), connect.NewRequest(&procmeshv1.ListNodeUpdateStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetLatestTag() != "v0.2.0" {
		t.Fatalf("latest_tag=%q", got.Msg.GetLatestTag())
	}
	if refreshes := checker.refreshes(); len(refreshes) != 1 || refreshes[0] {
		t.Fatalf("CheckLatest refresh flags=%v want [false]", refreshes)
	}
	byID := map[string]*procmeshv1.NodeUpdateStatus{}
	for _, n := range got.Msg.GetNodes() {
		byID[n.GetNodeId()] = n
	}
	assertSkip := func(id, reason string, eligible bool) {
		t.Helper()
		n := byID[id]
		if n == nil {
			t.Fatalf("missing node %s", id)
		}
		if n.GetSkipReason() != reason || n.GetEligible() != eligible {
			t.Fatalf("%s skip=%q eligible=%v want skip=%q eligible=%v freshness=%s",
				id, n.GetSkipReason(), n.GetEligible(), reason, eligible, n.GetFreshness())
		}
		if n.GetFreshness() == freshness.STALE && n.GetEligible() {
			t.Fatalf("%s STALE must not be eligible", id)
		}
	}
	assertSkip("stale", update.SkipSTALE, false)
	assertSkip("unknown", update.SkipUNKNOWN, false)
	assertSkip("failed", update.SkipFAILED, false)
	assertSkip("suspect", update.SkipSUSPECT, false)
	assertSkip("macos", update.SkipMACOS, false)
	assertSkip("empty-os", "", true)
	assertSkip("disabled", update.SkipDISABLED, false)
	assertSkip("busy", update.SkipBUSY, false)
	if !byID["busy"].GetBusy() {
		t.Fatal("busy node should mark busy")
	}
	assertSkip("current", update.SkipCURRENT, false)
	assertSkip("unavail", update.SkipUNAVAILABLE, false)
	assertSkip("timeout", update.SkipTIMEOUT, false)
	assertSkip("unimpl", update.SkipUNSUPPORTED, false)
	assertSkip("eligible", "", true)
	assertSkip("local", "", true)

	if byID["empty-os"].GetSkipReason() == update.SkipMACOS {
		t.Fatal("empty os/arch must not be treated as macOS")
	}
	if remote.callCount() == 0 {
		t.Fatal("expected remote probes")
	}
	if fwd.updateCalls() == 0 {
		t.Fatal("expected Forwarder.Update for remotes")
	}
	// local LIVE linux uses LocalInfo, not a self-hop
	for _, rt := range fwd.updateRoutes() {
		if rt.NodeID == "local" {
			t.Fatalf("probed local via forwarder: %+v", rt)
		}
	}
	// STALE / UNKNOWN / FAILED / SUSPECT / darwin must not be probed
	probed := map[string]bool{}
	for _, rt := range fwd.updateRoutes() {
		probed[rt.NodeID] = true
	}
	for _, id := range []string{"stale", "unknown", "failed", "suspect", "macos"} {
		if probed[id] {
			t.Fatalf("must not probe %s", id)
		}
	}
}

func TestUpdateAPI_ListNodeUpdateStatus_CheckFailedNotEligible(t *testing.T) {
	members := []cluster.NodeSummary{
		liveMember("local", "host-local", "linux", "amd64", "0.1.0"),
		liveMember("peer", "host-peer", "linux", "amd64", "0.1.0"),
	}
	remote := &fakeUpdateClient{
		info: &procmeshv1.GetLocalUpdateInfoResponse{Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
	}
	api, checker, _ := updateAPIForMembers(t, members, update.LocalInfo{
		OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true,
	}, remote)

	assertNoneEligible := func(t *testing.T, reason string) {
		t.Helper()
		got, err := api.ListNodeUpdateStatus(context.Background(), connect.NewRequest(&procmeshv1.ListNodeUpdateStatusRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Msg.GetNodes()) != 2 {
			t.Fatalf("nodes=%d", len(got.Msg.GetNodes()))
		}
		for _, n := range got.Msg.GetNodes() {
			if n.GetEligible() {
				t.Fatalf("%s eligible=true skip=%q want skip=%s", n.GetNodeId(), n.GetSkipReason(), reason)
			}
			if n.GetSkipReason() != reason {
				t.Fatalf("%s skip=%q want %s", n.GetNodeId(), n.GetSkipReason(), reason)
			}
		}
	}

	checker.res = update.Result{CheckError: true, ErrorMessage: "github down", Pin: update.Pin{Tag: "v0.2.0"}}
	assertNoneEligible(t, update.SkipCHECK_FAILED)

	checker.res = update.Result{}
	checker.err = nil
	assertNoneEligible(t, update.SkipCHECK_FAILED)

	checker.res = update.Result{CheckError: true}
	checker.err = errors.New("update source unavailable")
	assertNoneEligible(t, update.SkipCHECK_FAILED)
}

func TestUpdateAPI_ListNodeUpdateStatusAuthNilAllows(t *testing.T) {
	api := &UpdateAPI{
		Checker: &recordingChecker{res: pinResult("v0.2.0")},
		Local:   stubLocalInfo{info: update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true}},
		LocalID: "local",
		Cluster: ClusterDeps{
			Now: listNow,
			Mesh: &staticMesh{members: []cluster.NodeSummary{
				liveMember("local", "host-local", "linux", "amd64", "0.1.0"),
			}},
		},
	}
	got, err := api.ListNodeUpdateStatus(context.Background(), connect.NewRequest(&procmeshv1.ListNodeUpdateStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.GetNodes()) != 1 || !got.Msg.GetNodes()[0].GetEligible() {
		t.Fatalf("%+v", got.Msg.GetNodes())
	}
}

func errcodeUnavailable() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("owner unreachable"))
}
