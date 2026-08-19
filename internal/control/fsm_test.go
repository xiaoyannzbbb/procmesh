package control_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func TestFSM_FireLedgerIdempotencyAndLeaseTakeover(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	first, created, err := s.ClaimFire(control.FireClaimBody{OperationID: "op-fire-1", FireKey: "bp-1:1700000000", PolicyID: "bp-1", LeaderTerm: 3}, now)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	second, created, err := s.ClaimFire(control.FireClaimBody{OperationID: "op-fire-2", FireKey: "bp-1:1700000000", PolicyID: "bp-1", LeaderTerm: 4}, now.Add(time.Second))
	if err != nil || created || second.RunID != first.RunID {
		t.Fatalf("%+v created=%v err=%v", second, created, err)
	}
	other, created, err := s.ClaimFire(control.FireClaimBody{OperationID: "op-fire-3", FireKey: "bp-2:1700000000", PolicyID: "bp-2", LeaderTerm: 3}, now)
	if err != nil || !created || other.RunID == first.RunID {
		t.Fatalf("%+v created=%v err=%v", other, created, err)
	}

	first.Status = "RUNNING"
	first.LeaseUntilUnix = now.Add(-time.Second).Unix()
	s.BackupFireLedger[first.FireKey] = first
	taken, created, err := s.ClaimFire(control.FireClaimBody{OperationID: "op-fire-4", FireKey: first.FireKey, PolicyID: first.PolicyID, LeaderTerm: 4}, now)
	if err != nil || !created || taken.RunID != first.RunID || taken.LeaderTerm != 4 {
		t.Fatalf("%+v created=%v err=%v", taken, created, err)
	}
}

func TestFSM_BackupRunRejectsStaleTermAndPreservesTerminalTask(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	s.BackupPolicies["bp-1"] = control.BackupPolicy{PolicyID: "bp-1", Revision: 2, TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-a"}}
	run := control.ClusterBackupRun{RunID: "run-1", PolicyID: "bp-1", PolicyRevision: 2, TargetNodeIDs: []string{"node-a"}, Status: "RUNNING", CreatedUnix: now.Unix(), StartedUnix: now.Unix()}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-run-1", LeaderTerm: 4, Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-task-stale-first", LeaderTerm: 3, Task: control.ClusterBackupTask{RunID: "run-1", TaskID: "task-stale", NodeID: "node-a", Status: "FAILED"}}); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("stale first task update: %v", err)
	}
	success := control.ClusterBackupTask{RunID: "run-1", TaskID: "task-a", NodeID: "node-a", SnapshotID: "snap-1", SHA256: "sha-good", Status: "SUCCESS", Bytes: 42, LeaderTerm: 4, UpdatedUnix: now.Unix()}
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-task-1", LeaderTerm: 4, Task: success}); err != nil {
		t.Fatal(err)
	}
	stale := success
	stale.Status, stale.SHA256, stale.LeaderTerm = "FAILED", "sha-bad", 3
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-task-2", LeaderTerm: 3, Task: stale}); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("stale update: %v", err)
	}
	repeated := success
	repeated.SHA256 = "sha-overwrite"
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-task-3", LeaderTerm: 4, Task: repeated}); err != nil {
		t.Fatal(err)
	}
	if got := s.BackupTasks["run-1:task-a"]; got.Status != "SUCCESS" || got.SHA256 != "sha-good" {
		t.Fatalf("terminal task changed: %+v", got)
	}
	if err := s.FinishRun(control.FinishRunBody{OperationID: "op-run-2", RunID: "run-1", LeaderTerm: 3, Status: "FAILED", FinishedUnix: now.Add(time.Minute).Unix()}); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("stale finish: %v", err)
	}
}

