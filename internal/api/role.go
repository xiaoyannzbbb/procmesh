package api

import (
	"context"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.RoleServiceHandler = (*RoleAPI)(nil)

type RoleAPI struct {
	Auth *auth.Service
}

func (s *RoleAPI) ListRoles(ctx context.Context, _ *connect.Request[procmeshv1.ListRolesRequest]) (*connect.Response[procmeshv1.ListRolesResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermRoleRead, "", false, true); err != nil {
		return nil, err
	}
	st := s.Auth.Store().View()
	ids := make([]string, 0, len(st.Roles))
	for id := range st.Roles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := &procmeshv1.ListRolesResponse{
		Roles:    make([]*procmeshv1.Role, 0, len(ids)),
		Bindings: make([]*procmeshv1.Binding, 0, len(st.Bindings)),
	}
	for _, id := range ids {
		out.Roles = append(out.Roles, roleToProto(st.Roles[id]))
	}
	for _, b := range st.Bindings {
		out.Bindings = append(out.Bindings, bindingToProto(b))
	}
	return connect.NewResponse(out), nil
}

func (s *RoleAPI) CreateRole(ctx context.Context, req *connect.Request[procmeshv1.CreateRoleRequest]) (*connect.Response[procmeshv1.CreateRoleResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermRoleManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	name := req.Msg.GetName()
	if name == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "role name required"))
	}
	perms := req.Msg.GetPermissions()
	for _, p := range perms {
		if !knownPerm(p) {
			return nil, ToConnect(errcode.E(errcode.INVALID, "unknown permission"))
		}
	}
	id, err := newAuthID()
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := applyAuth(s.Auth, control.CmdRolePut, control.RolePutBody{
		ID:    id,
		Name:  name,
		Perms: append([]string(nil), perms...),
	}); err != nil {
		return nil, err
	}
	role, ok := s.Auth.Store().View().Roles[id]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "role not found after create"))
	}
	return connect.NewResponse(&procmeshv1.CreateRoleResponse{Role: roleToProto(role)}), nil
}

func (s *RoleAPI) UpdateRole(ctx context.Context, req *connect.Request[procmeshv1.UpdateRoleRequest]) (*connect.Response[procmeshv1.UpdateRoleResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermRoleManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	roleID := req.Msg.GetRoleId()
	if roleID == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "role_id required"))
	}
	if control.IsBuiltinRoleID(roleID) {
		return nil, ToConnect(errcode.E(errcode.DENIED, "built-in role cannot be changed"))
	}
	if _, ok := s.Auth.Store().View().Roles[roleID]; !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "role not found"))
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "role name required"))
	}
	perms := req.Msg.GetPermissions()
	for _, p := range perms {
		if !knownPerm(p) {
			return nil, ToConnect(errcode.E(errcode.INVALID, "unknown permission"))
		}
	}
	if err := applyAuth(s.Auth, control.CmdRolePut, control.RolePutBody{
		ID: roleID, Name: name, Perms: append([]string(nil), perms...), ExistingOnly: true,
	}); err != nil {
		return nil, err
	}
	role, ok := s.Auth.Store().View().Roles[roleID]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "role not found after update"))
	}
	return connect.NewResponse(&procmeshv1.UpdateRoleResponse{Role: roleToProto(role)}), nil
}

func (s *RoleAPI) DeleteRole(ctx context.Context, req *connect.Request[procmeshv1.DeleteRoleRequest]) (*connect.Response[procmeshv1.DeleteRoleResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermRoleManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	roleID := req.Msg.GetRoleId()
	if roleID == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "role_id required"))
	}
	if control.IsBuiltinRoleID(roleID) {
		return nil, ToConnect(errcode.E(errcode.DENIED, "built-in role cannot be deleted"))
	}
	if err := applyAuth(s.Auth, control.CmdRoleDelete, control.RoleDeleteBody{ID: roleID}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.DeleteRoleResponse{}), nil
}

