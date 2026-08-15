package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

const testAdminPass = "admin-pass-ok"

var (
	testHashOnce sync.Once
	testHash     string
	testHashErr  error
)

func testAdminHash(t *testing.T) string {
	t.Helper()
	testHashOnce.Do(func() {
		testHash, testHashErr = control.HashPassword(testAdminPass)
	})
	if testHashErr != nil {
		t.Fatal(testHashErr)
	}
	return testHash
}

type memAuthStore struct {
	mu     sync.Mutex
	state  *control.State
	now    func() time.Time
	quorum bool
	fresh  bool
}

func (m *memAuthStore) View() control.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.state
}

func (m *memAuthStore) Apply(cmd control.Command, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	return m.state.Apply(cmd, now)
}

func (m *memAuthStore) HasQuorum() bool                 { return m.quorum }
func (m *memAuthStore) CacheFresh(_ time.Duration) bool { return m.fresh }

func newTestAuthService(t *testing.T) *auth.Service {
	t.Helper()
	_, svc := newBootstrappedAuth(t)
	return svc
}

func newBootstrappedAuth(t *testing.T) (*memAuthStore, *auth.Service) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	st := control.NewState()
	cmd, err := control.EncodeCommand(control.CmdBootstrap, control.BootstrapBody{
		ClusterID:    "cid-authn",
		AdminUser:    "admin",
		PasswordHash: testAdminHash(t),
		AdminUserID:  "user-admin",
		NowUnix:      now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(cmd, now); err != nil {
		t.Fatal(err)
	}
	mem := &memAuthStore{state: st, now: func() time.Time { return now }, quorum: true, fresh: true}
	svc := &auth.Service{Now: func() time.Time { return now }}
	svc.SetStore(mem)
	return mem, svc
}

type authnEnv struct {
	srv     *Server
	url     string
	http    *http.Client
	proc    procmeshv1connect.ProcessServiceClient
	authc   procmeshv1connect.AuthServiceClient
	cluster procmeshv1connect.ClusterServiceClient
	svc     *auth.Service
}

func newAuthnEnv(t *testing.T, inited bool) *authnEnv {
	t.Helper()
	return newAuthnEnvInit(t, inited, false)
}

func newAuthnEnvInit(t *testing.T, inited, realInit bool) *authnEnv {
	t.Helper()
	m, st, layout := newTestManager(t)
	_, svc := newBootstrappedAuth(t)
	if inited && !realInit {
		if err := st.SetClusterID(context.Background(), "cid-authn"); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := NewServer(Options{
		Mgr:   m,
		Store: st,
		Auth:  svc,
		Cluster: ClusterDeps{
			Dir:   layout.ClusterDir,
			Store: st,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)
	env := &authnEnv{
		srv:     srv,
		url:     hs.URL,
		http:    hs.Client(),
		proc:    procmeshv1connect.NewProcessServiceClient(hs.Client(), hs.URL),
		authc:   procmeshv1connect.NewAuthServiceClient(hs.Client(), hs.URL),
		cluster: procmeshv1connect.NewClusterServiceClient(hs.Client(), hs.URL),
		svc:     svc,
	}
	if realInit {
		resp, err := env.cluster.Init(context.Background(), connect.NewRequest(&procmeshv1.InitClusterRequest{
			Meta:          &procmeshv1.MutationMeta{OperationId: "op-init", Operator: "t"},
			AdminUsername: "admin",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Msg.GetClusterId() == "" {
			t.Fatal("init cluster_id empty")
		}
	}
	return env
}

func (e *authnEnv) login(t *testing.T) (sessionID, csrf string) {
	t.Helper()
	resp, err := e.authc.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin",
		Password: testAdminPass,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.GetSessionId(), resp.Msg.GetCsrfToken()
}

func assertDenied(t *testing.T, err error) {
	t.Helper()
	code, detail := connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestPrincipal_RoundTrip(t *testing.T) {
	if _, ok := PrincipalFrom(context.Background()); ok {
		t.Fatal("empty ctx")
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "u1", SessionID: "s1"})
	p, ok := PrincipalFrom(ctx)
	if !ok || p.UserID != "u1" || p.SessionID != "s1" {
		t.Fatalf("%+v ok=%v", p, ok)
	}
}

func TestAuthn_UnauthAfterInitWhenAuthInjected(t *testing.T) {
	ctx := context.Background()
	e := newAuthnEnvInit(t, true, true)
	_, err := e.proc.ListProcesses(ctx, connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	assertDenied(t, err)

	sid, _ := e.login(t)
	req := connect.NewRequest(&procmeshv1.ListProcessesRequest{})
	req.Header().Set("Authorization", "Bearer "+sid)
	if _, err := e.proc.ListProcesses(ctx, req); err != nil {
		t.Fatal(err)
	}
}

func TestAuthn_StandaloneAllowsUnauth(t *testing.T) {
	e := newAuthnEnv(t, false)
	if _, err := e.proc.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{})); err != nil {
		t.Fatal(err)
	}
}

func TestAuthn_LoginThenList(t *testing.T) {
	e := newAuthnEnv(t, true)
	sid, _ := e.login(t)
	req := connect.NewRequest(&procmeshv1.ListProcessesRequest{})
	req.Header().Set("Authorization", "Bearer "+sid)
	if _, err := e.proc.ListProcesses(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestAuthn_BadPasswordDenied(t *testing.T) {
	e := newAuthnEnv(t, true)
	_, err := e.authc.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "admin",
		Password: "wrong-password",
	}))
	assertDenied(t, err)
	if !strings.Contains(err.Error(), "invalid credentials") {
		t.Fatalf("want invalid credentials: %v", err)
	}
}

func TestAuthn_JoinAndLoginRemainPublic(t *testing.T) {
	ctx := context.Background()
	e := newAuthnEnvInit(t, true, true)
	e.login(t)
	_, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-join", Operator: "t"},
	}))
	if err == nil {
		t.Fatal("join should fail on missing token/csr, not succeed")
	}
	code, detail := connectDetail(t, err)
	if detail == "DENIED" || code == connect.CodePermissionDenied {
		t.Fatalf("Join must stay public: code=%v detail=%s err=%v", code, detail, err)
	}

	_, err = e.cluster.RequestJoin(ctx, connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-rjoin", Operator: "t"},
		SeedServer: "http://127.0.0.1:9",
		Token:      "pmj_x",
	}))
	if err == nil {
		t.Fatal("requestjoin should fail after init")
	}
	code, detail = connectDetail(t, err)
	if detail == "DENIED" || code == connect.CodePermissionDenied {
		t.Fatalf("RequestJoin must stay public: code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestAuthn_CookieMutationNeedsCSRF(t *testing.T) {
	ctx := context.Background()
	e := newAuthnEnv(t, true)
	sid, csrf := e.login(t)

	list := connect.NewRequest(&procmeshv1.ListProcessesRequest{})
	list.Header().Set("Cookie", auth.CookieName+"="+sid)
	if _, err := e.proc.ListProcesses(ctx, list); err != nil {
		t.Fatal(err)
	}

	bad := connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-csrf", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	})
	bad.Header().Set("Cookie", auth.CookieName+"="+sid)
	_, err := e.proc.ApplyProcess(ctx, bad)
	assertDenied(t, err)
	if !strings.Contains(err.Error(), "csrf mismatch") {
		t.Fatalf("want csrf mismatch: %v", err)
	}

	ok := connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-csrf-ok", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	})
	ok.Header().Set("Cookie", auth.CookieName+"="+sid)
	ok.Header().Set(auth.HeaderCSRF, csrf)
	if _, err := e.proc.ApplyProcess(ctx, ok); err != nil {
		t.Fatal(err)
	}
}

func TestAuthn_HealthzOpen(t *testing.T) {
	e := newAuthnEnv(t, true)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		e.srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("%s %d %q", path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	e.srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics %d %q", rec.Code, rec.Body.String())
	}
}
