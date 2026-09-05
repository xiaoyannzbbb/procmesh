package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/hashicorp/raft"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
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
	return newClusterEnvAuth(t, degraded, withMesh, onReady, nil)
}

func newClusterEnvAuth(t *testing.T, degraded, withMesh bool, onReady func() error, authSvc *auth.Service) *clusterEnv {
	t.Helper()
	return newClusterEnvFull(t, clusterEnvCfg{
		degraded: degraded,
		withMesh: withMesh,
		onReady:  onReady,
		auth:     authSvc,
	})
}

type clusterEnvCfg struct {
	degraded       bool
	withMesh       bool
	initControl    func() error
	onReady        func() error
	auth           *auth.Service
	control        *control.Node
	controlFn      func() *control.Node
	raftMembership control.RaftMembershipReader
	onAdmit        func(nodeID, raftAddr string) error
	leaderAPI      func() string
	raftAddr       func() string
	logger         *slog.Logger
}

func newClusterEnvFull(t *testing.T, cfg clusterEnvCfg) *clusterEnv {
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
		APIAddress:      "127.0.0.1:18680",
		GossipAddress:   "127.0.0.1:18689",
	}
	env := &clusterEnv{
		dir:    layout.ClusterDir,
		store:  st,
		local:  local,
		now:    now,
		nodeID: nodeID,
		bootID: bootID,
	}
	if cfg.withMesh {
		env.mesh = &staticMesh{members: []cluster.NodeSummary{local}}
	}
	deps := ClusterDeps{
		Dir:   layout.ClusterDir,
		Store: st,
		Local: func() cluster.NodeSummary { return env.local },
		GossipAddr: func() string {
			return env.local.GossipAddress
		},
		Now:            func() time.Time { return env.now },
		NodeID:         nodeID,
		Hostname:       local.Hostname,
		BootID:         bootID,
		APIAddr:        local.APIAddress,
		InitControl:    cfg.initControl,
		OnReady:        cfg.onReady,
		Control:        cfg.control,
		ControlFn:      cfg.controlFn,
		RaftMembership: cfg.raftMembership,
		OnAdmit:        cfg.onAdmit,
		LeaderAPI:      cfg.leaderAPI,
		RaftAddr:       cfg.raftAddr,
	}
	if env.mesh != nil {
		deps.Mesh = env.mesh
	}
	srv, err := NewServer(Options{Mgr: m, Store: st, Cluster: deps, Degraded: cfg.degraded, Auth: cfg.auth, Logger: cfg.logger})
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

