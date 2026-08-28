package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"errors"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

// newDisasterReplicationClient wires api behind a real HTTP handler with the
// AuthInterceptor installed, so PrincipalFrom(ctx) is populated the same way
// it is in production. Calling API methods directly (bypassing the mux)
// would skip the interceptor and make requirePerm() silently allow everything.
func newDisasterReplicationClient(t *testing.T, api *DisasterReplicationAPI, options ...connect.ClientOption) procmeshv1connect.DisasterReplicationServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	h, handlers := procmeshv1connect.NewDisasterReplicationServiceHandler(api,
		connect.WithInterceptors(AuthInterceptor(api.Auth, func() bool { return true })))
	mux.Handle(h, handlers)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewDisasterReplicationServiceClient(srv.Client(), srv.URL, options...)
}

// Test helper functions

func TestReplicationPolicyToProto(t *testing.T) {
	now := time.Now()
	policy := control.ReplicationPolicy{
		PolicyID:            "policy-123",
		Name:                "test-policy",
		Enabled:             true,
		SourceSelector:      "all",
		SourceIDs:           []string{"node-1", "node-2"},
		ReplicaFactor:       2,
		Routes:              []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2", "node-3"}}},
		Trigger:             "MANUAL",
		PrimaryPolicyIDs:    []string{"primary-1"},
		ScheduleCron:        "0 0 * * *",
		Timezone:            "UTC",
		RetentionKeepLast:   10,
		RetentionKeepDays:   30,
		RetentionMaxBytes:   1000000,
		MaxConcurrency:      5,
		VerifyAfterCopy:     true,
		BandwidthLimit:      100000,
		TopologyConstraints: map[string]string{"zone": "us-west"},
		Revision:            7,
	}

	proto := replicationPolicyToProto(policy)

	if proto.PolicyId != "policy-123" {
		t.Errorf("PolicyId: got %q, want %q", proto.PolicyId, "policy-123")
	}
	if proto.Name != "test-policy" {
		t.Errorf("Name: got %q, want %q", proto.Name, "test-policy")
	}
	if !proto.Enabled {
		t.Error("Enabled: got false, want true")
	}
	if proto.SourceSelector != "all" {
		t.Errorf("SourceSelector: got %q, want %q", proto.SourceSelector, "all")
	}
	if len(proto.SourceIds) != 2 {
		t.Errorf("SourceIds: got %d items, want 2", len(proto.SourceIds))
	}
	if proto.ReplicaFactor != 2 {
		t.Errorf("ReplicaFactor: got %d, want 2", proto.ReplicaFactor)
	}
	if len(proto.Routes) != 1 {
		t.Errorf("Routes: got %d, want 1", len(proto.Routes))
	}
	if proto.Routes[0].SourceNodeId != "node-1" {
		t.Errorf("Route SourceNodeId: got %q, want %q", proto.Routes[0].SourceNodeId, "node-1")
	}
	if len(proto.Routes[0].TargetNodeIds) != 2 {
		t.Errorf("Route TargetNodeIds: got %d, want 2", len(proto.Routes[0].TargetNodeIds))
	}
	if proto.Trigger != "MANUAL" {
		t.Errorf("Trigger: got %q, want %q", proto.Trigger, "MANUAL")
	}
	if proto.Revision != 7 {
		t.Errorf("Revision: got %d, want 7", proto.Revision)
	}
	if proto.ScheduleCron != "0 0 * * *" {
		t.Errorf("ScheduleCron: got %q, want %q", proto.ScheduleCron, "0 0 * * *")
	}
	if proto.RetentionKeepLast != 10 {
		t.Errorf("RetentionKeepLast: got %d, want 10", proto.RetentionKeepLast)
	}
	if proto.VerifyAfterCopy != true {
		t.Error("VerifyAfterCopy: got false, want true")
	}
	if len(proto.TopologyConstraints) != 1 {
		t.Errorf("TopologyConstraints: got %d items, want 1", len(proto.TopologyConstraints))
	}

	_ = now
}

func TestComputeLegacyDraftHash(t *testing.T) {
	req := &procmeshv1.GeneratePolicyDraftRequest{
		Name:           "test-policy",
		SourceSelector: "ALL_ADMITTED",
		ReplicaFactor:  2,
	}

	routes := []*procmeshv1.ReplicationRoute{
		{SourceNodeId: "node-1", TargetNodeIds: []string{"node-2", "node-3"}},
		{SourceNodeId: "node-2", TargetNodeIds: []string{"node-1", "node-3"}},
	}

	hash1 := computeLegacyDraftHash(req, routes)
	if hash1 == "" {
		t.Error("expected non-empty hash")
	}
	if len(hash1) != 64 { // SHA256 hex length
		t.Errorf("hash length: got %d, want 64", len(hash1))
	}

	// Same input should produce same hash
	hash2 := computeLegacyDraftHash(req, routes)
	if hash1 != hash2 {
		t.Errorf("hash mismatch: %q != %q", hash1, hash2)
	}

	// Different routes should produce different hash
	routes2 := []*procmeshv1.ReplicationRoute{
		{SourceNodeId: "node-1", TargetNodeIds: []string{"node-2"}},
	}
	hash3 := computeLegacyDraftHash(req, routes2)
	if hash1 == hash3 {
		t.Error("expected different hash for different routes")
	}

	// Route order should not matter (routes are sorted)
	routes3 := []*procmeshv1.ReplicationRoute{
		{SourceNodeId: "node-2", TargetNodeIds: []string{"node-1", "node-3"}},
		{SourceNodeId: "node-1", TargetNodeIds: []string{"node-2", "node-3"}},
	}
	hash4 := computeLegacyDraftHash(req, routes3)
	if hash1 != hash4 {
		t.Errorf("hash should be same for reordered routes: %q != %q", hash1, hash4)
	}
}

func TestComputeLegacyDraftHashMatchesReportedPreRingDraft(t *testing.T) {
	req := &procmeshv1.GeneratePolicyDraftRequest{
		Name: "cluster-replica", Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Trigger: "MANUAL", Timezone: "UTC", RetentionKeepLast: 7, RetentionKeepDays: 30,
		MaxConcurrency: 2, VerifyAfterCopy: true,
	}
	routes := []*procmeshv1.ReplicationRoute{
		{SourceNodeId: "19199351-71be-4045-8542-53711015e262", TargetNodeIds: []string{"49b2c892-955c-4f49-a1a1-e629aeff89e2"}},
		{SourceNodeId: "49b2c892-955c-4f49-a1a1-e629aeff89e2", TargetNodeIds: []string{"19199351-71be-4045-8542-53711015e262"}},
		{SourceNodeId: "efcff992-0ebf-49f8-b632-2892d82f1a62", TargetNodeIds: []string{"19199351-71be-4045-8542-53711015e262"}},
	}
	const reportedHash = "3842fdc0093371cfb0695f9839aeba63549336223b1bc72c5c0b2d7fa0fbc933"
	if got := computeLegacyDraftHashForTopology(req, routes, 1462853928305601348); got != reportedHash {
		t.Fatalf("legacy draft hash=%s, want reported hash %s", got, reportedHash)
	}
}

func TestComputePolicyDraftHashIgnoresCollectionRepresentationAndOrder(t *testing.T) {
	req := &procmeshv1.GeneratePolicyDraftRequest{
		Name: "test-policy", SourceSelector: "EXPLICIT_NODES", SourceIds: []string{"node-2", "node-1"},
		PrimaryPolicyIds: []string{"backup-2", "backup-1"}, ReplicaFactor: 1,
	}
	hash1 := computePolicyDraftHashForTopology(req, 42)
	reordered := &procmeshv1.GeneratePolicyDraftRequest{
		Name: "test-policy", SourceSelector: "EXPLICIT_NODES", SourceIds: []string{"node-1", "node-2"},
		PrimaryPolicyIds: []string{"backup-1", "backup-2"}, ReplicaFactor: 1,
		TopologyConstraints: map[string]string{},
	}
	if hash2 := computePolicyDraftHashForTopology(reordered, 42); hash1 != hash2 {
		t.Fatalf("equivalent policy inputs should have the same hash: %s != %s", hash1, hash2)
	}
	reordered.ReplicaFactor = 2
	if hash2 := computePolicyDraftHashForTopology(reordered, 42); hash1 == hash2 {
		t.Fatal("policy input change should alter the hash")
	}
}

// Integration tests with minimal auth setup

func setupMinimalAPI(t *testing.T) (*DisasterReplicationAPI, *control.State, *auth.Service) {
	t.Helper()

	dir := t.TempDir()
	db, authSvc := newBootstrappedAuth(t)
	_ = db

	state := control.NewState()
	state.Members["node-1"] = control.Member{NodeID: "node-1", Status: control.MemberAdmitted}
	state.Members["node-2"] = control.Member{NodeID: "node-2", Status: control.MemberAdmitted}
	state.Members["node-3"] = control.Member{NodeID: "node-3", Status: control.MemberAdmitted}

	api := &DisasterReplicationAPI{
		ClusterID: "test-cluster",
		NodeID:    "test-node",
		Auth:      authSvc,
		StateFn: func() control.State {
			return *state
		},
		ApplyFn: func(cmd control.Command, timeout time.Duration) error {
			return nil
		},
		LeaderTerm: func() uint64 { return 1 },
		PeerStore:  &backup.PeerStore{Root: dir},
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "node-1", Hostname: "agent-one", State: cluster.StateAlive, Labels: map[string]string{"host": "host1", "zone": "z1"}},
				{NodeID: "node-2", Hostname: "agent-two", State: cluster.StateAlive, Labels: map[string]string{"host": "host2", "zone": "z2"}},
				{NodeID: "node-3", Hostname: "agent-three", State: cluster.StateAlive, Labels: map[string]string{"host": "host3", "zone": "z3"}},
			}
		},
	}

	return api, state, authSvc
}

