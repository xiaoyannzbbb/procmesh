package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/version"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type clusterBackupHarness struct {
	state *control.State
	apply func(control.Command, time.Duration) error
}

func newClusterBackupClient(t *testing.T, api *ClusterBackupAPI, authn bool) procmeshv1connect.ClusterBackupServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	h, handlers := procmeshv1connect.NewClusterBackupServiceHandler(api, func() []connect.HandlerOption {
		if authn {
			return []connect.HandlerOption{connect.WithInterceptors(AuthInterceptor(api.Auth, func() bool { return true }))}
		}
		return nil
	}()...)
	mux.Handle(h, handlers)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewClusterBackupServiceClient(srv.Client(), srv.URL)
}

func TestClusterBackupAPI_RequiresManagePermission(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	putViewerUser(t, svc)
	sid, _, _, _, err := svc.Login("viewer", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state := control.NewState()
	client := newClusterBackupClient(t, &ClusterBackupAPI{Auth: svc, StateFn: func() control.State { return *state }}, true)
	_, err = client.CreatePolicy(context.Background(), bearerReq(sid, &procmeshv1.CreateClusterBackupPolicyRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-denied"},
		Policy: &procmeshv1.ClusterBackupPolicy{PolicyId: "bp-1", Name: "nightly", Sink: "fs"},
	}))
	assertDenied(t, err)
}

