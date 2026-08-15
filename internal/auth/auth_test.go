package auth_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

const adminPass = "admin-pass-ok"

var (
	hashOnce  sync.Once
	adminHash string
	hashErr   error
)

func adminPasswordHash(t *testing.T) string {
	t.Helper()
	hashOnce.Do(func() {
		adminHash, hashErr = control.HashPassword(adminPass)
	})
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	return adminHash
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeStore struct {
	state  *control.State
	quorum bool
	fresh  bool
	now    func() time.Time
}

func (f *fakeStore) View() control.State { return *f.state }

func (f *fakeStore) Apply(cmd control.Command, _ time.Duration) error {
	now := time.Now()
	if f.now != nil {
		now = f.now()
	}
	return f.state.Apply(cmd, now)
}

func (f *fakeStore) HasQuorum() bool                 { return f.quorum }
func (f *fakeStore) CacheFresh(_ time.Duration) bool { return f.fresh }

func newTestSvc(t *testing.T) (*auth.Service, *fakeStore, *clock) {
	t.Helper()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	st := control.NewState()
	cmd, err := control.EncodeCommand(control.CmdBootstrap, control.BootstrapBody{
		ClusterID:    "cid-1",
		AdminUser:    "admin",
		PasswordHash: adminPasswordHash(t),
		AdminUserID:  "user-admin",
		NowUnix:      clk.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(cmd, clk.Now()); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{state: st, quorum: true, fresh: true, now: clk.Now}
	svc := &auth.Service{Now: clk.Now}
	svc.SetStore(store)
	return svc, store, clk
}

func mustLogin(t *testing.T, svc *auth.Service) (sessionID, csrf, userID string, expires time.Time) {
	t.Helper()
	sid, c, uid, exp, err := svc.Login("admin", adminPass)
	if err != nil {
		t.Fatal(err)
	}
	return sid, c, uid, exp
}

func requireCode(t *testing.T, err error, code errcode.Code, msg string) {
	t.Helper()
	if !errcode.Is(err, code) {
		t.Fatalf("got %v want %s", err, code)
	}
	if msg != "" {
		want := string(code) + ": " + msg
		if err.Error() != want {
			t.Fatalf("msg=%q want %q", err.Error(), want)
		}
	}
}

func apply(t *testing.T, store *fakeStore, typ string, body any) {
	t.Helper()
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(cmd, 0); err != nil {
		t.Fatal(err)
	}
}

func TestLogin_SuccessAndBearer(t *testing.T) {
	svc, store, clk := newTestSvc(t)
	sid, csrf, uid, exp, err := svc.Login("admin", adminPass)
	if err != nil {
		t.Fatal(err)
	}
	if uid != "user-admin" {
		t.Fatalf("userID=%q", uid)
	}
	if !strings.HasPrefix(sid, "pms_") || len(sid) != 4+64 {
		t.Fatalf("session=%q", sid)
	}
	raw, err := hex.DecodeString(csrf)
	if err != nil || len(raw) != 32 {
		t.Fatalf("csrf=%q err=%v", csrf, err)
	}
	wantExp := clk.Now().Add(control.SessionTTL)
	if !exp.Equal(wantExp) {
		t.Fatalf("exp=%s want=%s", exp, wantExp)
	}

	sess, ok := store.state.SessionByID(sid)
	if !ok || sess.UserID != "user-admin" || sess.CSRF != csrf {
		t.Fatalf("stored=%+v ok=%v", sess, ok)
	}
	if sess.ExpiresUnix != wantExp.Unix() {
		t.Fatalf("stored exp=%d", sess.ExpiresUnix)
	}
	admin := store.state.Users["admin"]
	if admin.FailCount != 0 || admin.LastLoginUnix != clk.Now().Unix() {
		t.Fatalf("login_ok not applied: %+v", admin)
	}

	p, err := svc.AuthenticateBearer(sid)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "user-admin" || p.Username != "admin" || p.SessionID != sid || p.TokenID != "" {
		t.Fatalf("bearer=%+v", p)
	}

	p, err = svc.AuthenticateSession(sid, csrf, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.CSRF != csrf {
		t.Fatalf("session principal=%+v", p)
	}
}

func TestLogin_BadPassword(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	_, _, _, _, err := svc.Login("admin", "wrong-password")
	requireCode(t, err, errcode.DENIED, "invalid credentials")
	if store.state.Users["admin"].FailCount != 1 {
		t.Fatalf("fail=%d", store.state.Users["admin"].FailCount)
	}

	_, _, _, _, err = svc.Login("nobody", adminPass)
	requireCode(t, err, errcode.DENIED, "invalid credentials")

	_, _, _, _, err = svc.Login("", adminPass)
	requireCode(t, err, errcode.INVALID, "")
	_, _, _, _, err = svc.Login("admin", "")
	requireCode(t, err, errcode.INVALID, "")
}

func TestLogin_RateLimit(t *testing.T) {
	svc, _, _ := newTestSvc(t)
	for i := 0; i < 5; i++ {
		_, _, _, _, err := svc.Login("admin", "wrong-password")
		requireCode(t, err, errcode.DENIED, "invalid credentials")
	}
	_, _, _, _, err := svc.Login("admin", "wrong-password")
	requireCode(t, err, errcode.DENIED, "login rate limited")
	_, _, _, _, err = svc.Login("admin", adminPass)
	requireCode(t, err, errcode.DENIED, "login rate limited")
}

func TestLogin_Lockout(t *testing.T) {
	svc, store, clk := newTestSvc(t)
	for i := 0; i < 10; i++ {
		if i == 5 {
			clk.Add(time.Minute + time.Second)
		}
		_, _, _, _, err := svc.Login("admin", "wrong-password")
		requireCode(t, err, errcode.DENIED, "invalid credentials")
	}
	u := store.state.Users["admin"]
	if u.Status != control.UserLocked {
		t.Fatalf("status=%s", u.Status)
	}
	wantUntil := clk.Now().Add(control.LockFor).Unix()
	if u.LockedUntilUnix != wantUntil {
		t.Fatalf("locked_until=%d want=%d", u.LockedUntilUnix, wantUntil)
	}

	clk.Add(time.Minute + time.Second)
	_, _, _, _, err := svc.Login("admin", adminPass)
	requireCode(t, err, errcode.DENIED, "user locked")

	clk.Add(control.LockFor)
	sid, _, uid, _, err := svc.Login("admin", adminPass)
	if err != nil {
		t.Fatal(err)
	}
	if uid != "user-admin" || !strings.HasPrefix(sid, "pms_") {
		t.Fatalf("sid=%q uid=%q", sid, uid)
	}
	if store.state.Users["admin"].Status != control.UserActive {
		t.Fatalf("status=%s", store.state.Users["admin"].Status)
	}
}

func TestLogin_ShortPasswordRejectedOnCreatePath(t *testing.T) {
	err := auth.ValidPassword("short")
	requireCode(t, err, errcode.INVALID, "")
	if err := auth.ValidPassword(strings.Repeat("x", control.MinPasswordLen-1)); err == nil {
		t.Fatal("expected invalid")
	}
	if err := auth.ValidPassword(strings.Repeat("x", control.MinPasswordLen)); err != nil {
		t.Fatal(err)
	}
}

func TestSession_CSRFRequiredForMutation(t *testing.T) {
	svc, _, _ := newTestSvc(t)
	sid, csrf, _, _ := mustLogin(t, svc)

	_, err := svc.AuthenticateSession(sid, "wrong-csrf", true)
	requireCode(t, err, errcode.DENIED, "csrf mismatch")

	p, err := svc.AuthenticateSession(sid, csrf, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.SessionID != sid {
		t.Fatalf("%+v", p)
	}

	p, err = svc.AuthenticateSession(sid, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "user-admin" {
		t.Fatalf("%+v", p)
	}

	if _, err := svc.AuthenticateBearer(sid); err != nil {
		t.Fatal(err)
	}
}

func TestAPIToken_ShownOnce(t *testing.T) {
	svc, store, clk := newTestSvc(t)
	plain, tokenID, exp, err := svc.CreateAPIToken("user-admin", "cli", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "pmt_") || tokenID == "" {
		t.Fatalf("plain=%q id=%q", plain, tokenID)
	}
	if !exp.Equal(clk.Now().Add(time.Hour)) {
		t.Fatalf("exp=%s", exp)
	}

	p, err := svc.AuthenticateBearer(plain)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "user-admin" || p.Username != "admin" || p.TokenID != tokenID || p.SessionID != "" {
		t.Fatalf("bearer=%+v", p)
	}

	view, err := json.Marshal(store.View())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(view, []byte(plain)) {
		t.Fatal("plaintext leaked")
	}
	sum := sha256.Sum256([]byte(plain))
	wantHash := hex.EncodeToString(sum[:])
	stored, ok := store.state.APITokens[tokenID]
	if !ok || stored.Hash != wantHash || stored.Hash == plain {
		t.Fatalf("stored=%+v", stored)
	}

	if err := svc.RevokeAPIToken(tokenID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.AuthenticateBearer(plain)
	requireCode(t, err, errcode.DENIED, "")
}

func TestAuthenticateTokenID_ByInternalID(t *testing.T) {
	svc, store, clk := newTestSvc(t)
	plain, tokenID, _, err := svc.CreateAPIToken("user-admin", "hop", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.AuthenticateTokenID(tokenID)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "user-admin" || p.TokenID != tokenID || p.SessionID != "" {
		t.Fatalf("principal=%+v", p)
	}
	_, err = svc.AuthenticateTokenID(plain)
	requireCode(t, err, errcode.DENIED, "")
	_, err = svc.AuthenticateTokenID("")
	requireCode(t, err, errcode.DENIED, "")
	_, err = svc.AuthenticateTokenID("missing")
	requireCode(t, err, errcode.DENIED, "")

	if err := svc.RevokeAPIToken(tokenID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.AuthenticateTokenID(tokenID)
	requireCode(t, err, errcode.DENIED, "token revoked")

	_, expID, _, err := svc.CreateAPIToken("user-admin", "exp", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Minute)
	_, err = svc.AuthenticateTokenID(expID)
	requireCode(t, err, errcode.DENIED, "token expired")

	_, foreverID, _, err := svc.CreateAPIToken("user-admin", "forever", 0)
	if err != nil {
		t.Fatal(err)
	}
	clk.Add(365 * 24 * time.Hour)
	p, err = svc.AuthenticateTokenID(foreverID)
	if err != nil {
		t.Fatal(err)
	}
	if p.TokenID != foreverID {
		t.Fatalf("zero ttl=%+v", p)
	}

	apply(t, store, control.CmdUserDisable, control.UserDisableBody{UserID: "user-admin"})
	_, err = svc.AuthenticateTokenID(foreverID)
	requireCode(t, err, errcode.DENIED, "user not active")
}

func TestAPIToken_ZeroTTLNeverExpires(t *testing.T) {
	svc, store, clk := newTestSvc(t)
	plain, tokenID, exp, err := svc.CreateAPIToken("user-admin", "forever", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.IsZero() {
		t.Fatalf("expires=%s want zero", exp)
	}
	stored, ok := store.state.APITokens[tokenID]
	if !ok {
		t.Fatal("missing token")
	}
	if stored.ExpiresUnix != 0 {
		t.Fatalf("ExpiresUnix=%d want 0", stored.ExpiresUnix)
	}
	if stored.ExpiresUnix == clk.Now().Add(time.Hour).Unix() {
		t.Fatal("stored now+1h default")
	}

	clk.Add(365 * 24 * time.Hour)
	p, err := svc.AuthenticateBearer(plain)
	if err != nil {
		t.Fatal(err)
	}
	if p.TokenID != tokenID || p.UserID != "user-admin" {
		t.Fatalf("bearer=%+v", p)
	}

	plain2, id2, exp2, err := svc.CreateAPIToken("user-admin", "neg", -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !exp2.IsZero() || store.state.APITokens[id2].ExpiresUnix != 0 {
		t.Fatalf("neg ttl exp=%s unix=%d", exp2, store.state.APITokens[id2].ExpiresUnix)
	}
	if _, err := svc.AuthenticateBearer(plain2); err != nil {
		t.Fatal(err)
	}
}

func TestLogin_DisabledUser(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	apply(t, store, control.CmdUserDisable, control.UserDisableBody{UserID: "user-admin"})
	_, _, _, _, err := svc.Login("admin", adminPass)
	requireCode(t, err, errcode.DENIED, "user disabled")
}

func TestLogout_DeletesSession(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	sid, _, _, _ := mustLogin(t, svc)
	if err := svc.Logout(sid); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.state.SessionByID(sid); ok {
		t.Fatal("session still present")
	}
	_, err := svc.AuthenticateSession(sid, "", false)
	requireCode(t, err, errcode.DENIED, "")
}

func TestAuthenticate_ExpiredAndInactive(t *testing.T) {
	svc, store, clk := newTestSvc(t)
	sid, csrf, _, _ := mustLogin(t, svc)
	clk.Add(control.SessionTTL + time.Second)
	_, err := svc.AuthenticateSession(sid, csrf, true)
	requireCode(t, err, errcode.DENIED, "")
	_, err = svc.AuthenticateBearer(sid)
	requireCode(t, err, errcode.DENIED, "")

	plain, _, _, err := svc.CreateAPIToken("user-admin", "old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Minute)
	_, err = svc.AuthenticateBearer(plain)
	requireCode(t, err, errcode.DENIED, "")

	_, err = svc.AuthenticateBearer("nope_xxx")
	requireCode(t, err, errcode.DENIED, "")
	_, err = svc.AuthenticateBearer("")
	requireCode(t, err, errcode.DENIED, "")

	sid, csrf, _, _ = mustLogin(t, svc)
	apply(t, store, control.CmdUserDisable, control.UserDisableBody{UserID: "user-admin"})
	_, err = svc.AuthenticateSession(sid, csrf, false)
	requireCode(t, err, errcode.DENIED, "")
	_, err = svc.AuthenticateBearer(sid)
	requireCode(t, err, errcode.DENIED, "")
}

func TestService_SetStoreConcurrentWithLogin(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	svc.SetStore(nil)
	_, _, _, _, err := svc.Login("admin", adminPass)
	requireCode(t, err, errcode.UNAVAILABLE, "auth store not ready")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			svc.SetStore(store)
			svc.SetStore(store)
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		_, _, _, _, last = svc.Login("admin", adminPass)
		if last == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	<-done
	if last != nil {
		t.Fatalf("login after SetStore: %v", last)
	}
	if svc.Store() == nil {
		t.Fatal("Store() nil after SetStore")
	}
}

func TestService_NilClockAndConstants(t *testing.T) {
	if auth.CookieName != "procmesh_session" {
		t.Fatalf("cookie=%q", auth.CookieName)
	}
	if auth.HeaderCSRF != "X-CSRF-Token" {
		t.Fatalf("csrf header=%q", auth.HeaderCSRF)
	}
	_, store, _ := newTestSvc(t)
	svc := &auth.Service{}
	svc.SetStore(store)
	sid, _, _, exp, err := svc.Login("admin", adminPass)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sid, "pms_") {
		t.Fatalf("sid=%q", sid)
	}
	if time.Until(exp) < 11*time.Hour {
		t.Fatalf("exp=%s", exp)
	}
}
