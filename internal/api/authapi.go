package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
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
	leaderRefreshPoll         = 20 * time.Millisecond
	defaultLeaderRefreshWait  = time.Second
)

type loginDetailCode string

const (
	loginDetailNotLeader          loginDetailCode = "LOGIN_NOT_LEADER"
	loginDetailForwardHopLimit    loginDetailCode = "LOGIN_FORWARD_HOP_LIMIT"
	loginDetailInvalidCredentials loginDetailCode = "INVALID_CREDENTIALS"
	loginDetailRateLimited        loginDetailCode = "LOGIN_RATE_LIMITED"
	loginDetailAccountLocked      loginDetailCode = "ACCOUNT_LOCKED"
	loginDetailLeaderUnknown      loginDetailCode = "LEADER_UNKNOWN"
	loginDetailLeaderUnreachable  loginDetailCode = "LEADER_UNREACHABLE"
	loginDetailQuorumUnavailable  loginDetailCode = "CONTROL_QUORUM_UNAVAILABLE"
	loginDetailSessionTimeout     loginDetailCode = "SESSION_VISIBILITY_TIMEOUT"
)

type LoginForwarder interface {
	Login(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error)
}

type AuthAPI struct {
	Auth               *auth.Service
	Logger             *slog.Logger
	LocalID            string
	IsLeader           func() bool
	HasQuorum          func() bool
	LeaderRoute        func() (Route, error)
	LoginForward       LoginForwarder
	SessionWaitTimeout time.Duration
	LeaderRefreshWait  time.Duration
}

func (s *AuthAPI) Login(ctx context.Context, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	if s.Auth == nil {
		return nil, unimplemented()
	}
	hop, err := loginHop(req.Header())
	if err != nil || hop > 1 {
		return nil, toConnectWithDetailCode(
			errcode.E(errcode.CONFLICT, "login forwarding hop limit exceeded"),
			string(loginDetailForwardHopLimit),
		)
	}
	isLeader := s.IsLeader == nil || s.IsLeader()
	if !isLeader {
		if hop == 1 {
			return nil, toConnectWithDetailCode(
				errcode.E(errcode.UNAVAILABLE, "login must be retried on the current leader"),
				string(loginDetailNotLeader),
			)
		}
		if s.HasQuorum != nil && !s.HasQuorum() {
			return nil, loginUnavailable(loginDetailQuorumUnavailable, "control quorum is unavailable")
		}
		return s.forwardLogin(ctx, req)
	}
	if s.HasQuorum != nil && !s.HasQuorum() {
		return nil, loginUnavailable(loginDetailQuorumUnavailable, "control quorum is unavailable")
	}
	ttl, err := auth.ResolveSessionTTL(req.Msg.GetTtlSeconds())
	if err != nil {
		return nil, toLoginConnect(err)
	}
	sid, csrf, userID, exp, err := s.Auth.LoginWithTTL(req.Msg.GetUsername(), req.Msg.GetPassword(), ttl)
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
	setSessionCookie(resp.Header(), sid, int(ttl/time.Second))
	return resp, nil
}

func loginHop(h http.Header) (int, error) {
	raw := rpc.LoginHopOf(h)
	if raw == "" {
		return 0, nil
	}
	hop, err := strconv.Atoi(raw)
	if err != nil || hop < 0 {
		return 0, errors.New("invalid login hop")
	}
	return hop, nil
}

func (s *AuthAPI) forwardLogin(ctx context.Context, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	if s.LeaderRoute == nil {
		return nil, loginUnavailable(loginDetailLeaderUnknown, "leader is unknown")
	}
	if s.LoginForward == nil {
		return nil, loginUnavailable(loginDetailLeaderUnreachable, "leader is unreachable")
	}
	discoveryStarted := time.Now()
	route, err := s.LeaderRoute()
	if err != nil || route.Local || route.NodeID == "" || route.RPC == "" {
		stale := route
		route, err = s.waitForFreshLeader(ctx, stale, err)
		if err != nil {
			s.logLoginLeaderDiscovery(1, discoveryStarted)
			return nil, loginUnavailableCause(loginDetailLeaderUnknown, "leader is unknown", err)
		}
		if route.Local {
			return s.Login(ctx, req)
		}
	}
	resp, err := s.forwardLoginAttempt(ctx, route, req, 1)
	if isLoginNotLeader(err) {
		discoveryStarted = time.Now()
		route, err = s.waitForFreshLeader(ctx, route, nil)
		if err != nil {
			s.logLoginLeaderDiscovery(2, discoveryStarted)
			return nil, loginUnavailableCause(loginDetailLeaderUnknown, "leader is unknown", err)
		}
		if route.Local {
			return s.Login(ctx, req)
		}
		resp, err = s.forwardLoginAttempt(ctx, route, req, 2)
	}
	if err != nil {
		switch loginDetailOf(err) {
		case "":
			return nil, loginUnavailableCause(loginDetailLeaderUnreachable, "leader is unreachable", err)
		case loginDetailNotLeader:
			return nil, loginUnavailableCause(loginDetailLeaderUnknown, "leader is unknown", err)
		case loginDetailCode(errcode.UNAVAILABLE), loginDetailCode(errcode.TIMEOUT):
			return nil, loginUnavailableCause(loginDetailQuorumUnavailable, "control quorum is unavailable", err)
		}
		return nil, toLoginConnect(rpc.MapCallError(err))
	}
	waitStarted := time.Now()
	if err := s.waitForSession(ctx, resp.Msg.GetSessionId(), resp.Msg.GetCsrfToken()); err != nil {
		s.logLoginSessionVisibility("timeout", waitStarted)
		return nil, loginUnavailableCause(loginDetailSessionTimeout, "session is not yet available on this agent", err)
	}
	s.logLoginSessionVisibility("success", waitStarted)
	return resp, nil
}

