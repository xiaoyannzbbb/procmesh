package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type loginForwarderFunc func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error)

func (f loginForwarderFunc) Login(ctx context.Context, route Route, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	return f(ctx, route, req)
}

func TestAuthAPI_LoginSetsSessionCookie(t *testing.T) {
	e := newAuthnEnv(t, true)
	resp, err := e.authc.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin",
		Password: testAdminPass,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetSessionId() == "" || resp.Msg.GetCsrfToken() == "" || resp.Msg.GetUserId() == "" {
		t.Fatalf("login %+v", resp.Msg)
	}
	if resp.Msg.GetUsername() != "admin" {
		t.Fatalf("username=%q", resp.Msg.GetUsername())
	}

	cookie := resp.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, auth.CookieName+"="+resp.Msg.GetSessionId()) {
		t.Fatalf("cookie missing session: %q", cookie)
	}
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Fatalf("cookie flags: %q", cookie)
	}
	if !strings.Contains(cookie, "Path=/") || !strings.Contains(cookie, "Max-Age=43200") {
		t.Fatalf("cookie path/age: %q", cookie)
	}
	if strings.Contains(cookie, "Secure") {
		t.Fatalf("cookie must not set Secure: %q", cookie)
	}
}

func TestAuthAPI_LoginRateLimitHasStableDetail(t *testing.T) {
	e := newAuthnEnv(t, true)
	for i := 0; i < 5; i++ {
		_, err := e.authc.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
			Username: "admin", Password: "wrong-password",
		}))
		_, detail := connectDetail(t, err)
		if detail != "INVALID_CREDENTIALS" {
			t.Fatalf("attempt %d detail=%q err=%v", i+1, detail, err)
		}
	}

	_, err := e.authc.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: "wrong-password",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeResourceExhausted || detail != "LOGIN_RATE_LIMITED" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
}

func TestAuthAPI_LockedAccountHasStableDetail(t *testing.T) {
	store, svc := newBootstrappedAuth(t)
	store.mu.Lock()
	admin := store.state.Users["admin"]
	admin.Status = control.UserLocked
	admin.LockedUntilUnix = time.Unix(1_700_000_000, 0).Add(time.Hour).Unix()
	store.state.Users["admin"] = admin
	store.mu.Unlock()

	api := &AuthAPI{Auth: svc}
	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "ACCOUNT_LOCKED" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
}

func TestToLoginConnectUsesTypedCodeInsteadOfMessage(t *testing.T) {
	tests := []struct {
		name        string
		code        errcode.Code
		message     string
		wantConnect connect.Code
		wantDetail  string
	}{
		{name: "invalid credentials", code: errcode.INVALID_CREDENTIALS, message: "changed copy", wantConnect: connect.CodePermissionDenied, wantDetail: "INVALID_CREDENTIALS"},
		{name: "rate limited", code: errcode.RATE_LIMITED, message: "changed copy", wantConnect: connect.CodeResourceExhausted, wantDetail: "LOGIN_RATE_LIMITED"},
		{name: "account locked", code: errcode.ACCOUNT_LOCKED, message: "changed copy", wantConnect: connect.CodePermissionDenied, wantDetail: "ACCOUNT_LOCKED"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, detail := connectDetail(t, toLoginConnect(errcode.E(tc.code, tc.message)))
			if code != tc.wantConnect || detail != tc.wantDetail {
				t.Fatalf("code=%v detail=%q", code, detail)
			}
		})
	}
}

