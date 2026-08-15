package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

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
