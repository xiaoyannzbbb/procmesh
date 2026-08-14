package api

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.ProcessServiceHandler = (*ProcessAPI)(nil)

type ProcessAPI struct {
	Mgr      *process.Manager
	Degraded func() bool
}

func (s *ProcessAPI) ListProcesses(ctx context.Context, _ *connect.Request[procmeshv1.ListProcessesRequest]) (*connect.Response[procmeshv1.ListProcessesResponse], error) {
	if err := requireMgr(s.Mgr); err != nil {
		return nil, err
	}
	specs, err := s.Mgr.ListSpecs(ctx)
	if err != nil {
		return nil, ToConnect(err)
	}
	out := &procmeshv1.ListProcessesResponse{
		Processes: make([]*procmeshv1.ProcessView, 0, len(specs)),
	}
	for _, spec := range specs {
		view, err := s.viewOf(ctx, spec)
		if err != nil {
			return nil, err
		}
		out.Processes = append(out.Processes, view)
	}
	return connect.NewResponse(out), nil
}

func (s *ProcessAPI) GetProcess(ctx context.Context, req *connect.Request[procmeshv1.GetProcessRequest]) (*connect.Response[procmeshv1.GetProcessResponse], error) {
	if err := requireMgr(s.Mgr); err != nil {
		return nil, err
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	view, err := s.viewOf(ctx, spec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.GetProcessResponse{Process: view}), nil
}

func (s *ProcessAPI) ApplyProcess(ctx context.Context, req *connect.Request[procmeshv1.ApplyProcessRequest]) (*connect.Response[procmeshv1.ApplyProcessResponse], error) {
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	done, result, err := s.peekDone(ctx, opID)
	if err != nil {
		return nil, err
	}
	if done {
		spec, ok := specFromJournal(result)
		if !ok {
			spec, err = s.resolveApplySpec(ctx, req.Msg.GetExpectedRevision(), ProtoToSpec(req.Msg.GetSpec()))
			if err != nil {
				return nil, ToConnect(err)
			}
		}
		return connect.NewResponse(&procmeshv1.ApplyProcessResponse{Spec: SpecToProto(spec)}), nil
	}
	spec := ProtoToSpec(req.Msg.GetSpec())
	if req.Msg.GetExpectedRevision() != 0 {
		spec, err = s.resolveApplySpec(ctx, req.Msg.GetExpectedRevision(), spec)
		if err != nil {
			return nil, ToConnect(err)
		}
	}
	got, err := s.Mgr.ApplySpec(ctx, spec, req.Msg.GetExpectedRevision(), opID, operator, req.Msg.GetComment())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.ApplyProcessResponse{Spec: SpecToProto(got)}), nil
}

func (s *ProcessAPI) DeleteProcess(ctx context.Context, req *connect.Request[procmeshv1.DeleteProcessRequest]) (*connect.Response[procmeshv1.DeleteProcessResponse], error) {
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	done, _, err := s.peekDone(ctx, opID)
	if err != nil {
		return nil, err
	}
	if done {
		return connect.NewResponse(&procmeshv1.DeleteProcessResponse{}), nil
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := s.Mgr.DeleteSpec(ctx, spec.ProcessID, req.Msg.GetExpectedRevision(), opID, operator); err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.DeleteProcessResponse{}), nil
}

func (s *ProcessAPI) StartProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, func(ctx context.Context, processID, opID, operator string) error {
		if err := s.Mgr.SetDesired(ctx, processID, process.DesiredRunning, opID, operator); err != nil {
			return err
		}
		return s.Mgr.Reconcile(ctx)
	})
}

func (s *ProcessAPI) StopProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, func(ctx context.Context, processID, opID, operator string) error {
		if err := s.Mgr.SetDesired(ctx, processID, process.DesiredStopped, opID, operator); err != nil {
			return err
		}
		return s.Mgr.Reconcile(ctx)
	})
}

func (s *ProcessAPI) RestartProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, func(ctx context.Context, processID, opID, operator string) error {
		if err := s.Mgr.Restart(ctx, processID, opID, operator); err != nil {
			return err
		}
		return s.Mgr.Reconcile(ctx)
	})
}

func (s *ProcessAPI) KillProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, func(ctx context.Context, processID, opID, operator string) error {
		return s.Mgr.Kill(ctx, processID, opID, operator)
	})
}

