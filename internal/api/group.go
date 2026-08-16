package api

import (
	"context"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.GroupServiceHandler = (*GroupAPI)(nil)

type GroupAPI struct{ Auth *auth.Service }

func (s *GroupAPI) ListAgentGroups(ctx context.Context, _ *connect.Request[procmeshv1.ListAgentGroupsRequest]) (*connect.Response[procmeshv1.ListAgentGroupsResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermNodeRead, "", false, true); err != nil {
		return nil, err
	}
	st := s.Auth.Store().View()
	ids := make([]string, 0, len(st.AgentGroups))
	for id := range st.AgentGroups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := &procmeshv1.ListAgentGroupsResponse{}
	for _, id := range ids {
		out.Groups = append(out.Groups, agentGroupToProto(st.AgentGroups[id]))
	}
	return connect.NewResponse(out), nil
}

func (s *GroupAPI) CreateAgentGroup(ctx context.Context, req *connect.Request[procmeshv1.CreateAgentGroupRequest]) (*connect.Response[procmeshv1.CreateAgentGroupResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	id, err := newAuthID()
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := applyAuth(s.Auth, control.CmdGroupPut, control.GroupPutBody{
		GroupID: id, Name: req.Msg.GetName(), Description: req.Msg.GetDescription(),
		NowUnix: time.Now().Unix(),
	}); err != nil {
		return nil, err
	}
	g := s.Auth.Store().View().AgentGroups[id]
	return connect.NewResponse(&procmeshv1.CreateAgentGroupResponse{Group: agentGroupToProto(g)}), nil
}

func (s *GroupAPI) DeleteAgentGroup(ctx context.Context, req *connect.Request[procmeshv1.DeleteAgentGroupRequest]) (*connect.Response[procmeshv1.DeleteAgentGroupResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := applyAuth(s.Auth, control.CmdGroupDelete, control.GroupDeleteBody{
		GroupID: req.Msg.GetGroupId(),
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.DeleteAgentGroupResponse{}), nil
}

func (s *GroupAPI) AddAgentGroupMember(ctx context.Context, req *connect.Request[procmeshv1.AgentGroupMemberRequest]) (*connect.Response[procmeshv1.AgentGroupMemberResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := applyAuth(s.Auth, control.CmdGroupMemberAdd, control.GroupMemberBody{
		GroupID: req.Msg.GetGroupId(), NodeID: req.Msg.GetNodeId(),
	}); err != nil {
		return nil, err
	}
	g := s.Auth.Store().View().AgentGroups[req.Msg.GetGroupId()]
	return connect.NewResponse(&procmeshv1.AgentGroupMemberResponse{Group: agentGroupToProto(g)}), nil
}

func (s *GroupAPI) RemoveAgentGroupMember(ctx context.Context, req *connect.Request[procmeshv1.AgentGroupMemberRequest]) (*connect.Response[procmeshv1.AgentGroupMemberResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := applyAuth(s.Auth, control.CmdGroupMemberRemove, control.GroupMemberBody{
		GroupID: req.Msg.GetGroupId(), NodeID: req.Msg.GetNodeId(),
	}); err != nil {
		return nil, err
	}
	g := s.Auth.Store().View().AgentGroups[req.Msg.GetGroupId()]
	return connect.NewResponse(&procmeshv1.AgentGroupMemberResponse{Group: agentGroupToProto(g)}), nil
}

func agentGroupToProto(g control.AgentGroup) *procmeshv1.AgentGroup {
	return &procmeshv1.AgentGroup{
		GroupId:       g.GroupID,
		Name:          g.Name,
		Description:   g.Description,
		MemberNodeIds: append([]string(nil), g.MemberIDs...),
		CreatedUnix:   g.CreatedUnix,
		UpdatedUnix:   g.UpdatedUnix,
	}
}
