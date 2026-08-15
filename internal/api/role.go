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
	if err := requirePerm(ctx, s.Auth, auth.PermRoleRead, "", false); err != nil {
		return nil, err
	}
	st := s.Auth.Store.View()
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
	if err := requirePerm(ctx, s.Auth, auth.PermRoleManage, "", true); err != nil {
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
	role, ok := s.Auth.Store.View().Roles[id]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "role not found after create"))
	}
	return connect.NewResponse(&procmeshv1.CreateRoleResponse{Role: roleToProto(role)}), nil
}

func (s *RoleAPI) GrantRole(ctx context.Context, req *connect.Request[procmeshv1.GrantRoleRequest]) (*connect.Response[procmeshv1.GrantRoleResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermRoleManage, "", true); err != nil {
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
	scope, err := parseScope(req.Msg.GetScopeType(), req.Msg.GetScopeId())
	if err != nil {
		return nil, err
	}
	st := s.Auth.Store.View()
	if _, ok := userFromState(st, userID); !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "user not found"))
	}
	if _, ok := st.Roles[roleID]; !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "role not found"))
	}
	if err := applyAuth(s.Auth, control.CmdBindPut, control.BindPutBody{
		UserID:  userID,
		RoleID:  roleID,
		ScopeID: req.Msg.GetScopeId(),
		Scope:   scope,
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.GrantRoleResponse{
		Binding: &procmeshv1.Binding{
			UserId:    userID,
			RoleId:    roleID,
			ScopeType: string(scope),
			ScopeId:   req.Msg.GetScopeId(),
		},
	}), nil
}

func parseScope(scopeType, scopeID string) (control.ScopeType, error) {
	s := strings.ToUpper(strings.TrimSpace(scopeType))
	if s == "" {
		s = string(control.ScopeCluster)
	}
	switch control.ScopeType(s) {
	case control.ScopeCluster:
		return control.ScopeCluster, nil
	case control.ScopeAgent:
		if scopeID == "" {
			return "", ToConnect(errcode.E(errcode.INVALID, "scope_id required for AGENT"))
		}
		return control.ScopeAgent, nil
	default:
		return "", ToConnect(errcode.E(errcode.INVALID, "invalid scope_type"))
	}
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
