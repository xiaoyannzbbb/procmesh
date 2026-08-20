package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.ClusterBackupServiceHandler = (*ClusterBackupAPI)(nil)

// ClusterBackupForwarder is intentionally separate from Forwarder so existing
// owner forwarding implementations remain source compatible.
type ClusterBackupForwarder interface {
	ClusterBackup(context.Context, Route) (procmeshv1connect.ClusterBackupServiceClient, error)
}

type ClusterBackupAPI struct {
	Auth              *auth.Service
	Control           *control.Node
	ControlFn         func() *control.Node
	StateFn           func() control.State
	ApplyFn           func(control.Command, time.Duration) error
	IsLeader          func() bool
	LeaderTerm        func() uint64
	LeaderAddr        func() string
	LeaderRoute       func() (Route, bool)
	Forward           any
	Router            *Router
	Members           func() []string
	LocalOnly         bool
	LocalID           string
	Now               func() time.Time
	DestinationHealth func(context.Context, string, string) backup.DestinationHealth
	DispatchRun       func(backup.FrozenRun)
}

func (s *ClusterBackupAPI) CreatePolicy(ctx context.Context, req *connect.Request[procmeshv1.CreateClusterBackupPolicyRequest]) (*connect.Response[procmeshv1.CreateClusterBackupPolicyResponse], error) {
	if err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	operationID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if local, cli, err := s.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		return cli.CreatePolicy(ctx, req)
	}
	policy := req.Msg.GetPolicy()
	if policy == nil {
		return nil, ToConnect(errcode.E(errcode.INVALID, "policy required"))
	}
	if err := s.apply(ctx, control.CmdBackupPolicyPut, policyBody(operationID, policy)); err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.CreateClusterBackupPolicyResponse{Policy: s.policyByID(policy.GetPolicyId())}), nil
}

func (s *ClusterBackupAPI) UpdatePolicy(ctx context.Context, req *connect.Request[procmeshv1.UpdateClusterBackupPolicyRequest]) (*connect.Response[procmeshv1.UpdateClusterBackupPolicyResponse], error) {
	if err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	operationID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if local, cli, err := s.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		return cli.UpdatePolicy(ctx, req)
	}
	policy := req.Msg.GetPolicy()
	if policy == nil {
		return nil, ToConnect(errcode.E(errcode.INVALID, "policy required"))
	}
	if err := s.apply(ctx, control.CmdBackupPolicyPut, policyBody(operationID, policy)); err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.UpdateClusterBackupPolicyResponse{Policy: s.policyByID(policy.GetPolicyId())}), nil
}

func (s *ClusterBackupAPI) DeletePolicy(ctx context.Context, req *connect.Request[procmeshv1.DeleteClusterBackupPolicyRequest]) (*connect.Response[procmeshv1.DeleteClusterBackupPolicyResponse], error) {
	if err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	operationID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if local, cli, err := s.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		return cli.DeletePolicy(ctx, req)
	}
	if err := s.apply(ctx, control.CmdBackupPolicyDelete, control.BackupPolicyDeleteBody{OperationID: operationID, PolicyID: req.Msg.GetPolicyId()}); err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.DeleteClusterBackupPolicyResponse{}), nil
}

func (s *ClusterBackupAPI) ListPolicies(ctx context.Context, req *connect.Request[procmeshv1.ListClusterBackupPoliciesRequest]) (*connect.Response[procmeshv1.ListClusterBackupPoliciesResponse], error) {
	if err := s.requireRead(ctx); err != nil {
		return nil, err
	}
	state := s.state()
	ids := make([]string, 0, len(state.BackupPolicies))
	for id := range state.BackupPolicies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := &procmeshv1.ListClusterBackupPoliciesResponse{Policies: make([]*procmeshv1.ClusterBackupPolicy, 0, len(ids))}
	for _, id := range ids {
		out.Policies = append(out.Policies, policyToProto(state.BackupPolicies[id]))
	}
	return connect.NewResponse(out), nil
}