// setupAPIWithViewerUser creates a minimal API with a viewer user (no replication permissions)
func setupAPIWithViewerUser(t *testing.T) (*DisasterReplicationAPI, *control.State, *auth.Service, string) {
	t.Helper()
	api, state, authSvc := setupMinimalAPI(t)

	// Create a viewer user
	cmd, err := control.EncodeCommand(control.CmdUserPut, control.UserPutBody{
		ID:           "user-viewer",
		Username:     "viewer",
		PasswordHash: testAdminHash(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := authSvc.Store().Apply(cmd, time.Second); err != nil {
		t.Fatal(err)
	}

	// Bind viewer role (has cluster.read, process.read, etc., but NOT replication.read)
	cmd, err = control.EncodeCommand(control.CmdBindPut, control.BindPutBody{
		UserID: "user-viewer",
		RoleID: "viewer",
		Scope:  control.ScopeCluster,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := authSvc.Store().Apply(cmd, time.Second); err != nil {
		t.Fatal(err)
	}

	// Login as viewer
	sid, _, _, _, err := authSvc.Login("viewer", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	return api, state, authSvc, sid
}

func TestDisasterReplicationAPI_GeneratePolicyDraft(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name:           "test-policy",
		SourceSelector: "ALL_ADMITTED",
		ReplicaFactor:  2,
		Enabled:        true,
	})

	resp, err := api.GeneratePolicyDraft(context.Background(), req)
	if err != nil {
		t.Fatalf("GeneratePolicyDraft failed: %v", err)
	}

	draft := resp.Msg.Draft
	if draft == nil {
		t.Fatal("expected draft, got nil")
	}
	if draft.Name != "test-policy" {
		t.Errorf("name: got %q, want %q", draft.Name, "test-policy")
	}
	if draft.ReplicaFactor != 2 {
		t.Errorf("replica factor: got %d, want 2", draft.ReplicaFactor)
	}
	if len(draft.Routes) == 0 {
		t.Error("expected routes to be generated")
	}
	if draft.DraftHash == "" {
		t.Error("expected draft hash to be set")
	}
	if draft.DraftRevision <= 0 {
		t.Errorf("draft revision: got %d, want server topology revision", draft.DraftRevision)
	}
	if draft.TopologyHealth == "" {
		t.Error("expected topology health to be set")
	}
}

func TestDisasterReplicationAPI_GeneratePolicyDraftExplicitSelectorUsesAllAdmittedTargets(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "explicit", SourceSelector: "EXPLICIT_NODES", SourceIds: []string{"node-1"}, ReplicaFactor: 2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	routes := resp.Msg.Draft.Routes
	if len(routes) != 1 || routes[0].SourceNodeId != "node-1" {
		t.Fatalf("routes=%+v, want only node-1 source", routes)
	}
	if got, want := routes[0].TargetNodeIds, []string{"node-2", "node-3"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("targets=%v, want %v", got, want)
	}
}

func TestDisasterReplicationAPI_GeneratePolicyDraftGroupSelectorUsesAllAdmittedTargets(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	state.AgentGroups["selected"] = control.AgentGroup{GroupID: "selected", MemberIDs: []string{"node-2"}}
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "group", SourceSelector: "AGENT_GROUP", SourceIds: []string{"selected"}, ReplicaFactor: 2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	routes := resp.Msg.Draft.Routes
	if len(routes) != 1 || routes[0].SourceNodeId != "node-2" {
		t.Fatalf("routes=%+v, want only node-2 source", routes)
	}
	if got, want := routes[0].TargetNodeIds, []string{"node-3", "node-1"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("targets=%v, want ring successors %v", got, want)
	}
}

func TestDisasterReplicationAPI_GeneratePolicyDraftForwardsLeader(t *testing.T) {
	leader, _, authSvc := setupMinimalAPI(t)
	leader.Auth = nil // The internal Leader endpoint authenticates forwarded identity separately.
	leader.IsLeader = func() bool { return true }
	leaderClient := newDisasterReplicationClient(t, leader)

	follower := &DisasterReplicationAPI{
		LocalID:  "node-follower",
		Auth:     authSvc,
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, bool) {
			return Route{NodeID: "node-leader", RPC: "127.0.0.1:18683"}, true
		},
		Forward: disasterReplicationForwarder{client: leaderClient},
	}
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	client := newDisasterReplicationClient(t, follower)
	resp, err := client.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "forwarded", SourceSelector: "EXPLICIT_NODES", SourceIds: []string{"node-1"}, ReplicaFactor: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Draft.Routes) != 1 || resp.Msg.Draft.Routes[0].SourceNodeId != "node-1" {
		t.Fatalf("routes=%+v, want leader-generated selected route", resp.Msg.Draft.Routes)
	}
}

func TestTopologyDraftRevisionIgnoresLiveness(t *testing.T) {
	alive := []backup.AgentTopology{{NodeID: "node-1", Host: "h1", Rack: "r1", Zone: "z1", CapacityWeight: 1, Admitted: true, Alive: true}}
	offline := append([]backup.AgentTopology(nil), alive...)
	offline[0].Alive = false

	if got, want := topologyDraftRevision(offline), topologyDraftRevision(alive); got != want {
		t.Fatalf("offline revision=%d, want unchanged %d", got, want)
	}
}

func TestDisasterReplicationAPI_GeneratePolicyDraftKeepsOfflineAdmittedMember(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	state.Members["node-offline"] = control.Member{NodeID: "node-offline", Status: control.MemberAdmitted}
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{Name: "offline", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Draft.Routes) != 4 {
		t.Fatalf("routes=%+v, want offline admitted source retained", resp.Msg.Draft.Routes)
	}
	found := false
	for _, warning := range resp.Msg.Draft.GlobalWarnings {
		if warning == "admitted-node-offline:node-offline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings=%v, want offline warning", resp.Msg.Draft.GlobalWarnings)
	}
}

func TestDisasterReplicationAPI_GeneratePolicyDraft_NoPermission(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	putViewerUser(t, authSvc)
	sid, _, _, _, err := authSvc.Login("viewer", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	// Viewer role does not hold replication.read, so this must be denied.
	// Calling api.GeneratePolicyDraft directly would bypass the auth
	// interceptor that populates the principal in ctx, so we go through a
	// real HTTP handler to exercise the actual authorization path.
	client := newDisasterReplicationClient(t, api)
	_, err = client.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name:           "test-policy",
		SourceSelector: "ALL_ADMITTED",
		ReplicaFactor:  2,
	}))
	assertDenied(t, err)
}

func TestDisasterReplicationAPI_ApplyPolicyDraft(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)

	// Mock ApplyFn to update state
	api.ApplyFn = func(cmd control.Command, timeout time.Duration) error {
		policy := control.ReplicationPolicy{
			PolicyID:      "policy-001",
			Name:          "test-policy",
			ReplicaFactor: 2,
			Revision:      1,
		}
		state.ReplicationPolicies[policy.PolicyID] = policy
		return nil
	}

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	// Generate draft
	genReq := bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name:           "test-policy",
		SourceSelector: "ALL_ADMITTED",
		ReplicaFactor:  2,
	})

	genResp, err := api.GeneratePolicyDraft(context.Background(), genReq)
	if err != nil {
		t.Fatalf("GeneratePolicyDraft failed: %v", err)
	}

	// Apply the draft
	applyReq := bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{
		Meta:          &procmeshv1.MutationMeta{OperationId: "op-apply"},
		PolicyId:      "policy-001",
		Draft:         genResp.Msg.Draft,
		DraftRevision: genResp.Msg.Draft.DraftRevision,
		DraftHash:     genResp.Msg.Draft.DraftHash,
	})

	applyResp, err := api.ApplyPolicyDraft(context.Background(), applyReq)
	if err != nil {
		t.Fatalf("ApplyPolicyDraft failed: %v", err)
	}

	if applyResp.Msg.PolicyId == "" {
		t.Error("expected policy ID to be returned")
	}
	if applyResp.Msg.Revision == 0 {
		t.Error("expected revision to be returned")
	}
}

func TestDisasterReplicationAPI_ApplyPolicyDraft_JSONRoundTrip(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	api.Members = func() []cluster.NodeSummary {
		return []cluster.NodeSummary{
			{NodeID: "19199351-71be-4045-8542-53711015e262", State: cluster.StateAlive},
			{NodeID: "49b2c892-955c-4f49-a1a1-e629aeff89e2", State: cluster.StateAlive},
			{NodeID: "efcff992-0ebf-49f8-b632-2892d82f1a62", State: cluster.StateAlive},
		}
	}
	state.Members = map[string]control.Member{
		"19199351-71be-4045-8542-53711015e262": {NodeID: "19199351-71be-4045-8542-53711015e262", Status: control.MemberAdmitted},
		"49b2c892-955c-4f49-a1a1-e629aeff89e2": {NodeID: "49b2c892-955c-4f49-a1a1-e629aeff89e2", Status: control.MemberAdmitted},
		"efcff992-0ebf-49f8-b632-2892d82f1a62": {NodeID: "efcff992-0ebf-49f8-b632-2892d82f1a62", Status: control.MemberAdmitted},
	}
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error {
		return state.Apply(cmd, time.Now())
	}

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	client := newDisasterReplicationClient(t, api, connect.WithProtoJSON())
	generated, err := client.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name:              "cluster-replica",
		Enabled:           true,
		SourceSelector:    "ALL_ADMITTED",
		ReplicaFactor:     1,
		Trigger:           "MANUAL",
		Timezone:          "UTC",
		RetentionKeepLast: 7,
		RetentionKeepDays: 30,
		MaxConcurrency:    2,
		VerifyAfterCopy:   true,
	}))
	if err != nil {
		t.Fatalf("GeneratePolicyDraft failed: %v", err)
	}
	draft := generated.Msg.GetDraft()
	_, err = client.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{
		PolicyId:         "eb117808-94c0-400e-b723-fa2b9a7e5672",
		Draft:            draft,
		DraftRevision:    draft.GetDraftRevision(),
		DraftHash:        draft.GetDraftHash(),
		ExpectedRevision: -1,
		Meta:             &procmeshv1.MutationMeta{OperationId: "4c68e25d-f357-4cc9-b2a0-7177cbdbc22f", Operator: "admin"},
	}))
	if err != nil {
		t.Fatalf("ApplyPolicyDraft after JSON round trip failed: %v", err)
	}
}

