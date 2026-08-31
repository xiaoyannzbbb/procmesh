package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/update"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type clusterMembership struct {
	deps ClusterDeps
}

func (c clusterMembership) Members() []cluster.NodeSummary {
	return c.deps.members()
}

func (c clusterMembership) Now() time.Time {
	return c.deps.now()
}

func (s *UpdateAPI) CreateClusterUpdate(ctx context.Context, req *connect.Request[procmeshv1.CreateClusterUpdateRequest]) (*connect.Response[procmeshv1.CreateClusterUpdateResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterManage, "", true, true); err != nil {
		return nil, err
	}
	if err := s.rejectUpdateMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	s.ensureEngine()
	pin, err := s.resolvePin(ctx, req.Msg.GetPin())
	if err != nil {
		return nil, ToConnect(err)
	}
	specs := s.collectTargets(ctx, pin.Tag, false)
	job, err := s.Engine.Create(ctx, operatorOf(ctx, operator), pin, specs, s.liveLeaderID(), opID)
	if err != nil {
		return nil, ToConnect(err)
	}
	s.rememberPrincipal(ctx, job)
	s.auditUpdate(ctx, "update.create", "update_job:"+job.JobID, opID, s.LocalID, nil)
	return connect.NewResponse(&procmeshv1.CreateClusterUpdateResponse{Job: jobToProto(job, true)}), nil
}

