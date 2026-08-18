package api

import (
	"context"
	"encoding/json"
	"net/http"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type configUpdatePayload struct {
	ExpectedRevision int64                   `json:"expected_revision"`
	Spec             *procmeshv1.ProcessSpec `json:"spec"`
}

type batchCluster struct {
	fn func() []cluster.NodeSummary
}

func (c batchCluster) Nodes() []batch.NodeView {
	if c.fn == nil {
		return nil
	}
	ms := c.fn()
	out := make([]batch.NodeView, 0, len(ms))
	for _, n := range ms {
		nv := batch.NodeView{NodeID: n.NodeID}
		for _, p := range n.Processes {
			nv.Processes = append(nv.Processes, batch.ProcView{
				ProcessID: p.ProcessID, Name: p.Name, Group: p.Group, LatestRevision: p.LatestRevision,
			})
		}
		out = append(out, nv)
	}
	return out
}

type batchGroups struct {
	auth *auth.Service
}

func (g batchGroups) Members(groupID string) ([]string, error) {
	if g.auth == nil || g.auth.Store() == nil {
		return nil, errcode.E(errcode.INVALID, "agent group")
	}
	ag, ok := g.auth.Store().View().AgentGroups[groupID]
	if !ok {
		return nil, errcode.E(errcode.INVALID, "agent group")
	}
	return append([]string(nil), ag.MemberIDs...), nil
}

type batchAuthorizer struct {
	svc *auth.Service
	p   auth.Principal
}

func (a batchAuthorizer) Allow(nodeID, processGroup, perm string) error {
	if a.svc == nil || a.p.UserID == "" {
		return nil
	}
	return a.svc.AllowOn(a.p, perm, control.CheckTarget{NodeID: nodeID, ProcessGroup: processGroup})
}

type batchSpecReader struct {
	api *BatchAPI
}

func (r *batchSpecReader) Get(ctx context.Context, nodeID, idOrName string) (batch.OwnerSpec, error) {
	if r == nil || r.api == nil {
		return batch.OwnerSpec{}, errcode.E(errcode.NOT_FOUND, "process")
	}
	return r.api.readOwnerSpec(ctx, nodeID, idOrName)
}

func (s *BatchAPI) newExpander(ctx context.Context, cfg *procmeshv1.ProcessSpec) *batch.RealExpander {
	p, _ := PrincipalFrom(ctx)
	x := &batch.RealExpander{
		Cluster: s.clusterView(),
		Groups:  batchGroups{auth: s.Auth},
		Specs:   &batchSpecReader{api: s},
		Auth:    batchAuthorizer{svc: s.Auth, p: p},
	}
	if cfg != nil {
		patch := cfg
		x.ConfigOverlay = func(owner batch.OwnerSpec) (string, int64, error) {
			return overlayConfig(owner, patch)
		}
	}
	return x
}

func (s *BatchAPI) clusterView() batch.ClusterView {
	return batchCluster{fn: s.members}
}

func (s *BatchAPI) members() []cluster.NodeSummary {
	if s.Members != nil {
		return s.Members()
	}
	if s.Router != nil && s.Router.Members != nil {
		return s.Router.Members()
	}
	return nil
}

func (s *BatchAPI) readOwnerSpec(ctx context.Context, nodeID, idOrName string) (batch.OwnerSpec, error) {
	h := make(http.Header)
	if nodeID != "" {
		rpc.SetTarget(h, nodeID)
	}
	local, rt, err := hopRoute(false, s.LocalID, s.Router, ctx, h, idOrName, "")
	if err != nil {
		return batch.OwnerSpec{}, err
	}
	if local {
		if s.Mgr == nil {
			return batch.OwnerSpec{}, errcode.E(errcode.NOT_FOUND, "process")
		}
		spec, err := s.Mgr.Resolve(ctx, idOrName)
		if err != nil {
			return batch.OwnerSpec{}, err
		}
		fallback := nodeID
		if fallback == "" {
			fallback = s.LocalID
		}
		return ownerFromProcess(spec, fallback), nil
	}
	if s.Forward == nil {
		return batch.OwnerSpec{}, errcode.E(errcode.UNAVAILABLE, "owner unreachable")
	}
	req := connect.NewRequest(&procmeshv1.GetProcessRequest{IdOrName: idOrName})
	stampHop(req.Header(), s.LocalID, rt.NodeID)
	stampIdentity(req.Header(), ctx)
	cli, err := s.Forward.Process(ctx, rt)
	if err != nil {
		return batch.OwnerSpec{}, rpc.MapDialError(err)
	}
	resp, err := cli.GetProcess(ctx, req)
	if err != nil {
		mapped := rpc.MapCallError(err)
		if errcode.Is(mapped, errcode.NOT_FOUND) {
			return batch.OwnerSpec{}, errcode.E(errcode.NOT_FOUND, "process")
		}
		return batch.OwnerSpec{}, mapped
	}
	view := resp.Msg.GetProcess()
	if view == nil || (view.GetProcessId() == "" && view.GetSpec() == nil) {
		return batch.OwnerSpec{}, errcode.E(errcode.NOT_FOUND, "process")
	}
	spec := ProtoToSpec(view.GetSpec())
	if spec.ProcessID == "" {
		spec.ProcessID = view.GetProcessId()
	}
	fallback := nodeID
	if fallback == "" {
		fallback = rt.NodeID
	}
	return ownerFromProcess(spec, fallback), nil
}

func ownerFromProcess(spec process.ProcessSpec, nodeID string) batch.OwnerSpec {
	if spec.OwnerAgentID != "" {
		nodeID = spec.OwnerAgentID
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		raw = nil
	}
	return batch.OwnerSpec{
		ProcessID:      spec.ProcessID,
		Name:           spec.Name,
		NodeID:         nodeID,
		Group:          spec.Group,
		LatestRevision: spec.LatestRevision,
		SpecJSON:       string(raw),
	}
}

func overlayConfig(owner batch.OwnerSpec, patch *procmeshv1.ProcessSpec) (string, int64, error) {
	base := specFromOwner(owner)
	applyNonEmptySpec(base, patch)
	if owner.ProcessID != "" {
		base.ProcessId = owner.ProcessID
	}
	if owner.Name != "" {
		base.Name = owner.Name
	}
	if owner.NodeID != "" {
		base.OwnerAgentId = owner.NodeID
	}
	base.LatestRevision = owner.LatestRevision
	raw, err := json.Marshal(configUpdatePayload{
		ExpectedRevision: owner.LatestRevision,
		Spec:             base,
	})
	if err != nil {
		return "", 0, err
	}
	return string(raw), owner.LatestRevision, nil
}

func specFromOwner(owner batch.OwnerSpec) *procmeshv1.ProcessSpec {
	if owner.SpecJSON != "" {
		var ps process.ProcessSpec
		if err := json.Unmarshal([]byte(owner.SpecJSON), &ps); err == nil && (ps.ProcessID != "" || ps.Name != "" || ps.Command != "") {
			return SpecToProto(ps)
		}
		var proto procmeshv1.ProcessSpec
		if err := json.Unmarshal([]byte(owner.SpecJSON), &proto); err == nil {
			return &proto
		}
	}
	return &procmeshv1.ProcessSpec{
		ProcessId:      owner.ProcessID,
		Name:           owner.Name,
		OwnerAgentId:   owner.NodeID,
		Group:          owner.Group,
		LatestRevision: owner.LatestRevision,
	}
}

func applyNonEmptySpec(dst, src *procmeshv1.ProcessSpec) {
	if dst == nil || src == nil {
		return
	}
	if src.GetGroup() != "" {
		dst.Group = src.GetGroup()
	}
	if src.GetCommand() != "" {
		dst.Command = src.GetCommand()
	}
	if len(src.GetArgs()) > 0 {
		dst.Args = append([]string(nil), src.GetArgs()...)
	}
	if src.GetWorkingDirectory() != "" {
		dst.WorkingDirectory = src.GetWorkingDirectory()
	}
	if src.GetRunAsUser() != "" {
		dst.RunAsUser = src.GetRunAsUser()
	}
	if env := src.GetEnvironment(); len(env) > 0 {
		dst.Environment = make(map[string]string, len(env))
		for k, v := range env {
			dst.Environment[k] = v
		}
	}
	if src.GetInstances() != 0 {
		dst.Instances = src.GetInstances()
	}
	if src.GetAutostart() {
		dst.Autostart = true
	}
	if src.GetStopSignal() != "" {
		dst.StopSignal = src.GetStopSignal()
	}
	if src.GetKillSignal() != "" {
		dst.KillSignal = src.GetKillSignal()
	}
	if src.GetStopTimeoutMs() != 0 {
		dst.StopTimeoutMs = src.GetStopTimeoutMs()
	}
	if src.GetStartupPriority() != 0 {
		dst.StartupPriority = src.GetStartupPriority()
	}
	if src.Restart != nil {
		if dst.Restart == nil {
			dst.Restart = &procmeshv1.RestartPolicy{}
		}
		applyNonEmptyRestart(dst.Restart, src.Restart)
	}
	if src.Health != nil {
		if dst.Health == nil {
			dst.Health = &procmeshv1.HealthCheck{}
		}
		applyNonEmptyHealth(dst.Health, src.Health)
	}
	if src.Log != nil {
		if dst.Log == nil {
			dst.Log = &procmeshv1.LogPolicy{}
		}
		applyNonEmptyLog(dst.Log, src.Log)
	}
	if src.Resources != nil {
		if dst.Resources == nil {
			dst.Resources = &procmeshv1.ResourceLimit{}
		}
		applyNonEmptyResources(dst.Resources, src.Resources)
	}
	if len(src.GetDependencies()) > 0 {
		dst.Dependencies = src.GetDependencies()
	}
}

func applyNonEmptyRestart(dst, src *procmeshv1.RestartPolicy) {
	if src.GetMode() != "" {
		dst.Mode = src.GetMode()
	}
	if src.GetMaxRetries() != 0 {
		dst.MaxRetries = src.GetMaxRetries()
	}
	if src.GetRetryWindowMs() != 0 {
		dst.RetryWindowMs = src.GetRetryWindowMs()
	}
	if src.Backoff != nil {
		if dst.Backoff == nil {
			dst.Backoff = &procmeshv1.Backoff{}
		}
		if src.Backoff.GetInitialMs() != 0 {
			dst.Backoff.InitialMs = src.Backoff.GetInitialMs()
		}
		if src.Backoff.GetMaxMs() != 0 {
			dst.Backoff.MaxMs = src.Backoff.GetMaxMs()
		}
		if src.Backoff.GetMultiplier() != 0 {
			dst.Backoff.Multiplier = src.Backoff.GetMultiplier()
		}
	}
}

func applyNonEmptyHealth(dst, src *procmeshv1.HealthCheck) {
	if src.GetType() != "" {
		dst.Type = src.GetType()
	}
	if src.GetUrl() != "" {
		dst.Url = src.GetUrl()
	}
	if src.GetMethod() != "" {
		dst.Method = src.GetMethod()
	}
	if src.GetAddress() != "" {
		dst.Address = src.GetAddress()
	}
	if src.GetCommand() != "" {
		dst.Command = src.GetCommand()
	}
	if src.GetExpectedStatus() != 0 {
		dst.ExpectedStatus = src.GetExpectedStatus()
	}
	if len(src.GetArgs()) > 0 {
		dst.Args = append([]string(nil), src.GetArgs()...)
	}
	if src.GetInitialDelayMs() != 0 {
		dst.InitialDelayMs = src.GetInitialDelayMs()
	}
	if src.GetIntervalMs() != 0 {
		dst.IntervalMs = src.GetIntervalMs()
	}
	if src.GetTimeoutMs() != 0 {
		dst.TimeoutMs = src.GetTimeoutMs()
	}
	if src.GetFailureThreshold() != 0 {
		dst.FailureThreshold = src.GetFailureThreshold()
	}
	if src.GetSuccessThreshold() != 0 {
		dst.SuccessThreshold = src.GetSuccessThreshold()
	}
	if src.GetRestartOnFailure() {
		dst.RestartOnFailure = true
	}
	if src.GetRestartCooldownMs() != 0 {
		dst.RestartCooldownMs = src.GetRestartCooldownMs()
	}
}

func applyNonEmptyLog(dst, src *procmeshv1.LogPolicy) {
	if src.GetMaxSize() != 0 {
		dst.MaxSize = src.GetMaxSize()
	}
	if src.GetMaxFiles() != 0 {
		dst.MaxFiles = src.GetMaxFiles()
	}
	if src.GetMaxAgeSeconds() != 0 {
		dst.MaxAgeSeconds = src.GetMaxAgeSeconds()
	}
	if src.GetCompress() {
		dst.Compress = true
	}
	if src.GetDirectory() != "" {
		dst.Directory = src.GetDirectory()
	}
	if src.GetRedirectStderr() {
		dst.RedirectStderr = true
	}
}

func applyNonEmptyResources(dst, src *procmeshv1.ResourceLimit) {
	if src.GetCpuQuotaMillis() != 0 {
		dst.CpuQuotaMillis = src.GetCpuQuotaMillis()
	}
	if src.GetMemoryBytes() != 0 {
		dst.MemoryBytes = src.GetMemoryBytes()
	}
	if src.GetOpenFiles() != 0 {
		dst.OpenFiles = src.GetOpenFiles()
	}
}