func TestFSM_RunMetadataPruningAndSnapshotSafety(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	s.BackupPolicies["bp-old"] = control.BackupPolicy{PolicyID: "bp-old", Revision: 1}
	s.ReplicationPolicies["rp-new"] = control.ReplicationPolicy{PolicyID: "rp-new", Revision: 1}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-backup", LeaderTerm: 1, Run: control.ClusterBackupRun{RunID: "backup-old", PolicyID: "bp-old", PolicyRevision: 1, Status: "SUCCESS", FinishedUnix: now.Add(-time.Hour).Unix()}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-replication", Replication: true, LeaderTerm: 1, Run: control.ClusterBackupRun{RunID: "replication-new", PolicyID: "rp-new", PolicyRevision: 1, Status: "RUNNING", CreatedUnix: now.Unix()}}); err != nil {
		t.Fatal(err)
	}
	s.PruneRunMetadata(now.Add(-time.Minute).Unix())
	if _, ok := s.BackupRuns["backup-old"]; ok {
		t.Fatal("old backup run retained")
	}
	if _, ok := s.ReplicationRuns["replication-new"]; !ok {
		t.Fatal("replication run missing")
	}
	f := control.NewFSM()
	view := f.View()
	view.BackupRuns = s.BackupRuns
	view.BackupTasks = s.BackupTasks
	view.ReplicationRuns = s.ReplicationRuns
	view.ReplicationTasks = s.ReplicationTasks
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"payload", "secret_key", "access_key", "backup_index"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("snapshot metadata contains %q: %s", forbidden, raw)
		}
	}
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	snap.Release()
	for _, forbidden := range []string{"payload", "secret_key", "access_key", "backup_index"} {
		if bytes.Contains(sink.buf.Bytes(), []byte(forbidden)) {
			t.Fatalf("raft snapshot contains %q: %s", forbidden, sink.buf.Bytes())
		}
	}
}

func TestFSM_MetadataUsesSpecTerminalStatuses(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	s.BackupPolicies["bp-1"] = control.BackupPolicy{PolicyID: "bp-1", Revision: 2, TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-a"}}
	run := control.ClusterBackupRun{RunID: "run-status", PolicyID: "bp-1", PolicyRevision: 2, TargetNodeIDs: []string{"node-a"}, Status: "RUNNING", CreatedUnix: now.Unix()}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-status-run", LeaderTerm: 1, Run: run}); err != nil {
		t.Fatal(err)
	}
	task := control.ClusterBackupTask{RunID: run.RunID, TaskID: "task-status", NodeID: "node-a", Status: "SUCCEEDED", SHA256: "abc", UpdatedUnix: now.Unix()}
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-status-task", LeaderTerm: 1, Task: task}); err != nil {
		t.Fatal(err)
	}
	task.Status, task.SHA256 = "FAILED", "changed"
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-status-task-2", LeaderTerm: 1, Task: task}); err != nil {
		t.Fatal(err)
	}
	if got := s.BackupTasks["run-status:task-status"]; got.Status != "SUCCEEDED" || got.SHA256 != "abc" {
		t.Fatalf("terminal task changed: %+v", got)
	}
	if err := s.FinishRun(control.FinishRunBody{OperationID: "op-status-finish", RunID: run.RunID, LeaderTerm: 1, Status: "PARTIAL", FinishedUnix: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if got := s.BackupRuns[run.RunID]; got.Status != "PARTIAL" {
		t.Fatalf("run status=%q", got.Status)
	}
}

func TestFSM_MetadataBoundsRejectOversizedInputs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	s.BackupPolicies["bp-1"] = control.BackupPolicy{PolicyID: "bp-1", Revision: 1, TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-a"}}
	base := control.ClusterBackupTask{RunID: "run-bounds", TaskID: "task-1", NodeID: "node-a", Status: "RUNNING", LeaderTerm: 1}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-bounds-run", LeaderTerm: 1, Run: control.ClusterBackupRun{RunID: "run-bounds", PolicyID: "bp-1", PolicyRevision: 1, TargetNodeIDs: []string{"node-a"}, Status: "RUNNING"}}); err != nil {
		t.Fatal(err)
	}
	cases := []control.ClusterBackupTask{
		func() control.ClusterBackupTask { x := base; x.ErrorSummary = strings.Repeat("x", 2049); return x }(),
		func() control.ClusterBackupTask { x := base; x.ErrorCode = strings.Repeat("x", 257); return x }(),
		func() control.ClusterBackupTask { x := base; x.SnapshotID = strings.Repeat("x", 513); return x }(),
		func() control.ClusterBackupTask { x := base; x.SHA256 = strings.Repeat("x", 129); return x }(),
		func() control.ClusterBackupTask { x := base; x.TaskID = strings.Repeat("x", 257); return x }(),
	}
	for i, task := range cases {
		if err := s.UpdateTask(control.UpdateTaskBody{OperationID: fmt.Sprintf("op-bounds-%d", i), LeaderTerm: 1, Task: task}); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	tooMany := make([]string, 0, 1025)
	for i := 0; i < 1025; i++ {
		tooMany = append(tooMany, fmt.Sprintf("node-%d", i))
	}
	run := control.ClusterBackupRun{RunID: "run-too-many", PolicyID: "bp-1", PolicyRevision: 1, TargetNodeIDs: tooMany, Status: "RUNNING", CreatedUnix: now.Unix()}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-too-many", LeaderTerm: 1, Run: run}); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("target count: %v", err)
	}
}

