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
		RaftAddr:   "127.0.0.1:18685",
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

func TestFSM_ClaimFireSkippedPersistsWithoutTakeover(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	key := "replication:rp:1700000000"
	first, created, err := s.ClaimFire(control.FireClaimBody{
		OperationID: "op-skip", FireKey: key, PolicyID: "rp", LeaderTerm: 3,
		ScheduledUnix: now.Unix(), Status: "SKIPPED",
	}, now)
	if err != nil || !created || first.Status != "SKIPPED" || first.LeaseUntilUnix != 0 {
		t.Fatalf("first=%+v created=%v err=%v", first, created, err)
	}
	second, created, err := s.ClaimFire(control.FireClaimBody{
		OperationID: "op-skip-again", FireKey: key, PolicyID: "rp", LeaderTerm: 9,
		ScheduledUnix: now.Unix(), Status: "CLAIMED",
	}, now.Add(time.Hour))
	if err != nil || created || second.Status != "SKIPPED" || second.LeaderTerm != 3 {
		t.Fatalf("takeover skipped fire: %+v created=%v err=%v", second, created, err)
	}
	s.PruneRunMetadata(now.Add(time.Hour).Unix())
	if _, ok := s.BackupFireLedger[key]; !ok {
		t.Fatal("SKIPPED replication fire pruned")
	}
	if _, created, err := s.ClaimFire(control.FireClaimBody{
		OperationID: "op-backup", FireKey: "rp:1700000000", PolicyID: "rp", LeaderTerm: 3,
	}, now); err != nil || !created {
		t.Fatalf("backup fire key collision err=%v created=%v", err, created)
	}
}

func TestFSM_ClaimFireReplicationClaimedPersistsWithoutTakeover(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	key := "replication:rp:1700000000"
	first, created, err := s.ClaimFire(control.FireClaimBody{
		OperationID: "op-claim", FireKey: key, PolicyID: "rp", LeaderTerm: 3,
		ScheduledUnix: now.Unix(), Status: "CLAIMED", RunID: "run-auto", Durable: true,
	}, now)
	if err != nil || !created || first.Status != "CLAIMED" || first.LeaseUntilUnix != 0 || first.RunID != "run-auto" {
		t.Fatalf("first=%+v created=%v err=%v", first, created, err)
	}
	second, created, err := s.ClaimFire(control.FireClaimBody{
		OperationID: "op-claim-again", FireKey: key, PolicyID: "rp", LeaderTerm: 9,
		ScheduledUnix: now.Unix(), Status: "CLAIMED", RunID: "run-other", Durable: true,
	}, now.Add(time.Hour))
	if err != nil || created || second.Status != "CLAIMED" || second.LeaderTerm != 3 || second.RunID != "run-auto" {
		t.Fatalf("takeover claimed fire: %+v created=%v err=%v", second, created, err)
	}
	s.PruneRunMetadata(now.Add(time.Hour).Unix())
	if got, ok := s.BackupFireLedger[key]; !ok || got.LeaseUntilUnix != 0 {
		t.Fatalf("CLAIMED replication fire pruned: %+v ok=%v", got, ok)
	}
}

