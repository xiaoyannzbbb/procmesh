package api

import (
	"context"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.BatchServiceHandler = (*BatchAPI)(nil)

// BatchAPI serves entry-local BatchService RPCs.
type BatchAPI struct {
	Auth     *auth.Service
	Engine   *batch.Engine
	Store    *store.Store
	LocalID  string
	Mgr      *process.Manager
	Router   *Router
	Forward  Forwarder
	Members  func() []cluster.NodeSummary
	Degraded func() bool
	Process  ProcessRemotePolicy

	identMu sync.Mutex
	idents  map[string]auth.Principal
}

func (s *BatchAPI) CreateBatch(ctx context.Context, req *connect.Request[procmeshv1.CreateBatchRequest]) (*connect.Response[procmeshv1.CreateBatchResponse], error) {
	if err := s.requireExecute(ctx); err != nil {
		return nil, err
	}
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	typ := batch.Type(req.Msg.GetType())
	if err := validateCreateConfig(typ, req.Msg.GetConfig()); err != nil {
		return nil, ToConnect(err)
	}
	s.ensureExec()
	b, err := s.create(ctx, operatorOf(ctx, operator), typ, selectorFromProto(req.Msg.GetSelector()), req.Msg.GetConfig(), req.Msg.GetComment())
	if err != nil {
		return nil, ToConnect(err)
	}
	s.rememberPrincipal(ctx, b)
	s.audit(ctx, "batch.create", b.BatchID, opID)
	return connect.NewResponse(&procmeshv1.CreateBatchResponse{Batch: batchToProto(b, true)}), nil
}

func (s *BatchAPI) GetBatch(ctx context.Context, req *connect.Request[procmeshv1.GetBatchRequest]) (*connect.Response[procmeshv1.GetBatchResponse], error) {
	if err := s.requireExecute(ctx); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	b, err := s.Engine.Get(ctx, req.Msg.GetBatchId())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.GetBatchResponse{Batch: batchToProto(b, true)}), nil
}

func (s *BatchAPI) ListBatches(ctx context.Context, req *connect.Request[procmeshv1.ListBatchesRequest]) (*connect.Response[procmeshv1.ListBatchesResponse], error) {
	if err := s.requireExecute(ctx); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	list, err := s.Engine.List(ctx, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, ToConnect(err)
	}
	out := &procmeshv1.ListBatchesResponse{Batches: make([]*procmeshv1.Batch, 0, len(list))}
	for _, b := range list {
		out.Batches = append(out.Batches, batchToProto(b, false))
	}
	return connect.NewResponse(out), nil
}

func (s *BatchAPI) RetryFailed(ctx context.Context, req *connect.Request[procmeshv1.RetryBatchRequest]) (*connect.Response[procmeshv1.RetryBatchResponse], error) {
	return s.mutateBatch(ctx, req, "batch.retry_failed", func(ctx context.Context, id, op string) (batch.Batch, error) {
		return s.Engine.RetryFailed(ctx, id, op)
	})
}

func (s *BatchAPI) ReplayTimeout(ctx context.Context, req *connect.Request[procmeshv1.RetryBatchRequest]) (*connect.Response[procmeshv1.RetryBatchResponse], error) {
	return s.mutateBatch(ctx, req, "batch.replay_timeout", func(ctx context.Context, id, op string) (batch.Batch, error) {
		return s.Engine.ReplayTimeout(ctx, id, op)
	})
}

func (s *BatchAPI) ExportBatch(ctx context.Context, req *connect.Request[procmeshv1.ExportBatchRequest]) (*connect.Response[procmeshv1.ExportBatchResponse], error) {
	if err := s.requireExecute(ctx); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	content, ct, name, err := s.Engine.Export(ctx, req.Msg.GetBatchId(), req.Msg.GetFormat())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.ExportBatchResponse{
		Content: content, ContentType: ct, Filename: name,
	}), nil
}

func (s *BatchAPI) mutateBatch(ctx context.Context, req *connect.Request[procmeshv1.RetryBatchRequest], action string, fn func(context.Context, string, string) (batch.Batch, error)) (*connect.Response[procmeshv1.RetryBatchResponse], error) {
	if err := s.requireExecute(ctx); err != nil {
		return nil, err
	}
	if err := s.rejectMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	s.ensureExec()
	b, err := fn(ctx, req.Msg.GetBatchId(), operatorOf(ctx, operator))
	if err != nil {
		return nil, ToConnect(err)
	}
	s.rememberPrincipal(ctx, b)
	s.audit(ctx, action, b.BatchID, opID)
	return connect.NewResponse(&procmeshv1.RetryBatchResponse{Batch: batchToProto(b, true)}), nil
}

func (s *BatchAPI) create(ctx context.Context, operator string, typ batch.Type, sel batch.Selector, cfg *procmeshv1.ProcessSpec, comment string) (batch.Batch, error) {
	expand := s.Engine.Expand
	if expand == nil {
		expand = s.newExpander(ctx, cfg)
	}
	return s.Engine.CreateWithExpand(ctx, operator, typ, sel, comment, expand)
}

func (s *BatchAPI) requireExecute(ctx context.Context) error {
	return requireAnyPerm(ctx, s.Auth, auth.PermBatchExecute)
}

func (s *BatchAPI) requireEngine() error {
	if s == nil || s.Engine == nil {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "batch not configured"))
	}
	return nil
}

func (s *BatchAPI) rejectMutation() error {
	if s.Degraded != nil && s.Degraded() {
		return ToConnect(errcode.E(errcode.DEGRADED, "degraded"))
	}
	return nil
}

