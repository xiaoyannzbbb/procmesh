package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runGroup(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return groupList(c, stdout)
	case "create":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return groupCreate(c, opt, stdout)
	case "delete":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("group delete requires GROUP_ID")
		}
		return groupDelete(c, pos[0], stdout)
	case "add-member":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return groupAddMember(c, opt, stdout)
	case "remove-member":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return groupRemoveMember(c, opt, stdout)
	default:
		return usageError("unknown group command")
	}
}

func groupList(c *client, stdout io.Writer) error {
	resp, err := c.group.ListAgentGroups(context.Background(), connect.NewRequest(&procmeshv1.ListAgentGroupsRequest{}))
	if err != nil {
		return err
	}
	for _, g := range resp.Msg.GetGroups() {
		printGroup(stdout, g)
	}
	return nil
}

func groupCreate(c *client, opt options, stdout io.Writer) error {
	if opt.name == "" {
		return usageError("group create requires --name")
	}
	resp, err := c.group.CreateAgentGroup(context.Background(), connect.NewRequest(&procmeshv1.CreateAgentGroupRequest{
		Meta:        c.meta(),
		Name:        opt.name,
		Description: opt.description,
	}))
	if err != nil {
		return err
	}
	printGroup(stdout, resp.Msg.GetGroup())
	return nil
}

func groupDelete(c *client, groupID string, stdout io.Writer) error {
	_, err := c.group.DeleteAgentGroup(context.Background(), connect.NewRequest(&procmeshv1.DeleteAgentGroupRequest{
		Meta:    c.meta(),
		GroupId: groupID,
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "group_id=%s\n", groupID)
	return nil
}

func groupAddMember(c *client, opt options, stdout io.Writer) error {
	if opt.groupID == "" || opt.nodeID == "" {
		return usageError("group add-member requires --group-id and --node-id")
	}
	resp, err := c.group.AddAgentGroupMember(context.Background(), connect.NewRequest(&procmeshv1.AgentGroupMemberRequest{
		Meta:    c.meta(),
		GroupId: opt.groupID,
		NodeId:  opt.nodeID,
	}))
	if err != nil {
		return err
	}
	printGroup(stdout, resp.Msg.GetGroup())
	return nil
}

func groupRemoveMember(c *client, opt options, stdout io.Writer) error {
	if opt.groupID == "" || opt.nodeID == "" {
		return usageError("group remove-member requires --group-id and --node-id")
	}
	resp, err := c.group.RemoveAgentGroupMember(context.Background(), connect.NewRequest(&procmeshv1.AgentGroupMemberRequest{
		Meta:    c.meta(),
		GroupId: opt.groupID,
		NodeId:  opt.nodeID,
	}))
	if err != nil {
		return err
	}
	printGroup(stdout, resp.Msg.GetGroup())
	return nil
}

func printGroup(stdout io.Writer, g *procmeshv1.AgentGroup) {
	if g == nil {
		return
	}
	fmt.Fprintf(stdout, "group_id=%s\n", g.GetGroupId())
	fmt.Fprintf(stdout, "name=%s\n", g.GetName())
	fmt.Fprintf(stdout, "members=%s\n", strings.Join(g.GetMemberNodeIds(), ","))
}