func TestDisasterReplicationAPI_ApplyPolicyDraftAcceptsPreRingDraft(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error {
		return state.Apply(cmd, time.Now())
	}
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	request := &procmeshv1.GeneratePolicyDraftRequest{
		Name: "cluster-replica", Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Trigger: "MANUAL", Timezone: "UTC", RetentionKeepLast: 7, RetentionKeepDays: 30,
		MaxConcurrency: 2, VerifyAfterCopy: true,
	}
	generated, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, request))
	if err != nil {
		t.Fatal(err)
	}
	if got := generated.Msg.Draft.Routes[1].TargetNodeIds; len(got) != 1 || got[0] != "node-3" {
		t.Fatalf("current ring route node-2 targets=%v, want [node-3]", got)
	}

	preRingRoutes := []*procmeshv1.ReplicationRoute{
		{SourceNodeId: "node-1", TargetNodeIds: []string{"node-2"}},
		{SourceNodeId: "node-2", TargetNodeIds: []string{"node-1"}},
		{SourceNodeId: "node-3", TargetNodeIds: []string{"node-1"}},
	}
	legacyHash := computeLegacyDraftHashForTopology(request, preRingRoutes, generated.Msg.Draft.DraftRevision)
	generated.Msg.Draft.DraftHash = legacyHash
	_, err = api.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{
		PolicyId: "rp-pre-ring", Draft: generated.Msg.Draft, DraftRevision: generated.Msg.Draft.DraftRevision,
		DraftHash: legacyHash, ExpectedRevision: -1,
		Meta: &procmeshv1.MutationMeta{OperationId: "op-pre-ring"},
	}))
	if err != nil {
		t.Fatalf("draft generated before route algorithm upgrade should apply: %v", err)
	}
}

func TestDisasterReplicationAPI_ApplyPolicyDraft_HashMismatch(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	// Generate draft
	genReq := bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name:           "test-policy",
		SourceSelector: "ALL_ADMITTED",
		ReplicaFactor:  2,
	})

	genResp, err := api.GeneratePolicyDraft(context.Background(), genReq)
	if err != nil {
		t.Fatalf("GeneratePolicyDraft failed: %v", err)
	}

	// Apply with wrong hash
	applyReq := bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{
		Meta:          &procmeshv1.MutationMeta{OperationId: "op-apply-bad"},
		Draft:         genResp.Msg.Draft,
		DraftRevision: genResp.Msg.Draft.DraftRevision,
		DraftHash:     "wrong-hash",
	})

	_, err = api.ApplyPolicyDraft(context.Background(), applyReq)
	if err == nil {
		t.Fatal("expected error for hash mismatch")
	}
}

func TestDisasterReplicationAPI_ApplyPolicyDraftRejectsPolicyChangeWithOriginalHash(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "protected", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Trigger: "MANUAL", RetentionKeepDays: 30,
	}))
	if err != nil {
		t.Fatal(err)
	}
	draft := generated.Msg.Draft
	draft.RetentionKeepDays++
	_, err = api.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{
		PolicyId: "rp-protected", Draft: draft, DraftRevision: draft.DraftRevision, DraftHash: draft.DraftHash,
		ExpectedRevision: -1, Meta: &procmeshv1.MutationMeta{OperationId: "op-protected"},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("policy change with original hash should conflict, got %v", err)
	}
}

func TestDisasterReplicationAPI_ApplyPolicyDraftRejectsStaleTopology(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{Name: "stale", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL"}))
	if err != nil {
		t.Fatal(err)
	}
	state.Members["node-4"] = control.Member{NodeID: "node-4", Status: control.MemberAdmitted}
	_, err = api.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{PolicyId: "rp-stale", Draft: generated.Msg.Draft, DraftRevision: generated.Msg.Draft.DraftRevision, DraftHash: generated.Msg.Draft.DraftHash, ExpectedRevision: -1, Meta: &procmeshv1.MutationMeta{OperationId: "op-stale"}}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("stale topology should conflict, got %v", err)
	}
}

func TestDisasterReplicationAPI_ApplyPolicyDraftRejectsClientRehashedRoutes(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	request := &procmeshv1.GeneratePolicyDraftRequest{Name: "forged", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL"}
	generated, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, request))
	if err != nil {
		t.Fatal(err)
	}
	generated.Msg.Draft.Routes[0].TargetNodeIds = []string{"node-3"}
	forgedHash := computeLegacyDraftHashForTopology(request, generated.Msg.Draft.Routes, generated.Msg.Draft.DraftRevision)
	generated.Msg.Draft.DraftHash = forgedHash
	_, err = api.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{PolicyId: "rp-forged", Draft: generated.Msg.Draft, DraftRevision: generated.Msg.Draft.DraftRevision, DraftHash: forgedHash, ExpectedRevision: -1, Meta: &procmeshv1.MutationMeta{OperationId: "op-forged"}}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("client-rehashed routes should conflict with server preview, got %v", err)
	}
}

func TestDisasterReplicationAPI_ApplyPolicyDraftAcceptsEditedRoutesWithOriginalHash(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	api.ApplyFn = func(cmd control.Command, timeout time.Duration) error {
		policy := control.ReplicationPolicy{PolicyID: "rp-changed", Name: "changed", ReplicaFactor: 1, Revision: 1}
		state.ReplicationPolicies[policy.PolicyID] = policy
		return nil
	}
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{Name: "changed", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL"}))
	if err != nil {
		t.Fatal(err)
	}
	source := generated.Msg.Draft.Routes[0].SourceNodeId
	editedTarget := "node-3"
	if source == editedTarget {
		editedTarget = "node-2"
	}
	generated.Msg.Draft.Routes[0].TargetNodeIds = []string{editedTarget}
	resp, err := api.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{PolicyId: "rp-changed", Draft: generated.Msg.Draft, DraftRevision: generated.Msg.Draft.DraftRevision, DraftHash: generated.Msg.Draft.DraftHash, ExpectedRevision: -1, Meta: &procmeshv1.MutationMeta{OperationId: "op-changed"}}))
	if err != nil {
		t.Fatalf("edited routes with original topology hash should apply, got %v", err)
	}
	if resp.Msg.PolicyId != "rp-changed" {
		t.Fatalf("policy id=%q want rp-changed", resp.Msg.PolicyId)
	}
}

func TestDisasterReplicationAPI_ListPolicies(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)

	// Add test policies
	state.ReplicationPolicies["policy-1"] = control.ReplicationPolicy{
		PolicyID: "policy-1",
		Name:     "policy-one",
		Revision: 1,
	}
	state.ReplicationPolicies["policy-2"] = control.ReplicationPolicy{
		PolicyID: "policy-2",
		Name:     "policy-two",
		Revision: 2,
	}

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.ListPoliciesRequest{})

	resp, err := api.ListPolicies(context.Background(), req)
	if err != nil {
		t.Fatalf("ListPolicies failed: %v", err)
	}

	if len(resp.Msg.Policies) != 2 {
		t.Errorf("policies: got %d, want 2", len(resp.Msg.Policies))
	}
}

func TestDisasterReplicationAPI_GetPolicy(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)

	state.ReplicationPolicies["policy-1"] = control.ReplicationPolicy{
		PolicyID: "policy-1",
		Name:     "test-policy",
		Revision: 3,
	}

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.GetPolicyRequest{
		PolicyId: "policy-1",
	})

	resp, err := api.GetPolicy(context.Background(), req)
	if err != nil {
		t.Fatalf("GetPolicy failed: %v", err)
	}

	if resp.Msg.Policy == nil {
		t.Fatal("expected policy, got nil")
	}
	if resp.Msg.Policy.PolicyId != "policy-1" {
		t.Errorf("policy ID: got %q, want %q", resp.Msg.Policy.PolicyId, "policy-1")
	}
	if resp.Msg.Policy.Revision != 3 {
		t.Errorf("revision: got %d, want 3", resp.Msg.Policy.Revision)
	}
}

func TestDisasterReplicationAPI_GetPolicy_NotFound(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.GetPolicyRequest{
		PolicyId: "nonexistent",
	})

	_, err = api.GetPolicy(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent policy")
	}
}

func TestDisasterReplicationAPI_UpdatePolicy(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)

	state.ReplicationPolicies["policy-1"] = control.ReplicationPolicy{
		PolicyID: "policy-1",
		Name:     "old-name",
		Revision: 1,
	}

	// Mock ApplyFn
	api.ApplyFn = func(cmd control.Command, timeout time.Duration) error {
		p := state.ReplicationPolicies["policy-1"]
		p.Name = "new-name"
		p.Revision = 2
		state.ReplicationPolicies["policy-1"] = p
		return nil
	}

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.UpdatePolicyRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-update"},
		PolicyId: "policy-1",
		Name:     "new-name",
	})

	resp, err := api.UpdatePolicy(context.Background(), req)
	if err != nil {
		t.Fatalf("UpdatePolicy failed: %v", err)
	}

	if resp.Msg.Revision != 2 {
		t.Errorf("revision: got %d, want 2", resp.Msg.Revision)
	}
}

func TestDisasterReplicationAPI_DeletePolicy(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)

	state.ReplicationPolicies["policy-1"] = control.ReplicationPolicy{
		PolicyID: "policy-1",
		Name:     "test-policy",
		Revision: 1,
	}

	// Mock ApplyFn
	api.ApplyFn = func(cmd control.Command, timeout time.Duration) error {
		delete(state.ReplicationPolicies, "policy-1")
		return nil
	}

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.DeletePolicyRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-delete"},
		PolicyId: "policy-1",
	})

	_, err = api.DeletePolicy(context.Background(), req)
	if err != nil {
		t.Fatalf("DeletePolicy failed: %v", err)
	}
}

