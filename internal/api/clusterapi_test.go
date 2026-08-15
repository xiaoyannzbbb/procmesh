package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/version"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var errMeshJoin = errors.New("mesh join failed")

type staticMesh struct {
	mu      sync.Mutex
	members []cluster.NodeSummary
	joins   [][]string
	joinErr error
}

func (m *staticMesh) Members() []cluster.NodeSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]cluster.NodeSummary, len(m.members))
	copy(out, m.members)
	return out
}

func (m *staticMesh) Join(seeds []string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(seeds))
	copy(cp, seeds)
	m.joins = append(m.joins, cp)
	if m.joinErr != nil {
		return 0, m.joinErr
	}
	return len(seeds), nil
}

func (m *staticMesh) setMembers(ms []cluster.NodeSummary) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.members = append([]cluster.NodeSummary(nil), ms...)
}

type clusterEnv struct {
	dir     string
	store   *store.Store
	local   cluster.NodeSummary
	mesh    *staticMesh
	cluster procmeshv1connect.ClusterServiceClient
	node    procmeshv1connect.NodeServiceClient
	http    *http.Client
	url     string
	now     time.Time
	nodeID  string
	bootID  string
}

func newClusterEnv(t *testing.T) *clusterEnv {
	t.Helper()
	return newClusterEnvReady(t, false, true, nil)
}

func newClusterEnvOpts(t *testing.T, degraded, withMesh bool) *clusterEnv {
	t.Helper()
	return newClusterEnvReady(t, degraded, withMesh, nil)
}

func newClusterEnvReady(t *testing.T, degraded, withMesh bool, onReady func() error) *clusterEnv {
	t.Helper()
	m, st, layout := newTestManager(t)
	ctx := context.Background()
	nodeID, err := st.GetOrCreateNodeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bootID, err := st.GetBootID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	local := cluster.NodeSummary{
		NodeID:          nodeID,
		Hostname:        "seed-host",
		BootID:          bootID,
		State:           cluster.StateAlive,
		ProtocolVersion: version.Protocol,
		APIAddress:      "127.0.0.1:9000",
		GossipAddress:   "127.0.0.1:7946",
	}
	env := &clusterEnv{
		dir:    layout.ClusterDir,
		store:  st,
		local:  local,
		now:    now,
		nodeID: nodeID,
		bootID: bootID,
	}
	if withMesh {
		env.mesh = &staticMesh{members: []cluster.NodeSummary{local}}
	}
	deps := ClusterDeps{
		Dir:   layout.ClusterDir,
		Store: st,
		Local: func() cluster.NodeSummary { return env.local },
		GossipAddr: func() string {
			return env.local.GossipAddress
		},
		Now:      func() time.Time { return env.now },
		NodeID:   nodeID,
		Hostname: local.Hostname,
		BootID:   bootID,
		APIAddr:  local.APIAddress,
		OnReady:  onReady,
	}
	if env.mesh != nil {
		deps.Mesh = env.mesh
	}
	srv, err := NewServer(Options{Mgr: m, Store: st, Cluster: deps, Degraded: degraded})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)
	env.cluster = procmeshv1connect.NewClusterServiceClient(hs.Client(), hs.URL)
	env.node = procmeshv1connect.NewNodeServiceClient(hs.Client(), hs.URL)
	env.http = hs.Client()
	env.url = hs.URL
	return env
}

func (e *clusterEnv) init(t *testing.T) *procmeshv1.InitClusterResponse {
	t.Helper()
	resp, err := e.cluster.Init(context.Background(), connect.NewRequest(&procmeshv1.InitClusterRequest{
		Meta:          &procmeshv1.MutationMeta{OperationId: "op-init", Operator: "t"},
		AdminUsername: "admin",
	}))
	if err != nil {
		t.Fatal(err)
	}
	e.local.ClusterID = resp.Msg.GetClusterId()
	if e.mesh != nil {
		e.mesh.setMembers([]cluster.NodeSummary{e.local})
	}
	return resp.Msg
}

func TestInit_OnReadyAfterCerts(t *testing.T) {
	var dir string
	var called, hadCerts bool
	e := newClusterEnvReady(t, false, true, func() error {
		called = true
		_, err := os.Stat(filepath.Join(dir, "agent.crt"))
		hadCerts = err == nil
		return errors.New("rpc listen failed")
	})
	dir = e.dir
	got := e.init(t)
	if got.GetClusterId() == "" {
		t.Fatal("init should succeed even if OnReady fails")
	}
	if !called {
		t.Fatal("OnReady not called after Init")
	}
	if !hadCerts {
		t.Fatal("OnReady ran before certs were written")
	}
	if !control.AlreadyInited(e.dir) {
		t.Fatal("OnReady error must not roll back Init")
	}
}

