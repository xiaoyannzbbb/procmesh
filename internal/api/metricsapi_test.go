package api

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/process"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestMetrics_GetAgentMetricsUptimeAndEmptyMgr(t *testing.T) {
	api := &MetricsAPI{Started: time.Now().Add(-time.Hour)}
	resp, err := api.GetAgentMetrics(context.Background(), connect.NewRequest(&procmeshv1.GetAgentMetricsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg.GetMetrics()
	if m == nil {
		t.Fatal("nil metrics")
	}
	if m.GetUptimeSeconds() < 3600 {
		t.Fatalf("uptime=%g want >= 3600", m.GetUptimeSeconds())
	}
	if m.GetProcessRunning() != 0 {
		t.Fatalf("process_running=%d want 0 (empty Mgr)", m.GetProcessRunning())
	}
}

func TestMetrics_GetAgentMetricsClusterCounts(t *testing.T) {
	api := &MetricsAPI{
		Started: time.Now().Add(-time.Hour),
		Cluster: ClusterDeps{
			Mesh: &staticMesh{members: []cluster.NodeSummary{
				{NodeID: "n1", State: cluster.StateAlive, Resources: cluster.ResourceSummary{CPUPercent: 11, MemoryPercent: 22, DiskPercent: 33}},
				{NodeID: "n2", State: cluster.StateFailed},
			}},
			Local: func() cluster.NodeSummary {
				return cluster.NodeSummary{Resources: cluster.ResourceSummary{CPUPercent: 11, MemoryPercent: 22, DiskPercent: 33}}
			},
		},
	}
	resp, err := api.GetAgentMetrics(context.Background(), connect.NewRequest(&procmeshv1.GetAgentMetricsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg.GetMetrics()
	if m.GetMembers() != 2 || m.GetAlive() != 1 {
		t.Fatalf("members=%d alive=%d", m.GetMembers(), m.GetAlive())
	}
	res := m.GetResources()
	if res == nil || res.GetCpuPercent() != 11 || res.GetMemoryPercent() != 22 || res.GetDiskPercent() != 33 {
		t.Fatalf("resources %+v", res)
	}
	if m.GetControlQuorum() {
		t.Fatal("control_quorum want false without Control")
	}
}

func TestMetrics_GetAgentMetricsUncollectedResources(t *testing.T) {
	api := &MetricsAPI{
		Started: time.Now().Add(-time.Hour),
		Cluster: ClusterDeps{
			Local: func() cluster.NodeSummary {
				return cluster.NodeSummary{}
			},
		},
	}
	resp, err := api.GetAgentMetrics(context.Background(), connect.NewRequest(&procmeshv1.GetAgentMetricsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	res := resp.Msg.GetMetrics().GetResources()
	if res == nil || res.GetCpuPercent() != -1 || res.GetMemoryPercent() != -1 || res.GetDiskPercent() != -1 {
		t.Fatalf("uncollected resources %+v want -1", res)
	}
}

func TestMetrics_GetProcessMetricsUnknownNotFound(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	api := &MetricsAPI{Mgr: mgr}
	_, err := api.GetProcessMetrics(context.Background(), connect.NewRequest(&procmeshv1.GetProcessMetricsRequest{
		IdOrName: "missing",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestMetrics_GetProcessMetricsStartedAt(t *testing.T) {
	ctx := context.Background()
	mgr, st, _ := newTestManager(t)
	spec, err := mgr.ApplySpec(ctx, process.ProcessSpec{Name: "web", Command: "/bin/true"}, 0, "op-metrics-apply", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	insts, err := mgr.ListInstances(ctx, spec.ProcessID)
	if err != nil || len(insts) == 0 {
		t.Fatalf("instances=%v err=%v", insts, err)
	}
	started := time.Now().Add(-time.Hour)
	inst := insts[0]
	inst.PID = os.Getpid()
	inst.StartedAt = &started
	inst.Observed = process.ObservedRunning
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	api := &MetricsAPI{Mgr: mgr}
	resp, err := api.GetProcessMetrics(ctx, connect.NewRequest(&procmeshv1.GetProcessMetricsRequest{
		IdOrName: "web",
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg.GetMetrics()
	if len(got) != 1 {
		t.Fatalf("metrics=%d want 1: %+v", len(got), got)
	}
	pm := got[0]
	if pm.GetInstanceId() != inst.InstanceID {
		t.Fatalf("instance_id=%q want %q", pm.GetInstanceId(), inst.InstanceID)
	}
	if pm.GetPid() != int32(inst.PID) {
		t.Fatalf("pid=%d want %d", pm.GetPid(), inst.PID)
	}
	if pm.GetUptimeSeconds() < 3600 {
		t.Fatalf("uptime=%d want >= 3600", pm.GetUptimeSeconds())
	}
	if runtime.GOOS != "linux" {
		if pm.GetCpuPercent() != -1 || pm.GetMemoryBytes() != -1 {
			t.Logf("warning: non-linux got cpu=%d mem=%d", pm.GetCpuPercent(), pm.GetMemoryBytes())
		}
	}
}

func TestMetrics_GetProcessMetricsLocalOnlyNoHop(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	fwd := &fakeForwarder{metrics: &fakeMetricsClient{}}
	api := &MetricsAPI{
		Mgr:       mgr,
		LocalOnly: true,
		LocalID:   "aaa",
		Router:    remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward:   fwd,
	}
	_, err := api.GetProcessMetrics(context.Background(), connect.NewRequest(&procmeshv1.GetProcessMetricsRequest{
		IdOrName: "nginx",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
	if fwd.metricsCalls() != 0 {
		t.Fatalf("LocalOnly hop metricsCalls=%d", fwd.metricsCalls())
	}
}

type fakeMetricsClient struct {
	out *connect.Response[procmeshv1.GetProcessMetricsResponse]
	err error
}

func (f *fakeMetricsClient) GetAgentMetrics(context.Context, *connect.Request[procmeshv1.GetAgentMetricsRequest]) (*connect.Response[procmeshv1.GetAgentMetricsResponse], error) {
	return connect.NewResponse(&procmeshv1.GetAgentMetricsResponse{}), nil
}

func (f *fakeMetricsClient) GetProcessMetrics(context.Context, *connect.Request[procmeshv1.GetProcessMetricsRequest]) (*connect.Response[procmeshv1.GetProcessMetricsResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.out != nil {
		return f.out, nil
	}
	return connect.NewResponse(&procmeshv1.GetProcessMetricsResponse{}), nil
}

var _ procmeshv1connect.MetricsServiceClient = (*fakeMetricsClient)(nil)