func TestFSM_ClaimScheduledRunIsAtomicAndFreezesRun(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := control.NewState()
	s.BackupPolicies["bp"] = control.BackupPolicy{PolicyID: "bp", Revision: 1, TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"a"}}
	bad := control.ScheduledRunClaimBody{Fire: control.FireClaimBody{OperationID: "op-bad", FireKey: "bp:1700000000", PolicyID: "bp", LeaderTerm: 1}, Run: control.ClusterBackupRun{RunID: "wrong", PolicyID: "bp", PolicyRevision: 1, TargetNodeIDs: []string{"a"}, Status: "RUNNING"}}
	if _, _, _, err := s.ClaimScheduledRun(bad, now); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("bad claim: %v", err)
	}
	if len(s.BackupFireLedger) != 0 || len(s.BackupRuns) != 0 {
		t.Fatalf("orphan state fires=%v runs=%v", s.BackupFireLedger, s.BackupRuns)
	}
	key := "bp:1700000000"
	sum := sha256.Sum256([]byte(key))
	good := control.ScheduledRunClaimBody{Fire: control.FireClaimBody{OperationID: "op-good", FireKey: key, PolicyID: "bp", LeaderTerm: 1}, Run: control.ClusterBackupRun{RunID: "run-" + fmt.Sprintf("%x", sum[:12]), PolicyID: "bp", PolicyRevision: 1, TargetNodeIDs: []string{"a"}, Status: "RUNNING", Sink: "fs", DestinationProfile: "archive", MaxConcurrency: 2}}
	record, run, acquired, err := s.ClaimScheduledRun(good, now)
	if err != nil || !acquired || record.RunID != run.RunID || run.DestinationProfile != "archive" {
		t.Fatalf("record=%+v run=%+v acquired=%v err=%v", record, run, acquired, err)
	}
	_, live, acquired, err := s.ClaimScheduledRun(good, now.Add(time.Second))
	if err != nil || acquired || live.RunID != run.RunID {
		t.Fatalf("live=%+v acquired=%v err=%v", live, acquired, err)
	}
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-complete", LeaderTerm: 1, Task: control.ClusterBackupTask{RunID: run.RunID, TaskID: "task-a", NodeID: "a", Status: "SUCCESS", UpdatedUnix: now.Add(time.Minute).Unix()}}); err != nil {
		t.Fatal(err)
	}
	good.Fire.OperationID = "op-after-complete"
	good.Fire.LeaderTerm = 2
	_, completed, acquired, err := s.ClaimScheduledRun(good, now.Add(3*time.Minute))
	if err != nil || acquired || completed.Status != "SUCCEEDED" {
		t.Fatalf("completed=%+v acquired=%v err=%v", completed, acquired, err)
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

func TestFSM_ReplicationRunCreateCommitsTasksAtomically(t *testing.T) {
	state := control.NewState()
	state.Members["source"] = control.Member{NodeID: "source", Status: control.MemberAdmitted}
	state.Members["target-a"] = control.Member{NodeID: "target-a", Status: control.MemberAdmitted}
	state.Members["target-b"] = control.Member{NodeID: "target-b", Status: control.MemberAdmitted}
	state.ReplicationPolicies["rp-atomic"] = control.ReplicationPolicy{
		PolicyID: "rp-atomic", Revision: 1, SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"},
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target-a", "target-b"}}},
	}
	run := control.ClusterBackupRun{RunID: "run-atomic", PolicyID: "rp-atomic", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING"}
	tasks := []control.ClusterBackupTask{
		{RunID: run.RunID, TaskID: "task-a", NodeID: "target-a", SourceNodeID: "source", Status: "PENDING"},
		{RunID: run.RunID, TaskID: "task-b", NodeID: "", SourceNodeID: "source", Status: "PENDING"},
	}
	err := state.CreateRun(control.CreateRunBody{OperationID: "op-atomic", LeaderTerm: 4, Replication: true, Run: run, Tasks: tasks})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("CreateRun error = %v, want INVALID", err)
	}
	if len(state.ReplicationRuns) != 0 || len(state.ReplicationTasks) != 0 {
		t.Fatalf("partial state after failed create: runs=%+v tasks=%+v", state.ReplicationRuns, state.ReplicationTasks)
	}

	tasks[1].NodeID = "target-b"
	if err := state.CreateRun(control.CreateRunBody{OperationID: "op-atomic", LeaderTerm: 4, Replication: true, Run: run, Tasks: tasks}); err != nil {
		t.Fatal(err)
	}
	if len(state.ReplicationRuns) != 1 || len(state.ReplicationTasks) != 2 {
		t.Fatalf("atomic state: runs=%+v tasks=%+v", state.ReplicationRuns, state.ReplicationTasks)
	}
}

func TestFSM_ReplicationRetryPreservesFrozenSnapshotBinding(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	state := control.NewState()
	state.Members["source"] = control.Member{NodeID: "source", Status: control.MemberAdmitted}
	state.Members["target"] = control.Member{NodeID: "target", Status: control.MemberAdmitted}
	state.ReplicationPolicies["rp-retry"] = control.ReplicationPolicy{
		PolicyID: "rp-retry", Revision: 1, SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"source"},
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	run := control.ClusterBackupRun{RunID: "run-retry-replica", PolicyID: "rp-retry", PolicyRevision: 1, TargetNodeIDs: []string{"source"}, Status: "RUNNING"}
	task := control.ClusterBackupTask{
		RunID: run.RunID, TaskID: "task-retry-replica", NodeID: "target", SourceNodeID: "source",
		SnapshotID: "snapshot-frozen", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "PENDING",
	}
	if err := state.CreateRun(control.CreateRunBody{OperationID: "op-create-replica", LeaderTerm: 5, Replication: true, Run: run, Tasks: []control.ClusterBackupTask{task}}); err != nil {
		t.Fatal(err)
	}
	task.Status = "FAILED"
	task.ErrorCode = "UNAVAILABLE"
	if err := state.UpdateTask(control.UpdateTaskBody{OperationID: "op-fail-replica", LeaderTerm: 5, Replication: true, Task: task}); err != nil {
		t.Fatal(err)
	}
	if err := state.RetryFailedTasks(control.RetryFailedTasksBody{OperationID: "op-retry-replica", RunID: run.RunID, LeaderTerm: 5, UpdatedUnix: now.Unix(), Replication: true}); err != nil {
		t.Fatal(err)
	}
	got := state.ReplicationTasks[run.RunID+":"+task.TaskID]
	if got.Status != "PENDING" || got.SnapshotID != task.SnapshotID || got.SHA256 != task.SHA256 {
		t.Fatalf("retried task=%+v, want frozen snapshot/checksum preserved", got)
	}
}

func TestFSM_BeginReplicationTask(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newState := func() *control.State {
		state := control.NewState()
		state.ReplicationRuns["run"] = control.ClusterBackupRun{RunID: "run", PolicyID: "policy", PolicyRevision: 3, Status: "RUNNING", LeaseUntilUnix: now.Add(time.Minute).Unix()}
		state.ReplicationRunTerms["run"] = 7
		state.ReplicationTasks["run:task"] = control.ClusterBackupTask{
			RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot", SHA256: sha,
			Status: "UNAVAILABLE", Bytes: 42, ErrorCode: "UNAVAILABLE", ErrorSummary: "safe summary", LeaderTerm: 7, UpdatedUnix: now.Add(-time.Minute).Unix(),
		}
		return state
	}
	begin := func(state *control.State, term uint64, task control.ClusterBackupTask) error {
		task.Status = "RUNNING"
		task.UpdatedUnix = now.Unix()
		return state.UpdateTask(control.UpdateTaskBody{OperationID: "begin", Task: task, LeaderTerm: term, Replication: true})
	}

	t.Run("terminal update does not blank captured snapshot", func(t *testing.T) {
		state := control.NewState()
		state.ReplicationRuns["run"] = control.ClusterBackupRun{RunID: "run", PolicyID: "policy", PolicyRevision: 3, Status: "RUNNING", LeaseUntilUnix: now.Add(time.Minute).Unix()}
		state.ReplicationRunTerms["run"] = 7
		state.ReplicationTasks["run:task"] = control.ClusterBackupTask{
			RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", Status: "PENDING", LeaderTerm: 7,
		}
		if err := begin(state, 7, state.ReplicationTasks["run:task"]); err != nil {
			t.Fatal(err)
		}
		frozen := state.ReplicationTasks["run:task"]
		frozen.SnapshotID, frozen.SHA256 = "snapshot", sha
		if err := begin(state, 7, frozen); err != nil {
			t.Fatal(err)
		}
		if err := state.UpdateTask(control.UpdateTaskBody{
			OperationID: "fail-empty", LeaderTerm: 7, Replication: true,
			Task: control.ClusterBackupTask{RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", Status: "UNAVAILABLE", ErrorCode: "UNAVAILABLE", UpdatedUnix: now.Unix()},
		}); err != nil {
			t.Fatal(err)
		}
		got := state.ReplicationTasks["run:task"]
		if got.Status != "UNAVAILABLE" || got.SnapshotID != "snapshot" || got.SHA256 != sha {
			t.Fatalf("blanked captured snapshot: %+v", got)
		}
	})

	t.Run("allows empty snapshot pending capture", func(t *testing.T) {
		state := control.NewState()
		state.ReplicationRuns["run"] = control.ClusterBackupRun{RunID: "run", PolicyID: "policy", PolicyRevision: 3, Status: "RUNNING", LeaseUntilUnix: now.Add(time.Minute).Unix()}
		state.ReplicationRunTerms["run"] = 7
		state.ReplicationTasks["run:task"] = control.ClusterBackupTask{
			RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", Status: "PENDING", LeaderTerm: 7,
		}
		if err := begin(state, 7, state.ReplicationTasks["run:task"]); err != nil {
			t.Fatal(err)
		}
		got := state.ReplicationTasks["run:task"]
		if got.Status != "RUNNING" || got.SnapshotID != "" || got.SHA256 != "" {
			t.Fatalf("empty capture begin=%+v", got)
		}
	})

	t.Run("freezes captured identity onto empty running task", func(t *testing.T) {
		state := control.NewState()
		state.ReplicationRuns["run"] = control.ClusterBackupRun{RunID: "run", PolicyID: "policy", PolicyRevision: 3, Status: "RUNNING", LeaseUntilUnix: now.Add(time.Minute).Unix()}
		state.ReplicationRunTerms["run"] = 7
		state.ReplicationTasks["run:task"] = control.ClusterBackupTask{
			RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", Status: "RUNNING", LeaderTerm: 7,
		}
		frozen := state.ReplicationTasks["run:task"]
		frozen.SnapshotID, frozen.SHA256 = "snapshot", sha
		if err := begin(state, 7, frozen); err != nil {
			t.Fatal(err)
		}
		got := state.ReplicationTasks["run:task"]
		if got.Status != "RUNNING" || got.SnapshotID != "snapshot" || got.SHA256 != sha {
			t.Fatalf("frozen running task=%+v", got)
		}
		changed := got
		changed.SnapshotID = "other"
		if err := begin(state, 7, changed); !errcode.Is(err, errcode.CONFLICT) {
			t.Fatalf("changed frozen identity error=%v, want CONFLICT", err)
		}
	})

	t.Run("moves retryable task to running without changing identity", func(t *testing.T) {
		state := newState()
		if err := begin(state, 7, state.ReplicationTasks["run:task"]); err != nil {
			t.Fatal(err)
		}
		got := state.ReplicationTasks["run:task"]
		if got.Status != "RUNNING" || got.Bytes != 0 || got.ErrorCode != "" || got.ErrorSummary != "" {
			t.Fatalf("begun task=%+v", got)
		}
		if got.RunID != "run" || got.TaskID != "task" || got.SourceNodeID != "source" || got.NodeID != "target" || got.SnapshotID != "snapshot" || got.SHA256 != sha {
			t.Fatalf("immutable identity changed: %+v", got)
		}
	})

	t.Run("same identity already running begin is idempotent", func(t *testing.T) {
		state := newState()
		if err := begin(state, 7, state.ReplicationTasks["run:task"]); err != nil {
			t.Fatal(err)
		}
		if err := begin(state, 7, state.ReplicationTasks["run:task"]); err != nil {
			t.Fatalf("same-identity running begin error=%v, want nil", err)
		}
		got := state.ReplicationTasks["run:task"]
		if got.Status != "RUNNING" || got.SnapshotID != "snapshot" || got.SHA256 != sha {
			t.Fatalf("idempotent begin changed task=%+v", got)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*control.State, *control.ClusterBackupTask)
	}{
		{name: "stale term", mutate: func(_ *control.State, task *control.ClusterBackupTask) { task.LeaderTerm = 6 }},
		{name: "expired lease", mutate: func(state *control.State, _ *control.ClusterBackupTask) {
			run := state.ReplicationRuns["run"]
			run.LeaseUntilUnix = now.Unix()
			state.ReplicationRuns["run"] = run
		}},
		{name: "changed identity", mutate: func(_ *control.State, task *control.ClusterBackupTask) { task.SnapshotID = "changed" }},
		{name: "succeeded task", mutate: func(state *control.State, task *control.ClusterBackupTask) {
			task.Status = "SUCCEEDED"
			state.ReplicationTasks["run:task"] = *task
		}},
		{name: "changed identity on running task", mutate: func(state *control.State, task *control.ClusterBackupTask) {
			running := *task
			running.Status = "RUNNING"
			state.ReplicationTasks["run:task"] = running
			task.SnapshotID = "changed"
		}},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			state := newState()
			task := state.ReplicationTasks["run:task"]
			tc.mutate(state, &task)
			term := uint64(7)
			if tc.name == "stale term" {
				term = 6
			}
			if err := begin(state, term, task); !errcode.Is(err, errcode.CONFLICT) {
				t.Fatalf("begin error=%v, want CONFLICT", err)
			}
			got := state.ReplicationTasks["run:task"]
			wantStatus := "UNAVAILABLE"
			switch tc.name {
			case "succeeded task":
				wantStatus = "SUCCEEDED"
			case "changed identity on running task":
				wantStatus = "RUNNING"
			}
			if got.Status != wantStatus {
				t.Fatalf("task changed after rejected begin: %+v want %s", got, wantStatus)
			}
		})
	}
}

func TestFSM_BeginReplicationTaskRejectsCommandAppliedAfterLeaseExpiry(t *testing.T) {
	commandTime := time.Unix(1_800_000_000, 0)
	state := control.NewState()
	state.ReplicationRuns["run"] = control.ClusterBackupRun{RunID: "run", PolicyID: "policy", PolicyRevision: 3, Status: "RUNNING", LeaseUntilUnix: commandTime.Add(time.Second).Unix()}
	state.ReplicationRunTerms["run"] = 7
	state.ReplicationTasks["run:task"] = control.ClusterBackupTask{
		RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot",
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "UNAVAILABLE", LeaderTerm: 7,
	}
	cmd, err := control.EncodeCommand(control.CmdBackupTaskUpdate, control.UpdateTaskBody{
		OperationID: "begin-delayed", LeaderTerm: 7, Replication: true,
		Task: control.ClusterBackupTask{
			RunID: "run", TaskID: "task", SourceNodeID: "source", NodeID: "target", SnapshotID: "snapshot",
			SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "RUNNING", UpdatedUnix: commandTime.Unix(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Apply(cmd, commandTime.Add(2*time.Second)); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("delayed begin error=%v, want CONFLICT", err)
	}
	if got := state.ReplicationTasks["run:task"]; got.Status != "UNAVAILABLE" {
		t.Fatalf("task changed after delayed begin: %+v", got)
	}
}

func TestFSM_ReplicationAggregatesRoutesSharingTarget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, statuses := range map[string][]string{"succeeded": {"SUCCEEDED", "SUCCEEDED"}, "partial": {"SUCCEEDED", "FAILED"}, "failed": {"FAILED", "FAILED"}} {
		t.Run(name, func(t *testing.T) {
			state := control.NewState()
			state.ReplicationRuns["rep"] = control.ClusterBackupRun{RunID: "rep", PolicyID: "rp", Status: "RUNNING", TargetNodeIDs: []string{"source-a", "source-b"}}
			state.ReplicationRunTerms["rep"] = 3
			for i := range statuses {
				task := control.ClusterBackupTask{RunID: "rep", TaskID: fmt.Sprintf("route-%d", i), SourceNodeID: fmt.Sprintf("source-%d", i), NodeID: "shared-target", SnapshotID: fmt.Sprintf("snap-%d", i), SHA256: strings.Repeat("a", 64), Status: "PENDING"}
				state.ReplicationTasks[task.RunID+":"+task.TaskID] = task
			}
			for i, status := range statuses {
				task := state.ReplicationTasks["rep:"+fmt.Sprintf("route-%d", i)]
				task.Status, task.UpdatedUnix = status, now.Unix()+int64(i)
				if err := state.UpdateTask(control.UpdateTaskBody{OperationID: fmt.Sprintf("op-%s-%d", name, i), LeaderTerm: 3, Replication: true, Task: task}); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string]string{"succeeded": "SUCCEEDED", "partial": "PARTIAL", "failed": "FAILED"}[name]
			if got := state.ReplicationRuns["rep"].Status; got != want {
				t.Fatalf("status=%s want=%s", got, want)
			}
		})
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
	if got := s.BackupRuns[run.RunID]; got.Status != "SUCCEEDED" || got.Success != 1 {
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

func TestFSM_BackupRunAggregatesTerminalTasks(t *testing.T) {
	tests := []struct {
		name       string
		statuses   []string
		wantStatus string
		want       [4]int
	}{
		{name: "all success", statuses: []string{"SUCCESS", "SUCCEEDED"}, wantStatus: "SUCCEEDED", want: [4]int{2, 0, 0, 0}},
		{name: "partial", statuses: []string{"SUCCESS", "UNAVAILABLE", "TIMEOUT", "CONFIG_MISSING"}, wantStatus: "PARTIAL", want: [4]int{1, 1, 1, 1}},
		{name: "all failure", statuses: []string{"FAILED", "RETENTION_FAILED", "SKIPPED"}, wantStatus: "FAILED", want: [4]int{0, 3, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := control.NewState()
			targets := make([]string, len(tt.statuses))
			for i := range targets {
				targets[i] = fmt.Sprintf("node-%d", i)
			}
			s.BackupPolicies["bp-aggregate"] = control.BackupPolicy{PolicyID: "bp-aggregate", Revision: 1, TargetSelector: "EXPLICIT_NODES", TargetIDs: targets}
			run := control.ClusterBackupRun{RunID: "run-aggregate", PolicyID: "bp-aggregate", PolicyRevision: 1, TargetNodeIDs: targets, Status: "RUNNING", CreatedUnix: 1_700_000_000, StartedUnix: 1_700_000_000}
			if err := s.CreateRun(control.CreateRunBody{OperationID: "op-create", LeaderTerm: 2, Run: run}); err != nil {
				t.Fatal(err)
			}
			for i, status := range tt.statuses {
				updated := int64(1_700_000_010 + i)
				if err := s.UpdateTask(control.UpdateTaskBody{OperationID: fmt.Sprintf("op-task-%d", i), LeaderTerm: 2, Task: control.ClusterBackupTask{RunID: run.RunID, TaskID: "task-" + targets[i], NodeID: targets[i], Status: status, UpdatedUnix: updated}}); err != nil {
					t.Fatal(err)
				}
				if i < len(tt.statuses)-1 && s.BackupRuns[run.RunID].Status != "RUNNING" {
					t.Fatalf("run completed before every frozen target reported: %+v", s.BackupRuns[run.RunID])
				}
			}
			got := s.BackupRuns[run.RunID]
			if got.Status != tt.wantStatus || got.Success != tt.want[0] || got.Failed != tt.want[1] || got.Unavailable != tt.want[2] || got.Timeout != tt.want[3] {
				t.Fatalf("run=%+v", got)
			}
			wantFinished := int64(1_700_000_010 + len(tt.statuses) - 1)
			if got.FinishedUnix != wantFinished {
				t.Fatalf("finished_unix=%d want=%d", got.FinishedUnix, wantFinished)
			}
		})
	}
}

func TestFSM_RetryFailedTasksResetsAllRetryableAndReopensRun(t *testing.T) {
	s := control.NewState()
	statuses := []string{"SUCCESS", "FAILED", "TIMEOUT", "UNAVAILABLE", "CONFIG_MISSING", "RETENTION_FAILED", "SKIPPED"}
	targets := make([]string, len(statuses))
	for i := range targets {
		targets[i] = fmt.Sprintf("node-%d", i)
	}
	s.BackupPolicies["bp-retry-all"] = control.BackupPolicy{PolicyID: "bp-retry-all", Revision: 1, TargetSelector: "EXPLICIT_NODES", TargetIDs: targets}
	run := control.ClusterBackupRun{RunID: "run-retry-all", PolicyID: "bp-retry-all", PolicyRevision: 1, TargetNodeIDs: targets, Status: "RUNNING"}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-create", LeaderTerm: 2, Run: run}); err != nil {
		t.Fatal(err)
	}
	for i, status := range statuses {
		task := control.ClusterBackupTask{RunID: run.RunID, TaskID: "task-" + targets[i], NodeID: targets[i], Status: status, SnapshotID: "snapshot", SHA256: "sha", Bytes: 42, ErrorCode: "E_TEST", ErrorSummary: "failed", UpdatedUnix: int64(100 + i)}
		if err := s.UpdateTask(control.UpdateTaskBody{OperationID: fmt.Sprintf("op-task-%d", i), LeaderTerm: 2, Task: task}); err != nil {
			t.Fatal(err)
		}
	}
	terminal := s.BackupRuns[run.RunID]
	if terminal.Status != "PARTIAL" {
		t.Fatalf("precondition run=%+v", terminal)
	}
	if err := s.RetryFailedTasks(control.RetryFailedTasksBody{OperationID: "op-retry", RunID: run.RunID, LeaderTerm: 3, UpdatedUnix: 1_700_000_100}); err != nil {
		t.Fatal(err)
	}
	gotRun := s.BackupRuns[run.RunID]
	if gotRun.Status != "RUNNING" || gotRun.Success != 0 || gotRun.Failed != 0 || gotRun.Unavailable != 0 || gotRun.Timeout != 0 || gotRun.FinishedUnix != 0 {
		t.Fatalf("run not reopened cleanly: %+v", gotRun)
	}
	for i, oldStatus := range statuses {
		got := s.BackupTasks[run.RunID+":"+"task-"+targets[i]]
		if oldStatus == "SUCCESS" {
			if got.Status != "SUCCESS" || got.SnapshotID != "snapshot" || got.SHA256 != "sha" || got.Bytes != 42 || got.LeaderTerm != 2 {
				t.Fatalf("successful task changed: %+v", got)
			}
			continue
		}
		if got.Status != "PENDING" || got.SnapshotID != "" || got.SHA256 != "" || got.Bytes != 0 || got.ErrorCode != "" || got.ErrorSummary != "" || got.LeaderTerm != 3 || got.UpdatedUnix != 1_700_000_100 {
			t.Fatalf("retryable %s task=%+v", oldStatus, got)
		}
	}
}

func TestFSM_ClaimExpiredBackupRunFencesOldLeader(t *testing.T) {
	s := control.NewState()
	s.BackupPolicies["bp-claim"] = control.BackupPolicy{PolicyID: "bp-claim", Revision: 1}
	run := control.ClusterBackupRun{
		RunID: "run-claim", PolicyID: "bp-claim", PolicyRevision: 1, TargetNodeIDs: []string{"node-a"}, Status: "RUNNING",
		TimeoutSeconds: 45, UnavailablePolicy: "FAIL_FAST", LeaseUntilUnix: 1_700_000_100,
	}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-create", LeaderTerm: 2, Run: run}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimRun(control.RunClaimBody{OperationID: "op-live", RunID: run.RunID, LeaderTerm: 3, UpdatedUnix: 1_700_000_099, LeaseUntilUnix: 1_700_000_200})
	if err != nil || claimed {
		t.Fatalf("live run claimed=%v err=%v", claimed, err)
	}
	claimed, err = s.ClaimRun(control.RunClaimBody{OperationID: "op-takeover", RunID: run.RunID, LeaderTerm: 3, UpdatedUnix: 1_700_000_101, LeaseUntilUnix: 1_700_000_200})
	if err != nil || !claimed {
		t.Fatalf("expired run claimed=%v err=%v", claimed, err)
	}
	got := s.BackupRuns[run.RunID]
	if got.LeaseUntilUnix != 1_700_000_200 || got.TimeoutSeconds != 45 || got.UnavailablePolicy != "FAIL_FAST" {
		t.Fatalf("claimed run=%+v", got)
	}
	if err := s.UpdateTask(control.UpdateTaskBody{OperationID: "op-stale-task", LeaderTerm: 2, Task: control.ClusterBackupTask{RunID: run.RunID, TaskID: "task-node-a", NodeID: "node-a", Status: "FAILED"}}); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("old leader was not fenced: %v", err)
	}
	claimed, err = s.ClaimRun(control.RunClaimBody{OperationID: "op-same-term", RunID: run.RunID, LeaderTerm: 3, UpdatedUnix: 1_700_000_201, LeaseUntilUnix: 1_700_000_300})
	if err != nil || claimed {
		t.Fatalf("same term reclaimed=%v err=%v", claimed, err)
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

func TestFSM_CustomRoleAndBindingMutations(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, control.CmdRolePut, control.RolePutBody{
		ID: "custom", Name: "Custom", Perms: []string{"cluster.read"},
	}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, control.CmdBindPut, control.BindPutBody{
		UserID: "user-admin", RoleID: "custom", Scope: control.ScopeAgent, ScopeID: "node-1",
	}), now); err != nil {
		t.Fatal(err)
	}

	if err := s.Apply(mustEncode(t, control.CmdRolePut, control.RolePutBody{
		ID: "custom", Name: "Updated", Perms: []string{"node.read"},
	}), now); err != nil {
		t.Fatal(err)
	}
	if got := s.Roles["custom"]; got.Name != "Updated" || !hasPerm(got.Perms, "node.read") {
		t.Fatalf("updated role=%+v", got)
	}

	err := s.Apply(mustEncode(t, control.CmdRoleDelete, control.RoleDeleteBody{ID: "custom"}), now)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("delete bound role: %v", err)
	}
	if err := s.Apply(mustEncode(t, control.CmdBindDelete, control.BindDeleteBody{
		UserID: "user-admin", RoleID: "custom", Scope: control.ScopeAgent, ScopeID: "node-1",
	}), now); err != nil {
		t.Fatal(err)
	}
	if len(s.Bindings) != 1 {
		t.Fatalf("bindings=%+v", s.Bindings)
	}
	if err := s.Apply(mustEncode(t, control.CmdRoleDelete, control.RoleDeleteBody{ID: "custom"}), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Roles["custom"]; ok {
		t.Fatal("custom role was not deleted")
	}
	err = s.Apply(mustEncode(t, control.CmdRolePut, control.RolePutBody{
		ID: "custom", Name: "Unexpected", ExistingOnly: true,
	}), now)
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("update deleted role: %v", err)
	}
}

func TestFSM_BuiltinRolesAndBindingsCannotBeMutated(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)

	err := s.Apply(mustEncode(t, control.CmdRolePut, control.RolePutBody{
		ID: "viewer", Name: "Changed", Perms: []string{"role.manage"},
	}), now)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("update builtin role: %v", err)
	}
	err = s.Apply(mustEncode(t, control.CmdRoleDelete, control.RoleDeleteBody{ID: "viewer"}), now)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("delete builtin role: %v", err)
	}
	err = s.Apply(mustEncode(t, control.CmdBindDelete, control.BindDeleteBody{
		UserID: "user-admin", RoleID: "super_admin", Scope: control.ScopeCluster,
	}), now)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("delete builtin binding: %v", err)
	}
	if !s.Check("user-admin", "role.manage", "") {
		t.Fatal("builtin binding was removed")
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
		{PolicyID: "rp-group-blank", Name: "group-blank", SourceSelector: "AGENT_GROUP", SourceIDs: []string{" "}, ReplicaFactor: 1},
		{PolicyID: "rp-group-duplicate", Name: "group-duplicate", SourceSelector: "AGENT_GROUP", SourceIDs: []string{"g-1", "g-1"}, ReplicaFactor: 1},
		{PolicyID: "rp-group-missing", Name: "group-missing", SourceSelector: "AGENT_GROUP", SourceIDs: []string{"missing"}, ReplicaFactor: 1},
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

func TestFSM_ReplicationPolicyPut_OptionalCronAndEpoch(t *testing.T) {
	s := admittedReplicationState(t)
	now := time.Unix(1_800_000_000, 0) // 不是 02:00
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-1", PolicyID: "rp-1", Name: "cluster-replica",
		Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes:       admittedReplicationRoutes(),
		ScheduleCron: "0 2 * * *", Timezone: "UTC", ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); err != nil {
		t.Fatal(err)
	}
	got := s.ReplicationPolicies["rp-1"]
	if got.ScheduleCron != "0 2 * * *" || got.Timezone != "UTC" {
		t.Fatalf("schedule=%q tz=%q", got.ScheduleCron, got.Timezone)
	}
	if got.Trigger != "" || len(got.PrimaryPolicyIDs) != 0 {
		t.Fatalf("legacy fields must be cleared: %+v", got)
	}
	if got.ScheduleEpochUnix != now.Unix() {
		t.Fatalf("epoch=%d want %d", got.ScheduleEpochUnix, now.Unix())
	}
}