func (s *ClusterBackupAPI) ValidatePolicy(ctx context.Context, req *connect.Request[procmeshv1.ValidateClusterBackupPolicyRequest]) (*connect.Response[procmeshv1.ValidateClusterBackupPolicyResponse], error) {
	if err := s.requireRead(ctx); err != nil {
		return nil, err
	}
	policy := req.Msg.GetPolicy()
	if policy == nil {
		return connect.NewResponse(&procmeshv1.ValidateClusterBackupPolicyResponse{Valid: false, Errors: []*procmeshv1.ErrorInfo{{Code: string(errcode.INVALID), Message: "policy required"}}}), nil
	}
	state := s.state()
	cmd, err := control.EncodeCommand(control.CmdBackupPolicyPut, policyBody("validate", policy))
	if err != nil {
		return nil, ToConnect(err)
	}
	clone := control.NewState()
	clone.Members, clone.AgentGroups = state.Members, state.AgentGroups
	if err := clone.Apply(cmd, s.now()); err != nil {
		return connect.NewResponse(&procmeshv1.ValidateClusterBackupPolicyResponse{Valid: false, Errors: []*procmeshv1.ErrorInfo{{Code: errorCode(err), Message: err.Error()}}}), nil
	}
	return connect.NewResponse(&procmeshv1.ValidateClusterBackupPolicyResponse{Valid: true}), nil
}

func (s *ClusterBackupAPI) StartRun(ctx context.Context, req *connect.Request[procmeshv1.StartClusterBackupRunRequest]) (*connect.Response[procmeshv1.StartClusterBackupRunResponse], error) {
	if err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	operationID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if local, cli, ferr := s.forwardMutation(ctx, req.Header()); !local {
		if ferr != nil {
			return nil, ferr
		}
		return cli.StartRun(ctx, req)
	}
	state := s.state()
	policy, ok := state.BackupPolicies[req.Msg.GetPolicyId()]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "backup policy not found"))
	}
	targets := append([]string(nil), req.Msg.GetTargetNodeIds()...)
	if len(targets) == 0 {
		targets = resolveTargets(state, policy)
	}
	if len(targets) == 0 {
		return nil, ToConnect(errcode.E(errcode.INVALID, "target nodes required"))
	}
	if err := validateStartTargets(state, policy, targets); err != nil {
		return nil, ToConnect(err)
	}
	now := s.now()
	runID := newRunID(operationID, policy.PolicyID, now)
	timeout := policy.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	leaseUntil := now.Add(time.Duration(timeout) * time.Second).Unix()
	run := control.ClusterBackupRun{RunID: runID, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision, TargetNodeIDs: targets, Status: "RUNNING", CreatedUnix: now.Unix(), StartedUnix: now.Unix(), Sink: policy.Sink, DestinationProfile: policy.DestinationProfile, MaxConcurrency: policy.MaxConcurrency, TimeoutSeconds: timeout, UnavailablePolicy: policy.UnavailablePolicy, LeaseUntilUnix: leaseUntil}
	term := s.leaderTerm()
	cmd, err := control.EncodeCommand(control.CmdBackupRunCreate, control.CreateRunBody{OperationID: operationID, LeaderTerm: term, Run: run})
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := s.applyCommand(cmd); err != nil {
		return nil, ToConnect(err)
	}
	if s.DispatchRun != nil {
		s.DispatchRun(backup.FrozenRun{
			RunID: runID, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
			TargetNodeIDs: append([]string(nil), targets...), Sink: policy.Sink,
			DestinationProfile: policy.DestinationProfile, MaxConcurrency: policy.MaxConcurrency,
			LeaderTerm: term, LeaseExpiresUnix: leaseUntil, TimeoutSeconds: timeout, UnavailablePolicy: policy.UnavailablePolicy,
		})
	}
	return connect.NewResponse(&procmeshv1.StartClusterBackupRunResponse{Run: runToProto(run, nil)}), nil
}

