package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runNode(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return nodeList(c, stdout)
	case "status":
		id, err := oneArg(pos, "node status")
		if err != nil {
			return err
		}
		return nodeStatus(c, id, stdout)
	case "token":
		if len(pos) == 0 {
			return usageError("missing node token subcommand")
		}
		return runNodeToken(c, pos[0], pos[1:], opt, stdout)
	default:
		return usageError("unknown node command")
	}
}

func runNodeToken(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "create":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return nodeTokenCreate(c, opt, stdout)
	case "revoke":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("node token revoke requires TOKEN_ID")
		}
		return nodeTokenRevoke(c, pos[0])
	default:
		return usageError("unknown node token command")
	}
}

func nodeList(c *client, stdout io.Writer) error {
	resp, err := c.node.ListNodes(context.Background(), connect.NewRequest(&procmeshv1.ListNodesRequest{}))
	if err != nil {
		return err
	}
	for _, n := range resp.Msg.GetNodes() {
		fmt.Fprintln(stdout, formatNodeLine(n))
	}
	return nil
}

func nodeStatus(c *client, id string, stdout io.Writer) error {
	resp, err := c.node.GetNode(context.Background(), connect.NewRequest(&procmeshv1.GetNodeRequest{
		IdOrHostname: id,
	}))
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, formatNodeLine(resp.Msg.GetNode()))
	return nil
}

func nodeTokenCreate(c *client, opt options, stdout io.Writer) error {
	resp, err := c.node.CreateJoinToken(context.Background(), connect.NewRequest(&procmeshv1.CreateJoinTokenRequest{
		Meta:       c.meta(),
		TtlSeconds: int64(opt.ttl / time.Second),
		Uses:       opt.uses,
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "token_id=%s\n", resp.Msg.GetTokenId())
	fmt.Fprintf(stdout, "token=%s\n", resp.Msg.GetToken())
	fmt.Fprintf(stdout, "expires=%d\n", resp.Msg.GetExpiresUnix())
	fmt.Fprintf(stdout, "uses=%d\n", resp.Msg.GetUses())
	return nil
}

func nodeTokenRevoke(c *client, tokenID string) error {
	_, err := c.node.RevokeJoinToken(context.Background(), connect.NewRequest(&procmeshv1.RevokeJoinTokenRequest{
		Meta:    c.meta(),
		TokenId: tokenID,
	}))
	return err
}

func formatNodeLine(n *procmeshv1.Node) string {
	if n == nil {
		return ""
	}
	return fmt.Sprintf("%s\t%s\t%s\t%d\t%s\t%s",
		n.GetNodeId(), n.GetHostname(), n.GetState(), n.GetProtocolVersion(),
		n.GetApiAddress(), n.GetGossipAddress())
}
