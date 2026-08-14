package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runProcess(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return processList(c, stdout)
	case "get":
		id, err := oneArg(pos, "process get")
		if err != nil {
			return err
		}
		return processGet(c, id, stdout)
	case "start":
		id, err := oneArg(pos, "process start")
		if err != nil {
			return err
		}
		return processRef(c.proc.StartProcess, c, id)
	case "stop":
		id, err := oneArg(pos, "process stop")
		if err != nil {
			return err
		}
		return processRef(c.proc.StopProcess, c, id)
	case "restart":
		id, err := oneArg(pos, "process restart")
		if err != nil {
			return err
		}
		return processRef(c.proc.RestartProcess, c, id)
	case "kill":
		id, err := oneArg(pos, "process kill")
		if err != nil {
			return err
		}
		return processRef(c.proc.KillProcess, c, id)
	case "reset-failure":
		id, err := oneArg(pos, "process reset-failure")
		if err != nil {
			return err
		}
		return processRef(c.proc.ResetFailure, c, id)
	case "logs":
		id, err := oneArg(pos, "process logs")
		if err != nil {
			return err
		}
		return processLogs(c, id, opt, stdout)
	case "apply":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return processApply(c, opt, stdout)
	case "delete":
		id, err := oneArg(pos, "process delete")
		if err != nil {
			return err
		}
		return processDelete(c, id, opt)
	case "history":
		id, err := oneArg(pos, "process history")
		if err != nil {
			return err
		}
		return processHistory(c, id, stdout)
	case "rollback":
		id, err := oneArg(pos, "process rollback")
		if err != nil {
			return err
		}
		return processRollback(c, id, opt, stdout)
	case "adopt":
		id, err := oneArg(pos, "process adopt")
		if err != nil {
			return err
		}
		return processAdopt(c, id, opt)
	default:
		return usageError("unknown process command")
	}
}

func oneArg(pos []string, cmd string) (string, error) {
	if len(pos) != 1 || pos[0] == "" {
		return "", usageError(cmd + " requires <id-or-name>")
	}
	return pos[0], nil
}

type refFn func(context.Context, *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error)

func processRef(fn refFn, c *client, id string) error {
	_, err := fn(context.Background(), connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     c.meta(),
		IdOrName: id,
	}))
	return err
}

func processList(c *client, stdout io.Writer) error {
	resp, err := c.proc.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if err != nil {
		return err
	}
	for _, p := range resp.Msg.GetProcesses() {
		name, id := viewNameID(p)
		insts := p.GetInstances()
		if len(insts) == 0 {
			fmt.Fprintf(stdout, "%s\t%s\t\t\t\t\n", name, id)
			continue
		}
		for _, inst := range insts {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%d\n",
				name, id, inst.GetDesired(), inst.GetObserved(), inst.GetHealth(), inst.GetPid())
		}
	}
	return nil
}

func processGet(c *client, id string, stdout io.Writer) error {
	resp, err := c.proc.GetProcess(context.Background(), connect.NewRequest(&procmeshv1.GetProcessRequest{IdOrName: id}))
	if err != nil {
		return err
	}
	p := resp.Msg.GetProcess()
	spec := p.GetSpec()
	fmt.Fprintf(stdout, "name\t%s\n", spec.GetName())
	fmt.Fprintf(stdout, "process_id\t%s\n", p.GetProcessId())
	fmt.Fprintf(stdout, "command\t%s\n", spec.GetCommand())
	if args := spec.GetArgs(); len(args) > 0 {
		fmt.Fprintf(stdout, "args\t%s\n", strings.Join(args, " "))
	}
	fmt.Fprintf(stdout, "revision\t%d\n", spec.GetLatestRevision())
	if spec.GetWorkingDirectory() != "" {
		fmt.Fprintf(stdout, "working_directory\t%s\n", spec.GetWorkingDirectory())
	}
	if spec.GetInstances() != 0 {
		fmt.Fprintf(stdout, "instances\t%d\n", spec.GetInstances())
	}
	if r := spec.GetRestart(); r != nil && r.GetMode() != "" {
		fmt.Fprintf(stdout, "restart.mode\t%s\n", r.GetMode())
	}
	for _, inst := range p.GetInstances() {
		fmt.Fprintf(stdout, "instance\t%s\t%d\t%s\t%s\t%s\t%d\n",
			inst.GetInstanceId(), inst.GetOrdinal(), inst.GetDesired(), inst.GetObserved(), inst.GetHealth(), inst.GetPid())
	}
	return nil
}