func (s *ClusterBackupAPI) GetRun(ctx context.Context, req *connect.Request[procmeshv1.GetClusterBackupRunRequest]) (*connect.Response[procmeshv1.GetClusterBackupRunResponse], error) {
	if err := s.requireRead(ctx); err != nil {
		return nil, err
	}
	state := s.state()
	run, ok := state.BackupRuns[req.Msg.GetRunId()]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "backup run not found"))
	}
	tasks := make([]control.ClusterBackupTask, 0, len(run.TargetNodeIDs))
	for _, nodeID := range run.TargetNodeIDs {
		var found bool
		for _, task := range state.BackupTasks {
			if task.RunID == run.RunID && task.NodeID == nodeID {
				tasks = append(tasks, task)
				found = true
				break
			}
		}
		if !found {
			tasks = append(tasks, control.ClusterBackupTask{RunID: run.RunID, TaskID: "task-" + nodeID, NodeID: nodeID, Status: "UNAVAILABLE", ErrorCode: string(errcode.UNAVAILABLE), ErrorSummary: "agent unavailable"})
		}
	}
	return connect.NewResponse(&procmeshv1.GetClusterBackupRunResponse{Run: runToProto(run, tasks)}), nil
}

func (s *ClusterBackupAPI) ListRuns(ctx context.Context, req *connect.Request[procmeshv1.ListClusterBackupRunsRequest]) (*connect.Response[procmeshv1.ListClusterBackupRunsResponse], error) {
	if err := s.requireRead(ctx); err != nil {
		return nil, err
	}
	state := s.state()
	runs := make([]control.ClusterBackupRun, 0)
	for _, run := range state.BackupRuns {
		if req.Msg.GetPolicyId() == "" || run.PolicyID == req.Msg.GetPolicyId() {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedUnix == runs[j].CreatedUnix {
			return runs[i].RunID > runs[j].RunID
		}
		return runs[i].CreatedUnix > runs[j].CreatedUnix
	})
	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(runs) > limit {
		runs = runs[:limit]
	}
	out := &procmeshv1.ListClusterBackupRunsResponse{Runs: make([]*procmeshv1.ClusterBackupRun, 0, len(runs))}
	for _, run := range runs {
		out.Runs = append(out.Runs, runToProto(run, nil))
	}
	return connect.NewResponse(out), nil
}

func (s *ClusterBackupAPI) RetryFailedTasks(ctx context.Context, req *connect.Request[procmeshv1.RetryFailedClusterBackupTasksRequest]) (*connect.Response[procmeshv1.RetryFailedClusterBackupTasksResponse], error) {
	if err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if local, cli, ferr := s.forwardMutation(ctx, req.Header()); !local {
		if ferr != nil {
			return nil, ferr
		}
		return cli.RetryFailedTasks(ctx, req)
	}
	state := s.state()
	run, ok := state.BackupRuns[req.Msg.GetRunId()]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "backup run not found"))
	}
	operationID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	now := s.now()
	timeout := run.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	term := s.leaderTerm()
	leaseUntil := now.Add(time.Duration(timeout) * time.Second).Unix()
	cmd, err := control.EncodeCommand(control.CmdBackupRetryFailedTasks, control.RetryFailedTasksBody{OperationID: operationID, RunID: run.RunID, LeaderTerm: term, UpdatedUnix: now.Unix(), LeaseUntilUnix: leaseUntil})
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := s.applyCommand(cmd); err != nil {
		return nil, ToConnect(err)
	}
	state = s.state()
	run = state.BackupRuns[run.RunID]
	if s.DispatchRun != nil {
		pending := make(map[string]bool, len(run.TargetNodeIDs))
		for _, task := range state.BackupTasks {
			if task.RunID == run.RunID && task.Status == "PENDING" {
				pending[task.NodeID] = true
			}
		}
		targets := make([]string, 0, len(pending))
		for _, nodeID := range run.TargetNodeIDs {
			if pending[nodeID] {
				targets = append(targets, nodeID)
			}
		}
		if len(targets) > 0 {
			s.DispatchRun(backup.FrozenRun{RunID: run.RunID, PolicyID: run.PolicyID, PolicyRevision: run.PolicyRevision, TargetNodeIDs: targets, Sink: run.Sink, DestinationProfile: run.DestinationProfile, MaxConcurrency: run.MaxConcurrency, LeaderTerm: term, LeaseExpiresUnix: leaseUntil, TimeoutSeconds: timeout, UnavailablePolicy: run.UnavailablePolicy})
		}
	}
	return connect.NewResponse(&procmeshv1.RetryFailedClusterBackupTasksResponse{Run: runToProto(run, nil)}), nil
}

