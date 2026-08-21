package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestMetrics_ControlQuorum(t *testing.T) {
	m, st, _ := newTestManager(t)
	srv, err := NewServer(Options{
		Mgr:       m,
		Store:     st,
		Started:   time.Now(),
		HasQuorum: func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics %d %q", rec.Code, body)
	}
	if !strings.Contains(body, "procmesh_cluster_control_quorum 0") {
		t.Fatalf("want procmesh_cluster_control_quorum 0, got:\n%s", body)
	}
	if !strings.Contains(body, "# HELP procmesh_cluster_control_quorum Whether this node sees a Raft leader (1) or not (0).") {
		t.Fatalf("missing HELP: %q", body)
	}
	if !strings.Contains(body, "# TYPE procmesh_cluster_control_quorum gauge") {
		t.Fatalf("missing TYPE: %q", body)
	}
	assertBatchMetricsPresent(t, body)
}

func TestMetrics_ControlQuorumTrue(t *testing.T) {
	m, st, _ := newTestManager(t)
	srv, err := NewServer(Options{
		Mgr:       m,
		Store:     st,
		Started:   time.Now(),
		HasQuorum: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics %d %q", rec.Code, body)
	}
	if !strings.Contains(body, "procmesh_cluster_control_quorum 1") {
		t.Fatalf("want procmesh_cluster_control_quorum 1, got:\n%s", body)
	}
	assertBatchMetricsPresent(t, body)
}

func TestMetrics_ForwardTotal(t *testing.T) {
	m, st, _ := newTestManager(t)
	fakeCli := &fakeProcessClient{
		restartResp: connect.NewResponse(&procmeshv1.ProcessRefResponse{
			Process: &procmeshv1.ProcessView{
				ProcessId: "nginx-1",
				Spec:      &procmeshv1.ProcessSpec{Name: "nginx"},
			},
		}),
	}
	fwd := &fakeForwarder{proc: fakeCli}
	srv, err := NewServer(Options{
		Mgr:     m,
		Store:   st,
		Started: time.Now(),
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	})
	if err != nil {
		t.Fatal(err)
	}

	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)

	client := procmeshv1connect.NewProcessServiceClient(hs.Client(), hs.URL)
	_, err = client.RestartProcess(context.Background(), connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta:     &procmeshv1.MutationMeta{OperationId: "op-fwd-metric", Operator: "t"},
		IdOrName: "nginx",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.processCalls() != 1 {
		t.Fatalf("forward Process calls=%d", fwd.processCalls())
	}

	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics %d %q", rec.Code, body)
	}
	if !strings.Contains(body, "procmesh_rpc_forward_total 1") {
		t.Fatalf("want procmesh_rpc_forward_total 1, got:\n%s", body)
	}
	if !strings.Contains(body, "# HELP procmesh_rpc_forward_total Remote owner RPC forward attempts.") {
		t.Fatalf("missing HELP: %q", body)
	}
	if !strings.Contains(body, "# TYPE procmesh_rpc_forward_total counter") {
		t.Fatalf("missing TYPE: %q", body)
	}
	assertBatchMetricsPresent(t, body)
}

func TestMetrics_IncludesBatchGauges(t *testing.T) {
	m, st, _ := newTestManager(t)
	srv, err := NewServer(Options{Mgr: m, Store: st, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics %d %q", rec.Code, rec.Body.String())
	}
	assertBatchMetricsPresent(t, rec.Body.String())
	assertAlertSendMetricsPresent(t, rec.Body.String())
}

func TestMetrics_SampleRowsGauge(t *testing.T) {
	m, st, _ := newTestManager(t)
	_ = st.InsertMetricSamples(context.Background(), []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n", Layer: "raw_min", TSUnix: 1, Value: 1},
		{Series: "node.cpu_percent", SubjectID: "n", Layer: "raw_min", TSUnix: 2, Value: 2},
	})
	srv, err := NewServer(Options{Mgr: m, Store: st, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "procmesh_metric_samples_rows 2") {
		t.Fatalf("%s", rec.Body.String())
	}
}

func assertBatchMetricsPresent(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, "procmesh_batch_running") {
		t.Fatalf("missing procmesh_batch_running:\n%s", body)
	}
	if !strings.Contains(body, "procmesh_batch_targets_total") {
		t.Fatalf("missing procmesh_batch_targets_total:\n%s", body)
	}
	for _, status := range []string{"success", "failed", "timeout", "denied", "conflict", "unavailable", "invalid"} {
		want := `procmesh_batch_targets_total{status="` + status + `"}`
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s:\n%s", want, body)
		}
	}
}

