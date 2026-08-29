package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
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

func TestUser_CreateOnFollowerForwardsToLeader(t *testing.T) {
	store, svc := newBootstrappedAuth(t)
	store.applyErr = errcode.E(errcode.UNAVAILABLE, "not raft leader")
	client := &recordingUserClient{}
	forwarder := &recordingUserForwarder{client: client}
	api := &UserAPI{
		Auth: svc, LocalID: "node-follower", IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{NodeID: "node-leader", RPC: "127.0.0.1:18685"}, nil
		},
		Forward: forwarder,
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{
		UserID: "user-admin", Username: "admin", SessionID: "session-admin",
	})

	req := connect.NewRequest(&procmeshv1.CreateUserRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-user-create-forward", Operator: "admin"},
		Username: "alice",
		Password: "alice-pass-ok",
	})
	req.Header().Set("Authorization", "Bearer must-not-forward")
	resp, err := api.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("CreateUser through follower: %v", err)
	}
	if resp.Msg.GetUser().GetUsername() != "alice" {
		t.Fatalf("response user = %+v", resp.Msg.GetUser())
	}
	if forwarder.calls != 1 || forwarder.route.NodeID != "node-leader" {
		t.Fatalf("forward calls=%d route=%+v", forwarder.calls, forwarder.route)
	}
	if client.createCalls != 1 || client.lastCreate.Msg.GetMeta().GetOperationId() != "op-user-create-forward" {
		t.Fatalf("forwarded request calls=%d request=%+v", client.createCalls, client.lastCreate)
	}
	h := client.lastCreate.Header()
	if rpc.SourceOf(h) != "node-follower" || rpc.TargetOf(h) != "node-leader" || rpc.UserIDOf(h) != "user-admin" || rpc.SessionIDOf(h) != "session-admin" {
		t.Fatalf("forwarded headers=%v", h)
	}
	if h.Get("Authorization") != "" {
		t.Fatal("forwarded browser authorization header")
	}
	if _, ok := store.View().Users["alice"]; ok {
		t.Fatal("follower applied user locally")
	}
}

func TestUser_CreateOnFollowerWithoutLeaderIsUnavailable(t *testing.T) {
	store, svc := newBootstrappedAuth(t)
	store.applyErr = errcode.E(errcode.UNAVAILABLE, "not raft leader")
	api := &UserAPI{Auth: svc, IsLeader: func() bool { return false }}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})

	_, err := api.CreateUser(ctx, connect.NewRequest(&procmeshv1.CreateUserRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-user-create-no-leader", Operator: "admin"},
		Username: "alice",
		Password: "alice-pass-ok",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	if strings.Contains(err.Error(), "not raft leader") {
		t.Fatalf("leaked local raft error: %v", err)
	}
}

func TestUser_CreateOnFollowerDialFailureIsRedacted(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	client := &recordingUserClient{createErr: connect.NewError(
		connect.CodeUnavailable, errors.New("dial tcp 10.0.0.9:18685: connection refused"),
	)}
	forwarder := &recordingUserForwarder{client: client}
	api := &UserAPI{
		Auth: svc, LocalID: "node-follower", IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{NodeID: "node-leader", RPC: "10.0.0.9:18685"}, nil
		},
		Forward: forwarder,
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})

	_, err := api.CreateUser(ctx, connect.NewRequest(&procmeshv1.CreateUserRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-user-create-dial-fail", Operator: "admin"},
		Username: "alice",
		Password: "alice-pass-ok",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "control leader unavailable") || strings.Contains(err.Error(), "10.0.0.9") {
		t.Fatalf("unredacted dial failure: %v", err)
	}
}

func TestUser_UpdateOnFollowerForwardsToLeader(t *testing.T) {
	store, svc := newBootstrappedAuth(t)
	store.applyErr = errcode.E(errcode.UNAVAILABLE, "not raft leader")
	client := &recordingUserClient{}
	forwarder := &recordingUserForwarder{client: client}
	api := &UserAPI{
		Auth: svc, LocalID: "node-follower", IsLeader: func() bool { return false },
		LeaderRoute: func() (Route, error) {
			return Route{NodeID: "node-leader", RPC: "127.0.0.1:18685"}, nil
		},
		Forward: forwarder,
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})

	if _, err := api.DisableUser(ctx, connect.NewRequest(&procmeshv1.DisableUserRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-user-disable-forward", Operator: "admin"}, UserId: "user-alice",
	})); err != nil {
		t.Fatalf("DisableUser through follower: %v", err)
	}
	if _, err := api.EnableUser(ctx, connect.NewRequest(&procmeshv1.EnableUserRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-user-enable-forward", Operator: "admin"}, UserId: "user-alice",
	})); err != nil {
		t.Fatalf("EnableUser through follower: %v", err)
	}
	if forwarder.calls != 2 || client.disableCalls != 1 || client.enableCalls != 1 {
		t.Fatalf("forward calls=%d disable=%d enable=%d", forwarder.calls, client.disableCalls, client.enableCalls)
	}
}

type recordingUserForwarder struct {
	client procmeshv1connect.UserServiceClient
	calls  int
	route  Route
	err    error
}

func (f *recordingUserForwarder) User(_ context.Context, route Route) (procmeshv1connect.UserServiceClient, error) {
	f.calls++
	f.route = route
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
}

type recordingUserClient struct {
	createCalls  int
	disableCalls int
	enableCalls  int
	lastCreate   *connect.Request[procmeshv1.CreateUserRequest]
	createErr    error
}

func (c *recordingUserClient) ListUsers(context.Context, *connect.Request[procmeshv1.ListUsersRequest]) (*connect.Response[procmeshv1.ListUsersResponse], error) {
	return connect.NewResponse(&procmeshv1.ListUsersResponse{}), nil
}

func (c *recordingUserClient) CreateUser(_ context.Context, req *connect.Request[procmeshv1.CreateUserRequest]) (*connect.Response[procmeshv1.CreateUserResponse], error) {
	c.createCalls++
	c.lastCreate = req
	if c.createErr != nil {
		return nil, c.createErr
	}
	return connect.NewResponse(&procmeshv1.CreateUserResponse{User: &procmeshv1.User{
		UserId: "user-alice", Username: req.Msg.GetUsername(), Status: string(control.UserActive),
	}}), nil
}

func (c *recordingUserClient) DisableUser(context.Context, *connect.Request[procmeshv1.DisableUserRequest]) (*connect.Response[procmeshv1.DisableUserResponse], error) {
	c.disableCalls++
	return connect.NewResponse(&procmeshv1.DisableUserResponse{User: &procmeshv1.User{
		UserId: "user-alice", Username: "alice", Status: string(control.UserDisabled),
	}}), nil
}

func (c *recordingUserClient) EnableUser(context.Context, *connect.Request[procmeshv1.EnableUserRequest]) (*connect.Response[procmeshv1.EnableUserResponse], error) {
	c.enableCalls++
	return connect.NewResponse(&procmeshv1.EnableUserResponse{User: &procmeshv1.User{
		UserId: "user-alice", Username: "alice", Status: string(control.UserActive),
	}}), nil
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
