package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.MetricsServiceHandler = (*MetricsAPI)(nil)

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
	Metrics   *metrics.Collector
	Store     *store.Store
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
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, auth.PermProcessRead, req.Msg.GetIdOrName(), local, rt, false); err != nil {
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
	if err := authorizeProcessSpec(ctx, s.Auth, auth.PermProcessRead, s.LocalID, spec.Group, false); err != nil {
		return nil, err
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
		out = append(out, processMetricsOf(s.Metrics, inst, now))
	}
	return connect.NewResponse(&procmeshv1.GetProcessMetricsResponse{Metrics: out}), nil
}

func (s *MetricsAPI) GetNodeHistory(ctx context.Context, req *connect.Request[procmeshv1.GetNodeHistoryRequest]) (*connect.Response[procmeshv1.GetNodeHistoryResponse], error) {
	nodeID := req.Msg.GetNodeId()
	if nodeID == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "node_id is required"))
	}
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	local, rt, err := hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, req.Header(), "", nodeID)
	if err != nil {
		return nil, ToConnect(err)
	}
	if !local {
		cli, err := s.remoteMetrics(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.GetNodeHistory(ctx, req)
		if err != nil {
			return nil, mapForwardErr(err)
		}
		return out, nil
	}
	st, ut, layer, err := s.historyQuery(req.Msg.GetSinceUnix(), req.Msg.GetUntilUnix(), req.Msg.GetResolution())
	if err != nil {
		return nil, err
	}
	series, err := s.loadNodeSeries(ctx, nodeID, layer, st, ut)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.GetNodeHistoryResponse{
		NodeId: nodeID,
		Layer:  layer,
		Series: series,
	}), nil
}

func (s *MetricsAPI) GetProcessHistory(ctx context.Context, req *connect.Request[procmeshv1.GetProcessHistoryRequest]) (*connect.Response[procmeshv1.GetProcessHistoryResponse], error) {
	local, rt, err := hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, req.Header(), req.Msg.GetIdOrName(), "")
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, auth.PermProcessRead, req.Msg.GetIdOrName(), local, rt, false); err != nil {
		return nil, err
	}
	if !local {
		cli, err := s.remoteMetrics(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.GetProcessHistory(ctx, req)
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
	st, ut, layer, err := s.historyQuery(req.Msg.GetSinceUnix(), req.Msg.GetUntilUnix(), req.Msg.GetResolution())
	if err != nil {
		return nil, err
	}
	series, err := s.loadProcessSeries(ctx, spec.ProcessID, layer, st, ut)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.GetProcessHistoryResponse{
		ProcessId: spec.ProcessID,
		Layer:     layer,
		Series:    series,
	}), nil
}

func normalizeHistoryRange(now time.Time, since, until int64, resolution string) (time.Time, time.Time, string, error) {
	u := until
	if u <= 0 {
		u = now.Unix()
	}
	s := since
	if s <= 0 {
		s = u - int64((24 * time.Hour).Seconds())
	}
	st := time.Unix(s, 0).UTC()
	ut := time.Unix(u, 0).UTC()
	if !ut.After(st) {
		return time.Time{}, time.Time{}, "", errcode.E(errcode.INVALID, "until must be after since")
	}
	switch resolution {
	case "", metrics.LayerRawMin, metrics.LayerDown5m:
	default:
		return time.Time{}, time.Time{}, "", errcode.E(errcode.INVALID, "invalid resolution")
	}
	layer := resolution
	if layer == "" {
		layer = metrics.SelectLayer(st, ut)
	}
	return st, ut, layer, nil
}

func (s *MetricsAPI) historyQuery(since, until int64, resolution string) (time.Time, time.Time, string, error) {
	if s.Store == nil {
		return time.Time{}, time.Time{}, "", ToConnect(errcode.E(errcode.UNAVAILABLE, "history store unavailable"))
	}
	st, ut, layer, err := normalizeHistoryRange(s.now(), since, until, resolution)
	if err != nil {
		return time.Time{}, time.Time{}, "", ToConnect(err)
	}
	return st, ut, layer, nil
}

func (s *MetricsAPI) loadNodeSeries(ctx context.Context, subject, layer string, since, until time.Time) ([]*procmeshv1.MetricSeries, error) {
	cpu, err := s.loadSeries(ctx, subject, layer, since, until, metrics.SeriesNodeCPU, "cpu_percent")
	if err != nil {
		return nil, err
	}
	mem, err := s.loadSeries(ctx, subject, layer, since, until, metrics.SeriesNodeMem, "memory_percent")
	if err != nil {
		return nil, err
	}
	disk, err := s.loadSeries(ctx, subject, layer, since, until, metrics.SeriesNodeDisk, "disk_percent")
	if err != nil {
		return nil, err
	}
	return []*procmeshv1.MetricSeries{cpu, mem, disk}, nil
}

func (s *MetricsAPI) loadProcessSeries(ctx context.Context, subject, layer string, since, until time.Time) ([]*procmeshv1.MetricSeries, error) {
	cpu, err := s.loadSeries(ctx, subject, layer, since, until, metrics.SeriesProcCPU, "cpu_percent")
	if err != nil {
		return nil, err
	}
	mem, err := s.loadSeries(ctx, subject, layer, since, until, metrics.SeriesProcMem, "memory_bytes")
	if err != nil {
		return nil, err
	}
	return []*procmeshv1.MetricSeries{cpu, mem}, nil
}

func (s *MetricsAPI) loadSeries(ctx context.Context, subject, layer string, since, until time.Time, fullSeriesName, shortName string) (*procmeshv1.MetricSeries, error) {
	out := &procmeshv1.MetricSeries{Name: shortName, Layer: layer}
	samples, err := s.Store.ListMetricSamples(ctx, fullSeriesName, subject, layer, since.Unix(), until.Unix())
	if err != nil {
		return nil, ToConnect(err)
	}
	for _, sample := range samples {
		out.Points = append(out.Points, &procmeshv1.MetricPoint{
			TsUnix: sample.TSUnix,
			Value:  sample.Value,
		})
	}
	return out, nil
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
		return protoResources(cluster.ResourceSummary{})
	}
	return protoResources(d.Local().Resources)
}

func processMetricsOf(c *metrics.Collector, inst process.Instance, now time.Time) *procmeshv1.ProcessMetrics {
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

	// Collector 未初始化
	if c == nil {
		out.CpuPercent = -1
		out.MemoryBytes = -1
		out.Note = "metrics collector unavailable"
		return out
	}

	// 采集进程指标
	pm, err := c.ProcessMetrics(inst.PID)
	if err != nil {
		out.CpuPercent = -1
		out.MemoryBytes = -1
		out.Note = fmt.Sprintf("metrics unavailable: %v", err)
		return out
	}

	out.CpuPercent = int32(math.Round(pm.CPUPercent))
	out.MemoryBytes = int64(pm.MemoryBytes)
	return out
}