func TestBackupMetricFamilies(t *testing.T) {
	m, st, _ := newTestManager(t)
	eng := &backup.Engine{}
	eng.LastSuccessUnix.Store(1_700_000_000)
	srv, err := NewServer(Options{Mgr: m, Store: st, Started: time.Now(), Backup: eng})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics %d %q", rec.Code, body)
	}
	if !strings.Contains(body, "procmesh_backup_last_success_unix 1700000000") {
		t.Fatalf("legacy unlabeled last success missing:\n%s", body)
	}
	for _, family := range []string{
		"procmesh_backup_runs_total",
		"procmesh_backup_tasks_total",
		"procmesh_backup_task_duration_seconds",
		"procmesh_backup_bytes_total",
		"procmesh_backup_retention_delete_total",
		"procmesh_replication_runs_total",
		"procmesh_replication_tasks_total",
		"procmesh_replication_lag_seconds",
		"procmesh_replication_bytes_total",
	} {
		if !strings.Contains(body, "# TYPE "+family+" ") {
			t.Errorf("missing TYPE %s:\n%s", family, body)
		}
	}
	if !strings.Contains(body, `procmesh_backup_retention_delete_total{sink="fs",result="success"}`) {
		t.Fatalf("missing bounded retention series:\n%s", body)
	}
	assertNoSecretMetricLabels(t, body)
}

func TestBackupMetricRedactsSecrets(t *testing.T) {
	state := control.NewState()
	state.BackupPolicies["bp-1"] = control.BackupPolicy{
		PolicyID: "bp-1", Sink: "s3", DestinationProfile: "https://AKIAIOSFODNN7EXAMPLE:wJalrXUtnFEMI@s3.example.com/bucket",
	}
	state.BackupRuns["run-1"] = control.ClusterBackupRun{
		RunID: "run-1", PolicyID: "bp-1", Sink: "s3", Status: "SUCCESS", StartedUnix: 100, FinishedUnix: 130,
	}
	state.BackupTasks["run-1:task-a"] = control.ClusterBackupTask{
		RunID: "run-1", TaskID: "task-a", NodeID: "node-a", Status: "SUCCESS", Bytes: 42, UpdatedUnix: 1_700_000_000,
		SnapshotID: "snap-1", SHA256: "deadbeef", ErrorSummary: "secret_key=wJalr path=/var/lib/procmesh/backup/index.json",
	}
	state.ReplicationRuns["rr-1"] = control.ClusterBackupRun{RunID: "rr-1", PolicyID: "rp-1", Status: "SUCCESS"}
	state.ReplicationTasks["rr-1:task-b"] = control.ClusterBackupTask{
		RunID: "rr-1", TaskID: "task-b", SourceNodeID: "node-a", NodeID: "node-b", Status: "SUCCESS", Bytes: 9, UpdatedUnix: 1_700_000_010,
	}
	body := renderBackupMetrics(0, clusterBackupMetricsFromState(*state, time.Unix(1_700_000_100, 0)))
	if !strings.Contains(body, `procmesh_backup_runs_total{policy="bp-1",sink="s3",status="SUCCESS"}`) {
		t.Fatalf("missing labeled run series:\n%s", body)
	}
	if !strings.Contains(body, `procmesh_backup_last_success_unix{policy="bp-1",node="node-a"}`) {
		t.Fatalf("missing labeled last success:\n%s", body)
	}
	if !strings.Contains(body, `procmesh_replication_lag_seconds{source="node-a",target="node-b"}`) {
		t.Fatalf("missing replication lag:\n%s", body)
	}
	assertNoSecretMetricLabels(t, body)
	if strings.Contains(body, "s3.example.com") || strings.Contains(body, "/var/lib/procmesh") || strings.Contains(body, "snap-1") || strings.Contains(body, "deadbeef") {
		t.Fatalf("unbounded or secret label leaked:\n%s", body)
	}
}

func assertNoSecretMetricLabels(t *testing.T, body string) {
	t.Helper()
	for _, leaked := range []string{"secret_key", "access_key", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI", "http://", "https://"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("metric output leaked %q:\n%s", leaked, body)
		}
	}
}

func assertAlertSendMetricsPresent(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, "# HELP procmesh_alert_send_total Alert outbound send attempts.") {
		t.Fatalf("missing HELP procmesh_alert_send_total:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE procmesh_alert_send_total counter") {
		t.Fatalf("missing TYPE procmesh_alert_send_total:\n%s", body)
	}
	if !strings.Contains(body, `procmesh_alert_send_total{type="WEBHOOK",result="ok"}`) {
		t.Fatalf("missing WEBHOOK ok series:\n%s", body)
	}
}
