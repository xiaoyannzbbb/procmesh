package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runMetrics(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "history":
		if len(pos) == 0 {
			return usageError("missing metrics history subcommand")
		}
		return runMetricsHistory(c, pos[0], pos[1:], opt, stdout)
	default:
		return usageError("unknown metrics command")
	}
}

func runMetricsHistory(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "node":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("metrics history node requires NODE_ID")
		}
		return metricsHistoryNode(c, pos[0], opt, stdout)
	case "process":
		id, err := oneArg(pos, "metrics history process")
		if err != nil {
			return err
		}
		return metricsHistoryProcess(c, id, opt, stdout)
	default:
		return usageError("unknown metrics history command")
	}
}

func metricsHistoryNode(c *client, nodeID string, opt options, stdout io.Writer) error {
	resp, err := c.metrics.GetNodeHistory(context.Background(), connect.NewRequest(&procmeshv1.GetNodeHistoryRequest{
		NodeId:    nodeID,
		SinceUnix: opt.sinceUnix,
		UntilUnix: opt.untilUnix,
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "node_id=%s layer=%s\n", resp.Msg.GetNodeId(), resp.Msg.GetLayer())
	writeHistorySeries(stdout, resp.Msg.GetSeries())
	return nil
}

func metricsHistoryProcess(c *client, idOrName string, opt options, stdout io.Writer) error {
	resp, err := c.metrics.GetProcessHistory(context.Background(), connect.NewRequest(&procmeshv1.GetProcessHistoryRequest{
		IdOrName:  idOrName,
		SinceUnix: opt.sinceUnix,
		UntilUnix: opt.untilUnix,
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "process_id=%s layer=%s\n", resp.Msg.GetProcessId(), resp.Msg.GetLayer())
	writeHistorySeries(stdout, resp.Msg.GetSeries())
	return nil
}

func writeHistorySeries(stdout io.Writer, series []*procmeshv1.MetricSeries) {
	for _, s := range series {
		fmt.Fprintf(stdout, "series=%s\n", s.GetName())
		for _, p := range s.GetPoints() {
			fmt.Fprintf(stdout, "ts=%d value=%s\n", p.GetTsUnix(), strconv.FormatFloat(p.GetValue(), 'f', -1, 64))
		}
	}
}