func TestFSM_PruneRunMetadataRetainsLiveFireLease(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	live, _, err := s.ClaimFire(control.FireClaimBody{OperationID: "op-live", FireKey: "bp-1:old-live", PolicyID: "bp-1", LeaderTerm: 1, ScheduledUnix: now.Add(-time.Hour).Unix(), LeaseUntilUnix: now.Add(time.Hour).Unix()}, now)
	if err != nil {
		t.Fatal(err)
	}
	old := control.FireRecord{FireKey: "bp-1:old-expired", PolicyID: "bp-1", RunID: "run-old", ScheduledUnix: now.Add(-time.Hour).Unix(), LeaseUntilUnix: now.Add(-time.Minute).Unix(), LeaderTerm: 1, Status: "CLAIMED"}
	s.BackupFireLedger[old.FireKey] = old
	s.PruneRunMetadata(now.Add(-time.Minute).Unix())
	if _, ok := s.BackupFireLedger[live.FireKey]; !ok {
		t.Fatal("live lease pruned")
	}
	if _, ok := s.BackupFireLedger[old.FireKey]; ok {
		t.Fatal("expired lease retained")
	}
}

func TestFSM_CreateRunValidatesAndFreezesPolicy(t *testing.T) {
	s := control.NewState()
	s.BackupPolicies["bp-1"] = control.BackupPolicy{PolicyID: "bp-1", Revision: 4, TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-a", "node-b"}}
	valid := control.ClusterBackupRun{RunID: "run-freeze", PolicyID: "bp-1", PolicyRevision: 4, TargetNodeIDs: []string{"node-a", "node-b"}, Status: "RUNNING"}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-freeze", LeaderTerm: 1, Run: valid}); err != nil {
		t.Fatal(err)
	}
	valid.TargetNodeIDs[0] = "changed"
	if got := s.BackupRuns[valid.RunID]; got.TargetNodeIDs[0] != "node-a" {
		t.Fatalf("targets not frozen: %+v", got)
	}
	badRevision := valid
	badRevision.RunID = "run-bad-rev"
	badRevision.PolicyRevision = 3
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-bad-rev", LeaderTerm: 1, Run: badRevision}); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("revision: %v", err)
	}
	badTargets := valid
	badTargets.RunID = "run-bad-target"
	badTargets.TargetNodeIDs = []string{"node-a"}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-bad-target", LeaderTerm: 1, Run: badTargets}); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("targets: %v", err)
	}
}

func TestFSM_CreateRunValidatesSelectorMembership(t *testing.T) {
	s := control.NewState()
	s.Members["a"] = control.Member{NodeID: "a", Status: control.MemberAdmitted}
	s.Members["b"] = control.Member{NodeID: "b", Status: control.MemberAdmitted}
	s.Members["gone"] = control.Member{NodeID: "gone", Status: control.MemberRevoked}
	s.AgentGroups["g1"] = control.AgentGroup{GroupID: "g1", MemberIDs: []string{"a"}}
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name    string
		policy  control.BackupPolicy
		targets []string
		want    errcode.Code
	}{
		{"all admitted missing", control.BackupPolicy{PolicyID: "all", Revision: 1, TargetSelector: "ALL_ADMITTED"}, []string{"a"}, errcode.CONFLICT},
		{"all admitted revoked", control.BackupPolicy{PolicyID: "all2", Revision: 1, TargetSelector: "ALL_ADMITTED"}, []string{"a", "gone"}, errcode.CONFLICT},
		{"group outsider", control.BackupPolicy{PolicyID: "group", Revision: 1, TargetSelector: "AGENT_GROUP", TargetIDs: []string{"g1"}}, []string{"b"}, errcode.INVALID},
	}
	for _, tc := range cases {
		s.BackupPolicies[tc.policy.PolicyID] = tc.policy
		err := s.CreateRun(control.CreateRunBody{OperationID: "op-" + tc.name, LeaderTerm: 1, Run: control.ClusterBackupRun{RunID: "run-" + tc.name, PolicyID: tc.policy.PolicyID, PolicyRevision: 1, TargetNodeIDs: tc.targets, Status: "RUNNING", CreatedUnix: now.Unix()}})
		if !errcode.Is(err, tc.want) {
			t.Fatalf("%s: err=%v want %s", tc.name, err, tc.want)
		}
	}
}

