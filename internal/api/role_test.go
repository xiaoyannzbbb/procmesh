package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func TestRole_CreateAndGrant(t *testing.T) {
	ctx := context.Background()
	stub := &RoleAPI{}
	_, err := stub.CreateRole(ctx, connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-nil", Operator: "t"},
		Name:        "ops",
		Permissions: []string{auth.PermProcessRead},
	}))
	assertUnavailableMsg(t, err, "auth not configured")

	_, svc := newBootstrappedAuth(t)
	api := &RoleAPI{Auth: svc}

	_, err = api.CreateRole(ctx, connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-bad-perm", Operator: "t"},
		Name:        "ops",
		Permissions: []string{"not.a.perm"},
	}))
	assertInvalidMsg(t, err, "unknown permission")

	created, err := api.CreateRole(ctx, connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-role-create", Operator: "t"},
		Name:        "ops",
		Permissions: []string{auth.PermProcessRead, auth.PermProcessRestart},
	}))
	if err != nil {
		t.Fatal(err)
	}
	role := created.Msg.GetRole()
	if role.GetRoleId() == "" || role.GetName() != "ops" {
		t.Fatalf("created %+v", role)
	}
	if !hasAllPerms(role.GetPermissions(), auth.PermProcessRead, auth.PermProcessRestart) {
		t.Fatalf("perms=%v", role.GetPermissions())
	}

	list, err := api.ListRoles(ctx, connect.NewRequest(&procmeshv1.ListRolesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if findRole(list.Msg.GetRoles(), "ops") == nil {
		t.Fatalf("list missing ops: %+v", list.Msg.GetRoles())
	}
	if findRole(list.Msg.GetRoles(), "Super Admin") == nil && findRoleID(list.Msg.GetRoles(), "super_admin") == nil {
		t.Fatalf("list missing builtin: %+v", list.Msg.GetRoles())
	}

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-bad-scope", Operator: "t"},
		UserId:    "user-admin",
		RoleId:    role.GetRoleId(),
		ScopeType: "GROUP",
	}))
	assertInvalidMsg(t, err, "invalid scope_type")

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-agent-scope", Operator: "t"},
		UserId:    "user-admin",
		RoleId:    role.GetRoleId(),
		ScopeType: "AGENT",
	}))
	assertInvalidMsg(t, err, "scope_id")

	grant, err := api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-grant", Operator: "t"},
		UserId:    "user-admin",
		RoleId:    role.GetRoleId(),
		ScopeType: string(control.ScopeCluster),
	}))
	if err != nil {
		t.Fatal(err)
	}
	b := grant.Msg.GetBinding()
	if b.GetUserId() != "user-admin" || b.GetRoleId() != role.GetRoleId() || b.GetScopeType() != string(control.ScopeCluster) {
		t.Fatalf("binding %+v", b)
	}

	agent, err := api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-grant-agent", Operator: "t"},
		UserId:    "user-admin",
		RoleId:    role.GetRoleId(),
		ScopeType: string(control.ScopeAgent),
		ScopeId:   "node-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if agent.Msg.GetBinding().GetScopeId() != "node-1" {
		t.Fatalf("agent bind %+v", agent.Msg.GetBinding())
	}

	list, err = api.ListRoles(ctx, connect.NewRequest(&procmeshv1.ListRolesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !hasBinding(list.Msg.GetBindings(), "user-admin", role.GetRoleId(), string(control.ScopeCluster), "") {
		t.Fatalf("cluster bind missing: %+v", list.Msg.GetBindings())
	}
	if !hasBinding(list.Msg.GetBindings(), "user-admin", role.GetRoleId(), string(control.ScopeAgent), "node-1") {
		t.Fatalf("agent bind missing: %+v", list.Msg.GetBindings())
	}
}

func TestRoleAPI_UpdateDeleteAndRevokeCustomRole(t *testing.T) {
	ctx := context.Background()
	_, svc := newBootstrappedAuth(t)
	api := &RoleAPI{Auth: svc}

	created, err := api.CreateRole(ctx, connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-role-create-custom", Operator: "t"},
		Name: "ops", Permissions: []string{auth.PermProcessRead},
	}))
	if err != nil {
		t.Fatal(err)
	}
	roleID := created.Msg.GetRole().GetRoleId()

	updated, err := api.UpdateRole(ctx, connect.NewRequest(&procmeshv1.UpdateRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-role-update", Operator: "t"},
		RoleId: roleID, Name: "operators", Permissions: []string{auth.PermProcessRead, auth.PermProcessRestart},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Msg.GetRole(); got.GetName() != "operators" || !hasAllPerms(got.GetPermissions(), auth.PermProcessRead, auth.PermProcessRestart) {
		t.Fatalf("updated=%+v", got)
	}

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-role-grant-custom", Operator: "t"},
		UserId: "user-admin", RoleId: roleID, ScopeType: string(control.ScopeAgent), ScopeId: "node-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.DeleteRole(ctx, connect.NewRequest(&procmeshv1.DeleteRoleRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-role-delete-bound", Operator: "t"}, RoleId: roleID,
	}))
	assertConnectError(t, err, connect.CodeFailedPrecondition, "CONFLICT", "role still has bindings")

	_, err = api.RevokeRole(ctx, connect.NewRequest(&procmeshv1.RevokeRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-role-revoke", Operator: "t"},
		UserId: "user-admin", RoleId: roleID, ScopeType: string(control.ScopeAgent), ScopeId: " node-1 ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.DeleteRole(ctx, connect.NewRequest(&procmeshv1.DeleteRoleRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-role-delete", Operator: "t"}, RoleId: roleID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Store().View().Roles[roleID]; ok {
		t.Fatal("role was not deleted")
	}
}