func (s *BatchAPI) ensureExec() {
	if s.Engine == nil {
		return
	}
	if s.Engine.Exec == nil {
		s.Engine.Exec = &batchExecutor{api: s}
	}
	if s.Engine.BindTargets == nil {
		s.Engine.BindTargets = s.bindTargets
	}
}

func (s *BatchAPI) audit(ctx context.Context, action, batchID, opID string) {
	if s.Store == nil {
		return
	}
	ev := store.AuditEvent{
		Resource:    "batch:" + batchID,
		Action:      action,
		OperationID: opID,
		Result:      "SUCCESS",
		SourceAgent: s.LocalID,
	}
	if p, ok := PrincipalFrom(ctx); ok {
		ev.UserID = p.UserID
		ev.Username = p.Username
	}
	_ = s.Store.AppendAudit(ctx, ev)
}

func (s *BatchAPI) rememberPrincipal(ctx context.Context, b batch.Batch) {
	s.bindTargets(ctx, b.Targets)
}

func (s *BatchAPI) bindTargets(ctx context.Context, targets []batch.Target) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return
	}
	s.identMu.Lock()
	defer s.identMu.Unlock()
	if s.idents == nil {
		s.idents = make(map[string]auth.Principal)
	}
	for _, t := range targets {
		if t.OperationID != "" {
			s.idents[t.OperationID] = p
		}
	}
}

func (s *BatchAPI) principalFor(opID string) (auth.Principal, bool) {
	if s == nil {
		return auth.Principal{}, false
	}
	s.identMu.Lock()
	defer s.identMu.Unlock()
	p, ok := s.idents[opID]
	return p, ok
}

func operatorOf(ctx context.Context, metaOp string) string {
	if strings.TrimSpace(metaOp) != "" {
		return metaOp
	}
	if p, ok := PrincipalFrom(ctx); ok {
		if p.Username != "" {
			return p.Username
		}
		if p.UserID != "" {
			return p.UserID
		}
	}
	return "unknown"
}

func validateCreateConfig(typ batch.Type, cfg *procmeshv1.ProcessSpec) error {
	if typ == batch.TypeConfigUpdate {
		if cfg == nil {
			return errcode.E(errcode.INVALID, "config")
		}
		return nil
	}
	if cfg != nil {
		return errcode.E(errcode.INVALID, "config")
	}
	return nil
}

func selectorFromProto(s *procmeshv1.BatchSelector) batch.Selector {
	if s == nil {
		return batch.Selector{}
	}
	out := batch.Selector{
		ProcessIDs:   s.GetProcessIds(),
		AgentGroupID: s.GetAgentGroupId(),
		ProcessGroup: s.GetProcessGroup(),
	}
	for _, n := range s.GetProcessNames() {
		if n == nil {
			continue
		}
		out.ProcessNames = append(out.ProcessNames, batch.ProcessNameRef{
			NodeID: n.GetNodeId(), ProcessName: n.GetProcessName(),
		})
	}
	return out
}

func selectorToProto(s batch.Selector) *procmeshv1.BatchSelector {
	out := &procmeshv1.BatchSelector{
		ProcessIds:   s.ProcessIDs,
		AgentGroupId: s.AgentGroupID,
		ProcessGroup: s.ProcessGroup,
	}
	for _, n := range s.ProcessNames {
		out.ProcessNames = append(out.ProcessNames, &procmeshv1.ProcessNameRef{
			NodeId: n.NodeID, ProcessName: n.ProcessName,
		})
	}
	return out
}

func batchToProto(b batch.Batch, includeTargets bool) *procmeshv1.Batch {
	out := &procmeshv1.Batch{
		BatchId:     b.BatchID,
		Operator:    b.Operator,
		SourceAgent: b.SourceAgent,
		Type:        string(b.Type),
		Status:      string(b.Status),
		Selector:    selectorToProto(b.Selector),
		Summary: &procmeshv1.BatchSummary{
			Success:     int32(b.Summary.Success),
			Failed:      int32(b.Summary.Failed),
			Timeout:     int32(b.Summary.Timeout),
			Denied:      int32(b.Summary.Denied),
			Conflict:    int32(b.Summary.Conflict),
			Unavailable: int32(b.Summary.Unavailable),
			Invalid:     int32(b.Summary.Invalid),
		},
	}
	if !b.CreatedAt.IsZero() {
		out.CreatedUnixMs = b.CreatedAt.UnixMilli()
	}
	if includeTargets {
		out.Targets = make([]*procmeshv1.BatchTarget, 0, len(b.Targets))
		for _, t := range b.Targets {
			out.Targets = append(out.Targets, targetToProto(t))
		}
	}
	return out
}

func targetToProto(t batch.Target) *procmeshv1.BatchTarget {
	out := &procmeshv1.BatchTarget{
		OperationId:      t.OperationID,
		NodeId:           t.NodeID,
		ProcessId:        t.ProcessID,
		ProcessName:      t.ProcessName,
		Status:           string(t.Status),
		Error:            t.Error,
		ExpectedRevision: t.ExpectedRevision,
	}
	if !t.StartedAt.IsZero() {
		out.StartedUnixMs = t.StartedAt.UnixMilli()
	}
	if !t.FinishedAt.IsZero() {
		out.FinishedUnixMs = t.FinishedAt.UnixMilli()
	}
	return out
}

func batchAuditStore(opts Options) *store.Store {
	if s, ok := opts.Store.(*store.Store); ok {
		return s
	}
	return nil
}