func TestFSM_ReplicationPolicyPut_EmptyCronIsManualOnly(t *testing.T) {
	s := admittedReplicationState(t)
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-2", PolicyID: "rp-manual", Name: "manual-only",
		Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes:           admittedReplicationRoutes(),
		ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestFSM_ReplicationPolicyPut_RejectsBadCronEvenWithoutTrigger(t *testing.T) {
	s := admittedReplicationState(t)
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-3", PolicyID: "rp-bad", Name: "bad",
		Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes:       admittedReplicationRoutes(),
		ScheduleCron: "0 * *", Timezone: "UTC", ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), time.Unix(1, 0)); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
}

func TestFSM_RetryFailedReplicationTasks_AllowsMissingSnapshot(t *testing.T) {
	s := admittedReplicationState(t)
	run := control.ClusterBackupRun{RunID: "run-1", PolicyID: "rp-1", PolicyRevision: 1, TargetNodeIDs: []string{"node-a"}, Status: "PARTIAL"}
	s.ReplicationPolicies["rp-1"] = control.ReplicationPolicy{
		PolicyID: "rp-1", Revision: 1, SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"node-a"}, ReplicaFactor: 2,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b", "node-c"}}},
	}
	s.ReplicationRuns[run.RunID] = run
	s.ReplicationRunTerms[run.RunID] = 3
	s.ReplicationTasks["run-1:t-copy"] = control.ClusterBackupTask{
		RunID: "run-1", TaskID: "t-copy", SourceNodeID: "node-a", NodeID: "node-b",
		SnapshotID: "snap", SHA256: strings.Repeat("a", 64), Status: "FAILED",
	}
	s.ReplicationTasks["run-1:t-cap"] = control.ClusterBackupTask{
		RunID: "run-1", TaskID: "t-cap", SourceNodeID: "node-a", NodeID: "node-c", Status: "FAILED",
	}
	if err := s.RetryFailedTasks(control.RetryFailedTasksBody{OperationID: "op-retry", RunID: "run-1", LeaderTerm: 3, UpdatedUnix: 100, LeaseUntilUnix: 130, Replication: true}); err != nil {
		t.Fatal(err)
	}
	if s.ReplicationTasks["run-1:t-cap"].Status != "PENDING" {
		t.Fatalf("capture-failed task not retried: %+v", s.ReplicationTasks["run-1:t-cap"])
	}
	if s.ReplicationTasks["run-1:t-copy"].SnapshotID != "snap" {
		t.Fatal("copy-failed task must keep snapshot_id")
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
		SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes:           admittedReplicationRoutes(),
		ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, valid), now); err != nil {
		t.Fatal(err)
	}
	if got := s.ReplicationPolicies["rp-1"].Routes[0].TargetNodeIDs; len(got) != 1 || got[0] != "node-b" {
		t.Fatalf("routes=%v", got)
	}
	if got := s.ReplicationPolicies["rp-1"]; got.Trigger != "" || len(got.PrimaryPolicyIDs) != 0 {
		t.Fatalf("legacy fields must be cleared: %+v", got)
	}
	afterPrimary := valid
	afterPrimary.PolicyID, afterPrimary.Name, afterPrimary.Trigger = "rp-after-primary", "after-primary", "AFTER_PRIMARY_BACKUP"
	afterPrimary.PrimaryPolicyIDs = []string{"bp-primary"}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, afterPrimary), now); err != nil {
		t.Fatal(err)
	}
	if got := s.ReplicationPolicies["rp-after-primary"]; got.Trigger != "" || len(got.PrimaryPolicyIDs) != 0 {
		t.Fatalf("legacy fields must be cleared: %+v", got)
	}

	legacyAccepted := []struct {
		name string
		body control.ReplicationPolicyPutBody
	}{
		{name: "invalid trigger stripped", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-trigger"
			b.Name = "trigger"
			b.Trigger = "ON_DEMAND"
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
	}
	for _, tc := range legacyAccepted {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, tc.body), now); err != nil {
				t.Fatal(err)
			}
			got := s.ReplicationPolicies[tc.body.PolicyID]
			if got.Trigger != "" || len(got.PrimaryPolicyIDs) != 0 {
				t.Fatalf("legacy fields must be cleared: %+v", got)
			}
		})
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
		{name: "malformed cron metadata", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-manual-cron"
			b.Name = "manual-cron"
			b.ScheduleCron = "0 * *"
			return b
		}()},
		{name: "malformed timezone metadata", body: func() control.ReplicationPolicyPutBody {
			b := valid
			b.PolicyID = "rp-manual-timezone"
			b.Name = "manual-timezone"
			b.ScheduleCron = "0 2 * * *"
			b.Timezone = "Moon/Base"
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

func TestFSM_ReplicationPolicyPutRouteSourcesMatchSelector(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	for _, nodeID := range []string{"node-a", "node-b", "node-c"} {
		if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
	}

	base := control.ReplicationPolicyPutBody{
		PolicyID: "rp-selector", Name: "selector", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"node-a"}, ReplicaFactor: 2, ExpectedRevision: -1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b", "node-c"}}},
	}
	for _, tc := range []struct {
		name   string
		routes []control.ReplicationRoute
	}{
		{name: "missing selected source", routes: nil},
		{name: "extra selector outsider", routes: []control.ReplicationRoute{
			{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b", "node-c"}},
			{SourceNodeID: "node-b", TargetNodeIDs: []string{"node-a", "node-c"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := base
			body.OperationID = "op-" + strings.ReplaceAll(tc.name, " ", "-")
			body.PolicyID = "rp-" + strings.ReplaceAll(tc.name, " ", "-")
			body.Name = tc.name
			body.Routes = tc.routes
			if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); !errcode.Is(err, errcode.INVALID) {
				t.Fatalf("Apply() error=%v, want INVALID", err)
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

func TestFSM_ReplicationPolicyPutExpectedRevision(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	for _, nodeID := range []string{"node-a", "node-b", "node-c"} {
		if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
	}
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-1", PolicyID: "rp-1", Name: "dr", Enabled: true,
		SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes: []control.ReplicationRoute{
			{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}},
			{SourceNodeID: "node-b", TargetNodeIDs: []string{"node-a"}},
			{SourceNodeID: "node-c", TargetNodeIDs: []string{"node-a"}},
		},
		ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); err != nil {
		t.Fatal(err)
	}
	if got := s.ReplicationPolicies["rp-1"].Revision; got != 1 {
		t.Fatalf("revision=%d want 1", got)
	}
	body.OperationID = "op-2"
	body.ExpectedRevision = 0
	err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("stale revision should reject, got %v", err)
	}
	body.OperationID = "op-3"
	body.ExpectedRevision = 1
	body.ReplicaFactor = 2
	body.Routes = []control.ReplicationRoute{
		{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b", "node-c"}},
		{SourceNodeID: "node-b", TargetNodeIDs: []string{"node-a", "node-c"}},
		{SourceNodeID: "node-c", TargetNodeIDs: []string{"node-a", "node-b"}},
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); err != nil {
		t.Fatal(err)
	}
	if got := s.ReplicationPolicies["rp-1"].Revision; got != 2 {
		t.Fatalf("revision=%d want 2", got)
	}
	if got := s.ReplicationPolicies["rp-1"].ReplicaFactor; got != 2 {
		t.Fatalf("factor=%d want 2", got)
	}
	body.OperationID = "op-4"
	body.PolicyID = "rp-99"
	body.ExpectedRevision = 5
	err = s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("expected revision on non-existent policy should reject, got %v", err)
	}
	body.OperationID = "op-5"
	body.PolicyID = "rp-2"
	body.Name = "dr-2"
	body.ExpectedRevision = -1
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); err != nil {
		t.Fatalf("negative expected revision should skip check: %v", err)
	}
	if got := s.ReplicationPolicies["rp-2"].Revision; got != 1 {
		t.Fatalf("new policy revision=%d want 1", got)
	}
}

func TestFSM_ReplicationPolicyPutReplicaFactorVsCandidates(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	for _, nodeID := range []string{"node-a", "node-b"} {
		if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
	}
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-1", PolicyID: "rp-1", Name: "dr", Enabled: true,
		SourceSelector: "ALL_ADMITTED", ReplicaFactor: 3,
		Routes: []control.ReplicationRoute{
			{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}},
			{SourceNodeID: "node-b", TargetNodeIDs: []string{"node-a"}},
		},
		ExpectedRevision: -1,
	}
	err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("factor 3 > 2 candidates should reject, got %v", err)
	}
	body.OperationID = "op-2"
	body.ReplicaFactor = 1
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); err != nil {
		t.Fatal(err)
	}
	body.OperationID = "op-3"
	body.PolicyID = "rp-2"
	body.Name = "explicit"
	body.SourceSelector = "EXPLICIT_NODES"
	body.SourceIDs = []string{"node-a"}
	body.ReplicaFactor = 2
	body.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}}
	err = s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("factor 2 > 1 explicit candidate should reject, got %v", err)
	}
	if err := s.Apply(mustEncode(t, control.CmdGroupPut, control.GroupPutBody{GroupID: "g-web", Name: "web", NowUnix: now.Unix()}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, control.CmdGroupMemberAdd, control.GroupMemberBody{GroupID: "g-web", NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	body.OperationID = "op-4"
	body.PolicyID = "rp-3"
	body.Name = "group"
	body.SourceSelector = "AGENT_GROUP"
	body.SourceIDs = []string{"g-web"}
	body.ReplicaFactor = 2
	body.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}}
	err = s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("factor 2 > 1 group candidate should reject, got %v", err)
	}
	body.OperationID = "op-5"
	body.PolicyID = "rp-4"
	body.Name = "missing-group"
	body.SourceSelector = "AGENT_GROUP"
	body.SourceIDs = []string{"g-nonexistent"}
	body.ReplicaFactor = 1
	body.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}}
	err = s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("non-existent group should have 0 candidates, got %v", err)
	}
}