func TestAuthAPI_FollowerForwardsLoginAndWaitsForLocalSession(t *testing.T) {
	store, followerAuth := newBootstrappedAuth(t)
	store.mu.Lock()
	admin := store.state.Users["admin"]
	admin.PasswordHash = testAdminHashForPassword(t, "different-pass")
	store.state.Users["admin"] = admin
	store.mu.Unlock()

	const (
		sessionID = "pms_forwarded"
		csrf      = "forwarded-csrf"
	)
	expiresUnix := time.Unix(1_700_000_000, 0).Add(time.Hour).Unix()
	replicated := make(chan error, 1)
	forward := loginForwarderFunc(func(_ context.Context, route Route, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
		if route.NodeID != "leader" || route.RPC != "127.0.0.1:18683" {
			t.Fatalf("route=%+v", route)
		}
		if rpc.SourceOf(req.Header()) != "follower" || rpc.TargetOf(req.Header()) != "leader" {
			t.Fatalf("hop headers source=%q target=%q", rpc.SourceOf(req.Header()), rpc.TargetOf(req.Header()))
		}
		if req.Msg.GetUsername() != "admin" || req.Msg.GetPassword() != testAdminPass {
			t.Fatalf("credentials not forwarded: %+v", req.Msg)
		}
		go func() {
			time.Sleep(20 * time.Millisecond)
			cmd, err := control.EncodeCommand(control.CmdSessionPut, control.SessionPutBody{
				ID: sessionID, UserID: "user-admin", CSRF: csrf, ExpiresUnix: expiresUnix,
			})
			if err == nil {
				err = store.Apply(cmd, 0)
			}
			replicated <- err
		}()
		resp := connect.NewResponse(&procmeshv1.LoginResponse{
			SessionId: sessionID, UserId: "user-admin", Username: "admin",
			ExpiresUnix: expiresUnix, CsrfToken: csrf,
		})
		setSessionCookie(resp.Header(), sessionID, sessionCookieMaxAge)
		return resp, nil
	})

	api := &AuthAPI{
		Auth: followerAuth, LocalID: "follower",
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{NodeID: "leader", RPC: "127.0.0.1:18683"}, nil
		},
		LoginForward: forward,
	}
	resp, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := <-replicated; err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetSessionId() != sessionID {
		t.Fatalf("session=%q", resp.Msg.GetSessionId())
	}
	if cookie := resp.Header().Get("Set-Cookie"); !strings.Contains(cookie, auth.CookieName+"="+sessionID) {
		t.Fatalf("forwarded response missing entry cookie: %q", cookie)
	}
	if _, err := followerAuth.AuthenticateSession(sessionID, csrf, true); err != nil {
		t.Fatalf("login returned before follower could authenticate session: %v", err)
	}
}

func TestAuthAPI_FollowerRefreshesLeaderAfterConfirmedNotLeader(t *testing.T) {
	store, followerAuth := newBootstrappedAuth(t)
	const (
		sessionID = "pms_after_leader_change"
		csrf      = "csrf-after-leader-change"
	)
	expiresUnix := time.Now().Add(time.Hour).Unix()

	var routes []string
	leaderCalls := 0
	api := &AuthAPI{
		Auth: followerAuth, LocalID: "entry",
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			leaderCalls++
			if leaderCalls == 1 {
				return Route{NodeID: "old-leader", RPC: "127.0.0.1:18683"}, nil
			}
			return Route{NodeID: "new-leader", RPC: "127.0.0.1:28683"}, nil
		},
		LoginForward: loginForwarderFunc(func(_ context.Context, route Route, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			routes = append(routes, route.NodeID)
			if got := req.Header().Get("Procmesh-Login-Hop"); got != "1" {
				t.Fatalf("login hop=%q, want 1", got)
			}
			if route.NodeID == "old-leader" {
				return nil, toConnectWithDetailCode(
					errcode.E(errcode.UNAVAILABLE, "login must be retried on the current leader"),
					"LOGIN_NOT_LEADER",
				)
			}
			cmd, err := control.EncodeCommand(control.CmdSessionPut, control.SessionPutBody{
				ID: sessionID, UserID: "user-admin", CSRF: csrf, ExpiresUnix: expiresUnix,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Apply(cmd, 0); err != nil {
				t.Fatal(err)
			}
			return connect.NewResponse(&procmeshv1.LoginResponse{
				SessionId: sessionID, UserId: "user-admin", Username: "admin",
				ExpiresUnix: expiresUnix, CsrfToken: csrf,
			}), nil
		}),
	}

	resp, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetSessionId() != sessionID {
		t.Fatalf("session=%q", resp.Msg.GetSessionId())
	}
	if got := strings.Join(routes, ","); got != "old-leader,new-leader" {
		t.Fatalf("routes=%q", got)
	}
}

func TestAuthAPI_FollowerWaitsForFreshLeaderBeforeSafeRetry(t *testing.T) {
	store, followerAuth := newBootstrappedAuth(t)
	const (
		sessionID = "pms_after_delayed_refresh"
		csrf      = "csrf-after-delayed-refresh"
	)
	expiresUnix := time.Now().Add(time.Hour).Unix()
	resolverCalls := 0
	var routes []string
	api := &AuthAPI{
		Auth: followerAuth, LocalID: "entry",
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			resolverCalls++
			if resolverCalls < 4 {
				return Route{NodeID: "old-leader", RPC: "127.0.0.1:18683"}, nil
			}
			return Route{NodeID: "new-leader", RPC: "127.0.0.1:28683"}, nil
		},
		LoginForward: loginForwarderFunc(func(_ context.Context, route Route, _ *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			routes = append(routes, route.NodeID)
			if route.NodeID == "old-leader" {
				return nil, toConnectWithDetailCode(
					errcode.E(errcode.UNAVAILABLE, "login must be retried on the current leader"),
					string(loginDetailNotLeader),
				)
			}
			cmd, err := control.EncodeCommand(control.CmdSessionPut, control.SessionPutBody{
				ID: sessionID, UserID: "user-admin", CSRF: csrf, ExpiresUnix: expiresUnix,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Apply(cmd, 0); err != nil {
				t.Fatal(err)
			}
			return connect.NewResponse(&procmeshv1.LoginResponse{
				SessionId: sessionID, UserId: "user-admin", Username: "admin",
				ExpiresUnix: expiresUnix, CsrfToken: csrf,
			}), nil
		}),
	}

	if _, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	})); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(routes, ","); got != "old-leader,new-leader" {
		t.Fatalf("routes=%q, stale leader must not receive a second login", got)
	}
}

