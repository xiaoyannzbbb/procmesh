package api

import (
	"context"
	"testing"

	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestGroupAPI_CRUDAndRBAC(t *testing.T) {
	e := newRBACEnv(t)
	applyAuthCmd(t, e.svc, control.CmdMemberPut, control.MemberPutBody{NodeID: "node-1"})
	adminSid := e.loginAs(t, "admin", testAdminPass)
	gcli := procmeshv1connect.NewGroupServiceClient(e.http, e.url)
	ctx := context.Background()

	created, err := gcli.CreateAgentGroup(ctx, bearerReq(adminSid, &procmeshv1.CreateAgentGroupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-cg", Operator: "t"},
		Name: "finance",
	}))
	if err != nil {
		t.Fatal(err)
	}
	gid := created.Msg.GetGroup().GetGroupId()
	if gid == "" || created.Msg.GetGroup().GetName() != "finance" {
		t.Fatalf("%+v", created.Msg.GetGroup())
	}

	_, err = gcli.AddAgentGroupMember(ctx, bearerReq(adminSid, &procmeshv1.AgentGroupMemberRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-add", Operator: "t"},
		GroupId: gid, NodeId: "node-1",
	}))
	if err != nil {
		t.Fatal(err)
	}

	list, err := gcli.ListAgentGroups(ctx, bearerReq(adminSid, &procmeshv1.ListAgentGroupsRequest{}))
	if err != nil || len(list.Msg.GetGroups()) != 1 {
		t.Fatalf("list %+v err=%v", list, err)
	}
	if len(list.Msg.GetGroups()[0].GetMemberNodeIds()) != 1 {
		t.Fatal("member missing")
	}

	viewSid := e.loginAs(t, "viewer", testAdminPass)
	_, err = gcli.CreateAgentGroup(ctx, bearerReq(viewSid, &procmeshv1.CreateAgentGroupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-den", Operator: "v"},
		Name: "other",
	}))
	if err == nil {
		t.Fatal("viewer must be denied")
	}
}