func TestFSM_ReplicationPolicyPutReplicaFactorUsesAvailableTargetsPerSource(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	for _, nodeID := range []string{"node-a", "node-b", "node-c"} {
		if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
	}
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-factor", PolicyID: "rp-factor", Name: "factor", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{"node-a"}, ReplicaFactor: 2,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b", "node-c"}}}, ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); err != nil {
		t.Fatalf("one source still has two admitted targets: %v", err)
	}
	body.OperationID, body.PolicyID, body.Name = "op-short", "rp-short", "short"
	body.Routes = []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("route shorter than factor must reject, got %v", err)
	}
}

func TestFSM_ReplicationPolicyDeletePreservesReplicas(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	for _, nodeID := range []string{"node-a", "node-b"} {
		if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
	}
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-1", PolicyID: "rp-1", Name: "dr", Enabled: true,
		SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes: []control.ReplicationRoute{
			{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}},
			{SourceNodeID: "node-b", TargetNodeIDs: []string{"node-a"}},
		},
		ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); err != nil {
		t.Fatal(err)
	}
	run := control.ClusterBackupRun{
		RunID: "run-1", PolicyID: "rp-1", PolicyRevision: 1,
		Status: "RUNNING", CreatedUnix: now.Unix(), TargetNodeIDs: []string{"node-a", "node-b"},
	}
	s.ReplicationRuns[run.RunID] = run
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyDelete, control.ReplicationPolicyDeleteBody{OperationID: "op-2", PolicyID: "rp-1"}), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ReplicationPolicies["rp-1"]; ok {
		t.Fatal("policy should be deleted")
	}
	if _, ok := s.ReplicationRuns["run-1"]; !ok {
		t.Fatal("replication run should be preserved after policy deletion")
	}
}