func TestAuthAPI_LeaderRefreshHasBoundedTimeout(t *testing.T) {
	_, followerAuth := newBootstrappedAuth(t)
	forwardCalls := 0
	api := &AuthAPI{
		Auth: followerAuth, LocalID: "entry", LeaderRefreshWait: 30 * time.Millisecond,
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{NodeID: "stale-leader", RPC: "127.0.0.1:18683"}, nil
		},
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			forwardCalls++
			return nil, toConnectWithDetailCode(
				errcode.E(errcode.UNAVAILABLE, "login must be retried on the current leader"),
				string(loginDetailNotLeader),
			)
		}),
	}

	started := time.Now()
	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("leader refresh exceeded bound: %s", elapsed)
	}
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "LEADER_UNKNOWN" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	if forwardCalls != 1 {
		t.Fatalf("forward calls=%d, stale route must not receive a retry", forwardCalls)
	}
}

func TestAuthAPI_RefreshesStaleLocalLeaderBeforeLogin(t *testing.T) {
	store, followerAuth := newBootstrappedAuth(t)
	routeCalls := 0
	forwardCalls := 0
	api := &AuthAPI{
		Auth: followerAuth, LocalID: "old-leader",
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			routeCalls++
			if routeCalls == 1 {
				return Route{Local: true, NodeID: "old-leader", RPC: "127.0.0.1:18683"}, nil
			}
			return Route{NodeID: "new-leader", RPC: "127.0.0.1:28683"}, nil
		},
		LoginForward: loginForwarderFunc(func(_ context.Context, route Route, _ *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			forwardCalls++
			if route.NodeID != "new-leader" {
				t.Fatalf("forwarded to stale route: %+v", route)
			}
			return nil, toConnectWithDetailCode(
				errcode.E(errcode.INVALID_CREDENTIALS, "invalid credentials"),
				string(loginDetailInvalidCredentials),
			)
		}),
	}

	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: "wrong-password",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "INVALID_CREDENTIALS" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	if routeCalls < 2 || forwardCalls != 1 {
		t.Fatalf("route calls=%d forward calls=%d", routeCalls, forwardCalls)
	}
	store.mu.Lock()
	sessions := len(store.state.Sessions)
	store.mu.Unlock()
	if sessions != 0 {
		t.Fatalf("stale local leader created %d sessions", sessions)
	}
}