func (s *ClusterBackupAPI) GetDestinationHealth(ctx context.Context, req *connect.Request[procmeshv1.GetClusterBackupDestinationHealthRequest]) (*connect.Response[procmeshv1.GetClusterBackupDestinationHealthResponse], error) {
	if err := s.requireRead(ctx); err != nil {
		return nil, err
	}
	health := backup.DestinationHealth{Sink: req.Msg.GetSink(), DestinationProfile: req.Msg.GetDestinationProfile(), Status: "UNKNOWN"}
	if s.DestinationHealth != nil {
		health = s.DestinationHealth(ctx, req.Msg.GetSink(), req.Msg.GetDestinationProfile())
	}
	return connect.NewResponse(&procmeshv1.GetClusterBackupDestinationHealthResponse{Health: &procmeshv1.ClusterBackupDestinationHealth{
		Sink: health.Sink, DestinationProfile: health.DestinationProfile, EndpointHost: health.EndpointHost,
		Status: health.Status, ErrorSummary: health.ErrorSummary, CheckedUnix: s.now().Unix(), NodeId: s.LocalID,
	}}), nil
}

func (s *ClusterBackupAPI) requireWrite(ctx context.Context) error {
	return requirePerm(ctx, s.Auth, auth.PermBackupManage, s.LocalID, true, true)
}
func (s *ClusterBackupAPI) requireRead(ctx context.Context) error {
	return requirePerm(ctx, s.Auth, auth.PermBackupRead, s.LocalID, false, true)
}
func (s *ClusterBackupAPI) state() control.State {
	if s.StateFn != nil {
		v := s.StateFn()
		v.EnsureForTest()
		return v
	}
	if n := s.controlNode(); n != nil {
		return n.View()
	}
	return *control.NewState()
}
func (s *ClusterBackupAPI) controlNode() *control.Node {
	if s.ControlFn != nil {
		if n := s.ControlFn(); n != nil {
			return n
		}
	}
	return s.Control
}
func (s *ClusterBackupAPI) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *ClusterBackupAPI) leaderTerm() uint64 {
	if s.LeaderTerm != nil && s.LeaderTerm() > 0 {
		return s.LeaderTerm()
	}
	return 1
}
func (s *ClusterBackupAPI) apply(ctx context.Context, typ string, body any) error {
	cmd, err := control.EncodeCommand(typ, body)
	if err != nil {
		return err
	}
	return s.applyCommand(cmd)
}
func (s *ClusterBackupAPI) applyCommand(cmd control.Command) error {
	if s.ApplyFn != nil {
		return s.ApplyFn(cmd, 5*time.Second)
	}
	n := s.controlNode()
	if n == nil {
		return errcode.E(errcode.UNAVAILABLE, "control plane unavailable")
	}
	return n.Apply(cmd, 5*time.Second)
}