func TestRoleAPI_RejectsBuiltinRoleMutations(t *testing.T) {
	ctx := context.Background()
	_, svc := newBootstrappedAuth(t)
	api := &RoleAPI{Auth: svc}

	_, err := api.UpdateRole(ctx, connect.NewRequest(&procmeshv1.UpdateRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-builtin-update", Operator: "t"},
		RoleId: "viewer", Name: "Changed", Permissions: []string{auth.PermRoleManage},
	}))
	assertConnectError(t, err, connect.CodePermissionDenied, "DENIED", "built-in role")

	_, err = api.DeleteRole(ctx, connect.NewRequest(&procmeshv1.DeleteRoleRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-builtin-delete", Operator: "t"}, RoleId: "viewer",
	}))
	assertConnectError(t, err, connect.CodePermissionDenied, "DENIED", "built-in role")

	_, err = api.RevokeRole(ctx, connect.NewRequest(&procmeshv1.RevokeRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-builtin-revoke", Operator: "t"},
		UserId: "user-admin", RoleId: "super_admin", ScopeType: string(control.ScopeCluster),
	}))
	assertConnectError(t, err, connect.CodePermissionDenied, "DENIED", "built-in role")
}

func assertConnectError(t *testing.T, err error, code connect.Code, detail, message string) {
	t.Helper()
	gotCode, gotDetail := connectDetail(t, err)
	if gotCode != code || gotDetail != detail || !strings.Contains(err.Error(), message) {
		t.Fatalf("code=%v detail=%s err=%v", gotCode, gotDetail, err)
	}
}

func TestRoleAPI_GrantGroupScopes(t *testing.T) {
	ctx := context.Background()
	_, svc := newBootstrappedAuth(t)
	api := &RoleAPI{Auth: svc}
	now := time.Unix(1_700_000_000, 0)
	applyAuthCmd(t, svc, control.CmdMemberPut, control.MemberPutBody{NodeID: "node-1"})
	applyAuthCmd(t, svc, control.CmdGroupPut, control.GroupPutBody{GroupID: "g-fin", Name: "finance", NowUnix: now.Unix()})

	created, err := api.CreateRole(ctx, connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-role", Operator: "t"},
		Name:        "ops",
		Permissions: []string{auth.PermProcessRead},
	}))
	if err != nil {
		t.Fatal(err)
	}
	role := created.Msg.GetRole()

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-ag", Operator: "t"},
		UserId: "user-admin", RoleId: role.GetRoleId(),
		ScopeType: "AGENT_GROUP", ScopeId: "g-fin",
	}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-ag-miss", Operator: "t"},
		UserId: "user-admin", RoleId: role.GetRoleId(),
		ScopeType: "AGENT_GROUP", ScopeId: "missing",
	}))
	assertInvalidOrNotFound(t, err)

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-pg", Operator: "t"},
		UserId: "user-admin", RoleId: role.GetRoleId(),
		ScopeType: "PROCESS_GROUP", ScopeId: "finance",
	}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-pg-bad", Operator: "t"},
		UserId: "user-admin", RoleId: role.GetRoleId(),
		ScopeType: "PROCESS_GROUP", ScopeId: "bad name",
	}))
	assertInvalidMsg(t, err, "scope_id")
}

