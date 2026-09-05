package agent

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/update"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
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
	_ = rpc.NewUpdateClient(hc, "https://127.0.0.1:1")
}

func TestAgentForwarder_UpdateHopCoversDownloadTimeout(t *testing.T) {
	if updateHopTimeout < update.DownloadTimeout {
		t.Fatalf("updateHopTimeout=%v want >= DownloadTimeout=%v", updateHopTimeout, update.DownloadTimeout)
	}
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
	if err == nil || connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "control unavailable") {
		t.Fatalf("valid mTLS cluster should reach control-state authorization, got %v", err)
	}
}

func TestRPCRuntime_InternalUpdateServiceExposesLocalInfo(t *testing.T) {
	const clusterID, nodeID = "cluster-update", "node-update"
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
		logger: slog.New(slog.DiscardHandler), fwd: &agentForwarder{},
		updateLocal: &update.Applier{
			Enabled: true, GOOS: "linux", GOARCH: "amd64", Version: "v0.1.29",
		},
	}
	if err := r.startRPC(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.shutdown(context.Background()) })

	creds := control.AgentCreds{
		CACertPEM: bundle.CACertPEM, AgentCertPEM: bundle.AgentCertPEM, AgentKeyPEM: bundle.AgentKeyPEM,
	}
	hc, base, err := rpc.Dial(rpc.DialConfig{
		Creds: creds, ClusterID: clusterID, ExpectNodeID: nodeID,
		Address: r.ln.Addr().String(), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := rpc.NewUpdateClient(hc, base).GetLocalUpdateInfo(
		context.Background(), connect.NewRequest(&procmeshv1.GetLocalUpdateInfoRequest{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetNodeId() != nodeID || got.Msg.GetOs() != "linux" || got.Msg.GetArch() != "amd64" ||
		got.Msg.GetVersion() != "v0.1.29" || !got.Msg.GetEnabled() {
		t.Fatalf("local update info = %+v", got.Msg)
	}
}

func TestRPCRuntime_PeerAuthorization(t *testing.T) {
	const (
		clusterID   = "cluster-auth"
		sourceID    = "source"
		targetID    = "target"
		bystanderID = "bystander"
		policyID    = "rp"
		snapshotID  = "snapshot-auth"
		runID       = "run-auth"
		taskID      = "task-auth"
		intentID    = "intent-auth"
	)
	now := time.Now().Truncate(time.Second)
	dir := t.TempDir()
	bundle, err := control.NewBundle(clusterID, targetID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.WriteBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	sourceCreds := issuePeerAgentCreds(t, bundle, clusterID, sourceID, now)
	bystanderCreds := issuePeerAgentCreds(t, bundle, clusterID, bystanderID, now)

	node, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: targetID, ClusterID: clusterID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Shutdown() })
	if err := node.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := waitRaftLeader(node, raftStartTO); err != nil {
		t.Fatal(err)
	}
	apply := func(commandType string, body any) {
		t.Helper()
		command, encodeErr := control.EncodeCommand(commandType, body)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if applyErr := node.Apply(command, raftApplyTO); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: sourceID, Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: targetID, Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: bystanderID, Status: control.MemberAdmitted})
	// Selector-correct routes: selected sources only; admitted non-sources remain targets.
	apply(control.CmdReplicationPolicyPut, control.ReplicationPolicyPutBody{
		OperationID: "policy", PolicyID: policyID, Name: "policy", Enabled: true,
		SourceSelector: "EXPLICIT_NODES", SourceIDs: []string{sourceID}, ReplicaFactor: 2,
		Routes:  []control.ReplicationRoute{{SourceNodeID: sourceID, TargetNodeIDs: []string{bystanderID, targetID}}},
		Trigger: "MANUAL", ExpectedRevision: -1,
	})
	payload, sha, err := backup.Encode(backup.Snapshot{FormatVersion: 1, SnapshotID: snapshotID, ClusterID: clusterID, NodeID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	term := node.CurrentTerm()
	leaseUntil := time.Now().Add(time.Hour).Unix()
	apply(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: "run", LeaderTerm: term, Replication: true,
		Run: control.ClusterBackupRun{RunID: runID, PolicyID: policyID, PolicyRevision: 1, TargetNodeIDs: []string{sourceID}, Status: "RUNNING", LeaseUntilUnix: leaseUntil},
		Tasks: []control.ClusterBackupTask{
			{RunID: runID, TaskID: taskID, SourceNodeID: sourceID, NodeID: targetID, SnapshotID: snapshotID, SHA256: sha, Status: "PENDING"},
			{RunID: runID, TaskID: "task-bystander", SourceNodeID: sourceID, NodeID: bystanderID, SnapshotID: snapshotID, SHA256: sha, Status: "PENDING"},
		},
	})
	apply(control.CmdBackupTaskUpdate, control.UpdateTaskBody{
		OperationID: "begin", LeaderTerm: term, Replication: true,
		Task: control.ClusterBackupTask{RunID: runID, TaskID: taskID, SourceNodeID: sourceID, NodeID: targetID, SnapshotID: snapshotID, SHA256: sha, Status: "RUNNING", UpdatedUnix: now.Unix()},
	})
	apply(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{
		OperationID: "op-intent", Intent: control.ReplicationDeleteIntent{
			IntentID: intentID, PolicyID: policyID, PolicyRevision: 1,
			SourceNodeID: sourceID, TargetNodeID: targetID, SnapshotID: snapshotID,
			LeaderTerm: term, ExpiresUnix: time.Now().Add(time.Hour).Unix(), Status: "PENDING",
		},
	})
	apply(control.CmdReplicationDeleteIntentPut, control.ReplicationDeleteIntentPutBody{
		OperationID: "op-intent-stale", Intent: control.ReplicationDeleteIntent{
			IntentID: "intent-stale", PolicyID: policyID, PolicyRevision: 1,
			SourceNodeID: sourceID, TargetNodeID: targetID, SnapshotID: snapshotID,
			LeaderTerm: term + 10, ExpiresUnix: time.Now().Add(time.Hour).Unix(), Status: "PENDING",
		},
	})

	r := &rpcRuntime{
		dir: dir, nodeID: targetID, node: node, opt: Options{RPCListen: "127.0.0.1:0"},
		logger: slog.New(slog.DiscardHandler), fwd: &agentForwarder{}, backup: &backup.Engine{},
	}
	if err := r.startRPC(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.shutdown(context.Background()) })

	sourceClient := dialPeerReplication(t, sourceCreds, clusterID, targetID, r.ln.Addr().String())
	replica := filepath.Join(dir, "backup", "peer", sourceID, clusterID, snapshotID+".json")
	authorizedPut := func() *procmeshv1.PutSnapshotRequest {
		return &procmeshv1.PutSnapshotRequest{
			ClusterId: clusterID, SnapshotId: snapshotID, Sha256: sha, RunId: runID, TaskId: taskID,
			Payload: payload, PolicyId: policyID, PolicyRevision: 1,
		}
	}
	if _, err := sourceClient.PutSnapshot(context.Background(), connect.NewRequest(authorizedPut())); err != nil {
		t.Fatalf("authorized put: %v", err)
	}
	afterPut := mustReadReplica(t, replica)

	for _, tc := range []struct {
		name   string
		mutate func(*procmeshv1.PutSnapshotRequest)
	}{
		{name: "run id", mutate: func(req *procmeshv1.PutSnapshotRequest) { req.RunId = "changed-run" }},
		{name: "task id", mutate: func(req *procmeshv1.PutSnapshotRequest) { req.TaskId = "changed-task" }},
		{name: "policy id", mutate: func(req *procmeshv1.PutSnapshotRequest) { req.PolicyId = "changed-policy" }},
		{name: "policy revision", mutate: func(req *procmeshv1.PutSnapshotRequest) { req.PolicyRevision = 2 }},
		{name: "snapshot id", mutate: func(req *procmeshv1.PutSnapshotRequest) { req.SnapshotId = "changed-snapshot" }},
		{name: "sha256", mutate: func(req *procmeshv1.PutSnapshotRequest) { req.Sha256 = strings.Repeat("b", 64) }},
		{name: "cluster id", mutate: func(req *procmeshv1.PutSnapshotRequest) { req.ClusterId = "changed-cluster" }},
	} {
		t.Run("put/"+tc.name, func(t *testing.T) {
			req := authorizedPut()
			tc.mutate(req)
			_, err := sourceClient.PutSnapshot(context.Background(), connect.NewRequest(req))
			assertPeerDenied(t, err)
			assertReplicaUnchanged(t, replica, afterPut)
		})
	}

	t.Run("selector isolation", func(t *testing.T) {
		bystanderClient := dialPeerReplication(t, bystanderCreds, clusterID, targetID, r.ln.Addr().String())
		_, err := bystanderClient.PutSnapshot(context.Background(), connect.NewRequest(authorizedPut()))
		assertPeerDenied(t, err)
		assertReplicaUnchanged(t, replica, afterPut)
		_, err = bystanderClient.DeleteSnapshot(context.Background(), connect.NewRequest(&procmeshv1.DeleteSnapshotRequest{
			SourceNodeId: bystanderID, ClusterId: clusterID, SnapshotId: snapshotID,
			IntentId: intentID, PolicyId: policyID, PolicyRevision: 1,
		}))
		assertPeerDenied(t, err)
		assertReplicaUnchanged(t, replica, afterPut)
	})

	exactDelete := &procmeshv1.DeleteSnapshotRequest{
		SourceNodeId: sourceID, ClusterId: clusterID, SnapshotId: snapshotID,
		IntentId: intentID, PolicyId: policyID, PolicyRevision: 1,
	}
	for _, tc := range []struct {
		name string
		req  *procmeshv1.DeleteSnapshotRequest
	}{
		{name: "missing intent", req: &procmeshv1.DeleteSnapshotRequest{SourceNodeId: sourceID, ClusterId: clusterID, SnapshotId: snapshotID, IntentId: "missing", PolicyId: policyID, PolicyRevision: 1}},
		{name: "policy id", req: &procmeshv1.DeleteSnapshotRequest{SourceNodeId: sourceID, ClusterId: clusterID, SnapshotId: snapshotID, IntentId: intentID, PolicyId: "changed-policy", PolicyRevision: 1}},
		{name: "policy revision", req: &procmeshv1.DeleteSnapshotRequest{SourceNodeId: sourceID, ClusterId: clusterID, SnapshotId: snapshotID, IntentId: intentID, PolicyId: policyID, PolicyRevision: 2}},
		{name: "snapshot id", req: &procmeshv1.DeleteSnapshotRequest{SourceNodeId: sourceID, ClusterId: clusterID, SnapshotId: "changed-snapshot", IntentId: intentID, PolicyId: policyID, PolicyRevision: 1}},
		{name: "stale term", req: &procmeshv1.DeleteSnapshotRequest{SourceNodeId: sourceID, ClusterId: clusterID, SnapshotId: snapshotID, IntentId: "intent-stale", PolicyId: policyID, PolicyRevision: 1}},
	} {
		t.Run("delete/"+tc.name, func(t *testing.T) {
			_, err := sourceClient.DeleteSnapshot(context.Background(), connect.NewRequest(tc.req))
			assertPeerDenied(t, err)
			assertReplicaUnchanged(t, replica, afterPut)
		})
	}

	resp, err := sourceClient.DeleteSnapshot(context.Background(), connect.NewRequest(exactDelete))
	if err != nil {
		t.Fatalf("exact delete: %v", err)
	}
	if !resp.Msg.GetDeleted() {
		t.Fatal("exact delete did not remove replica")
	}
	if _, err := os.Stat(replica); !os.IsNotExist(err) {
		t.Fatalf("replica still present after exact delete: %v", err)
	}
	_, err = sourceClient.DeleteSnapshot(context.Background(), connect.NewRequest(exactDelete))
	assertPeerDenied(t, err)
	if _, err := os.Stat(replica); !os.IsNotExist(err) {
		t.Fatalf("denied delete rewrote replica: %v", err)
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

func TestAgentForwarder_UserHopUsesMutationTimeout(t *testing.T) {
	if userHopTimeout != rpc.MutationTimeout {
		t.Fatalf("userHopTimeout=%v want MutationTimeout=%v", userHopTimeout, rpc.MutationTimeout)
	}
	f := testForwarder(t)
	hc, _, err := f.dial(api.Route{NodeID: "leader", RPC: "127.0.0.1:1"}, userHopTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if hc.Timeout != rpc.MutationTimeout {
		t.Fatalf("user hop Timeout=%v want %v", hc.Timeout, rpc.MutationTimeout)
	}
}

func TestCapabilityEndpointRejectsNonLeaderAndAcceptsPreparedLeader(t *testing.T) {
	const (
		clusterID = "capability-cluster"
		leaderID  = "leader"
		targetID  = "target"
	)
	now := time.Now().Truncate(time.Second)
	leaderBundle, err := control.NewBundle(clusterID, leaderID, now)
	if err != nil {
		t.Fatal(err)
	}
	targetCreds := issuePeerAgentCreds(t, leaderBundle, clusterID, targetID, now)
	bystanderCreds := issuePeerAgentCreds(t, leaderBundle, clusterID, "bystander", now)
	leader, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: leaderID, ClusterID: clusterID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = leader.Shutdown() })
	target, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: targetID, ClusterID: clusterID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Shutdown() })
	if err := leader.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := waitRaftLeader(leader, raftStartTO); err != nil {
		t.Fatal(err)
	}
	if err := leader.AddNonvoter(targetID, target.Advertise()); err != nil {
		t.Fatal(err)
	}
	apply := func(typ string, body any) {
		t.Helper()
		command, encodeErr := control.EncodeCommand(typ, body)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if applyErr := leader.Apply(command, raftApplyTO); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	apply(control.CmdBootstrap, control.BootstrapBody{ClusterID: clusterID, AdminUser: "admin", PasswordHash: "hash", AdminUserID: "admin", NowUnix: now.Unix()})
	leaderSerial, _ := control.CertSerial(leaderBundle.AgentCertPEM)
	targetSerial, _ := control.CertSerial(targetCreds.AgentCertPEM)
	bystanderSerial, _ := control.CertSerial(bystanderCreds.AgentCertPEM)
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: leaderID, RaftAddr: leader.Advertise(), CertSerial: leaderSerial, Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: targetID, RaftAddr: target.Advertise(), CertSerial: targetSerial, Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "bystander", CertSerial: bystanderSerial, Status: control.MemberAdmitted})
	fingerprint, err := control.CASPKIFingerprint(leaderBundle.CACertPEM)
	if err != nil {
		t.Fatal(err)
	}
	apply(control.CmdCapabilityInit, control.CapabilityInitBody{CAFingerprint: fingerprint, Epoch: 1, NodeID: leaderID, CertSerial: leaderSerial})
	prepare := control.CapabilityPrepareBody{
		OperationID: "op-capability", NodeID: targetID, CertSerial: targetSerial,
		CAFingerprint: fingerprint, Epoch: 1, LeaderTerm: leader.CurrentTerm(),
		Nonce: "0011223344", ExpiresUnix: now.Add(time.Minute).Unix(),
	}
	apply(control.CmdCapabilityPrepare, prepare)
	deadline := time.Now().Add(5 * time.Second)
	for target.View().AdmissionCapability.Nodes[targetID].Status != control.CapabilityPrepared {
		if time.Now().After(deadline) {
			t.Fatal("target did not apply PREPARED")
		}
		time.Sleep(10 * time.Millisecond)
	}

	targetDir := t.TempDir()
	writeAgentCreds(t, targetDir, targetCreds)
	runtime := &rpcRuntime{
		dir: targetDir, nodeID: targetID, node: target, clusterID: clusterID,
		opt: Options{RPCListen: "127.0.0.1:0"}, logger: slog.New(slog.DiscardHandler), fwd: &agentForwarder{},
	}
	if err := runtime.startRPC(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.shutdown(context.Background()) })
	route := api.Route{NodeID: targetID, RPC: runtime.ln.Addr().String()}
	request := control.CapabilityTransferRequest{Prepare: prepare, CAKeyPEM: leaderBundle.CAKeyPEM}

	wrong := &agentForwarder{}
	wrong.set(bystanderCreds, clusterID, nil)
	if _, err := wrong.PromoteCapability(context.Background(), route, request); err == nil || !strings.Contains(err.Error(), "DENIED") {
		t.Fatalf("non-leader err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "ca.key")); !os.IsNotExist(err) {
		t.Fatalf("non-leader installed CA key: %v", err)
	}

	authorized := &agentForwarder{}
	authorized.set(control.AgentCreds{CACertPEM: leaderBundle.CACertPEM, AgentCertPEM: leaderBundle.AgentCertPEM, AgentKeyPEM: leaderBundle.AgentKeyPEM}, clusterID, nil)
	response, err := authorized.PromoteCapability(context.Background(), route, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.VerifyCapabilityProof(leaderBundle.CACertPEM, control.CapabilityChallenge(prepare), response.Proof); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityEndpointRejectsRevokedTarget(t *testing.T) {
	const (
		clusterID = "capability-cluster"
		leaderID  = "leader"
		targetID  = "target"
	)
	now := time.Now().Truncate(time.Second)
	leaderBundle, err := control.NewBundle(clusterID, leaderID, now)
	if err != nil {
		t.Fatal(err)
	}
	targetCreds := issuePeerAgentCreds(t, leaderBundle, clusterID, targetID, now)
	leader, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: leaderID, ClusterID: clusterID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = leader.Shutdown() })
	target, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: targetID, ClusterID: clusterID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Shutdown() })
	if err := leader.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := waitRaftLeader(leader, raftStartTO); err != nil {
		t.Fatal(err)
	}
	if err := leader.AddNonvoter(targetID, target.Advertise()); err != nil {
		t.Fatal(err)
	}
	apply := func(typ string, body any) {
		t.Helper()
		command, encodeErr := control.EncodeCommand(typ, body)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if applyErr := leader.Apply(command, raftApplyTO); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	apply(control.CmdBootstrap, control.BootstrapBody{ClusterID: clusterID, AdminUser: "admin", PasswordHash: "hash", AdminUserID: "admin", NowUnix: now.Unix()})
	leaderSerial, _ := control.CertSerial(leaderBundle.AgentCertPEM)
	targetSerial, _ := control.CertSerial(targetCreds.AgentCertPEM)
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: leaderID, RaftAddr: leader.Advertise(), CertSerial: leaderSerial, Status: control.MemberAdmitted})
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: targetID, RaftAddr: target.Advertise(), CertSerial: targetSerial, Status: control.MemberAdmitted})
	fingerprint, err := control.CASPKIFingerprint(leaderBundle.CACertPEM)
	if err != nil {
		t.Fatal(err)
	}
	apply(control.CmdCapabilityInit, control.CapabilityInitBody{CAFingerprint: fingerprint, Epoch: 1, NodeID: leaderID, CertSerial: leaderSerial})
	prepare := control.CapabilityPrepareBody{
		OperationID: "op-capability-revoked", NodeID: targetID, CertSerial: targetSerial,
		CAFingerprint: fingerprint, Epoch: 1, LeaderTerm: leader.CurrentTerm(),
		Nonce: "0011223344", ExpiresUnix: now.Add(time.Minute).Unix(),
	}
	apply(control.CmdCapabilityPrepare, prepare)
	deadline := time.Now().Add(5 * time.Second)
	for target.View().AdmissionCapability.Nodes[targetID].Status != control.CapabilityPrepared {
		if time.Now().After(deadline) {
			t.Fatal("target did not apply PREPARED")
		}
		time.Sleep(10 * time.Millisecond)
	}
	apply(control.CmdMemberRemove, control.MemberRemoveBody{NodeID: targetID})
	deadline = time.Now().Add(5 * time.Second)
	for {
		view := target.View()
		member, ok := view.Member(targetID)
		if ok && member.Status == control.MemberRevoked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("target did not apply revoke")
		}
		time.Sleep(10 * time.Millisecond)
	}

	targetDir := t.TempDir()
	writeAgentCreds(t, targetDir, targetCreds)
	runtime := &rpcRuntime{
		dir: targetDir, nodeID: targetID, node: target, clusterID: clusterID,
		opt: Options{RPCListen: "127.0.0.1:0"}, logger: slog.New(slog.DiscardHandler), fwd: &agentForwarder{},
	}
	if err := runtime.startRPC(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.shutdown(context.Background()) })
	route := api.Route{NodeID: targetID, RPC: runtime.ln.Addr().String()}
	request := control.CapabilityTransferRequest{Prepare: prepare, CAKeyPEM: leaderBundle.CAKeyPEM}
	authorized := &agentForwarder{}
	authorized.set(control.AgentCreds{CACertPEM: leaderBundle.CACertPEM, AgentCertPEM: leaderBundle.AgentCertPEM, AgentKeyPEM: leaderBundle.AgentKeyPEM}, clusterID, nil)
	if _, err := authorized.PromoteCapability(context.Background(), route, request); err == nil || !(strings.Contains(err.Error(), "DENIED") || strings.Contains(err.Error(), "CONFLICT")) {
		t.Fatalf("revoked target err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "ca.key")); !os.IsNotExist(err) {
		t.Fatalf("revoked target installed CA key: %v", err)
	}
}

