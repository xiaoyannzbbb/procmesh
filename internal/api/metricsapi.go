package api

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.MetricsServiceHandler = (*MetricsAPI)(nil)

const (
	procStatUnavailable    = "process cpu/memory unavailable"
	procStatUnavailableMac = "macos: process cpu/memory unavailable"
)

type MetricsAPI struct {
	Mgr       *process.Manager
	Auth      *auth.Service
	Started   time.Time
	Cluster   ClusterDeps
	LocalOnly bool
	LocalID   string
	Router    *Router
	Forward   Forwarder
	Degraded  func() bool
}

func (s *MetricsAPI) GetAgentMetrics(ctx context.Context, _ *connect.Request[procmeshv1.GetAgentMetricsRequest]) (*connect.Response[procmeshv1.GetAgentMetricsResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	started := s.Started
	if started.IsZero() {
		started = s.now()
	}
	uptime := s.now().Sub(started).Seconds()
	if uptime < 0 {
		uptime = 0
	}
	sum := summarize(s.Cluster.members())
	var quorum bool
	if n := s.Cluster.controlNode(); n != nil {
		quorum = n.HasQuorum()
	}
	return connect.NewResponse(&procmeshv1.GetAgentMetricsResponse{
		Metrics: &procmeshv1.AgentMetrics{
			UptimeSeconds:  uptime,
			ProcessRunning: int32(runningInstances(s.Mgr)),
			Members:        sum.members,
			Alive:          sum.alive,
			ControlQuorum:  quorum,
			Resources:      localResourceSummary(s.Cluster),
		},
	}), nil
}

func (s *MetricsAPI) GetProcessMetrics(ctx context.Context, req *connect.Request[procmeshv1.GetProcessMetricsRequest]) (*connect.Response[procmeshv1.GetProcessMetricsResponse], error) {
	local, rt, err := hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, req.Header(), req.Msg.GetIdOrName(), "")
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := requireRoutePerm(ctx, s.Auth, auth.PermProcessRead, local, rt, s.LocalID, false); err != nil {
		return nil, err
	}
	if !local {
		cli, err := s.remoteMetrics(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.GetProcessMetrics(ctx, req)
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
	insts, err := s.Mgr.ListInstances(ctx, spec.ProcessID)
	if err != nil {
		return nil, ToConnect(err)
	}
	wantInst := req.Msg.GetInstanceId()
	now := s.now()
	out := make([]*procmeshv1.ProcessMetrics, 0, len(insts))
	for _, inst := range insts {
		if wantInst != "" && inst.InstanceID != wantInst {
			continue
		}
		out = append(out, processMetricsOf(inst, now))
	}
	return connect.NewResponse(&procmeshv1.GetProcessMetricsResponse{Metrics: out}), nil
}

func (s *MetricsAPI) remoteMetrics(ctx context.Context, rt Route, header http.Header) (procmeshv1connect.MetricsServiceClient, error) {
	if s.Forward == nil {
		return nil, unavailableOwner()
	}
	stampHop(header, s.LocalID, rt.NodeID)
	stampIdentity(header, ctx)
	cli, err := s.Forward.Metrics(ctx, rt)
	if err != nil {
		return nil, ToConnect(rpc.MapDialError(err))
	}
	return cli, nil
}

func (s *MetricsAPI) now() time.Time {
	return s.Cluster.now()
}

func localResourceSummary(d ClusterDeps) *procmeshv1.ResourceSummary {
	if d.Local == nil {
		return nil
	}
	r := d.Local().Resources
	return &procmeshv1.ResourceSummary{
		CpuPercent:    int32(r.CPUPercent),
		MemoryPercent: int32(r.MemoryPercent),
		DiskPercent:   int32(r.DiskPercent),
	}
}

func processMetricsOf(inst process.Instance, now time.Time) *procmeshv1.ProcessMetrics {
	out := &procmeshv1.ProcessMetrics{
		InstanceId: inst.InstanceID,
		Pid:        int32(inst.PID),
	}
	if inst.StartedAt != nil && !inst.StartedAt.IsZero() {
		u := now.Sub(*inst.StartedAt).Seconds()
		if u < 0 {
			u = 0
		}
		out.UptimeSeconds = int64(u)
	}
	cpu, mem, ok := readProcStat(inst.PID)
	if !ok {
		out.CpuPercent = -1
		out.MemoryBytes = -1
		out.Note = procStatNote()
		return out
	}
	out.CpuPercent = cpu
	out.MemoryBytes = mem
	return out
}

func procStatNote() string {
	if runtime.GOOS == "linux" {
		return procStatUnavailable
	}
	return procStatUnavailableMac
}