func TestReplicationRoleAllowlist(t *testing.T) {
	ctx := context.Background()
	_, svc := newBootstrappedAuth(t)
	api := &RoleAPI{Auth: svc}

	created, err := api.CreateRole(ctx, connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-repl-role", Operator: "t"},
		Name:        "replicator",
		Permissions: []string{auth.PermReplicationRead, auth.PermReplicationManage},
	}))
	if err != nil {
		t.Fatal(err)
	}
	role := created.Msg.GetRole()
	if !hasAllPerms(role.GetPermissions(), auth.PermReplicationRead, auth.PermReplicationManage) {
		t.Fatalf("perms=%v", role.GetPermissions())
	}
}

func TestRoleAPI_GrantProcessGroupTrimsScopeID(t *testing.T) {
	ctx := context.Background()
	st, svc := newBootstrappedAuth(t)
	api := &RoleAPI{Auth: svc}

	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: "u-fin", Username: "finop", PasswordHash: testAdminHash(t),
	})
	created, err := api.CreateRole(ctx, connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-role-trim", Operator: "t"},
		Name:        "fin-ops",
		Permissions: []string{auth.PermProcessRead},
	}))
	if err != nil {
		t.Fatal(err)
	}
	roleID := created.Msg.GetRole().GetRoleId()

	grant, err := api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-pg-trim", Operator: "t"},
		UserId:    "u-fin",
		RoleId:    roleID,
		ScopeType: "PROCESS_GROUP",
		ScopeId:   " finance ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := grant.Msg.GetBinding().GetScopeId(); got != "finance" {
		t.Fatalf("response scope_id=%q want finance", got)
	}

	listed, err := api.ListRoles(ctx, connect.NewRequest(&procmeshv1.ListRolesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !hasBinding(listed.Msg.GetBindings(), "u-fin", roleID, "PROCESS_GROUP", "finance") {
		t.Fatalf("stored binding missing trimmed finance: %+v", listed.Msg.GetBindings())
	}
	if hasBinding(listed.Msg.GetBindings(), "u-fin", roleID, "PROCESS_GROUP", " finance ") {
		t.Fatalf("raw padded scope_id persisted: %+v", listed.Msg.GetBindings())
	}
	view := st.View()
	if !view.CheckTarget("u-fin", auth.PermProcessRead, control.CheckTarget{ProcessGroup: "finance"}) {
		t.Fatal("trimmed scope_id must match CheckTarget ProcessGroup=finance")
	}
	if view.CheckTarget("u-fin", auth.PermProcessRead, control.CheckTarget{ProcessGroup: " finance "}) {
		t.Fatal("padded scope must not match CheckTarget")
	}
}

func assertInvalidMsg(t *testing.T, err error, msg string) {
	t.Helper()
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), msg) {
		t.Fatalf("want %q: %v", msg, err)
	}
}

func assertInvalidOrNotFound(t *testing.T, err error) {
	t.Helper()
	code, detail := connectDetail(t, err)
	if (code == connect.CodeInvalidArgument && detail == "INVALID") || (code == connect.CodeNotFound && detail == "NOT_FOUND") {
		return
	}
	t.Fatalf("want INVALID or NOT_FOUND: code=%v detail=%s err=%v", code, detail, err)
}

func findRole(roles []*procmeshv1.Role, name string) *procmeshv1.Role {
	for _, r := range roles {
		if r.GetName() == name {
			return r
		}
	}
	return nil
}

func findRoleID(roles []*procmeshv1.Role, id string) *procmeshv1.Role {
	for _, r := range roles {
		if r.GetRoleId() == id {
			return r
		}
	}
	return nil
}

func hasAllPerms(got []string, want ...string) bool {
	set := map[string]struct{}{}
	for _, p := range got {
		set[p] = struct{}{}
	}
	for _, p := range want {
		if _, ok := set[p]; !ok {
			return false
		}
	}
	return true
}

func hasBinding(binds []*procmeshv1.Binding, userID, roleID, scope, scopeID string) bool {
	for _, b := range binds {
		if b.GetUserId() == userID && b.GetRoleId() == roleID && b.GetScopeType() == scope && b.GetScopeId() == scopeID {
			return true
		}
	}
	return false
}
