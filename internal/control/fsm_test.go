package control_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestFSM_BootstrapCreatesAdminAndBuiltinRoles(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)

	if s.ClusterID != "cid-1" {
		t.Fatalf("cluster=%q", s.ClusterID)
	}
	if s.Policy.RBACCacheTTL != 5*time.Minute {
		t.Fatalf("ttl=%s", s.Policy.RBACCacheTTL)
	}

	admin, ok := s.Users["admin"]
	if !ok || admin.Status != control.UserActive || admin.ID != "user-admin" {
		t.Fatalf("admin=%+v ok=%v", admin, ok)
	}

	wantRoles := map[string][]string{
		"super_admin":   allPerms,
		"cluster_admin": clusterAdminPerms,
		"operator":      operatorPerms,
		"viewer":        viewerPerms,
	}
	for id, perms := range wantRoles {
		role, ok := s.Roles[id]
		if !ok {
			t.Fatalf("missing role %s", id)
		}
		if !sameStrings(role.Perms, perms) {
			t.Fatalf("role %s perms=%v want=%v", id, role.Perms, perms)
		}
	}

	found := false
	for _, b := range s.Bindings {
		if b.UserID == "user-admin" && b.RoleID == "super_admin" && b.Scope == control.ScopeCluster && b.ScopeID == "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("admin binding missing: %+v", s.Bindings)
	}
}

func TestFSM_BootstrapConflict(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	cmd := mustEncode(t, "bootstrap", control.BootstrapBody{
		ClusterID:    "other",
		AdminUser:    "admin",
		PasswordHash: "x",
		AdminUserID:  "u2",
		NowUnix:      now.Unix(),
	})
	err := s.Apply(cmd, now)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("got %v", err)
	}
}

func TestFSM_UserPutDuplicateUsername(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	body := control.UserPutBody{ID: "u1", Username: "alice", PasswordHash: "h1"}
	if err := s.Apply(mustEncode(t, "user_put", body), now); err != nil {
		t.Fatal(err)
	}
	err := s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u2", Username: "alice", PasswordHash: "h2"}), now)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("got %v", err)
	}
}

func TestFSM_LoginFailLocksAfter10(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u1", Username: "bob", PasswordHash: "h"}), now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := s.Apply(mustEncode(t, "login_fail", control.LoginFailBody{Username: "bob"}), now); err != nil {
			t.Fatal(err)
		}
	}
	u := s.Users["bob"]
	if u.Status != control.UserLocked {
		t.Fatalf("status=%s", u.Status)
	}
	want := now.Add(15 * time.Minute).Unix()
	if u.LockedUntilUnix != want {
		t.Fatalf("locked_until=%d want=%d", u.LockedUntilUnix, want)
	}
	if u.FailCount != 10 {
		t.Fatalf("fail=%d", u.FailCount)
	}
}

func TestFSM_SessionPutAndGet(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "session_put", control.SessionPutBody{
		ID:     "pms_abc",
		UserID: "user-admin",
		CSRF:   "csrf-1",
	}), now); err != nil {
		t.Fatal(err)
	}
	sess, ok := s.SessionByID("pms_abc")
	if !ok {
		t.Fatal("missing session")
	}
	if sess.UserID != "user-admin" || sess.CSRF != "csrf-1" {
		t.Fatalf("%+v", sess)
	}
	if sess.ExpiresUnix != now.Add(12*time.Hour).Unix() {
		t.Fatalf("exp=%d", sess.ExpiresUnix)
	}
}

