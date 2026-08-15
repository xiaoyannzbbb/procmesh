package api

import (
	"context"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.AuthServiceHandler = (*AuthAPI)(nil)

type AuthAPI struct{}

func (s *AuthAPI) Login(_ context.Context, _ *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	return nil, unimplemented()
}

func (s *AuthAPI) Logout(_ context.Context, _ *connect.Request[procmeshv1.LogoutRequest]) (*connect.Response[procmeshv1.LogoutResponse], error) {
	return nil, unimplemented()
}

func (s *AuthAPI) CreateAPIToken(_ context.Context, _ *connect.Request[procmeshv1.CreateAPITokenRequest]) (*connect.Response[procmeshv1.CreateAPITokenResponse], error) {
	return nil, unimplemented()
}

func (s *AuthAPI) RevokeAPIToken(_ context.Context, _ *connect.Request[procmeshv1.RevokeAPITokenRequest]) (*connect.Response[procmeshv1.RevokeAPITokenResponse], error) {
	return nil, unimplemented()
}
