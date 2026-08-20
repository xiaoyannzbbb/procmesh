package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/qleelulu/procmesh/internal/alert"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type batchMetricSnapshot struct {
	Running     int
	Success     int
	Failed      int
	Timeout     int
	Denied      int
	Conflict    int
	Unavailable int
	Invalid     int
}

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

func (f *countingForwarder) Audit(ctx context.Context, rt Route) (procmeshv1connect.AuditServiceClient, error) {
	f.n.Add(1)
	return f.inner.Audit(ctx, rt)
}

func (f *countingForwarder) Metrics(ctx context.Context, rt Route) (procmeshv1connect.MetricsServiceClient, error) {
	f.n.Add(1)
	return f.inner.Metrics(ctx, rt)
}

func (f *countingForwarder) Alert(ctx context.Context, rt Route) (procmeshv1connect.AlertServiceClient, error) {
	f.n.Add(1)
	return f.inner.Alert(ctx, rt)
}

func (f *countingForwarder) Backup(ctx context.Context, rt Route) (procmeshv1connect.BackupServiceClient, error) {
	f.n.Add(1)
	return f.inner.Backup(ctx, rt)
}

func (f *countingForwarder) ClusterBackup(ctx context.Context, rt Route) (procmeshv1connect.ClusterBackupServiceClient, error) {
	f.n.Add(1)
	cf, ok := f.inner.(ClusterBackupForwarder)
	if !ok {
		return nil, unavailableOwner()
	}
	return cf.ClusterBackup(ctx, rt)
}

func (f *countingForwarder) DisasterReplication(ctx context.Context, rt Route) (procmeshv1connect.DisasterReplicationServiceClient, error) {
	f.n.Add(1)
	forwarder, ok := f.inner.(DisasterReplicationForwarder)
	if !ok {
		return nil, unavailableOwner()
	}
	return forwarder.DisasterReplication(ctx, rt)
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

func collectBatchMetrics(eng *batch.Engine) batchMetricSnapshot {
	var out batchMetricSnapshot
	if eng == nil || eng.DB == nil {
		return out
	}
	recs, err := eng.DB.ListBatches(context.Background(), 0)
	if err != nil {
		return out
	}
	for _, rec := range recs {
		if rec.Status == string(batch.StatusRunning) {
			out.Running++
		}
		var sum batch.Summary
		if rec.SummaryJSON != "" {
			_ = json.Unmarshal([]byte(rec.SummaryJSON), &sum)
		}
		out.Success += sum.Success
		out.Failed += sum.Failed
		out.Timeout += sum.Timeout
		out.Denied += sum.Denied
		out.Conflict += sum.Conflict
		out.Unavailable += sum.Unavailable
		out.Invalid += sum.Invalid
	}
	return out
}

func renderMetrics(uptimeSeconds float64, running, members, alive int, rpcForward uint64, quorum int, batchStats batchMetricSnapshot, sampleRows int64, backupLastSuccess int64) []byte {
	body := fmt.Sprintf(
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
			"procmesh_rpc_forward_total %d\n"+
			"# HELP procmesh_cluster_control_quorum Whether this node sees a Raft leader (1) or not (0).\n"+
			"# TYPE procmesh_cluster_control_quorum gauge\n"+
			"procmesh_cluster_control_quorum %d\n"+
			"# HELP procmesh_batch_running Number of local batches with status=RUNNING.\n"+
			"# TYPE procmesh_batch_running gauge\n"+
			"procmesh_batch_running %d\n"+
			"# HELP procmesh_batch_targets_total Local batch target counts by terminal status.\n"+
			"# TYPE procmesh_batch_targets_total gauge\n"+
			"procmesh_batch_targets_total{status=\"success\"} %d\n"+
			"procmesh_batch_targets_total{status=\"failed\"} %d\n"+
			"procmesh_batch_targets_total{status=\"timeout\"} %d\n"+
			"procmesh_batch_targets_total{status=\"denied\"} %d\n"+
			"procmesh_batch_targets_total{status=\"conflict\"} %d\n"+
			"procmesh_batch_targets_total{status=\"unavailable\"} %d\n"+
			"procmesh_batch_targets_total{status=\"invalid\"} %d\n"+
			"# HELP procmesh_metric_samples_rows Local historical metric sample rows.\n"+
			"# TYPE procmesh_metric_samples_rows gauge\n"+
			"procmesh_metric_samples_rows %d\n",
		uptimeSeconds, running, members, alive, rpcForward, quorum,
		batchStats.Running, batchStats.Success, batchStats.Failed, batchStats.Timeout,
		batchStats.Denied, batchStats.Conflict, batchStats.Unavailable, batchStats.Invalid,
		sampleRows,
	)
	return []byte(body + renderAlertSendMetrics() + renderBackupMetrics(backupLastSuccess))
}

func backupLastSuccessUnix(eng *backup.Engine) int64 {
	if eng == nil {
		return 0
	}
	return eng.LastSuccessUnix.Load()
}

func renderBackupMetrics(lastSuccess int64) string {
	return "# HELP procmesh_backup_last_success_unix Unix time of last successful local backup create.\n" +
		"# TYPE procmesh_backup_last_success_unix gauge\n" +
		fmt.Sprintf("procmesh_backup_last_success_unix %d\n", lastSuccess)
}

func renderAlertSendMetrics() string {
	counts := map[string]map[string]uint64{}
	for _, s := range alert.SendTotals() {
		if counts[s.Type] == nil {
			counts[s.Type] = map[string]uint64{}
		}
		counts[s.Type][s.Result] = s.N
	}
	var b []byte
	b = append(b, "# HELP procmesh_alert_send_total Alert outbound send attempts.\n"...)
	b = append(b, "# TYPE procmesh_alert_send_total counter\n"...)
	for _, typ := range []string{"WEBHOOK", "EMAIL", "WECOM", "DINGTALK", "SLACK"} {
		for _, result := range []string{"ok", "error"} {
			var n uint64
			if counts[typ] != nil {
				n = counts[typ][result]
			}
			b = append(b, fmt.Sprintf("procmesh_alert_send_total{type=%q,result=%q} %d\n", typ, result, n)...)
		}
	}
	return string(b)
}

func countMetricSampleRows(st RevisionStore) int64 {
	s, ok := st.(*store.Store)
	if !ok || s == nil {
		return 0
	}
	n, err := s.CountMetricSamples(context.Background())
	if err != nil {
		return 0
	}
	return n
}