func TestFSM_APITokenHashOnly(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	f := control.NewFSM()
	mustFSMApply(t, f, "bootstrap", control.BootstrapBody{
		ClusterID:    "cid-1",
		AdminUser:    "admin",
		PasswordHash: "h",
		AdminUserID:  "user-admin",
		NowUnix:      now.Unix(),
	}, now)

	plain := "pmt_only-shown-once-secret"
	sum := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(sum[:])
	mustFSMApply(t, f, "token_put", control.TokenPutBody{
		ID:          "tok-1",
		UserID:      "user-admin",
		Name:        "cli",
		Hash:        hash,
		ExpiresUnix: now.Add(time.Hour).Unix(),
	}, now)

	view := f.View()
	tok, ok := view.TokenByPlain(plain)
	if !ok || tok.ID != "tok-1" || tok.Hash != hash {
		t.Fatalf("lookup=%+v ok=%v", tok, ok)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(plain)) {
		t.Fatal("plaintext leaked into View")
	}
	stored, ok := view.APITokens["tok-1"]
	if !ok || stored.Hash != hash {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestFSM_JoinTokenConsumeOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	plain := "pmj_join-once"
	sum := sha256.Sum256([]byte(plain))
	if err := s.Apply(mustEncode(t, "join_token_put", control.JoinTokenPutBody{
		ID:         "jt-1",
		Hash:       hex.EncodeToString(sum[:]),
		TTLSeconds: int64(time.Hour.Seconds()),
		Remaining:  1,
	}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "join_token_consume", control.JoinTokenConsumeBody{Plain: plain}), now); err != nil {
		t.Fatal(err)
	}
	err := s.Apply(mustEncode(t, "join_token_consume", control.JoinTokenConsumeBody{Plain: plain}), now)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("second consume: %v", err)
	}
}

func TestFSM_MemberRemoveAddsCRL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "member_put", control.MemberPutBody{
		NodeID:     "node-1",
		RaftAddr:   "127.0.0.1:9002",
		CertSerial: "DEADBEEF",
		Status:     control.MemberAdmitted,
	}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "member_remove", control.MemberRemoveBody{NodeID: "node-1"}), now); err != nil {
		t.Fatal(err)
	}
	m, ok := s.Member("node-1")
	if !ok || m.Status != control.MemberRevoked {
		t.Fatalf("member=%+v ok=%v", m, ok)
	}
	if !s.SerialRevoked("DEADBEEF") {
		t.Fatal("serial not on CRL")
	}
}

func TestFSM_CheckSuperAdminAllowsAll(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	for _, perm := range allPerms {
		if !s.Check("user-admin", perm, "") {
			t.Fatalf("super_admin denied %s", perm)
		}
	}
}

func TestFSM_CheckViewerDeniesRestart(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u-view", Username: "view", PasswordHash: "h"}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "bind_put", control.BindPutBody{
		UserID: "u-view",
		RoleID: "viewer",
		Scope:  control.ScopeCluster,
	}), now); err != nil {
		t.Fatal(err)
	}
	if !s.Check("u-view", "process.read", "") {
		t.Fatal("viewer should read")
	}
	if s.Check("u-view", "process.restart", "") {
		t.Fatal("viewer must not restart")
	}
}

func TestFSM_CheckAgentScope(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u-op", Username: "op", PasswordHash: "h"}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "bind_put", control.BindPutBody{
		UserID:  "u-op",
		RoleID:  "operator",
		Scope:   control.ScopeAgent,
		ScopeID: "node-c",
	}), now); err != nil {
		t.Fatal(err)
	}
	if !s.Check("u-op", "process.restart", "node-c") {
		t.Fatal("agent scope node-c should allow restart")
	}
	if s.Check("u-op", "process.restart", "node-a") {
		t.Fatal("agent scope must not allow other node")
	}
	if s.Check("u-op", "process.restart", "") {
		t.Fatal("empty target must not match AGENT binding")
	}
	if s.Check("u-op", "user.create", "") {
		t.Fatal("cluster API with empty target must not match AGENT binding")
	}
}

