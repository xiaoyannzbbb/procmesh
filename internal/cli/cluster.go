package cli

import (
	"context"
	"fmt"
	"io"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runCluster(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "init":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return clusterInit(c, opt, stdout)
	default:
		return usageError("unknown cluster command")
	}
}

func clusterInit(c *client, opt options, stdout io.Writer) error {
	resp, err := c.cluster.Init(context.Background(), connect.NewRequest(&procmeshv1.InitClusterRequest{
		Meta:          c.meta(),
		AdminUsername: opt.adminUser,
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "cluster_id=%s\n", resp.Msg.GetClusterId())
	fmt.Fprintf(stdout, "node_id=%s\n", resp.Msg.GetNodeId())
	fmt.Fprintf(stdout, "admin_user=%s\n", resp.Msg.GetAdminUsername())
	fmt.Fprintf(stdout, "admin_password=%s\n", resp.Msg.GetAdminPassword())
	return nil
}

func runAgent(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "join":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return agentJoin(c, opt, stdout)
	default:
		return usageError("unknown agent command")
	}
}

func agentJoin(c *client, opt options, stdout io.Writer) error {
	if opt.seed == "" || opt.token == "" {
		return usageError("agent join requires --seed and --token")
	}
	resp, err := c.cluster.RequestJoin(context.Background(), connect.NewRequest(&procmeshv1.RequestJoinRequest{
		Meta:       c.meta(),
		SeedServer: opt.seed,
		Token:      opt.token,
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "cluster_id=%s\n", resp.Msg.GetClusterId())
	fmt.Fprintf(stdout, "gossip=%s\n", resp.Msg.GetGossipAddress())
	return nil
}