func TestFSM_RetryFailedTasksResetsOnlyRetryableTasks(t *testing.T) {
	s := control.NewState()
	s.Members["a"] = control.Member{NodeID: "a", Status: control.MemberAdmitted}
	s.BackupPolicies["bp"] = control.BackupPolicy{PolicyID: "bp", Revision: 1, TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"a"}}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-run-retry", LeaderTerm: 2, Run: control.ClusterBackupRun{RunID: "run-retry", PolicyID: "bp", PolicyRevision: 1, TargetNodeIDs: []string{"a"}, Status: "PARTIAL"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-task-retry", LeaderTerm: 2, Task: control.ClusterBackupTask{RunID: "run-retry", TaskID: "task-a", NodeID: "a", Status: "FAILED", ErrorCode: "E_IO", ErrorSummary: "disk"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, control.CmdBackupRetryFailedTasks, control.RetryFailedTasksBody{OperationID: "op-retry", RunID: "run-retry", LeaderTerm: 3, UpdatedUnix: 1_700_000_100}), time.Unix(1_700_000_100, 0)); err != nil {
		t.Fatal(err)
	}
	got := s.BackupTasks["run-retry:task-a"]
	if got.Status != "PENDING" || got.ErrorCode != "" || got.ErrorSummary != "" || got.LeaderTerm != 3 || got.UpdatedUnix != 1_700_000_100 {
		t.Fatalf("task=%+v", got)
	}
}

func TestFSM_MetadataFencesNewerTermsAndRejectsInvalidLeases(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	s.BackupPolicies["bp-term"] = control.BackupPolicy{PolicyID: "bp-term", Revision: 1}
	run := control.ClusterBackupRun{RunID: "run-term", PolicyID: "bp-term", PolicyRevision: 1, Status: "RUNNING"}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-term-run", LeaderTerm: 1, Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-term-task-new", LeaderTerm: 4, Task: control.ClusterBackupTask{RunID: run.RunID, TaskID: "task", NodeID: "node", Status: "RUNNING"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(control.FinishRunBody{OperationID: "op-term-finish-old", RunID: run.RunID, LeaderTerm: 2, Status: "FAILED"}); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("stale finish after newer task: %v", err)
	}
	if _, _, err := s.ClaimFire(control.FireClaimBody{OperationID: "op-bad-lease", FireKey: "bp-term:1", PolicyID: "bp-term", LeaderTerm: 1, LeaseUntilUnix: now.Unix() - 1}, now); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("invalid lease: %v", err)
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

func TestFSM_CheckAgentGroupAndProcessGroup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	_ = s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-fin"}), now)
	_ = s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-ads"}), now)
	_ = s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g-fin", Name: "finance", NowUnix: now.Unix()}), now)
	_ = s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-fin"}), now)
	_ = s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u-fin", Username: "finop", PasswordHash: "h"}), now)
	_ = s.Apply(mustEncode(t, "bind_put", control.BindPutBody{
		UserID: "u-fin", RoleID: "operator", Scope: control.ScopeAgentGroup, ScopeID: "g-fin",
	}), now)
	_ = s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u-pg", Username: "pgop", PasswordHash: "h"}), now)
	_ = s.Apply(mustEncode(t, "bind_put", control.BindPutBody{
		UserID: "u-pg", RoleID: "operator", Scope: control.ScopeProcessGroup, ScopeID: "finance",
	}), now)

	if !s.CheckTarget("u-fin", "process.restart", control.CheckTarget{NodeID: "node-fin"}) {
		t.Fatal("agent group should allow finance node")
	}
	if s.CheckTarget("u-fin", "process.restart", control.CheckTarget{NodeID: "node-ads"}) {
		t.Fatal("agent group must not allow ads node")
	}
	if !s.CheckTarget("u-pg", "process.restart", control.CheckTarget{NodeID: "node-ads", ProcessGroup: "finance"}) {
		t.Fatal("process group matches spec.group regardless of node")
	}
	if s.CheckTarget("u-pg", "process.restart", control.CheckTarget{NodeID: "node-ads", ProcessGroup: "ads"}) {
		t.Fatal("process group must not match other name")
	}
	if s.CheckTarget("u-pg", "process.restart", control.CheckTarget{NodeID: "node-ads"}) {
		t.Fatal("empty process group must not match PROCESS_GROUP")
	}
	if !s.CanAny("u-fin", "process.read") {
		t.Fatal("CanAny")
	}
}