func startTestRaft(t *testing.T, nodeID string) *control.Node {
	t.Helper()
	_, trans := raft.NewInmemTransport(raft.ServerAddress(nodeID))
	n, err := control.StartInmem(nodeID, control.NewFSM(), trans)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if n.IsLeader() && n.HasQuorum() {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no raft leader")
	return n
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
	var logOut bytes.Buffer
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		logger:   slog.New(slog.NewJSONHandler(&logOut, nil)),
		onReady: func() error {
			called = true
			_, err := os.Stat(filepath.Join(dir, "agent.crt"))
			hadCerts = err == nil
			return errors.New("rpc listen failed")
		},
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
	var record map[string]any
	if err := json.Unmarshal(logOut.Bytes(), &record); err != nil {
		t.Fatalf("cluster warning is not JSON: %v; output=%q", err, logOut.String())
	}
	if record["level"] != "WARN" || record["msg"] != "cluster ready failed" || record["error"] != "rpc listen failed" {
		t.Fatalf("cluster warning = %#v", record)
	}
}

func TestInit_ControlStartupFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	controlNode := startTestRaft(t, "init-control")
	controlReady := false
	startAttempts := 0
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		controlFn: func() *control.Node {
			if controlReady {
				return controlNode
			}
			return nil
		},
		initControl: func() error {
			startAttempts++
			if startAttempts == 1 {
				return errors.New("injected startRaft failure")
			}
			controlReady = true
			return nil
		},
	})
	request := func(operationID string) (*connect.Response[procmeshv1.InitClusterResponse], error) {
		return e.cluster.Init(ctx, connect.NewRequest(&procmeshv1.InitClusterRequest{
			Meta:          &procmeshv1.MutationMeta{OperationId: operationID, Operator: "t"},
			AdminUsername: "admin",
		}))
	}
	_, err := request("op-init-control-failure")
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("init code=%v detail=%s err=%v", code, detail, err)
	}
	clusterID, storeErr := e.store.GetClusterID(ctx)
	if storeErr != nil || clusterID != "" {
		t.Fatalf("cluster id published after failed control startup: id=%q err=%v", clusterID, storeErr)
	}
	if control.AlreadyInited(e.dir) {
		t.Fatal("failed control startup left cluster initialized")
	}
	for _, name := range []string{"cluster.json", "secret", "admin.bootstrap", "ca.crt", "ca.key", "agent.crt", "agent.key"} {
		if _, statErr := os.Stat(filepath.Join(e.dir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("failed control startup left %s: %v", name, statErr)
		}
	}

	retried, err := request("op-init-control-retry")
	if err != nil {
		t.Fatalf("retry init: %v", err)
	}
	if retried.Msg.GetClusterId() == "" || retried.Msg.GetAdminPassword() == "" {
		t.Fatalf("retry did not return usable credentials: %+v", retried.Msg)
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

	// Auth==nil: Init must not close loopback unauthenticated access.
	proc := procmeshv1connect.NewProcessServiceClient(e.http, e.url)
	if _, err := proc.ListProcesses(ctx, connect.NewRequest(&procmeshv1.ListProcessesRequest{})); err != nil {
		t.Fatalf("process still unauth after init: %v", err)
	}
}

func TestInit_ClosesUnauthWhenAuthInjected(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnvAuth(t, false, true, nil, newTestAuthService(t))
	e.init(t)
	proc := procmeshv1connect.NewProcessServiceClient(e.http, e.url)
	_, err := proc.ListProcesses(ctx, connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("unauth after init code=%v detail=%s err=%v", code, detail, err)
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
		ApiAddress:      "127.0.0.1:18683",
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
	_, err = e.cluster.Join(ctx, joinReq)
	if err == nil {
		t.Fatal("join without ca.key succeeded")
	}
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("no ca.key code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "ca key not available") {
		t.Fatalf("want ca key not available: %v", err)
	}
	if err := os.WriteFile(caKey, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	joinReq.Msg.Meta.OperationId = "op-join-retry"
	if _, err := e.cluster.Join(ctx, joinReq); err != nil {
		t.Fatalf("same token after restoring ca.key: %v", err)
	}
}

func TestJoin_FSMMissingCADoesNotConsumeToken(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{withMesh: true, control: raftNode})
	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok-fsm-ca", Operator: "t"},
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
	csr, _, err := control.NewCSR("join", "n-fsm")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-fsm-noca", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          "n-fsm",
		BootId:          "boot-n-fsm",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "n-fsm-raft",
		CsrPem:          csr,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("no ca.key code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "ca key not available") {
		t.Fatalf("want ca key not available: %v", err)
	}
	if err := os.WriteFile(caKey, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-fsm-retry", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          "n-fsm",
		BootId:          "boot-n-fsm",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "n-fsm-raft",
		CsrPem:          csr,
	})); err != nil {
		t.Fatalf("same token after restoring ca.key: %v", err)
	}
}

func TestJoin_ControlUnavailableDoesNotFallBackToFileToken(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh:  true,
		controlFn: func() *control.Node { return nil },
	})
	if _, err := control.Init(e.dir, e.nodeID, "admin", e.now); err != nil {
		t.Fatal(err)
	}
	plain, _, err := control.CreateToken(e.dir, time.Hour, 1, e.now)
	if err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "control-missing-joiner")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-control-missing-join", Operator: "t"},
		Token:           plain,
		NodeId:          "control-missing-joiner",
		BootId:          "control-missing-boot",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "control-missing-raft",
		CsrPem:          csr,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("join code=%v detail=%s err=%v", code, detail, err)
	}
	if err := control.ConsumeToken(e.dir, plain, e.now); err != nil {
		t.Fatalf("file token was consumed while control was unavailable: %v", err)
	}
}

func TestJoin_FSMBadCSRDoesNotConsumeToken(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{withMesh: true, control: raftNode})
	e.init(t)
	tok, err := e.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok-fsm-csr", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-fsm-bad", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          "n-bad",
		BootId:          "boot-n-bad",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "n-bad-raft",
		CsrPem:          []byte("not-a-csr"),
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("bad csr code=%v detail=%s err=%v", code, detail, err)
	}
	csr, _, err := control.NewCSR("join", "n-bad")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-fsm-ok", Operator: "t"},
		Token:           tok.Msg.GetToken(),
		NodeId:          "n-bad",
		BootId:          "boot-n-bad",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "n-bad-raft",
		CsrPem:          csr,
	})); err != nil {
		t.Fatalf("same token after bad csr: %v", err)
	}
}

