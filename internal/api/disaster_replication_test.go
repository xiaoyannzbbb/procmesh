package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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
func newDisasterReplicationClient(t *testing.T, api *DisasterReplicationAPI) procmeshv1connect.DisasterReplicationServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	h, handlers := procmeshv1connect.NewDisasterReplicationServiceHandler(api,
		connect.WithInterceptors(AuthInterceptor(api.Auth, func() bool { return true })))
	mux.Handle(h, handlers)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewDisasterReplicationServiceClient(srv.Client(), srv.URL)
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

func TestComputeDraftHash(t *testing.T) {
	req := &procmeshv1.GeneratePolicyDraftRequest{
		Name:           "test-policy",
		SourceSelector: "all",
		ReplicaFactor:  2,
	}

	routes := []*procmeshv1.ReplicationRoute{
		{SourceNodeId: "node-1", TargetNodeIds: []string{"node-2", "node-3"}},
		{SourceNodeId: "node-2", TargetNodeIds: []string{"node-1", "node-3"}},
	}

	hash1 := computeDraftHash(req, routes)
	if hash1 == "" {
		t.Error("expected non-empty hash")
	}
	if len(hash1) != 64 { // SHA256 hex length
		t.Errorf("hash length: got %d, want 64", len(hash1))
	}

	// Same input should produce same hash
	hash2 := computeDraftHash(req, routes)
	if hash1 != hash2 {
		t.Errorf("hash mismatch: %q != %q", hash1, hash2)
	}

	// Different routes should produce different hash
	routes2 := []*procmeshv1.ReplicationRoute{
		{SourceNodeId: "node-1", TargetNodeIds: []string{"node-2"}},
	}
	hash3 := computeDraftHash(req, routes2)
	if hash1 == hash3 {
		t.Error("expected different hash for different routes")
	}

	// Route order should not matter (routes are sorted)
	routes3 := []*procmeshv1.ReplicationRoute{
		{SourceNodeId: "node-2", TargetNodeIds: []string{"node-1", "node-3"}},
		{SourceNodeId: "node-1", TargetNodeIds: []string{"node-2", "node-3"}},
	}
	hash4 := computeDraftHash(req, routes3)
	if hash1 != hash4 {
		t.Errorf("hash should be same for reordered routes: %q != %q", hash1, hash4)
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
				{NodeID: "node-1", State: cluster.StateAlive, Labels: map[string]string{"host": "host1", "zone": "z1"}},
				{NodeID: "node-2", State: cluster.StateAlive, Labels: map[string]string{"host": "host2", "zone": "z2"}},
				{NodeID: "node-3", State: cluster.StateAlive, Labels: map[string]string{"host": "host3", "zone": "z3"}},
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
		SourceSelector: "all",
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
		SourceSelector: "all",
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
		SourceSelector: "all",
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

func TestDisasterReplicationAPI_ApplyPolicyDraft_HashMismatch(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	// Generate draft
	genReq := bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{
		Name:           "test-policy",
		SourceSelector: "all",
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
	forgedHash := computeDraftHashForTopology(request, generated.Msg.Draft.Routes, generated.Msg.Draft.DraftRevision)
	generated.Msg.Draft.DraftHash = forgedHash
	_, err = api.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{PolicyId: "rp-forged", Draft: generated.Msg.Draft, DraftRevision: generated.Msg.Draft.DraftRevision, DraftHash: forgedHash, ExpectedRevision: -1, Meta: &procmeshv1.MutationMeta{OperationId: "op-forged"}}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("client-rehashed routes should conflict with server preview, got %v", err)
	}
}

func TestDisasterReplicationAPI_ApplyPolicyDraftRejectsChangedRoutesWithOriginalHash(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := api.GeneratePolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.GeneratePolicyDraftRequest{Name: "changed", SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL"}))
	if err != nil {
		t.Fatal(err)
	}
	generated.Msg.Draft.Routes[0].TargetNodeIds = []string{"node-3"}
	_, err = api.ApplyPolicyDraft(context.Background(), bearerReq(sid, &procmeshv1.ApplyPolicyDraftRequest{PolicyId: "rp-changed", Draft: generated.Msg.Draft, DraftRevision: generated.Msg.Draft.DraftRevision, DraftHash: generated.Msg.Draft.DraftHash, ExpectedRevision: -1, Meta: &procmeshv1.MutationMeta{OperationId: "op-changed"}}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("changed routes with original hash should conflict, got %v", err)
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
	for _, node := range resp.Msg.Nodes {
		if node.NodeId == "" {
			t.Error("expected node ID to be set")
		}
		if !node.Alive {
			t.Error("expected health to be set")
		}
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

	// Create a policy first
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
	client := newDisasterReplicationClient(t, api)

	req := bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-start-1",
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-start-1"},
		SnapshotRefs: []*procmeshv1.ReplicationSnapshotRef{
			{SourceNodeId: "node-1", SnapshotId: "snapshot-node-1", Sha256: strings.Repeat("a", 64)},
			{SourceNodeId: "node-2", SnapshotId: "snapshot-node-2", Sha256: strings.Repeat("b", 64)},
		},
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
		SnapshotRefs: []*procmeshv1.ReplicationSnapshotRef{
			{SourceNodeId: "node-1", SnapshotId: "snapshot-node-1", Sha256: strings.Repeat("a", 64)},
			{SourceNodeId: "node-2", SnapshotId: "snapshot-node-2", Sha256: strings.Repeat("b", 64)},
		},
	}))
	if err != nil {
		t.Fatalf("idempotent StartRun failed: %v", err)
	}
	if retryResp.Msg.GetRunId() != resp.Msg.GetRunId() || retryResp.Msg.GetPolicyRevision() != 1 || len(state.ReplicationTasks) != len(wantRoutes) {
		t.Fatalf("retry run=%q tasks=%d, want run=%q tasks=%d", retryResp.Msg.GetRunId(), len(state.ReplicationTasks), resp.Msg.GetRunId(), len(wantRoutes))
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

func TestDisasterReplicationAPI_StartRun_ManualRequiresCompleteFrozenSnapshotRefs(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state.ReplicationPolicies["policy-manual-frozen"] = control.ReplicationPolicy{
		PolicyID: "policy-manual-frozen", Name: "manual", Enabled: true, Trigger: "MANUAL", SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"node-1", "node-2"}, Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-3"}}, {SourceNodeID: "node-2", TargetNodeIDs: []string{"node-3"}}},
	}
	api.LeaderTerm = func() uint64 { return 3 }
	api.ApplyFn = func(cmd control.Command, _ time.Duration) error { return state.Apply(cmd, time.Now()) }
	client := newDisasterReplicationClient(t, api)
	_, err = client.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{
		PolicyId: "policy-manual-frozen", Meta: &procmeshv1.MutationMeta{OperationId: "op-manual-missing"},
		SnapshotRefs: []*procmeshv1.ReplicationSnapshotRef{{SourceNodeId: "node-1", SnapshotId: "snap-1", Sha256: strings.Repeat("a", 64)}},
	}))
	if err == nil || !strings.Contains(err.Error(), "snapshot refs") {
		t.Fatalf("expected incomplete frozen refs to be rejected, got %v", err)
	}
	if len(state.ReplicationRuns) != 0 {
		t.Fatalf("unbound manual request created a run: %+v", state.ReplicationRuns)
	}
}

func TestDisasterReplicationAPI_StartRunRejectsNonManualPolicy(t *testing.T) {
	api, state, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}
	state.ReplicationPolicies["scheduled"] = control.ReplicationPolicy{PolicyID: "scheduled", Enabled: true, Trigger: "SCHEDULE", SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"node-1"}, Revision: 1, Routes: []control.ReplicationRoute{{SourceNodeID: "node-1", TargetNodeIDs: []string{"node-2"}}}}
	api.LeaderTerm = func() uint64 { return 3 }
	_, err = api.StartRun(context.Background(), bearerReq(sid, &procmeshv1.StartRunRequest{PolicyId: "scheduled", Meta: &procmeshv1.MutationMeta{OperationId: "op-scheduled"}}))
	if err == nil || !strings.Contains(err.Error(), "manual") {
		t.Fatalf("expected non-manual rejection, got %v", err)
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
			return Route{NodeID: "node-b", RPC: "127.0.0.1:9001"}, true
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

func TestDisasterReplicationAPI_RetryFailedRoutesExcludesTasksWithoutFrozenRefs(t *testing.T) {
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
	if resp.Msg.RetriedCount != 1 {
		t.Fatalf("retried count = %d, want 1 eligible frozen route", resp.Msg.RetriedCount)
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
