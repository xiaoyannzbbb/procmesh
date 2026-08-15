package api

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.AuthServiceHandler = (*AuthAPI)(nil)

const sessionCookieMaxAge = 43200 // 12h; matches control.SessionTTL

type AuthAPI struct {
	Auth *auth.Service
}

func (s *AuthAPI) Login(_ context.Context, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	if s.Auth == nil {
		return nil, unimplemented()
	}
	sid, csrf, userID, exp, err := s.Auth.Login(req.Msg.GetUsername(), req.Msg.GetPassword())
	if err != nil {
		return nil, ToConnect(err)
	}
	resp := connect.NewResponse(&procmeshv1.LoginResponse{
		SessionId:   sid,
		UserId:      userID,
		Username:    req.Msg.GetUsername(),
		ExpiresUnix: exp.Unix(),
		CsrfToken:   csrf,
	})
	setSessionCookie(resp.Header(), sid, sessionCookieMaxAge)
	return resp, nil
}

func (s *AuthAPI) Logout(ctx context.Context, _ *connect.Request[procmeshv1.LogoutRequest]) (*connect.Response[procmeshv1.LogoutResponse], error) {
	if s.Auth == nil {
		return nil, unimplemented()
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, ToConnect(errcode.E(errcode.DENIED, "authentication required"))
	}
	if p.SessionID != "" {
		if err := s.Auth.Logout(p.SessionID); err != nil {
			return nil, ToConnect(err)
		}
	}
	resp := connect.NewResponse(&procmeshv1.LogoutResponse{})
	setSessionCookie(resp.Header(), "", -1)
	return resp, nil
}

func (s *AuthAPI) CreateAPIToken(ctx context.Context, req *connect.Request[procmeshv1.CreateAPITokenRequest]) (*connect.Response[procmeshv1.CreateAPITokenResponse], error) {
	if s.Auth == nil {
		return nil, unimplemented()
	}
	if err := requirePerm(ctx, s.Auth, auth.PermUserUpdate, "", true, true); err != nil {
		return nil, err
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, ToConnect(errcode.E(errcode.DENIED, "authentication required"))
	}
	ttl := time.Duration(req.Msg.GetTtlSeconds()) * time.Second
	plain, tokenID, exp, err := s.Auth.CreateAPIToken(p.UserID, req.Msg.GetName(), ttl)
	if err != nil {
		return nil, ToConnect(err)
	}
	var expUnix int64
	if !exp.IsZero() {
		expUnix = exp.Unix()
	}
	return connect.NewResponse(&procmeshv1.CreateAPITokenResponse{
		TokenId:     tokenID,
		Token:       plain,
		ExpiresUnix: expUnix,
	}), nil
}

func (s *AuthAPI) RevokeAPIToken(ctx context.Context, req *connect.Request[procmeshv1.RevokeAPITokenRequest]) (*connect.Response[procmeshv1.RevokeAPITokenResponse], error) {
	if s.Auth == nil {
		return nil, unimplemented()
	}
	if err := requirePerm(ctx, s.Auth, auth.PermUserUpdate, "", true, true); err != nil {
		return nil, err
	}
	if _, ok := PrincipalFrom(ctx); !ok {
		return nil, ToConnect(errcode.E(errcode.DENIED, "authentication required"))
	}
	if err := s.Auth.RevokeAPIToken(req.Msg.GetTokenId()); err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.RevokeAPITokenResponse{}), nil
}

func setSessionCookie(h http.Header, value string, maxAge int) {
	// :9000 may be plaintext this phase; P5/反代终结 TLS. Do not set Secure.
	c := &http.Cookie{
		Name:     auth.CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}