func TestInit_OnReadyBeforeSetClusterID(t *testing.T) {
	ctx := context.Background()
	var storeRef *store.Store
	var called bool
	e := newClusterEnvReady(t, false, true, func() error {
		called = true
		if storeRef == nil {
			t.Fatal("store not set")
		}
		id, err := storeRef.GetClusterID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if id != "" {
			t.Fatalf("SetClusterID ran before OnReady: %q", id)
		}
		return errors.New("rpc listen failed")
	})
	storeRef = e.store
	got := e.init(t)
	if !called {
		t.Fatal("OnReady not called")
	}
	if got.GetClusterId() == "" {
		t.Fatal("init should succeed even if OnReady fails")
	}
	id, err := e.store.GetClusterID(ctx)
	if err != nil || id != got.GetClusterId() {
		t.Fatalf("cluster id after init=%q err=%v", id, err)
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

func TestJoinErrorRetryable_PreservesPendingUnlessRejectionIsDefinitive(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{name: "request canceled with unknown outcome", err: connect.NewError(connect.CodeCanceled, context.Canceled), retryable: true},
		{name: "transport failure", err: errors.New("connection reset"), retryable: true},
		{name: "admission unavailable", err: ToConnect(errcode.E(errcode.UNAVAILABLE, "add raft nonvoter failed")), retryable: true},
		{name: "admission timeout", err: ToConnect(errcode.E(errcode.TIMEOUT, "join timed out")), retryable: true},
		{name: "invalid token", err: ToConnect(errcode.E(errcode.INVALID, "invalid join token")), retryable: false},
		{name: "revoked node", err: ToConnect(errcode.E(errcode.DENIED, "node removed")), retryable: false},
		{name: "duplicate node", err: ToConnect(errcode.E(errcode.DUPLICATE_NODE_ID, "node_id already admitted")), retryable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinErrorRetryable(tt.err); got != tt.retryable {
				t.Fatalf("joinErrorRetryable()=%v want %v: %v", got, tt.retryable, tt.err)
			}
		})
	}
}

func TestRequestJoin_RetriesPendingAdmissionWithSameIdentity(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	addCalls := 0
	seed := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		control:  raftNode,
		onAdmit: func(nodeID, raftAddr string) error {
			addCalls++
			if addCalls == 1 {
				return errors.New("injected AddNonvoter failure")
			}
			return raftNode.AddNonvoter(nodeID, raftAddr)
		},
	})
	seed.init(t)
	tok, err := seed.node.CreateJoinToken(ctx, connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-token-pending", Operator: "t"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	joiner := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		raftAddr: func() string { return "pending-joiner-raft" },
	})
	first := connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-local-first", Operator: "t"},
		SeedServer: seed.url,
		Token:      tok.Msg.GetToken(),
	})
	_, firstErr := joiner.cluster.RequestJoin(ctx, first)
	if connect.CodeOf(firstErr) != connect.CodeUnavailable {
		t.Fatalf("first RequestJoin code=%v err=%v", connect.CodeOf(firstErr), firstErr)
	}
	if !strings.Contains(firstErr.Error(), "add raft nonvoter failed") {
		t.Fatalf("first RequestJoin hid admission failure: %v", firstErr)
	}
	if control.AlreadyInited(joiner.dir) {
		t.Fatal("failed RequestJoin persisted completed cluster identity")
	}
	pendingPath := filepath.Join(joiner.dir, "join.pending.json")
	stat, err := os.Stat(pendingPath)
	if err != nil {
		t.Fatalf("pending join file: %v", err)
	}
	if stat.Mode().Perm() != 0o600 {
		t.Fatalf("pending join mode=%o", stat.Mode().Perm())
	}
	prepared := raftNode.View().JoinAttempts[joiner.nodeID]
	if prepared.OperationID != "op-local-first" || prepared.Status != control.JoinPreparing {
		t.Fatalf("prepared=%+v", prepared)
	}

	second := connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-local-second", Operator: "t"},
		SeedServer: seed.url,
		Token:      tok.Msg.GetToken(),
	})
	resp, err := joiner.cluster.RequestJoin(ctx, second)
	if err != nil {
		t.Fatalf("resume RequestJoin: %v", err)
	}
	if resp.Msg.GetClusterId() == "" {
		t.Fatal("resume returned empty cluster_id")
	}
	certPEM, err := os.ReadFile(filepath.Join(joiner.dir, "agent.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(certPEM) != string(prepared.CertPEM) {
		t.Fatal("resume did not persist the prepared certificate")
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending join file remains after success: %v", err)
	}
}

