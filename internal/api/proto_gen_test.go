package api

import (
	"testing"

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
