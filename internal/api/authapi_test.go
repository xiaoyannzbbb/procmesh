package api

import (
	"context"
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
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
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
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
}

func TestAuthAPI_FollowerWithoutLeaderDoesNotLoginLocally(t *testing.T) {
	store, followerAuth := newBootstrappedAuth(t)
	api := &AuthAPI{
		Auth:     followerAuth,
		IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{}, errcode.E(errcode.UNAVAILABLE, "raft leader unavailable")
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
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	store.mu.Lock()
	sessions := len(store.state.Sessions)
	store.mu.Unlock()
	if sessions != 0 {
		t.Fatalf("follower created %d local sessions", sessions)
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
