package api

import (
	"context"
	"encoding/json"
	"net/http"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.ProcessServiceHandler = (*ProcessAPI)(nil)

// Forwarder obtains Agent-to-Agent clients for a resolved owner route.
type Forwarder interface {
	Process(ctx context.Context, rt Route) (procmeshv1connect.ProcessServiceClient, error)
	Config(ctx context.Context, rt Route) (procmeshv1connect.ConfigServiceClient, error)
	Log(ctx context.Context, rt Route) (procmeshv1connect.LogServiceClient, error)
	Audit(ctx context.Context, rt Route) (procmeshv1connect.AuditServiceClient, error)
	Metrics(ctx context.Context, rt Route) (procmeshv1connect.MetricsServiceClient, error)
}

type ProcessAPI struct {
	Mgr       *process.Manager
	Auth      *auth.Service
	Degraded  func() bool
	LocalOnly bool
	LocalID   string
	Router    *Router
	Forward   Forwarder
}

func (s *ProcessAPI) hop(ctx context.Context, header http.Header, idOrName, ownerAgentID string) (local bool, rt Route, err error) {
	return hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, header, idOrName, ownerAgentID)
}

func hopRoute(localOnly bool, localID string, router *Router, ctx context.Context, header http.Header, idOrName, ownerAgentID string) (bool, Route, error) {
	if localOnly || router == nil {
		return true, Route{Local: true, NodeID: localID}, nil
	}
	rt, err := router.Resolve(ctx, rpc.TargetOf(header), idOrName, ownerAgentID)
	if err != nil {
		return false, Route{}, err
	}
	return rt.Local, rt, nil
}

func stampHop(h http.Header, localID, target string) {
	rpc.SetSource(h, localID)
	rpc.SetTarget(h, target)
}

// stampIdentity 按当前 Principal 覆盖 hop 身份头。Token 只转发内部 id，不带 pmt_ 明文。
func stampIdentity(h http.Header, ctx context.Context) {
	src := make(http.Header)
	if p, ok := PrincipalFrom(ctx); ok {
		rpc.SetUserID(src, p.UserID)
		if p.SessionID != "" {
			rpc.SetSessionID(src, p.SessionID)
		} else if p.TokenID != "" {
			rpc.SetTokenID(src, p.TokenID)
		}
	}
	rpc.CopyIdentity(h, src)
	h.Del("Authorization")
}

func mapForwardErr(err error) error {
	if err == nil {
		return nil
	}
	return ToConnect(rpc.MapCallError(err))
}

func unavailableOwner() error {
	return ToConnect(errcode.E(errcode.UNAVAILABLE, "owner unreachable"))
}

func (s *ProcessAPI) remoteProcess(ctx context.Context, rt Route, header http.Header) (procmeshv1connect.ProcessServiceClient, error) {
	if s.Forward == nil {
		return nil, unavailableOwner()
	}
	stampHop(header, s.LocalID, rt.NodeID)
	stampIdentity(header, ctx)
	cli, err := s.Forward.Process(ctx, rt)
	if err != nil {
		return nil, ToConnect(rpc.MapDialError(err))
	}
	return cli, nil
}