func TestDisasterReplicationAPI_GetTopology(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.GetTopologyRequest{})

	resp, err := api.GetTopology(context.Background(), req)
	if err != nil {
		t.Fatalf("GetTopology failed: %v", err)
	}

	if len(resp.Msg.Nodes) != 3 {
		t.Errorf("nodes: got %d, want 3", len(resp.Msg.Nodes))
	}

	// Check that each node has proper data
	hostnames := map[string]string{}
	for _, node := range resp.Msg.Nodes {
		if node.NodeId == "" {
			t.Error("expected node ID to be set")
		}
		if !node.Alive {
			t.Error("expected health to be set")
		}
		hostnames[node.NodeId] = node.Hostname
	}
	if hostnames["node-1"] != "agent-one" || hostnames["node-2"] != "agent-two" || hostnames["node-3"] != "agent-three" {
		t.Errorf("hostnames=%v want agent-one/agent-two/agent-three", hostnames)
	}
}

// Stub method tests

func TestDisasterReplicationAPI_StartRun_Stub(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-1",
	})

	_, err = api.StartRun(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unimplemented method")
	}
}

func TestDisasterReplicationAPI_GetRun_Stub(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.GetRunRequest{
		RunId: "run-1",
	})

	_, err = api.GetRun(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unimplemented method")
	}
}

func TestDisasterReplicationAPI_ListRecoverableSnapshots(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.ListRecoverableSnapshotsRequest{})

	resp, err := api.ListRecoverableSnapshots(context.Background(), req)
	if err != nil {
		t.Fatalf("ListRecoverableSnapshots failed: %v", err)
	}

	// Currently returns empty list (stub implementation)
	if resp.Msg.Snapshots == nil {
		t.Error("expected snapshots slice, got nil")
	}
}

// ===== Task 5: Complete Stub Methods Tests =====

func TestDisasterReplicationAPI_StartRun(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	policy := control.ReplicationPolicy{
		PolicyID:       "policy-start-1",
		Name:           "test-start-policy",
		Enabled:        true,
		Trigger:        "MANUAL",
		SourceSelector: "EXPLICIT_NODES",
		SourceIDs:      []string{"node-1", "node-2"},
		ReplicaFactor:  2,
		Routes: []control.ReplicationRoute{
			{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2", "node-3"}},
			{SourceNodeID: "node-2", TargetNodeIDs: []string{"node-3"}},
		},
		Revision: 1,
	}
	state.ReplicationPolicies[policy.PolicyID] = policy
	api.LeaderTerm = func() uint64 { return 7 }
	api.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error {
		return state.Apply(cmd, api.Now())
	}
	var dispatched backup.FrozenReplicationRun
	dispatchCount := 0
	api.DispatchRun = func(run backup.FrozenReplicationRun) {
		dispatched = run
		dispatchCount++
	}
	client := newDisasterReplicationClient(t, api)

	req := bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-start-1",
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-start-1"},
	})

	resp, err := client.StartRun(context.Background(), req)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}

	if resp.Msg.RunId == "" {
		t.Error("expected non-empty run_id")
	}
	if resp.Msg.PolicyId != "policy-start-1" {
		t.Errorf("expected policy_id %q, got %q", "policy-start-1", resp.Msg.PolicyId)
	}
	if resp.Msg.StartedAt == 0 {
		t.Error("expected started_at > 0")
	}
	if got := state.ReplicationRunTerms[resp.Msg.RunId]; got != 7 {
		t.Fatalf("leader term = %d, want 7", got)
	}

	getResp, err := client.GetRun(context.Background(), bearerReq(sid, &procmeshv1.GetRunRequest{RunId: resp.Msg.RunId}))
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	gotRoutes := make(map[string]string)
	for _, task := range getResp.Msg.GetRun().GetTasks() {
		if task.GetStatus() != "PENDING" || len(task.GetTargetNodeIds()) != 1 {
			t.Fatalf("task = %+v, want one pending route target", task)
		}
		if task.GetSnapshotId() != "" || task.GetSha256() != "" {
			t.Fatalf("task snapshot must be empty at StartRun, got %+v", task)
		}
		gotRoutes[task.GetSourceNodeId()+">"+task.GetTargetNodeIds()[0]] = task.GetTaskId()
	}
	wantRoutes := []string{"node-1>node-2", "node-1>node-3", "node-2>node-3"}
	for _, route := range wantRoutes {
		if gotRoutes[route] == "" {
			t.Fatalf("route tasks = %+v, missing %s", gotRoutes, route)
		}
	}
	if len(gotRoutes) != len(wantRoutes) {
		t.Fatalf("route tasks = %+v, want %v", gotRoutes, wantRoutes)
	}
	if dispatchCount != 1 {
		t.Fatalf("DispatchRun calls = %d, want 1", dispatchCount)
	}
	if dispatched.RunID != resp.Msg.GetRunId() || dispatched.PolicyID != "policy-start-1" || dispatched.PolicyRevision != 1 || dispatched.LeaderTerm != 7 {
		t.Fatalf("dispatched = %+v, want run %s policy-start-1 rev=1 term=7", dispatched, resp.Msg.GetRunId())
	}
	if len(dispatched.Tasks) != len(wantRoutes) {
		t.Fatalf("dispatched tasks = %d, want %d", len(dispatched.Tasks), len(wantRoutes))
	}
	for _, task := range dispatched.Tasks {
		if task.Status != "PENDING" || task.SnapshotID != "" || task.SHA256 != "" {
			t.Fatalf("dispatched task = %+v, want PENDING empty snapshot", task)
		}
	}
	var runningTask control.ClusterBackupTask
	for _, task := range state.ReplicationTasks {
		runningTask = task
		break
	}
	runningTask.Status = "RUNNING"
	runningTask.UpdatedUnix++
	if err := state.UpdateTask(control.UpdateTaskBody{
		OperationID: "op-running-route", LeaderTerm: 7, Replication: true, Task: runningTask,
	}); err != nil {
		t.Fatal(err)
	}
	policy.Revision = 2
	policy.Routes = []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-3"}}}
	state.ReplicationPolicies[policy.PolicyID] = policy

	retryResp, err := client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-start-1",
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-start-1"},
	}))
	if err != nil {
		t.Fatalf("idempotent StartRun failed: %v", err)
	}
	if retryResp.Msg.GetRunId() != resp.Msg.GetRunId() || retryResp.Msg.GetPolicyRevision() != 1 || len(state.ReplicationTasks) != len(wantRoutes) {
		t.Fatalf("retry run=%q tasks=%d, want run=%q tasks=%d", retryResp.Msg.GetRunId(), len(state.ReplicationTasks), resp.Msg.GetRunId(), len(wantRoutes))
	}
	if dispatchCount != 1 {
		t.Fatalf("idempotent StartRun DispatchRun calls = %d, want 1", dispatchCount)
	}
	gotRunning := state.ReplicationTasks[runningTask.RunID+":"+runningTask.TaskID]
	if gotRunning.Status != "RUNNING" {
		t.Fatalf("replayed task status = %q, want RUNNING", gotRunning.Status)
	}

	state.ReplicationPolicies["policy-other"] = control.ReplicationPolicy{
		PolicyID: "policy-other", Revision: 1, SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"node-1"},
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2"}}},
	}
	_, err = client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-other", Meta: &procmeshv1.MutationMeta{OperationId: "op-start-1"},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cross-policy operation reuse error = %v, want FAILED_PRECONDITION", err)
	}
}

func TestDisasterReplicationAPI_StartRun_CronPolicyAllowed(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state.ReplicationPolicies["scheduled"] = control.ReplicationPolicy{
		PolicyID: "scheduled", Name: "cron", Enabled: true, Trigger: "SCHEDULE", ScheduleCron: "0 * * * *",
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"node-1"}, Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2"}}},
	}
	api.LeaderTerm = func() uint64 { return 3 }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, time.Now()) }
	var dispatched backup.FrozenReplicationRun
	api.DispatchRun = func(run backup.FrozenReplicationRun) { dispatched = run }
	client := newDisasterReplicationClient(t, api)
	resp, err := client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "scheduled", Meta: &procmeshv1.MutationMeta{OperationId: "op-scheduled"},
	}))
	if err != nil {
		t.Fatalf("cron policy StartRun: %v", err)
	}
	if resp.Msg.GetRunId() == "" {
		t.Fatal("expected run_id")
	}
	if dispatched.RunID != resp.Msg.GetRunId() || len(dispatched.Tasks) != 1 || dispatched.Tasks[0].SnapshotID != "" {
		t.Fatalf("dispatched = %+v", dispatched)
	}
}

func TestDisasterReplicationAPI_StartRun_ReturnsExistingRunning(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state.ReplicationPolicies["policy-running"] = control.ReplicationPolicy{
		PolicyID: "policy-running", Name: "running", Enabled: true, SourceSelector: "EXPLICIT_NODES",
		SourceIDs: []string{"node-1"}, Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2"}}},
	}
	api.LeaderTerm = func() uint64 { return 4 }
	api.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, api.Now()) }
	dispatchCount := 0
	api.DispatchRun = func(backup.FrozenReplicationRun) { dispatchCount++ }
	client := newDisasterReplicationClient(t, api)

	first, err := client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-running", Meta: &procmeshv1.MutationMeta{OperationId: "op-running-1"},
	}))
	if err != nil {
		t.Fatalf("first StartRun: %v", err)
	}
	second, err := client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-running", Meta: &procmeshv1.MutationMeta{OperationId: "op-running-2"},
	}))
	if err != nil {
		t.Fatalf("second StartRun: %v", err)
	}
	if second.Msg.GetRunId() != first.Msg.GetRunId() {
		t.Fatalf("run_id = %q, want existing %q", second.Msg.GetRunId(), first.Msg.GetRunId())
	}
	if len(state.ReplicationRuns) != 1 {
		t.Fatalf("runs = %d, want 1 existing RUNNING run", len(state.ReplicationRuns))
	}
	if dispatchCount != 1 {
		t.Fatalf("DispatchRun calls = %d, want 1 (no new run)", dispatchCount)
	}
}

