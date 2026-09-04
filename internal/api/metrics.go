package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qleelulu/procmesh/internal/alert"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
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

func (f *countingForwarder) Update(ctx context.Context, rt Route) (procmeshv1connect.UpdateServiceClient, error) {
	f.n.Add(1)
	return f.inner.Update(ctx, rt)
}

func (f *countingForwarder) User(ctx context.Context, rt Route) (procmeshv1connect.UserServiceClient, error) {
	f.n.Add(1)
	forwarder, ok := f.inner.(UserForwarder)
	if !ok {
		return nil, unavailableOwner()
	}
	return forwarder.User(ctx, rt)
}

func (f *countingForwarder) Node(ctx context.Context, rt Route) (procmeshv1connect.NodeServiceClient, error) {
	f.n.Add(1)
	forwarder, ok := f.inner.(NodeForwarder)
	if !ok {
		return nil, errcode.E(errcode.UNAVAILABLE, "node forwarder unavailable")
	}
	return forwarder.Node(ctx, rt)
}

func (f *countingForwarder) PromoteCapability(ctx context.Context, rt Route, request control.CapabilityTransferRequest) (control.CapabilityTransferResponse, error) {
	f.n.Add(1)
	forwarder, ok := f.inner.(CapabilityForwarder)
	if !ok {
		return control.CapabilityTransferResponse{}, errcode.E(errcode.UNAVAILABLE, "capability forwarder unavailable")
	}
	return forwarder.PromoteCapability(ctx, rt, request)
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

func (f *countingForwarder) PeerReplication(ctx context.Context, rt Route) (procmeshv1connect.PeerReplicationServiceClient, error) {
	f.n.Add(1)
	forwarder, ok := f.inner.(PeerReplicationForwarder)
	if !ok {
		return nil, unavailableOwner()
	}
	return forwarder.PeerReplication(ctx, rt)
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

func renderMetrics(uptimeSeconds float64, running, members, alive int, rpcForward uint64, quorum int, batchStats batchMetricSnapshot, sampleRows int64, backupLastSuccess int64, clusterSnap clusterBackupMetricSnapshot) []byte {
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
	return []byte(body + renderAlertSendMetrics() + renderBackupMetrics(backupLastSuccess, clusterSnap))
}

func backupLastSuccessUnix(eng *backup.Engine) int64 {
	if eng == nil {
		return 0
	}
	return eng.LastSuccessUnix.Load()
}

func renderBackupMetrics(lastSuccess int64, snap clusterBackupMetricSnapshot) string {
	var b strings.Builder
	b.WriteString("# HELP procmesh_backup_last_success_unix Unix time of last successful local backup create.\n")
	b.WriteString("# TYPE procmesh_backup_last_success_unix gauge\n")
	fmt.Fprintf(&b, "procmesh_backup_last_success_unix %d\n", lastSuccess)
	keys := make([]string, 0, len(snap.LastSuccess))
	for key := range snap.LastSuccess {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		policy, node, _ := strings.Cut(key, "\x00")
		fmt.Fprintf(&b, "procmesh_backup_last_success_unix{policy=%q,node=%q} %d\n", policy, node, snap.LastSuccess[key])
	}
	b.WriteString(renderClusterBackupAndReplicationMetrics(snap))
	return b.String()
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

type clusterBackupMetricSnapshot struct {
	Runs        map[string]uint64 // policy\x00sink\x00status
	Tasks       map[string]uint64 // sink\x00status
	Duration    map[string]float64
	Bytes       map[string]uint64  // sink\x00result
	ReplRuns    map[string]uint64  // policy\x00status
	ReplTasks   map[string]uint64  // status
	Lag         map[string]float64 // source\x00target
	ReplBytes   map[string]uint64  // result
	LastSuccess map[string]int64   // policy\x00node
}

type backupObservationState struct {
	mu        sync.Mutex
	runs      map[string]uint64
	tasks     map[string]uint64
	bytes     map[string]uint64
	duration  map[string]float64
	replRuns  map[string]uint64
	replTasks map[string]uint64
	replBytes map[string]uint64
}

var backupObservations backupObservationState

func collectClusterBackupMetrics(d ClusterDeps) clusterBackupMetricSnapshot {
	snap := observationSnapshot()
	n := d.controlNode()
	if n == nil {
		return snap
	}
	gauges := clusterBackupGaugesFromState(n.View(), time.Now())
	snap.LastSuccess = gauges.LastSuccess
	snap.Lag = gauges.Lag
	return snap
}

func observationSnapshot() clusterBackupMetricSnapshot {
	backupObservations.mu.Lock()
	defer backupObservations.mu.Unlock()
	return clusterBackupMetricSnapshot{
		Runs: cloneUint64Map(backupObservations.runs), Tasks: cloneUint64Map(backupObservations.tasks),
		Duration: cloneFloat64Map(backupObservations.duration), Bytes: cloneUint64Map(backupObservations.bytes),
		ReplRuns: cloneUint64Map(backupObservations.replRuns), ReplTasks: cloneUint64Map(backupObservations.replTasks),
		ReplBytes: cloneUint64Map(backupObservations.replBytes),
		Lag:       map[string]float64{}, LastSuccess: map[string]int64{},
	}
}

func ObserveBackupRun(policy, sink, status string) {
	if policy == "" || !isTerminalRunStatus(status) {
		return
	}
	incObserved(&backupObservations.runs, policy+"\x00"+boundedSink(sink)+"\x00"+boundedStatus(status))
}

func ObserveBackupTask(sink, status string, bytes int64, duration float64) {
	if !isTerminalTaskStatus(status) {
		return
	}
	sink = boundedSink(sink)
	incObserved(&backupObservations.tasks, sink+"\x00"+boundedStatus(status))
	if result := metricResult(status); result != "" {
		addObserved(&backupObservations.bytes, sink+"\x00"+result, uint64(max64(bytes, 0)))
	}
	backupObservations.mu.Lock()
	if backupObservations.duration == nil {
		backupObservations.duration = map[string]float64{}
	}
	backupObservations.duration[sink] = duration
	backupObservations.mu.Unlock()
}

func ObserveReplicationRun(policy, status string) {
	if policy == "" || !isTerminalRunStatus(status) {
		return
	}
	incObserved(&backupObservations.replRuns, policy+"\x00"+boundedStatus(status))
}

func ObserveReplicationTask(status string, bytes int64) {
	if !isTerminalTaskStatus(status) {
		return
	}
	incObserved(&backupObservations.replTasks, boundedStatus(status))
	if result := metricResult(status); result != "" {
		addObserved(&backupObservations.replBytes, result, uint64(max64(bytes, 0)))
	}
}

// ObserveControlTransition increments backup/replication counters for tasks and
// runs that newly reached a terminal status between before and after.
func ObserveControlTransition(before, after control.State, runID string, replication bool) {
	if runID == "" {
		return
	}
	beforeRuns, beforeTasks := observationRunMaps(before, replication)
	afterRuns, afterTasks := observationRunMaps(after, replication)
	run := afterRuns[runID]
	sink := boundedSink(run.Sink)
	for key, task := range afterTasks {
		if task.RunID != runID {
			continue
		}
		prev := beforeTasks[key]
		if isTerminalTaskStatus(prev.Status) || !isTerminalTaskStatus(task.Status) {
			continue
		}
		duration := 0.0
		if run.StartedUnix > 0 && task.UpdatedUnix > run.StartedUnix {
			duration = float64(task.UpdatedUnix - run.StartedUnix)
		}
		if replication {
			ObserveReplicationTask(task.Status, task.Bytes)
		} else {
			ObserveBackupTask(sink, task.Status, task.Bytes, duration)
		}
	}
	prevRun := beforeRuns[runID]
	if !isTerminalRunStatus(prevRun.Status) && isTerminalRunStatus(run.Status) {
		if replication {
			ObserveReplicationRun(run.PolicyID, run.Status)
		} else {
			ObserveBackupRun(run.PolicyID, sink, run.Status)
		}
	}
}

func observationRunMaps(st control.State, replication bool) (map[string]control.ClusterBackupRun, map[string]control.ClusterBackupTask) {
	if replication {
		return st.ReplicationRuns, st.ReplicationTasks
	}
	return st.BackupRuns, st.BackupTasks
}

func clusterBackupGaugesFromState(st control.State, now time.Time) clusterBackupMetricSnapshot {
	snap := clusterBackupMetricSnapshot{
		LastSuccess: map[string]int64{},
		Lag:         map[string]float64{},
	}
	backupRuns := st.BackupRuns
	if backupRuns == nil {
		backupRuns = map[string]control.ClusterBackupRun{}
	}
	for _, task := range st.BackupTasks {
		if metricResult(task.Status) != "success" {
			continue
		}
		run := backupRuns[task.RunID]
		if run.PolicyID == "" || task.NodeID == "" {
			continue
		}
		key := run.PolicyID + "\x00" + task.NodeID
		if task.UpdatedUnix >= snap.LastSuccess[key] {
			snap.LastSuccess[key] = task.UpdatedUnix
		}
	}
	maxUpdated := map[string]int64{}
	for _, task := range st.ReplicationTasks {
		if metricResult(task.Status) != "success" || task.SourceNodeID == "" || task.NodeID == "" || task.UpdatedUnix <= 0 {
			continue
		}
		key := task.SourceNodeID + "\x00" + task.NodeID
		if task.UpdatedUnix > maxUpdated[key] {
			maxUpdated[key] = task.UpdatedUnix
		}
	}
	for key, updated := range maxUpdated {
		lag := float64(now.Unix() - updated)
		if lag < 0 {
			lag = 0
		}
		snap.Lag[key] = lag
	}
	return snap
}

func renderClusterBackupAndReplicationMetrics(snap clusterBackupMetricSnapshot) string {
	var b strings.Builder
	writeMetricFamily(&b, "procmesh_backup_runs_total", "Cluster backup runs by policy, sink, and status.", "counter", labeledCounts(snap.Runs, "policy", "sink", "status"))
	writeMetricFamily(&b, "procmesh_backup_tasks_total", "Cluster backup tasks by sink and status.", "counter", labeledCounts(snap.Tasks, "sink", "status"))
	writeDurationFamily(&b, snap.Duration)
	writeMetricFamily(&b, "procmesh_backup_bytes_total", "Cluster backup bytes by sink and result.", "counter", labeledCounts(snap.Bytes, "sink", "result"))
	writeRetentionFamily(&b)
	writeMetricFamily(&b, "procmesh_replication_runs_total", "Disaster replication runs by policy and status.", "counter", labeledCounts(snap.ReplRuns, "policy", "status"))
	writeMetricFamily(&b, "procmesh_replication_tasks_total", "Disaster replication tasks by status.", "counter", labeledCounts(snap.ReplTasks, "status"))
	writeLagFamily(&b, snap.Lag)
	writeMetricFamily(&b, "procmesh_replication_bytes_total", "Disaster replication bytes by result.", "counter", labeledCounts(snap.ReplBytes, "result"))
	return b.String()
}

func writeRetentionFamily(b *strings.Builder) {
	b.WriteString("# HELP procmesh_backup_retention_delete_total Cluster backup retention deletions by sink and result.\n")
	b.WriteString("# TYPE procmesh_backup_retention_delete_total counter\n")
	totals := retentionDeleteTotals()
	for _, sink := range []string{"fs", "s3"} {
		for _, result := range []string{"success", "error"} {
			fmt.Fprintf(b, "procmesh_backup_retention_delete_total{sink=%q,result=%q} %d\n", sink, result, totals[sink][result])
		}
	}
}

func writeDurationFamily(b *strings.Builder, duration map[string]float64) {
	b.WriteString("# HELP procmesh_backup_task_duration_seconds Last completed cluster backup task duration by sink.\n")
	b.WriteString("# TYPE procmesh_backup_task_duration_seconds gauge\n")
	keys := sortedMapKeys(duration)
	for _, sink := range keys {
		fmt.Fprintf(b, "procmesh_backup_task_duration_seconds{sink=%q} %g\n", sink, duration[sink])
	}
}

func writeLagFamily(b *strings.Builder, lag map[string]float64) {
	b.WriteString("# HELP procmesh_replication_lag_seconds Age of last successful replica copy from source to target.\n")
	b.WriteString("# TYPE procmesh_replication_lag_seconds gauge\n")
	keys := make([]string, 0, len(lag))
	for key := range lag {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		source, target, _ := strings.Cut(key, "\x00")
		fmt.Fprintf(b, "procmesh_replication_lag_seconds{source=%q,target=%q} %g\n", source, target, lag[key])
	}
}

func writeMetricFamily(b *strings.Builder, name, help, typ string, series []metricSeries) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	for _, s := range series {
		b.WriteString(name)
		if s.Labels != "" {
			b.WriteByte('{')
			b.WriteString(s.Labels)
			b.WriteByte('}')
		}
		fmt.Fprintf(b, " %d\n", s.Value)
	}
}

type metricSeries struct {
	Labels string
	Value  uint64
}

func labeledCounts(values map[string]uint64, names ...string) []metricSeries {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]metricSeries, 0, len(keys))
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		if len(parts) != len(names) {
			continue
		}
		labels := make([]string, 0, len(names))
		for i, name := range names {
			labels = append(labels, fmt.Sprintf("%s=%q", name, parts[i]))
		}
		out = append(out, metricSeries{Labels: strings.Join(labels, ","), Value: values[key]})
	}
	return out
}

func sortedMapKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func boundedSink(sink string) string {
	switch sink {
	case "fs", "s3":
		return sink
	default:
		return "fs"
	}
}

func boundedStatus(status string) string {
	switch status {
	case "PENDING", "RUNNING", "SUCCESS", "SUCCEEDED", "FAILED", "PARTIAL", "TIMEOUT", "UNAVAILABLE", "CONFIG_MISSING", "RETENTION_FAILED", "SKIPPED":
		return status
	case "":
		return ""
	default:
		return "FAILED"
	}
}

func metricResult(status string) string {
	switch status {
	case "SUCCESS", "SUCCEEDED":
		return "success"
	case "FAILED", "TIMEOUT", "UNAVAILABLE", "CONFIG_MISSING", "RETENTION_FAILED":
		return "error"
	default:
		return ""
	}
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case "SUCCEEDED", "SUCCESS", "PARTIAL", "FAILED", "CANCELED":
		return true
	default:
		return false
	}
}

func isTerminalTaskStatus(status string) bool {
	switch status {
	case "SUCCEEDED", "SUCCESS", "FAILED", "TIMEOUT", "UNAVAILABLE", "CONFIG_MISSING", "RETENTION_FAILED", "SKIPPED":
		return true
	default:
		return false
	}
}

func incObserved(dst *map[string]uint64, key string) {
	addObserved(dst, key, 1)
}

func addObserved(dst *map[string]uint64, key string, n uint64) {
	backupObservations.mu.Lock()
	defer backupObservations.mu.Unlock()
	if *dst == nil {
		*dst = map[string]uint64{}
	}
	(*dst)[key] += n
}

func cloneUint64Map(in map[string]uint64) map[string]uint64 {
	out := map[string]uint64{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneFloat64Map(in map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func max64(v int64, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}