func (s *UpdateAPI) GetUpdateJob(ctx context.Context, req *connect.Request[procmeshv1.GetUpdateJobRequest]) (*connect.Response[procmeshv1.GetUpdateJobResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	job, err := s.Engine.Get(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.GetUpdateJobResponse{Job: jobToProto(job, true)}), nil
}

func (s *UpdateAPI) ListUpdateJobs(ctx context.Context, req *connect.Request[procmeshv1.ListUpdateJobsRequest]) (*connect.Response[procmeshv1.ListUpdateJobsResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	list, err := s.Engine.List(ctx, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, ToConnect(err)
	}
	out := &procmeshv1.ListUpdateJobsResponse{Jobs: make([]*procmeshv1.UpdateJob, 0, len(list))}
	for _, job := range list {
		out.Jobs = append(out.Jobs, jobToProto(job, false))
	}
	return connect.NewResponse(out), nil
}

func (s *UpdateAPI) CancelRemaining(ctx context.Context, req *connect.Request[procmeshv1.CancelRemainingRequest]) (*connect.Response[procmeshv1.CancelRemainingResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterManage, "", true, true); err != nil {
		return nil, err
	}
	if err := s.rejectUpdateMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	job, err := s.Engine.CancelRemaining(ctx, req.Msg.GetJobId(), operatorOf(ctx, operator))
	if err != nil {
		return nil, ToConnect(err)
	}
	s.auditUpdate(ctx, "update.cancel_remaining", "update_job:"+job.JobID, opID, s.LocalID, nil)
	return connect.NewResponse(&procmeshv1.CancelRemainingResponse{Job: jobToProto(job, true)}), nil
}

func (s *UpdateAPI) RetryUpdateJob(ctx context.Context, req *connect.Request[procmeshv1.RetryUpdateJobRequest]) (*connect.Response[procmeshv1.RetryUpdateJobResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterManage, "", true, true); err != nil {
		return nil, err
	}
	if err := s.rejectUpdateMutation(); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	s.ensureEngine()
	job, err := s.Engine.Retry(ctx, req.Msg.GetJobId(), operatorOf(ctx, operator))
	if err != nil {
		return nil, ToConnect(err)
	}
	s.rememberPrincipal(ctx, job)
	s.auditUpdate(ctx, "update.retry", "update_job:"+job.JobID, opID, s.LocalID, nil)
	return connect.NewResponse(&procmeshv1.RetryUpdateJobResponse{Job: jobToProto(job, true)}), nil
}

func (s *UpdateAPI) ApplyNode(ctx context.Context, req *connect.Request[procmeshv1.ApplyNodeRequest]) (*connect.Response[procmeshv1.ApplyNodeResponse], error) {
	nodeID := strings.TrimSpace(req.Msg.GetNodeId())
	if nodeID == "" {
		nodeID = s.LocalID
	}
	local := s.LocalOnly || nodeID == s.LocalID
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, nodeID, true, local); err != nil {
		return nil, err
	}
	if err := s.rejectUpdateMutation(); err != nil {
		return nil, err
	}
	opID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	pin := pinFromProto(req.Msg.GetPin())
	if err := update.ValidatePin(pin); err != nil {
		return nil, ToConnect(err)
	}
	if !local {
		if err := s.forwardApplyNode(ctx, nodeID, pin, opID); err != nil {
			return nil, ToConnect(err)
		}
		s.auditUpdate(ctx, "update.apply", "node:"+nodeID, opID, nodeID, pinAuditMeta(pin))
		return connect.NewResponse(&procmeshv1.ApplyNodeResponse{}), nil
	}
	if err := s.applyLocal(ctx, pin); err != nil {
		return nil, ToConnect(err)
	}
	s.auditUpdate(ctx, "update.apply", "node:"+nodeID, opID, nodeID, pinAuditMeta(pin))
	return connect.NewResponse(&procmeshv1.ApplyNodeResponse{}), nil
}

// Apply implements update.NodeApplier for the rolling-update engine.
func (s *UpdateAPI) Apply(ctx context.Context, nodeID string, pin update.Pin, operationID string) error {
	ctx = s.withIdentity(ctx, operationID)
	if s.remoteMissingPrincipal(ctx, nodeID) {
		return errcode.E(errcode.TIMEOUT, "missing hop principal")
	}
	if nodeID == "" || nodeID == s.LocalID || s.LocalOnly {
		if err := s.applyLocal(ctx, pin); err != nil {
			return err
		}
		target := nodeID
		if target == "" {
			target = s.LocalID
		}
		s.auditUpdate(ctx, "update.apply", "node:"+target, operationID, target, pinAuditMeta(pin))
		return nil
	}
	return s.forwardApplyNode(ctx, nodeID, pin, operationID)
}

func (s *UpdateAPI) applyLocal(ctx context.Context, pin update.Pin) error {
	applier := s.localApply()
	if applier == nil {
		return errcode.E(errcode.UNAVAILABLE, "update applier unavailable")
	}
	return applier.Apply(ctx, pin)
}

func (s *UpdateAPI) localApply() LocalApplier {
	if s != nil && s.Applier != nil {
		return s.Applier
	}
	if s != nil {
		if a, ok := s.Local.(LocalApplier); ok {
			return a
		}
	}
	return nil
}

func (s *UpdateAPI) forwardApplyNode(ctx context.Context, nodeID string, pin update.Pin, operationID string) error {
	if s.LocalOnly || s.Router == nil || s.Forward == nil {
		return errcode.E(errcode.UNAVAILABLE, "owner unreachable")
	}
	rt, err := s.Router.Resolve(ctx, "", "", nodeID)
	if err != nil {
		return err
	}
	if rt.Local {
		return s.applyLocal(ctx, pin)
	}
	cli, err := s.remoteUpdate(ctx, rt)
	if err != nil {
		return err
	}
	req := connect.NewRequest(&procmeshv1.ApplyNodeRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: operationID, Operator: operatorOf(ctx, "")},
		NodeId: nodeID,
		Pin:    pinToProto(pin),
	})
	stampHop(req.Header(), s.LocalID, rt.NodeID)
	stampIdentity(req.Header(), ctx)
	_, err = cli.ApplyNode(ctx, req)
	if err != nil {
		return rpc.MapCallError(err)
	}
	return nil
}