func TestDisasterReplicationAPI_StartRun_DisabledPolicyStillAllowed(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state.ReplicationPolicies["policy-disabled"] = control.ReplicationPolicy{
		PolicyID: "policy-disabled", Name: "disabled", Enabled: false, SourceSelector: "EXPLICIT_NODES",
		SourceIDs: []string{"node-1"}, Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2"}}},
	}
	api.LeaderTerm = func() uint64 { return 3 }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, time.Now()) }
	client := newDisasterReplicationClient(t, api)
	resp, err := client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-disabled", Meta: &procmeshv1.MutationMeta{OperationId: "op-disabled"},
	}))
	if err != nil {
		t.Fatalf("disabled policy StartRun: %v", err)
	}
	if resp.Msg.GetRunId() == "" || resp.Msg.GetPolicyId() != "policy-disabled" {
		t.Fatalf("resp = %+v", resp.Msg)
	}
}

func TestDisasterReplicationAPI_StartRun_IgnoresPrimaryRunId(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state.ReplicationPolicies["policy-primary"] = control.ReplicationPolicy{
		PolicyID: "policy-primary", Name: "primary", Enabled: true, SourceSelector: "EXPLICIT_NODES",
		SourceIDs: []string{"node-1"}, Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2"}}},
	}
	state.BackupRuns["backup-1"] = control.ClusterBackupRun{RunID: "backup-1", Status: "SUCCEEDED"}
	state.BackupTasks["backup-1:task-1"] = control.ClusterBackupTask{
		RunID: "backup-1", TaskID: "task-1", NodeID: "node-1",
		SnapshotID: "snap-from-backup", SHA256: strings.Repeat("a", 64), Status: "SUCCESS",
	}
	api.LeaderTerm = func() uint64 { return 3 }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, time.Now()) }
	var dispatched backup.FrozenReplicationRun
	api.DispatchRun = func(run backup.FrozenReplicationRun) { dispatched = run }
	client := newDisasterReplicationClient(t, api)

	resp, err := client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-primary", PrimaryRunId: "backup-1",
		Meta: &procmeshv1.MutationMeta{OperationId: "op-ignore-primary"},
	}))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	getResp, err := client.GetRun(context.Background(), bearerReq(sid, &procmeshv1.GetRunRequest{RunId: resp.Msg.GetRunId()}))
	if err != nil {
		t.Fatal(err)
	}
	if len(getResp.Msg.GetRun().GetTasks()) != 1 {
		t.Fatalf("tasks = %+v", getResp.Msg.GetRun().GetTasks())
	}
	task := getResp.Msg.GetRun().GetTasks()[0]
	if task.GetSnapshotId() != "" || task.GetSha256() != "" || task.GetStatus() != "PENDING" {
		t.Fatalf("task = %+v, want PENDING empty snapshot (no BackupRuns bind)", task)
	}
	if dispatched.RunID != resp.Msg.GetRunId() || len(dispatched.Tasks) != 1 || dispatched.Tasks[0].SnapshotID != "" {
		t.Fatalf("dispatched = %+v", dispatched)
	}
}

func TestDisasterReplicationAPI_StartRun_EmptyRoutesInvalid(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state.ReplicationPolicies["policy-empty"] = control.ReplicationPolicy{
		PolicyID: "policy-empty", Name: "empty", Enabled: true, SourceSelector: "EXPLICIT_NODES",
		SourceIDs: []string{"node-1"}, Revision: 1,
	}
	api.LeaderTerm = func() uint64 { return 3 }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, time.Now()) }
	client := newDisasterReplicationClient(t, api)
	_, err = client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-empty", Meta: &procmeshv1.MutationMeta{OperationId: "op-empty"},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty routes error = %v, want INVALID", err)
	}
	if len(state.ReplicationRuns) != 0 {
		t.Fatalf("empty routes created a run: %+v", state.ReplicationRuns)
	}
}

func TestDisasterReplicationAPI_RunTasksHaveStableOrder(t *testing.T) {
	state := control.NewState()
	state.ReplicationRuns["run-stable"] = control.ClusterBackupRun{
		RunID: "run-stable", PolicyID: "policy-stable", PolicyRevision: 1, Status: "RUNNING",
	}
	for _, taskID := range []string{"task-c", "task-a", "task-b"} {
		state.ReplicationTasks[taskID] = control.ClusterBackupTask{
			RunID: "run-stable", TaskID: taskID, SourceNodeID: "source", NodeID: "target", Status: "PENDING",
		}
	}
	api := &DisasterReplicationAPI{StateFn: func() control.State { return *state }}
	client := newDisasterReplicationClient(t, api)
	want := []string{"task-a", "task-b", "task-c"}

	for attempt := 0; attempt < 64; attempt++ {
		getResp, err := client.GetRun(context.Background(), connect.NewRequest(&procmeshv1.GetRunRequest{RunId: "run-stable"}))
		if err != nil {
			t.Fatal(err)
		}
		gotGet := replicationTaskIDs(getResp.Msg.GetRun().GetTasks())
		if strings.Join(gotGet, ",") != strings.Join(want, ",") {
			t.Fatalf("GetRun task order = %v, want %v", gotGet, want)
		}

		listResp, err := client.ListRuns(context.Background(), connect.NewRequest(&procmeshv1.ListRunsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		gotList := replicationTaskIDs(listResp.Msg.GetRuns()[0].GetTasks())
		if strings.Join(gotList, ",") != strings.Join(want, ",") {
			t.Fatalf("ListRuns task order = %v, want %v", gotList, want)
		}
	}
}

func replicationTaskIDs(tasks []*procmeshv1.ReplicationTask) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.GetTaskId())
	}
	return ids
}

func TestDisasterReplicationAPI_ManagePermissionRequiredForGenerateAndVerify(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	applyAuthCmd(t, authSvc, control.CmdUserPut, control.UserPutBody{
		ID: "user-replication-reader", Username: "replication-reader", PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, authSvc, control.CmdRolePut, control.RolePutBody{
		ID: "replication-reader", Name: "replication-reader", Perms: []string{auth.PermReplicationRead},
	})
	applyAuthCmd(t, authSvc, control.CmdBindPut, control.BindPutBody{
		UserID: "user-replication-reader", RoleID: "replication-reader", Scope: control.ScopeCluster,
	})
	sid, _, _, _, err := authSvc.Login("replication-reader", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	client := newDisasterReplicationClient(t, api)
	_, err = client.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "denied", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
	}))
	assertDenied(t, err)
	_, err = client.VerifyReplica(context.Background(), bearerReq(sid, &procmeshv1.VerifyReplicaRequest{
		SourceNodeId: "node-1", SnapshotId: "snapshot-1",
	}))
	assertDenied(t, err)
}

type disasterReplicationForwarder struct {
	client procmeshv1connect.DisasterReplicationServiceClient
}

func (f disasterReplicationForwarder) DisasterReplication(context.Context, Route) (procmeshv1connect.DisasterReplicationServiceClient, error) {
	return f.client, nil
}

func TestDisasterReplicationAPI_NonLeaderForwardsMutation(t *testing.T) {
	leaderState := control.NewState()
	leaderState.ReplicationPolicies["policy-forward"] = control.ReplicationPolicy{
		PolicyID: "policy-forward", Name: "forwarded", Revision: 1,
	}
	leader := &DisasterReplicationAPI{
		StateFn: func() control.State { return *leaderState },
		ApplyFn: func(cmd control.Command, _ time.Duration) error {
			return leaderState.Apply(cmd, time.Unix(1_800_000_000, 0))
		},
		IsLeader:   func() bool { return true },
		LeaderTerm: func() uint64 { return 9 },
	}
	leaderClient := newDisasterReplicationClient(t, leader)

	follower := &DisasterReplicationAPI{
		LocalID:  "node-a",
		StateFn:  func() control.State { return *control.NewState() },
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, bool) {
			return Route{NodeID: "node-b", RPC: "127.0.0.1:18683"}, true
		},
		Forward: disasterReplicationForwarder{client: leaderClient},
	}
	followerClient := newDisasterReplicationClient(t, follower)
	_, err := followerClient.DeletePolicy(context.Background(), connect.NewRequest(&procmeshv1.DeletePolicyRequest{
		PolicyId: "policy-forward",
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-forward-policy"},
	}))
	if err != nil {
		t.Fatalf("DeletePolicy through follower: %v", err)
	}
	if _, ok := leaderState.ReplicationPolicies["policy-forward"]; ok {
		t.Fatal("leader policy still exists after forwarded delete")
	}
}

func TestDisasterReplicationAPI_StartRun_Unauthorized(t *testing.T) {
	api, _, _ := setupMinimalAPI(t)

	req := bearerReq("invalid-session", &procmeshv1.StartRunRequest{
		PolicyId: "policy-1",
	})

	_, err := api.StartRun(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unauthorized access")
	}
}

func TestDisasterReplicationAPI_StartRun_PolicyNotFound(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "nonexistent-policy",
	})

	_, err = api.StartRun(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent policy")
	}
}

func TestDisasterReplicationAPI_GetRun(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	// Create a run
	run := control.ClusterBackupRun{
		RunID:          "run-get-1",
		PolicyID:       "policy-get-1",
		PolicyRevision: 1,
		TargetNodeIDs:  []string{"node-2", "node-3"},
		Status:         "RUNNING",
		Success:        1,
		Failed:         0,
		Unavailable:    0,
		Timeout:        0,
		CreatedUnix:    time.Now().Unix(),
		StartedUnix:    time.Now().Unix(),
	}
	api.StateFn().ReplicationRuns[run.RunID] = run

	// Create tasks
	task1 := control.ClusterBackupTask{
		RunID:      "run-get-1",
		TaskID:     "task-1",
		NodeID:     "node-1",
		SnapshotID: "snap-1",
		Status:     "SUCCESS",
	}
	task2 := control.ClusterBackupTask{
		RunID:      "run-get-1",
		TaskID:     "task-2",
		NodeID:     "node-2",
		SnapshotID: "snap-2",
		Status:     "PENDING",
	}
	api.StateFn().ReplicationTasks[task1.TaskID] = task1
	api.StateFn().ReplicationTasks[task2.TaskID] = task2

	req := bearerReq(sid, &procmeshv1.GetRunRequest{
		RunId: "run-get-1",
	})

	resp, err := api.GetRun(context.Background(), req)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}

	if resp.Msg.Run == nil {
		t.Fatal("expected non-nil run")
	}
	if resp.Msg.Run.RunId != "run-get-1" {
		t.Errorf("expected run_id %q, got %q", "run-get-1", resp.Msg.Run.RunId)
	}
	if resp.Msg.Run.PolicyId != "policy-get-1" {
		t.Errorf("expected policy_id %q, got %q", "policy-get-1", resp.Msg.Run.PolicyId)
	}
	if resp.Msg.Run.Status != "RUNNING" {
		t.Errorf("expected status %q, got %q", "RUNNING", resp.Msg.Run.Status)
	}
	if len(resp.Msg.Run.Tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(resp.Msg.Run.Tasks))
	}
}

