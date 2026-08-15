package rpc_test

import (
	"net/http"
	"testing"

	"github.com/qleelulu/procmesh/internal/rpc"
)

func TestHeader_TargetAndSource(t *testing.T) {
	h := make(http.Header)
	rpc.SetTarget(h, "node-c")
	rpc.SetSource(h, "node-a")
	if rpc.TargetOf(h) != "node-c" || rpc.SourceOf(h) != "node-a" {
		t.Fatalf("%v", h)
	}
}