func (s *ProcessAPI) ResetFailure(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, func(ctx context.Context, processID, opID, operator string) error {
		return s.Mgr.ResetFailure(ctx, processID, opID, operator)
	})
}

func (s *ProcessAPI) AdoptInstance(ctx context.Context, req *connect.Request[procmeshv1.AdoptRequest]) (*connect.Response[procmeshv1.AdoptResponse], error) {
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	done, _, err := s.peekDone(ctx, opID)
	if err != nil {
		return nil, err
	}
	if !done {
		if err := s.Mgr.Adopt(ctx, req.Msg.GetInstanceId(), int(req.Msg.GetPid()), opID, operator); err != nil {
			return nil, ToConnect(err)
		}
	}
	inst, err := s.Mgr.GetInstance(ctx, req.Msg.GetInstanceId())
	if err != nil {
		return nil, ToConnect(err)
	}
	spec, err := s.Mgr.GetSpec(ctx, inst.ProcessID)
	if err != nil {
		return nil, ToConnect(err)
	}
	view, err := s.viewOf(ctx, spec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.AdoptResponse{Process: view}), nil
}

func (s *ProcessAPI) mutateRef(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest], fn func(ctx context.Context, processID, opID, operator string) error) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	done, _, err := s.peekDone(ctx, opID)
	if err != nil {
		return nil, err
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	if !done {
		if err := fn(ctx, spec.ProcessID, opID, operator); err != nil {
			return nil, ToConnect(err)
		}
		spec, err = s.Mgr.GetSpec(ctx, spec.ProcessID)
		if err != nil {
			return nil, ToConnect(err)
		}
	}
	view, err := s.viewOf(ctx, spec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.ProcessRefResponse{Process: view}), nil
}

func (s *ProcessAPI) rejectMutation() error {
	if s.Degraded != nil && s.Degraded() {
		return ToConnect(errcode.E(errcode.DEGRADED, "degraded"))
	}
	return nil
}

func requireMgr(mgr *process.Manager) error {
	if mgr == nil {
		return ToConnect(errcode.E(errcode.DEGRADED, "degraded"))
	}
	return nil
}

func metaOf(m *procmeshv1.MutationMeta) (opID, operator string, err error) {
	if m == nil || m.GetOperationId() == "" {
		return "", "", ToConnect(errcode.E(errcode.INVALID, "operation_id required"))
	}
	return m.GetOperationId(), m.GetOperator(), nil
}

func (s *ProcessAPI) peekDone(ctx context.Context, opID string) (bool, []byte, error) {
	status, result, errMsg, err := s.Mgr.PeekOp(ctx, opID)
	if err != nil {
		if errcode.Is(err, errcode.NOT_FOUND) {
			return false, nil, nil
		}
		return false, nil, ToConnect(err)
	}
	switch status {
	case "SUCCESS":
		return true, result, nil
	case "FAILED":
		if errMsg == "" {
			errMsg = "operation failed"
		}
		return true, nil, ToConnect(errcode.E(errcode.INVALID, errMsg))
	default:
		return false, nil, nil
	}
}

func (s *ProcessAPI) viewOf(ctx context.Context, spec process.ProcessSpec) (*procmeshv1.ProcessView, error) {
	insts, err := s.Mgr.ListInstances(ctx, spec.ProcessID)
	if err != nil {
		return nil, ToConnect(err)
	}
	return ViewOf(spec, insts), nil
}

func (s *ProcessAPI) resolveApplySpec(ctx context.Context, expectedRevision int64, spec process.ProcessSpec) (process.ProcessSpec, error) {
	if expectedRevision == 0 {
		return spec, nil
	}
	idOrName := spec.ProcessID
	if idOrName == "" {
		idOrName = spec.Name
	}
	existing, err := s.Mgr.Resolve(ctx, idOrName)
	if err != nil {
		return process.ProcessSpec{}, err
	}
	spec.ProcessID = existing.ProcessID
	return spec, nil
}

func specFromJournal(result []byte) (process.ProcessSpec, bool) {
	if len(result) == 0 {
		return process.ProcessSpec{}, false
	}
	var spec process.ProcessSpec
	if json.Unmarshal(result, &spec) != nil || spec.ProcessID == "" {
		return process.ProcessSpec{}, false
	}
	return spec, true
}