func TestAuthAPI_RejectsLoginBeyondOneAgentHop(t *testing.T) {
	_, followerAuth := newBootstrappedAuth(t)
	api := &AuthAPI{
		Auth:     followerAuth,
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			t.Fatal("hop limit must be checked before leader discovery")
			return Route{}, nil
		},
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			t.Fatal("hop limit must be checked before forwarding")
			return nil, nil
		}),
	}
	req := connect.NewRequest(&procmeshv1.LoginRequest{Username: "admin", Password: testAdminPass})
	req.Header().Set("Procmesh-Login-Hop", "2")

	_, err := api.Login(context.Background(), req)
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "LOGIN_FORWARD_HOP_LIMIT" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	if strings.Contains(err.Error(), "raft") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("internal routing detail leaked: %v", err)
	}
}

func TestAuthAPI_FollowerDoesNotRetryAmbiguousForwardFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "disconnect", err: connect.NewError(connect.CodeUnavailable, errors.New("dial tcp 10.0.0.4:18683: connection reset"))},
		{name: "timeout", err: connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, followerAuth := newBootstrappedAuth(t)
			forwardCalls := 0
			api := &AuthAPI{
				Auth: followerAuth, LocalID: "follower",
				IsLeader: func() bool { return false },
				LeaderRoute: func() (Route, error) {
					return Route{NodeID: "leader", RPC: "10.0.0.4:18683"}, nil
				},
				LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
					forwardCalls++
					return nil, tc.err
				}),
			}

			_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
				Username: "admin", Password: testAdminPass,
			}))
			code, detail := connectDetail(t, err)
			if code != connect.CodeUnavailable || detail != "LEADER_UNREACHABLE" {
				t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
			}
			if forwardCalls != 1 {
				t.Fatalf("forward calls=%d, ambiguous failure must not retry", forwardCalls)
			}
			if strings.Contains(err.Error(), "10.0.0.4") || strings.Contains(err.Error(), "dial tcp") {
				t.Fatalf("internal address leaked: %v", err)
			}
		})
	}
}

func TestAuthAPI_ForwardObservabilityRedactsSecrets(t *testing.T) {
	store, followerAuth := newBootstrappedAuth(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	const (
		username  = "sensitive-user"
		password  = "sensitive-password"
		sessionID = "pms_sensitive_session"
		csrf      = "sensitive-csrf"
		apiToken  = "pmt_sensitive_token"
	)
	expiresUnix := time.Now().Add(time.Hour).Unix()
	api := &AuthAPI{
		Auth: followerAuth, LocalID: "follower", Logger: logger,
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{NodeID: "leader", RPC: "127.0.0.1:18683"}, nil
		},
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			cmd, err := control.EncodeCommand(control.CmdSessionPut, control.SessionPutBody{
				ID: sessionID, UserID: "user-admin", CSRF: csrf, ExpiresUnix: expiresUnix,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Apply(cmd, 0); err != nil {
				t.Fatal(err)
			}
			return connect.NewResponse(&procmeshv1.LoginResponse{
				SessionId: sessionID, UserId: "user-admin", Username: username,
				ExpiresUnix: expiresUnix, CsrfToken: csrf,
			}), nil
		}),
	}
	req := connect.NewRequest(&procmeshv1.LoginRequest{Username: username, Password: password})
	req.Header().Set("Authorization", "Bearer "+apiToken)
	if _, err := api.Login(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	blob := logs.String()
	for _, want := range []string{
		`"msg":"login forward"`, `"attempt":1`, `"hop":1`, `"result":"success"`, `"duration_ms":`,
		`"msg":"login session visibility"`,
	} {
		if !strings.Contains(blob, want) {
			t.Fatalf("log missing %q: %s", want, blob)
		}
	}
	for _, secret := range []string{username, password, sessionID, csrf, apiToken} {
		if strings.Contains(blob, secret) {
			t.Fatalf("secret %q leaked in logs: %s", secret, blob)
		}
	}
}

func TestAuthAPI_FollowerSessionWaitTimeoutIsUnavailable(t *testing.T) {
	_, followerAuth := newBootstrappedAuth(t)
	api := &AuthAPI{
		Auth: followerAuth, LocalID: "follower",
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{NodeID: "leader", RPC: "127.0.0.1:18683"}, nil
		},
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			return connect.NewResponse(&procmeshv1.LoginResponse{
				SessionId: "pms_not_replicated", UserId: "user-admin", Username: "admin",
				ExpiresUnix: time.Unix(1_700_000_000, 0).Add(time.Hour).Unix(), CsrfToken: "csrf",
			}), nil
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := api.Login(ctx, connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "SESSION_VISIBILITY_TIMEOUT" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
}

func TestAuthAPI_FollowerSessionWaitHasServerTimeout(t *testing.T) {
	_, followerAuth := newBootstrappedAuth(t)
	api := &AuthAPI{
		Auth: followerAuth, LocalID: "follower", SessionWaitTimeout: 25 * time.Millisecond,
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{NodeID: "leader", RPC: "127.0.0.1:18683"}, nil
		},
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			return connect.NewResponse(&procmeshv1.LoginResponse{
				SessionId: "pms_not_replicated", UserId: "user-admin", Username: "admin",
				ExpiresUnix: time.Unix(1_700_000_000, 0).Add(time.Hour).Unix(), CsrfToken: "csrf",
			}), nil
		}),
	}

	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "SESSION_VISIBILITY_TIMEOUT" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
}

