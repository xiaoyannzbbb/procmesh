package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/update"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func adminCtx() context.Context {
	return WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin", SessionID: "sess-admin"})
}

type stubNodeApply struct {
	err error
	n   int
}

func (s *stubNodeApply) Apply(context.Context, update.Pin) error {
	s.n++
	return s.err
}

type bumpMeshApplier struct {
	mu   sync.Mutex
	mesh *staticMesh
	now  time.Time
	n    int
}

func (b *bumpMeshApplier) Apply(_ context.Context, nodeID string, pin update.Pin, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	b.mesh.mu.Lock()
	defer b.mesh.mu.Unlock()
	for i := range b.mesh.members {
		if b.mesh.members[i].NodeID == nodeID {
			b.mesh.members[i].AgentVersion = pin.Tag
			b.mesh.members[i].State = cluster.StateAlive
			b.mesh.members[i].LastUpdatedUnixMs = b.now.UnixMilli()
		}
	}
	return nil
}

type staticRaftView struct {
	view control.RaftMembershipView
}

func (s staticRaftView) RaftMembershipView() (control.RaftMembershipView, error) {
	return s.view, nil
}

func protoPin(tag string) *procmeshv1.UpdatePin {
	return &procmeshv1.UpdatePin{
		Repository: "owner/procmesh",
		Tag:        tag,
		Checksums:  map[string]string{"linux/amd64": "a", "linux/arm64": "b", "linux/armv7": "c"},
	}
}

func newUpdateJobAPI(t *testing.T, members []cluster.NodeSummary, apply update.NodeApplier) (*UpdateAPI, *store.Store, *recordingChecker, *auth.Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, svc := newBootstrappedAuth(t)
	putViewerUser(t, svc)
	checker := &recordingChecker{res: pinResult("v0.2.0")}
	now := listNow()
	eng := &update.Engine{
		DB:           st,
		Apply:        apply,
		SourceAgent:  "local",
		WaitTimeout:  300 * time.Millisecond,
		PollInterval: 10 * time.Millisecond,
	}
	eng.Start(context.Background())
	api := &UpdateAPI{
		Auth:    svc,
		Checker: checker,
		Local: stubLocalInfo{info: update.LocalInfo{
			OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true,
		}},
		Engine:  eng,
		Store:   st,
		LocalID: "local",
		Cluster: ClusterDeps{
			Now:    func() time.Time { return now },
			Mesh:   &staticMesh{members: members},
			NodeID: "local",
		},
	}
	return api, st, checker, svc
}

func TestUpdateAPI_CreateClusterUpdateRequiresManageAndOperationID(t *testing.T) {
	members := []cluster.NodeSummary{liveMember("local", "host-local", "linux", "amd64", "0.1.0")}
	api, _, _, _ := newUpdateJobAPI(t, members, &bumpMeshApplier{mesh: &staticMesh{members: members}, now: listNow()})

	_, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(&procmeshv1.CreateClusterUpdateRequest{}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("no op: code=%v detail=%s err=%v", code, detail, err)
	}

	_, err = api.CreateClusterUpdate(viewerCtx(), connect.NewRequest(&procmeshv1.CreateClusterUpdateRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-view"},
	}))
	assertDenied(t, err)
}

