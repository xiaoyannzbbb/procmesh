package agent

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
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
	_ = rpc.NewAuditClient(hc, "https://127.0.0.1:1")
	_ = rpc.NewMetricsClient(hc, "https://127.0.0.1:1")
	_ = rpc.NewAlertClient(hc, "https://127.0.0.1:1")
}

func TestRPCRuntime_StartRPCLockedWiresClusterIDIntoPeerReplicationHandler(t *testing.T) {
	const clusterID, nodeID = "cluster-runtime", "node-runtime"
	dir := t.TempDir()
	bundle, err := control.NewBundle(clusterID, nodeID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := control.WriteBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	r := &rpcRuntime{
		dir: dir, nodeID: nodeID, opt: Options{RPCListen: "127.0.0.1:0"},
		logger: slog.New(slog.DiscardHandler), fwd: &agentForwarder{}, backup: &backup.Engine{},
	}
	if err := r.startRPC(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.shutdown(context.Background()) })
	if r.clusterID != clusterID {
		t.Fatalf("runtime cluster ID = %q, want %q", r.clusterID, clusterID)
	}
	creds := control.AgentCreds{CACertPEM: bundle.CACertPEM, AgentCertPEM: bundle.AgentCertPEM, AgentKeyPEM: bundle.AgentKeyPEM}
	client, base, err := rpc.Dial(rpc.DialConfig{Creds: creds, ClusterID: clusterID, ExpectNodeID: nodeID, Address: r.ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	payload, sha, err := backup.Encode(backup.Snapshot{FormatVersion: 1, SnapshotID: "snapshot-runtime", ClusterID: clusterID, NodeID: nodeID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = rpc.NewPeerReplicationClient(client, base).PutSnapshot(context.Background(), connect.NewRequest(&procmeshv1.PutSnapshotRequest{
		ClusterId: clusterID, SnapshotId: "snapshot-runtime", Sha256: sha, RunId: "run-runtime", TaskId: "task-runtime", Payload: payload,
	}))
	if err != nil {
		t.Fatalf("peer snapshot upload rejected valid mTLS cluster: %v", err)
	}
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