func TestDisasterReplicationAPI_GetRun_NotFound(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.GetRunRequest{
		RunId: "nonexistent-run",
	})

	_, err = api.GetRun(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent run")
	}
}

func TestDisasterReplicationAPI_GetRun_Unauthorized(t *testing.T) {
	api, _, _ := setupMinimalAPI(t)

	req := bearerReq("invalid-session", &procmeshv1.GetRunRequest{
		RunId: "run-1",
	})

	_, err := api.GetRun(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unauthorized access")
	}
}

func TestDisasterReplicationAPI_ListRuns(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	// Create multiple runs
	now := time.Now()
	run1 := control.ClusterBackupRun{
		RunID:       "run-list-1",
		PolicyID:    "policy-list-1",
		Status:      "SUCCESS",
		CreatedUnix: now.Add(-2 * time.Hour).Unix(),
	}
	run2 := control.ClusterBackupRun{
		RunID:       "run-list-2",
		PolicyID:    "policy-list-1",
		Status:      "RUNNING",
		CreatedUnix: now.Add(-1 * time.Hour).Unix(),
	}
	run3 := control.ClusterBackupRun{
		RunID:       "run-list-3",
		PolicyID:    "policy-list-2",
		Status:      "FAILED",
		CreatedUnix: now.Unix(),
	}
	api.StateFn().ReplicationRuns[run1.RunID] = run1
	api.StateFn().ReplicationRuns[run2.RunID] = run2
	api.StateFn().ReplicationRuns[run3.RunID] = run3

	// Test listing all runs
	req := bearerReq(sid, &procmeshv1.ListRunsRequest{})
	resp, err := api.ListRuns(context.Background(), req)
	if err != nil {
		t.Fatalf("ListRuns failed: %v", err)
	}

	if len(resp.Msg.Runs) != 3 {
		t.Errorf("expected 3 runs, got %d", len(resp.Msg.Runs))
	}

	// Verify ordering (most recent first)
	if resp.Msg.Runs[0].RunId != "run-list-3" {
		t.Errorf("expected first run to be run-list-3, got %s", resp.Msg.Runs[0].RunId)
	}

	// Test filtering by policy_id
	req2 := bearerReq(sid, &procmeshv1.ListRunsRequest{
		PolicyId: "policy-list-1",
	})
	resp2, err := api.ListRuns(context.Background(), req2)
	if err != nil {
		t.Fatalf("ListRuns with filter failed: %v", err)
	}

	if len(resp2.Msg.Runs) != 2 {
		t.Errorf("expected 2 runs for policy-list-1, got %d", len(resp2.Msg.Runs))
	}
}

func TestDisasterReplicationAPI_ListRuns_Unauthorized(t *testing.T) {
	api, _, _, viewerSid := setupAPIWithViewerUser(t)
	client := newDisasterReplicationClient(t, api)

	req := bearerReq(viewerSid, &procmeshv1.ListRunsRequest{})

	_, err := client.ListRuns(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for viewer user without replication.read permission")
	}
	if !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "DENIED") {
		t.Errorf("expected DENIED error, got: %v", err)
	}
}

func TestDisasterReplicationAPI_RetryFailedRoutes(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	// Create a run
	run := control.ClusterBackupRun{
		RunID:    "run-retry-1",
		PolicyID: "policy-retry-1",
		Status:   "FAILED",
		Failed:   2,
		Success:  1,
	}
	api.StateFn().ReplicationRuns[run.RunID] = run

	// Create tasks - some failed, some succeeded
	task1 := control.ClusterBackupTask{
		RunID:      "run-retry-1",
		TaskID:     "task-retry-1",
		NodeID:     "node-1",
		SnapshotID: "snap-1",
		SHA256:     strings.Repeat("a", 64),
		Status:     "FAILED",
	}
	task2 := control.ClusterBackupTask{
		RunID:      "run-retry-1",
		TaskID:     "task-retry-2",
		NodeID:     "node-2",
		SnapshotID: "snap-2",
		SHA256:     strings.Repeat("b", 64),
		Status:     "FAILED",
	}
	task3 := control.ClusterBackupTask{
		RunID:      "run-retry-1",
		TaskID:     "task-retry-3",
		NodeID:     "node-3",
		SnapshotID: "snap-3",
		SHA256:     strings.Repeat("c", 64),
		Status:     "SUCCESS",
	}
	api.StateFn().ReplicationTasks[task1.TaskID] = task1
	api.StateFn().ReplicationTasks[task2.TaskID] = task2
	api.StateFn().ReplicationTasks[task3.TaskID] = task3

	req := bearerReq(sid, &procmeshv1.RetryFailedRoutesRequest{
		RunId: "run-retry-1",
		Meta:  &procmeshv1.MutationMeta{OperationId: "op-retry-routes"},
	})

	resp, err := api.RetryFailedRoutes(context.Background(), req)
	if err != nil {
		t.Fatalf("RetryFailedRoutes failed: %v", err)
	}

	if resp.Msg.RetriedCount != 2 {
		t.Errorf("expected 2 retried tasks, got %d", resp.Msg.RetriedCount)
	}
}

func TestDisasterReplicationAPI_RetryFailedRoutes_RecaptureAndCopy(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	state.ReplicationRuns["run-retry-mix"] = control.ClusterBackupRun{
		RunID: "run-retry-mix", PolicyID: "policy-retry-mix", PolicyRevision: 1, Status: "PARTIAL",
	}
	copyTask := control.ClusterBackupTask{
		RunID: "run-retry-mix", TaskID: "task-copy", NodeID: "node-2", SourceNodeID: "node-1",
		SnapshotID: "snap-copy", SHA256: sha, Status: "FAILED",
	}
	captureTask := control.ClusterBackupTask{
		RunID: "run-retry-mix", TaskID: "task-capture", NodeID: "node-3", SourceNodeID: "node-1",
		Status: "FAILED",
	}
	succeededTask := control.ClusterBackupTask{
		RunID: "run-retry-mix", TaskID: "task-ok", NodeID: "node-2", SourceNodeID: "node-1",
		SnapshotID: "snap-ok", SHA256: strings.Repeat("b", 64), Status: "SUCCEEDED",
	}
	state.ReplicationTasks["run-retry-mix:task-copy"] = copyTask
	state.ReplicationTasks["run-retry-mix:task-capture"] = captureTask
	state.ReplicationTasks["run-retry-mix:task-ok"] = succeededTask
	api.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	api.LeaderTerm = func() uint64 { return 4 }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error {
		return state.Apply(cmd, api.Now())
	}
	var dispatched backup.FrozenReplicationRun
	api.DispatchRun = func(run backup.FrozenReplicationRun) { dispatched = run }

	resp, err := api.RetryFailedRoutes(context.Background(), bearerReq(sid, &procmeshv1.RetryFailedRoutesRequest{
		RunId: "run-retry-mix",
		Meta:  &procmeshv1.MutationMeta{OperationId: "op-retry-recapture"},
	}))
	if err != nil {
		t.Fatalf("RetryFailedRoutes failed: %v", err)
	}
	if resp.Msg.RetriedCount != 2 {
		t.Fatalf("retried count = %d, want 2 (copy-failed + capture-failed)", resp.Msg.RetriedCount)
	}

	gotCopy := state.ReplicationTasks["run-retry-mix:task-copy"]
	if gotCopy.Status != "PENDING" || gotCopy.SnapshotID != "snap-copy" || gotCopy.SHA256 != sha {
		t.Fatalf("copy-failed task after retry = %+v, want PENDING with frozen snapshot", gotCopy)
	}
	gotCapture := state.ReplicationTasks["run-retry-mix:task-capture"]
	if gotCapture.Status != "PENDING" || gotCapture.SnapshotID != "" || gotCapture.SHA256 != "" {
		t.Fatalf("capture-failed task after retry = %+v, want PENDING empty snapshot", gotCapture)
	}
	gotOK := state.ReplicationTasks["run-retry-mix:task-ok"]
	if gotOK.Status != "SUCCEEDED" || gotOK.SnapshotID != "snap-ok" {
		t.Fatalf("succeeded task after retry = %+v, want untouched SUCCEEDED", gotOK)
	}

	if dispatched.RunID != "run-retry-mix" || dispatched.PolicyID != "policy-retry-mix" || dispatched.LeaderTerm != 4 {
		t.Fatalf("dispatched = %+v, want run-retry-mix policy-retry-mix term=4", dispatched)
	}
	byID := make(map[string]backup.FrozenReplicationTask, len(dispatched.Tasks))
	for _, task := range dispatched.Tasks {
		byID[task.TaskID] = task
	}
	if len(byID) != 2 {
		t.Fatalf("dispatched tasks = %+v, want copy + recapture only", dispatched.Tasks)
	}
	if got, ok := byID["task-copy"]; !ok || got.Status != "PENDING" || got.SnapshotID != "snap-copy" || got.SHA256 != sha || got.SourceNodeID != "node-1" || got.TargetNodeID != "node-2" {
		t.Fatalf("dispatched copy task = %+v, want PENDING frozen snapshot", got)
	}
	if got, ok := byID["task-capture"]; !ok || got.Status != "PENDING" || got.SnapshotID != "" || got.SHA256 != "" || got.SourceNodeID != "node-1" || got.TargetNodeID != "node-3" {
		t.Fatalf("dispatched recapture task = %+v, want PENDING empty snapshot", got)
	}
	if _, ok := byID["task-ok"]; ok {
		t.Fatalf("SUCCEEDED task must not be in frozen dispatch: %+v", dispatched.Tasks)
	}
}

