package cli

import (
	"context"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/control"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runCluster(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "init":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return clusterInit(c, opt, stdout)
	case "membership":
		return runClusterMembership(c, pos, stdout)
	default:
		return usageError("unknown cluster command")
	}
}

func runClusterMembership(c *client, pos []string, stdout io.Writer) error {
	if len(pos) == 0 {
		return usageError("missing cluster membership subcommand")
	}
	if len(pos) != 1 {
		return usageError("unexpected arguments")
	}
	var (
		report   *procmeshv1.MembershipReport
		repaired *int32
	)
	switch pos[0] {
	case "check":
		resp, err := c.node.CheckMembership(context.Background(), connect.NewRequest(&procmeshv1.CheckMembershipRequest{}))
		if err != nil {
			return err
		}
		report = resp.Msg.GetReport()
	case "reconcile":
		resp, err := c.node.ReconcileMembership(context.Background(), connect.NewRequest(&procmeshv1.ReconcileMembershipRequest{Meta: c.meta()}))
		if err != nil {
			return err
		}
		report = resp.Msg.GetReport()
		value := resp.Msg.GetRepaired()
		repaired = &value
	default:
		return usageError("unknown cluster membership command")
	}
	if report == nil {
		return fmt.Errorf("membership report missing")
	}
	printMembershipReport(stdout, report, repaired)
	if report.GetStatus() != string(control.MembershipClean) {
		return fmt.Errorf("membership status %s", report.GetStatus())
	}
	return nil
}

func printMembershipReport(stdout io.Writer, report *procmeshv1.MembershipReport, repaired *int32) {
	fmt.Fprintf(stdout, "status=%s\n", report.GetStatus())
	fmt.Fprintf(stdout, "freshness=%s\n", report.GetFreshness())
	fmt.Fprintf(stdout, "has_quorum=%t\n", report.GetHasQuorum())
	fmt.Fprintf(stdout, "leader=%t\n", report.GetLeader())
	fmt.Fprintf(stdout, "observed_unix_ms=%d\n", report.GetObservedUnixMs())
	if repaired != nil {
		fmt.Fprintf(stdout, "repaired=%d\n", *repaired)
	}
	fmt.Fprintf(stdout, "issues=%d\n", len(report.GetIssues()))
	for _, issue := range report.GetIssues() {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%t\n",
			membershipValue(issue.GetNodeId()),
			membershipValue(issue.GetKind()),
			membershipValue(issue.GetMemberState()),
			membershipValue(issue.GetActualRole()),
			issue.GetRepairable(),
		)
	}
}

func membershipValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
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
