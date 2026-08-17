package api

import (
	"testing"

	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

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
	_ = (&procmeshv1.RemoveNodeRequest{}).GetNodeId
	_ = (&procmeshv1.PromoteNodeRequest{}).GetNodeId
	_ = (&procmeshv1.ClusterOverviewResponse{}).GetControlQuorum
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