func (s *ProcessAPI) ListProcesses(ctx context.Context, req *connect.Request[procmeshv1.ListProcessesRequest]) (*connect.Response[procmeshv1.ListProcessesResponse], error) {
	local, rt, err := s.hop(ctx, req.Header(), "", "")
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := requireAnyPerm(ctx, s.Auth, auth.PermProcessRead); err != nil {
		return nil, err
	}
	if !local {
		cli, err := s.remoteProcess(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.ListProcesses(ctx, req)
		if err != nil {
			return nil, mapForwardErr(err)
		}
		return out, nil
	}
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
	filter := req.Msg.GetGroup()
	p, hasP := PrincipalFrom(ctx)
	nodeID := hopTarget(local, rt, s.LocalID)
	for _, spec := range specs {
		if filter != "" && spec.Group != filter {
			continue
		}
		if s.Auth != nil && hasP {
			if err := s.Auth.AllowOn(p, auth.PermProcessRead, control.CheckTarget{
				NodeID: nodeID, ProcessGroup: spec.Group,
			}); err != nil {
				if errcode.Is(err, errcode.DENIED) {
					continue
				}
				return nil, ToConnect(err)
			}
		}
		view, err := s.viewOf(ctx, spec)
		if err != nil {
			return nil, err
		}
		out.Processes = append(out.Processes, view)
	}
	return connect.NewResponse(out), nil
}

func (s *ProcessAPI) GetProcess(ctx context.Context, req *connect.Request[procmeshv1.GetProcessRequest]) (*connect.Response[procmeshv1.GetProcessResponse], error) {
	local, rt, err := s.hop(ctx, req.Header(), req.Msg.GetIdOrName(), "")
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, auth.PermProcessRead, req.Msg.GetIdOrName(), local, rt, false); err != nil {
		return nil, err
	}
	if !local {
		cli, err := s.remoteProcess(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.GetProcess(ctx, req)
		if err != nil {
			return nil, mapForwardErr(err)
		}
		return out, nil
	}
	if err := requireMgr(s.Mgr); err != nil {
		return nil, err
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessSpec(ctx, s.Auth, auth.PermProcessRead, s.LocalID, spec.Group, false); err != nil {
		return nil, err
	}
	view, err := s.viewOf(ctx, spec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.GetProcessResponse{Process: view}), nil
}

func (s *ProcessAPI) ApplyProcess(ctx context.Context, req *connect.Request[procmeshv1.ApplyProcessRequest]) (*connect.Response[procmeshv1.ApplyProcessResponse], error) {
	idOrName, owner := applyIdentity(req.Msg.GetSpec())
	local, rt, err := s.hop(ctx, req.Header(), idOrName, owner)
	if err != nil {
		return nil, ToConnect(err)
	}
	perm := auth.PermProcessCreate
	if req.Msg.GetExpectedRevision() != 0 || s.processExists(ctx, idOrName) {
		perm = auth.PermProcessUpdate
	}
	if err := requireAnyPerm(ctx, s.Auth, perm); err != nil {
		return nil, err
	}
	if !local {
		group := req.Msg.GetSpec().GetGroup()
		if group == "" {
			group = gossipGroup(s.Router, rt.NodeID, idOrName)
		}
		if err := requirePermOn(ctx, s.Auth, perm, control.CheckTarget{
			NodeID: rt.NodeID, ProcessGroup: group,
		}, true, false); err != nil {
			return nil, err
		}
		cli, err := s.remoteProcess(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.ApplyProcess(ctx, req)
		if err != nil {
			return nil, mapForwardErr(err)
		}
		return out, nil
	}
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	group := req.Msg.GetSpec().GetGroup()
	if s.processExists(ctx, idOrName) {
		if existing, err := s.Mgr.Resolve(ctx, idOrName); err == nil {
			group = existing.Group
		}
	}
	if err := authorizeProcessSpec(ctx, s.Auth, perm, s.LocalID, group, true); err != nil {
		return nil, err
	}
	if spec := req.Msg.GetSpec(); spec != nil && spec.GetOwnerAgentId() == "" && s.LocalID != "" {
		spec.OwnerAgentId = s.LocalID
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
	local, rt, err := s.hop(ctx, req.Header(), req.Msg.GetIdOrName(), "")
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, auth.PermProcessDelete, req.Msg.GetIdOrName(), local, rt, true); err != nil {
		return nil, err
	}
	if !local {
		cli, err := s.remoteProcess(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.DeleteProcess(ctx, req)
		if err != nil {
			return nil, mapForwardErr(err)
		}
		return out, nil
	}
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
	if err := authorizeProcessSpec(ctx, s.Auth, auth.PermProcessDelete, s.LocalID, spec.Group, true); err != nil {
		return nil, err
	}
	if err := s.Mgr.DeleteSpec(ctx, spec.ProcessID, req.Msg.GetExpectedRevision(), opID, operator); err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.DeleteProcessResponse{}), nil
}

func (s *ProcessAPI) StartProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, auth.PermProcessStart, func(cli procmeshv1connect.ProcessServiceClient) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
		return cli.StartProcess(ctx, req)
	}, func(ctx context.Context, processID, opID, operator string) error {
		if err := s.Mgr.SetDesired(ctx, processID, process.DesiredRunning, opID, operator); err != nil {
			return err
		}
		return s.Mgr.Reconcile(ctx)
	})
}

func (s *ProcessAPI) StopProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, auth.PermProcessStop, func(cli procmeshv1connect.ProcessServiceClient) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
		return cli.StopProcess(ctx, req)
	}, func(ctx context.Context, processID, opID, operator string) error {
		if err := s.Mgr.SetDesired(ctx, processID, process.DesiredStopped, opID, operator); err != nil {
			return err
		}
		return s.Mgr.Reconcile(ctx)
	})
}

func (s *ProcessAPI) RestartProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, auth.PermProcessRestart, func(cli procmeshv1connect.ProcessServiceClient) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
		return cli.RestartProcess(ctx, req)
	}, func(ctx context.Context, processID, opID, operator string) error {
		if err := s.Mgr.Restart(ctx, processID, opID, operator); err != nil {
			return err
		}
		return s.Mgr.Reconcile(ctx)
	})
}