func TestRequestJoin_OnReadyAfterCerts(t *testing.T) {
	ctx := context.Background()
	seed := newClusterEnv(t)
	inited := seed.init(t)
	tok, err := seed.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	var dir string
	var called, hadCerts bool
	joiner := newClusterEnvReady(t, false, true, func() error {
		called = true
		_, err := os.Stat(filepath.Join(dir, "agent.crt"))
		hadCerts = err == nil
		return errors.New("rpc listen failed")
	})
	dir = joiner.dir
	resp, err := joiner.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin", Operator: "t"},
		SeedServer: seed.url,
		Token:      tok.Msg.GetToken(),
	}))
	if err != nil {
		t.Fatalf("OnReady error should not fail RequestJoin: %v", err)
	}
	if resp.Msg.GetClusterId() != inited.GetClusterId() {
		t.Fatalf("cluster_id=%q want %q", resp.Msg.GetClusterId(), inited.GetClusterId())
	}
	if !called {
		t.Fatal("OnReady not called after RequestJoin")
	}
	if !hadCerts {
		t.Fatal("OnReady ran before joiner certs were written")
	}
	if !control.AlreadyInited(joiner.dir) {
		t.Fatal("OnReady error must not roll back RequestJoin")
	}
}

func TestInit_ReturnsClusterIDAndPasswordThenConflict(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	got := e.init(t)
	if got.GetClusterId() == "" || got.GetAdminPassword() == "" {
		t.Fatalf("init %+v", got)
	}
	if got.GetNodeId() != e.nodeID {
		t.Fatalf("node_id=%q want %q", got.GetNodeId(), e.nodeID)
	}
	id, err := e.store.GetClusterID(ctx)
	if err != nil || id != got.GetClusterId() {
		t.Fatalf("store cluster_id=%q err=%v", id, err)
	}
	meta, err := control.LoadMeta(e.dir)
	if err != nil || !meta.ControlMember || meta.ClusterID != got.GetClusterId() {
		t.Fatalf("meta %+v err=%v", meta, err)
	}

	_, err = e.cluster.Init(ctx, connect.NewRequest(&procmeshv1.InitClusterRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-init-2", Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" {
		t.Fatalf("second init code=%v detail=%s err=%v", code, detail, err)
	}

	// Init must not close loopback unauthenticated access.
	proc := procmeshv1connect.NewProcessServiceClient(e.http, e.url)
	if _, err := proc.ListProcesses(ctx, connect.NewRequest(&procmeshv1.ListProcessesRequest{})); err != nil {
		t.Fatalf("process still unauth after init: %v", err)
	}
}

func TestCreateJoinToken_BeforeInitThenPrefix(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	_, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("before init code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "cluster not initialized") {
		t.Fatalf("want cluster not initialized: %v", err)
	}

	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok-2", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Msg.GetToken(), "pmj_") {
		t.Fatalf("token=%q", tok.Msg.GetToken())
	}
	if tok.Msg.GetTokenId() == "" || tok.Msg.GetUses() != 1 {
		t.Fatalf("token resp %+v", tok.Msg)
	}
	if tok.Msg.GetExpiresUnix() != e.now.Add(time.Hour).Unix() {
		t.Fatalf("expires=%d", tok.Msg.GetExpiresUnix())
	}
}

func TestJoin_SignsCSRVerifyAgent(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	inited := e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	const joinerID = "joiner-node-1"
	csr, _, err := control.NewCSR("join", joinerID)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          joinerID,
		Hostname:        "joiner-host",
		BootId:          "joiner-boot",
		ProtocolVersion: int32(version.Protocol),
		ApiAddress:      "127.0.0.1:9001",
		GossipAddress:   "127.0.0.1:7947",
		CsrPem:          csr,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if joined.Msg.GetClusterId() != inited.GetClusterId() {
		t.Fatalf("cluster_id=%q want %q", joined.Msg.GetClusterId(), inited.GetClusterId())
	}
	if joined.Msg.GetGossipAddress() != e.local.GossipAddress {
		t.Fatalf("gossip=%q", joined.Msg.GetGossipAddress())
	}
	if err := control.VerifyAgent(joined.Msg.GetCaPem(), joined.Msg.GetCertPem(), inited.GetClusterId(), joinerID, e.now); err != nil {
		t.Fatal(err)
	}
	cid, nid, err := control.ParseIDs(joined.Msg.GetCertPem())
	if err != nil {
		t.Fatal(err)
	}
	if cid != inited.GetClusterId() || nid != joinerID {
		t.Fatalf("uri %s/%s", cid, nid)
	}
	seedMeta, err := control.LoadMeta(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(seedMeta.GossipSeeds) != 1 || seedMeta.GossipSeeds[0] != "127.0.0.1:7947" {
		t.Fatalf("seed gossip seeds=%v", seedMeta.GossipSeeds)
	}
}

func TestJoin_BadCSRDoesNotConsumeToken(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-bad", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          "n1",
		BootId:          "boot-n1",
		ProtocolVersion: int32(version.Protocol),
		CsrPem:          []byte("not-a-csr"),
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("bad csr code=%v detail=%s err=%v", code, detail, err)
	}
	csr, _, err := control.NewCSR("join", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-ok", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          "n1",
		BootId:          "boot-n1",
		ProtocolVersion: int32(version.Protocol),
		GossipAddress:   "127.0.0.1:7947",
		CsrPem:          csr,
	})); err != nil {
		t.Fatalf("same token after bad csr: %v", err)
	}
}