func (s *UpdateAPI) resolvePin(ctx context.Context, reqPin *procmeshv1.UpdatePin) (update.Pin, error) {
	if reqPin != nil && strings.TrimSpace(reqPin.GetTag()) != "" {
		pin := pinFromProto(reqPin)
		if err := update.ValidatePin(pin); err != nil {
			return update.Pin{}, err
		}
		return pin, nil
	}
	if s.Checker == nil {
		return update.Pin{}, errcode.E(errcode.UNAVAILABLE, "update checker unavailable")
	}
	res, err := s.Checker.CheckLatest(ctx, false)
	if err != nil {
		return update.Pin{}, err
	}
	if strings.TrimSpace(res.Pin.Tag) == "" {
		if res.CheckError {
			return update.Pin{}, errcode.E(errcode.UNAVAILABLE, "update source failed")
		}
		return update.Pin{}, errcode.E(errcode.INVALID, "release tag required")
	}
	if err := update.ValidatePin(res.Pin); err != nil {
		return update.Pin{}, err
	}
	return res.Pin, nil
}

func (s *UpdateAPI) collectTargets(ctx context.Context, latestTag string, checkFailed bool) []update.TargetSpec {
	members := s.Cluster.members()
	now := s.Cluster.now()
	out := make([]update.TargetSpec, len(members))
	var wg sync.WaitGroup
	for i, member := range members {
		i, member := i, member
		out[i] = update.TargetSpec{NodeID: member.NodeID, Hostname: member.Hostname}
		eval := update.Evaluate(now, member, latestTag, checkFailed, nil, nil)
		if eval.SkipReason == update.SkipMACOS || eval.SkipReason == update.SkipSTALE ||
			eval.SkipReason == update.SkipUNKNOWN || eval.SkipReason == update.SkipFAILED ||
			eval.SkipReason == update.SkipSUSPECT {
			out[i].SkipReason = eval.SkipReason
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := s.probeNode(ctx, member.NodeID)
			got := update.Evaluate(now, member, latestTag, checkFailed, info, err)
			if !got.Eligible {
				out[i].SkipReason = got.SkipReason
			}
		}()
	}
	wg.Wait()
	return out
}

func (s *UpdateAPI) liveLeaderID() string {
	view := readRaftMembership(s.Cluster.raftMembershipReader())
	leaderID := ""
	if view != nil {
		leaderID = view.LeaderID
	}
	return update.LiveLeaderID(leaderID, s.Cluster.now(), s.Cluster.members())
}

func (s *UpdateAPI) requireEngine() error {
	if s == nil || s.Engine == nil {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "update engine unavailable"))
	}
	return nil
}

func (s *UpdateAPI) rejectUpdateMutation() error {
	if s != nil && s.Degraded != nil && s.Degraded() {
		return ToConnect(errcode.E(errcode.DEGRADED, "degraded"))
	}
	return nil
}

func (s *UpdateAPI) ensureEngine() {
	if s == nil || s.Engine == nil {
		return
	}
	if s.Engine.SourceAgent == "" {
		s.Engine.SourceAgent = s.LocalID
	}
	if s.Engine.Members == nil {
		s.Engine.Members = clusterMembership{s.Cluster}
	}
	if s.Engine.Apply == nil {
		s.Engine.Apply = s
	}
	if s.Engine.BindTargets == nil {
		s.Engine.BindTargets = s.bindTargets
	}
}

func (s *UpdateAPI) rememberPrincipal(ctx context.Context, job update.Job) {
	s.bindTargets(ctx, job.Targets)
}

