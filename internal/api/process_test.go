package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func TestProcess_ApplyGetList(t *testing.T) {
	ctx := context.Background()
	c := newProcessClient(t, nil)

	applied, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-create", Operator: "t"},
		ExpectedRevision: 0,
		Spec:             &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
		Comment:          "add",
	}))
	if err != nil {
		t.Fatal(err)
	}
	spec := applied.Msg.GetSpec()
	if spec.GetName() != "web" || spec.GetProcessId() == "" || spec.GetLatestRevision() != 1 {
		t.Fatalf("apply %+v", spec)
	}

	got, err := c.GetProcess(ctx, connect.NewRequest(&procmeshv1.GetProcessRequest{IdOrName: "web"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetProcess().GetProcessId() != spec.GetProcessId() {
		t.Fatalf("get %+v", got.Msg.GetProcess())
	}
	if got.Msg.GetProcess().GetSpec().GetName() != "web" {
		t.Fatalf("get spec %+v", got.Msg.GetProcess().GetSpec())
	}

	listed, err := c.ListProcesses(ctx, connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetProcesses()) == 0 {
		t.Fatal("list empty")
	}
}

func TestProcess_StartDesiredRunning(t *testing.T) {
	ctx := context.Background()
	c := newProcessClient(t, nil)
	if _, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-c", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "true", Command: "/bin/true"},
	})); err != nil {
		t.Fatal(err)
	}
	started, err := c.StartProcess(ctx, connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-s", Operator: "t"},
		IdOrName: "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	insts := started.Msg.GetProcess().GetInstances()
	if len(insts) == 0 || insts[0].GetDesired() != "RUNNING" {
		t.Fatalf("desired %+v", insts)
	}
}

func TestProcess_MissingOperationID(t *testing.T) {
	ctx := context.Background()
	c := newProcessClient(t, nil)
	_, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestProcess_ApplyConflictRevision(t *testing.T) {
	ctx := context.Background()
	c := newProcessClient(t, nil)
	first, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-c", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-c2", Operator: "t"},
		ExpectedRevision: 99,
		Spec: &procmeshv1.ProcessSpec{
			ProcessId: first.Msg.GetSpec().GetProcessId(),
			Name:      "web",
			Command:   "/bin/true",
		},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestProcess_StartIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newProcessClient(t, nil)
	if _, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-c", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "true", Command: "/bin/true"},
	})); err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-s", Operator: "t"},
		IdOrName: "true",
	})
	if _, err := c.StartProcess(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartProcess(ctx, req); err != nil {
		t.Fatal(err)
	}
}

func TestProcess_DegradedStartListOK(t *testing.T) {
	ctx := context.Background()
	c := newProcessClient(t, func() bool { return true })
	_, err := c.StartProcess(ctx, connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-s", Operator: "t"},
		IdOrName: "web",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "DEGRADED" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if _, err := c.ListProcesses(ctx, connect.NewRequest(&procmeshv1.ListProcessesRequest{})); err != nil {
		t.Fatal(err)
	}
}

func TestProcess_SpecConvertRoundtrip(t *testing.T) {
	in := process.ProcessSpec{
		ProcessID:        "p1",
		Name:             "web",
		OwnerAgentID:     "agent-1",
		Group:            "g1",
		Command:          "/bin/sleep",
		Args:             []string{"2"},
		WorkingDirectory: "/tmp",
		RunAsUser:        "nobody",
		Environment:      map[string]string{"A": "1"},
		Instances:        2,
		Autostart:        true,
		StopSignal:       "SIGTERM",
		KillSignal:       "SIGKILL",
		StopTimeout:      15 * time.Second,
		StartupPriority:  10,
		Restart: process.RestartPolicy{
			Mode:        process.RestartAlways,
			MaxRetries:  3,
			RetryWindow: time.Minute,
			Backoff:     process.Backoff{Initial: time.Second, Max: 30 * time.Second, Multiplier: 2},
		},
		Health: process.HealthCheckSpec{
			Type:             "alive",
			URL:              "http://127.0.0.1/h",
			Method:           "GET",
			Address:          "127.0.0.1:80",
			Command:          "/bin/true",
			ExpectedStatus:   200,
			Args:             []string{"-x"},
			InitialDelay:     100 * time.Millisecond,
			Interval:         time.Second,
			Timeout:          500 * time.Millisecond,
			FailureThreshold: 3,
			SuccessThreshold: 1,
			RestartOnFailure: true,
			RestartCooldown:  2 * time.Second,
		},
		Log: process.LogPolicy{
			MaxSize:  1 << 20,
			MaxFiles: 5,
			MaxAge:   time.Hour,
			Compress: true,
		},
		Resources:      process.ResourceLimit{CPUQuotaMillis: 500, MemoryBytes: 1 << 26, OpenFiles: 1024},
		Dependencies:   []process.Dependency{{ProcessName: "db", Condition: process.DepStarted}},
		LatestRevision: 4,
	}
	got := ProtoToSpec(SpecToProto(in))
	if got.ProcessID != in.ProcessID || got.Name != in.Name || got.Command != in.Command {
		t.Fatalf("identity %+v", got)
	}
	if got.StopTimeout != in.StopTimeout || got.Restart.RetryWindow != in.Restart.RetryWindow {
		t.Fatalf("duration stop=%s retry=%s", got.StopTimeout, got.Restart.RetryWindow)
	}
	if got.Log.MaxAge != in.Log.MaxAge || got.Health.Interval != in.Health.Interval {
		t.Fatalf("log/health %+v %+v", got.Log, got.Health)
	}
	if got.Restart.Mode != in.Restart.Mode || got.Resources.MemoryBytes != in.Resources.MemoryBytes {
		t.Fatalf("restart/res %+v %+v", got.Restart, got.Resources)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].ProcessName != "db" {
		t.Fatalf("deps %+v", got.Dependencies)
	}
	view := ViewOf(in, []process.Instance{{
		InstanceID:     "p1:0",
		Ordinal:        0,
		Desired:        process.DesiredRunning,
		Observed:       process.ObservedStopped,
		Health:         process.HealthUnknown,
		PID:            42,
		ActiveRevision: 4,
		RestartCount:   1,
	}})
	if view.GetProcessId() != "p1" || len(view.GetInstances()) != 1 || view.GetInstances()[0].GetPid() != 42 {
		t.Fatalf("view %+v", view)
	}
}

func TestProcess_RestartForwardsToOwner(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	fakeCli := &fakeProcessClient{
		restartResp: connect.NewResponse(&procmeshv1.ProcessRefResponse{
			Process: &procmeshv1.ProcessView{
				ProcessId: "nginx-1",
				Spec:      &procmeshv1.ProcessSpec{Name: "nginx"},
			},
		}),
	}
	fwd := &fakeForwarder{proc: fakeCli}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:     m,
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	})

	got, err := c.RestartProcess(ctx, connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-restart", Operator: "t"},
		IdOrName: "nginx",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetProcess().GetSpec().GetName() != "nginx" {
		t.Fatalf("view %+v", got.Msg.GetProcess())
	}
	if fwd.processCalls() != 1 {
		t.Fatalf("forward Process calls=%d", fwd.processCalls())
	}
	restarts := fakeCli.restartReqs()
	if len(restarts) != 1 {
		t.Fatalf("RestartProcess calls=%d", len(restarts))
	}
	if restarts[0].Msg.GetMeta().GetOperationId() != "op-restart" {
		t.Fatalf("operation_id=%q", restarts[0].Msg.GetMeta().GetOperationId())
	}
	if rpc.SourceOf(restarts[0].Header()) != "aaa" {
		t.Fatalf("source=%q", rpc.SourceOf(restarts[0].Header()))
	}
	if rpc.TargetOf(restarts[0].Header()) != "ccc" {
		t.Fatalf("target=%q", rpc.TargetOf(restarts[0].Header()))
	}

	listed, err := c.ListProcesses(ctx, connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetProcesses()) != 0 {
		t.Fatalf("local list %+v", listed.Msg.GetProcesses())
	}
}

