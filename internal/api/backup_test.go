package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/process"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type failSink struct{}

func (failSink) Name() string { return "s3" }
func (failSink) Put(context.Context, string, []byte) (string, error) {
	return "", errors.New("s3 down")
}
func (failSink) List(context.Context) ([]backup.Listed, error) {
	return nil, errors.New("s3 down")
}
func (failSink) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("s3 down")
}
func (failSink) Delete(context.Context, string) error { return errors.New("s3 down") }

func testBackupEngine(t *testing.T, nodeID string) (*backup.Engine, *process.Manager) {
	t.Helper()
	mgr, st, layout := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "web", Command: "/bin/true"}
	if _, err := mgr.ApplySpec(context.Background(), spec, 0, "op-seed", "t", ""); err != nil {
		t.Fatal(err)
	}
	e := &backup.Engine{
		Store:     st,
		NodeID:    nodeID,
		ClusterID: "c1",
		Apply:     mgr,
		Sinks:     map[string]backup.Sink{"fs": backup.NewFSSink(filepath.Join(layout.Root, "backup", "fs"))},
		PeerStore: &backup.PeerStore{Root: layout.Root},
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		NewID:     func() (string, error) { return "snap-1", nil },
	}
	return e, mgr
}

func TestBackupAPI_CreateRequiresOperationID(t *testing.T) {
	e, _ := testBackupEngine(t, "node-a")
	api := &BackupAPI{Engine: e, LocalID: "node-a"}
	_, err := api.CreateBackup(context.Background(), connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Sink: "fs",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestBackupAPI_CreateFSAndListLIVE(t *testing.T) {
	ctx := context.Background()
	e, _ := testBackupEngine(t, "node-a")
	now := time.Unix(1_700_000_000, 0).UTC()
	api := &BackupAPI{Engine: e, LocalID: "node-a", Now: func() time.Time { return now }}
	created, err := api.CreateBackup(ctx, connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-bak", Operator: "t"},
		Sink: "fs",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if created.Msg.GetSnapshot().GetSnapshotId() != "snap-1" {
		t.Fatalf("snapshot %+v", created.Msg.GetSnapshot())
	}
	listed, err := api.ListBackups(ctx, connect.NewRequest(&procmeshv1.ListBackupsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetEntries()) != 1 {
		t.Fatalf("entries=%d", len(listed.Msg.GetEntries()))
	}
	ent := listed.Msg.GetEntries()[0]
	if ent.GetFreshness() != freshness.LIVE || ent.GetSourceNode() != "node-a" || ent.GetSnapshot() == nil {
		t.Fatalf("entry %+v", ent)
	}
	if ent.GetSnapshot().GetSnapshotId() != "snap-1" {
		t.Fatalf("id %s", ent.GetSnapshot().GetSnapshotId())
	}
	got, err := api.GetBackup(ctx, connect.NewRequest(&procmeshv1.GetBackupRequest{
		SnapshotId:     "snap-1",
		Sink:           "fs",
		IncludePayload: true,
	}))
	if err != nil || got.Msg.GetSnapshot().GetSnapshotId() != "snap-1" || len(got.Msg.GetPayload()) == 0 {
		t.Fatalf("get %+v %v", got, err)
	}
}

func TestBackupAPI_ListBackupsHidesReplicaSnapshots(t *testing.T) {
	ctx := context.Background()
	e, _ := testBackupEngine(t, "node-a")
	e.Sinks[backup.ReplicaSinkName] = backup.NewFSSink(filepath.Join(t.TempDir(), "backup", "replica"))
	now := time.Unix(1_700_000_000, 0).UTC()
	api := &BackupAPI{Engine: e, LocalID: "node-a", Now: func() time.Time { return now }}
	if _, err := api.CreateBackup(ctx, connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-bak", Operator: "t"},
		Sink: "fs",
	})); err != nil {
		t.Fatal(err)
	}
	replicaID := backup.StableReplicationSnapshotID("run-1", "node-a")
	replica, err := e.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: e.NodeID, SnapshotID: replicaID,
	})
	if err != nil {
		t.Fatal(err)
	}

	indexed, err := e.ListLocal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var haveReplica bool
	for _, m := range indexed {
		if m.Sink == backup.ReplicaSinkName && m.SnapshotID == replica.SnapshotID {
			haveReplica = true
			break
		}
	}
	if !haveReplica {
		t.Fatalf("Engine.ListLocal must still index replica snapshots: %+v", indexed)
	}

	page, err := api.ListBackups(ctx, connect.NewRequest(&procmeshv1.ListBackupsRequest{IncludeS3: true}))
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range page.Msg.GetEntries() {
		snap := ent.GetSnapshot()
		if snap != nil && (snap.GetSink() == backup.ReplicaSinkName || snap.GetSnapshotId() == replicaID) {
			t.Fatalf("replica snapshot leaked into default ListBackups: %+v", ent)
		}
	}

	replicaList, err := api.ListBackups(ctx, connect.NewRequest(&procmeshv1.ListBackupsRequest{Sink: backup.ReplicaSinkName}))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ent := range replicaList.Msg.GetEntries() {
		snap := ent.GetSnapshot()
		if snap != nil && snap.GetSnapshotId() == replicaID && snap.GetSink() == backup.ReplicaSinkName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sink=replica ListBackups missing replica snapshot: %+v", replicaList.Msg.GetEntries())
	}
}