func TestValidLocalPrepareRejectsExpiredChallenge(t *testing.T) {
	const nodeID = "target"
	now := time.Now().Truncate(time.Second)
	bundle, err := control.NewBundle("cluster", nodeID, now)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := control.WriteBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	serial, err := control.CertSerial(bundle.AgentCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := control.CASPKIFingerprint(bundle.CACertPEM)
	if err != nil {
		t.Fatal(err)
	}
	prepare := control.CapabilityPrepareBody{
		OperationID: "op-expired", NodeID: nodeID, CertSerial: serial,
		CAFingerprint: fingerprint, Epoch: 1, LeaderTerm: 2,
		Nonce: "nonce", ExpiresUnix: now.Add(-time.Second).Unix(),
	}
	state := control.NewState()
	state.AdmissionCapability = control.AdmissionCapabilityState{
		CAFingerprint: fingerprint,
		Epoch:         1,
		Nodes: map[string]control.AdmissionCapabilityNode{
			nodeID: {
				Status: control.CapabilityPrepared, OperationID: prepare.OperationID,
				CertSerial: serial, LeaderTerm: prepare.LeaderTerm, Epoch: prepare.Epoch,
				Nonce: prepare.Nonce, ExpiresUnix: prepare.ExpiresUnix,
			},
		},
	}
	runtime := &rpcRuntime{dir: dir, nodeID: nodeID}
	if runtime.validLocalPrepare(*state, prepare) {
		t.Fatal("expired capability challenge accepted")
	}
}

func TestValidLocalPrepareRejectsRevokedMember(t *testing.T) {
	const nodeID = "target"
	now := time.Now().Truncate(time.Second)
	bundle, err := control.NewBundle("cluster", nodeID, now)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := control.WriteBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	serial, err := control.CertSerial(bundle.AgentCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := control.CASPKIFingerprint(bundle.CACertPEM)
	if err != nil {
		t.Fatal(err)
	}
	prepare := control.CapabilityPrepareBody{
		OperationID: "op-revoked", NodeID: nodeID, CertSerial: serial,
		CAFingerprint: fingerprint, Epoch: 1, LeaderTerm: 2,
		Nonce: "nonce", ExpiresUnix: now.Add(time.Minute).Unix(),
	}
	state := control.NewState()
	state.Members[nodeID] = control.Member{NodeID: nodeID, CertSerial: serial, Status: control.MemberRevoked}
	state.CRL[strings.ToUpper(serial)] = struct{}{}
	state.AdmissionCapability = control.AdmissionCapabilityState{
		CAFingerprint: fingerprint,
		Epoch:         1,
		Nodes: map[string]control.AdmissionCapabilityNode{
			nodeID: {
				Status: control.CapabilityPrepared, OperationID: prepare.OperationID,
				CertSerial: serial, LeaderTerm: prepare.LeaderTerm, Epoch: prepare.Epoch,
				Nonce: prepare.Nonce, ExpiresUnix: prepare.ExpiresUnix,
			},
		},
	}
	runtime := &rpcRuntime{dir: dir, nodeID: nodeID}
	if runtime.validLocalPrepare(*state, prepare) {
		t.Fatal("revoked capability target accepted")
	}
	state.Members[nodeID] = control.Member{NodeID: nodeID, CertSerial: serial, Status: control.MemberAdmitted}
	delete(state.CRL, strings.ToUpper(serial))
	if !runtime.validLocalPrepare(*state, prepare) {
		t.Fatal("admitted prepared target rejected")
	}
}

func writeAgentCreds(t *testing.T, dir string, creds control.AgentCreds) {
	t.Helper()
	for _, file := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{name: "ca.crt", data: creds.CACertPEM, mode: 0o640},
		{name: "agent.crt", data: creds.AgentCertPEM, mode: 0o640},
		{name: "agent.key", data: creds.AgentKeyPEM, mode: 0o600},
	} {
		if err := os.WriteFile(filepath.Join(dir, file.name), file.data, file.mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentForwarder_AlertHopUsesMutationTimeout(t *testing.T) {
	if alertHopTimeout != rpc.MutationTimeout {
		t.Fatalf("alertHopTimeout=%v want MutationTimeout=%v", alertHopTimeout, rpc.MutationTimeout)
	}
	f := testForwarder(t)
	hc, _, err := f.dial(api.Route{NodeID: "leader", RPC: "127.0.0.1:1"}, alertHopTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if hc.Timeout != rpc.MutationTimeout {
		t.Fatalf("alert hop Timeout=%v want %v", hc.Timeout, rpc.MutationTimeout)
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

func issuePeerAgentCreds(t *testing.T, bundle control.Bundle, clusterID, nodeID string, now time.Time) control.AgentCreds {
	t.Helper()
	csr, key, err := control.NewCSR(clusterID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := control.SignCSR(bundle.CACertPEM, bundle.CAKeyPEM, csr, clusterID, nodeID, now)
	if err != nil {
		t.Fatal(err)
	}
	return control.AgentCreds{CACertPEM: bundle.CACertPEM, AgentCertPEM: cert, AgentKeyPEM: key}
}

func dialPeerReplication(t *testing.T, creds control.AgentCreds, clusterID, expectNodeID, addr string) procmeshv1connect.PeerReplicationServiceClient {
	t.Helper()
	client, base, err := rpc.Dial(rpc.DialConfig{Creds: creds, ClusterID: clusterID, ExpectNodeID: expectNodeID, Address: addr, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return rpc.NewPeerReplicationClient(client, base)
}

func assertPeerDenied(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected PermissionDenied or FailedPrecondition")
	}
	code := connect.CodeOf(err)
	if code != connect.CodePermissionDenied && code != connect.CodeFailedPrecondition {
		t.Fatalf("error=%v code=%v, want PermissionDenied or FailedPrecondition", err, code)
	}
}

func mustReadReplica(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertReplicaUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replica changed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("denied peer operation wrote replica")
	}
}