func TestProcess_ApplyDoesNotCreateLocalWhenOwnerRemote(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	fakeCli := &fakeProcessClient{
		applyResp: connect.NewResponse(&procmeshv1.ApplyProcessResponse{
			Spec: &procmeshv1.ProcessSpec{ProcessId: "nginx-1", Name: "nginx", Command: "/bin/true", OwnerAgentId: "ccc"},
		}),
	}
	fwd := &fakeForwarder{proc: fakeCli}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:     m,
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", ""),
		Forward: fwd,
	})

	got, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-apply", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "nginx", Command: "/bin/true", OwnerAgentId: "ccc"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetSpec().GetOwnerAgentId() != "ccc" {
		t.Fatalf("spec %+v", got.Msg.GetSpec())
	}
	if fwd.processCalls() != 1 {
		t.Fatalf("forward Process calls=%d", fwd.processCalls())
	}
	applies := fakeCli.applyReqs()
	if len(applies) != 1 {
		t.Fatalf("ApplyProcess calls=%d", len(applies))
	}
	if applies[0].Msg.GetMeta().GetOperationId() != "op-apply" {
		t.Fatalf("operation_id=%q", applies[0].Msg.GetMeta().GetOperationId())
	}

	specs, err := m.ListSpecs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 0 {
		t.Fatalf("local specs %+v", specs)
	}
}

func TestProcess_LocalOnlyIgnoresTargetHeader(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	fwd := &fakeForwarder{proc: &fakeProcessClient{}}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:       m,
		LocalOnly: true,
		LocalID:   "aaa",
		Router:    remoteOwnerRouter("aaa", "ccc", "web"),
		Forward:   fwd,
	})
	if _, err := c.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-c", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	})); err != nil {
		t.Fatal(err)
	}

	req := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-restart", Operator: "t"},
		IdOrName: "web",
	})
	rpc.SetTarget(req.Header(), "ccc")
	got, err := c.RestartProcess(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetProcess().GetSpec().GetName() != "web" {
		t.Fatalf("view %+v", got.Msg.GetProcess())
	}
	if fwd.processCalls() != 0 {
		t.Fatalf("forward Process calls=%d want 0", fwd.processCalls())
	}
}

func TestProcess_ForwardedOwnerConflictRemainsConflict(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	fakeCli := &fakeProcessClient{err: ToConnect(errcode.E(errcode.CONFLICT, "revision mismatch"))}
	fwd := &fakeForwarder{proc: fakeCli}
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:     m,
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	})

	_, err := c.RestartProcess(ctx, connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-restart", Operator: "t"},
		IdOrName: "nginx",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestProcess_ForwardNilUnavailable(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	c := serveProcessAPI(t, &ProcessAPI{
		Mgr:     m,
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
	})
	_, err := c.RestartProcess(ctx, connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-restart", Operator: "t"},
		IdOrName: "nginx",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func connectDetail(t *testing.T, err error) (connect.Code, string) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("%T %v", err, err)
	}
	code := ""
	if len(ce.Details()) > 0 {
		msg, derr := ce.Details()[0].Value()
		if derr != nil {
			t.Fatal(derr)
		}
		info, ok := msg.(*procmeshv1.ErrorInfo)
		if !ok {
			t.Fatalf("%T", msg)
		}
		code = info.GetCode()
	}
	return ce.Code(), code
}