func TestFSM_EnsureInitializesPolicyMaps(t *testing.T) {
	s := &control.State{}
	s.EnsureForTest()
	if s.BackupPolicies == nil || s.ReplicationPolicies == nil || s.ReplicationDeleteIntents == nil {
		t.Fatalf("backup=%v replication=%v deleteIntents=%v", s.BackupPolicies, s.ReplicationPolicies, s.ReplicationDeleteIntents)
	}
	_ = s.BackupPolicies["missing"]
	_ = s.ReplicationPolicies["missing"]
	_ = s.ReplicationDeleteIntents["missing"]
}

func TestFSM_ReplicationDeleteIntent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	valid := func() control.ReplicationDeleteIntent {
		return control.ReplicationDeleteIntent{
			IntentID: "intent-1", PolicyID: "rp", PolicyRevision: 3,
			SourceNodeID: "source", TargetNodeID: "target", SnapshotID: "snapshot",
			LeaderTerm: 7, ExpiresUnix: now.Add(time.Minute).Unix(), Status: "PENDING",
		}
	}
	put := func(state *control.State, intent control.ReplicationDeleteIntent, applyAt time.Time) error {
		return state.Apply(mustEncode(t, control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{
			OperationID: "op-" + intent.IntentID + "-" + intent.Status, Intent: intent,
		}), applyAt)
	}

	t.Run("stores pending metadata-only intent", func(t *testing.T) {
		state := control.NewState()
		intent := valid()
		if err := put(state, intent, now); err != nil {
			t.Fatal(err)
		}
		got, ok := state.ReplicationDeleteIntents[intent.IntentID]
		if !ok {
			t.Fatal("intent not stored")
		}
		if got != intent {
			t.Fatalf("stored intent=%+v want=%+v", got, intent)
		}
	})

	t.Run("idempotent identical put", func(t *testing.T) {
		state := control.NewState()
		intent := valid()
		if err := put(state, intent, now); err != nil {
			t.Fatal(err)
		}
		if err := put(state, intent, now); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("transitions pending to succeeded", func(t *testing.T) {
		state := control.NewState()
		intent := valid()
		if err := put(state, intent, now); err != nil {
			t.Fatal(err)
		}
		intent.Status = "SUCCEEDED"
		if err := put(state, intent, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := state.ReplicationDeleteIntents[intent.IntentID]; got.Status != "SUCCEEDED" {
			t.Fatalf("status=%q", got.Status)
		}
	})

	t.Run("rejects expired create at apply time", func(t *testing.T) {
		state := control.NewState()
		intent := valid()
		intent.ExpiresUnix = now.Unix()
		if err := put(state, intent, now); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("error=%v want INVALID", err)
		}
		if _, ok := state.ReplicationDeleteIntents[intent.IntentID]; ok {
			t.Fatal("expired intent stored")
		}
	})

	t.Run("rejects create applied after expiry", func(t *testing.T) {
		state := control.NewState()
		intent := valid()
		intent.ExpiresUnix = now.Add(time.Second).Unix()
		if err := put(state, intent, now.Add(2*time.Second)); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("error=%v want INVALID", err)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*control.ReplicationDeleteIntent)
	}{
		{name: "missing operation fields", mutate: func(intent *control.ReplicationDeleteIntent) { intent.IntentID = "" }},
		{name: "missing policy", mutate: func(intent *control.ReplicationDeleteIntent) { intent.PolicyID = "" }},
		{name: "zero policy revision", mutate: func(intent *control.ReplicationDeleteIntent) { intent.PolicyRevision = 0 }},
		{name: "missing source", mutate: func(intent *control.ReplicationDeleteIntent) { intent.SourceNodeID = "" }},
		{name: "missing target", mutate: func(intent *control.ReplicationDeleteIntent) { intent.TargetNodeID = "" }},
		{name: "missing snapshot", mutate: func(intent *control.ReplicationDeleteIntent) { intent.SnapshotID = "" }},
		{name: "zero leader term", mutate: func(intent *control.ReplicationDeleteIntent) { intent.LeaderTerm = 0 }},
		{name: "non-pending create", mutate: func(intent *control.ReplicationDeleteIntent) { intent.Status = "SUCCEEDED" }},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			state := control.NewState()
			intent := valid()
			tc.mutate(&intent)
			body := control.ReplicationDeleteIntentPutBody{OperationID: "op-bad", Intent: intent}
			if tc.name == "missing operation fields" {
				body.OperationID = ""
			}
			err := state.Apply(mustEncode(t, control.CmdReplicationDeleteIntentPut, body), now)
			if !errcode.Is(err, errcode.INVALID) {
				t.Fatalf("error=%v want INVALID", err)
			}
		})
	}

	t.Run("rejects identity mismatch on update", func(t *testing.T) {
		state := control.NewState()
		intent := valid()
		if err := put(state, intent, now); err != nil {
			t.Fatal(err)
		}
		intent.SnapshotID = "other-snapshot"
		intent.Status = "SUCCEEDED"
		if err := put(state, intent, now.Add(time.Second)); !errcode.Is(err, errcode.CONFLICT) {
			t.Fatalf("error=%v want CONFLICT", err)
		}
		if got := state.ReplicationDeleteIntents["intent-1"]; got.Status != "PENDING" || got.SnapshotID != "snapshot" {
			t.Fatalf("intent changed after rejected update: %+v", got)
		}
	})
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

func admittedReplicationState(t *testing.T) *control.State {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	for _, nodeID := range []string{"node-a", "node-b", "node-c"} {
		if err := s.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: nodeID, Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func admittedReplicationRoutes() []control.ReplicationRoute {
	return []control.ReplicationRoute{
		{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}},
		{SourceNodeID: "node-b", TargetNodeIDs: []string{"node-a"}},
		{SourceNodeID: "node-c", TargetNodeIDs: []string{"node-a"}},
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
	"replication.read", "replication.manage",
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
	"replication.read", "replication.manage",
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
