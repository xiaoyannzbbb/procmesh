package api

import (
	"context"
	"sort"
	"sync"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/update"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.UpdateServiceHandler = (*UpdateAPI)(nil)

// LatestChecker is satisfied by *update.Checker.
type LatestChecker interface {
	CheckLatest(ctx context.Context, refresh bool) (update.Result, error)
}

// LocalInfoProvider is satisfied by *update.Applier.
type LocalInfoProvider interface {
	LocalInfo() update.LocalInfo
}

// LocalApplier is satisfied by *update.Applier.
type LocalApplier interface {
	Apply(ctx context.Context, pin update.Pin) error
}

// UpdateAPI serves UpdateService RPCs.
type UpdateAPI struct {
	Auth      *auth.Service
	Checker   LatestChecker
	Local     LocalInfoProvider
	Applier   LocalApplier
	Engine    *update.Engine
	Store     *store.Store
	Cluster   ClusterDeps
	LocalID   string
	LocalOnly bool
	Router    *Router
	Forward   Forwarder
	Degraded  func() bool

	identMu sync.Mutex
	idents  map[string]auth.Principal
}

func (s *UpdateAPI) CheckLatest(ctx context.Context, req *connect.Request[procmeshv1.CheckLatestRequest]) (*connect.Response[procmeshv1.CheckLatestResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	if s.Checker == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "update checker unavailable"))
	}
	res, err := s.Checker.CheckLatest(ctx, req.Msg.GetRefresh())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.CheckLatestResponse{
		Repository:    res.Pin.Repository,
		Tag:           res.Pin.Tag,
		Checksums:     res.Pin.Checksums,
		CheckedUnixMs: res.CheckedUnixMs,
		FromCache:     res.FromCache,
		CheckError:    res.CheckError,
		ErrorMessage:  res.ErrorMessage,
	}), nil
}

func (s *UpdateAPI) GetLocalUpdateInfo(ctx context.Context, _ *connect.Request[procmeshv1.GetLocalUpdateInfoRequest]) (*connect.Response[procmeshv1.GetLocalUpdateInfoResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	info := s.localInfo()
	return connect.NewResponse(&procmeshv1.GetLocalUpdateInfoResponse{
		Os:      info.OS,
		Arch:    info.Arch,
		Version: info.Version,
		Enabled: info.Enabled,
		Busy:    info.Busy,
		NodeId:  s.LocalID,
	}), nil
}

func (s *UpdateAPI) ListNodeUpdateStatus(ctx context.Context, req *connect.Request[procmeshv1.ListNodeUpdateStatusRequest]) (*connect.Response[procmeshv1.ListNodeUpdateStatusResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	var latest string
	var checkError bool
	var errorMessage string
	var checkedUnixMs int64
	if s.Checker != nil {
		res, err := s.Checker.CheckLatest(ctx, false)
		latest = res.Pin.Tag
		checkError = res.CheckError || err != nil
		errorMessage = res.ErrorMessage
		checkedUnixMs = res.CheckedUnixMs
	}

	members := s.Cluster.members()
	now := s.Cluster.now()
	out := make([]*procmeshv1.NodeUpdateStatus, len(members))
	var wg sync.WaitGroup
	for i, member := range members {
		i, member := i, member
		classified := freshness.Classify(now, member.LastUpdatedUnixMs, string(member.State))
		if classified != freshness.LIVE {
			eval := update.NodeEval{
				OS: member.OS, Arch: member.Arch, Version: member.AgentVersion,
				SkipReason: update.SkipNotLive(string(member.State), classified),
			}
			out[i] = nodeUpdateStatusOf(member, classified, eval)
			continue
		}
		if !update.ShouldProbe(now, member) {
			eval := update.NodeEval{
				OS: member.OS, Arch: member.Arch, Version: member.AgentVersion,
				SkipReason: update.SkipMACOS,
			}
			out[i] = nodeUpdateStatusOf(member, classified, eval)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := s.probeNode(ctx, member.NodeID)
			eval := update.EvaluateProbed(update.ProbedInput{
				GossipOS: member.OS, GossipArch: member.Arch, GossipVersion: member.AgentVersion,
				LatestTag: latest, CheckFailed: checkError, Info: info, ProbeErr: err,
			})
			out[i] = nodeUpdateStatusOf(member, classified, eval)
		}()
	}
	wg.Wait()

	sort.SliceStable(out, func(i, j int) bool {
		li, lj := out[i], out[j]
		lh, rh := li.GetHostname(), lj.GetHostname()
		if lh == rh {
			return li.GetNodeId() < lj.GetNodeId()
		}
		return lh < rh
	})
	return connect.NewResponse(&procmeshv1.ListNodeUpdateStatusResponse{
		Nodes:         out,
		LatestTag:     latest,
		CheckError:    checkError,
		ErrorMessage:  errorMessage,
		CheckedUnixMs: checkedUnixMs,
	}), nil
}

func (s *UpdateAPI) localInfo() update.LocalInfo {
	if s != nil && s.Local != nil {
		return s.Local.LocalInfo()
	}
	return (*update.Applier)(nil).LocalInfo()
}

func (s *UpdateAPI) probeNode(ctx context.Context, nodeID string) (*update.LocalInfo, error) {
	if nodeID != "" && nodeID == s.LocalID {
		info := s.localInfo()
		return &info, nil
	}
	if s.LocalOnly || s.Router == nil || s.Forward == nil {
		return nil, errcode.E(errcode.UNAVAILABLE, "owner unreachable")
	}
	rt, err := s.Router.Resolve(ctx, "", "", nodeID)
	if err != nil {
		return nil, err
	}
	if rt.Local {
		info := s.localInfo()
		return &info, nil
	}
	cli, err := s.remoteUpdate(ctx, rt)
	if err != nil {
		return nil, err
	}
	req := connect.NewRequest(&procmeshv1.GetLocalUpdateInfoRequest{})
	stampHop(req.Header(), s.LocalID, rt.NodeID)
	stampIdentity(req.Header(), ctx)
	out, err := cli.GetLocalUpdateInfo(ctx, req)
	if err != nil {
		return nil, err
	}
	msg := out.Msg
	return &update.LocalInfo{
		OS:      msg.GetOs(),
		Arch:    msg.GetArch(),
		Version: msg.GetVersion(),
		Enabled: msg.GetEnabled(),
		Busy:    msg.GetBusy(),
	}, nil
}

func (s *UpdateAPI) remoteUpdate(ctx context.Context, rt Route) (procmeshv1connect.UpdateServiceClient, error) {
	if s.Forward == nil {
		return nil, unavailableOwner()
	}
	cli, err := s.Forward.Update(ctx, rt)
	if err != nil {
		return nil, ToConnect(rpc.MapDialError(err))
	}
	return cli, nil
}

func nodeUpdateStatusOf(m cluster.NodeSummary, classified string, eval update.NodeEval) *procmeshv1.NodeUpdateStatus {
	os, arch, ver := eval.OS, eval.Arch, eval.Version
	if os == "" {
		os = m.OS
	}
	if arch == "" {
		arch = m.Arch
	}
	if ver == "" {
		ver = m.AgentVersion
	}
	return &procmeshv1.NodeUpdateStatus{
		NodeId:            m.NodeID,
		Hostname:          m.Hostname,
		Os:                os,
		Arch:              arch,
		Version:           ver,
		Freshness:         classified,
		LastUpdatedUnixMs: m.LastUpdatedUnixMs,
		Eligible:          eval.Eligible,
		SkipReason:        eval.SkipReason,
		Busy:              eval.Busy,
	}
}