func (s *ClusterBackupAPI) forwardMutation(ctx context.Context, header http.Header) (bool, procmeshv1connect.ClusterBackupServiceClient, error) {
	if s.LocalOnly || s.isLeader() {
		return true, nil, nil
	}
	f, ok := s.Forward.(ClusterBackupForwarder)
	if !ok || f == nil {
		return false, nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader unavailable"))
	}
	rt, ok := s.leaderRoute()
	if !ok || rt.Local {
		return false, nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader unavailable"))
	}
	stampHop(header, s.LocalID, rt.NodeID)
	stampIdentity(header, ctx)
	cli, err := f.ClusterBackup(ctx, rt)
	if err != nil {
		return false, nil, ToConnect(rpc.MapDialError(err))
	}
	return false, cli, nil
}
func (s *ClusterBackupAPI) isLeader() bool {
	if s.IsLeader != nil {
		return s.IsLeader()
	}
	n := s.controlNode()
	return n == nil || n.IsLeader()
}
func (s *ClusterBackupAPI) leaderRoute() (Route, bool) {
	if s.LeaderRoute != nil {
		return s.LeaderRoute()
	}
	if s.Router != nil {
		leaderAddr := ""
		if s.LeaderAddr != nil {
			leaderAddr = s.LeaderAddr()
		} else if n := s.controlNode(); n != nil {
			leaderAddr = n.LeaderAddr()
		}
		if leaderAddr == "" {
			return Route{}, false
		}
		state := s.state()
		for _, n := range s.Router.members() {
			member, ok := state.Members[n.NodeID]
			if !ok || member.RaftAddr != leaderAddr {
				continue
			}
			if n.NodeID != s.LocalID {
				rt, err := s.Router.routeForNode(n)
				if err == nil {
					return rt, true
				}
			}
		}
	}
	return Route{}, false
}