func TestUpdateAPI_CreateClusterUpdateUsesCachedPinAndAudit(t *testing.T) {
	now := listNow()
	members := []cluster.NodeSummary{
		liveMember("a", "host-a", "linux", "amd64", "0.1.0"),
		liveMember("local", "host-local", "linux", "amd64", "0.1.0"),
		liveMember("leader", "host-leader", "linux", "amd64", "0.1.0"),
		liveMember("mac", "host-mac", "darwin", "arm64", "0.1.0"),
	}
	mesh := &staticMesh{members: members}
	apply := &bumpMeshApplier{mesh: mesh, now: now}
	api, st, checker, _ := newUpdateJobAPI(t, members, apply)
	api.Cluster.Mesh = mesh
	api.Cluster.RaftMembership = staticRaftView{view: control.RaftMembershipView{
		Members: map[string]control.RaftSuffrage{
			"a": control.RaftVoter, "local": control.RaftVoter, "leader": control.RaftVoter,
		},
		LeaderID:  "leader",
		HasQuorum: true,
	}}
	api.Local = stubLocalInfo{info: update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true}}
	api.Router = &Router{LocalID: "local", Members: mesh.Members}
	api.Forward = &fakeForwarder{update: &fakeUpdateClient{
		info: &procmeshv1.GetLocalUpdateInfoResponse{Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
	}}

	resp, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(&procmeshv1.CreateClusterUpdateRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-create", Operator: "admin"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if refreshes := checker.refreshes(); len(refreshes) != 1 || refreshes[0] {
		t.Fatalf("CheckLatest refresh=%v want [false]", refreshes)
	}
	job := resp.Msg.GetJob()
	if job.GetPin().GetTag() != "v0.2.0" {
		t.Fatalf("pin %+v", job.GetPin())
	}
	var order []string
	for _, tg := range job.GetTargets() {
		if tg.GetStatus() == string(update.TargetSkipped) {
			continue
		}
		order = append(order, tg.GetNodeId())
	}
	if len(order) < 3 || order[len(order)-1] != "local" || order[len(order)-2] != "leader" {
		t.Fatalf("order %v", order)
	}
	deadline := time.Now().Add(2 * time.Second)
	var got *procmeshv1.UpdateJob
	for time.Now().Before(deadline) {
		gr, err := api.GetUpdateJob(adminCtx(), connect.NewRequest(&procmeshv1.GetUpdateJobRequest{JobId: job.GetJobId()}))
		if err != nil {
			t.Fatal(err)
		}
		got = gr.Msg.GetJob()
		if got.GetStatus() == string(update.JobCompleted) {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got.GetStatus() != string(update.JobCompleted) {
		t.Fatalf("status=%s targets=%v", got.GetStatus(), got.GetTargets())
	}
	list, err := api.ListUpdateJobs(viewerCtx(), connect.NewRequest(&procmeshv1.ListUpdateJobsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.GetJobs()) != 1 || len(list.Msg.GetJobs()[0].GetTargets()) != 0 {
		t.Fatalf("list local omit targets %+v", list.Msg.GetJobs())
	}
	assertAudit(t, st, "update_job:"+job.GetJobId(), "update.create", "op-create")
}

func TestUpdateAPI_CreateClusterUpdateUsesRequestPin(t *testing.T) {
	members := []cluster.NodeSummary{liveMember("mac", "host-mac", "darwin", "arm64", "0.1.0")}
	api, _, checker, _ := newUpdateJobAPI(t, members, &bumpMeshApplier{mesh: &staticMesh{members: members}, now: listNow()})
	resp, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(&procmeshv1.CreateClusterUpdateRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-pin"},
		Pin:  protoPin("v0.9.0"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetJob().GetPin().GetTag() != "v0.9.0" {
		t.Fatalf("%+v", resp.Msg.GetJob().GetPin())
	}
	if len(checker.refreshes()) != 0 {
		t.Fatalf("request pin must not CheckLatest: %v", checker.refreshes())
	}
}

func TestUpdateAPI_CancelAndRetryRequireManage(t *testing.T) {
	members := []cluster.NodeSummary{liveMember("mac", "host-mac", "darwin", "arm64", "0.1.0")}
	api, st, _, _ := newUpdateJobAPI(t, members, &bumpMeshApplier{mesh: &staticMesh{members: members}, now: listNow()})
	created, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(&procmeshv1.CreateClusterUpdateRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-c"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetJob().GetJobId()
	_, err = api.CancelRemaining(viewerCtx(), connect.NewRequest(&procmeshv1.CancelRemainingRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-cancel"}, JobId: id,
	}))
	assertDenied(t, err)
	_, err = api.RetryUpdateJob(viewerCtx(), connect.NewRequest(&procmeshv1.RetryUpdateJobRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-retry"}, JobId: id,
	}))
	assertDenied(t, err)
	_, err = api.RetryUpdateJob(adminCtx(), connect.NewRequest(&procmeshv1.RetryUpdateJobRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-retry"}, JobId: id,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("nothing to retry: code=%v detail=%s err=%v", code, detail, err)
	}
	_ = st
}

func TestUpdateAPI_ApplyNodeRequiresNodeManageAndAudits(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	putViewerUser(t, svc)
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	applier := &stubNodeApply{}
	api := &UpdateAPI{
		Auth: svc, Applier: applier, Store: st, LocalID: "local",
	}
	_, err = api.ApplyNode(adminCtx(), connect.NewRequest(&procmeshv1.ApplyNodeRequest{
		Pin: protoPin("v0.2.0"),
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("no op: code=%v detail=%s err=%v", code, detail, err)
	}
	_, err = api.ApplyNode(viewerCtx(), connect.NewRequest(&procmeshv1.ApplyNodeRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-view"},
		Pin:  protoPin("v0.2.0"),
	}))
	assertDenied(t, err)

	_, err = api.ApplyNode(adminCtx(), connect.NewRequest(&procmeshv1.ApplyNodeRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-apply"},
		Pin:  protoPin("v0.2.0"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if applier.n != 1 {
		t.Fatalf("apply calls=%d", applier.n)
	}
	assertAuditMeta(t, st, "node:local", "update.apply", "op-apply", map[string]string{
		"tag":        "v0.2.0",
		"repository": "owner/procmesh",
	})
}

func assertAuditMeta(t *testing.T, st *store.Store, resource, action, opID string, want map[string]string) {
	t.Helper()
	evs, err := st.ListAudit(context.Background(), resource, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Action != action || ev.OperationID != opID || ev.Resource != resource {
			continue
		}
		meta := map[string]string{}
		if len(ev.Metadata) > 0 {
			if err := json.Unmarshal(ev.Metadata, &meta); err != nil {
				t.Fatalf("metadata %s: %v", ev.Metadata, err)
			}
		}
		for k, v := range want {
			if meta[k] != v {
				t.Fatalf("metadata %s=%q want %q in %s", k, meta[k], v, ev.Metadata)
			}
		}
		return
	}
	t.Fatalf("missing audit action=%s op=%s resource=%s evs=%+v", action, opID, resource, evs)
}

func TestUpdateAPI_ApplyNodeDisabledIsDenied(t *testing.T) {
	api := &UpdateAPI{
		Applier: &stubNodeApply{err: errcode.E(errcode.DENIED, "updates are disabled")},
		LocalID: "local",
	}
	_, err := api.ApplyNode(context.Background(), connect.NewRequest(&procmeshv1.ApplyNodeRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-den"},
		Pin:  protoPin("v0.2.0"),
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestUpdateAPI_GetUpdateJobNotFound(t *testing.T) {
	members := []cluster.NodeSummary{liveMember("mac", "host-mac", "darwin", "arm64", "0.1.0")}
	api, _, _, _ := newUpdateJobAPI(t, members, &bumpMeshApplier{mesh: &staticMesh{members: members}, now: listNow()})
	_, err := api.GetUpdateJob(adminCtx(), connect.NewRequest(&procmeshv1.GetUpdateJobRequest{JobId: "missing"}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestUpdateAPI_CreateClusterUpdateIdempotentOnOperationID(t *testing.T) {
	members := []cluster.NodeSummary{liveMember("mac", "host-mac", "darwin", "arm64", "0.1.0")}
	api, _, _, _ := newUpdateJobAPI(t, members, &bumpMeshApplier{mesh: &staticMesh{members: members}, now: listNow()})
	req := &procmeshv1.CreateClusterUpdateRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-same"},
		Pin:  protoPin("v0.9.0"),
	}
	first, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	second, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.GetJob().GetJobId() == "" || first.Msg.GetJob().GetJobId() != second.Msg.GetJob().GetJobId() {
		t.Fatalf("first=%s second=%s", first.Msg.GetJob().GetJobId(), second.Msg.GetJob().GetJobId())
	}
	list, err := api.ListUpdateJobs(adminCtx(), connect.NewRequest(&procmeshv1.ListUpdateJobsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.GetJobs()) != 1 {
		t.Fatalf("jobs=%d", len(list.Msg.GetJobs()))
	}
}

func TestUpdateAPI_CreateClusterUpdateStaleLeaderNotLast(t *testing.T) {
	now := listNow()
	leader := liveMember("leader", "aaa-leader", "linux", "amd64", "0.1.0")
	leader.LastUpdatedUnixMs = now.Add(-time.Minute).UnixMilli()
	members := []cluster.NodeSummary{
		liveMember("a", "host-a", "linux", "amd64", "0.1.0"),
		liveMember("local", "host-local", "linux", "amd64", "0.1.0"),
		leader,
	}
	mesh := &staticMesh{members: members}
	apply := &bumpMeshApplier{mesh: mesh, now: now}
	api, _, _, _ := newUpdateJobAPI(t, members, apply)
	api.Cluster.Mesh = mesh
	api.Cluster.RaftMembership = staticRaftView{view: control.RaftMembershipView{
		Members: map[string]control.RaftSuffrage{
			"a": control.RaftVoter, "local": control.RaftVoter, "leader": control.RaftVoter,
		},
		LeaderID:  "leader",
		HasQuorum: true,
	}}
	api.Local = stubLocalInfo{info: update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true}}
	api.Router = &Router{LocalID: "local", Members: mesh.Members}
	api.Forward = &fakeForwarder{update: &fakeUpdateClient{
		info: &procmeshv1.GetLocalUpdateInfoResponse{Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
	}}

	resp, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(&procmeshv1.CreateClusterUpdateRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-stale-leader"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var eligible []string
	for _, tg := range resp.Msg.GetJob().GetTargets() {
		if tg.GetStatus() == string(update.TargetSkipped) {
			continue
		}
		eligible = append(eligible, tg.GetNodeId())
	}
	if len(eligible) < 1 || eligible[len(eligible)-1] == "leader" {
		t.Fatalf("stale leader must not be last: %v", eligible)
	}
	for _, id := range eligible {
		if id == "leader" {
			t.Fatalf("stale leader eligible: %v", eligible)
		}
	}
}

func TestUpdateAPI_CreateClusterUpdateEntryLastWithoutRaftQuorum(t *testing.T) {
	now := listNow()
	members := []cluster.NodeSummary{
		liveMember("a", "host-a", "linux", "amd64", "0.1.0"),
		liveMember("local", "host-local", "linux", "amd64", "0.1.0"),
		liveMember("leader", "host-leader", "linux", "amd64", "0.1.0"),
	}
	mesh := &staticMesh{members: members}
	apply := &bumpMeshApplier{mesh: mesh, now: now}
	api, _, _, _ := newUpdateJobAPI(t, members, apply)
	api.Cluster.Mesh = mesh
	api.Cluster.RaftMembership = staticRaftView{view: control.RaftMembershipView{
		Members: map[string]control.RaftSuffrage{
			"a": control.RaftVoter, "local": control.RaftVoter, "leader": control.RaftVoter,
		},
		LeaderID:  "leader",
		HasQuorum: false,
	}}
	api.Local = stubLocalInfo{info: update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true}}
	api.Router = &Router{LocalID: "local", Members: mesh.Members}
	api.Forward = &fakeForwarder{update: &fakeUpdateClient{
		info: &procmeshv1.GetLocalUpdateInfoResponse{Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
	}}

	resp, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(&procmeshv1.CreateClusterUpdateRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-live-leader"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, tg := range resp.Msg.GetJob().GetTargets() {
		if tg.GetStatus() == string(update.TargetSkipped) {
			continue
		}
		order = append(order, tg.GetNodeId())
	}
	if len(order) < 3 || order[len(order)-1] != "local" || order[len(order)-2] != "leader" {
		t.Fatalf("entry last and LIVE gossip leader second-last, not Raft quorum: %v", order)
	}
}

func TestUpdateAPI_RemoteApplyStampsBoundSession(t *testing.T) {
	now := listNow()
	members := []cluster.NodeSummary{
		liveMember("peer", "host-a", "linux", "amd64", "0.1.0"),
		liveMember("local", "host-local", "linux", "amd64", "0.1.0"),
	}
	mesh := &staticMesh{members: members}
	saw := make(chan string, 1)
	hold := make(chan struct{})
	remote := &fakeUpdateClient{
		info:      &procmeshv1.GetLocalUpdateInfoResponse{Os: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
		mesh:      mesh,
		now:       now,
		applySaw:  saw,
		applyHold: hold,
	}
	api, _, _, _ := newUpdateJobAPI(t, members, nil)
	api.Cluster.Mesh = mesh
	api.Engine.Members = clusterMembership{api.Cluster}
	api.Engine.Apply = nil
	api.Applier = &bumpLocalApplier{mesh: mesh, now: now, localID: "local"}
	api.Local = stubLocalInfo{info: update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true}}
	api.Router = &Router{LocalID: "local", Members: mesh.Members}
	api.Forward = &fakeForwarder{update: remote}
	api.ensureEngine()

	done := make(chan error, 1)
	go func() {
		_, err := api.CreateClusterUpdate(adminCtx(), connect.NewRequest(&procmeshv1.CreateClusterUpdateRequest{
			Meta: &procmeshv1.MutationMeta{OperationId: "op-hop"},
			Pin:  protoPin("v0.2.0"),
		}))
		done <- err
	}()
	var sess string
	select {
	case sess = <-saw:
	case <-time.After(2 * time.Second):
		t.Fatal("remote ApplyNode did not start")
	}
	if sess != "sess-admin" {
		t.Fatalf("hop session=%q; identity must be bound before enqueue", sess)
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type bumpLocalApplier struct {
	mesh    *staticMesh
	now     time.Time
	localID string
}

func (b *bumpLocalApplier) Apply(_ context.Context, pin update.Pin) error {
	if b == nil || b.mesh == nil {
		return nil
	}
	b.mesh.mu.Lock()
	defer b.mesh.mu.Unlock()
	for i := range b.mesh.members {
		if b.mesh.members[i].NodeID == b.localID {
			b.mesh.members[i].AgentVersion = pin.Tag
			b.mesh.members[i].State = cluster.StateAlive
			b.mesh.members[i].LastUpdatedUnixMs = b.now.UnixMilli()
		}
	}
	return nil
}