func TestFSM_AlertChannelAndPolicy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if s.AlertPolicy.DedupWindowSec != 600 || !s.AlertPolicy.NotifyOnResolve {
		t.Fatalf("defaults %+v", s.AlertPolicy)
	}
	if err := s.Apply(mustEncode(t, "alert_channel_put", control.AlertChannelPutBody{
		ChannelID: "c1", Type: "WEBHOOK", Name: "hook", Enabled: true,
		ConfigJSON: `{"url":"https://example","hmac_secret":"s"}`, NowUnix: now.Unix(),
	}), now); err != nil {
		t.Fatal(err)
	}
	ch := s.AlertChannels["c1"]
	if ch.Name != "hook" || !strings.Contains(ch.ConfigJSON, "hmac_secret") {
		t.Fatalf("channel %+v", ch)
	}
	if err := s.Apply(mustEncode(t, "alert_policy_put", control.AlertPolicyPutBody{
		DedupWindowSec: 120, NotifyOnResolve: false, CPUHighPercent: 80,
		MemoryHighPercent: 80, DiskHighPercent: 85, HighConsecutiveMins: 3, SuspectTooLongSec: 60,
	}), now); err != nil {
		t.Fatal(err)
	}
	if s.AlertPolicy.CPUHighPercent != 80 || s.AlertPolicy.NotifyOnResolve {
		t.Fatalf("policy %+v", s.AlertPolicy)
	}
	if err := s.Apply(mustEncode(t, "alert_channel_delete", control.AlertChannelDeleteBody{ChannelID: "c1"}), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AlertChannels["c1"]; ok {
		t.Fatal("channel should be gone")
	}
}

func TestFSM_AlertChannelValidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(mustEncode(t, "alert_channel_put", control.AlertChannelPutBody{
		ChannelID: "c1", Type: "SMS", Name: "x", NowUnix: now.Unix(),
	}), now)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestFSM_EnsureSyncsBuiltinAlertPerms(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	r := s.Roles["operator"]
	r.Perms = []string{"cluster.read"} // 模拟升级前旧种子
	s.Roles["operator"] = r
	s.EnsureForTest()
	if !hasPerm(s.Roles["operator"].Perms, "batch.execute") {
		t.Fatal("operator should gain batch.execute")
	}
	if !hasPerm(s.Roles["viewer"].Perms, "alert.read") {
		t.Fatal("viewer should gain alert.read")
	}
	if hasPerm(s.Roles["operator"].Perms, "alert.manage") {
		t.Fatal("operator must not gain alert.manage")
	}
}

func TestFSM_BackupPolicyPutStoresFSAndS3Policies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)

	fs := control.BackupPolicyPutBody{
		PolicyID: "bp-fs", Name: "nightly", Enabled: true,
		ScheduleCron: "0 2 * * *", Timezone: "Asia/Shanghai",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", RetentionKeepLast: 7,
	}
	if err := s.Apply(mustEncode(t, control.CmdBackupPolicyPut, fs), now); err != nil {
		t.Fatal(err)
	}
	if got := s.BackupPolicies["bp-fs"]; got.Name != "nightly" || got.Sink != "fs" || got.RetentionKeepLast != 7 {
		t.Fatalf("fs policy=%+v", got)
	}
	if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: "node-a", Status: control.MemberAdmitted}), now); err != nil {
		t.Fatal(err)
	}

	s3 := control.BackupPolicyPutBody{
		PolicyID: "bp-s3", Name: "offsite", Enabled: true,
		ScheduleCron: "15 3 * * *", Timezone: "UTC",
		TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-a"},
		Sink: "s3", DestinationProfile: "archive", RetentionKeepLast: 14,
	}
	if err := s.Apply(mustEncode(t, control.CmdBackupPolicyPut, s3), now); err != nil {
		t.Fatal(err)
	}
	if got := s.BackupPolicies["bp-s3"]; got.DestinationProfile != "archive" || got.TargetIDs[0] != "node-a" {
		t.Fatalf("s3 policy=%+v", got)
	}
}