func TestFSM_SnapshotRestore(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	f := control.NewFSM()
	mustFSMApply(t, f, "bootstrap", control.BootstrapBody{
		ClusterID:    "cid-snap",
		AdminUser:    "admin",
		PasswordHash: "h",
		AdminUserID:  "user-admin",
		NowUnix:      now.Unix(),
	}, now)
	mustFSMApply(t, f, "user_put", control.UserPutBody{ID: "u1", Username: "alice", PasswordHash: "ha"}, now)

	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	snap.Release()

	restored := control.NewFSM()
	if err := restored.Restore(io.NopCloser(bytes.NewReader(sink.buf.Bytes()))); err != nil {
		t.Fatal(err)
	}
	view := restored.View()
	if view.ClusterID != "cid-snap" {
		t.Fatalf("cluster=%q", view.ClusterID)
	}
	if _, ok := view.Users["alice"]; !ok {
		t.Fatal("alice missing after restore")
	}
	if _, ok := view.Roles["viewer"]; !ok {
		t.Fatal("roles missing after restore")
	}
}

func TestLoadAdminBootstrap(t *testing.T) {
	dir := t.TempDir()
	_, _, err := control.LoadAdminBootstrap(dir)
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("missing: %v", err)
	}
	now := time.Now()
	res, err := control.Init(dir, "n", "root", now)
	if err != nil {
		t.Fatal(err)
	}
	user, hash, err := control.LoadAdminBootstrap(dir)
	if err != nil {
		t.Fatal(err)
	}
	if user != "root" {
		t.Fatalf("user=%q", user)
	}
	if !control.VerifyPassword(hash, res.AdminPassword) {
		t.Fatal("hash mismatch")
	}
}

func TestCertSerial(t *testing.T) {
	now := time.Now()
	b, err := control.NewBundle("c", "n", now)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := control.CertSerial(b.AgentCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if serial == "" || serial != strings.ToUpper(serial) {
		t.Fatalf("serial=%q", serial)
	}
	for _, r := range serial {
		if (r < '0' || r > '9') && (r < 'A' || r > 'F') {
			t.Fatalf("not uppercase hex: %q", serial)
		}
	}
}

func TestFSM_UserPutInvalid(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u", Username: "", PasswordHash: "h"}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty user: %v", err)
	}
	err = s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u", Username: "x", PasswordHash: ""}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty hash: %v", err)
	}
}

func TestFSM_LoginOKClearsLock(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u1", Username: "bob", PasswordHash: "h"}), now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := s.Apply(mustEncode(t, "login_fail", control.LoginFailBody{Username: "bob"}), now); err != nil {
			t.Fatal(err)
		}
	}
	later := now.Add(16 * time.Minute)
	if err := s.Apply(mustEncode(t, "login_ok", control.LoginOKBody{Username: "bob"}), later); err != nil {
		t.Fatal(err)
	}
	u := s.Users["bob"]
	if u.Status != control.UserActive || u.FailCount != 0 || u.LastLoginUnix != later.Unix() {
		t.Fatalf("%+v", u)
	}
}

func TestFSM_JoinTokenErrorsMatchConsumeToken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(mustEncode(t, "join_token_consume", control.JoinTokenConsumeBody{Plain: "pmj_nope"}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("invalid: %v", err)
	}

	plain := "pmj_revoked"
	sum := sha256.Sum256([]byte(plain))
	if err := s.Apply(mustEncode(t, "join_token_put", control.JoinTokenPutBody{
		ID:         "jt-r",
		Hash:       hex.EncodeToString(sum[:]),
		TTLSeconds: int64(time.Hour.Seconds()),
		Remaining:  2,
	}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "join_token_revoke", control.JoinTokenRevokeBody{ID: "jt-r"}), now); err != nil {
		t.Fatal(err)
	}
	err = s.Apply(mustEncode(t, "join_token_consume", control.JoinTokenConsumeBody{Plain: plain}), now)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("revoked: %v", err)
	}

	expired := "pmj_expired"
	sum = sha256.Sum256([]byte(expired))
	if err := s.Apply(mustEncode(t, "join_token_put", control.JoinTokenPutBody{
		ID:         "jt-e",
		Hash:       hex.EncodeToString(sum[:]),
		TTLSeconds: 1, // 1 second
		Remaining:  1,
	}), now); err != nil {
		t.Fatal(err)
	}
	err = s.Apply(mustEncode(t, "join_token_consume", control.JoinTokenConsumeBody{Plain: expired}), now.Add(2*time.Second))
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("expired: %v", err)
	}
}

