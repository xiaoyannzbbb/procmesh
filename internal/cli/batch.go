package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runBatch(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "create":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return batchCreate(c, opt, stdout)
	case "get":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("batch get requires BATCH_ID")
		}
		return batchGet(c, pos[0], stdout)
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return batchList(c, stdout)
	case "retry":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("batch retry requires BATCH_ID")
		}
		return batchRetry(c, pos[0], stdout)
	case "replay-timeout":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("batch replay-timeout requires BATCH_ID")
		}
		return batchReplayTimeout(c, pos[0], stdout)
	case "export":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("batch export requires BATCH_ID")
		}
		return batchExport(c, pos[0], opt, stdout)
	default:
		return usageError("unknown batch command")
	}
}

func batchCreate(c *client, opt options, stdout io.Writer) error {
	typ, err := mapBatchType(opt.batchType)
	if err != nil {
		return err
	}
	sel, err := buildBatchSelector(opt)
	if err != nil {
		return err
	}
	req := &procmeshv1.CreateBatchRequest{
		Meta:     c.meta(),
		Type:     typ,
		Selector: sel,
		Comment:  opt.comment,
	}
	if typ == "CONFIG_UPDATE" {
		if opt.file == "" {
			return usageError("batch create --type apply requires --file")
		}
		spec, err := Load(opt.file)
		if err != nil {
			return err
		}
		req.Config = spec
	} else if opt.file != "" {
		return usageError("batch create --file is only valid with --type apply")
	}
	resp, err := c.batch.CreateBatch(context.Background(), connect.NewRequest(req))
	if err != nil {
		return err
	}
	printBatch(stdout, resp.Msg.GetBatch(), false)
	return nil
}

func batchGet(c *client, batchID string, stdout io.Writer) error {
	resp, err := c.batch.GetBatch(context.Background(), connect.NewRequest(&procmeshv1.GetBatchRequest{
		BatchId: batchID,
	}))
	if err != nil {
		return err
	}
	printBatch(stdout, resp.Msg.GetBatch(), true)
	return nil
}

func batchList(c *client, stdout io.Writer) error {
	resp, err := c.batch.ListBatches(context.Background(), connect.NewRequest(&procmeshv1.ListBatchesRequest{}))
	if err != nil {
		return err
	}
	for _, b := range resp.Msg.GetBatches() {
		printBatch(stdout, b, false)
	}
	return nil
}

func batchRetry(c *client, batchID string, stdout io.Writer) error {
	resp, err := c.batch.RetryFailed(context.Background(), connect.NewRequest(&procmeshv1.RetryBatchRequest{
		Meta:    c.meta(),
		BatchId: batchID,
	}))
	if err != nil {
		return err
	}
	printBatch(stdout, resp.Msg.GetBatch(), false)
	return nil
}

func batchReplayTimeout(c *client, batchID string, stdout io.Writer) error {
	resp, err := c.batch.ReplayTimeout(context.Background(), connect.NewRequest(&procmeshv1.RetryBatchRequest{
		Meta:    c.meta(),
		BatchId: batchID,
	}))
	if err != nil {
		return err
	}
	printBatch(stdout, resp.Msg.GetBatch(), false)
	return nil
}

func batchExport(c *client, batchID string, opt options, stdout io.Writer) error {
	format := opt.format
	if format == "" {
		format = "json"
	}
	switch format {
	case "json", "csv":
	default:
		return usageError("batch export --format must be json or csv")
	}
	resp, err := c.batch.ExportBatch(context.Background(), connect.NewRequest(&procmeshv1.ExportBatchRequest{
		BatchId: batchID,
		Format:  format,
	}))
	if err != nil {
		return err
	}
	_, err = stdout.Write(resp.Msg.GetContent())
	return err
}

func mapBatchType(t string) (string, error) {
	switch strings.ToLower(t) {
	case "start":
		return "START", nil
	case "stop":
		return "STOP", nil
	case "restart":
		return "RESTART", nil
	case "apply":
		return "CONFIG_UPDATE", nil
	case "":
		return "", usageError("batch create requires --type")
	default:
		return "", usageError("batch create --type must be start|stop|restart|apply")
	}
}

func buildBatchSelector(opt options) (*procmeshv1.BatchSelector, error) {
	sel := &procmeshv1.BatchSelector{
		ProcessIds:   append([]string(nil), opt.processIDs...),
		AgentGroupId: opt.agentGroupID,
		ProcessGroup: opt.processGroup,
	}
	for _, raw := range opt.processNames {
		node, name, ok := strings.Cut(raw, ":")
		if !ok || node == "" || name == "" {
			return nil, usageError("batch --process-name requires NODE:NAME")
		}
		sel.ProcessNames = append(sel.ProcessNames, &procmeshv1.ProcessNameRef{
			NodeId:      node,
			ProcessName: name,
		})
	}
	if len(sel.ProcessIds) == 0 && len(sel.ProcessNames) == 0 &&
		sel.AgentGroupId == "" && sel.ProcessGroup == "" {
		return nil, usageError("batch create requires a selector")
	}
	return sel, nil
}

func printBatch(stdout io.Writer, b *procmeshv1.Batch, withTargets bool) {
	if b == nil {
		return
	}
	fmt.Fprintf(stdout, "batch_id=%s\n", b.GetBatchId())
	fmt.Fprintf(stdout, "type=%s\n", b.GetType())
	fmt.Fprintf(stdout, "status=%s\n", b.GetStatus())
	if s := b.GetSummary(); s != nil {
		fmt.Fprintf(stdout, "success=%d\n", s.GetSuccess())
		fmt.Fprintf(stdout, "failed=%d\n", s.GetFailed())
		fmt.Fprintf(stdout, "timeout=%d\n", s.GetTimeout())
		fmt.Fprintf(stdout, "denied=%d\n", s.GetDenied())
		fmt.Fprintf(stdout, "conflict=%d\n", s.GetConflict())
		fmt.Fprintf(stdout, "unavailable=%d\n", s.GetUnavailable())
		fmt.Fprintf(stdout, "invalid=%d\n", s.GetInvalid())
	}
	if withTargets {
		for _, t := range b.GetTargets() {
			fmt.Fprintf(stdout, "target process=%s name=%s node=%s status=%s op=%s\n",
				t.GetProcessId(), t.GetProcessName(), t.GetNodeId(), t.GetStatus(), t.GetOperationId())
		}
	}
}
