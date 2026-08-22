package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.AuthServiceHandler = (*AuthAPI)(nil)

const (
	sessionCookieMaxAge       = 43200 // 12h; matches control.SessionTTL
	sessionReplicationPoll    = 10 * time.Millisecond
	defaultSessionWaitTimeout = 5 * time.Second
)

type LoginForwarder interface {
	Login(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error)
}

type AuthAPI struct {
	Auth               *auth.Service
	LocalID            string
	IsLeader           func() bool
	LeaderRoute        func() (Route, error)
	LoginForward       LoginForwarder
	SessionWaitTimeout time.Duration
}

func (s *AuthAPI) Login(ctx context.Context, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	if s.Auth == nil {
		return nil, unimplemented()
	}
	if s.IsLeader != nil && !s.IsLeader() {
		return s.forwardLogin(ctx, req)
	}
	sid, csrf, userID, exp, err := s.Auth.Login(req.Msg.GetUsername(), req.Msg.GetPassword())
	if err != nil {
		return nil, toLoginConnect(err)
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

func (s *AuthAPI) forwardLogin(ctx context.Context, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	if s.LeaderRoute == nil || s.LoginForward == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader unavailable"))
	}
	route, err := s.LeaderRoute()
	if err != nil {
		return nil, ToConnect(err)
	}
	if route.Local {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader route resolved locally"))
	}
	if route.NodeID == "" || route.RPC == "" {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader unavailable"))
	}
	stampHop(req.Header(), s.LocalID, route.NodeID)
	resp, err := s.LoginForward.Login(ctx, route, req)
	if err != nil {
		return nil, toLoginConnect(rpc.MapCallError(err))
	}
	if err := s.waitForSession(ctx, resp.Msg.GetSessionId(), resp.Msg.GetCsrfToken()); err != nil {
		return nil, ToConnect(err)
	}
	return resp, nil
}

func (s *AuthAPI) waitForSession(ctx context.Context, sessionID, csrf string) error {
	timeout := s.SessionWaitTimeout
	if timeout <= 0 {
		timeout = defaultSessionWaitTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(sessionReplicationPoll)
	defer ticker.Stop()
	for {
		if _, err := s.Auth.AuthenticateSession(sessionID, csrf, true); err == nil {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return errcode.E(errcode.UNAVAILABLE, "session replication timed out")
		case <-ticker.C:
		}
	}
}

func toLoginConnect(err error) error {
	var coded *errcode.Error
	if errors.As(err, &coded) && coded.Code == errcode.DENIED && coded.Msg == "invalid credentials" {
		return toConnectWithDetailCode(err, "INVALID_CREDENTIALS")
	}
	return ToConnect(err)
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

func (s *AuthAPI) GetMe(ctx context.Context, _ *connect.Request[procmeshv1.GetMeRequest]) (*connect.Response[procmeshv1.GetMeResponse], error) {
	if s.Auth == nil {
		return nil, unimplemented()
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, ToConnect(errcode.E(errcode.DENIED, "authentication required"))
	}
	return connect.NewResponse(&procmeshv1.GetMeResponse{
		UserId:      p.UserID,
		Username:    p.Username,
		CsrfToken:   p.CSRF,
		Permissions: collectPermissions(s.Auth, p.UserID),
	}), nil
}

func collectPermissions(svc *auth.Service, userID string) []string {
	if svc == nil || svc.Store() == nil {
		return nil
	}
	view := svc.Store().View()
	seen := make(map[string]struct{})
	var out []string
	for _, b := range view.Bindings {
		if b.UserID != userID {
			continue
		}
		role, ok := view.Roles[b.RoleID]
		if !ok {
			continue
		}
		for _, perm := range role.Perms {
			if _, dup := seen[perm]; dup {
				continue
			}
			seen[perm] = struct{}{}
			out = append(out, perm)
		}
	}
	sort.Strings(out)
	return out
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
	// :18680 may be plaintext this phase; P5/反代终结 TLS. Do not set Secure.
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