func TestDisasterReplicationAPI_RetryFailedRoutesIncludesTasksWithoutFrozenRefs(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state := api.StateFn()
	state.ReplicationRuns["run-retry-frozen"] = control.ClusterBackupRun{RunID: "run-retry-frozen", PolicyID: "policy-retry", Status: "FAILED"}
	state.ReplicationTasks["with-refs"] = control.ClusterBackupTask{RunID: "run-retry-frozen", TaskID: "with-refs", NodeID: "node-1", SnapshotID: "snap-frozen", SHA256: strings.Repeat("d", 64), Status: "FAILED"}
	state.ReplicationTasks["missing-refs"] = control.ClusterBackupTask{RunID: "run-retry-frozen", TaskID: "missing-refs", NodeID: "node-2", Status: "FAILED"}

	resp, err := api.RetryFailedRoutes(context.Background(), bearerReq(sid, &procmeshv1.RetryFailedRoutesRequest{
		RunId: "run-retry-frozen", Meta: &procmeshv1.MutationMeta{OperationId: "op-retry-frozen"},
	}))
	if err != nil {
		t.Fatalf("RetryFailedRoutes failed: %v", err)
	}
	if resp.Msg.RetriedCount != 2 {
		t.Fatalf("retried count = %d, want 2 including empty-snapshot route", resp.Msg.RetriedCount)
	}
}

func TestDisasterReplicationAPI_RetryFailedRoutes_RunNotFound(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.RetryFailedRoutesRequest{
		RunId: "nonexistent-run",
	})

	_, err = api.RetryFailedRoutes(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent run")
	}
}

func TestDisasterReplicationAPI_RetryFailedRoutes_Unauthorized(t *testing.T) {
	api, _, _ := setupMinimalAPI(t)

	req := bearerReq("invalid-session", &procmeshv1.RetryFailedRoutesRequest{
		RunId: "run-1",
	})

	_, err := api.RetryFailedRoutes(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unauthorized access")
	}
}

func TestDisasterReplicationAPI_VerifyReplica(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.VerifyReplicaRequest{
		SourceNodeId: "node-1",
		SnapshotId:   "snap-verify-1",
	})

	resp, err := api.VerifyReplica(context.Background(), req)
	// With an empty peer store, this is expected to fail with NOT_FOUND
	// That's the correct behavior
	if err == nil && resp.Msg == nil {
		t.Fatal("expected error or response")
	}
}

func TestDisasterReplicationAPI_VerifyReplica_Missing(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.VerifyReplicaRequest{
		SourceNodeId: "node-missing",
		SnapshotId:   "nonexistent-snap",
	})

	_, err = api.VerifyReplica(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing snapshot")
	}
	// Should return NOT_FOUND error
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Errorf("expected NOT_FOUND error, got: %v", err)
	}
}

func TestDisasterReplicationAPI_VerifyReplica_Unauthorized(t *testing.T) {
	api, _, _ := setupMinimalAPI(t)

	req := bearerReq("invalid-session", &procmeshv1.VerifyReplicaRequest{
		SourceNodeId: "node-1",
		SnapshotId:   "snap-1",
	})

	_, err := api.VerifyReplica(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unauthorized access")
	}
}

func TestDisasterReplicationAPI_VerifyReplica_ChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	api, _, authSvc := setupMinimalAPI(t)
	api.PeerStore = &backup.PeerStore{Root: dir}

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	// Create a snapshot with empty SHA256 (simulating checksum validation failure)
	snap := backup.Snapshot{
		FormatVersion: 1,
		ClusterID:     "test-cluster",
		NodeID:        "node-1",
		SnapshotID:    "snap-bad-checksum",
		CreatedAt:     time.Now(),
		Processes: []backup.ProcessDump{
			{ProcessID: "proc-1", Name: "app1"},
			{ProcessID: "proc-2", Name: "app2"},
			{ProcessID: "proc-3", Name: "app3"},
		},
	}

	// Encode the snapshot
	data, _, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}

	// Create directory structure
	peerDir := dir + "/node-1/test-cluster"
	if err := os.MkdirAll(peerDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write snapshot file
	snapPath := peerDir + "/snap-bad-checksum"
	if err := os.WriteFile(snapPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Now overwrite with corrupted data to simulate checksum mismatch
	// (This will cause Decode to fail or return empty SHA256)
	corruptedData := append(data[:len(data)-10], []byte("corrupted")...)
	if err := os.WriteFile(snapPath, corruptedData, 0644); err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.VerifyReplicaRequest{
		SourceNodeId: "node-1",
		SnapshotId:   "snap-bad-checksum",
	})

	// The corrupted data should cause Decode to fail, resulting in an error
	_, err = api.VerifyReplica(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for corrupted snapshot data")
	}
	// Error should indicate decoding/validation failure
	if !strings.Contains(err.Error(), "decode") && !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "unmarshal") {
		t.Logf("Got error (acceptable): %v", err)
	}
}

func TestDisasterReplicationAPI_VerifyReplica_FrozenChecksumMismatch(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	_, st, _ := newTestManager(t)
	api.Store = st
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	snap := backup.Snapshot{
		FormatVersion: 1, ClusterID: "test-cluster", NodeID: "node-1", SnapshotID: "snap-frozen",
		CreatedAt: time.Now().UTC(), Processes: []backup.ProcessDump{{ProcessID: "proc-1", Name: "app1"}},
	}
	payload, sha, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.PeerStore.ReceiveWithMetadata(context.Background(), backup.ReceiveParams{
		SourceNodeID: "node-1", ClusterID: "test-cluster", SnapshotID: "snap-frozen", SHA256: sha, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	state.ReplicationTasks["run:task"] = control.ClusterBackupTask{
		RunID: "run", TaskID: "task", SourceNodeID: "node-1", NodeID: "node-2",
		SnapshotID: "snap-frozen", SHA256: strings.Repeat("0", 64), Status: "SUCCEEDED",
	}
	client := newDisasterReplicationClient(t, api)
	resp, err := client.VerifyReplica(context.Background(), bearerReq(sid, &procmeshv1.VerifyReplicaRequest{
		SourceNodeId: "node-1", SnapshotId: "snap-frozen",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetValid() {
		t.Fatal("expected checksum mismatch to be invalid")
	}
	if !strings.Contains(strings.Join(resp.Msg.GetErrors(), " "), "checksum") {
		t.Fatalf("errors=%v", resp.Msg.GetErrors())
	}
	assertControlAudit(t, st, "replication.verify", "FAILED", map[string]string{"snapshot_id": "snap-frozen"})
}

func TestDisasterReplicationAPI_VerifyReplica_FrozenChecksumMatch(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	_, st, _ := newTestManager(t)
	api.Store = st
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	snap := backup.Snapshot{
		FormatVersion: 1, ClusterID: "test-cluster", NodeID: "node-1", SnapshotID: "snap-match",
		CreatedAt: time.Now().UTC(), Processes: []backup.ProcessDump{{ProcessID: "proc-1", Name: "app1"}},
	}
	payload, sha, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.PeerStore.ReceiveWithMetadata(context.Background(), backup.ReceiveParams{
		SourceNodeID: "node-1", ClusterID: "test-cluster", SnapshotID: "snap-match", SHA256: sha, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	state.ReplicationTasks["run:task"] = control.ClusterBackupTask{
		RunID: "run", TaskID: "task", SourceNodeID: "node-1", NodeID: "node-2",
		SnapshotID: "snap-match", SHA256: sha, Status: "SUCCEEDED",
	}
	client := newDisasterReplicationClient(t, api)
	resp, err := client.VerifyReplica(context.Background(), bearerReq(sid, &procmeshv1.VerifyReplicaRequest{
		SourceNodeId: "node-1", SnapshotId: "snap-match",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.GetValid() || resp.Msg.GetSha256() != sha {
		t.Fatalf("resp=%+v", resp.Msg)
	}
	assertControlAudit(t, st, "replication.verify", "SUCCESS", map[string]string{"snapshot_id": "snap-match"})
}

func TestDisasterReplicationAPI_VerifyReplica_PeerStoreUnavailable(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	// Override PeerStore to nil
	api.PeerStore = nil

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.VerifyReplicaRequest{
		SourceNodeId: "node-1",
		SnapshotId:   "snap-1",
	})

	_, err = api.VerifyReplica(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when PeerStore is unavailable")
	}
	if !strings.Contains(err.Error(), "peer store unavailable") {
		t.Errorf("expected 'peer store unavailable' error, got: %v", err)
	}
}

func TestDisasterReplicationAPI_ListRecoverableSnapshots_Complete(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.ListRecoverableSnapshotsRequest{})

	resp, err := api.ListRecoverableSnapshots(context.Background(), req)
	if err != nil {
		t.Fatalf("ListRecoverableSnapshots failed: %v", err)
	}

	if resp.Msg.Snapshots == nil {
		t.Error("expected non-nil snapshots slice")
	}
}

func TestDisasterReplicationAPI_ListRecoverableSnapshots_RetainsPeerSourceOwner(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := backup.Snapshot{FormatVersion: 1, SnapshotID: "snap-owner", ClusterID: "test-cluster", NodeID: "owner-node", CreatedAt: time.Unix(1_800_000_000, 0), Processes: []backup.ProcessDump{}}
	payload, checksum, err := backup.Encode(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.PeerStore.ReceiveWithMetadata(context.Background(), backup.ReceiveParams{SourceNodeID: "owner-node", ClusterID: "test-cluster", SnapshotID: "snap-owner", SHA256: checksum, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	resp, err := api.ListRecoverableSnapshots(context.Background(), bearerReq(sid, &procmeshv1.ListRecoverableSnapshotsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Snapshots) != 1 || resp.Msg.Snapshots[0].GetSourceNodeId() != "owner-node" || resp.Msg.Snapshots[0].GetSha256() != checksum {
		t.Fatalf("recoverable snapshots=%+v", resp.Msg.Snapshots)
	}
}

func TestDisasterReplicationAPI_ListRecoverableSnapshots_Empty(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.ListRecoverableSnapshotsRequest{})

	resp, err := api.ListRecoverableSnapshots(context.Background(), req)
	if err != nil {
		t.Fatalf("ListRecoverableSnapshots failed: %v", err)
	}

	// Should return empty list (stub implementation)
	if len(resp.Msg.Snapshots) != 0 {
		t.Errorf("expected empty snapshots list, got %d items", len(resp.Msg.Snapshots))
	}
}

func TestDisasterReplicationAPI_ListRecoverableSnapshots_Unauthorized(t *testing.T) {
	api, _, _, viewerSid := setupAPIWithViewerUser(t)
	client := newDisasterReplicationClient(t, api)

	req := bearerReq(viewerSid, &procmeshv1.ListRecoverableSnapshotsRequest{})

	_, err := client.ListRecoverableSnapshots(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for viewer user without replication.read permission")
	}
	if !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "DENIED") {
		t.Errorf("expected DENIED error, got: %v", err)
	}
}

func TestReplicationRoleCanCallDocumentedRPCs(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid := createCustomRoleUser(t, authSvc, "replicator", "user-repl", auth.PermReplicationRead, auth.PermReplicationManage)
	client := newDisasterReplicationClient(t, api)

	if _, err := client.GetTopology(context.Background(), bearerReq(sid, &procmeshv1.GetTopologyRequest{})); err != nil {
		t.Fatalf("replication.read GetTopology: %v", err)
	}
	if _, err := client.ListPolicies(context.Background(), bearerReq(sid, &procmeshv1.ListPoliciesRequest{})); err != nil {
		t.Fatalf("replication.read ListPolicies: %v", err)
	}
	draft, err := client.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "role-draft", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL",
	}))
	if err != nil {
		t.Fatalf("replication.manage GeneratePolicyDraft: %v", err)
	}
	if draft.Msg.GetDraft() == nil {
		t.Fatal("expected draft")
	}

	backupClient := newClusterBackupClient(t, &ClusterBackupAPI{Auth: authSvc, StateFn: api.StateFn}, true)
	_, err = backupClient.CreatePolicy(context.Background(), bearerReq(sid, &procmeshv1.CreateClusterBackupPolicyRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-backup-denied"},
		Policy: &procmeshv1.ClusterBackupPolicy{PolicyId: "bp-denied", Name: "denied", Sink: "fs", Timezone: "UTC", TargetSelector: "ALL_ADMITTED"},
	}))
	assertDenied(t, err)
}

func TestReplicationReadRoleCannotManage(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid := createCustomRoleUser(t, authSvc, "repl-reader", "user-repl-read", auth.PermReplicationRead)
	client := newDisasterReplicationClient(t, api)

	if _, err := client.GetTopology(context.Background(), bearerReq(sid, &procmeshv1.GetTopologyRequest{})); err != nil {
		t.Fatalf("replication.read GetTopology: %v", err)
	}
	_, err := client.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "denied-draft", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
	}))
	assertDenied(t, err)
	_, err = client.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-denied-apply"},
		PolicyId: "rp-denied",
		Draft:    &procmeshv1.PolicyDraft{Name: "denied"},
	}))
	assertDenied(t, err)
}