func TestFSM_CheckInactiveUserDenied(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u-d", Username: "dis", PasswordHash: "h"}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "bind_put", control.BindPutBody{
		UserID: "u-d", RoleID: "super_admin", Scope: control.ScopeCluster,
	}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "user_disable", control.UserDisableBody{UserID: "u-d"}), now); err != nil {
		t.Fatal(err)
	}
	if s.Check("u-d", "cluster.read", "") {
		t.Fatal("disabled user allowed")
	}
	if s.Check("missing", "cluster.read", "") {
		t.Fatal("missing user allowed")
	}
}

func TestFSM_RolePutAndJoinTokenLookup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "role_put", control.RolePutBody{
		ID:    "custom",
		Name:  "Custom",
		Perms: []string{"cluster.read"},
	}), now); err != nil {
		t.Fatal(err)
	}
	if s.Roles["custom"].Name != "Custom" {
		t.Fatalf("%+v", s.Roles["custom"])
	}
	plain := "pmj_lookup"
	sum := sha256.Sum256([]byte(plain))
	if err := s.Apply(mustEncode(t, "join_token_put", control.JoinTokenPutBody{
		ID:         "jt-l",
		Hash:       hex.EncodeToString(sum[:]),
		TTLSeconds: int64(time.Hour.Seconds()),
		Remaining:  3,
	}), now); err != nil {
		t.Fatal(err)
	}
	jt, ok := s.JoinTokenByPlain(plain)
	if !ok || jt.ID != "jt-l" {
		t.Fatalf("lookup=%+v ok=%v", jt, ok)
	}
	if _, ok := s.JoinTokenByPlain("pmj_missing"); ok {
		t.Fatal("missing token hit")
	}
	if _, ok := s.TokenByPlain("pmt_missing"); ok {
		t.Fatal("missing api token hit")
	}
}

