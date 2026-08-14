package api

import (
	"context"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.ConfigServiceHandler = (*ConfigAPI)(nil)

type RevisionStore interface {
	GetRevisionSpec(ctx context.Context, processID string, rev int64) (process.ProcessSpec, error)
}

type ConfigAPI struct {
	Mgr      *process.Manager
	Revs     RevisionStore
	Degraded func() bool
}

func (s *ConfigAPI) GetConfig(ctx context.Context, req *connect.Request[procmeshv1.GetConfigRequest]) (*connect.Response[procmeshv1.GetConfigResponse], error) {
	if err := requireMgr(s.Mgr); err != nil {
		return nil, err
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.GetConfigResponse{Spec: SpecToProto(spec)}), nil
}

func (s *ConfigAPI) UpdateConfig(ctx context.Context, req *connect.Request[procmeshv1.UpdateConfigRequest]) (*connect.Response[procmeshv1.UpdateConfigResponse], error) {
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetExpectedRevision() <= 0 {
		return nil, ToConnect(errcode.E(errcode.INVALID, "expected_revision required"))
	}
	existing, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	spec := ProtoToSpec(req.Msg.GetSpec())
	spec.ProcessID = existing.ProcessID
	got, err := s.Mgr.ApplySpec(ctx, spec, req.Msg.GetExpectedRevision(), opID, operator, req.Msg.GetComment())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.UpdateConfigResponse{Spec: SpecToProto(got)}), nil
}

func (s *ConfigAPI) History(ctx context.Context, req *connect.Request[procmeshv1.HistoryRequest]) (*connect.Response[procmeshv1.HistoryResponse], error) {
	if err := requireMgr(s.Mgr); err != nil {
		return nil, err
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	revs, err := s.Mgr.ListRevisions(ctx, spec.ProcessID)
	if err != nil {
		return nil, ToConnect(err)
	}
	out := &procmeshv1.HistoryResponse{Revisions: make([]*procmeshv1.Revision, 0, len(revs))}
	for _, r := range revs {
		out.Revisions = append(out.Revisions, &procmeshv1.Revision{
			Revision:        r.Revision,
			Operator:        r.Operator,
			TimestampUnixMs: r.Timestamp.UnixMilli(),
			Diff:            r.Diff,
			Comment:         r.Comment,
		})
	}
	return connect.NewResponse(out), nil
}

func (s *ConfigAPI) Diff(ctx context.Context, req *connect.Request[procmeshv1.DiffRequest]) (*connect.Response[procmeshv1.DiffResponse], error) {
	if err := requireMgr(s.Mgr); err != nil {
		return nil, err
	}
	if s.Revs == nil {
		return nil, ToConnect(errcode.E(errcode.DEGRADED, "diff unavailable"))
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	from, err := s.Revs.GetRevisionSpec(ctx, spec.ProcessID, req.Msg.GetFromRevision())
	if err != nil {
		return nil, ToConnect(err)
	}
	to, err := s.Revs.GetRevisionSpec(ctx, spec.ProcessID, req.Msg.GetToRevision())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.DiffResponse{Diff: store.SpecDiff(from, to)}), nil
}

func (s *ConfigAPI) Rollback(ctx context.Context, req *connect.Request[procmeshv1.RollbackRequest]) (*connect.Response[procmeshv1.RollbackResponse], error) {
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	existing, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	got, err := s.Mgr.Rollback(ctx, existing.ProcessID, req.Msg.GetToRevision(), req.Msg.GetExpectedRevision(), opID, operator, req.Msg.GetComment())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.RollbackResponse{Spec: SpecToProto(got)}), nil
}

func (s *ConfigAPI) rejectMutation() error {
	if s.Degraded != nil && s.Degraded() {
		return ToConnect(errcode.E(errcode.DEGRADED, "degraded"))
	}
	return nil
}
