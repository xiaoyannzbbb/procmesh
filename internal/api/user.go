package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.UserServiceHandler = (*UserAPI)(nil)

const authApplyTimeout = 5 * time.Second

type UserAPI struct {
	Auth *auth.Service
}

func (s *UserAPI) ListUsers(ctx context.Context, _ *connect.Request[procmeshv1.ListUsersRequest]) (*connect.Response[procmeshv1.ListUsersResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermUserRead, "", false); err != nil {
		return nil, err
	}
	st := s.Auth.Store.View()
	names := make([]string, 0, len(st.Users))
	for name := range st.Users {
		names = append(names, name)
	}
	sort.Strings(names)
	out := &procmeshv1.ListUsersResponse{Users: make([]*procmeshv1.User, 0, len(names))}
	for _, name := range names {
		out.Users = append(out.Users, userToProto(st.Users[name]))
	}
	return connect.NewResponse(out), nil
}

func (s *UserAPI) CreateUser(ctx context.Context, req *connect.Request[procmeshv1.CreateUserRequest]) (*connect.Response[procmeshv1.CreateUserResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermUserCreate, "", true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	username := req.Msg.GetUsername()
	if username == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "username required"))
	}
	if err := auth.ValidPassword(req.Msg.GetPassword()); err != nil {
		return nil, ToConnect(err)
	}
	hash, err := control.HashPassword(req.Msg.GetPassword())
	if err != nil {
		return nil, ToConnect(err)
	}
	id, err := newAuthID()
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := applyAuth(s.Auth, control.CmdUserPut, control.UserPutBody{
		ID:           id,
		Username:     username,
		PasswordHash: hash,
		DisplayName:  req.Msg.GetDisplayName(),
		Email:        req.Msg.GetEmail(),
	}); err != nil {
		return nil, err
	}
	u, ok := s.Auth.Store.View().Users[username]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "user not found after create"))
	}
	return connect.NewResponse(&procmeshv1.CreateUserResponse{User: userToProto(u)}), nil
}

func (s *UserAPI) DisableUser(ctx context.Context, req *connect.Request[procmeshv1.DisableUserRequest]) (*connect.Response[procmeshv1.DisableUserResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermUserUpdate, "", true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	id := req.Msg.GetUserId()
	if id == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "user_id required"))
	}
	if err := applyAuth(s.Auth, control.CmdUserDisable, control.UserDisableBody{UserID: id}); err != nil {
		return nil, err
	}
	u, ok := userFromState(s.Auth.Store.View(), id)
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "user not found"))
	}
	return connect.NewResponse(&procmeshv1.DisableUserResponse{User: userToProto(u)}), nil
}

func requireAuthConfigured(svc *auth.Service) error {
	if svc == nil {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "auth not configured"))
	}
	return nil
}

func applyAuth(svc *auth.Service, typ string, body any) error {
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		return ToConnect(err)
	}
	if err := svc.Store.Apply(cmd, authApplyTimeout); err != nil {
		return ToConnect(err)
	}
	return nil
}

func userToProto(u control.User) *procmeshv1.User {
	return &procmeshv1.User{
		UserId:        u.ID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		Email:         u.Email,
		Status:        string(u.Status),
		CreatedUnix:   u.CreatedUnix,
		LastLoginUnix: u.LastLoginUnix,
	}
}

func userFromState(st control.State, id string) (control.User, bool) {
	name, ok := st.UsersByID[id]
	if !ok {
		return control.User{}, false
	}
	u, ok := st.Users[name]
	return u, ok
}

func newAuthID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