func TestFSM_ApplyRejectsUnknownAndInvalid(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(control.Command{Type: "nope", Body: []byte(`{}`)}, now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("unknown: %v", err)
	}
	err = s.Apply(control.Command{Type: "user_put", Body: []byte(`{`)}, now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("bad body: %v", err)
	}
	err = s.Apply(mustEncode(t, "bootstrap", control.BootstrapBody{}), now)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("empty rebootstrap: %v", err)
	}
	fresh := control.NewState()
	err = fresh.Apply(mustEncode(t, "bootstrap", control.BootstrapBody{ClusterID: "c"}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("incomplete bootstrap: %v", err)
	}
	err = s.Apply(mustEncode(t, "session_put", control.SessionPutBody{}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty session: %v", err)
	}
	err = s.Apply(mustEncode(t, "token_put", control.TokenPutBody{ID: "t", UserID: "u"}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty token hash: %v", err)
	}
	err = s.Apply(mustEncode(t, "join_token_put", control.JoinTokenPutBody{ID: "j"}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty join hash: %v", err)
	}
	err = s.Apply(mustEncode(t, "role_put", control.RolePutBody{Name: "x"}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty role id: %v", err)
	}
	err = s.Apply(mustEncode(t, "bind_put", control.BindPutBody{UserID: "u"}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty bind: %v", err)
	}
	err = s.Apply(mustEncode(t, "member_put", control.MemberPutBody{}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty member: %v", err)
	}
	err = s.Apply(mustEncode(t, "crl_add", control.CRLAddBody{}), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty crl: %v", err)
	}
}

func TestFSM_NotFoundAndIdempotentBind(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if !errcode.Is(s.Apply(mustEncode(t, "user_disable", control.UserDisableBody{UserID: "missing"}), now), errcode.NOT_FOUND) {
		t.Fatal("disable")
	}
	if !errcode.Is(s.Apply(mustEncode(t, "login_ok", control.LoginOKBody{Username: "nope"}), now), errcode.NOT_FOUND) {
		t.Fatal("login_ok")
	}
	if !errcode.Is(s.Apply(mustEncode(t, "login_fail", control.LoginFailBody{Username: "nope"}), now), errcode.NOT_FOUND) {
		t.Fatal("login_fail")
	}
	if !errcode.Is(s.Apply(mustEncode(t, "token_revoke", control.TokenRevokeBody{ID: "missing"}), now), errcode.NOT_FOUND) {
		t.Fatal("token_revoke")
	}
	if !errcode.Is(s.Apply(mustEncode(t, "join_token_revoke", control.JoinTokenRevokeBody{ID: "missing"}), now), errcode.NOT_FOUND) {
		t.Fatal("join_token_revoke")
	}
	if !errcode.Is(s.Apply(mustEncode(t, "member_remove", control.MemberRemoveBody{NodeID: "missing"}), now), errcode.NOT_FOUND) {
		t.Fatal("member_remove")
	}
	bind := control.BindPutBody{UserID: "user-admin", RoleID: "viewer", Scope: control.ScopeCluster}
	if err := s.Apply(mustEncode(t, "bind_put", bind), now); err != nil {
		t.Fatal(err)
	}
	n := len(s.Bindings)
	if err := s.Apply(mustEncode(t, "bind_put", bind), now); err != nil {
		t.Fatal(err)
	}
	if len(s.Bindings) != n {
		t.Fatalf("duplicate bind appended: %d -> %d", n, len(s.Bindings))
	}
	if err := s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "n2"}), now); err != nil {
		t.Fatal(err)
	}
	if s.Members["n2"].Status != control.MemberAdmitted {
		t.Fatalf("%+v", s.Members["n2"])
	}
	if err := s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "user-admin", Username: "other", PasswordHash: "h"}), now); err == nil || !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("id clash: %v", err)
	}
}

func TestFSM_RaftApplyAndSnapshotErrors(t *testing.T) {
	f := control.NewFSM()
	ret := f.Apply(&raft.Log{Data: []byte("not-json")})
	if err, ok := ret.(error); !ok || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("bad log: %v", ret)
	}
	now := time.Unix(1_700_000_000, 0)
	cmd := mustEncode(t, "bootstrap", control.BootstrapBody{
		ClusterID: "c", AdminUser: "admin", PasswordHash: "h", AdminUserID: "u", NowUnix: now.Unix(),
	})
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if ret := f.Apply(&raft.Log{Data: data}); ret != nil {
		t.Fatal(ret)
	}
	if ret := f.Apply(&raft.Log{Data: data}); ret == nil {
		t.Fatal("expected bootstrap conflict")
	} else if err, ok := ret.(error); !ok || !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("got %v", ret)
	}

	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.Persist(&failSink{}); err == nil {
		t.Fatal("expected persist error")
	}
	snap.Release()

	if err := f.Restore(io.NopCloser(strings.NewReader("not-json"))); err == nil {
		t.Fatal("expected restore error")
	}
	if err := f.Restore(errCloser{}); err == nil {
		t.Fatal("expected restore read error")
	}
}

