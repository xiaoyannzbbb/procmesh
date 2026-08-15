package api

import (
	"context"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.RoleServiceHandler = (*RoleAPI)(nil)

type RoleAPI struct{}

func (s *RoleAPI) ListRoles(_ context.Context, _ *connect.Request[procmeshv1.ListRolesRequest]) (*connect.Response[procmeshv1.ListRolesResponse], error) {
	return nil, unimplemented()
}

func (s *RoleAPI) CreateRole(_ context.Context, _ *connect.Request[procmeshv1.CreateRoleRequest]) (*connect.Response[procmeshv1.CreateRoleResponse], error) {
	return nil, unimplemented()
}

func (s *RoleAPI) GrantRole(_ context.Context, _ *connect.Request[procmeshv1.GrantRoleRequest]) (*connect.Response[procmeshv1.GrantRoleResponse], error) {
	return nil, unimplemented()
}