func TestFSM_BackupPolicyPutRejectsInvalidFields(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	valid := control.BackupPolicyPutBody{
		PolicyID: "bp-1", Name: "nightly", ScheduleCron: "0 2 * * *", Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", RetentionKeepLast: 1,
	}
	cases := []struct {
		name string
		body control.BackupPolicyPutBody
	}{
		{name: "empty name", body: func() control.BackupPolicyPutBody { b := valid; b.Name = ""; return b }()},
		{name: "invalid cron", body: func() control.BackupPolicyPutBody { b := valid; b.ScheduleCron = "0 * *"; return b }()},
		{name: "invalid timezone", body: func() control.BackupPolicyPutBody { b := valid; b.Timezone = "Moon/Base"; return b }()},
		{name: "unknown selector", body: func() control.BackupPolicyPutBody { b := valid; b.TargetSelector = "NEARBY"; return b }()},
		{name: "unknown explicit node", body: func() control.BackupPolicyPutBody {
			b := valid
			b.TargetSelector = "EXPLICIT_NODES"
			b.TargetIDs = []string{"missing"}
			return b
		}()},
		{name: "s3 missing profile", body: func() control.BackupPolicyPutBody { b := valid; b.Sink = "s3"; return b }()},
		{name: "invalid unavailable policy", body: func() control.BackupPolicyPutBody { b := valid; b.UnavailablePolicy = "IGNORE"; return b }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Apply(mustEncode(t, control.CmdBackupPolicyPut, tc.body), now); !errcode.Is(err, errcode.INVALID) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestFSM_BackupPolicyPutDefaultsAndValidatesUnavailablePolicy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	body := control.BackupPolicyPutBody{
		PolicyID: "bp-default", Name: "default-unavailable", ScheduleCron: "0 2 * * *", Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs",
	}
	if err := s.Apply(mustEncode(t, control.CmdBackupPolicyPut, body), now); err != nil {
		t.Fatal(err)
	}
	if got := s.BackupPolicies[body.PolicyID].UnavailablePolicy; got != "RECORD_AND_CONTINUE" {
		t.Fatalf("unavailable policy=%q", got)
	}
}

func TestFSM_PolicyPutRejectsInvalidAgentGroupSelectors(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, control.CmdGroupPut, control.GroupPutBody{GroupID: "g-1", Name: "ops", NowUnix: now.Unix()}), now); err != nil {
		t.Fatal(err)
	}
	backupCases := []control.BackupPolicyPutBody{
		{PolicyID: "bp-group-blank", Name: "group-blank", ScheduleCron: "0 2 * * *", Timezone: "UTC", TargetSelector: "AGENT_GROUP", TargetIDs: []string{" "}, Sink: "fs"},
		{PolicyID: "bp-group-duplicate", Name: "group-duplicate", ScheduleCron: "0 2 * * *", Timezone: "UTC", TargetSelector: "AGENT_GROUP", TargetIDs: []string{"g-1", "g-1"}, Sink: "fs"},
		{PolicyID: "bp-group-missing", Name: "group-missing", ScheduleCron: "0 2 * * *", Timezone: "UTC", TargetSelector: "AGENT_GROUP", TargetIDs: []string{"missing"}, Sink: "fs"},
	}
	for _, body := range backupCases {
		if err := s.Apply(mustEncode(t, control.CmdBackupPolicyPut, body), now); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("backup %s: %v", body.PolicyID, err)
		}
	}
	replicationCases := []control.ReplicationPolicyPutBody{
		{PolicyID: "rp-group-blank", Name: "group-blank", SourceSelector: "AGENT_GROUP", SourceIDs: []string{" "}, ReplicaFactor: 1, Trigger: "MANUAL"},
		{PolicyID: "rp-group-duplicate", Name: "group-duplicate", SourceSelector: "AGENT_GROUP", SourceIDs: []string{"g-1", "g-1"}, ReplicaFactor: 1, Trigger: "MANUAL"},
		{PolicyID: "rp-group-missing", Name: "group-missing", SourceSelector: "AGENT_GROUP", SourceIDs: []string{"missing"}, ReplicaFactor: 1, Trigger: "MANUAL"},
	}
	for _, body := range replicationCases {
		if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("replication %s: %v", body.PolicyID, err)
		}
	}
}

func TestFSM_BackupPolicyDeleteRejectsMissingPolicy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(mustEncode(t, control.CmdBackupPolicyDelete, control.BackupPolicyDeleteBody{PolicyID: "missing"}), now)
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("got %v", err)
	}
}

func TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	for _, nodeID := range []string{"node-a", "node-b", "node-c"} {
		if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: "node-revoked", Status: control.MemberRevoked}), now); err != nil {
		t.Fatal(err)
	}
	valid := control.ReplicationPolicyPutBody{
		PolicyID: "rp-1", Name: "dr", Enabled: true,
		SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL",
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}},
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, valid), now); err != nil {
		t.Fatal(err)
	}
	if got := s.ReplicationPolicies["rp-1"].Routes[0].TargetNodeIDs; len(got) != 1 || got[0] != "node-b" {
		t.Fatalf("routes=%v", got)
	}
	if err := s.Apply(mustEncode(t, control.CmdBackupPolicyPut, control.BackupPolicyPutBody{
		PolicyID: "bp-primary", Name: "primary", ScheduleCron: "0 2 * * *", Timezone: "UTC", TargetSelector: "ALL_ADMITTED", Sink: "fs",
	}), now); err != nil {
		t.Fatal(err)
	}
	afterPrimary := valid
	afterPrimary.PolicyID, afterPrimary.Name, afterPrimary.Trigger = "rp-after-primary", "after-primary", "AFTER_PRIMARY_BACKUP"
	afterPrimary.PrimaryPolicyIDs = []string{"bp-primary"}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, afterPrimary), now); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		body control.ReplicationPolicyPutBody
	}{
		{name: "zero factor", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-zero"
			b.Name = "zero"
			b.ReplicaFactor = 0
			return b
		}()},
		{name: "self replication", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-self"
			b.Name = "self"
			b.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-a"}}}
			return b
		}()},
		{name: "duplicate target", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-dup"
			b.Name = "dup"
			b.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b", "node-b"}}}
			return b
		}()},
		{name: "invalid trigger", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-trigger"
			b.Name = "trigger"
			b.Trigger = "ON_DEMAND"
			return b
		}()},
		{name: "manual malformed cron metadata", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-manual-cron"
			b.Name = "manual-cron"
			b.Trigger = "MANUAL"
			b.ScheduleCron = "0 * *"
			return b
		}()},
		{name: "manual malformed timezone metadata", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-manual-timezone"
			b.Name = "manual-timezone"
			b.Trigger = "MANUAL"
			b.Timezone = "Moon/Base"
			return b
		}()},
		{name: "after primary malformed cron metadata", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-primary-cron"
			b.Name = "primary-cron"
			b.Trigger = "AFTER_PRIMARY_BACKUP"
			b.PrimaryPolicyIDs = []string{"bp-primary"}
			b.ScheduleCron = "0 * *"
			return b
		}()},
		{name: "scheduled trigger missing schedule", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-schedule-empty"
			b.Name = "schedule-empty"
			b.Trigger = "SCHEDULE"
			b.ScheduleCron = ""
			b.Timezone = "UTC"
			return b
		}()},
		{name: "scheduled trigger invalid timezone", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-schedule-timezone"
			b.Name = "schedule-timezone"
			b.Trigger = "SCHEDULE"
			b.ScheduleCron = "0 2 * * *"
			b.Timezone = "Moon/Base"
			return b
		}()},
		{name: "scheduled trigger invalid cron", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-schedule-cron"
			b.Name = "schedule-cron"
			b.Trigger = "SCHEDULE"
			b.ScheduleCron = "0 * *"
			b.Timezone = "UTC"
			return b
		}()},
		{name: "after primary missing policies", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-primary-empty"
			b.Name = "primary-empty"
			b.Trigger = "AFTER_PRIMARY_BACKUP"
			return b
		}()},
		{name: "after primary blank policy", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-primary-blank"
			b.Name = "primary-blank"
			b.Trigger = "AFTER_PRIMARY_BACKUP"
			b.PrimaryPolicyIDs = []string{" "}
			return b
		}()},
		{name: "after primary unknown policy", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-primary-unknown"
			b.Name = "primary-unknown"
			b.Trigger = "AFTER_PRIMARY_BACKUP"
			b.PrimaryPolicyIDs = []string{"missing"}
			return b
		}()},
		{name: "after primary duplicate policy", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-primary-duplicate"
			b.Name = "primary-duplicate"
			b.Trigger = "AFTER_PRIMARY_BACKUP"
			b.PrimaryPolicyIDs = []string{"bp-primary", "bp-primary"}
			return b
		}()},
		{name: "negative retention", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-negative-retention"
			b.Name = "negative-retention"
			b.RetentionKeepLast = -1
			return b
		}()},
		{name: "negative retention days", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-negative-days"
			b.Name = "negative-days"
			b.RetentionKeepDays = -1
			return b
		}()},
		{name: "negative retention bytes", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-negative-bytes"
			b.Name = "negative-bytes"
			b.RetentionMaxBytes = -1
			return b
		}()},
		{name: "negative concurrency", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-negative-concurrency"
			b.Name = "negative-concurrency"
			b.MaxConcurrency = -1
			return b
		}()},
		{name: "negative bandwidth", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-negative-bandwidth"
			b.Name = "negative-bandwidth"
			b.BandwidthLimit = -1
			return b
		}()},
		{name: "blank source route", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-blank-source"
			b.Name = "blank-source"
			b.Routes = []control.ReplicationRoute{{SourceNodeID: "  ", TargetNodeIDs: []string{"node-b"}}}
			return b
		}()},
		{name: "blank route target", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-blank-target"
			b.Name = "blank-target"
			b.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"  "}}}
			return b
		}()},
		{name: "duplicate source route", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-dup-source"
			b.Name = "dup-source"
			b.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}, {SourceNodeID: "node-a", TargetNodeIDs: []string{"node-c"}}}
			return b
		}()},
		{name: "unknown explicit source", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-unknown-source"
			b.Name = "unknown-source"
			b.SourceSelector = "EXPLICIT_NODES"
			b.SourceIDs = []string{"node-z"}
			b.Routes = nil
			return b
		}()},
		{name: "unknown route target", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-unknown-target"
			b.Name = "unknown-target"
			b.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-z"}}}
			return b
		}()},
		{name: "revoked route target", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-revoked-target"
			b.Name = "revoked-target"
			b.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-revoked"}}}
			return b
		}()},
		{name: "unknown route source", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-unknown-route-source"
			b.Name = "unknown-route-source"
			b.Routes = []control.ReplicationRoute{{SourceNodeID: "node-z", TargetNodeIDs: []string{"node-b"}}}
			return b
		}()},
		{name: "duplicate explicit sources", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-dup-explicit"
			b.Name = "dup-explicit"
			b.SourceSelector = "EXPLICIT_NODES"
			b.SourceIDs = []string{"node-a", "node-a"}
			b.Routes = nil
			return b
		}()},
		{name: "blank explicit source", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-blank-explicit"
			b.Name = "blank-explicit"
			b.SourceSelector = "EXPLICIT_NODES"
			b.SourceIDs = []string{" "}
			b.Routes = nil
			return b
		}()},
		{name: "revoked explicit source", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-revoked-explicit"
			b.Name = "revoked-explicit"
			b.SourceSelector = "EXPLICIT_NODES"
			b.SourceIDs = []string{"node-revoked"}
			b.Routes = nil
			return b
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, tc.body), now); !errcode.Is(err, errcode.INVALID) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestFSM_ReplicationPolicyDeleteRejectsMissingPolicy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(mustEncode(t, control.CmdReplicationPolicyDelete, control.ReplicationPolicyDeleteBody{PolicyID: "missing"}), now)
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("got %v", err)
	}
}