func TestEncodeCommand_RejectsBadBody(t *testing.T) {
	_, err := control.EncodeCommand("user_put", make(chan int))
	if err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestFSM_AgentGroupCRUD(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{
		GroupID: "g-fin", Name: "finance", Description: "fin", NowUnix: now.Unix(),
	}), now); err != nil {
		t.Fatal(err)
	}
	g, ok := s.AgentGroups["g-fin"]
	if !ok || g.Name != "finance" {
		t.Fatalf("group %+v ok=%v", g, ok)
	}
	if err := s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if !s.NodeInGroup("node-a", "g-fin") {
		t.Fatal("expected member")
	}
	if err := s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "missing"}), now); err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("missing node: %v", err)
	}
	if err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g2", Name: "finance", NowUnix: now.Unix()}), now); err == nil || !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("dup name: %v", err)
	}
	if err := s.Apply(mustEncode(t, "group_delete", control.GroupDeleteBody{GroupID: "g-fin"}), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AgentGroups["g-fin"]; ok {
		t.Fatal("group should be gone")
	}
}

func TestFSM_GroupNameValidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g1", Name: "bad name", NowUnix: now.Unix()}), now)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestFSM_AgentGroupErrorsAndMemberRemove(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-rev", Status: control.MemberRevoked}), now); err != nil {
		t.Fatal(err)
	}

	err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "", Name: "ok", NowUnix: now.Unix()}), now)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty group id: %v", err)
	}
	err = s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g1", Name: "   ", NowUnix: now.Unix()}), now)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("blank name: %v", err)
	}
	err = s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g1", Name: strings.Repeat("a", 65), NowUnix: now.Unix()}), now)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("long name: %v", err)
	}
	err = s.Apply(mustEncode(t, "group_put", control.GroupPutBody{
		GroupID: "g1", Name: "ok", Description: strings.Repeat("d", 257), NowUnix: now.Unix(),
	}), now)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("long description: %v", err)
	}

	if err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{
		GroupID: "g-fin", Name: " finance ", Description: "fin", NowUnix: now.Unix(),
	}), now); err != nil {
		t.Fatal(err)
	}
	g := s.AgentGroups["g-fin"]
	if g.Name != "finance" || g.CreatedUnix != now.Unix() || g.UpdatedUnix != now.Unix() {
		t.Fatalf("create %+v", g)
	}
	later := now.Add(time.Minute)
	if err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{
		GroupID: "g-fin", Name: "finance", Description: "updated", NowUnix: later.Unix(),
	}), now); err != nil {
		t.Fatal(err)
	}
	g = s.AgentGroups["g-fin"]
	if g.Description != "updated" || g.CreatedUnix != now.Unix() || g.UpdatedUnix != later.Unix() {
		t.Fatalf("update %+v", g)
	}

	if !errcode.Is(s.Apply(mustEncode(t, "group_delete", control.GroupDeleteBody{GroupID: "missing"}), now), errcode.NOT_FOUND) {
		t.Fatal("delete missing")
	}
	if !errcode.Is(s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "missing", NodeID: "node-a"}), now), errcode.NOT_FOUND) {
		t.Fatal("add missing group")
	}
	if !errcode.Is(s.Apply(mustEncode(t, "group_member_remove", control.GroupMemberBody{GroupID: "missing", NodeID: "node-a"}), now), errcode.NOT_FOUND) {
		t.Fatal("remove missing group")
	}
	if err := s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-rev"}), now); err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("revoked member: %v", err)
	}

	if err := s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	n := len(s.AgentGroups["g-fin"].MemberIDs)
	if err := s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if len(s.AgentGroups["g-fin"].MemberIDs) != n {
		t.Fatal("add member not idempotent")
	}
	if s.NodeInGroup("node-a", "missing") || s.NodeInGroup("nope", "g-fin") {
		t.Fatal("NodeInGroup false positives")
	}
	if err := s.Apply(mustEncode(t, "group_member_remove", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if s.NodeInGroup("node-a", "g-fin") {
		t.Fatal("member still present")
	}

	if err := s.Apply(mustEncode(t, "bind_put", control.BindPutBody{
		UserID: "user-admin", RoleID: "operator", Scope: control.ScopeAgentGroup, ScopeID: "g-fin",
	}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "group_delete", control.GroupDeleteBody{GroupID: "g-fin"}), now); err == nil || !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("delete with binding: %v", err)
	}
}

