package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
)

// TestClusterPath_FS tests that CreateCluster uses namespaced FS paths.
func TestClusterPath_FS(t *testing.T) {
	ctx := context.Background()
	st, _ := seedProcess(t)
	fsRoot := filepath.Join(t.TempDir(), "fs")
	e := &backup.Engine{
		Store:     st,
		NodeID:    "node-1",
		ClusterID: "cluster-abc",
		Sinks:     map[string]backup.Sink{"fs": backup.NewFSSink(fsRoot)},
		NewID:     func() (string, error) { return "snap-123", nil },
	}

	opts := backup.ClusterCreateOpts{
		RunID:     "run-1",
		TaskID:    "task-a",
		PolicyID:  "policy-1",
		ClusterID: "cluster-abc",
		NodeID:    "node-1",
		Sink:      "fs",
	}

	meta, err := e.CreateCluster(ctx, opts)
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}

	// Verify path follows {fs_dir}/{cluster_id}/{node_id}/{snapshot_id}.json
	expectedPath := filepath.Join(fsRoot, "cluster-abc", "node-1", "snap-123.json")
	if meta.Location != expectedPath {
		t.Errorf("location = %q, want %q", meta.Location, expectedPath)
	}

	// Verify file exists at expected path
	if _, err := filepath.Glob(expectedPath); err != nil {
		t.Errorf("file not found at %s", expectedPath)
	}
}

// TestClusterPath_S3 tests that CreateCluster uses namespaced S3 keys.
func TestClusterPath_S3(t *testing.T) {
	ctx := context.Background()
	st, _ := seedProcess(t)

	fake := newFakeS3(t)
	cfg := backup.S3Config{
		Endpoint:  fake.URL,
		Bucket:    "test-bucket",
		Prefix:    "backups",
		Region:    "us-east-1",
		AccessKey: "test",
		SecretKey: "test",
		Insecure:  true,
	}
	s3Sink, err := backup.NewS3Sink(cfg)
	if err != nil {
		t.Fatalf("NewS3Sink failed: %v", err)
	}

	e := &backup.Engine{
		Store:     st,
		NodeID:    "node-2",
		ClusterID: "cluster-xyz",
		Sinks:     map[string]backup.Sink{"s3": s3Sink},
		NewID:     func() (string, error) { return "snap-456", nil },
	}

	opts := backup.ClusterCreateOpts{
		RunID:     "run-2",
		TaskID:    "task-b",
		PolicyID:  "policy-2",
		ClusterID: "cluster-xyz",
		NodeID:    "node-2",
		Sink:      "s3",
	}

	meta, err := e.CreateCluster(ctx, opts)
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}

	// Verify S3 key follows {prefix}/{cluster_id}/{policy_id}/{node_id}/{snapshot_id}.json
	expectedLocation := "s3://test-bucket/backups/cluster-xyz/policy-2/node-2/snap-456.json"
	if meta.Location != expectedLocation {
		t.Errorf("location = %q, want %q", meta.Location, expectedLocation)
	}
}

// TestEngine_CreateCluster_Idempotency tests that repeated calls with same task return the same snapshot.
func TestEngine_CreateCluster_Idempotency(t *testing.T) {
	ctx := context.Background()
	st, _ := seedProcess(t)
	fsRoot := filepath.Join(t.TempDir(), "fs")
	e := &backup.Engine{
		Store:     st,
		NodeID:    "n1",
		ClusterID: "c1",
		Sinks:     map[string]backup.Sink{"fs": backup.NewFSSink(fsRoot)},
		NewID:     func() (string, error) { return "snap-1", nil },
	}

	opts := backup.ClusterCreateOpts{
		RunID:     "run-1",
		TaskID:    "task-a",
		PolicyID:  "policy-1",
		ClusterID: "c1",
		NodeID:    "n1",
		Sink:      "fs",
	}

	// First call
	meta1, err := e.CreateCluster(ctx, opts)
	if err != nil {
		t.Fatalf("first CreateCluster failed: %v", err)
	}

	// Second call with same task_id
	meta2, err := e.CreateCluster(ctx, opts)
	if err != nil {
		t.Fatalf("second CreateCluster failed: %v", err)
	}

	// Must return the same snapshot
	if meta1.SnapshotID != meta2.SnapshotID {
		t.Errorf("snapshot_id mismatch: %q vs %q", meta1.SnapshotID, meta2.SnapshotID)
	}
	if meta1.SHA256 != meta2.SHA256 {
		t.Errorf("checksum mismatch: %q vs %q", meta1.SHA256, meta2.SHA256)
	}
}