func TestClusterBackupAPI_NonLeaderForwardsMutation(t *testing.T) {
	state := control.NewState()
	fwd := &fakeClusterBackupForwarder{}
	client := newClusterBackupClient(t, &ClusterBackupAPI{
		LocalID: "node-a", StateFn: func() control.State { return *state },
		IsLeader: func() bool { return false }, LeaderRoute: func() (Route, bool) {
			return Route{NodeID: "node-b", RPC: "127.0.0.1:9003"}, true
		}, Forward: fwd,
	}, false)
	_, err := client.DeletePolicy(context.Background(), connect.NewRequest(&procmeshv1.DeleteClusterBackupPolicyRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-forward"}, PolicyId: "bp-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.deleteCalls != 1 {
		t.Fatalf("forward calls=%d", fwd.deleteCalls)
	}
}

func TestClusterBackupAPI_StartRunFreezesTargetsAndRevision(t *testing.T) {
	state := control.NewState()
	state.BackupPolicies["bp-1"] = control.BackupPolicy{PolicyID: "bp-1", Revision: 7, TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-a", "node-b"}}
	state.Members["node-a"] = control.Member{NodeID: "node-a", Status: control.MemberAdmitted}
	state.Members["node-b"] = control.Member{NodeID: "node-b", Status: control.MemberAdmitted}
	client := newClusterBackupClient(t, &ClusterBackupAPI{
		LocalID: "node-a", StateFn: func() control.State { return *state }, IsLeader: func() bool { return true },
		ApplyFn: func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, time.Now()) },
	}, false)
	resp, err := client.StartRun(context.Background(), connect.NewRequest(&procmeshv1.StartClusterBackupRunRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-run"}, PolicyId: "bp-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	run := resp.Msg.GetRun()
	if run.GetPolicyRevision() != 7 || strings.Join(run.GetTargetNodeIds(), ",") != "node-a,node-b" {
		t.Fatalf("run=%+v", run)
	}
}

func TestClusterBackupAPI_StartRunRejectsSelectorTargetMismatch(t *testing.T) {
	state := control.NewState()
	state.Members["node-a"] = control.Member{NodeID: "node-a", Status: control.MemberAdmitted}
	state.Members["node-b"] = control.Member{NodeID: "node-b", Status: control.MemberAdmitted}
	state.BackupPolicies["all"] = control.BackupPolicy{PolicyID: "all", Revision: 1, TargetSelector: "ALL_ADMITTED"}
	client := newClusterBackupClient(t, &ClusterBackupAPI{StateFn: func() control.State { return *state }, IsLeader: func() bool { return true }, ApplyFn: func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, time.Now()) }}, false)
	_, err := client.StartRun(context.Background(), connect.NewRequest(&procmeshv1.StartClusterBackupRunRequest{Meta: &procmeshv1.MutationMeta{OperationId: "op-mismatch"}, PolicyId: "all", TargetNodeIds: []string{"node-a"}}))
	if err == nil || !strings.Contains(err.Error(), "target nodes changed") {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterBackupAPI_LeaderRouteUsesRaftLeaderAddress(t *testing.T) {
	state := control.NewState()
	state.Members["node-b"] = control.Member{NodeID: "node-b", RaftAddr: "raft-b", Status: control.MemberAdmitted}
	state.Members["node-c"] = control.Member{NodeID: "node-c", RaftAddr: "raft-c", Status: control.MemberAdmitted}
	api := &ClusterBackupAPI{LocalID: "node-a", StateFn: func() control.State { return *state }, LeaderAddr: func() string { return "raft-c" }, Router: &Router{LocalID: "node-a", Members: func() []cluster.NodeSummary {
		return []cluster.NodeSummary{{NodeID: "node-b", State: cluster.StateAlive, RPCAddress: "rpc-b", ProtocolVersion: version.Protocol}, {NodeID: "node-c", State: cluster.StateAlive, RPCAddress: "rpc-c", ProtocolVersion: version.Protocol}}
	}}}
	rt, ok := api.leaderRoute()
	if !ok || rt.NodeID != "node-c" || rt.RPC != "rpc-c" {
		t.Fatalf("route=%+v ok=%v", rt, ok)
	}
}

func TestClusterBackupAPI_RetryFailedTasksMutatesRaftState(t *testing.T) {
	state := control.NewState()
	state.Members["node-a"] = control.Member{NodeID: "node-a", Status: control.MemberAdmitted}
	state.BackupPolicies["bp"] = control.BackupPolicy{PolicyID: "bp", Revision: 1, TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-a"}}
	if err := state.CreateRun(control.CreateRunBody{OperationID: "op-run", LeaderTerm: 1, Run: control.ClusterBackupRun{RunID: "run", PolicyID: "bp", PolicyRevision: 1, TargetNodeIDs: []string{"node-a"}, Status: "PARTIAL"}}); err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateTask(control.UpdateTaskBody{OperationID: "op-task", LeaderTerm: 1, Task: control.ClusterBackupTask{RunID: "run", TaskID: "task", NodeID: "node-a", Status: "FAILED", ErrorCode: "E"}}); err != nil {
		t.Fatal(err)
	}
	client := newClusterBackupClient(t, &ClusterBackupAPI{StateFn: func() control.State { return *state }, IsLeader: func() bool { return true }, LeaderTerm: func() uint64 { return 2 }, Now: func() time.Time { return time.Unix(100, 0) }, ApplyFn: func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, time.Unix(100, 0)) }}, false)
	_, err := client.RetryFailedTasks(context.Background(), connect.NewRequest(&procmeshv1.RetryFailedClusterBackupTasksRequest{Meta: &procmeshv1.MutationMeta{OperationId: "op-retry"}, RunId: "run"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := state.BackupTasks["run:task"]; got.Status != "PENDING" {
		t.Fatalf("task=%+v", got)
	}
}

func TestClusterBackupAPI_GetRunAddsUnavailableTasks(t *testing.T) {
	state := control.NewState()
	state.BackupRuns["run-1"] = control.ClusterBackupRun{RunID: "run-1", PolicyID: "bp-1", TargetNodeIDs: []string{"node-a", "node-b"}, Status: "RUNNING"}
	state.BackupTasks["run-1:task-node-a"] = control.ClusterBackupTask{RunID: "run-1", TaskID: "task-node-a", NodeID: "node-a", Status: "SUCCESS"}
	client := newClusterBackupClient(t, &ClusterBackupAPI{StateFn: func() control.State { return *state }}, false)
	resp, err := client.GetRun(context.Background(), connect.NewRequest(&procmeshv1.GetClusterBackupRunRequest{RunId: "run-1"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetRun().GetTasks()) != 2 {
		t.Fatalf("tasks=%d", len(resp.Msg.GetRun().GetTasks()))
	}
	for _, task := range resp.Msg.GetRun().GetTasks() {
		if task.GetNodeId() == "node-b" && task.GetStatus() != "UNAVAILABLE" {
			t.Fatalf("task=%+v", task)
		}
	}
}

func TestClusterBackupAPI_PolicyResponseDoesNotExposeSecret(t *testing.T) {
	state := control.NewState()
	state.BackupPolicies["bp-1"] = control.BackupPolicy{PolicyID: "bp-1", Name: "s3", Sink: "s3", DestinationProfile: "prod"}
	client := newClusterBackupClient(t, &ClusterBackupAPI{StateFn: func() control.State { return *state }}, false)
	resp, err := client.ListPolicies(context.Background(), connect.NewRequest(&procmeshv1.ListClusterBackupPoliciesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Msg.GetPolicies()[0].ProtoReflect().Descriptor().FullName()); got == "" {
		t.Fatal("missing policy")
	}
	if strings.Contains(resp.Msg.String(), "secret_key") || strings.Contains(resp.Msg.String(), "access_key") {
		t.Fatalf("secret leaked: %s", resp.Msg.String())
	}
}

func TestClusterBackupDestinationHealthSchemaExposesEndpointHost(t *testing.T) {
	fields := (&procmeshv1.ClusterBackupDestinationHealth{}).ProtoReflect().Descriptor().Fields()
	if fields.ByName("endpoint_host") == nil {
		t.Fatal("ClusterBackupDestinationHealth.endpoint_host is required")
	}
}

func TestClusterBackupAPI_GetDestinationHealthUsesLocalChecker(t *testing.T) {
	now := time.Unix(123, 0)
	client := newClusterBackupClient(t, &ClusterBackupAPI{
		LocalID: "node-a", Now: func() time.Time { return now },
		DestinationHealth: func(_ context.Context, sink, profile string) backup.DestinationHealth {
			if sink != "s3" || profile != "archive" {
				t.Fatalf("checker input sink=%q profile=%q", sink, profile)
			}
			return backup.DestinationHealth{Sink: sink, DestinationProfile: profile, EndpointHost: "s3.example.com", Status: "AVAILABLE"}
		},
	}, false)

	resp, err := client.GetDestinationHealth(context.Background(), connect.NewRequest(&procmeshv1.GetClusterBackupDestinationHealthRequest{
		Sink: "s3", DestinationProfile: "archive",
	}))
	if err != nil {
		t.Fatal(err)
	}
	health := resp.Msg.GetHealth()
	if health.GetStatus() != "AVAILABLE" || health.GetEndpointHost() != "s3.example.com" || health.GetNodeId() != "node-a" || health.GetCheckedUnix() != now.Unix() {
		t.Fatalf("health = %+v", health)
	}
	if strings.Contains(health.String(), "access_key") || strings.Contains(health.String(), "secret_key") {
		t.Fatalf("credentials leaked: %s", health.String())
	}
}

func TestClusterBackupAPI_PolicyMutationCarriesOperationID(t *testing.T) {
	state := control.NewState()
	var applied control.Command
	client := newClusterBackupClient(t, &ClusterBackupAPI{
		StateFn:  func() control.State { return *state },
		IsLeader: func() bool { return true },
		ApplyFn: func(cmd control.Command, _ time.Duration) error {
			applied = cmd
			return state.Apply(cmd, time.Now())
		},
	}, false)
	_, err := client.CreatePolicy(context.Background(), connect.NewRequest(&procmeshv1.CreateClusterBackupPolicyRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-policy"},
		Policy: &procmeshv1.ClusterBackupPolicy{
			PolicyId: "bp-op", Name: "nightly", Sink: "fs", Timezone: "UTC",
			TargetSelector: "ALL_ADMITTED", UnavailablePolicy: "RECORD_AND_CONTINUE",
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied.Body), `"operation_id":"op-policy"`) {
		t.Fatalf("operation id missing from command body: %s", applied.Body)
	}
}

type fakeClusterBackupForwarder struct {
	deleteCalls int
}

func (f *fakeClusterBackupForwarder) ClusterBackup(context.Context, Route) (procmeshv1connect.ClusterBackupServiceClient, error) {
	return f, nil
}
func (f *fakeClusterBackupForwarder) CreatePolicy(context.Context, *connect.Request[procmeshv1.CreateClusterBackupPolicyRequest]) (*connect.Response[procmeshv1.CreateClusterBackupPolicyResponse], error) {
	return connect.NewResponse(&procmeshv1.CreateClusterBackupPolicyResponse{}), nil
}
func (f *fakeClusterBackupForwarder) UpdatePolicy(context.Context, *connect.Request[procmeshv1.UpdateClusterBackupPolicyRequest]) (*connect.Response[procmeshv1.UpdateClusterBackupPolicyResponse], error) {
	return connect.NewResponse(&procmeshv1.UpdateClusterBackupPolicyResponse{}), nil
}
func (f *fakeClusterBackupForwarder) DeletePolicy(context.Context, *connect.Request[procmeshv1.DeleteClusterBackupPolicyRequest]) (*connect.Response[procmeshv1.DeleteClusterBackupPolicyResponse], error) {
	f.deleteCalls++
	return connect.NewResponse(&procmeshv1.DeleteClusterBackupPolicyResponse{}), nil
}
func (f *fakeClusterBackupForwarder) ListPolicies(context.Context, *connect.Request[procmeshv1.ListClusterBackupPoliciesRequest]) (*connect.Response[procmeshv1.ListClusterBackupPoliciesResponse], error) {
	return connect.NewResponse(&procmeshv1.ListClusterBackupPoliciesResponse{}), nil
}
func (f *fakeClusterBackupForwarder) ValidatePolicy(context.Context, *connect.Request[procmeshv1.ValidateClusterBackupPolicyRequest]) (*connect.Response[procmeshv1.ValidateClusterBackupPolicyResponse], error) {
	return connect.NewResponse(&procmeshv1.ValidateClusterBackupPolicyResponse{}), nil
}
func (f *fakeClusterBackupForwarder) StartRun(context.Context, *connect.Request[procmeshv1.StartClusterBackupRunRequest]) (*connect.Response[procmeshv1.StartClusterBackupRunResponse], error) {
	return connect.NewResponse(&procmeshv1.StartClusterBackupRunResponse{}), nil
}
func (f *fakeClusterBackupForwarder) GetRun(context.Context, *connect.Request[procmeshv1.GetClusterBackupRunRequest]) (*connect.Response[procmeshv1.GetClusterBackupRunResponse], error) {
	return connect.NewResponse(&procmeshv1.GetClusterBackupRunResponse{}), nil
}
func (f *fakeClusterBackupForwarder) ListRuns(context.Context, *connect.Request[procmeshv1.ListClusterBackupRunsRequest]) (*connect.Response[procmeshv1.ListClusterBackupRunsResponse], error) {
	return connect.NewResponse(&procmeshv1.ListClusterBackupRunsResponse{}), nil
}
func (f *fakeClusterBackupForwarder) RetryFailedTasks(context.Context, *connect.Request[procmeshv1.RetryFailedClusterBackupTasksRequest]) (*connect.Response[procmeshv1.RetryFailedClusterBackupTasksResponse], error) {
	return connect.NewResponse(&procmeshv1.RetryFailedClusterBackupTasksResponse{}), nil
}
func (f *fakeClusterBackupForwarder) GetDestinationHealth(context.Context, *connect.Request[procmeshv1.GetClusterBackupDestinationHealthRequest]) (*connect.Response[procmeshv1.GetClusterBackupDestinationHealthResponse], error) {
	return connect.NewResponse(&procmeshv1.GetClusterBackupDestinationHealthResponse{}), nil
}
