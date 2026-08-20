package api

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		PeerStore: &backup.PeerStore{Root: dir},
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
	if draft.DraftRevision != 1 {
		t.Errorf("draft revision: got %d, want 1", draft.DraftRevision)
	}
	if draft.TopologyHealth == "" {
		t.Error("expected topology health to be set")
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
		Meta: &procmeshv1.MutationMeta{OperationId: "op-update"},
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
		Meta: &procmeshv1.MutationMeta{OperationId: "op-delete"},
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

func TestDisasterReplicationAPI_ListRuns_Stub(t *testing.T) {
	api, _, authSvc := setupMinimalAPI(t)
	sid, _, _, _, err := authSvc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	req := bearerReq(sid, &procmeshv1.ListRunsRequest{
		PolicyId: "policy-1",
	})

	_, err = api.ListRuns(context.Background(), req)
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