func TestJoin_UsesRaftTokenNotFile(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	var admitted []string
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		control:  raftNode,
		onAdmit: func(nodeID, raftAddr string) error {
			admitted = append(admitted, nodeID+"="+raftAddr)
			return raftNode.AddNonvoter(nodeID, raftAddr)
		},
	})
	inited := e.init(t)
	adm := control.Admission{Node: raftNode}
	plain, _, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "tokens.json")); !os.IsNotExist(err) {
		t.Fatalf("tokens.json should not exist before join: %v", err)
	}
	const joinerID = "raft-joiner"
	csr, _, err := control.NewCSR("join", joinerID)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-raft", Operator: "t"},
		Token:           plain,
		NodeId:          joinerID,
		Hostname:        "joiner-host",
		BootId:          "boot-rj",
		ProtocolVersion: int32(version.Protocol),
		ApiAddress:      "127.0.0.1:18683",
		GossipAddress:   "127.0.0.1:7947",
		RaftAddress:     "127.0.0.1:118685",
		CsrPem:          csr,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if joined.Msg.GetClusterId() != inited.GetClusterId() {
		t.Fatalf("cluster_id=%q want %q", joined.Msg.GetClusterId(), inited.GetClusterId())
	}
	if joined.Msg.GetRaftLeader() != raftNode.Advertise() {
		t.Fatalf("raft_leader=%q want %q", joined.Msg.GetRaftLeader(), raftNode.Advertise())
	}
	if err := control.VerifyAgent(joined.Msg.GetCaPem(), joined.Msg.GetCertPem(), inited.GetClusterId(), joinerID, e.now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "tokens.json")); !os.IsNotExist(err) {
		t.Fatal("tokens.json must not be written")
	}
	m, ok := raftNode.View().Members[joinerID]
	if !ok || m.Status != control.MemberAdmitted || m.RaftAddr != "127.0.0.1:118685" {
		t.Fatalf("member=%+v ok=%v", m, ok)
	}
	if len(admitted) != 1 || admitted[0] != joinerID+"=127.0.0.1:118685" {
		t.Fatalf("onAdmit=%v", admitted)
	}
}

func TestJoin_AddNonvoterFailureReturnsUnavailableAndCanResume(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	addCalls := 0
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		control:  raftNode,
		onAdmit: func(nodeID, raftAddr string) error {
			addCalls++
			if addCalls == 1 {
				return errors.New("injected AddNonvoter failure")
			}
			return raftNode.AddNonvoter(nodeID, raftAddr)
		},
	})
	e.init(t)
	adm := control.Admission{Node: raftNode}
	plain, info, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "retry-joiner")
	if err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-retry-join", Operator: "t"},
		Token:           plain,
		NodeId:          "retry-joiner",
		BootId:          "retry-boot",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "retry-joiner-raft",
		CsrPem:          csr,
	})
	if _, err := e.cluster.Join(ctx, req); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("first join code=%v err=%v", connect.CodeOf(err), err)
	}
	view := raftNode.View()
	if got := view.JoinTokens[info.ID].Remaining; got != 0 {
		t.Fatalf("remaining=%d want 0", got)
	}
	if member := view.Members[req.Msg.GetNodeId()]; member.Status != control.MemberJoining {
		t.Fatalf("member after failure=%+v", member)
	}
	prepared := view.JoinAttempts[req.Msg.GetNodeId()]
	if prepared.Status != control.JoinPreparing || len(prepared.CertPEM) == 0 {
		t.Fatalf("attempt after failure=%+v", prepared)
	}

	joined, err := e.cluster.Join(ctx, req)
	if err != nil {
		t.Fatalf("resume join: %v", err)
	}
	if string(joined.Msg.GetCertPem()) != string(prepared.CertPEM) {
		t.Fatal("resume returned a different certificate")
	}
	view = raftNode.View()
	if view.Members[req.Msg.GetNodeId()].Status != control.MemberAdmitted || view.JoinAttempts[req.Msg.GetNodeId()].Status != control.JoinCompleted {
		t.Fatalf("completed member=%+v attempt=%+v", view.Members[req.Msg.GetNodeId()], view.JoinAttempts[req.Msg.GetNodeId()])
	}
	membership, err := raftNode.RaftMembershipView()
	if err != nil {
		t.Fatal(err)
	}
	if membership.Members[req.Msg.GetNodeId()] != control.RaftNonVoter {
		t.Fatalf("raft role=%q", membership.Members[req.Msg.GetNodeId()])
	}
	if addCalls != 2 {
		t.Fatalf("AddNonvoter calls=%d want 2", addCalls)
	}
}

