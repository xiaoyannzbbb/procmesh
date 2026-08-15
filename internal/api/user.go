package api

import (
	"context"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.UserServiceHandler = (*UserAPI)(nil)

type UserAPI struct{}

func (s *UserAPI) ListUsers(_ context.Context, _ *connect.Request[procmeshv1.ListUsersRequest]) (*connect.Response[procmeshv1.ListUsersResponse], error) {
	return nil, unimplemented()
}

func (s *UserAPI) CreateUser(_ context.Context, _ *connect.Request[procmeshv1.CreateUserRequest]) (*connect.Response[procmeshv1.CreateUserResponse], error) {
	return nil, unimplemented()
}

func (s *UserAPI) DisableUser(_ context.Context, _ *connect.Request[procmeshv1.DisableUserRequest]) (*connect.Response[procmeshv1.DisableUserResponse], error) {
	return nil, unimplemented()
}
