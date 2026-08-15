package agent

import (
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/rpc"
)

func TestAgentForwarder_LogHopHasNoClientTimeout(t *testing.T) {
	if logHopTimeout != 0 {
		t.Fatalf("logHopTimeout=%v want 0", logHopTimeout)
	}
	f := testForwarder(t)
	hc, _, err := f.dial(api.Route{NodeID: "owner", RPC: "127.0.0.1:1"}, logHopTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if hc.Timeout != 0 {
		t.Fatalf("log hop Timeout=%v want 0", hc.Timeout)
	}
	_ = rpc.NewLogClient(hc, "https://127.0.0.1:1")
}

func TestAgentForwarder_ProcessHopUsesMutationTimeout(t *testing.T) {
	if processHopTimeout < 30*time.Second {
		t.Fatalf("processHopTimeout=%v want >=30s", processHopTimeout)
	}
	if processHopTimeout != rpc.MutationTimeout {
		t.Fatalf("processHopTimeout=%v want MutationTimeout=%v", processHopTimeout, rpc.MutationTimeout)
	}
	f := testForwarder(t)
	hc, _, err := f.dial(api.Route{NodeID: "owner", RPC: "127.0.0.1:1"}, processHopTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if hc.Timeout != rpc.MutationTimeout {
		t.Fatalf("process hop Timeout=%v want %v", hc.Timeout, rpc.MutationTimeout)
	}
}

func testForwarder(t *testing.T) *agentForwarder {
	t.Helper()
	b, err := control.NewBundle("cid", "entry", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := &agentForwarder{}
	f.set(control.AgentCreds{
		CACertPEM:    b.CACertPEM,
		AgentCertPEM: b.AgentCertPEM,
		AgentKeyPEM:  b.AgentKeyPEM,
	}, "cid", nil)
	return f
}