func TestAuthAPI_FollowerWithoutLeaderDoesNotLoginLocally(t *testing.T) {
	store, followerAuth := newBootstrappedAuth(t)
	resolverErr := errors.New("raft leader unavailable")
	api := &AuthAPI{
		Auth:     followerAuth,
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{}, resolverErr
		},
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			t.Fatal("forwarder must not run without a leader route")
			return nil, nil
		}),
	}
	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "LEADER_UNKNOWN" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	if strings.Contains(err.Error(), "raft") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("internal leader detail leaked: %v", err)
	}
	if !errors.Is(err, resolverErr) {
		t.Fatalf("leader resolver cause was lost: %v", err)
	}
	store.mu.Lock()
	sessions := len(store.state.Sessions)
	store.mu.Unlock()
	if sessions != 0 {
		t.Fatalf("follower created %d local sessions", sessions)
	}
}

func TestAuthAPI_LeaderRefreshPreservesResolverCause(t *testing.T) {
	_, followerAuth := newBootstrappedAuth(t)
	resolverErr := errors.New("resolver failed")
	forwardCalls := 0
	api := &AuthAPI{
		Auth: followerAuth, LocalID: "entry", LeaderRefreshWait: 30 * time.Millisecond,
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			if forwardCalls == 0 {
				return Route{NodeID: "stale-leader", RPC: "127.0.0.1:18683"}, nil
			}
			return Route{}, resolverErr
		},
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			forwardCalls++
			return nil, toConnectWithDetailCode(
				errcode.E(errcode.UNAVAILABLE, "login must be retried on the current leader"),
				string(loginDetailNotLeader),
			)
		}),
	}

	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "LEADER_UNKNOWN" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	if !errors.Is(err, resolverErr) {
		t.Fatalf("leader refresh cause was lost: %v", err)
	}
	if strings.Contains(err.Error(), resolverErr.Error()) {
		t.Fatalf("leader resolver cause leaked: %v", err)
	}
}

func TestAuthAPI_LeaderDiscoveryFailureIsObservableAndRedacted(t *testing.T) {
	_, followerAuth := newBootstrappedAuth(t)
	var logs bytes.Buffer
	api := &AuthAPI{
		Auth: followerAuth, Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{}, errors.New("raft leader 10.0.0.9:18685 unavailable")
		},
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			t.Fatal("must not forward without leader")
			return nil, nil
		}),
	}

	_, _ = api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "secret-user", Password: "secret-password",
	}))
	blob := logs.String()
	for _, want := range []string{
		`"msg":"login leader discovery"`, `"attempt":1`, `"result":"leader_unknown"`, `"duration_ms":`,
	} {
		if !strings.Contains(blob, want) {
			t.Fatalf("log missing %q: %s", want, blob)
		}
	}
	for _, secret := range []string{"secret-user", "secret-password", "10.0.0.9", "raft"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("internal or secret value %q leaked: %s", secret, blob)
		}
	}
}