func TestJoin_AddNonvoterErrorAfterCommitUsesMembershipReadback(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		control:  raftNode,
		onAdmit: func(nodeID, raftAddr string) error {
			if err := raftNode.AddNonvoter(nodeID, raftAddr); err != nil {
				return err
			}
			return errors.New("injected lost AddNonvoter acknowledgement")
		},
	})
	e.init(t)
	adm := control.Admission{Node: raftNode}
	plain, _, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "uncertain-joiner")
	if err != nil {
		t.Fatal(err)
	}
	joined, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-uncertain-join", Operator: "t"},
		Token:           plain,
		NodeId:          "uncertain-joiner",
		BootId:          "uncertain-boot",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "uncertain-joiner-raft",
		CsrPem:          csr,
	}))
	if err != nil {
		t.Fatalf("join after committed AddNonvoter: %v", err)
	}
	if len(joined.Msg.GetCertPem()) == 0 || raftNode.View().Members["uncertain-joiner"].Status != control.MemberAdmitted {
		t.Fatalf("join did not complete after membership readback: %+v", raftNode.View().Members["uncertain-joiner"])
	}
}

func TestJoin_DoesNotSucceedWithoutCommittedRaftMembership(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		control:  raftNode,
		onAdmit:  func(string, string) error { return nil },
	})
	e.init(t)
	adm := control.Admission{Node: raftNode}
	plain, _, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "missing-membership")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-missing-membership", Operator: "t"},
		Token:           plain,
		NodeId:          "missing-membership",
		BootId:          "missing-membership-boot",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "missing-membership-raft",
		CsrPem:          csr,
	}))
	if connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "raft nonvoter not committed") {
		t.Fatalf("join without membership code=%v err=%v", connect.CodeOf(err), err)
	}
	if member := raftNode.View().Members["missing-membership"]; member.Status != control.MemberJoining {
		t.Fatalf("member=%+v want JOINING", member)
	}
}

func TestJoin_StaleRaftAddressDoesNotCompleteAdmission(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	if err := raftNode.AddNonvoter("stale-address", "old-raft-address"); err != nil {
		t.Fatal(err)
	}
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		control:  raftNode,
		onAdmit:  func(string, string) error { return errors.New("injected AddNonvoter failure") },
	})
	e.init(t)
	adm := control.Admission{Node: raftNode}
	plain, _, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "stale-address")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-stale-address", Operator: "t"},
		Token:           plain,
		NodeId:          "stale-address",
		BootId:          "stale-address-boot",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "new-raft-address",
		CsrPem:          csr,
	}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("join with stale Raft address code=%v err=%v", connect.CodeOf(err), err)
	}
	if member := raftNode.View().Members["stale-address"]; member.Status != control.MemberJoining {
		t.Fatalf("member=%+v want JOINING", member)
	}
}

func TestJoin_MixedProtocolRaftMemberBlocksNewFSMCommands(t *testing.T) {
	if version.Protocol != 2 {
		t.Fatalf("join FSM protocol=%d want 2", version.Protocol)
	}
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		control:  raftNode,
		raftMembership: staticRaftMembershipReader{view: control.RaftMembershipView{
			Members: map[string]control.RaftSuffrage{
				"seed":        control.RaftVoter,
				"old-control": control.RaftVoter,
			},
			LeaderID:  "seed",
			HasQuorum: true,
		}},
	})
	e.init(t)
	e.mesh.setMembers([]cluster.NodeSummary{
		e.local,
		{NodeID: "seed", State: cluster.StateAlive, ProtocolVersion: version.Protocol},
		{NodeID: "old-control", State: cluster.StateAlive, ProtocolVersion: 1},
	})
	adm := control.Admission{Node: raftNode}
	plain, info, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "mixed-version-joiner")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-mixed-version", Operator: "t"},
		Token:           plain,
		NodeId:          "mixed-version-joiner",
		BootId:          "mixed-version-boot",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "mixed-version-raft",
		CsrPem:          csr,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "INCOMPATIBLE_VERSION" {
		t.Fatalf("mixed-version join code=%v detail=%s err=%v", code, detail, err)
	}
	view := raftNode.View()
	if got := view.JoinTokens[info.ID].Remaining; got != 1 {
		t.Fatalf("mixed-version join consumed token: remaining=%d", got)
	}
	if _, exists := view.JoinAttempts["mixed-version-joiner"]; exists {
		t.Fatal("mixed-version join created an attempt")
	}
}