func TestJoin_MissingCADoesNotConsumeToken(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	caKey := filepath.Join(e.dir, "ca.key")
	saved, err := os.ReadFile(caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(caKey); err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "n1")
	if err != nil {
		t.Fatal(err)
	}
	joinReq := connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          "n1",
		BootId:          "boot-n1",
		ProtocolVersion: int32(version.Protocol),
		GossipAddress:   "127.0.0.1:7947",
		CsrPem:          csr,
	})
	if _, err := e.cluster.Join(ctx, joinReq); err == nil {
		t.Fatal("join without ca.key succeeded")
	}
	if err := os.WriteFile(caKey, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	joinReq.Msg.Meta.OperationId = "op-join-retry"
	if _, err := e.cluster.Join(ctx, joinReq); err != nil {
		t.Fatalf("same token after restoring ca.key: %v", err)
	}
}

func TestJoin_GossipSeedDeduped(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.init(t)
	const gossip = "127.0.0.1:7947"
	for i, nodeID := range []string{"n1", "n2"} {
		tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
			Meta: &procmeshv1.MutationMeta{OperationId: "op-tok-" + nodeID, Operator: "t"},
		}))
		if err != nil {
			t.Fatal(err)
		}
		csr, _, err := control.NewCSR("join", nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
			Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-" + nodeID, Operator: "t"},
			Token:           tok.Msg.GetToken(),
			NodeId:          nodeID,
			BootId:          "boot-" + nodeID,
			ProtocolVersion: int32(version.Protocol),
			GossipAddress:   gossip,
			CsrPem:          csr,
		})); err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
	}
	meta, err := control.LoadMeta(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.GossipSeeds) != 1 || meta.GossipSeeds[0] != gossip {
		t.Fatalf("seeds=%v", meta.GossipSeeds)
	}
}