func TestFSM_EnsureInitializesPolicyMaps(t *testing.T) {
	s := &control.State{}
	s.EnsureForTest()
	if s.BackupPolicies == nil || s.ReplicationPolicies == nil {
		t.Fatalf("backup=%v replication=%v", s.BackupPolicies, s.ReplicationPolicies)
	}
	_ = s.BackupPolicies["missing"]
	_ = s.ReplicationPolicies["missing"]
}

func hasPerm(perms []string, want string) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}

func TestFSM_MemberRemoveStripsGroups(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	_ = s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-a", CertSerial: "AA"}), now)
	_ = s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g-fin", Name: "finance", NowUnix: now.Unix()}), now)
	_ = s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-a"}), now)
	if err := s.Apply(mustEncode(t, "member_remove", control.MemberRemoveBody{NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if s.NodeInGroup("node-a", "g-fin") {
		t.Fatal("removed node must leave groups")
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
	switch typ {
	case control.CmdBackupPolicyPut:
		if b, ok := body.(control.BackupPolicyPutBody); ok && b.OperationID == "" {
			b.OperationID = "test-op"
			body = b
		}
	case control.CmdBackupPolicyDelete:
		if b, ok := body.(control.BackupPolicyDeleteBody); ok && b.OperationID == "" {
			b.OperationID = "test-op"
			body = b
		}
	case control.CmdReplicationPolicyPut:
		if b, ok := body.(control.ReplicationPolicyPutBody); ok && b.OperationID == "" {
			b.OperationID = "test-op"
			body = b
		}
	case control.CmdReplicationPolicyDelete:
		if b, ok := body.(control.ReplicationPolicyDeleteBody); ok && b.OperationID == "" {
			b.OperationID = "test-op"
			body = b
		}
	}
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
	"batch.execute", "alert.read", "alert.manage", "backup.read", "backup.manage",
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
	"batch.execute", "alert.read", "alert.manage", "backup.read", "backup.manage",
}

var operatorPerms = []string{
	"cluster.read", "node.read", "process.read",
	"process.start", "process.stop", "process.restart",
	"process.config.read", "process.logs.read",
	"batch.execute", "alert.read",
}

var viewerPerms = []string{
	"cluster.read", "node.read", "process.read",
	"process.config.read", "process.logs.read",
	"alert.read",
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
