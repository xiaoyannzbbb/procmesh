package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/creack/pty"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestCLI_LoginInteractivePasswordIsNotEchoed(t *testing.T) {
	t.Setenv("PROCMESH_PASSWORD", "")
	var mu sync.Mutex
	var lastAuth string
	srv := newAuthUserServer(t, &mu, &lastAuth)

	const password = "unique-pty-password"
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLI_LoginPTYHelper$", "--",
		"--server", srv.URL, "login", "--user", "admin")
	cmd.Env = append(os.Environ(),
		"GO_WANT_PROCMESH_CLI_HELPER=1",
		"PROCMESH_PASSWORD=",
		"PROCMESH_SESSION="+filepath.Join(t.TempDir(), "session"),
	)

	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = terminal.Close() })

	type readResult struct {
		output []byte
		err    error
	}
	readDone := make(chan readResult, 1)
	go func() {
		output, err := io.ReadAll(terminal)
		readDone <- readResult{output: output, err: err}
	}()

	// Let the child block in its password reader before writing to the PTY.
	time.Sleep(100 * time.Millisecond)
	if _, err := io.WriteString(terminal, password+"\n"); err != nil {
		t.Fatal(err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("login helper: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("login helper timed out")
	}
	_ = terminal.Close()
	result := <-readDone
	if result.err != nil && !strings.Contains(result.err.Error(), "file already closed") {
		t.Fatalf("read PTY: %v", result.err)
	}
	if strings.Contains(string(result.output), password) {
		t.Fatalf("terminal echoed password: %q", result.output)
	}
}

func TestCLI_LoginPTYHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PROCMESH_CLI_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		os.Exit(2)
	}
	os.Exit(Main(os.Args[separator+1:], os.Stdin, os.Stdout, os.Stderr))
}

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

func TestCLI_LoginPasswordInputCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     string
		env      string
		stdin    string
		password string
	}{
		{name: "flag wins", flag: "flag-password", env: "env-password", stdin: "stdin-password\n", password: "flag-password"},
		{name: "environment wins", env: "env-password", stdin: "stdin-password\n", password: "env-password"},
		{name: "stdin line", stdin: "stdin-password\n", password: "stdin-password"},
		{name: "stdin EOF", stdin: "stdin-password", password: "stdin-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROCMESH_PASSWORD", tc.env)
			sessionPath := filepath.Join(t.TempDir(), "session")
			t.Setenv("PROCMESH_SESSION", sessionPath)
			passwords := make(chan string, 1)
			srv := newPasswordServer(t, passwords)
			args := []string{"--server", srv.URL, "login", "--user", "admin"}
			if tc.flag != "" {
				args = append(args, "--password", tc.flag)
			}
			var stdout, stderr bytes.Buffer
			if code := Main(args, strings.NewReader(tc.stdin), &stdout, &stderr); code != 0 {
				t.Fatalf("login exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
			}
			if got := <-passwords; got != tc.password {
				t.Fatalf("password=%q want %q", got, tc.password)
			}
			raw, err := os.ReadFile(sessionPath)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(tc.password)) || strings.Contains(stdout.String(), tc.password) || strings.Contains(stderr.String(), tc.password) {
				t.Fatal("login output or session file contains password")
			}
		})
	}
}

func TestCLI_LoginRejectsMissingPassword(t *testing.T) {
	t.Setenv("PROCMESH_PASSWORD", "")
	for _, tc := range []struct {
		name  string
		stdin io.Reader
	}{
		{name: "nil input"},
		{name: "empty input", stdin: strings.NewReader("")},
		{name: "blank line", stdin: strings.NewReader("\n")},
		{name: "read error", stdin: errorReader{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Main([]string{"login", "--user", "admin"}, tc.stdin, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
			}
			if !strings.Contains(stderr.String(), "login requires a password") {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
}

func TestResolvePasswordReportsTerminalReadError(t *testing.T) {
	origIsTerminal := isTerminalFn
	origReadPassword := readPasswordFn
	t.Cleanup(func() {
		isTerminalFn = origIsTerminal
		readPasswordFn = origReadPassword
	})

	input, err := os.CreateTemp(t.TempDir(), "terminal-input")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })

	wantErr := errors.New("terminal read failed")
	isTerminalFn = func(int) bool { return true }
	readPasswordFn = func(int) ([]byte, error) { return nil, wantErr }

	_, err = resolvePassword("", input)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want wrapped %v", err, wantErr)
	}
	if !isUsageError(err) {
		t.Fatalf("error=%T, want usage error", err)
	}
	if !strings.Contains(err.Error(), "login could not read password") {
		t.Fatalf("error=%q", err)
	}

	var stdout, stderr bytes.Buffer
	if code := Main([]string{"login", "--user", "admin"}, input, &stdout, &stderr); code != 2 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "login could not read password: terminal read failed") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

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
	passwords chan<- string
}

func (s loginStub) Login(_ context.Context, req *connect.Request[procmeshv1.LoginRequest]) (*connect.Response[procmeshv1.LoginResponse], error) {
	if s.passwords != nil {
		s.passwords <- req.Msg.GetPassword()
	}
	return connect.NewResponse(&procmeshv1.LoginResponse{
		SessionId:   "pms_cli_session",
		UserId:      "user-admin",
		Username:    req.Msg.GetUsername(),
		ExpiresUnix: 1_700_003_600,
		CsrfToken:   "csrf",
	}), nil
}

func newPasswordServer(t *testing.T, passwords chan<- string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := procmeshv1connect.NewAuthServiceHandler(loginStub{passwords: passwords})
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
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