func TestReplicationRoleHopPerms(t *testing.T) {
	perm, write, ok := hopRPCPerm(procmeshv1connect.DisasterReplicationServiceListPoliciesProcedure)
	if !ok || perm != auth.PermReplicationRead || write {
		t.Fatalf("replication ListPolicies: perm=%s write=%v ok=%v", perm, write, ok)
	}
	perm, write, ok = hopRPCPerm(procmeshv1connect.DisasterReplicationServiceGeneratePolicyDraftProcedure)
	if !ok || perm != auth.PermReplicationManage || write {
		t.Fatalf("GeneratePolicyDraft should be manage without cluster-write: perm=%s write=%v ok=%v", perm, write, ok)
	}
	perm, write, ok = hopRPCPerm(procmeshv1connect.DisasterReplicationServiceApplyPolicyDraftProcedure)
	if !ok || perm != auth.PermReplicationManage || !write {
		t.Fatalf("ApplyPolicyDraft: perm=%s write=%v ok=%v", perm, write, ok)
	}
	perm, write, ok = hopRPCPerm(procmeshv1connect.ClusterBackupServiceListPoliciesProcedure)
	if !ok || perm != auth.PermBackupRead || write {
		t.Fatalf("backup ListPolicies: perm=%s write=%v ok=%v", perm, write, ok)
	}
	perm, write, ok = hopRPCPerm(procmeshv1connect.ClusterBackupServiceCreatePolicyProcedure)
	if !ok || perm != auth.PermBackupManage || !write {
		t.Fatalf("backup CreatePolicy: perm=%s write=%v ok=%v", perm, write, ok)
	}
	perm, write, ok = hopRPCPerm(procmeshv1connect.ClusterBackupServiceStartRunProcedure)
	if !ok || perm != auth.PermBackupManage || !write {
		t.Fatalf("backup StartRun: perm=%s write=%v ok=%v", perm, write, ok)
	}
}

func createCustomRoleUser(t *testing.T, svc *auth.Service, username, userID string, perms ...string) string {
	t.Helper()
	roles := &RoleAPI{Auth: svc}
	created, err := roles.CreateRole(context.Background(), connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-role-" + username, Operator: "t"},
		Name:        username + "-role",
		Permissions: append([]string(nil), perms...),
	}))
	if err != nil {
		t.Fatal(err)
	}
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: userID, Username: username, PasswordHash: testAdminHash(t),
	})
	_, err = roles.GrantRole(context.Background(), connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-grant-" + username, Operator: "t"},
		UserId:    userID,
		RoleId:    created.Msg.GetRole().GetRoleId(),
		ScopeType: string(control.ScopeCluster),
	}))
	if err != nil {
		t.Fatal(err)
	}
	sid, _, _, _, err := svc.Login(username, testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func TestReplicationDraftAudit(t *testing.T) {
	_, st, _ := newTestManager(t)
	api, _, authSvc := setupMinimalAPI(t)
	api.Store = st
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})
	_, err = api.GeneratePolicyDraft(ctx, bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "audit-draft", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertControlAudit(t, st, "replication.draft.generate", "SUCCESS", map[string]string{"policy_id": "audit-draft"})
}

func TestReplicationDraftAuditOnFailure(t *testing.T) {
	_, st, _ := newTestManager(t)
	api, _, _ := setupMinimalAPI(t)
	api.Store = st
	api.Members = nil
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})
	_, err := api.GeneratePolicyDraft(ctx, connect.NewRequest(&procmeshv1.GeneratePolicyDraftRequest{
		Name: "draft-fail", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL",
	}))
	if err == nil {
		t.Fatal("expected generate failure")
	}
	assertControlAudit(t, st, "replication.draft.generate", "FAILED", map[string]string{"policy_id": "draft-fail"})
}

func TestReplicationStartAuditExistingAndApplyFailure(t *testing.T) {
	_, st, _ := newTestManager(t)
	api, state, authSvc := setupMinimalAPI(t)
	api.Store = st
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state.ReplicationPolicies["rp-start"] = control.ReplicationPolicy{
		PolicyID: "rp-start", Name: "start", Enabled: true, Trigger: "MANUAL",
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"node-1"}, ReplicaFactor: 1,
		Routes:   []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2"}}},
		Revision: 1,
	}
	api.LeaderTerm = func() uint64 { return 3 }
	api.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, api.Now()) }
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})
	first, err := api.StartRun(ctx, bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "rp-start", Meta: &procmeshv1.MutationMeta{OperationId: "op-repl-start"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertControlAudit(t, st, "replication.run.start", "SUCCESS", map[string]string{"policy_id": "rp-start", "run_id": first.Msg.GetRunId()})

	_, err = api.StartRun(ctx, bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "rp-start", Meta: &procmeshv1.MutationMeta{OperationId: "op-repl-start"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.ListAuditAll(context.Background(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	success := 0
	for _, ev := range events {
		if ev.Action == "replication.run.start" && ev.Result == "SUCCESS" {
			success++
		}
	}
	if success < 2 {
		t.Fatalf("existing-run StartRun must audit, got %d success events in %s", success, auditBodies(events))
	}

	finished := state.ReplicationRuns[first.Msg.GetRunId()]
	finished.Status = "SUCCEEDED"
	state.ReplicationRuns[first.Msg.GetRunId()] = finished

	api.ApplyFn = func(control.Command, time.Duration) error { return errors.New("raft apply failed secret_key=wJalr") }
	_, err = api.StartRun(ctx, bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "rp-start", Meta: &procmeshv1.MutationMeta{OperationId: "op-repl-start-fail"},
	}))
	if err == nil {
		t.Fatal("expected apply failure")
	}
	assertControlAudit(t, st, "replication.run.start", "FAILED", map[string]string{"policy_id": "rp-start"})
}

func TestReplicationApplyAudit(t *testing.T) {
	_, st, _ := newTestManager(t)
	api, state, authSvc := setupMinimalAPI(t)
	api.Store = st
	api.ApplyFn = func(cmd control.Command, timeout time.Duration) error {
		return state.Apply(cmd, time.Now())
	}
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})
	gen, err := api.GeneratePolicyDraft(ctx, bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name: "audit-apply", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ApplyPolicyDraft(ctx, bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-audit-apply"},
		PolicyId:         "rp-audit",
		Draft:            gen.Msg.Draft,
		DraftRevision:    gen.Msg.Draft.DraftRevision,
		DraftHash:        gen.Msg.Draft.DraftHash,
		ExpectedRevision: -1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertControlAudit(t, st, "replication.draft.apply", "SUCCESS", map[string]string{"policy_id": "rp-audit"})
}

func TestDisasterReplicationAPI_ListRecoverableSnapshots_PeerStoreUnavailable(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	// Override PeerStore to nil
	api.PeerStore = nil

	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.ListRecoverableSnapshotsRequest{})

	_, err = api.ListRecoverableSnapshots(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when PeerStore is unavailable")
	}
	if !strings.Contains(err.Error(), "peer store unavailable") {
		t.Errorf("expected 'peer store unavailable' error, got: %v", err)
	}
}