func TestJoin_SecondTokenDenied(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	csr1, _, err := control.NewCSR("join", "n1")
	if err != nil {
		t.Fatal(err)
	}
	joinReq := func(nodeID string, csr []byte) *connect.Request[procmeshv1.JoinClusterRequest] {
		return connect.NewRequest(&procmeshv1.JoinClusterRequest{
			Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-" + nodeID, Operator: "t"},
			Token:           tok.Msg.GetToken(),
			NodeId:          nodeID,
			BootId:          "boot-" + nodeID,
			ProtocolVersion: int32(version.Protocol),
			CsrPem:          csr,
		})
	}
	if _, err := e.cluster.Join(ctx, joinReq("n1", csr1)); err != nil {
		t.Fatal(err)
	}
	csr2, _, err := control.NewCSR("join", "n2")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, joinReq("n2", csr2))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("second join code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestJoin_DuplicateNodeID(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	const dupID = "dup-node"
	e.mesh.setMembers([]cluster.NodeSummary{
		e.local,
		{NodeID: dupID, BootID: "boot-1", State: cluster.StateAlive, ProtocolVersion: version.Protocol},
	})
	csr, _, err := control.NewCSR("join", dupID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          dupID,
		BootId:          "boot-2",
		ProtocolVersion: int32(version.Protocol),
		CsrPem:          csr,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeAlreadyExists || detail != "DUPLICATE_NODE_ID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestListNodes_IncludesLocal(t *testing.T) {
	e := newClusterEnv(t)
	e.init(t)
	listed, err := e.node.ListNodes(context.Background(), connect.NewRequest(&procmeshv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range listed.Msg.GetNodes() {
		if n.GetNodeId() == e.nodeID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("local node_id %q missing: %+v", e.nodeID, listed.Msg.GetNodes())
	}
}

func TestInit_MissingOperationID(t *testing.T) {
	e := newClusterEnv(t)
	_, err := e.cluster.Init(context.Background(), connect.NewRequest(&procmeshv1.InitClusterRequest{
		Meta: &procmeshv1.MutationMeta{Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestRequestJoin_JoinerPersistsBundle(t *testing.T) {
	ctx := context.Background()
	seed := newClusterEnv(t)
	inited := seed.init(t)
	tok, err := seed.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	joiner := newClusterEnv(t)
	resp, err := joiner.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin", Operator: "t"},
		SeedServer: seed.url,
		Token:      tok.Msg.GetToken(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetClusterId() != inited.GetClusterId() {
		t.Fatalf("cluster_id=%q want %q", resp.Msg.GetClusterId(), inited.GetClusterId())
	}

	meta, err := control.LoadMeta(joiner.dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ClusterID != inited.GetClusterId() || meta.ControlMember {
		t.Fatalf("joiner meta %+v", meta)
	}
	if len(meta.GossipSeeds) != 1 || meta.GossipSeeds[0] != seed.local.GossipAddress {
		t.Fatalf("joiner gossip seeds=%v want [%q]", meta.GossipSeeds, seed.local.GossipAddress)
	}
	if _, err := os.Stat(filepath.Join(joiner.dir, "ca.key")); !os.IsNotExist(err) {
		t.Fatalf("joiner must not have ca.key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(joiner.dir, "secret")); !os.IsNotExist(err) {
		t.Fatalf("joiner must not have secret: %v", err)
	}
	seedBundle, err := control.LoadBundle(seed.dir)
	if err != nil {
		t.Fatal(err)
	}
	agentCert, err := os.ReadFile(filepath.Join(joiner.dir, "agent.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := control.VerifyAgent(seedBundle.CACertPEM, agentCert, inited.GetClusterId(), joiner.nodeID, joiner.now); err != nil {
		t.Fatal(err)
	}
	stored, err := joiner.store.GetClusterID(ctx)
	if err != nil || stored != inited.GetClusterId() {
		t.Fatalf("joiner store cluster_id=%q err=%v", stored, err)
	}
	joiner.mesh.mu.Lock()
	joins := append([][]string(nil), joiner.mesh.joins...)
	joiner.mesh.mu.Unlock()
	if len(joins) != 1 || len(joins[0]) != 1 || joins[0][0] != seed.local.GossipAddress {
		t.Fatalf("mesh join seeds=%v want [%q]", joins, seed.local.GossipAddress)
	}
	seedMeta, err := control.LoadMeta(seed.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(seedMeta.GossipSeeds) != 1 || seedMeta.GossipSeeds[0] != joiner.local.GossipAddress {
		t.Fatalf("seed gossip seeds=%v want [%q]", seedMeta.GossipSeeds, joiner.local.GossipAddress)
	}
}

func TestRequestJoin_MeshJoinFailureStillSucceeds(t *testing.T) {
	ctx := context.Background()
	seed := newClusterEnv(t)
	inited := seed.init(t)
	tok, err := seed.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	joiner := newClusterEnv(t)
	joiner.mesh.joinErr = errMeshJoin
	resp, err := joiner.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin", Operator: "t"},
		SeedServer: seed.url,
		Token:      tok.Msg.GetToken(),
	}))
	if err != nil {
		t.Fatalf("mesh join failure should not fail RequestJoin: %v", err)
	}
	if resp.Msg.GetClusterId() != inited.GetClusterId() {
		t.Fatalf("cluster_id=%q want %q", resp.Msg.GetClusterId(), inited.GetClusterId())
	}
	if !control.AlreadyInited(joiner.dir) {
		t.Fatal("joiner cluster.json missing after persist")
	}
	_, err = joiner.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin-2", Operator: "t"},
		SeedServer: seed.url,
		Token:      tok.Msg.GetToken(),
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" {
		t.Fatalf("retry code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestRequestJoin_AlreadyInitedConflict(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.init(t)
	_, err := e.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin", Operator: "t"},
		SeedServer: "http://127.0.0.1:9",
		Token:      "pmj_x",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestRequestJoin_SeedUnreachable(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, err = e.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin", Operator: "t"},
		SeedServer: addr,
		Token:      "pmj_x",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestInit_NotConfigured(t *testing.T) {
	m, st, _ := newTestManager(t)
	srv, err := NewServer(Options{Mgr: m, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)
	c := procmeshv1connect.NewClusterServiceClient(hs.Client(), hs.URL)
	_, err = c.Init(context.Background(), connect.NewRequest(&procmeshv1.InitClusterRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-init", Operator: "t"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "cluster not configured") {
		t.Fatalf("want cluster not configured: %v", err)
	}
}
