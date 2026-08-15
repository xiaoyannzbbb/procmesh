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