func processApply(c *client, opt options, stdout io.Writer) error {
	if opt.file == "" {
		return usageError("process apply requires --file")
	}
	spec, err := Load(opt.file)
	if err != nil {
		return err
	}
	resp, err := c.proc.ApplyProcess(context.Background(), connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta:             c.meta(),
		ExpectedRevision: opt.expectedRevision,
		Spec:             spec,
		Comment:          opt.comment,
	}))
	if err != nil {
		return err
	}
	got := resp.Msg.GetSpec()
	fmt.Fprintf(stdout, "%s revision=%d\n", got.GetProcessId(), got.GetLatestRevision())
	return nil
}

func processDelete(c *client, id string, opt options) error {
	if !opt.expectedSet {
		return usageError("process delete requires --expected-revision")
	}
	_, err := c.proc.DeleteProcess(context.Background(), connect.NewRequest(&procmeshv1.DeleteProcessRequest{
		Meta:             c.meta(),
		IdOrName:         id,
		ExpectedRevision: opt.expectedRevision,
	}))
	return err
}

func processHistory(c *client, id string, stdout io.Writer) error {
	resp, err := c.cfg.History(context.Background(), connect.NewRequest(&procmeshv1.HistoryRequest{IdOrName: id}))
	if err != nil {
		return err
	}
	for _, r := range resp.Msg.GetRevisions() {
		ts := time.UnixMilli(r.GetTimestampUnixMs()).UTC().Format(time.RFC3339)
		fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\n", r.GetRevision(), r.GetOperator(), ts, r.GetComment())
		if d := r.GetDiff(); d != "" {
			fmt.Fprintln(stdout, d)
		}
	}
	return nil
}

func processRollback(c *client, id string, opt options, stdout io.Writer) error {
	if !opt.toSet || !opt.expectedSet {
		return usageError("process rollback requires --to and --expected-revision")
	}
	resp, err := c.cfg.Rollback(context.Background(), connect.NewRequest(&procmeshv1.RollbackRequest{
		Meta:             c.meta(),
		IdOrName:         id,
		ToRevision:       opt.toRevision,
		ExpectedRevision: opt.expectedRevision,
		Comment:          opt.comment,
	}))
	if err != nil {
		return err
	}
	got := resp.Msg.GetSpec()
	fmt.Fprintf(stdout, "%s revision=%d\n", got.GetProcessId(), got.GetLatestRevision())
	return nil
}

func processAdopt(c *client, instanceID string, opt options) error {
	if !opt.pidSet {
		return usageError("process adopt requires --pid")
	}
	_, err := c.proc.AdoptInstance(context.Background(), connect.NewRequest(&procmeshv1.AdoptRequest{
		Meta:       c.meta(),
		InstanceId: instanceID,
		Pid:        opt.pid,
	}))
	return err
}

func processLogs(c *client, id string, opt options, stdout io.Writer) error {
	resp, err := c.logs.TailLogs(context.Background(), connect.NewRequest(&procmeshv1.TailLogsRequest{
		IdOrName:   id,
		InstanceId: opt.instance,
		Stream:     opt.stream,
		Lines:      opt.lines,
	}))
	if err != nil {
		return err
	}
	for _, line := range resp.Msg.GetLines() {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

func viewNameID(p *procmeshv1.ProcessView) (name, id string) {
	id = p.GetProcessId()
	if spec := p.GetSpec(); spec != nil {
		name = spec.GetName()
		if id == "" {
			id = spec.GetProcessId()
		}
	}
	return name, id
}
