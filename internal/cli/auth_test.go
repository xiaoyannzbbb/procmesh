package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestCLI_LoginWritesSessionAndSendsBearer(t *testing.T) {
	orig := sessionFileFn
	t.Cleanup(func() { sessionFileFn = orig })
	path := filepath.Join(t.TempDir(), "session")
	sessionFileFn = func() string { return path }
	t.Setenv("PROCMESH_PASSWORD", "")

	var mu sync.Mutex
	var lastAuth string
	srv := newAuthUserServer(t, &mu, &lastAuth)
	const sid = "pms_cli_session"

	code, out, errb := runCLI("--server", srv.URL, "login", "--user", "admin", "--password", "admin-pass-ok")
	if code != 0 {
		t.Fatalf("login exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session perm=%o want 0600", info.Mode().Perm())
	}
	var sess fileSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatal(err)
	}
	if sess.SessionID != sid || sess.UserID != "user-admin" || sess.ExpiresUnix != 1_700_003_600 {
		t.Fatalf("session %+v", sess)
	}
	if sess.Server == "" || normalizeServer(sess.Server) != normalizeServer(srv.URL) {
		t.Fatalf("session.server=%q url=%q", sess.Server, srv.URL)
	}

	mu.Lock()
	lastAuth = ""
	mu.Unlock()
	code, _, errb = runCLI("--server", srv.URL, "user", "list")
	if code != 0 {
		t.Fatalf("user list exit=%d stderr=%q", code, errb)
	}
	mu.Lock()
	got := lastAuth
	mu.Unlock()
	if got != "Bearer "+sid {
		t.Fatalf("bearer=%q", got)
	}

	mu.Lock()
	lastAuth = ""
	mu.Unlock()
	code, _, errb = runCLI("--server", srv.URL, "--auth-token", "pmt_override", "user", "list")
	if code != 0 {
		t.Fatalf("override exit=%d stderr=%q", code, errb)
	}
	mu.Lock()
	got = lastAuth
	mu.Unlock()
	if got != "Bearer pmt_override" {
		t.Fatalf("override bearer=%q", got)
	}

	sess.Server = "http://127.0.0.1:9"
	if err := writeSession(path, sess); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	lastAuth = "sentinel"
	mu.Unlock()
	code, _, _ = runCLI("--server", srv.URL, "user", "list")
	if code != 0 {
		t.Fatalf("mismatch list exit=%d", code)
	}
	mu.Lock()
	got = lastAuth
	mu.Unlock()
	if got != "" {
		t.Fatalf("mismatched server must not send bearer, got=%q", got)
	}
}

func TestCLI_UserCreateUsage(t *testing.T) {
	code, _, errb := runCLI("user", "create")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "--user") || !strings.Contains(errb, "--password") {
		t.Fatalf("stderr=%q", errb)
	}
	code, _, errb = runCLI("user")
	if code != 2 {
		t.Fatalf("missing sub exit=%d stderr=%q", code, errb)
	}
}

type loginStub struct {
	procmeshv1connect.UnimplementedAuthServiceHandler
}

func (loginStub) Login(_ context.Context, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	return connect.NewResponse(&procmeshv1.LoginResponse{
		SessionId:   "pms_cli_session",
		UserId:      "user-admin",
		Username:    req.Msg.GetUsername(),
		ExpiresUnix: 1_700_003_600,
		CsrfToken:   "csrf",
	}), nil
}

type userListStub struct {
	procmeshv1connect.UnimplementedUserServiceHandler
}

func (userListStub) ListUsers(context.Context, *connect.Request[procmeshv1.ListUsersRequest]) (*connect.Response[procmeshv1.ListUsersResponse], error) {
	return connect.NewResponse(&procmeshv1.ListUsersResponse{}), nil
}

func newAuthUserServer(t *testing.T, mu *sync.Mutex, lastAuth *string) *httptest.Server {
	t.Helper()
	capture := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			mu.Lock()
			*lastAuth = req.Header().Get("Authorization")
			mu.Unlock()
			return next(ctx, req)
		}
	}))
	mux := http.NewServeMux()
	ap, ah := procmeshv1connect.NewAuthServiceHandler(loginStub{}, capture)
	mux.Handle(ap, ah)
	up, uh := procmeshv1connect.NewUserServiceHandler(userListStub{}, capture)
	mux.Handle(up, uh)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
