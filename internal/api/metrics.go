package api

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// countingForwarder increments n on each remote owner dial attempt.
type countingForwarder struct {
	inner Forwarder
	n     *atomic.Uint64
}

func (f *countingForwarder) Process(ctx context.Context, rt Route) (procmeshv1connect.ProcessServiceClient, error) {
	f.n.Add(1)
	return f.inner.Process(ctx, rt)
}

func (f *countingForwarder) Config(ctx context.Context, rt Route) (procmeshv1connect.ConfigServiceClient, error) {
	f.n.Add(1)
	return f.inner.Config(ctx, rt)
}

func (f *countingForwarder) Log(ctx context.Context, rt Route) (procmeshv1connect.LogServiceClient, error) {
	f.n.Add(1)
	return f.inner.Log(ctx, rt)
}

func wrapForwarder(f Forwarder, n *atomic.Uint64) Forwarder {
	if f == nil || n == nil {
		return f
	}
	return &countingForwarder{inner: f, n: n}
}

func runningInstances(mgr *process.Manager) int {
	if mgr == nil {
		return 0
	}
	specs, err := mgr.ListSpecs(context.Background())
	if err != nil {
		return 0
	}
	n := 0
	for _, spec := range specs {
		insts, err := mgr.ListInstances(context.Background(), spec.ProcessID)
		if err != nil {
			continue
		}
		for _, inst := range insts {
			if inst.Observed == process.ObservedRunning {
				n++
			}
		}
	}
	return n
}

func renderMetrics(uptimeSeconds float64, running, members, alive int, rpcForward uint64) []byte {
	return []byte(fmt.Sprintf(
		"# HELP procmesh_agent_uptime Agent uptime in seconds.\n"+
			"# TYPE procmesh_agent_uptime gauge\n"+
			"procmesh_agent_uptime %g\n"+
			"# HELP procmesh_process_running Number of process instances with observed=RUNNING.\n"+
			"# TYPE procmesh_process_running gauge\n"+
			"procmesh_process_running %d\n"+
			"# HELP procmesh_cluster_members Number of known cluster members.\n"+
			"# TYPE procmesh_cluster_members gauge\n"+
			"procmesh_cluster_members %d\n"+
			"# HELP procmesh_cluster_alive_members Number of ALIVE cluster members.\n"+
			"# TYPE procmesh_cluster_alive_members gauge\n"+
			"procmesh_cluster_alive_members %d\n"+
			"# HELP procmesh_rpc_forward_total Remote owner RPC forward attempts.\n"+
			"# TYPE procmesh_rpc_forward_total counter\n"+
			"procmesh_rpc_forward_total %d\n",
		uptimeSeconds, running, members, alive, rpcForward,
	))
}
