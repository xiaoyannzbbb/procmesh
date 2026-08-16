package api

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type batchExecutor struct {
	api *BatchAPI
}

func (e *batchExecutor) Execute(ctx context.Context, t batch.Target, typ batch.Type) error {
	if e == nil || e.api == nil {
		return errcode.E(errcode.INVALID, "executor")
	}
	ctx = e.withIdentity(ctx, t)
	switch typ {
	case batch.TypeStart, batch.TypeStop, batch.TypeRestart:
		return e.execProcess(ctx, t, typ)
	case batch.TypeConfigUpdate:
		return e.execConfig(ctx, t)
	default:
		return errcode.E(errcode.INVALID, "type")
	}
}

func (e *batchExecutor) withIdentity(ctx context.Context, t batch.Target) context.Context {
	if _, ok := PrincipalFrom(ctx); ok {
		return ctx
	}
	if p, ok := e.api.principalFor(t.OperationID); ok {
		return WithPrincipal(ctx, p)
	}
	return ctx
}

func (e *batchExecutor) execProcess(ctx context.Context, t batch.Target, typ batch.Type) error {
	id := t.ProcessID
	if id == "" {
		id = t.ProcessName
	}
	req := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: t.OperationID, Operator: operatorOf(ctx, "")},
		IdOrName: id,
	})
	rpc.SetTarget(req.Header(), t.NodeID)
	stampIdentity(req.Header(), ctx)
	proc := e.api.processAPI()
	var err error
	switch typ {
	case batch.TypeStart:
		_, err = proc.StartProcess(ctx, req)
	case batch.TypeStop:
		_, err = proc.StopProcess(ctx, req)
	default:
		_, err = proc.RestartProcess(ctx, req)
	}
	if err != nil {
		return rpc.MapCallError(err)
	}
	return nil
}

func (e *batchExecutor) execConfig(ctx context.Context, t batch.Target) error {
	var payload configUpdatePayload
	if t.PayloadJSON != "" {
		if err := json.Unmarshal([]byte(t.PayloadJSON), &payload); err != nil {
			return errcode.E(errcode.INVALID, "payload")
		}
	}
	expected := t.ExpectedRevision
	if expected == 0 {
		expected = payload.ExpectedRevision
	}
	if expected <= 0 {
		return errcode.E(errcode.INVALID, "expected_revision")
	}
	if payload.Spec == nil {
		return errcode.E(errcode.INVALID, "config spec")
	}
	id := t.ProcessID
	if id == "" {
		id = t.ProcessName
	}
	req := connect.NewRequest(&procmeshv1.UpdateConfigRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: t.OperationID, Operator: operatorOf(ctx, "")},
		IdOrName:         id,
		ExpectedRevision: expected,
		Spec:             payload.Spec,
	})
	rpc.SetTarget(req.Header(), t.NodeID)
	stampIdentity(req.Header(), ctx)
	_, err := e.api.configAPI().UpdateConfig(ctx, req)
	if err != nil {
		return rpc.MapCallError(err)
	}
	return nil
}

func (s *BatchAPI) processAPI() *ProcessAPI {
	return &ProcessAPI{
		Mgr: s.Mgr, Auth: s.Auth, Degraded: s.Degraded,
		LocalID: s.LocalID, Router: s.Router, Forward: s.Forward,
	}
}

func (s *BatchAPI) configAPI() *ConfigAPI {
	var revs RevisionStore
	if s.Store != nil {
		revs = s.Store
	}
	return &ConfigAPI{
		Mgr: s.Mgr, Auth: s.Auth, Revs: revs, Degraded: s.Degraded,
		LocalID: s.LocalID, Router: s.Router, Forward: s.Forward,
	}
}
