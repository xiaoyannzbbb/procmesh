package alert_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/alert"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/store"
)

type scanEnv struct {
	ctx      context.Context
	st       *store.Store
	now      time.Time
	procs    []alert.ProcessSnap
	snap     alert.NodeSample
	degraded bool
	procCPU  map[string]float64
	procMem  map[string]int64
	pol      control.AlertPolicy
	sc       *alert.Scanner
}

func newScanEnv(t *testing.T) *scanEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	e := &scanEnv{
		ctx:     ctx,
		st:      st,
		now:     time.Unix(1_700_000_400, 0).UTC(), // aligned to a minute
		procCPU: map[string]float64{},
		procMem: map[string]int64{},
		pol:     control.DefaultAlertPolicy(),
	}
	var id int
	eng := &alert.Engine{
		Store:    st,
		NodeID:   "n1",
		NewID:    func() string { id++; return fmt.Sprintf("a%d", id) },
		Policy:   func() control.AlertPolicy { return e.pol },
		Channels: func() []control.AlertChannel { return nil },
		Sender:   &recordingSender{},
		Now:      func() time.Time { return e.now },
	}
	e.sc = &alert.Scanner{
		Engine: eng,
		NodeID: "n1",
		ListProcs: func() []alert.ProcessSnap {
			return e.procs
		},
		Samples: func(ctx context.Context, series, subject, layer string, from, to int64) ([]store.MetricSample, error) {
			return st.ListMetricSamples(ctx, series, subject, layer, from, to)
		},
		Snapshot: func() alert.NodeSample { return e.snap },
		ProcCPU: func(processID string) float64 {
			if v, ok := e.procCPU[processID]; ok {
				return v
			}
			return -1
		},
		ProcMem: func(processID string) int64 {
			if v, ok := e.procMem[processID]; ok {
				return v
			}
			return -1
		},
		Degraded: func() bool { return e.degraded },
		Now:      func() time.Time { return e.now },
	}
	return e
}

func (e *scanEnv) scan(t *testing.T) {
	t.Helper()
	if err := e.sc.ScanLocal(e.ctx); err != nil {
		t.Fatal(err)
	}
}