func TestJoin_PreviouslyJoinedRaftMemberStillRequiresCurrentProtocol(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{withMesh: true, control: raftNode})
	e.init(t)
	adm := control.Admission{Node: raftNode}
	oldToken, _, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adm.PrepareJoin(control.JoinPrepare{
		OperationID: "op-previous", Token: oldToken, NodeID: "previous", RaftAddr: "previous-raft",
		CSRHash: "previous-csr", CertPEM: []byte("previous-cert"), CertSerial: "AA",
	}); err != nil {
		t.Fatal(err)
	}
	if err := raftNode.AddNonvoter("previous", "previous-raft"); err != nil {
		t.Fatal(err)
	}
	e.mesh.setMembers([]cluster.NodeSummary{
		e.local,
		{NodeID: "previous", State: cluster.StateAlive, ProtocolVersion: version.Protocol - 1},
	})

	plain, info, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "next")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-next", Operator: "t"},
		Token:           plain,
		NodeId:          "next",
		BootId:          "next-boot",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "next-raft",
		CsrPem:          csr,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "INCOMPATIBLE_VERSION" {
		t.Fatalf("downgraded member join code=%v detail=%s err=%v", code, detail, err)
	}
	if got := raftNode.View().JoinTokens[info.ID].Remaining; got != 1 {
		t.Fatalf("blocked join consumed token: remaining=%d", got)
	}
}

