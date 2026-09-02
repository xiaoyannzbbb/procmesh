package api

import (
	"testing"

	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestGeneratedServicesHaveDomainProtoFiles(t *testing.T) {
	wantFiles := map[protoreflect.FullName]string{
		"procmesh.v1.ProcessService":             "procmesh/v1/process.proto",
		"procmesh.v1.ConfigService":              "procmesh/v1/process.proto",
		"procmesh.v1.LogService":                 "procmesh/v1/process.proto",
		"procmesh.v1.NodeService":                "procmesh/v1/cluster.proto",
		"procmesh.v1.ClusterService":             "procmesh/v1/cluster.proto",
		"procmesh.v1.AuthService":                "procmesh/v1/auth.proto",
		"procmesh.v1.UserService":                "procmesh/v1/access.proto",
		"procmesh.v1.RoleService":                "procmesh/v1/access.proto",
		"procmesh.v1.GroupService":               "procmesh/v1/access.proto",
		"procmesh.v1.AuditService":               "procmesh/v1/audit.proto",
		"procmesh.v1.MetricsService":             "procmesh/v1/metrics.proto",
		"procmesh.v1.BatchService":               "procmesh/v1/batch.proto",
		"procmesh.v1.AlertService":               "procmesh/v1/alert.proto",
		"procmesh.v1.BackupService":              "procmesh/v1/backup.proto",
		"procmesh.v1.ClusterBackupService":       "procmesh/v1/cluster_backup.proto",
		"procmesh.v1.ClusterBackupAgentService":  "procmesh/v1/cluster_backup_agent.proto",
		"procmesh.v1.PeerReplicationService":     "procmesh/v1/peer_replication.proto",
		"procmesh.v1.DisasterReplicationService": "procmesh/v1/disaster_replication.proto",
		"procmesh.v1.UpdateService":              "procmesh/v1/update.proto",
	}

	for name, wantFile := range wantFiles {
		t.Run(string(name), func(t *testing.T) {
			desc, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
			if err != nil {
				t.Fatal(err)
			}
			if got := desc.ParentFile().Path(); got != wantFile {
				t.Fatalf("descriptor file = %q, want %q", got, wantFile)
			}
		})
	}
}

func TestGeneratedServiceNames(t *testing.T) {
	if procmeshv1connect.ProcessServiceName != "procmesh.v1.ProcessService" {
		t.Fatalf("process=%s", procmeshv1connect.ProcessServiceName)
	}
	if procmeshv1connect.ConfigServiceName != "procmesh.v1.ConfigService" {
		t.Fatalf("config=%s", procmeshv1connect.ConfigServiceName)
	}
	if procmeshv1connect.LogServiceName != "procmesh.v1.LogService" {
		t.Fatalf("log=%s", procmeshv1connect.LogServiceName)
	}
}

func TestProto_NodeAndClusterServicesGenerated(t *testing.T) {
	if procmeshv1connect.NodeServiceName == "" {
		t.Fatal("missing NodeService")
	}
	if procmeshv1connect.ClusterServiceName == "" {
		t.Fatal("missing ClusterService")
	}
	_ = (&procmeshv1.JoinClusterRequest{}).GetCsrPem
	_ = (&procmeshv1.InitClusterResponse{}).GetAdminPassword
	_ = (&procmeshv1.RequestJoinRequest{}).GetSeedServer
}

func TestProto_P4ServicesGenerated(t *testing.T) {
	if procmeshv1connect.AuthServiceName != "procmesh.v1.AuthService" {
		t.Fatalf("auth=%s", procmeshv1connect.AuthServiceName)
	}
	if procmeshv1connect.UserServiceName != "procmesh.v1.UserService" {
		t.Fatalf("user=%s", procmeshv1connect.UserServiceName)
	}
	if procmeshv1connect.RoleServiceName != "procmesh.v1.RoleService" {
		t.Fatalf("role=%s", procmeshv1connect.RoleServiceName)
	}
	_ = (&procmeshv1.JoinClusterRequest{}).GetRaftAddress
	_ = (&procmeshv1.LoginResponse{}).GetSessionId
	_ = (&procmeshv1.LoginRequest{}).GetTtlSeconds
	_ = (&procmeshv1.RemoveNodeRequest{}).GetNodeId
	_ = (&procmeshv1.PromoteNodeRequest{}).GetNodeId
	_ = (&procmeshv1.ClusterOverviewResponse{}).GetControlQuorum
	_ = (&procmeshv1.Node{}).GetRaftRole
	_ = (&procmeshv1.Node{}).GetRaftRoleFreshness
}

func TestProto_P5ServicesGenerated(t *testing.T) {
	if procmeshv1connect.AuditServiceName != "procmesh.v1.AuditService" {
		t.Fatalf("audit=%s", procmeshv1connect.AuditServiceName)
	}
	if procmeshv1connect.MetricsServiceName != "procmesh.v1.MetricsService" {
		t.Fatalf("metrics=%s", procmeshv1connect.MetricsServiceName)
	}
	if procmeshv1connect.AuthServiceGetMeProcedure == "" {
		t.Fatal("missing AuthService.GetMe")
	}
	_ = (&procmeshv1.ClusterOverviewResponse{}).GetProcessTotal
	_ = (&procmeshv1.Instance{}).GetStartedUnixMs
	_ = (&procmeshv1.LogPolicy{}).GetDirectory
	_ = (&procmeshv1.Instance{}).GetLogPathPending
	_ = (&procmeshv1.Instance{}).GetLastError
}

func TestProto_BatchServiceGenerated(t *testing.T) {
	if procmeshv1connect.BatchServiceName != "procmesh.v1.BatchService" {
		t.Fatalf("batch=%s", procmeshv1connect.BatchServiceName)
	}
	if procmeshv1connect.BatchServiceCreateBatchProcedure == "" {
		t.Fatal("missing BatchService.CreateBatch")
	}
	if procmeshv1connect.BatchServiceGetBatchProcedure == "" {
		t.Fatal("missing BatchService.GetBatch")
	}
	if procmeshv1connect.BatchServiceListBatchesProcedure == "" {
		t.Fatal("missing BatchService.ListBatches")
	}
	if procmeshv1connect.BatchServiceRetryFailedProcedure == "" {
		t.Fatal("missing BatchService.RetryFailed")
	}
	if procmeshv1connect.BatchServiceReplayTimeoutProcedure == "" {
		t.Fatal("missing BatchService.ReplayTimeout")
	}
	if procmeshv1connect.BatchServiceExportBatchProcedure == "" {
		t.Fatal("missing BatchService.ExportBatch")
	}
	_ = (&procmeshv1.Batch{}).GetBatchId
	_ = (&procmeshv1.BatchSelector{}).GetProcessIds
	_ = (&procmeshv1.ProcessNameRef{}).GetNodeId
	_ = (&procmeshv1.BatchSummary{}).GetTimeout
	_ = (&procmeshv1.BatchTarget{}).GetOperationId
	_ = (&procmeshv1.CreateBatchRequest{}).GetType
	var _ procmeshv1connect.BatchServiceHandler = (*BatchAPI)(nil)
}

func TestProto_Q3HistoryRPCsGenerated(t *testing.T) {
	if procmeshv1connect.MetricsServiceGetNodeHistoryProcedure == "" {
		t.Fatal("missing GetNodeHistory")
	}
	if procmeshv1connect.MetricsServiceGetProcessHistoryProcedure == "" {
		t.Fatal("missing GetProcessHistory")
	}
	_ = (&procmeshv1.GetNodeHistoryRequest{}).GetNodeId
	_ = (&procmeshv1.GetNodeHistoryRequest{}).GetSinceUnix
	_ = (&procmeshv1.GetNodeHistoryRequest{}).GetUntilUnix
	_ = (&procmeshv1.GetNodeHistoryRequest{}).GetResolution
	_ = (&procmeshv1.GetProcessHistoryRequest{}).GetIdOrName
	_ = (&procmeshv1.MetricPoint{}).GetTsUnix
	_ = (&procmeshv1.MetricSeries{}).GetName
	var _ procmeshv1connect.MetricsServiceHandler = (*MetricsAPI)(nil)
}

func TestProto_AlertServiceGenerated(t *testing.T) {
	if procmeshv1connect.AlertServiceName != "procmesh.v1.AlertService" {
		t.Fatalf("alert=%s", procmeshv1connect.AlertServiceName)
	}
	if procmeshv1connect.AlertServiceListAlertsProcedure == "" {
		t.Fatal("missing ListAlerts")
	}
	_ = (&procmeshv1.Alert{}).GetFingerprint
	_ = (&procmeshv1.AlertEntry{}).GetFreshness
	_ = (&procmeshv1.AlertChannel{}).GetConfigJson
	_ = (&procmeshv1.AlertPolicy{}).GetDedupWindowSec
	_ = (&procmeshv1.PutAlertChannelRequest{}).GetMeta
	var _ procmeshv1connect.AlertServiceHandler = (*AlertAPI)(nil)
}

func TestProto_BackupServiceGenerated(t *testing.T) {
	if procmeshv1connect.BackupServiceName != "procmesh.v1.BackupService" {
		t.Fatalf("backup=%s", procmeshv1connect.BackupServiceName)
	}
	if procmeshv1connect.BackupServiceCreateBackupProcedure == "" {
		t.Fatal("missing CreateBackup")
	}
	if procmeshv1connect.BackupServiceRestoreBackupProcedure == "" {
		t.Fatal("missing RestoreBackup")
	}
	if procmeshv1connect.BackupServicePutPeerSnapshotProcedure == "" {
		t.Fatal("missing PutPeerSnapshot")
	}
	_ = (&procmeshv1.BackupSnapshot{}).GetSha256
	_ = (&procmeshv1.BackupEntry{}).GetFreshness
	_ = (&procmeshv1.RestoreBackupRequest{}).GetTargets
	_ = (&procmeshv1.CreateBackupRequest{}).GetTargetNodeIds
}

func TestProto_UpdateJobRPCsGenerated(t *testing.T) {
	if procmeshv1connect.UpdateServiceCreateClusterUpdateProcedure == "" {
		t.Fatal("missing CreateClusterUpdate")
	}
	if procmeshv1connect.UpdateServiceGetUpdateJobProcedure == "" {
		t.Fatal("missing GetUpdateJob")
	}
	if procmeshv1connect.UpdateServiceListUpdateJobsProcedure == "" {
		t.Fatal("missing ListUpdateJobs")
	}
	if procmeshv1connect.UpdateServiceCancelRemainingProcedure == "" {
		t.Fatal("missing CancelRemaining")
	}
	if procmeshv1connect.UpdateServiceRetryUpdateJobProcedure == "" {
		t.Fatal("missing RetryUpdateJob")
	}
	if procmeshv1connect.UpdateServiceApplyNodeProcedure == "" {
		t.Fatal("missing ApplyNode")
	}
	_ = (&procmeshv1.UpdateJob{}).GetJobId
	_ = (&procmeshv1.UpdatePin{}).GetTag
	_ = (&procmeshv1.UpdateJobTarget{}).GetSkipReason
	_ = (&procmeshv1.CreateClusterUpdateRequest{}).GetMeta
	_ = (&procmeshv1.ApplyNodeRequest{}).GetNodeId
	_ = (&procmeshv1.GetLocalUpdateInfoResponse{}).GetNodeId
	var _ procmeshv1connect.UpdateServiceHandler = (*UpdateAPI)(nil)
}
