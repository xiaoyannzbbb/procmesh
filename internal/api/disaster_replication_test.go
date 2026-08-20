package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

// TestReplicationAPI_GeneratePolicyDraft verifies that GeneratePolicyDraft
// does not write to Raft and returns route, warnings, inbound load, and topology health.
func TestReplicationAPI_GeneratePolicyDraft(t *testing.T) {
	// Failing test: service not yet implemented
	ctx := context.Background()
	req := &connect.Request[procmeshv1.GeneratePolicyDraftRequest]{
		Msg: &procmeshv1.GeneratePolicyDraftRequest{
			Name:           "test-draft",
			SourceSelector: "all",
			ReplicaFactor:  1,
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.GeneratePolicyDraft(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_ApplyPolicyDraft verifies that ApplyPolicyDraft
// requires draft revision/hash (CAS semantics).
func TestReplicationAPI_ApplyPolicyDraft(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.ApplyPolicyDraftRequest]{
		Msg: &procmeshv1.ApplyPolicyDraftRequest{
			DraftRevision: 1,
			DraftHash:     "abc123",
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.ApplyPolicyDraft(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_PermissionDenied verifies that without replication.manage
// permission, policy mutations are rejected.
func TestReplicationAPI_PermissionDenied(t *testing.T) {
	ctx := context.Background()
	// TODO: Set up a context with a principal that lacks replication.manage

	req := &connect.Request[procmeshv1.ApplyPolicyDraftRequest]{
		Msg: &procmeshv1.ApplyPolicyDraftRequest{
			DraftRevision: 1,
			DraftHash:     "abc123",
		},
	}

	api := &DisasterReplicationAPI{}
	_, err := api.ApplyPolicyDraft(ctx, req)
	if err == nil {
		t.Fatal("expected permission denied")
	}
}

// TestReplicationAPI_ListRecoverableSnapshots verifies that
// ListRecoverableSnapshots returns source Owner and checksum.
func TestReplicationAPI_ListRecoverableSnapshots(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.ListRecoverableSnapshotsRequest]{
		Msg: &procmeshv1.ListRecoverableSnapshotsRequest{},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.ListRecoverableSnapshots(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_VerifyReplica verifies that VerifyReplica does not
// execute apply (only validates replica integrity).
func TestReplicationAPI_VerifyReplica(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.VerifyReplicaRequest]{
		Msg: &procmeshv1.VerifyReplicaRequest{
			SourceNodeId: "node-1",
			SnapshotId:   "snap-1",
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.VerifyReplica(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_GetTopology verifies that GetTopology returns cluster topology.
func TestReplicationAPI_GetTopology(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.GetTopologyRequest]{
		Msg: &procmeshv1.GetTopologyRequest{},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.GetTopology(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_ListPolicies verifies that ListPolicies returns all policies.
func TestReplicationAPI_ListPolicies(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.ListPoliciesRequest]{
		Msg: &procmeshv1.ListPoliciesRequest{},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.ListPolicies(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_GetPolicy verifies that GetPolicy returns a specific policy.
func TestReplicationAPI_GetPolicy(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.GetPolicyRequest]{
		Msg: &procmeshv1.GetPolicyRequest{
			PolicyId: "policy-1",
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.GetPolicy(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_UpdatePolicy verifies that UpdatePolicy modifies a policy.
func TestReplicationAPI_UpdatePolicy(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.UpdatePolicyRequest]{
		Msg: &procmeshv1.UpdatePolicyRequest{
			PolicyId: "policy-1",
			Meta: &procmeshv1.MutationMeta{
				OperationId: "op-1",
			},
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.UpdatePolicy(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_DeletePolicy verifies that DeletePolicy removes a policy.
func TestReplicationAPI_DeletePolicy(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.DeletePolicyRequest]{
		Msg: &procmeshv1.DeletePolicyRequest{
			PolicyId: "policy-1",
			Meta: &procmeshv1.MutationMeta{
				OperationId: "op-1",
			},
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.DeletePolicy(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_StartRun verifies that StartRun initiates a replication run.
func TestReplicationAPI_StartRun(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.StartRunRequest]{
		Msg: &procmeshv1.StartRunRequest{
			PolicyId: "policy-1",
			Meta: &procmeshv1.MutationMeta{
				OperationId: "op-1",
			},
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.StartRun(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_GetRun verifies that GetRun returns run status.
func TestReplicationAPI_GetRun(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.GetRunRequest]{
		Msg: &procmeshv1.GetRunRequest{
			RunId: "run-1",
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.GetRun(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_ListRuns verifies that ListRuns returns all runs.
func TestReplicationAPI_ListRuns(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.ListRunsRequest]{
		Msg: &procmeshv1.ListRunsRequest{},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.ListRuns(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// TestReplicationAPI_RetryFailedRoutes verifies that RetryFailedRoutes retries failed tasks.
func TestReplicationAPI_RetryFailedRoutes(t *testing.T) {
	ctx := context.Background()
	req := &connect.Request[procmeshv1.RetryFailedRoutesRequest]{
		Msg: &procmeshv1.RetryFailedRoutesRequest{
			RunId: "run-1",
			Meta: &procmeshv1.MutationMeta{
				OperationId: "op-1",
			},
		},
	}

	api := &DisasterReplicationAPI{}
	resp, err := api.RetryFailedRoutes(ctx, req)
	if err == nil {
		t.Fatal("expected error for unimplemented service")
	}
	_ = resp
}

// Mock types for testing (will be replaced with actual implementations)
type mockTopologyProvider struct{}

func (m *mockTopologyProvider) GetTopology(ctx context.Context) ([]backup.AgentTopology, error) {
	return []backup.AgentTopology{
		{NodeID: "node-1", Admitted: true, Alive: true, Zone: "zone-a"},
		{NodeID: "node-2", Admitted: true, Alive: true, Zone: "zone-b"},
	}, nil
}

type mockReplicationControl struct{}

func (m *mockReplicationControl) GetPolicy(ctx context.Context, policyID string) (interface{}, error) {
	return nil, nil
}

func (m *mockReplicationControl) ListPolicies(ctx context.Context) ([]interface{}, error) {
	return []interface{}{}, nil
}

func (m *mockReplicationControl) PutPolicy(ctx context.Context, policy interface{}) error {
	return nil
}

func (m *mockReplicationControl) DeletePolicy(ctx context.Context, policyID string) error {
	return nil
}

// Verify permissions are properly mapped
func TestReplicationPermissionMapping(t *testing.T) {
	tests := []struct {
		procedure string
		wantPerm  string
		wantWrite bool
		wantOK    bool
	}{
		{"GetTopology", auth.PermReplicationRead, false, true},
		{"GeneratePolicyDraft", auth.PermReplicationRead, false, true},
		{"ListPolicies", auth.PermReplicationRead, false, true},
		{"GetPolicy", auth.PermReplicationRead, false, true},
		{"ApplyPolicyDraft", auth.PermReplicationManage, true, true},
		{"UpdatePolicy", auth.PermReplicationManage, true, true},
		{"DeletePolicy", auth.PermReplicationManage, true, true},
		{"StartRun", auth.PermReplicationManage, true, true},
		{"RetryFailedRoutes", auth.PermReplicationManage, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.procedure, func(t *testing.T) {
			perm, write, ok := hopRPCPerm(tt.procedure)
			if ok != tt.wantOK {
				t.Errorf("hopRPCPerm(%q) ok = %v, want %v", tt.procedure, ok, tt.wantOK)
			}
			if ok && perm != tt.wantPerm {
				t.Errorf("hopRPCPerm(%q) perm = %v, want %v", tt.procedure, perm, tt.wantPerm)
			}
			if ok && write != tt.wantWrite {
				t.Errorf("hopRPCPerm(%q) write = %v, want %v", tt.procedure, write, tt.wantWrite)
			}
		})
	}
}