func (s *AuthAPI) waitForFreshLeader(ctx context.Context, stale Route, initialErr error) (Route, error) {
	timeout := s.LeaderRefreshWait
	if timeout <= 0 {
		timeout = defaultLeaderRefreshWait
	}
	refreshCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(leaderRefreshPoll)
	defer ticker.Stop()
	lastErr := initialErr
	for {
		route, err := s.LeaderRoute()
		if err == nil {
			switch {
			case route.Local && (s.IsLeader == nil || s.IsLeader()) && (s.HasQuorum == nil || s.HasQuorum()):
				return route, nil
			case !route.Local && route.NodeID != "" && route.RPC != "" && (route.NodeID != stale.NodeID || route.RPC != stale.RPC):
				return route, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-refreshCtx.Done():
			cause := error(refreshCtx.Err())
			if lastErr != nil {
				cause = errors.Join(cause, lastErr)
			}
			return Route{}, errcode.Wrap(errcode.UNAVAILABLE, "leader refresh timed out", cause)
		case <-ticker.C:
		}
	}
}

func (s *AuthAPI) logLoginLeaderDiscovery(attempt int, started time.Time) {
	if s.Logger == nil {
		return
	}
	s.Logger.Info("login leader discovery",
		"attempt", attempt,
		"result", "leader_unknown",
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func loginUnavailable(detailCode loginDetailCode, message string) error {
	return toConnectWithDetailCode(errcode.E(errcode.UNAVAILABLE, message), string(detailCode))
}

func loginUnavailableCause(detailCode loginDetailCode, message string, cause error) error {
	return newConnectWithDetailCode(errcode.Wrap(errcode.UNAVAILABLE, message, cause), string(detailCode))
}

func (s *AuthAPI) forwardLoginAttempt(ctx context.Context, route Route, req *connect.Request[procmeshv1.LoginRequest], attempt int) (*connect.Response[procmeshv1.LoginResponse], error) {
	stampHop(req.Header(), s.LocalID, route.NodeID)
	rpc.SetLoginHop(req.Header(), "1")
	started := time.Now()
	resp, err := s.LoginForward.Login(ctx, route, req)
	if s.Logger != nil {
		s.Logger.Info("login forward",
			"attempt", attempt,
			"hop", 1,
			"result", loginForwardResult(err),
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}
	return resp, err
}

func (s *AuthAPI) logLoginSessionVisibility(result string, started time.Time) {
	if s.Logger == nil {
		return
	}
	s.Logger.Info("login session visibility",
		"result", result,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func loginForwardResult(err error) string {
	if err == nil {
		return "success"
	}
	switch loginDetailOf(err) {
	case loginDetailNotLeader:
		return "not_leader"
	case loginDetailInvalidCredentials:
		return "invalid_credentials"
	case loginDetailRateLimited:
		return "rate_limited"
	case loginDetailAccountLocked:
		return "account_locked"
	case loginDetailQuorumUnavailable:
		return "quorum_unavailable"
	default:
		return "unavailable"
	}
}

func isLoginNotLeader(err error) bool {
	return loginDetailOf(err) == loginDetailNotLeader
}

func loginDetailOf(err error) loginDetailCode {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return ""
	}
	for _, detail := range ce.Details() {
		msg, detailErr := detail.Value()
		if detailErr != nil {
			continue
		}
		if info, ok := msg.(*procmeshv1.ErrorInfo); ok {
			return loginDetailCode(info.GetCode())
		}
	}
	return ""
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
	if errors.As(err, &coded) {
		switch coded.Code {
		case errcode.UNAVAILABLE, errcode.TIMEOUT:
			return loginUnavailableCause(loginDetailQuorumUnavailable, "control quorum is unavailable", err)
		case errcode.INVALID_CREDENTIALS:
			return toConnectWithDetailCode(err, string(loginDetailInvalidCredentials))
		case errcode.RATE_LIMITED:
			return toConnectWithDetailCode(err, string(loginDetailRateLimited))
		case errcode.ACCOUNT_LOCKED:
			return toConnectWithDetailCode(err, string(loginDetailAccountLocked))
		}
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

func (s *AuthAPI) ChangePassword(ctx context.Context, req *connect.Request[procmeshv1.ChangePasswordRequest]) (*connect.Response[procmeshv1.ChangePasswordResponse], error) {
	if s.Auth == nil {
		return nil, unimplemented()
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, ToConnect(errcode.E(errcode.DENIED, "authentication required"))
	}
	if err := s.Auth.ChangePassword(p.UserID, req.Msg.GetCurrentPassword(), req.Msg.GetNewPassword()); err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.ChangePasswordResponse{}), nil
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
