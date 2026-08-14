package api

import (
	"context"
	"fmt"

	"github.com/qleelulu/procmesh/internal/process"
)

const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

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

func renderMetrics(uptimeSeconds float64, running, members, alive int) []byte {
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
			"procmesh_cluster_alive_members %d\n",
		uptimeSeconds, running, members, alive,
	))
}