func (s *RoleAPI) GrantRole(ctx context.Context, req *connect.Request[procmeshv1.GrantRoleRequest]) (*connect.Response[procmeshv1.GrantRoleResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermRoleManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	userID := req.Msg.GetUserId()
	roleID := req.Msg.GetRoleId()
	if userID == "" || roleID == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "user_id and role_id required"))
	}
	scope, scopeID, err := parseScope(req.Msg.GetScopeType(), req.Msg.GetScopeId())
	if err != nil {
		return nil, err
	}
	st := s.Auth.Store().View()
	if scope == control.ScopeAgentGroup {
		if _, ok := st.AgentGroups[scopeID]; !ok {
			return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "agent group not found"))
		}
	}
	if _, ok := userFromState(st, userID); !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "user not found"))
	}
	if _, ok := st.Roles[roleID]; !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "role not found"))
	}
	if err := applyAuth(s.Auth, control.CmdBindPut, control.BindPutBody{
		UserID:  userID,
		RoleID:  roleID,
		ScopeID: scopeID,
		Scope:   scope,
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.GrantRoleResponse{
		Binding: &procmeshv1.Binding{
			UserId:    userID,
			RoleId:    roleID,
			ScopeType: string(scope),
			ScopeId:   scopeID,
		},
	}), nil
}

func (s *RoleAPI) RevokeRole(ctx context.Context, req *connect.Request[procmeshv1.RevokeRoleRequest]) (*connect.Response[procmeshv1.RevokeRoleResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermRoleManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	userID := req.Msg.GetUserId()
	roleID := req.Msg.GetRoleId()
	if userID == "" || roleID == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "user_id and role_id required"))
	}
	if control.IsBuiltinRoleID(roleID) {
		return nil, ToConnect(errcode.E(errcode.DENIED, "built-in role binding cannot be deleted"))
	}
	scope, scopeID, err := parseScope(req.Msg.GetScopeType(), req.Msg.GetScopeId())
	if err != nil {
		return nil, err
	}
	if err := applyAuth(s.Auth, control.CmdBindDelete, control.BindDeleteBody{
		UserID: userID, RoleID: roleID, Scope: scope, ScopeID: scopeID,
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.RevokeRoleResponse{}), nil
}

func parseScope(scopeType, scopeID string) (control.ScopeType, string, error) {
	s := strings.ToUpper(strings.TrimSpace(scopeType))
	id := strings.TrimSpace(scopeID)
	if s == "" {
		s = string(control.ScopeCluster)
	}
	switch control.ScopeType(s) {
	case control.ScopeCluster:
		return control.ScopeCluster, "", nil
	case control.ScopeAgent:
		if id == "" {
			return "", "", ToConnect(errcode.E(errcode.INVALID, "scope_id required for AGENT"))
		}
		return control.ScopeAgent, id, nil
	case control.ScopeAgentGroup:
		if id == "" {
			return "", "", ToConnect(errcode.E(errcode.INVALID, "scope_id required for AGENT_GROUP"))
		}
		return control.ScopeAgentGroup, id, nil
	case control.ScopeProcessGroup:
		if !processGroupNameOK(id) {
			return "", "", ToConnect(errcode.E(errcode.INVALID, "scope_id"))
		}
		return control.ScopeProcessGroup, id, nil
	default:
		return "", "", ToConnect(errcode.E(errcode.INVALID, "invalid scope_type"))
	}
}

func processGroupNameOK(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func knownPerm(p string) bool {
	switch p {
	case auth.PermClusterRead, auth.PermClusterManage,
		auth.PermNodeRead, auth.PermNodeManage, auth.PermNodeRemove,
		auth.PermProcessRead, auth.PermProcessCreate, auth.PermProcessUpdate, auth.PermProcessDelete,
		auth.PermProcessStart, auth.PermProcessStop, auth.PermProcessRestart,
		auth.PermProcessConfigRead, auth.PermProcessConfigUpdate,
		auth.PermProcessLogsRead, auth.PermProcessLogsDownload,
		auth.PermUserRead, auth.PermUserCreate, auth.PermUserUpdate, auth.PermUserDelete,
		auth.PermRoleRead, auth.PermRoleManage,
		auth.PermAuditRead,
		auth.PermBatchExecute, auth.PermAlertRead, auth.PermAlertManage,
		auth.PermBackupRead, auth.PermBackupManage,
		auth.PermReplicationRead, auth.PermReplicationManage,
		auth.PermCommandExecute, auth.PermCommandExecuteBatch:
		return true
	default:
		return false
	}
}

func roleToProto(r control.Role) *procmeshv1.Role {
	return &procmeshv1.Role{
		RoleId:      r.ID,
		Name:        r.Name,
		Permissions: append([]string(nil), r.Perms...),
	}
}

func bindingToProto(b control.Binding) *procmeshv1.Binding {
	return &procmeshv1.Binding{
		UserId:    b.UserID,
		RoleId:    b.RoleID,
		ScopeType: string(b.Scope),
		ScopeId:   b.ScopeID,
	}
}
