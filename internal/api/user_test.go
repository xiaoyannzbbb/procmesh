package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func TestUser_CreateListDisable(t *testing.T) {
	ctx := context.Background()
	stub := &UserAPI{}
	_, err := stub.ListUsers(ctx, connect.NewRequest(&procmeshv1.ListUsersRequest{}))
	assertUnavailableMsg(t, err, "auth not configured")

	_, svc := newBootstrappedAuth(t)
	api := &UserAPI{Auth: svc}
	ctx = WithPrincipal(ctx, auth.Principal{UserID: "user-admin", Username: "admin"})

	created, err := api.CreateUser(ctx, connect.NewRequest(&procmeshv1.CreateUserRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-user-create", Operator: "t"},
		Username:    "alice",
		Password:    "alice-pass-ok",
		DisplayName: "Alice",
		Email:       "alice@example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	u := created.Msg.GetUser()
	if u.GetUserId() == "" || u.GetUsername() != "alice" || u.GetStatus() != string(control.UserActive) {
		t.Fatalf("created %+v", u)
	}
	if u.GetDisplayName() != "Alice" || u.GetEmail() != "alice@example.com" {
		t.Fatalf("profile %+v", u)
	}

	list, err := api.ListUsers(ctx, connect.NewRequest(&procmeshv1.ListUsersRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if findUser(list.Msg.GetUsers(), "alice") == nil {
		t.Fatalf("list missing alice: %+v", list.Msg.GetUsers())
	}
	if findUser(list.Msg.GetUsers(), "admin") == nil {
		t.Fatalf("list missing admin: %+v", list.Msg.GetUsers())
	}

	_, err = api.CreateUser(ctx, connect.NewRequest(&procmeshv1.CreateUserRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-user-dup", Operator: "t"},
		Username: "alice",
		Password: "alice-pass-ok",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" {
		t.Fatalf("dup code=%v detail=%s err=%v", code, detail, err)
	}

	dis, err := api.DisableUser(ctx, connect.NewRequest(&procmeshv1.DisableUserRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-user-dis", Operator: "t"},
		UserId: u.GetUserId(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if dis.Msg.GetUser().GetStatus() != string(control.UserDisabled) {
		t.Fatalf("disabled %+v", dis.Msg.GetUser())
	}

	list, err = api.ListUsers(ctx, connect.NewRequest(&procmeshv1.ListUsersRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := findUser(list.Msg.GetUsers(), "alice")
	if got == nil || got.GetStatus() != string(control.UserDisabled) {
		t.Fatalf("list after disable %+v", got)
	}

	enabled, err := api.EnableUser(ctx, connect.NewRequest(&procmeshv1.EnableUserRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-user-enable", Operator: "spoofed"},
		UserId: u.GetUserId(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Msg.GetUser().GetStatus() != string(control.UserActive) {
		t.Fatalf("enabled %+v", enabled.Msg.GetUser())
	}

	_, err = api.DisableUser(ctx, connect.NewRequest(&procmeshv1.DisableUserRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-user-missing", Operator: "t"},
		UserId: "missing-user",
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("missing code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestUser_DisableRejectsCurrentUser(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	api := &UserAPI{Auth: svc}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})

	_, err := api.DisableUser(ctx, connect.NewRequest(&procmeshv1.DisableUserRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-user-self-disable", Operator: "spoofed"},
		UserId: "user-admin",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" || !strings.Contains(err.Error(), "current user") {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestUser_DisableRejectsLastActiveSuperAdmin(t *testing.T) {
	store, svc := newBootstrappedAuth(t)
	for _, cmd := range []control.Command{
		mustAPICommand(t, control.CmdUserPut, control.UserPutBody{ID: "user-delegate", Username: "delegate", PasswordHash: "hash"}),
		mustAPICommand(t, control.CmdBindPut, control.BindPutBody{UserID: "user-delegate", RoleID: "cluster_admin", Scope: control.ScopeCluster}),
	} {
		if err := store.Apply(cmd, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	api := &UserAPI{Auth: svc}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-delegate", Username: "delegate"})

	_, err := api.DisableUser(ctx, connect.NewRequest(&procmeshv1.DisableUserRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-user-last-admin", Operator: "spoofed"},
		UserId: "user-admin",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" || !strings.Contains(err.Error(), "last active super admin") {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if got := store.View().Users["admin"].Status; got != control.UserActive {
		t.Fatalf("admin status=%s", got)
	}
}

func mustAPICommand(t *testing.T, typ string, body any) control.Command {
	t.Helper()
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestUser_ShortPassword(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	api := &UserAPI{Auth: svc}
	_, err := api.CreateUser(context.Background(), connect.NewRequest(&procmeshv1.CreateUserRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-short", Operator: "t"},
		Username: "bob",
		Password: "short",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "password too short") {
		t.Fatalf("want password too short: %v", err)
	}
}

func findUser(users []*procmeshv1.User, name string) *procmeshv1.User {
	for _, u := range users {
		if u.GetUsername() == name {
			return u
		}
	}
	return nil
}

func assertUnavailableMsg(t *testing.T, err error, msg string) {
	t.Helper()
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), msg) {
		t.Fatalf("want %q: %v", msg, err)
	}
}
