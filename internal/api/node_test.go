package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestListNodes_StandaloneLocal(t *testing.T) {
	e := newClusterEnvOpts(t, false, false)
	listed, err := e.node.ListNodes(context.Background(), connect.NewRequest(&procmeshv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetNodes()) != 1 || listed.Msg.GetNodes()[0].GetNodeId() != e.nodeID {
		t.Fatalf("standalone %+v", listed.Msg.GetNodes())
	}
	res := listed.Msg.GetNodes()[0].GetResources()
	if res == nil || res.GetCpuPercent() != -1 || res.GetMemoryPercent() != -1 || res.GetDiskPercent() != -1 {
		t.Fatalf("uncollected resources %+v want -1", res)
	}
}

func TestGetNode_ByIDAndHostname(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	byID, err := e.node.GetNode(ctx, connect.NewRequest(&procmeshv1.GetNodeRequest{IdOrHostname: e.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	if byID.Msg.GetNode().GetNodeId() != e.nodeID || byID.Msg.GetNode().GetHostname() != "seed-host" {
		t.Fatalf("by id %+v", byID.Msg.GetNode())
	}
	byHost, err := e.node.GetNode(ctx, connect.NewRequest(&procmeshv1.GetNodeRequest{IdOrHostname: "seed-host"}))
	if err != nil {
		t.Fatal(err)
	}
	if byHost.Msg.GetNode().GetNodeId() != e.nodeID {
		t.Fatalf("by hostname %+v", byHost.Msg.GetNode())
	}
}

func TestGetNode_NotFound(t *testing.T) {
	e := newClusterEnv(t)
	_, err := e.node.GetNode(context.Background(), connect.NewRequest(&procmeshv1.GetNodeRequest{IdOrHostname: "missing"}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestCreateJoinToken_JoinerDenied(t *testing.T) {
	ctx := context.Background()
	seed := newClusterEnv(t)
	seed.init(t)
	tok, err := seed.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	joiner := newClusterEnv(t)
	if _, err := joiner.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin", Operator: "t"},
		SeedServer: seed.url,
		Token:      tok.Msg.GetToken(),
	})); err != nil {
		t.Fatal(err)
	}
	_, err = joiner.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok-joiner", Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("joiner token code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestCreateJoinToken_NoCAKey(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.init(t)
	if err := os.Remove(filepath.Join(e.dir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	_, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("no ca.key code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestCreateJoinToken_MissingOperationID(t *testing.T) {
	e := newClusterEnv(t)
	e.init(t)
	_, err := e.node.CreateJoinToken(context.Background(), connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestCreateJoinToken_UsesRaftNotFile(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{withMesh: true, control: raftNode})
	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok-raft", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Msg.GetToken(), "pmj_") || tok.Msg.GetTokenId() == "" {
		t.Fatalf("token %+v", tok.Msg)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "tokens.json")); !os.IsNotExist(err) {
		t.Fatal("tokens.json must not be written")
	}
	if _, ok := raftNode.View().JoinTokens[tok.Msg.GetTokenId()]; !ok {
		t.Fatal("token missing from raft")
	}
	if _, err := e.node.RevokeJoinToken(ctx, connect.NewRequest(&procmeshv1.RevokeJoinTokenRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-rev-raft", Operator: "t"},
		TokenId: tok.Msg.GetTokenId(),
	})); err != nil {
		t.Fatal(err)
	}
	if !raftNode.View().JoinTokens[tok.Msg.GetTokenId()].Revoked {
		t.Fatal("expected raft token revoked")
	}
}

func TestRevokeJoinToken(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.node.RevokeJoinToken(ctx, connect.NewRequest(&procmeshv1.RevokeJoinTokenRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-rev", Operator: "t"},
		TokenId: tok.Msg.GetTokenId(),
	})); err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "n1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          "n1",
		BootId:          "b1",
		ProtocolVersion: 1,
		CsrPem:          csr,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("revoked join code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestCluster_DegradedWritesListOK(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnvOpts(t, true, true)
	_, err := e.cluster.Init(ctx, connect.NewRequest(&procmeshv1.InitClusterRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-init", Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("init code=%v detail=%s err=%v", code, detail, err)
	}
	_, err = e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("token code=%v detail=%s err=%v", code, detail, err)
	}
	_, err = e.node.RevokeJoinToken(ctx, connect.NewRequest(&procmeshv1.RevokeJoinTokenRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-rev", Operator: "t"},
		TokenId: "x",
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("revoke code=%v detail=%s err=%v", code, detail, err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-join", Operator: "t"},
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("join code=%v detail=%s err=%v", code, detail, err)
	}
	_, err = e.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin", Operator: "t"},
		SeedServer: "http://127.0.0.1:9",
		Token:      "pmj_x",
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("request-join code=%v detail=%s err=%v", code, detail, err)
	}
	if _, err := e.node.ListNodes(ctx, connect.NewRequest(&procmeshv1.ListNodesRequest{})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.node.GetNode(ctx, connect.NewRequest(&procmeshv1.GetNodeRequest{IdOrHostname: e.nodeID})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cluster.Overview(ctx, connect.NewRequest(&procmeshv1.ClusterOverviewRequest{})); err != nil {
		t.Fatal(err)
	}
}

func TestOverview_Counts(t *testing.T) {
	e := newClusterEnv(t)
	inited := e.init(t)
	e.mesh.setMembers([]cluster.NodeSummary{
		e.local,
		{NodeID: "other", State: cluster.StateFailed, ProtocolVersion: 1},
	})
	ov, err := e.cluster.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if ov.Msg.GetClusterId() != inited.GetClusterId() {
		t.Fatalf("cluster_id=%q", ov.Msg.GetClusterId())
	}
	if ov.Msg.GetMembers() != 2 || ov.Msg.GetAlive() != 1 {
		t.Fatalf("overview %+v", ov.Msg)
	}
}

func TestListNodes_ZeroClusterUsesLocalIfProvided(t *testing.T) {
	m, st, _ := newTestManager(t)
	ctx := context.Background()
	nodeID, err := st.GetOrCreateNodeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Options{
		Mgr:   m,
		Store: st,
		Cluster: ClusterDeps{
			Local: func() cluster.NodeSummary {
				return cluster.NodeSummary{NodeID: nodeID, Hostname: "solo", State: cluster.StateAlive}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)
	n := procmeshv1connect.NewNodeServiceClient(hs.Client(), hs.URL)
	listed, err := n.ListNodes(ctx, connect.NewRequest(&procmeshv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetNodes()) != 1 || listed.Msg.GetNodes()[0].GetNodeId() != nodeID {
		t.Fatalf("%+v", listed.Msg.GetNodes())
	}
	c := procmeshv1connect.NewClusterServiceClient(hs.Client(), hs.URL)
	_, err = c.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-join", Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("join code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "cluster not configured") {
		t.Fatalf("want cluster not configured: %v", err)
	}
}

func TestNodeAPI_FiltersByAgentGroup(t *testing.T) {
	// 两节点摘要 + 一组只含 node-fin
	// finance operator principal
	// ListNodes 只能看到 node-fin
	// GetNode(node-ads) DENIED 或 NOT_FOUND（锁定：DENIED，避免探测）
	_, svc := newBootstrappedAuth(t)
	now := time.Unix(1_700_000_000, 0)
	applyAuthCmd(t, svc, control.CmdMemberPut, control.MemberPutBody{NodeID: "node-fin"})
	applyAuthCmd(t, svc, control.CmdMemberPut, control.MemberPutBody{NodeID: "node-ads"})
	applyAuthCmd(t, svc, control.CmdGroupPut, control.GroupPutBody{GroupID: "g-fin", Name: "finance", NowUnix: now.Unix()})
	applyAuthCmd(t, svc, control.CmdGroupMemberAdd, control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-fin"})
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: "u-fin", Username: "finop", PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, svc, control.CmdBindPut, control.BindPutBody{
		UserID: "u-fin", RoleID: "operator", Scope: control.ScopeAgentGroup, ScopeID: "g-fin",
	})

	api := &NodeAPI{
		Deps: ClusterDeps{Mesh: &staticMesh{members: []cluster.NodeSummary{
			{
				NodeID: "node-fin", Hostname: "fin-host",
				Processes: []cluster.ProcessSummary{{
					ProcessID: "p-web", Name: "web", Group: "finance",
				}},
			},
			{NodeID: "node-ads", Hostname: "ads-host"},
		}}},
		Auth: svc,
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "u-fin", Username: "finop"})

	listed, err := api.ListNodes(ctx, connect.NewRequest(&procmeshv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetNodes()) != 1 || listed.Msg.GetNodes()[0].GetNodeId() != "node-fin" {
		t.Fatalf("list %+v", listed.Msg.GetNodes())
	}
	got := listed.Msg.GetNodes()[0]
	if len(got.GetAgentGroupIds()) != 1 || got.GetAgentGroupIds()[0] != "g-fin" {
		t.Fatalf("agent_group_ids %+v", got.GetAgentGroupIds())
	}
	if len(got.GetProcesses()) != 1 || got.GetProcesses()[0].GetProcessId() != "p-web" || got.GetProcesses()[0].GetGroup() != "finance" {
		t.Fatalf("process summary %+v", got.GetProcesses())
	}

	_, err = api.GetNode(ctx, connect.NewRequest(&procmeshv1.GetNodeRequest{IdOrHostname: "node-ads"}))
	assertDenied(t, err)

	gotNode, err := api.GetNode(ctx, connect.NewRequest(&procmeshv1.GetNodeRequest{IdOrHostname: "node-fin"}))
	if err != nil {
		t.Fatal(err)
	}
	if gotNode.Msg.GetNode().GetNodeId() != "node-fin" {
		t.Fatalf("get %+v", gotNode.Msg.GetNode())
	}
}
