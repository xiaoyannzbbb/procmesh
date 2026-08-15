package auth_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func putViewer(t *testing.T, store *fakeStore) {
	t.Helper()
	apply(t, store, control.CmdUserPut, control.UserPutBody{
		ID: "user-view", Username: "viewer", PasswordHash: "x",
	})
	apply(t, store, control.CmdBindPut, control.BindPutBody{
		UserID: "user-view", RoleID: "viewer", Scope: control.ScopeCluster,
	})
}

func TestAllow_ViewerCannotRestart(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	putViewer(t, store)
	view := auth.Principal{UserID: "user-view", Username: "viewer"}
	if err := svc.Allow(view, auth.PermProcessRead, ""); err != nil {
		t.Fatal(err)
	}
	err := svc.Allow(view, auth.PermProcessRestart, "")
	requireCode(t, err, errcode.DENIED, "permission denied")

	admin := auth.Principal{UserID: "user-admin", Username: "admin"}
	if err := svc.Allow(admin, auth.PermProcessRestart, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAllowWrite_NoQuorumBlocksUserCreate(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	store.quorum = false
	admin := auth.Principal{UserID: "user-admin", Username: "admin"}
	err := svc.AllowWrite(admin, auth.PermUserCreate, "", true)
	requireCode(t, err, errcode.UNAVAILABLE, "control quorum lost")
	err = svc.AllowWrite(admin, auth.PermUserUpdate, "", true)
	requireCode(t, err, errcode.UNAVAILABLE, "control quorum lost")
	err = svc.AllowWrite(admin, auth.PermUserDelete, "", true)
	requireCode(t, err, errcode.UNAVAILABLE, "control quorum lost")
	err = svc.AllowWrite(admin, auth.PermRoleManage, "", true)
	requireCode(t, err, errcode.UNAVAILABLE, "control quorum lost")
	err = svc.AllowWrite(admin, auth.PermNodeRemove, "", true)
	requireCode(t, err, errcode.UNAVAILABLE, "control quorum lost")
	err = svc.AllowWrite(admin, auth.PermNodeManage, "", true)
	requireCode(t, err, errcode.UNAVAILABLE, "control quorum lost")
	err = svc.AllowWrite(admin, auth.PermClusterManage, "", true)
	requireCode(t, err, errcode.UNAVAILABLE, "control quorum lost")
}

func TestAllowWrite_NoQuorumAllowsProcessRead(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	store.quorum = false
	store.fresh = false
	admin := auth.Principal{UserID: "user-admin", Username: "admin"}
	if err := svc.AllowWrite(admin, auth.PermProcessRead, "", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.AllowWrite(admin, auth.PermClusterRead, "", false); err != nil {
		t.Fatal(err)
	}
}

func TestAllowWrite_StaleCacheBlocksRemoteMutation(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	store.quorum = false
	store.fresh = false
	admin := auth.Principal{UserID: "user-admin", Username: "admin"}
	err := svc.AllowWrite(admin, auth.PermProcessRestart, "node-a", false)
	requireCode(t, err, errcode.DENIED, "rbac cache expired")

	store.fresh = true
	if err := svc.AllowWrite(admin, auth.PermProcessRestart, "node-a", false); err != nil {
		t.Fatal(err)
	}

	store.fresh = false
	store.quorum = true
	if err := svc.AllowWrite(admin, auth.PermProcessCreate, "node-a", false); err != nil {
		t.Fatal(err)
	}
}

func TestAllowWrite_NoQuorumAllowsLocalProcessMutation(t *testing.T) {
	svc, store, _ := newTestSvc(t)
	store.quorum = false
	store.fresh = false
	admin := auth.Principal{UserID: "user-admin", Username: "admin"}
	if err := svc.AllowWrite(admin, auth.PermProcessRestart, "node-a", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.AllowWrite(admin, auth.PermProcessCreate, "node-a", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.AllowWrite(admin, auth.PermProcessUpdate, "", true); err != nil {
		t.Fatal(err)
	}
}

func TestAllow_MissingUserDenied(t *testing.T) {
	svc, _, _ := newTestSvc(t)
	err := svc.Allow(auth.Principal{UserID: "missing"}, auth.PermClusterRead, "")
	requireCode(t, err, errcode.DENIED, "permission denied")
}