func (s *UpdateAPI) bindTargets(ctx context.Context, targets []update.Target) {
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

func (s *UpdateAPI) principalFor(opID string) (auth.Principal, bool) {
	if s == nil {
		return auth.Principal{}, false
	}
	s.identMu.Lock()
	defer s.identMu.Unlock()
	p, ok := s.idents[opID]
	return p, ok
}

func (s *UpdateAPI) withIdentity(ctx context.Context, opID string) context.Context {
	if _, ok := PrincipalFrom(ctx); ok {
		return ctx
	}
	if p, ok := s.principalFor(opID); ok {
		return WithPrincipal(ctx, p)
	}
	return ctx
}

func (s *UpdateAPI) remoteMissingPrincipal(ctx context.Context, nodeID string) bool {
	if _, ok := PrincipalFrom(ctx); ok {
		return false
	}
	if s == nil || s.Router == nil {
		return false
	}
	if nodeID == "" || nodeID == s.LocalID || s.LocalOnly {
		return false
	}
	return true
}

func pinAuditMeta(pin update.Pin) map[string]string {
	meta := make(map[string]string)
	if tag := strings.TrimSpace(pin.Tag); tag != "" {
		meta["tag"] = tag
	}
	if repo := strings.TrimSpace(pin.Repository); repo != "" {
		meta["repository"] = repo
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func (s *UpdateAPI) auditUpdate(ctx context.Context, action, resource, opID, target string, metadata map[string]string) {
	if s == nil || s.Store == nil {
		return
	}
	ev := store.AuditEvent{
		Resource:    resource,
		Action:      action,
		OperationID: opID,
		Result:      "SUCCESS",
		SourceAgent: s.LocalID,
		TargetAgent: target,
	}
	if p, ok := PrincipalFrom(ctx); ok {
		ev.UserID = p.UserID
		ev.Username = p.Username
	}
	if len(metadata) > 0 {
		if raw, err := json.Marshal(metadata); err == nil {
			ev.Metadata = raw
		}
	}
	_ = s.Store.AppendAudit(ctx, ev)
}

func pinFromProto(p *procmeshv1.UpdatePin) update.Pin {
	if p == nil {
		return update.Pin{}
	}
	return update.Pin{Repository: p.GetRepository(), Tag: p.GetTag(), Checksums: p.GetChecksums()}
}

func pinToProto(p update.Pin) *procmeshv1.UpdatePin {
	return &procmeshv1.UpdatePin{Repository: p.Repository, Tag: p.Tag, Checksums: p.Checksums}
}

func jobToProto(j update.Job, includeTargets bool) *procmeshv1.UpdateJob {
	out := &procmeshv1.UpdateJob{
		JobId:       j.JobID,
		Operator:    j.Operator,
		SourceAgent: j.SourceAgent,
		Pin:         pinToProto(j.Pin),
		Status:      string(j.Status),
		Summary: &procmeshv1.UpdateJobSummary{
			Success:   int32(j.Summary.Success),
			Failed:    int32(j.Summary.Failed),
			Timeout:   int32(j.Summary.Timeout),
			Conflict:  int32(j.Summary.Conflict),
			Skipped:   int32(j.Summary.Skipped),
			Cancelled: int32(j.Summary.Cancelled),
		},
	}
	if !j.CreatedAt.IsZero() {
		out.CreatedUnixMs = j.CreatedAt.UnixMilli()
	}
	if !j.StartedAt.IsZero() {
		out.StartedUnixMs = j.StartedAt.UnixMilli()
	}
	if !j.FinishedAt.IsZero() {
		out.FinishedUnixMs = j.FinishedAt.UnixMilli()
	}
	if includeTargets {
		out.Targets = make([]*procmeshv1.UpdateJobTarget, 0, len(j.Targets))
		for _, t := range j.Targets {
			out.Targets = append(out.Targets, jobTargetToProto(t))
		}
	}
	return out
}

func jobTargetToProto(t update.Target) *procmeshv1.UpdateJobTarget {
	out := &procmeshv1.UpdateJobTarget{
		OperationId: t.OperationID,
		NodeId:      t.NodeID,
		Hostname:    t.Hostname,
		Status:      string(t.Status),
		SkipReason:  t.SkipReason,
		Error:       t.Error,
		OrderIndex:  int32(t.OrderIndex),
	}
	if !t.StartedAt.IsZero() {
		out.StartedUnixMs = t.StartedAt.UnixMilli()
	}
	if !t.FinishedAt.IsZero() {
		out.FinishedUnixMs = t.FinishedAt.UnixMilli()
	}
	return out
}