func policyBody(operationID string, p *procmeshv1.ClusterBackupPolicy) control.BackupPolicyPutBody {
	return control.BackupPolicyPutBody{OperationID: operationID, PolicyID: p.GetPolicyId(), Name: p.GetName(), Enabled: p.GetEnabled(), ScheduleCron: p.GetScheduleCron(), Timezone: p.GetTimezone(), TargetSelector: p.GetTargetSelector(), TargetIDs: p.GetTargetNodeIds(), Sink: p.GetSink(), DestinationProfile: p.GetDestinationProfile(), RetentionKeepLast: int(p.GetRetentionKeepLast()), RetentionKeepDays: int(p.GetRetentionKeepDays()), RetentionMaxBytes: p.GetRetentionMaxBytes(), TimeoutSeconds: int(p.GetTimeoutSeconds()), MaxConcurrency: int(p.GetMaxConcurrency()), UnavailablePolicy: p.GetUnavailablePolicy()}
}
func policyToProto(p control.BackupPolicy) *procmeshv1.ClusterBackupPolicy {
	return &procmeshv1.ClusterBackupPolicy{PolicyId: p.PolicyID, Name: p.Name, Enabled: p.Enabled, ScheduleCron: p.ScheduleCron, Timezone: p.Timezone, TargetSelector: p.TargetSelector, TargetNodeIds: append([]string(nil), p.TargetIDs...), Sink: p.Sink, DestinationProfile: p.DestinationProfile, RetentionKeepLast: int32(p.RetentionKeepLast), RetentionKeepDays: int32(p.RetentionKeepDays), RetentionMaxBytes: p.RetentionMaxBytes, TimeoutSeconds: int32(p.TimeoutSeconds), MaxConcurrency: int32(p.MaxConcurrency), UnavailablePolicy: p.UnavailablePolicy, Revision: p.Revision}
}
func runToProto(r control.ClusterBackupRun, tasks []control.ClusterBackupTask) *procmeshv1.ClusterBackupRun {
	out := &procmeshv1.ClusterBackupRun{RunId: r.RunID, PolicyId: r.PolicyID, PolicyRevision: r.PolicyRevision, TargetNodeIds: append([]string(nil), r.TargetNodeIDs...), Status: r.Status, Success: int32(r.Success), Failed: int32(r.Failed), Unavailable: int32(r.Unavailable), Timeout: int32(r.Timeout), CreatedUnix: r.CreatedUnix, StartedUnix: r.StartedUnix, FinishedUnix: r.FinishedUnix}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, taskToProto(t))
	}
	return out
}
func taskToProto(t control.ClusterBackupTask) *procmeshv1.ClusterBackupTask {
	return &procmeshv1.ClusterBackupTask{RunId: t.RunID, TaskId: t.TaskID, NodeId: t.NodeID, SnapshotId: t.SnapshotID, Sha256: t.SHA256, Status: t.Status, Bytes: t.Bytes, ErrorCode: t.ErrorCode, ErrorSummary: t.ErrorSummary, LeaderTerm: t.LeaderTerm, UpdatedUnix: t.UpdatedUnix}
}
func (s *ClusterBackupAPI) policyByID(id string) *procmeshv1.ClusterBackupPolicy {
	p, ok := s.state().BackupPolicies[id]
	if !ok {
		return nil
	}
	return policyToProto(p)
}
func resolveTargets(st control.State, p control.BackupPolicy) []string {
	if p.TargetSelector == "EXPLICIT_NODES" {
		return append([]string(nil), p.TargetIDs...)
	}
	if p.TargetSelector == "AGENT_GROUP" && len(p.TargetIDs) > 0 {
		seen := map[string]struct{}{}
		ids := make([]string, 0)
		for _, groupID := range p.TargetIDs {
			for _, nodeID := range st.AgentGroups[groupID].MemberIDs {
				if member, ok := st.Members[nodeID]; ok && member.Status == control.MemberAdmitted {
					if _, exists := seen[nodeID]; !exists {
						seen[nodeID] = struct{}{}
						ids = append(ids, nodeID)
					}
				}
			}
		}
		sort.Strings(ids)
		return ids
	}
	ids := make([]string, 0, len(st.Members))
	for id, m := range st.Members {
		if m.Status == control.MemberAdmitted {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func validateStartTargets(st control.State, p control.BackupPolicy, targets []string) error {
	seen := make(map[string]struct{}, len(targets))
	for _, id := range targets {
		if id == "" {
			return errcode.E(errcode.INVALID, "target node id required")
		}
		if _, ok := seen[id]; ok {
			return errcode.E(errcode.INVALID, "duplicate target node")
		}
		seen[id] = struct{}{}
	}
	switch p.TargetSelector {
	case "EXPLICIT_NODES":
		if !equalStringSlices(targets, p.TargetIDs) {
			return errcode.E(errcode.CONFLICT, "target nodes changed")
		}
	case "ALL_ADMITTED":
		admitted := make([]string, 0, len(st.Members))
		for id, member := range st.Members {
			if member.Status == control.MemberAdmitted {
				admitted = append(admitted, id)
			}
		}
		sort.Strings(admitted)
		got := append([]string(nil), targets...)
		sort.Strings(got)
		if !equalStringSlices(got, admitted) {
			return errcode.E(errcode.CONFLICT, "target nodes changed")
		}
	case "AGENT_GROUP":
		allowed := map[string]struct{}{}
		for _, groupID := range p.TargetIDs {
			group, ok := st.AgentGroups[groupID]
			if !ok {
				return errcode.E(errcode.INVALID, "agent group not found")
			}
			for _, nodeID := range group.MemberIDs {
				if member, ok := st.Members[nodeID]; ok && member.Status == control.MemberAdmitted {
					allowed[nodeID] = struct{}{}
				}
			}
		}
		got := make([]string, 0, len(targets))
		for _, id := range targets {
			if _, ok := allowed[id]; !ok {
				return errcode.E(errcode.INVALID, "target node not in agent group")
			}
			got = append(got, id)
		}
		want := make([]string, 0, len(allowed))
		for id := range allowed {
			want = append(want, id)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !equalStringSlices(got, want) {
			return errcode.E(errcode.CONFLICT, "target nodes changed")
		}
	}
	return nil
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func newRunID(op, policy string, now time.Time) string {
	sum := sha256.Sum256([]byte(op + "\x00" + policy + "\x00" + now.String()))
	return "run-" + hex.EncodeToString(sum[:12])
}
func errorCode(err error) string {
	if errcode.Is(err, errcode.INVALID) {
		return string(errcode.INVALID)
	}
	if errcode.Is(err, errcode.CONFLICT) {
		return string(errcode.CONFLICT)
	}
	if errcode.Is(err, errcode.NOT_FOUND) {
		return string(errcode.NOT_FOUND)
	}
	return string(errcode.UNAVAILABLE)
}