func TestJoin_RevokedNodeDenied(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{withMesh: true, control: raftNode})
	e.init(t)
	adm := control.Admission{Node: raftNode}
	if err := adm.Admit("gone", "", "AA"); err != nil {
		t.Fatal(err)
	}
	cmd, err := control.EncodeCommand(control.CmdMemberRemove, control.MemberRemoveBody{NodeID: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := raftNode.Apply(cmd, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	csr, _, err := control.NewCSR("join", "gone")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-revoked", Operator: "t"},
		Token:           "pmj_unused",
		NodeId:          "gone",
		BootId:          "boot-gone",
		ProtocolVersion: int32(version.Protocol),
		CsrPem:          csr,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "node removed") {
		t.Fatalf("want node removed: %v", err)
	}
}

func TestJoin_RevokedWhileStillInGossip(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{withMesh: true, control: raftNode})
	e.init(t)
	adm := control.Admission{Node: raftNode}
	if err := adm.Admit("gone", "127.0.0.1:19099", "BB"); err != nil {
		t.Fatal(err)
	}
	cmd, err := control.EncodeCommand(control.CmdMemberRemove, control.MemberRemoveBody{NodeID: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := raftNode.Apply(cmd, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	e.mesh.setMembers([]cluster.NodeSummary{
		e.local,
		{
			NodeID:          "gone",
			BootID:          "boot-old",
			State:           cluster.StateAlive,
			ProtocolVersion: version.Protocol,
		},
	})
	csr, _, err := control.NewCSR("join", "gone")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-gossip-revoked", Operator: "t"},
		Token:           "pmj_unused",
		NodeId:          "gone",
		BootId:          "boot-new",
		ProtocolVersion: int32(version.Protocol),
		CsrPem:          csr,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if strings.Contains(err.Error(), "DUPLICATE_NODE_ID") {
		t.Fatalf("revoked rejoin must not be DUPLICATE_NODE_ID: %v", err)
	}
	if !strings.Contains(err.Error(), "node removed") {
		t.Fatalf("want node removed: %v", err)
	}
}

func TestJoin_ForwardsToLeader(t *testing.T) {
	ctx := context.Background()
	raftA, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raftA.Shutdown() })
	if err := raftA.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !raftA.IsLeader() || !raftA.HasQuorum() {
		if time.Now().After(deadline) {
			t.Fatal("raftA never became leader")
		}
		time.Sleep(20 * time.Millisecond)
	}

	raftB, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raftB.Shutdown() })
	if err := raftA.AddVoter("b", raftB.Advertise()); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for raftB.LeaderAddr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("raftB never learned leader")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if raftB.IsLeader() {
		t.Fatal("raftB should not be leader")
	}

	envA := newClusterEnvFull(t, clusterEnvCfg{control: raftA, withMesh: true})
	inited := envA.init(t)
	envA.mesh.setMembers([]cluster.NodeSummary{
		{NodeID: "a", State: cluster.StateAlive, ProtocolVersion: version.Protocol},
		{NodeID: "b", State: cluster.StateAlive, ProtocolVersion: version.Protocol},
	})

	envB := newClusterEnvFull(t, clusterEnvCfg{
		control:   raftB,
		leaderAPI: func() string { return envA.url },
	})
	if err := control.SaveMeta(envB.dir, control.Meta{
		ClusterID:     inited.GetClusterId(),
		NodeID:        envB.nodeID,
		ControlMember: false,
		CreatedAt:     envB.now.UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	adm := control.Admission{Node: raftA}
	plain, _, err := adm.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	const joinerID = "forward-joiner"
	csr, _, err := control.NewCSR("join", joinerID)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := envB.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-fwd", Operator: "t"},
		Token:           plain,
		NodeId:          joinerID,
		Hostname:        "joiner-host",
		BootId:          "boot-fwd",
		ProtocolVersion: int32(version.Protocol),
		ApiAddress:      "127.0.0.1:9101",
		RaftAddress:     "forward-joiner-raft",
		CsrPem:          csr,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if joined.Msg.GetClusterId() != inited.GetClusterId() {
		t.Fatalf("cluster_id=%q want %q", joined.Msg.GetClusterId(), inited.GetClusterId())
	}
	if joined.Msg.GetRaftLeader() != raftA.Advertise() {
		t.Fatalf("raft_leader=%q want %q", joined.Msg.GetRaftLeader(), raftA.Advertise())
	}
	if err := control.VerifyAgent(joined.Msg.GetCaPem(), joined.Msg.GetCertPem(), inited.GetClusterId(), joinerID, envB.now); err != nil {
		t.Fatal(err)
	}
	m, ok := raftA.View().Members[joinerID]
	if !ok || m.Status != control.MemberAdmitted {
		t.Fatalf("member=%+v ok=%v", m, ok)
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

func TestCluster_Overview_Aggregates(t *testing.T) {
	e := newClusterEnv(t)
	e.mesh.setMembers([]cluster.NodeSummary{
		{
			NodeID:       "n1",
			State:        cluster.StateAlive,
			AgentVersion: "v1.0.0",
			Resources:    cluster.ResourceSummary{CPUPercent: 10, MemoryPercent: 20, DiskPercent: 30},
			Processes:    []cluster.ProcessSummary{{Name: "p1", Observed: "RUNNING", Health: "HEALTHY"}},
		},
		{
			NodeID:       "n2",
			State:        cluster.StateAlive,
			AgentVersion: "v1.0.1",
			Resources:    cluster.ResourceSummary{CPUPercent: 30, MemoryPercent: 40, DiskPercent: 50},
			Processes:    []cluster.ProcessSummary{{Name: "p2", Observed: "RUNNING", Health: "UNHEALTHY"}},
		},
		{
			NodeID:       "n3",
			State:        cluster.StateFailed,
			AgentVersion: "v1.0.0",
			Resources:    cluster.ResourceSummary{CPUPercent: 99, MemoryPercent: 99, DiskPercent: 99},
			Processes:    []cluster.ProcessSummary{{Name: "p3", Observed: "RUNNING", Health: "HEALTHY"}},
		},
	})
	ov, err := e.cluster.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	msg := ov.Msg
	if msg.GetMembers() != 3 || msg.GetAlive() != 2 || msg.GetFailed() != 1 {
		t.Fatalf("members=%d alive=%d failed=%d", msg.GetMembers(), msg.GetAlive(), msg.GetFailed())
	}
	if msg.GetProcessTotal() != 3 || msg.GetProcessRunning() != 3 || msg.GetProcessUnhealthy() != 1 {
		t.Fatalf("process total=%d running=%d unhealthy=%d", msg.GetProcessTotal(), msg.GetProcessRunning(), msg.GetProcessUnhealthy())
	}
	if msg.GetVersionCounts()["v1.0.0"] != 2 {
		t.Fatalf("version_counts=%v", msg.GetVersionCounts())
	}
	if msg.GetViewUnixMs() <= 0 {
		t.Fatalf("view_unix_ms=%d", msg.GetViewUnixMs())
	}
}

func TestCluster_Overview_EmptyPercents(t *testing.T) {
	e := newClusterEnv(t)
	e.mesh.setMembers(nil)
	ov, err := e.cluster.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if ov.Msg.GetCpuPercent() != -1 || ov.Msg.GetMemoryPercent() != -1 || ov.Msg.GetDiskPercent() != -1 {
		t.Fatalf("percents cpu=%d mem=%d disk=%d want -1 (unknown)", ov.Msg.GetCpuPercent(), ov.Msg.GetMemoryPercent(), ov.Msg.GetDiskPercent())
	}
}

func TestCluster_Overview_UncollectedResourcesNotZero(t *testing.T) {
	e := newClusterEnv(t)
	e.mesh.setMembers([]cluster.NodeSummary{
		{NodeID: "n1", State: cluster.StateAlive},
		{NodeID: "n2", State: cluster.StateAlive, Resources: cluster.ResourceSummary{CPUPercent: -1, MemoryPercent: -1, DiskPercent: -1}},
	})
	ov, err := e.cluster.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if ov.Msg.GetAlive() != 2 {
		t.Fatalf("alive=%d want 2", ov.Msg.GetAlive())
	}
	if ov.Msg.GetCpuPercent() != -1 || ov.Msg.GetMemoryPercent() != -1 || ov.Msg.GetDiskPercent() != -1 {
		t.Fatalf("uncollected percents cpu=%d mem=%d disk=%d want -1", ov.Msg.GetCpuPercent(), ov.Msg.GetMemoryPercent(), ov.Msg.GetDiskPercent())
	}
}

func TestCluster_Overview_PlatformNote(t *testing.T) {
	e := newClusterEnv(t)
	ov, err := e.cluster.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	note := ov.Msg.GetPlatformNote()
	switch runtime.GOOS {
	case "linux":
		if note != "" {
			t.Fatalf("linux platform_note=%q want empty", note)
		}
	case "darwin":
		const want = "macOS: resource_limit ignored (no cgroup); Host reboot recovery depends on how the Agent is started."
		if note != want {
			t.Fatalf("darwin platform_note=%q want %q", note, want)
		}
	default:
		if note == "" {
			t.Fatalf("%s platform_note empty", runtime.GOOS)
		}
	}
}

func TestCluster_Overview_CertExpires(t *testing.T) {
	e := newClusterEnv(t)
	e.init(t)
	ov, err := e.cluster.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if ov.Msg.GetCaExpiresUnix() <= now {
		t.Fatalf("ca_expires_unix=%d want > %d", ov.Msg.GetCaExpiresUnix(), now)
	}
	if ov.Msg.GetCertExpiresUnix() <= now {
		t.Fatalf("cert_expires_unix=%d want > %d", ov.Msg.GetCertExpiresUnix(), now)
	}
}

func TestCluster_Overview_Summarize(t *testing.T) {
	got := summarize([]cluster.NodeSummary{
		{
			State:     cluster.StateSuspect,
			Processes: []cluster.ProcessSummary{{Observed: "FATAL", Health: "UNHEALTHY"}},
		},
		{
			State:        cluster.StateAlive,
			AgentVersion: "v2",
			Resources:    cluster.ResourceSummary{CPUPercent: 11, MemoryPercent: 21, DiskPercent: 31},
		},
		{
			State:        cluster.StateAlive,
			AgentVersion: "v2",
			Resources:    cluster.ResourceSummary{CPUPercent: 10, MemoryPercent: 20, DiskPercent: 30},
		},
	})
	if got.suspect != 1 || got.alive != 2 || got.processFatal != 1 || got.processUnhealthy != 1 {
		t.Fatalf("counts %+v", got)
	}
	if got.versionCounts["unknown"] != 1 || got.versionCounts["v2"] != 2 {
		t.Fatalf("versions %v", got.versionCounts)
	}
	if got.cpuPercent != 10 || got.memoryPercent != 20 || got.diskPercent != 30 {
		t.Fatalf("percents cpu=%d mem=%d disk=%d", got.cpuPercent, got.memoryPercent, got.diskPercent)
	}
}