func TestBackupAPI_ListMarksS3FailureSTALE(t *testing.T) {
	e, _ := testBackupEngine(t, "node-a")
	e.Sinks["s3"] = failSink{}
	now := time.Unix(1_700_000_000, 0).UTC()
	api := &BackupAPI{Engine: e, LocalID: "node-a", Now: func() time.Time { return now }}
	resp, err := api.ListBackups(context.Background(), connect.NewRequest(&procmeshv1.ListBackupsRequest{
		IncludeS3: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetEntries()) == 0 {
		t.Fatal("S3 failure must not look like an empty success")
	}
	var stale *procmeshv1.BackupEntry
	for _, ent := range resp.Msg.GetEntries() {
		if ent.GetSourceNode() == "s3" {
			stale = ent
			break
		}
	}
	if stale == nil || stale.GetFreshness() != freshness.STALE || stale.GetSnapshot() != nil {
		t.Fatalf("want STALE s3 placeholder, got %+v", resp.Msg.GetEntries())
	}
}

func TestBackupAPI_ListMarksPeerFailureSTALE(t *testing.T) {
	e, _ := testBackupEngine(t, "node-a")
	now := time.Unix(1_700_000_000, 0).UTC()
	api := &BackupAPI{
		Engine:  e,
		LocalID: "node-a",
		Now:     func() time.Time { return now },
		Router:  remoteOwnerRouter("node-a", "node-c", ""),
		Forward: &blockingAuditForwarder{err: errors.New("unavailable")},
	}
	resp, err := api.ListBackups(context.Background(), connect.NewRequest(&procmeshv1.ListBackupsRequest{
		PeerNodeIds: []string{"node-c"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetEntries()) == 0 {
		t.Fatal("peer hop failure must not look like an empty success")
	}
	var stale *procmeshv1.BackupEntry
	for _, ent := range resp.Msg.GetEntries() {
		if ent.GetSourceNode() == "node-c" {
			stale = ent
			break
		}
	}
	if stale == nil || stale.GetFreshness() != freshness.STALE || stale.GetSnapshot() != nil {
		t.Fatalf("want STALE peer placeholder, got %+v", resp.Msg.GetEntries())
	}
}

func TestBackupAPI_RestoreConflict(t *testing.T) {
	ctx := context.Background()
	e, _ := testBackupEngine(t, "node-a")
	api := &BackupAPI{Engine: e, LocalID: "node-a"}
	if _, err := api.CreateBackup(ctx, connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-bak", Operator: "t"},
		Sink: "fs",
	})); err != nil {
		t.Fatal(err)
	}
	_, err := api.RestoreBackup(ctx, connect.NewRequest(&procmeshv1.RestoreBackupRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-empty", Operator: "t"},
		SnapshotId: "snap-1",
		Sink:       "fs",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("empty targets code=%v detail=%s err=%v", code, detail, err)
	}
	resp, err := api.RestoreBackup(ctx, connect.NewRequest(&procmeshv1.RestoreBackupRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-bad", Operator: "t"},
		SnapshotId: "snap-1",
		Sink:       "fs",
		Targets:    []*procmeshv1.RestoreTarget{{ProcessId: "p1", ExpectedRevision: 99}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetResults()) != 1 || resp.Msg.GetResults()[0].GetStatus() != "CONFLICT" {
		t.Fatalf("%+v", resp.Msg.GetResults())
	}
}

func TestBackupAPI_RestoreLocalOwnerIgnoresRemoteSourceNode(t *testing.T) {
	ctx := context.Background()
	e, _ := testBackupEngine(t, "node-a")
	if _, err := e.Create(ctx, backup.CreateOpts{Sink: "fs"}); err != nil {
		t.Fatal(err)
	}
	fwd := &fakeForwarder{err: errors.New("must not hop")}
	api := &BackupAPI{
		Engine:  e,
		LocalID: "node-a",
		Router:  remoteOwnerRouter("node-a", "node-c", ""),
		Forward: fwd,
	}
	for _, src := range []string{"s3", "node-c"} {
		resp, err := api.RestoreBackup(ctx, connect.NewRequest(&procmeshv1.RestoreBackupRequest{
			Meta:         &procmeshv1.MutationMeta{OperationId: "op-src-" + src, Operator: "t"},
			SnapshotId:   "snap-1",
			Sink:         "fs",
			SourceNodeId: src,
			Targets:      []*procmeshv1.RestoreTarget{{ProcessId: "p1", ExpectedRevision: 99}},
		}))
		if err != nil {
			t.Fatalf("source=%s err %v", src, err)
		}
		if len(resp.Msg.GetResults()) != 1 || resp.Msg.GetResults()[0].GetStatus() != "CONFLICT" {
			t.Fatalf("source=%s %+v", src, resp.Msg.GetResults())
		}
	}
	if fwd.backupCalls() != 0 {
		t.Fatalf("hops=%d want 0", fwd.backupCalls())
	}
}

func TestBackupAPI_RestoreHopsToOwner(t *testing.T) {
	ctx := context.Background()
	e, _ := testBackupEngine(t, "node-c")
	if _, err := e.Create(ctx, backup.CreateOpts{Sink: "fs"}); err != nil {
		t.Fatal(err)
	}
	fakeCli := &fakeBackupClient{
		restoreResp: connect.NewResponse(&procmeshv1.RestoreBackupResponse{
			Results: []*procmeshv1.RestoreProcessResult{{ProcessId: "p1", Status: "SUCCESS", NewRevision: 2}},
		}),
	}
	fwd := &fakeForwarder{backup: fakeCli}
	api := &BackupAPI{
		Engine:  e,
		LocalID: "node-a",
		Router:  remoteOwnerRouter("node-a", "node-c", ""),
		Forward: fwd,
	}
	resp, err := api.RestoreBackup(ctx, connect.NewRequest(&procmeshv1.RestoreBackupRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-hop", Operator: "t"},
		SnapshotId: "snap-1",
		Sink:       "fs",
		Targets:    []*procmeshv1.RestoreTarget{{ProcessId: "p1", ExpectedRevision: 1}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.backupCalls() != 1 {
		t.Fatalf("backup hops=%d", fwd.backupCalls())
	}
	if len(fakeCli.restores) != 1 {
		t.Fatalf("remote restores=%d", len(fakeCli.restores))
	}
	if len(resp.Msg.GetResults()) != 1 || resp.Msg.GetResults()[0].GetStatus() != "SUCCESS" {
		t.Fatalf("%+v", resp.Msg.GetResults())
	}
}

func TestBackupAPI_PutPeerSnapshotDoesNotCreateProcess(t *testing.T) {
	ctx := context.Background()
	e, mgr := testBackupEngine(t, "node-a")
	counter := &countingBackupApplier{inner: e.Apply}
	e.Apply = counter
	snap := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "peer-1",
		ClusterID:     "c1",
		NodeID:        "node-b",
		CreatedAt:     time.Unix(1, 0).UTC(),
		Processes: []backup.ProcessDump{{
			ProcessID: "p-remote", Name: "other", MinRevision: 1, MaxRevision: 1,
			Revisions: []backup.RevisionDump{{Revision: 1, Spec: json.RawMessage(`{"process_id":"p-remote","name":"other","command":"/bin/true"}`)}},
		}},
	}
	payload, _, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	api := &BackupAPI{Engine: e, LocalID: "node-a", LocalOnly: true}
	resp, err := api.PutPeerSnapshot(ctx, connect.NewRequest(&procmeshv1.PutPeerSnapshotRequest{
		Meta:         &procmeshv1.MutationMeta{OperationId: "op-peer", Operator: "t"},
		SourceNodeId: "node-b",
		Payload:      payload,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetSnapshot().GetSnapshotId() != "peer-1" {
		t.Fatalf("snapshot %+v", resp.Msg.GetSnapshot())
	}
	if counter.applies != 0 {
		t.Fatalf("ApplySpec calls %d", counter.applies)
	}
	if _, err := mgr.GetSpec(ctx, "p-remote"); err == nil {
		t.Fatal("must not create foreign process")
	}
}

func TestBackupRestoreAudit(t *testing.T) {
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})
	e, _ := testBackupEngine(t, "node-a")
	api := &BackupAPI{Engine: e, Store: e.Store, LocalID: "node-a"}
	if _, err := api.CreateBackup(ctx, connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-bak-audit", Operator: "admin"},
		Sink: "fs",
	})); err != nil {
		t.Fatal(err)
	}
	got, err := api.GetBackup(ctx, connect.NewRequest(&procmeshv1.GetBackupRequest{
		SnapshotId: "snap-1", Sink: "fs", IncludePayload: true,
	}))
	if err != nil || len(got.Msg.GetPayload()) == 0 {
		t.Fatalf("payload required for redaction fixture: %+v %v", got, err)
	}
	payloadSnippet := string(got.Msg.GetPayload())
	_, err = api.RestoreBackup(ctx, connect.NewRequest(&procmeshv1.RestoreBackupRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-restore-audit", Operator: "admin"},
		SnapshotId: "snap-1",
		Sink:       "fs",
		Targets:    []*procmeshv1.RestoreTarget{{ProcessId: "p1", ExpectedRevision: 1}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertControlAudit(t, e.Store, "backup.restore", "SUCCESS", map[string]string{"snapshot_id": "snap-1"})
	events, err := e.Store.ListAuditAll(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	raw := auditBodies(events)
	if strings.Contains(raw, payloadSnippet) || strings.Contains(raw, "secret_key") || strings.Contains(raw, "access_key") {
		t.Fatalf("restore audit leaked payload or secret: %s", raw)
	}
}

func TestBackupAPI_DeniedWithoutPerm(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	putViewerUser(t, svc)
	e, _ := testBackupEngine(t, "node-a")
	api := &BackupAPI{Engine: e, Auth: svc, LocalID: "node-a"}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-view", Username: "viewer"})
	_, err := api.CreateBackup(ctx, connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-den", Operator: "viewer"},
		Sink: "fs",
	}))
	assertDenied(t, err)
	_, err = api.ListBackups(ctx, connect.NewRequest(&procmeshv1.ListBackupsRequest{}))
	assertDenied(t, err)
}

func TestMetrics_BackupLastSuccess(t *testing.T) {
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
		t.Fatalf("missing backup gauge:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE procmesh_backup_last_success_unix gauge") {
		t.Fatalf("missing TYPE:\n%s", body)
	}
}

func TestBackupAPI_LocalOnlyRestoreForeignInvalid(t *testing.T) {
	ctx := context.Background()
	e, _ := testBackupEngine(t, "node-c")
	if _, err := e.Create(ctx, backup.CreateOpts{Sink: "fs"}); err != nil {
		t.Fatal(err)
	}
	api := &BackupAPI{Engine: e, LocalID: "node-a", LocalOnly: true}
	_, err := api.RestoreBackup(ctx, connect.NewRequest(&procmeshv1.RestoreBackupRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-local", Operator: "t"},
		SnapshotId: "snap-1",
		Sink:       "fs",
		Targets:    []*procmeshv1.RestoreTarget{{ProcessId: "p1", ExpectedRevision: 1}},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

type countingBackupApplier struct {
	inner   backup.Applier
	applies int
}

func (c *countingBackupApplier) ApplySpec(ctx context.Context, spec process.ProcessSpec, expectedRevision int64, opID, operator, comment string) (process.ProcessSpec, error) {
	c.applies++
	return c.inner.ApplySpec(ctx, spec, expectedRevision, opID, operator, comment)
}

func (c *countingBackupApplier) GetSpec(ctx context.Context, processID string) (process.ProcessSpec, error) {
	return c.inner.GetSpec(ctx, processID)
}

type fakeBackupClient struct {
	restores    []*connect.Request[procmeshv1.RestoreBackupRequest]
	restoreResp *connect.Response[procmeshv1.RestoreBackupResponse]
	err         error
}

func (f *fakeBackupClient) CreateBackup(context.Context, *connect.Request[procmeshv1.CreateBackupRequest]) (*connect.Response[procmeshv1.CreateBackupResponse], error) {
	return connect.NewResponse(&procmeshv1.CreateBackupResponse{}), nil
}
func (f *fakeBackupClient) ListBackups(context.Context, *connect.Request[procmeshv1.ListBackupsRequest]) (*connect.Response[procmeshv1.ListBackupsResponse], error) {
	return connect.NewResponse(&procmeshv1.ListBackupsResponse{}), nil
}
func (f *fakeBackupClient) GetBackup(context.Context, *connect.Request[procmeshv1.GetBackupRequest]) (*connect.Response[procmeshv1.GetBackupResponse], error) {
	return connect.NewResponse(&procmeshv1.GetBackupResponse{}), nil
}
func (f *fakeBackupClient) DeleteBackup(context.Context, *connect.Request[procmeshv1.DeleteBackupRequest]) (*connect.Response[procmeshv1.DeleteBackupResponse], error) {
	return connect.NewResponse(&procmeshv1.DeleteBackupResponse{}), nil
}
func (f *fakeBackupClient) RestoreBackup(_ context.Context, req *connect.Request[procmeshv1.RestoreBackupRequest]) (*connect.Response[procmeshv1.RestoreBackupResponse], error) {
	f.restores = append(f.restores, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.restoreResp != nil {
		return f.restoreResp, nil
	}
	return connect.NewResponse(&procmeshv1.RestoreBackupResponse{}), nil
}
func (f *fakeBackupClient) PutPeerSnapshot(context.Context, *connect.Request[procmeshv1.PutPeerSnapshotRequest]) (*connect.Response[procmeshv1.PutPeerSnapshotResponse], error) {
	return connect.NewResponse(&procmeshv1.PutPeerSnapshotResponse{}), nil
}

var _ procmeshv1connect.BackupServiceClient = (*fakeBackupClient)(nil)
