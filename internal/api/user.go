package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.UserServiceHandler = (*UserAPI)(nil)

const authApplyTimeout = 5 * time.Second

type UserForwarder interface {
	User(context.Context, Route) (procmeshv1connect.UserServiceClient, error)
}

type UserAPI struct {
	Auth        *auth.Service
	LocalOnly   bool
	LocalID     string
	IsLeader    func() bool
	LeaderRoute func() (Route, error)
	Forward     UserForwarder
}

func (s *UserAPI) ListUsers(ctx context.Context, _ *connect.Request[procmeshv1.ListUsersRequest]) (*connect.Response[procmeshv1.ListUsersResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermUserRead, "", false, true); err != nil {
		return nil, err
	}
	st := s.Auth.Store().View()
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
	if err := requirePerm(ctx, s.Auth, auth.PermUserCreate, "", true, true); err != nil {
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
	if local, cli, err := s.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		resp, err := cli.CreateUser(ctx, req)
		if err != nil {
			return nil, mapUserForwardErr(err)
		}
		return resp, nil
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
	u, ok := s.Auth.Store().View().Users[username]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "user not found after create"))
	}
	return connect.NewResponse(&procmeshv1.CreateUserResponse{User: userToProto(u)}), nil
}

func (s *UserAPI) DisableUser(ctx context.Context, req *connect.Request[procmeshv1.DisableUserRequest]) (*connect.Response[procmeshv1.DisableUserResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermUserUpdate, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	id := req.Msg.GetUserId()
	if id == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "user_id required"))
	}
	p, ok := PrincipalFrom(ctx)
	if !ok || p.UserID == "" {
		return nil, ToConnect(errcode.E(errcode.DENIED, "authentication required"))
	}
	if local, cli, err := s.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		resp, err := cli.DisableUser(ctx, req)
		if err != nil {
			return nil, mapUserForwardErr(err)
		}
		return resp, nil
	}
	if err := applyAuth(s.Auth, control.CmdUserDisableGuarded, control.UserDisableGuardedBody{
		UserID: id, ActorUserID: p.UserID,
	}); err != nil {
		return nil, err
	}
	u, ok := userFromState(s.Auth.Store().View(), id)
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "user not found"))
	}
	return connect.NewResponse(&procmeshv1.DisableUserResponse{User: userToProto(u)}), nil
}

func (s *UserAPI) EnableUser(ctx context.Context, req *connect.Request[procmeshv1.EnableUserRequest]) (*connect.Response[procmeshv1.EnableUserResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermUserUpdate, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	id := req.Msg.GetUserId()
	if id == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "user_id required"))
	}
	if local, cli, err := s.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		resp, err := cli.EnableUser(ctx, req)
		if err != nil {
			return nil, mapUserForwardErr(err)
		}
		return resp, nil
	}
	if err := applyAuth(s.Auth, control.CmdUserEnable, control.UserEnableBody{UserID: id}); err != nil {
		return nil, err
	}
	u, ok := userFromState(s.Auth.Store().View(), id)
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "user not found"))
	}
	return connect.NewResponse(&procmeshv1.EnableUserResponse{User: userToProto(u)}), nil
}

func (s *UserAPI) forwardMutation(ctx context.Context, header http.Header) (bool, procmeshv1connect.UserServiceClient, error) {
	if s.LocalOnly || s.IsLeader == nil || s.IsLeader() {
		return true, nil, nil
	}
	if s.Forward == nil || s.LeaderRoute == nil {
		return false, nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control leader unavailable"))
	}
	rt, err := s.LeaderRoute()
	if err != nil || rt.NodeID == "" {
		return false, nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control leader unavailable"))
	}
	if rt.Local {
		return true, nil, nil
	}
	if rt.RPC == "" {
		return false, nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control leader unavailable"))
	}
	stampHop(header, s.LocalID, rt.NodeID)
	stampIdentity(header, ctx)
	cli, err := s.Forward.User(ctx, rt)
	if err != nil {
		return false, nil, ToConnect(errcode.Wrap(errcode.UNAVAILABLE, "control leader unavailable", rpc.MapDialError(err)))
	}
	return false, cli, nil
}

func mapUserForwardErr(err error) error {
	mapped := rpc.MapCallError(err)
	if CodeOf(mapped) == errcode.UNAVAILABLE || connect.CodeOf(mapped) == connect.CodeUnavailable {
		return ToConnect(errcode.Wrap(errcode.UNAVAILABLE, "control leader unavailable", mapped))
	}
	return ToConnect(mapped)
}

func requireAuthConfigured(svc *auth.Service) error {
	if svc == nil {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "auth not configured"))
	}
	if svc.Store() == nil {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "auth store not ready"))
	}
	return nil
}

func applyAuth(svc *auth.Service, typ string, body any) error {
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		return ToConnect(err)
	}
	if err := svc.Store().Apply(cmd, authApplyTimeout); err != nil {
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