func TestEngine_RunClusterTaskReportsRetryableRetentionFailure(t *testing.T) {
	ctx := context.Background()
	st, _ := seedProcess(t)
	sink := &fakeSink{errors: map[string]bool{}}
	ids := []string{"snap-old", "snap-new"}
	times := []time.Time{time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC), time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)}
	index := 0
	e := &backup.Engine{
		Store: st, NodeID: "n1", ClusterID: "c1", Sinks: map[string]backup.Sink{"fs": sink},
		NewID: func() (string, error) { return ids[index], nil }, Now: func() time.Time { return times[index] },
		RetentionPolicy: func(policyID string) (backup.Policy, bool) {
			return backup.Policy{PolicyID: policyID, Sink: "fs", Timezone: "UTC", RetentionKeepLast: 1}, true
		},
	}
	if _, err := e.CreateCluster(ctx, backup.ClusterCreateOpts{RunID: "run-old", TaskID: "task-old", PolicyID: "bp", ClusterID: "c1", NodeID: "n1", Sink: "fs"}); err != nil {
		t.Fatal(err)
	}
	sink.errors["snap-old"] = true
	index = 1
	result, err := e.RunClusterTask(ctx, backup.ClusterTaskRequest{RunID: "run-new", TaskID: "task-new", PolicyID: "bp", NodeID: "n1", Sink: "fs", LeaderTerm: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "RETENTION_FAILED" || result.ErrorCode != "RETENTION_DELETE_FAILED" || result.SnapshotID != "snap-new" || result.SHA256 == "" {
		t.Fatalf("result=%+v", result)
	}
	if _, err := st.GetBackup(ctx, "snap-old"); err != nil {
		t.Fatalf("failed retention removed index: %v", err)
	}
}

// TestEngine_CreateCluster_ChecksumConflict tests that same task with different checksum returns CONFLICT.
func TestEngine_CreateCluster_ChecksumConflict(t *testing.T) {
	ctx := context.Background()
	st, spec := seedProcess(t)
	fsRoot := filepath.Join(t.TempDir(), "fs")

	callCount := 0
	e := &backup.Engine{
		Store:     st,
		NodeID:    "n1",
		ClusterID: "c1",
		Sinks:     map[string]backup.Sink{"fs": backup.NewFSSink(fsRoot)},
		NewID: func() (string, error) {
			callCount++
			if callCount == 1 {
				return "snap-1", nil
			}
			return "snap-2", nil
		},
	}

	opts := backup.ClusterCreateOpts{
		RunID:     "run-1",
		TaskID:    "task-conflict",
		PolicyID:  "policy-1",
		ClusterID: "c1",
		NodeID:    "n1",
		Sink:      "fs",
	}

	// First call creates snapshot-1
	meta1, err := e.CreateCluster(ctx, opts)
	if err != nil {
		t.Fatalf("first CreateCluster failed: %v", err)
	}

	// Modify process to change checksum
	spec.Command = "/bin/changed"
	if _, err := st.PutSpec(ctx, spec, spec.LatestRevision, "t", "change"); err != nil {
		t.Fatalf("PutSpec failed: %v", err)
	}

	// Second call with same task_id but different data should return CONFLICT
	_, err = e.CreateCluster(ctx, opts)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Errorf("expected CONFLICT error, got: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "checksum") {
		t.Errorf("expected checksum conflict message, got: %v", err)
	}

	// Verify original snapshot is unchanged
	meta2, err := st.GetBackup(ctx, meta1.SnapshotID)
	if err != nil {
		t.Fatalf("GetBackup failed: %v", err)
	}
	if meta2.SHA256 != meta1.SHA256 {
		t.Errorf("original snapshot was modified")
	}
}

// TestEngine_Create_LegacyPathUnchanged tests that old Create method still uses flat paths.
func TestEngine_Create_LegacyPathUnchanged(t *testing.T) {
	ctx := context.Background()
	st, _ := seedProcess(t)
	fsRoot := filepath.Join(t.TempDir(), "fs")
	e := &backup.Engine{
		Store:     st,
		NodeID:    "n1",
		ClusterID: "c1",
		Sinks:     map[string]backup.Sink{"fs": backup.NewFSSink(fsRoot)},
		NewID:     func() (string, error) { return "snap-legacy", nil },
	}

	// Old Create method
	meta, err := e.Create(ctx, backup.CreateOpts{Sink: "fs"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify path is still flat: {fs_dir}/{snapshot_id}.json
	expectedPath := filepath.Join(fsRoot, "snap-legacy.json")
	if meta.Location != expectedPath {
		t.Errorf("location = %q, want %q", meta.Location, expectedPath)
	}
}

func TestEngine_RunClusterTaskUsesDestinationProfile(t *testing.T) {
	ctx := context.Background()
	st, _ := seedProcess(t)
	profileRoot := filepath.Join(t.TempDir(), "archive")
	var resolved string
	e := &backup.Engine{
		Store:     st,
		NodeID:    "node-1",
		ClusterID: "cluster-1",
		ResolveDestination: func(profile string) (backup.Sink, error) {
			resolved = profile
			if profile != "archive" {
				return nil, errcode.E(errcode.INVALID, "destination profile not configured")
			}
			return backup.NewFSSink(profileRoot), nil
		},
		NewID: func() (string, error) { return "snap-profile", nil },
	}

	result, err := e.RunClusterTask(ctx, backup.ClusterTaskRequest{
		RunID: "run-profile", TaskID: "task-profile", PolicyID: "policy-1", NodeID: "node-1",
		Sink: "s3", DestinationProfile: "archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "archive" {
		t.Fatalf("resolved profile = %q", resolved)
	}
	if result.Status != "SUCCESS" || result.SnapshotID != "snap-profile" {
		t.Fatalf("result = %+v", result)
	}
	want := filepath.Join(profileRoot, "cluster-1", "node-1", "snap-profile.json")
	if _, err := filepath.Glob(want); err != nil {
		t.Fatalf("profile sink path %q: %v", want, err)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("profile sink path %q: %v", want, err)
	}
	if result.Bytes != info.Size() {
		t.Fatalf("result bytes = %d, file size = %d", result.Bytes, info.Size())
	}
}

func TestEngine_RunClusterTaskReportsMissingDestinationProfile(t *testing.T) {
	st, _ := seedProcess(t)
	e := &backup.Engine{
		Store: st, NodeID: "node-1", ClusterID: "cluster-1",
		ResolveDestination: func(string) (backup.Sink, error) {
			return nil, errcode.E(errcode.INVALID, "destination profile not configured")
		},
	}

	result, err := e.RunClusterTask(context.Background(), backup.ClusterTaskRequest{
		RunID: "run-missing", TaskID: "task-missing", NodeID: "node-1",
		Sink: "s3", DestinationProfile: "missing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "CONFIG_MISSING" || result.ErrorCode != "CONFIG_MISSING" {
		t.Fatalf("result = %+v", result)
	}
}

func TestEngine_CheckDestinationReportsAvailabilityWithoutSecrets(t *testing.T) {
	fake := newFakeS3(t)
	s3, err := backup.NewS3Sink(backup.S3Config{
		Endpoint: fake.URL, Bucket: "archive", Prefix: "p", Region: "us-east-1",
		AccessKey: "sensitive-ak", SecretKey: "sensitive-sk", Insecure: true, HTTP: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &backup.Engine{ResolveDestination: func(profile string) (backup.Sink, error) {
		if profile != "archive" {
			return nil, errcode.E(errcode.INVALID, "destination profile not configured")
		}
		return s3, nil
	}}

	health := e.CheckDestination(context.Background(), "s3", "archive")
	if health.Status != "AVAILABLE" || health.EndpointHost == "" || health.ErrorSummary != "" {
		t.Fatalf("health = %+v", health)
	}
	if strings.Contains(health.EndpointHost+health.ErrorSummary, "sensitive-ak") || strings.Contains(health.EndpointHost+health.ErrorSummary, "sensitive-sk") {
		t.Fatalf("credentials leaked: %+v", health)
	}
	missing := e.CheckDestination(context.Background(), "s3", "missing")
	if missing.Status != "CONFIG_MISSING" || missing.ErrorSummary == "" {
		t.Fatalf("missing health = %+v", missing)
	}
}