func TestAuthAPI_LeaderWithoutQuorumRejectsBeforeCreatingSession(t *testing.T) {
	store, svc := newBootstrappedAuth(t)
	api := &AuthAPI{
		Auth:      svc,
		IsLeader:  func() bool { return true },
		HasQuorum: func() bool { return false },
	}

	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "CONTROL_QUORUM_UNAVAILABLE" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	store.mu.Lock()
	sessions := len(store.state.Sessions)
	store.mu.Unlock()
	if sessions != 0 {
		t.Fatalf("created %d sessions without quorum", sessions)
	}
	if strings.Contains(err.Error(), "raft") {
		t.Fatalf("raft detail leaked: %v", err)
	}
}

func TestAuthAPI_FollowerWithoutQuorumDoesNotCreateAuthState(t *testing.T) {
	store, svc := newBootstrappedAuth(t)
	api := &AuthAPI{
		Auth:      svc,
		IsLeader:  func() bool { return false },
		HasQuorum: func() bool { return false },
		LoginForward: loginForwarderFunc(func(context.Context, Route, *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
			t.Fatal("follower must not forward login without quorum")
			return nil, nil
		}),
	}

	for _, password := range []string{testAdminPass, "wrong-password"} {
		_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
			Username: "admin", Password: password,
		}))
		code, detail := connectDetail(t, err)
		if code != connect.CodeUnavailable || detail != "CONTROL_QUORUM_UNAVAILABLE" {
			t.Fatalf("password=%q code=%v detail=%q err=%v", password, code, detail, err)
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	admin := store.state.Users["admin"]
	if len(store.state.Sessions) != 0 || admin.FailCount != 0 || admin.Status != control.UserActive || admin.LockedUntilUnix != 0 {
		t.Fatalf("follower created split auth state: sessions=%d admin=%+v", len(store.state.Sessions), admin)
	}
}

func TestAuthAPI_LoginDoesNotExposeAmbiguousRaftWriteFailure(t *testing.T) {
	store, svc := newBootstrappedAuth(t)
	store.applyErr = errcode.E(errcode.UNAVAILABLE, "not raft leader at 10.0.0.8:18685")
	api := &AuthAPI{
		Auth:      svc,
		IsLeader:  func() bool { return true },
		HasQuorum: func() bool { return true },
	}

	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin", Password: testAdminPass,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "CONTROL_QUORUM_UNAVAILABLE" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	if strings.Contains(err.Error(), "raft") || strings.Contains(err.Error(), "10.0.0.8") {
		t.Fatalf("internal raft detail leaked: %v", err)
	}
}

func testAdminHashForPassword(t *testing.T, password string) string {
	t.Helper()
	hash, err := control.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestAuthAPI_LogoutRevokesSession(t *testing.T) {
	ctx := context.Background()
	e := newAuthnEnv(t, true)
	sid, csrf := e.login(t)

	req := connect.NewRequest(&procmeshv1.LogoutRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-logout", Operator: "t"},
	})
	req.Header().Set("Cookie", auth.CookieName+"="+sid)
	req.Header().Set(auth.HeaderCSRF, csrf)
	if _, err := e.authc.Logout(ctx, req); err != nil {
		t.Fatal(err)
	}

	list := connect.NewRequest(&procmeshv1.ListProcessesRequest{})
	list.Header().Set("Authorization", "Bearer "+sid)
	_, err := e.proc.ListProcesses(ctx, list)
	assertDenied(t, err)
}

func TestAuthAPI_CreateAndRevokeToken(t *testing.T) {
	ctx := context.Background()
	e := newAuthnEnv(t, true)
	sid, csrf := e.login(t)

	create := connect.NewRequest(&procmeshv1.CreateAPITokenRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
		Name:       "ci",
		TtlSeconds: 3600,
	})
	create.Header().Set("Cookie", auth.CookieName+"="+sid)
	create.Header().Set(auth.HeaderCSRF, csrf)
	tok, err := e.authc.CreateAPIToken(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	plain := tok.Msg.GetToken()
	if !strings.HasPrefix(plain, "pmt_") || tok.Msg.GetTokenId() == "" {
		t.Fatalf("token %+v", tok.Msg)
	}

	list := connect.NewRequest(&procmeshv1.ListProcessesRequest{})
	list.Header().Set("Authorization", "Bearer "+plain)
	if _, err := e.proc.ListProcesses(ctx, list); err != nil {
		t.Fatal(err)
	}

	rev := connect.NewRequest(&procmeshv1.RevokeAPITokenRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-rev", Operator: "t"},
		TokenId: tok.Msg.GetTokenId(),
	})
	rev.Header().Set("Cookie", auth.CookieName+"="+sid)
	rev.Header().Set(auth.HeaderCSRF, csrf)
	if _, err := e.authc.RevokeAPIToken(ctx, rev); err != nil {
		t.Fatal(err)
	}

	_, err = e.proc.ListProcesses(ctx, list)
	assertDenied(t, err)
}