func (e *scanEnv) insert(t *testing.T, series, subject string, ts time.Time, value float64) {
	t.Helper()
	err := e.st.InsertMetricSamples(e.ctx, []store.MetricSample{{
		Series:    series,
		SubjectID: subject,
		Layer:     metrics.LayerRawMin,
		TSUnix:    metrics.MinuteUnix(ts),
		Value:     value,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *scanEnv) get(t *testing.T, fp string) (store.AlertRecord, bool) {
	t.Helper()
	rec, err := e.st.GetAlertByFingerprint(e.ctx, fp)
	if err != nil {
		if errcode.Is(err, errcode.NOT_FOUND) {
			return store.AlertRecord{}, false
		}
		t.Fatal(err)
	}
	return rec, true
}

func (e *scanEnv) requireFiring(t *testing.T, fp string) store.AlertRecord {
	t.Helper()
	rec, ok := e.get(t, fp)
	if !ok || rec.State != string(alert.StateFiring) {
		t.Fatalf("want FIRING %s, got ok=%v %+v", fp, ok, rec)
	}
	return rec
}

func (e *scanEnv) requireResolved(t *testing.T, fp string) {
	t.Helper()
	rec, ok := e.get(t, fp)
	if !ok || rec.State != string(alert.StateResolved) {
		t.Fatalf("want RESOLVED %s, got ok=%v %+v", fp, ok, rec)
	}
}

func (e *scanEnv) requireNotFiring(t *testing.T, fp string) {
	t.Helper()
	rec, ok := e.get(t, fp)
	if ok && rec.State == string(alert.StateFiring) {
		t.Fatalf("want not FIRING %s, got %+v", fp, rec)
	}
}

func TestScanner_ProcessExitAndRecover(t *testing.T) {
	e := newScanEnv(t)
	e.procs = []alert.ProcessSnap{{
		ProcessID: "p1",
		Desired:   "RUNNING",
		Observed:  "EXITED",
		Health:    "UNKNOWN",
	}}
	e.scan(t)
	e.requireFiring(t, "PROCESS_EXIT:p1")
	e.requireNotFiring(t, "PROCESS_CRASH_LOOP:p1")
	e.requireNotFiring(t, "PROCESS_FATAL:p1")
	e.requireNotFiring(t, "HEALTH_FAILED:p1")

	e.procs[0].Observed = "RUNNING"
	e.procs[0].Health = "HEALTHY"
	e.scan(t)
	e.requireResolved(t, "PROCESS_EXIT:p1")
}

func TestScanner_CrashLoopBackoffAndFatal(t *testing.T) {
	e := newScanEnv(t)
	e.procs = []alert.ProcessSnap{
		{ProcessID: "p1", Desired: "RUNNING", Observed: "BACKOFF", Health: "UNKNOWN"},
		{ProcessID: "p2", Desired: "RUNNING", Observed: "FATAL", Health: "UNKNOWN"},
	}
	e.scan(t)
	e.requireFiring(t, "PROCESS_CRASH_LOOP:p1")
	e.requireFiring(t, "PROCESS_FATAL:p2")
	e.requireNotFiring(t, "PROCESS_EXIT:p1")
	e.requireNotFiring(t, "PROCESS_FATAL:p1")
	e.requireNotFiring(t, "PROCESS_CRASH_LOOP:p2")
}

func TestScanner_HealthFailed(t *testing.T) {
	e := newScanEnv(t)
	e.procs = []alert.ProcessSnap{{
		ProcessID: "p1",
		Desired:   "RUNNING",
		Observed:  "RUNNING",
		Health:    "UNHEALTHY",
	}}
	e.scan(t)
	e.requireFiring(t, "HEALTH_FAILED:p1")
	e.requireNotFiring(t, "PROCESS_EXIT:p1")

	e.procs[0].Health = "HEALTHY"
	e.scan(t)
	e.requireResolved(t, "HEALTH_FAILED:p1")
}

func TestScanner_LocalDBError(t *testing.T) {
	e := newScanEnv(t)
	e.degraded = true
	e.scan(t)
	e.requireFiring(t, "LOCAL_DB_ERROR:n1")

	e.degraded = false
	e.scan(t)
	e.requireResolved(t, "LOCAL_DB_ERROR:n1")
}

func TestScanner_CPUHighNeedsConsecutiveMinutes(t *testing.T) {
	e := newScanEnv(t)
	e.pol.HighConsecutiveMins = 2
	e.pol.CPUHighPercent = 90

	// 1 existing raw_min point ≥ threshold is not enough when N=2.
	e.insert(t, metrics.SeriesNodeCPU, "n1", e.now, 95)
	e.scan(t)
	e.requireNotFiring(t, "CPU_HIGH:n1")

	// Two consecutive existing minutes both ≥ threshold → FIRING.
	e.insert(t, metrics.SeriesNodeCPU, "n1", e.now.Add(-time.Minute), 96)
	e.scan(t)
	e.requireFiring(t, "CPU_HIGH:n1")

	// Gap minutes do not span the consecutive count (now-2m and now, missing now-1m).
	e2 := newScanEnv(t)
	e2.pol.HighConsecutiveMins = 2
	e2.pol.CPUHighPercent = 90
	e2.insert(t, metrics.SeriesNodeCPU, "n1", e2.now, 95)
	e2.insert(t, metrics.SeriesNodeCPU, "n1", e2.now.Add(-2*time.Minute), 97)
	e2.scan(t)
	e2.requireNotFiring(t, "CPU_HIGH:n1")

	// Existing FIRING + gappy window is not recovery — leave the row FIRING.
	if err := e2.st.UpsertAlert(e2.ctx, store.AlertRecord{
		AlertID: "pre-fire", Fingerprint: "CPU_HIGH:n1", Type: "CPU_HIGH",
		Severity: "WARNING", NodeID: "n1", PayloadJSON: `{}`,
		State: string(alert.StateFiring), FirstAt: e2.now.Add(-time.Hour), LastAt: e2.now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	e2.scan(t)
	e2.requireFiring(t, "CPU_HIGH:n1")
}

func TestScanner_CPUHighRecoversOnSnapshotBelow(t *testing.T) {
	e := newScanEnv(t)
	e.pol.HighConsecutiveMins = 2
	e.pol.CPUHighPercent = 90
	e.insert(t, metrics.SeriesNodeCPU, "n1", e.now, 95)
	e.insert(t, metrics.SeriesNodeCPU, "n1", e.now.Add(-time.Minute), 96)
	e.scan(t)
	e.requireFiring(t, "CPU_HIGH:n1")

	// Advance so the high minutes leave the window; snapshot below is recovery.
	e.now = e.now.Add(3 * time.Minute)
	e.snap = alert.NodeSample{CPUPercent: 10, HaveSnapshot: true}
	e.scan(t)
	e.requireResolved(t, "CPU_HIGH:n1")
}

func TestScanner_SnapshotAloneNotEnoughWhenConsecutiveMinsGT1(t *testing.T) {
	e := newScanEnv(t)
	e.pol.HighConsecutiveMins = 2
	e.pol.CPUHighPercent = 90
	e.snap = alert.NodeSample{CPUPercent: 99, HaveSnapshot: true}
	e.scan(t)
	e.requireNotFiring(t, "CPU_HIGH:n1")

	e.pol.HighConsecutiveMins = 1
	e.scan(t)
	e.requireFiring(t, "CPU_HIGH:n1")
}

func TestScanner_ProcessMemSkippedWhenTotalUnknown(t *testing.T) {
	e := newScanEnv(t)
	e.pol.HighConsecutiveMins = 1
	e.pol.MemoryHighPercent = 90
	e.procs = []alert.ProcessSnap{{
		ProcessID: "p1",
		Desired:   "RUNNING",
		Observed:  "RUNNING",
		Health:    "HEALTHY",
	}}
	e.snap = alert.NodeSample{MemoryTotalBytes: 0, HaveSnapshot: true}
	e.procMem["p1"] = 95
	e.insert(t, metrics.SeriesProcMem, "p1", e.now, 95)
	e.scan(t)
	e.requireNotFiring(t, "MEMORY_HIGH:p1")

	e.snap.MemoryTotalBytes = 100
	e.scan(t)
	e.requireFiring(t, "MEMORY_HIGH:p1")
}

func (e *scanEnv) scanCluster(t *testing.T, view alert.ClusterView) {
	t.Helper()
	if err := e.sc.ScanCluster(e.ctx, view); err != nil {
		t.Fatal(err)
	}
}

func (e *scanEnv) requireAbsent(t *testing.T, fp string) {
	t.Helper()
	if rec, ok := e.get(t, fp); ok {
		t.Fatalf("want no row %s, got %+v", fp, rec)
	}
}

func TestScanner_OnlyLeaderSendsAgentFailed(t *testing.T) {
	e := newScanEnv(t)
	view := alert.ClusterView{
		ClusterID:  "cid",
		Leader:     false,
		Voter:      true,
		HasQuorum:  true,
		LeaderAddr: "127.0.0.1:9",
		Members: []cluster.NodeSummary{{
			NodeID:            "n2",
			State:             cluster.StateFailed,
			LastUpdatedUnixMs: e.now.UnixMilli(),
		}},
	}
	e.scanCluster(t, view)
	e.requireAbsent(t, "NODE_FAILED:n2")

	view.Leader = true
	e.scanCluster(t, view)
	e.requireFiring(t, "NODE_FAILED:n2")

	view.Members[0].State = cluster.StateAlive
	e.scanCluster(t, view)
	e.requireResolved(t, "NODE_FAILED:n2")
}

func TestScanner_FollowerDoesNotSendCertOrVersion(t *testing.T) {
	e := newScanEnv(t)
	view := alert.ClusterView{
		ClusterID:  "cid",
		Leader:     false,
		Voter:      true,
		HasQuorum:  true,
		LeaderAddr: "127.0.0.1:9",
		Members: []cluster.NodeSummary{{
			NodeID:          "n2",
			State:           cluster.StateAlive,
			ProtocolVersion: 99,
		}},
		CertNotAfter: map[string]time.Time{
			"n2": e.now.Add(24 * time.Hour),
		},
	}
	e.scanCluster(t, view)
	e.requireAbsent(t, "CERT_EXPIRING:n2")
	e.requireAbsent(t, "VERSION_MISMATCH:n2")
	e.requireAbsent(t, "NODE_SUSPECT:n2")

	view.Leader = true
	e.scanCluster(t, view)
	e.requireFiring(t, "CERT_EXPIRING:n2")
	e.requireFiring(t, "VERSION_MISMATCH:n2")
}

func TestScanner_VoterNoQuorumAfterSuspectWindow(t *testing.T) {
	e := newScanEnv(t)
	e.pol.SuspectTooLongSec = 60
	view := alert.ClusterView{
		ClusterID:  "cid",
		Leader:     false,
		Voter:      true,
		HasQuorum:  false,
		LeaderAddr: "",
	}
	e.scanCluster(t, view)
	e.requireAbsent(t, "CONTROL_NO_QUORUM:cid")

	e.now = e.now.Add(60 * time.Second)
	e.scanCluster(t, view)
	e.requireFiring(t, "CONTROL_NO_QUORUM:cid")

	view.HasQuorum = true
	view.LeaderAddr = "127.0.0.1:9"
	e.scanCluster(t, view)
	e.requireResolved(t, "CONTROL_NO_QUORUM:cid")
}

func TestScanner_NonVoterNoQuorumSilent(t *testing.T) {
	e := newScanEnv(t)
	e.pol.SuspectTooLongSec = 60
	view := alert.ClusterView{
		ClusterID:  "cid",
		Leader:     false,
		Voter:      false,
		HasQuorum:  false,
		LeaderAddr: "",
	}
	e.scanCluster(t, view)
	e.now = e.now.Add(2 * time.Minute)
	e.scanCluster(t, view)
	e.requireAbsent(t, "CONTROL_NO_QUORUM:cid")
}

func TestScanner_LeaderWithoutQuorumSkipsMemberAlerts(t *testing.T) {
	e := newScanEnv(t)
	view := alert.ClusterView{
		ClusterID: "cid",
		Leader:    true,
		Voter:     true,
		HasQuorum: false,
		Members: []cluster.NodeSummary{
			{NodeID: "n2", State: cluster.StateFailed, LastUpdatedUnixMs: e.now.UnixMilli()},
			{NodeID: "n3", State: cluster.StateAlive, ProtocolVersion: 7},
		},
		CertNotAfter: map[string]time.Time{"n3": e.now.Add(time.Hour)},
	}
	e.scanCluster(t, view)
	e.requireAbsent(t, "NODE_FAILED:n2")
	e.requireAbsent(t, "CERT_EXPIRING:n3")
	e.requireAbsent(t, "VERSION_MISMATCH:n3")
}

func TestScanner_LeaderSuspectTooLongAndRecover(t *testing.T) {
	e := newScanEnv(t)
	e.pol.SuspectTooLongSec = 60
	view := alert.ClusterView{
		ClusterID:  "cid",
		Leader:     true,
		Voter:      true,
		HasQuorum:  true,
		LeaderAddr: "127.0.0.1:9",
		Members: []cluster.NodeSummary{{
			NodeID:            "n2",
			State:             cluster.StateSuspect,
			LastUpdatedUnixMs: e.now.Add(-30 * time.Second).UnixMilli(),
		}},
	}
	e.scanCluster(t, view)
	e.requireAbsent(t, "NODE_SUSPECT:n2")

	view.Members[0].LastUpdatedUnixMs = e.now.Add(-60 * time.Second).UnixMilli()
	e.scanCluster(t, view)
	e.requireFiring(t, "NODE_SUSPECT:n2")

	view.Members[0].State = cluster.StateAlive
	e.scanCluster(t, view)
	e.requireResolved(t, "NODE_SUSPECT:n2")
}

func TestScanner_ProtocolZeroAndOneNotMismatch(t *testing.T) {
	e := newScanEnv(t)
	view := alert.ClusterView{
		ClusterID:  "cid",
		Leader:     true,
		Voter:      true,
		HasQuorum:  true,
		LeaderAddr: "127.0.0.1:9",
		Members: []cluster.NodeSummary{
			{NodeID: "n2", State: cluster.StateAlive, ProtocolVersion: 0},
			{NodeID: "n3", State: cluster.StateAlive, ProtocolVersion: 1},
		},
	}
	e.scanCluster(t, view)
	e.requireAbsent(t, "VERSION_MISMATCH:n2")
	e.requireAbsent(t, "VERSION_MISMATCH:n3")
}