type failSink struct{}

func (failSink) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (failSink) Close() error              { return nil }
func (failSink) ID() string                { return "x" }
func (failSink) Cancel() error             { return nil }

type errCloser struct{}

func (errCloser) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (errCloser) Close() error             { return nil }

func TestFSM_CRLAddAndSessionDel(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "crl_add", control.CRLAddBody{Serial: "aa"}), now); err != nil {
		t.Fatal(err)
	}
	if !s.SerialRevoked("AA") {
		t.Fatal("crl not normalized")
	}
	if err := s.Apply(mustEncode(t, "session_put", control.SessionPutBody{ID: "s1", UserID: "user-admin", CSRF: "c"}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "session_del", control.SessionDelBody{ID: "s1"}), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SessionByID("s1"); ok {
		t.Fatal("session still present")
	}
	if err := s.Apply(mustEncode(t, "token_put", control.TokenPutBody{ID: "t1", UserID: "user-admin", Name: "n", Hash: "abc"}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "token_revoke", control.TokenRevokeBody{ID: "t1"}), now); err != nil {
		t.Fatal(err)
	}
	if !s.APITokens["t1"].Revoked {
		t.Fatal("token not revoked")
	}
}

func mustBootstrap(t *testing.T, now time.Time) *control.State {
	t.Helper()
	s := control.NewState()
	cmd := mustEncode(t, "bootstrap", control.BootstrapBody{
		ClusterID:    "cid-1",
		AdminUser:    "admin",
		PasswordHash: "hashed-admin",
		AdminUserID:  "user-admin",
		NowUnix:      now.Unix(),
	})
	if err := s.Apply(cmd, now); err != nil {
		t.Fatal(err)
	}
	return s
}

func mustEncode(t *testing.T, typ string, body any) control.Command {
	t.Helper()
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func mustFSMApply(t *testing.T, f *control.FSM, typ string, body any, now time.Time) {
	t.Helper()
	cmd := mustEncode(t, typ, body)
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	ret := f.Apply(&raft.Log{Data: data, AppendedAt: now})
	if ret == nil {
		return
	}
	if err, ok := ret.(error); ok {
		t.Fatal(err)
	}
	t.Fatalf("apply: %v", ret)
}

type memSink struct {
	buf bytes.Buffer
}

func (m *memSink) Write(p []byte) (int, error) { return m.buf.Write(p) }
func (m *memSink) Close() error                { return nil }
func (m *memSink) ID() string                  { return "snap" }
func (m *memSink) Cancel() error               { return nil }

var allPerms = []string{
	"cluster.read", "cluster.manage",
	"node.read", "node.manage", "node.remove",
	"process.read", "process.create", "process.update", "process.delete",
	"process.start", "process.stop", "process.restart",
	"process.config.read", "process.config.update",
	"process.logs.read", "process.logs.download",
	"user.read", "user.create", "user.update", "user.delete",
	"role.read", "role.manage",
	"audit.read",
	"command.execute", "command.execute.batch",
}

var clusterAdminPerms = []string{
	"cluster.read", "cluster.manage",
	"node.read", "node.manage", "node.remove",
	"process.read", "process.create", "process.update", "process.delete",
	"process.start", "process.stop", "process.restart",
	"process.config.read", "process.config.update",
	"process.logs.read", "process.logs.download",
	"user.read", "user.create", "user.update",
	"role.read",
	"audit.read",
}

var operatorPerms = []string{
	"cluster.read", "node.read", "process.read",
	"process.start", "process.stop", "process.restart",
	"process.config.read", "process.logs.read",
}

var viewerPerms = []string{
	"cluster.read", "node.read", "process.read",
	"process.config.read", "process.logs.read",
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	m := make(map[string]int, len(want))
	for _, s := range want {
		m[s]++
	}
	for _, s := range got {
		m[s]--
		if m[s] < 0 {
			return false
		}
	}
	return true
}