func TestAuthAPI_LogoutUnauthDenied(t *testing.T) {
	e := newAuthnEnv(t, true)
	_, err := e.authc.Logout(context.Background(), connect.NewRequest(&procmeshv1.LogoutRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-lo", Operator: "t"},
	}))
	assertDenied(t, err)
}

func TestAuthAPI_CreateTokenUnauthDenied(t *testing.T) {
	e := newAuthnEnv(t, true)
	_, err := e.authc.CreateAPIToken(context.Background(), connect.NewRequest(&procmeshv1.CreateAPITokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-tok", Operator: "t"},
		Name: "ci",
	}))
	assertDenied(t, err)
}

func TestAuthAPI_LoginNilAuthUnimplemented(t *testing.T) {
	// Auth==nil keeps existing stub behavior for unit tests that do not inject Auth.
	api := &AuthAPI{}
	_, err := api.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin",
		Password: testAdminPass,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestAuthAPI_CookieRoundTrip(t *testing.T) {
	// httptest client does not persist cookies; send Set-Cookie value back explicitly.
	e := newAuthnEnv(t, true)
	resp, err := e.authc.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin",
		Password: testAdminPass,
	}))
	if err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&procmeshv1.ListProcessesRequest{})
	req.Header().Set("Cookie", parseSetCookie(resp.Header()))
	if _, err := e.proc.ListProcesses(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func parseSetCookie(h http.Header) string {
	v := h.Get("Set-Cookie")
	if i := strings.IndexByte(v, ';'); i >= 0 {
		return v[:i]
	}
	return v
}

func TestAuth_GetMeReturnsPermissions(t *testing.T) {
	ctx := context.Background()
	e := newAuthnEnv(t, true)

	sid, _ := e.login(t)
	adminReq := connect.NewRequest(&procmeshv1.GetMeRequest{})
	adminReq.Header().Set("Cookie", auth.CookieName+"="+sid)
	adminMe, err := e.authc.GetMe(ctx, adminReq)
	if err != nil {
		t.Fatal(err)
	}
	adminPerms := adminMe.Msg.GetPermissions()
	if !containsStr(adminPerms, "process.restart") {
		t.Fatalf("super_admin missing process.restart: %v", adminPerms)
	}
	if !sortedStrings(adminPerms) {
		t.Fatalf("super_admin permissions not sorted: %v", adminPerms)
	}

	putViewerUser(t, e.svc)
	viewLogin, err := e.authc.Login(ctx, connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "viewer",
		Password: testAdminPass,
	}))
	if err != nil {
		t.Fatal(err)
	}
	viewReq := connect.NewRequest(&procmeshv1.GetMeRequest{})
	viewReq.Header().Set("Cookie", auth.CookieName+"="+viewLogin.Msg.GetSessionId())
	viewMe, err := e.authc.GetMe(ctx, viewReq)
	if err != nil {
		t.Fatal(err)
	}
	viewPerms := viewMe.Msg.GetPermissions()
	if containsStr(viewPerms, "process.restart") {
		t.Fatalf("viewer must not have process.restart: %v", viewPerms)
	}
	if !containsStr(viewPerms, "process.read") {
		t.Fatalf("viewer missing process.read: %v", viewPerms)
	}
	if !sortedStrings(viewPerms) {
		t.Fatalf("viewer permissions not sorted: %v", viewPerms)
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func sortedStrings(ss []string) bool {
	for i := 1; i < len(ss); i++ {
		if ss[i-1] > ss[i] {
			return false
		}
	}
	return true
}