func (s *ProcessAPI) KillProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, auth.PermProcessStop, func(cli procmeshv1connect.ProcessServiceClient) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
		return cli.KillProcess(ctx, req)
	}, func(ctx context.Context, processID, opID, operator string) error {
		return s.Mgr.Kill(ctx, processID, opID, operator)
	})
}

func (s *ProcessAPI) ResetFailure(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return s.mutateRef(ctx, req, auth.PermProcessUpdate, func(cli procmeshv1connect.ProcessServiceClient) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
		return cli.ResetFailure(ctx, req)
	}, func(ctx context.Context, processID, opID, operator string) error {
		return s.Mgr.ResetFailure(ctx, processID, opID, operator)
	})
}

func (s *ProcessAPI) AdoptInstance(ctx context.Context, req *connect.Request[procmeshv1.AdoptRequest]) (*connect.Response[procmeshv1.AdoptResponse], error) {
	local, rt, err := s.hop(ctx, req.Header(), req.Msg.GetInstanceId(), "")
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, auth.PermProcessUpdate, req.Msg.GetInstanceId(), local, rt, true); err != nil {
		return nil, err
	}
	if !local {
		cli, err := s.remoteProcess(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.AdoptInstance(ctx, req)
		if err != nil {
			return nil, mapForwardErr(err)
		}
		return out, nil
	}
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
	inst, err := s.Mgr.GetInstance(ctx, req.Msg.GetInstanceId())
	if err != nil {
		return nil, ToConnect(err)
	}
	spec, err := s.Mgr.GetSpec(ctx, inst.ProcessID)
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessSpec(ctx, s.Auth, auth.PermProcessUpdate, s.LocalID, spec.Group, true); err != nil {
		return nil, err
	}
	if !done {
		if err := s.Mgr.Adopt(ctx, req.Msg.GetInstanceId(), int(req.Msg.GetPid()), opID, operator); err != nil {
			return nil, ToConnect(err)
		}
		inst, err = s.Mgr.GetInstance(ctx, req.Msg.GetInstanceId())
		if err != nil {
			return nil, ToConnect(err)
		}
		spec, err = s.Mgr.GetSpec(ctx, inst.ProcessID)
		if err != nil {
			return nil, ToConnect(err)
		}
	}
	view, err := s.viewOf(ctx, spec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.AdoptResponse{Process: view}), nil
}

func (s *ProcessAPI) mutateRef(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest], perm string, remote func(procmeshv1connect.ProcessServiceClient) (*connect.Response[procmeshv1.ProcessRefResponse], error), fn func(ctx context.Context, processID, opID, operator string) error) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	local, rt, err := s.hop(ctx, req.Header(), req.Msg.GetIdOrName(), "")
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, perm, req.Msg.GetIdOrName(), local, rt, true); err != nil {
		return nil, err
	}
	if !local {
		cli, err := s.remoteProcess(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := remote(cli)
		if err != nil {
			return nil, mapForwardErr(err)
		}
		return out, nil
	}
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
	if err := authorizeProcessSpec(ctx, s.Auth, perm, s.LocalID, spec.Group, true); err != nil {
		return nil, err
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

func (s *ProcessAPI) processExists(ctx context.Context, idOrName string) bool {
	if s.Mgr == nil || idOrName == "" {
		return false
	}
	_, err := s.Mgr.Resolve(ctx, idOrName)
	return err == nil
}

func applyIdentity(spec *procmeshv1.ProcessSpec) (idOrName, owner string) {
	if spec == nil {
		return "", ""
	}
	idOrName = spec.GetProcessId()
	if idOrName == "" {
		idOrName = spec.GetName()
	}
	return idOrName, spec.GetOwnerAgentId()
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

func gossipGroup(router *Router, nodeID, processName string) string {
	if router == nil || processName == "" {
		return ""
	}
	for _, n := range router.members() {
		if n.NodeID != nodeID {
			continue
		}
		for _, p := range n.Processes {
			if p.Name == processName {
				return p.Group
			}
		}
	}
	return ""
}

func authorizeProcessRoute(ctx context.Context, svc *auth.Service, router *Router, perm, idOrName string, local bool, rt Route, write bool) error {
	if err := requireAnyPerm(ctx, svc, perm); err != nil {
		return err
	}
	if local {
		return nil
	}
	return requirePermOn(ctx, svc, perm, control.CheckTarget{
		NodeID: rt.NodeID, ProcessGroup: gossipGroup(router, rt.NodeID, idOrName),
	}, write, false)
}

func authorizeProcessSpec(ctx context.Context, svc *auth.Service, perm, localID, processGroup string, write bool) error {
	return requirePermOn(ctx, svc, perm, control.CheckTarget{
		NodeID: localID, ProcessGroup: processGroup,
	}, write, true)
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
