package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/paths"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type backupAcceptNode struct {
	addr, root, id, rpc, control string
	stop                         context.CancelFunc
}

func TestClusterBackup_FS_ThreeAgentNamespace(t *testing.T) {
	nodes := startThreeBackupAgents(t, agentcfg.Backup{})
	joinThree(t, nodes[0].addr, nodes[1].addr, nodes[2].addr)
	seedOwnerSpecs(t, nodes)

	clusterID := waitClusterID(t, nodes[0].addr)
	policyID := uniqueID("bp-fs")
	createClusterBackupPolicy(t, nodes[0].addr, &procmeshv1.ClusterBackupPolicy{
		PolicyId: policyID, Name: policyID, Enabled: true, Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", UnavailablePolicy: "RECORD_AND_CONTINUE",
		TimeoutSeconds: 20, MaxConcurrency: 3,
	})
	run := startClusterBackupRun(t, nodes[0].addr, policyID)
	got := waitClusterBackupRun(t, nodes[0].addr, run.GetRunId(), "SUCCEEDED", 60*time.Second)
	if got.GetSuccess() != 3 || got.GetFailed() != 0 || got.GetUnavailable() != 0 {
		t.Fatalf("want 3 success, got status=%s success=%d failed=%d unavailable=%d tasks=%v", got.GetStatus(), got.GetSuccess(), got.GetFailed(), got.GetUnavailable(), taskStatuses(got))
	}
	for _, node := range nodes {
		assertOnlyNodeNamespace(t, node.root, clusterID, node.id)
	}
}

func TestClusterBackup_PartialUnavailable(t *testing.T) {
	cfg := agentcfg.Backup{}
	nodes := startThreeBackupAgents(t, cfg)
	joinThree(t, nodes[0].addr, nodes[1].addr, nodes[2].addr)
	seedOwnerSpecs(t, nodes)

	offline := nodes[2]
	if offline.rpc == "" || offline.control == "" {
		t.Fatalf("missing listen addrs for offline node rpc=%q control=%q", offline.rpc, offline.control)
	}
	nodes[2].stop()
	policyID := uniqueID("bp-partial")
	createClusterBackupPolicy(t, nodes[0].addr, &procmeshv1.ClusterBackupPolicy{
		PolicyId: policyID, Name: policyID, Enabled: true, Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", UnavailablePolicy: "RECORD_AND_CONTINUE",
		TimeoutSeconds: 15, MaxConcurrency: 3,
	})
	run := startClusterBackupRun(t, nodes[0].addr, policyID)
	got := waitClusterBackupRun(t, nodes[0].addr, run.GetRunId(), "PARTIAL", 45*time.Second)
	if got.GetSuccess() != 2 || got.GetUnavailable() < 1 {
		t.Fatalf("want PARTIAL 2 success + unavailable, got status=%s success=%d unavailable=%d failed=%d tasks=%v", got.GetStatus(), got.GetSuccess(), got.GetUnavailable(), got.GetFailed(), taskStatuses(got))
	}

	addr, stop := startClusterAgentAtOpts(t, offline.root, Options{
		Backup:          cfg,
		DiskPercent:     func() float64 { return 10 },
		RPCListen:       offline.rpc,
		ControlListen:   offline.control,
		OnRPCListen:     func(a string) { nodes[2].rpc = a },
		OnControlListen: func(a string) { nodes[2].control = a },
	})
	nodes[2].addr = addr
	nodes[2].stop = stop
	waitBackupAgentRPC(t, nodes[0].addr, offline.id)

	got = retryUnavailableUntilTerminal(t, nodes[0].addr, run.GetRunId())
	if got.GetStatus() != "SUCCEEDED" || got.GetSuccess() != 3 {
		t.Fatalf("after retry want 3 success, got status=%s success=%d unavailable=%d tasks=%v", got.GetStatus(), got.GetSuccess(), got.GetUnavailable(), taskStatuses(got))
	}
}

func TestClusterBackup_S3_KeysAndRedaction(t *testing.T) {
	const secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	fake := newAcceptFakeS3(t)
	cfg := agentcfg.Backup{S3Profiles: map[string]agentcfg.S3{
		"archive": {Endpoint: fake.URL, Bucket: "backups", Prefix: "cluster", Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: secret, Insecure: true},
	}}
	nodes := startThreeBackupAgents(t, cfg)
	joinThree(t, nodes[0].addr, nodes[1].addr, nodes[2].addr)
	seedOwnerSpecs(t, nodes)

	clusterID := waitClusterID(t, nodes[0].addr)
	policyID := uniqueID("bp-s3")
	createClusterBackupPolicy(t, nodes[0].addr, &procmeshv1.ClusterBackupPolicy{
		PolicyId: policyID, Name: policyID, Enabled: true, Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "s3", DestinationProfile: "archive",
		UnavailablePolicy: "RECORD_AND_CONTINUE", TimeoutSeconds: 25, MaxConcurrency: 3,
	})
	policies, err := clusterBackupClient(t, nodes[0].addr).ListPolicies(context.Background(), connect.NewRequest(&procmeshv1.ListClusterBackupPoliciesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, policies.Msg.String(), secret, "AKIAIOSFODNN7EXAMPLE")

	health, err := clusterBackupClient(t, nodes[0].addr).GetDestinationHealth(context.Background(), connect.NewRequest(&procmeshv1.GetClusterBackupDestinationHealthRequest{Sink: "s3", DestinationProfile: "archive"}))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, health.Msg.String(), secret, "AKIAIOSFODNN7EXAMPLE")
	if health.Msg.GetHealth().GetEndpointHost() == "" {
		t.Fatalf("destination health missing endpoint host: %s", health.Msg.String())
	}

	run := startClusterBackupRun(t, nodes[0].addr, policyID)
	got := waitClusterBackupRun(t, nodes[0].addr, run.GetRunId(), "SUCCEEDED", 60*time.Second)
	if got.GetSuccess() != 3 {
		t.Fatalf("s3 run %+v tasks=%v", got, taskStatuses(got))
	}
	assertNoSecret(t, got.String(), secret, "AKIAIOSFODNN7EXAMPLE")

	keys := fake.keys()
	if len(keys) != 3 {
		t.Fatalf("want 3 s3 objects, got %v", keys)
	}
	for _, node := range nodes {
		found := false
		prefix := "cluster/" + clusterID + "/" + policyID + "/" + node.id + "/"
		for _, key := range keys {
			if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, ".json") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing s3 key for node %s in %v", node.id, keys)
		}
	}

	audits, err := procmeshv1connect.NewAuditServiceClient(&http.Client{Timeout: 10 * time.Second}, "http://"+nodes[0].addr, testConnectOpts()...).ListAudit(context.Background(), connect.NewRequest(&procmeshv1.ListAuditRequest{Limit: 100}))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, audits.Msg.String(), secret, "AKIAIOSFODNN7EXAMPLE")
}

func TestClusterBackup_LeaderFailover_Schedule(t *testing.T) {
	nodes := startThreeBackupAgents(t, agentcfg.Backup{})
	joinThree(t, nodes[0].addr, nodes[1].addr, nodes[2].addr)
	seedOwnerSpecs(t, nodes)

	policyID := uniqueID("bp-sched")
	createClusterBackupPolicy(t, nodes[0].addr, &procmeshv1.ClusterBackupPolicy{
		PolicyId: policyID, Name: policyID, Enabled: true, ScheduleCron: "* * * * *", Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", UnavailablePolicy: "RECORD_AND_CONTINUE",
		TimeoutSeconds: 8, MaxConcurrency: 3,
	})
	runID := waitPolicyRunID(t, nodes[0].addr, policyID, 75*time.Second)
	clusterID := waitClusterID(t, nodes[0].addr)
	before := countClusterSnapshots(t, nodes, clusterID)
	nodes[0].stop()
	entry := remainingBackupEntry(t, nodes[1:])
	waitStableSingleRun(t, entry, policyID, runID)
	assertNoDuplicateSnapshots(t, nodes[1:], clusterID, before)
}

func TestClusterBackup_LeaderFailover_Upload(t *testing.T) {
	nodes := startThreeBackupAgents(t, agentcfg.Backup{})
	joinThree(t, nodes[0].addr, nodes[1].addr, nodes[2].addr)
	seedOwnerSpecs(t, nodes)

	policyID := uniqueID("bp-upload")
	createClusterBackupPolicy(t, nodes[0].addr, &procmeshv1.ClusterBackupPolicy{
		PolicyId: policyID, Name: policyID, Enabled: true, Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", UnavailablePolicy: "RECORD_AND_CONTINUE",
		TimeoutSeconds: 8, MaxConcurrency: 3,
	})
	run := startClusterBackupRun(t, nodes[0].addr, policyID)
	clusterID := waitClusterID(t, nodes[0].addr)
	before := countClusterSnapshots(t, nodes, clusterID)
	nodes[0].stop()
	entry := remainingBackupEntry(t, nodes[1:])
	waitStableSingleRun(t, entry, policyID, run.GetRunId())
	assertNoDuplicateSnapshots(t, nodes[1:], clusterID, before)
}

func TestClusterBackup_LeaderFailover_StatusReport(t *testing.T) {
	nodes := startThreeBackupAgents(t, agentcfg.Backup{})
	joinThree(t, nodes[0].addr, nodes[1].addr, nodes[2].addr)
	seedOwnerSpecs(t, nodes)

	policyID := uniqueID("bp-status")
	createClusterBackupPolicy(t, nodes[0].addr, &procmeshv1.ClusterBackupPolicy{
		PolicyId: policyID, Name: policyID, Enabled: true, Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", UnavailablePolicy: "RECORD_AND_CONTINUE",
		TimeoutSeconds: 8, MaxConcurrency: 3,
	})
	run := startClusterBackupRun(t, nodes[0].addr, policyID)
	clusterID := waitClusterID(t, nodes[0].addr)
	waitAnyClusterSnapshot(t, nodes, clusterID)
	before := countClusterSnapshots(t, nodes, clusterID)
	nodes[0].stop()
	entry := remainingBackupEntry(t, nodes[1:])
	waitStableSingleRun(t, entry, policyID, run.GetRunId())
	assertNoDuplicateSnapshots(t, nodes[1:], clusterID, before)
}

func TestDisasterReplication_PeerIdempotencyAndConflict(t *testing.T) {
	nodes := startThreeBackupAgents(t, agentcfg.Backup{})
	joinThree(t, nodes[0].addr, nodes[1].addr, nodes[2].addr)
	seedOwnerSpecs(t, nodes)

	clusterID := waitClusterID(t, nodes[0].addr)
	backupPolicyID := uniqueID("bp-rep")
	createClusterBackupPolicy(t, nodes[0].addr, &procmeshv1.ClusterBackupPolicy{
		PolicyId: backupPolicyID, Name: backupPolicyID, Enabled: true, Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", UnavailablePolicy: "RECORD_AND_CONTINUE",
		TimeoutSeconds: 20, MaxConcurrency: 3,
	})
	backupRun := startClusterBackupRun(t, nodes[0].addr, backupPolicyID)
	waitClusterBackupRun(t, nodes[0].addr, backupRun.GetRunId(), "SUCCEEDED", 60*time.Second)

	rep := disasterReplicationClient(t, nodes[0].addr)
	draft, err := rep.GeneratePolicyDraft(context.Background(), connect.NewRequest(&procmeshv1.GeneratePolicyDraftRequest{
		Name: uniqueID("replica"), Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1, Trigger: "MANUAL", Timezone: "UTC", MaxConcurrency: 3,
	}))
	if err != nil {
		t.Fatal(err)
	}
	policyID := uniqueID("rp")
	if _, err := rep.ApplyPolicyDraft(context.Background(), connect.NewRequest(&procmeshv1.ApplyPolicyDraftRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-apply")}, PolicyId: policyID,
		Draft: draft.Msg.GetDraft(), DraftRevision: draft.Msg.GetDraft().GetDraftRevision(), DraftHash: draft.Msg.GetDraft().GetDraftHash(),
		ExpectedRevision: -1,
	})); err != nil {
		t.Fatal(err)
	}
	started, err := rep.StartRun(context.Background(), connect.NewRequest(&procmeshv1.StartRunRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-rep")}, PolicyId: policyID, PrimaryRunId: backupRun.GetRunId(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	repRun := waitReplicationRun(t, nodes[0].addr, started.Msg.GetRunId(), "", 60*time.Second)
	if repRun.GetStatus() == "PARTIAL" {
		if _, err := rep.RetryFailedRoutes(context.Background(), connect.NewRequest(&procmeshv1.RetryFailedRoutesRequest{
			Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-retry")}, RunId: started.Msg.GetRunId(),
		})); err != nil {
			t.Fatal(err)
		}
		repRun = waitReplicationRun(t, nodes[0].addr, started.Msg.GetRunId(), "SUCCEEDED", 60*time.Second)
	}
	if repRun.GetStatus() != "SUCCEEDED" {
		t.Fatalf("replication run %s status=%s", started.Msg.GetRunId(), repRun.GetStatus())
	}

	foundPeer := false
	for _, node := range nodes {
		matches, err := filepath.Glob(filepath.Join(node.root, "backup", "peer", "*", clusterID, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) > 0 {
			foundPeer = true
			payload, err := os.ReadFile(matches[0])
			if err != nil {
				t.Fatal(err)
			}
			snap, err := backup.Decode(payload)
			if err != nil {
				t.Fatal(err)
			}
			snap.Processes = append([]backup.ProcessDump(nil), snap.Processes...)
			if len(snap.Processes) > 0 {
				snap.Processes[0].Name = snap.Processes[0].Name + "-conflict"
			}
			changed, sha, err := backup.Encode(snap)
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Base(filepath.Dir(filepath.Dir(matches[0])))
			_, err = (&backup.PeerStore{Root: node.root}).ReceiveWithMetadata(context.Background(), backup.ReceiveParams{
				SourceNodeID: source, ClusterID: clusterID, SnapshotID: snap.SnapshotID, SHA256: sha, Payload: changed,
			})
			if !errcode.Is(err, errcode.CONFLICT) {
				t.Fatalf("checksum conflict err=%v path=%s", err, matches[0])
			}
			again, err := (&backup.PeerStore{Root: node.root}).ReceiveWithMetadata(context.Background(), backup.ReceiveParams{
				SourceNodeID: source, ClusterID: clusterID, SnapshotID: snap.SnapshotID, SHA256: snapSHA(t, payload), Payload: payload,
			})
			if err != nil || again.SnapshotID != snap.SnapshotID {
				t.Fatalf("idempotent replay err=%v meta=%+v", err, again)
			}
		}
		list := mustCLI(t, node.addr, "process", "list")
		for _, other := range nodes {
			if other.id == node.id {
				continue
			}
			if listHasProcessName(list, "proc-"+other.id[:min(8, len(other.id))]) {
				t.Fatalf("peer replica must not apply source process on %s: %s", node.id, list)
			}
		}
	}
	if !foundPeer {
		t.Fatal("expected peer replica files after disaster replication run")
	}
}

func TestRestore_OwnerCAS(t *testing.T) {
	nodes := startThreeBackupAgents(t, agentcfg.Backup{})
	joinThree(t, nodes[0].addr, nodes[1].addr, nodes[2].addr)
	seedOwnerSpecs(t, nodes)

	policyID := uniqueID("bp-restore")
	createClusterBackupPolicy(t, nodes[0].addr, &procmeshv1.ClusterBackupPolicy{
		PolicyId: policyID, Name: policyID, Enabled: true, Timezone: "UTC",
		TargetSelector: "ALL_ADMITTED", Sink: "fs", UnavailablePolicy: "RECORD_AND_CONTINUE",
		TimeoutSeconds: 20, MaxConcurrency: 3,
	})
	run := startClusterBackupRun(t, nodes[0].addr, policyID)
	got := waitClusterBackupRun(t, nodes[0].addr, run.GetRunId(), "SUCCEEDED", 60*time.Second)
	var owner *backupAcceptNode
	var snapshotID, processID string
	for _, task := range got.GetTasks() {
		if task.GetStatus() != "SUCCESS" && task.GetStatus() != "SUCCEEDED" {
			continue
		}
		for i := range nodes {
			if nodes[i].id == task.GetNodeId() {
				owner = &nodes[i]
				snapshotID = task.GetSnapshotId()
				processID = "proc-" + shortID(nodes[i].id)
				break
			}
		}
		if owner != nil {
			break
		}
	}
	if owner == nil || snapshotID == "" {
		t.Fatalf("missing owner snapshot: %+v", got)
	}

	mustCLI(t, owner.addr, "process", "apply", "--file", writeBackupSpec(t, processID, "/bin/sleep"), "--expected-revision", "1")
	cmd, rev := mustProcessCommandRev(t, owner.addr, processID)
	if cmd != "/bin/sleep" || rev != 2 {
		t.Fatalf("pre-restore want /bin/sleep rev=2, got %s rev=%d", cmd, rev)
	}

	backupAPI := procmeshv1connect.NewBackupServiceClient(&http.Client{Timeout: 15 * time.Second}, "http://"+owner.addr, testConnectOpts()...)
	conflict, err := backupAPI.RestoreBackup(context.Background(), connect.NewRequest(&procmeshv1.RestoreBackupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-conflict")}, SnapshotId: snapshotID, Sink: "fs",
		Targets: []*procmeshv1.RestoreTarget{{ProcessId: processID, ExpectedRevision: 1}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(conflict.Msg.GetResults()) != 1 || conflict.Msg.GetResults()[0].GetStatus() != "CONFLICT" {
		t.Fatalf("want CONFLICT, got %+v", conflict.Msg.GetResults())
	}
	cmd, rev = mustProcessCommandRev(t, owner.addr, processID)
	if cmd != "/bin/sleep" || rev != 2 {
		t.Fatalf("conflict must not rewrite, got %s rev=%d", cmd, rev)
	}

	ok, err := backupAPI.RestoreBackup(context.Background(), connect.NewRequest(&procmeshv1.RestoreBackupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-restore")}, SnapshotId: snapshotID, Sink: "fs",
		Targets: []*procmeshv1.RestoreTarget{{ProcessId: processID, ExpectedRevision: 2}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.Msg.GetResults()) != 1 || ok.Msg.GetResults()[0].GetStatus() != "SUCCESS" {
		t.Fatalf("want SUCCESS restore, got %+v", ok.Msg.GetResults())
	}
	cmd, rev = mustProcessCommandRev(t, owner.addr, processID)
	if cmd != "/bin/true" {
		t.Fatalf("command must return to snapshot, got %q", cmd)
	}
	if rev != 3 {
		t.Fatalf("latest must become 3, got %d", rev)
	}

	for _, node := range nodes {
		if node.id == owner.id {
			continue
		}
		list := mustCLI(t, node.addr, "process", "list")
		if listHasProcessName(list, processID) {
			t.Fatalf("non-owner %s must not apply restored process: %s", node.id, list)
		}
	}
}

func startThreeBackupAgents(t *testing.T, cfg agentcfg.Backup) []backupAcceptNode {
	t.Helper()
	nodes := make([]backupAcceptNode, 3)
	for i := range nodes {
		root, err := os.MkdirTemp("", "pm")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		n := &nodes[i]
		n.root = root
		addr, stop := startClusterAgentAtOpts(t, root, Options{
			Backup:          cfg,
			DiskPercent:     func() float64 { return 10 },
			OnRPCListen:     func(a string) { n.rpc = a },
			OnControlListen: func(a string) { n.control = a },
		})
		n.addr = addr
		n.stop = stop
		n.id = readNodeID(t, root)
	}
	return nodes
}

func joinThree(t *testing.T, addrA, addrB, addrC string) string {
	t.Helper()
	password := joinTwoAndPassword(t, addrA, addrB)
	token := createJoinToken(t, addrA)
	code, out, errb := runP1CLI("--server", addrC, "agent", "join", "--seed", addrA, "--token", token)
	if code != 0 {
		t.Fatalf("agent join C exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	deadline := time.Now().Add(20 * time.Second)
	var listA, listB, listC string
	for time.Now().Before(deadline) {
		_, listA, _ = runP1CLI("--server", addrA, "node", "list")
		_, listB, _ = runP1CLI("--server", addrB, "node", "list")
		_, listC, _ = runP1CLI("--server", addrC, "node", "list")
		idsA, idsB, idsC := parseNodeIDs(listA), parseNodeIDs(listB), parseNodeIDs(listC)
		if len(idsA) == 3 && len(idsB) == 3 && len(idsC) == 3 && distinctIDs(idsA) && distinctIDs(idsB) && distinctIDs(idsC) {
			ready := true
			for _, id := range idsA {
				if rpcAddrForNode(listA, id) == "" || rpcAddrForNode(listB, id) == "" || rpcAddrForNode(listC, id) == "" {
					ready = false
					break
				}
			}
			if ready {
				waitClusterQuorum(t, addrA)
				return password
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("want 3 members; A=%q B=%q C=%q", listA, listB, listC)
	return ""
}

func seedOwnerSpecs(t *testing.T, nodes []backupAcceptNode) {
	t.Helper()
	for _, node := range nodes {
		name := "proc-" + shortID(node.id)
		mustCLI(t, node.addr, "process", "apply", "--file", writeBackupSpec(t, name, "/bin/true"), "--expected-revision", "0")
	}
}

func createClusterBackupPolicy(t *testing.T, addr string, policy *procmeshv1.ClusterBackupPolicy) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		_, err = clusterBackupClient(t, addr).CreatePolicy(context.Background(), connect.NewRequest(&procmeshv1.CreateClusterBackupPolicyRequest{
			Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-policy")}, Policy: policy,
		}))
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("create policy: %v", err)
}

func startClusterBackupRun(t *testing.T, addr, policyID string) *procmeshv1.ClusterBackupRun {
	t.Helper()
	resp, err := clusterBackupClient(t, addr).StartRun(context.Background(), connect.NewRequest(&procmeshv1.StartClusterBackupRunRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-run")}, PolicyId: policyID,
	}))
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	return resp.Msg.GetRun()
}

func waitClusterBackupRun(t *testing.T, addr, runID, want string, d time.Duration) *procmeshv1.ClusterBackupRun {
	t.Helper()
	deadline := time.Now().Add(d)
	var last *procmeshv1.ClusterBackupRun
	for time.Now().Before(deadline) {
		resp, err := clusterBackupClient(t, addr).GetRun(context.Background(), connect.NewRequest(&procmeshv1.GetClusterBackupRunRequest{RunId: runID}))
		if err == nil {
			last = resp.Msg.GetRun()
			if last.GetStatus() == want {
				return last
			}
			if terminalRunStatus(last.GetStatus()) && last.GetStatus() != want {
				t.Fatalf("run %s status=%s want=%s tasks=%v", runID, last.GetStatus(), want, taskStatuses(last))
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach %s: %+v", runID, want, last)
	return last
}

func waitPolicyRunID(t *testing.T, addr, policyID string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	cli := clusterBackupClient(t, addr)
	for time.Now().Before(deadline) {
		resp, err := cli.ListRuns(context.Background(), connect.NewRequest(&procmeshv1.ListClusterBackupRunsRequest{PolicyId: policyID, Limit: 10}))
		if err == nil && len(resp.Msg.GetRuns()) > 0 {
			return resp.Msg.GetRuns()[0].GetRunId()
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("scheduled run for %s not observed", policyID)
	return ""
}

func waitReplicationRun(t *testing.T, addr, runID, want string, d time.Duration) *procmeshv1.ReplicationRun {
	t.Helper()
	deadline := time.Now().Add(d)
	var last *procmeshv1.ReplicationRun
	cli := disasterReplicationClient(t, addr)
	for time.Now().Before(deadline) {
		resp, err := cli.GetRun(context.Background(), connect.NewRequest(&procmeshv1.GetRunRequest{RunId: runID}))
		if err == nil {
			last = resp.Msg.GetRun()
			if want == "" && terminalRunStatus(last.GetStatus()) {
				return last
			}
			if last.GetStatus() == want {
				return last
			}
			if terminalRunStatus(last.GetStatus()) && last.GetStatus() != want {
				details := make([]string, 0, len(last.GetTasks()))
				for _, task := range last.GetTasks() {
					details = append(details, task.GetSourceNodeId()+">"+strings.Join(task.GetTargetNodeIds(), ",")+":"+task.GetStatus()+":"+task.GetErrorCode()+":"+task.GetErrorSummary())
				}
				t.Fatalf("replication run %s status=%s want=%s tasks=%v", runID, last.GetStatus(), want, details)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("replication run %s did not reach %s: %+v", runID, want, last)
	return last
}

func waitBackupAgentRPC(t *testing.T, addr, nodeID string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		code, out, errb := runP1CLI("--server", addr, "node", "list")
		if code == 0 {
			if rpc := rpcAddrForNode(out, nodeID); rpc != "" {
				return rpc
			}
			last = out
		} else {
			last = errb
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("agent %s rpc not visible on %s: %q", nodeID, addr, last)
	return ""
}

func retryUnavailableUntilTerminal(t *testing.T, addr, runID string) *procmeshv1.ClusterBackupRun {
	t.Helper()
	cli := clusterBackupClient(t, addr)
	deadline := time.Now().Add(45 * time.Second)
	var last *procmeshv1.ClusterBackupRun
	for time.Now().Before(deadline) {
		resp, err := cli.GetRun(context.Background(), connect.NewRequest(&procmeshv1.GetClusterBackupRunRequest{RunId: runID}))
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		last = resp.Msg.GetRun()
		if last.GetStatus() == "SUCCEEDED" && last.GetSuccess() == 3 {
			return last
		}
		if last.GetStatus() == "PARTIAL" || last.GetStatus() == "FAILED" {
			if _, rerr := cli.RetryFailedTasks(context.Background(), connect.NewRequest(&procmeshv1.RetryFailedClusterBackupTasksRequest{
				Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-retry")}, RunId: runID,
			})); rerr != nil {
				t.Fatalf("retry failed tasks: %v", rerr)
			}
		}
		wait := time.Now().Add(20 * time.Second)
		for time.Now().Before(wait) && time.Now().Before(deadline) {
			resp, err = cli.GetRun(context.Background(), connect.NewRequest(&procmeshv1.GetClusterBackupRunRequest{RunId: runID}))
			if err == nil {
				last = resp.Msg.GetRun()
				if last.GetStatus() == "SUCCEEDED" && last.GetSuccess() == 3 {
					return last
				}
				if terminalRunStatus(last.GetStatus()) {
					break
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	t.Fatalf("retry did not reach SUCCEEDED: %+v tasks=%v", last, taskStatuses(last))
	return last
}

func waitBackupAgentRPCChanged(t *testing.T, addr, nodeID, staleRPC string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		code, out, errb := runP1CLI("--server", addr, "node", "list")
		if code == 0 {
			if rpc := rpcAddrForNode(out, nodeID); rpc != "" && rpc != staleRPC {
				return rpc
			}
			last = out
		} else {
			last = errb
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("restarted agent %s rpc did not change from %s on %s: %q", nodeID, staleRPC, addr, last)
	return ""
}

func waitClusterID(t *testing.T, addr string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	cli := procmeshv1connect.NewClusterServiceClient(&http.Client{Timeout: 5 * time.Second}, "http://"+addr, testConnectOpts()...)
	for time.Now().Before(deadline) {
		resp, err := cli.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
		if err == nil && resp.Msg.GetClusterId() != "" {
			return resp.Msg.GetClusterId()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("cluster id unavailable")
	return ""
}

func waitClusterQuorum(t *testing.T, addr string) {
	t.Helper()
	waitClusterOverview(t, addr, 15*time.Second, func(resp *procmeshv1.ClusterOverviewResponse) bool {
		return resp.GetControlQuorum() && resp.GetControlLeader() != ""
	}, "control quorum not ready")
}

func clusterBackupClient(t *testing.T, addr string) procmeshv1connect.ClusterBackupServiceClient {
	t.Helper()
	return procmeshv1connect.NewClusterBackupServiceClient(&http.Client{Timeout: 30 * time.Second}, "http://"+addr, testConnectOpts()...)
}

func disasterReplicationClient(t *testing.T, addr string) procmeshv1connect.DisasterReplicationServiceClient {
	t.Helper()
	return procmeshv1connect.NewDisasterReplicationServiceClient(&http.Client{Timeout: 30 * time.Second}, "http://"+addr, testConnectOpts()...)
}

func assertOnlyNodeNamespace(t *testing.T, root, clusterID, nodeID string) {
	t.Helper()
	clusterDir := filepath.Join(paths.New(root).BackupFSDir(), clusterID)
	entries, err := os.ReadDir(clusterDir)
	if err != nil {
		t.Fatalf("cluster fs dir %s: %v", clusterDir, err)
	}
	if len(entries) != 1 || entries[0].Name() != nodeID || !entries[0].IsDir() {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("agent %s must write only its namespace, got %v", nodeID, names)
	}
	matches, err := filepath.Glob(filepath.Join(clusterDir, nodeID, "*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("missing snapshot in %s/%s: %v %v", clusterDir, nodeID, matches, err)
	}
}

func countClusterSnapshots(t *testing.T, nodes []backupAcceptNode, clusterID string) int {
	t.Helper()
	n := 0
	for _, node := range nodes {
		matches, err := filepath.Glob(filepath.Join(paths.New(node.root).BackupFSDir(), clusterID, node.id, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		n += len(matches)
	}
	return n
}

func waitAnyClusterSnapshot(t *testing.T, nodes []backupAcceptNode, clusterID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if countClusterSnapshots(t, nodes, clusterID) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no cluster snapshot files before status-report failover")
}

func assertNoDuplicateSnapshots(t *testing.T, nodes []backupAcceptNode, clusterID string, before int) {
	t.Helper()
	after := countClusterSnapshots(t, nodes, clusterID)
	if after > 3 {
		t.Fatalf("duplicate snapshots: before=%d after=%d", before, after)
	}
	for _, node := range nodes {
		matches, _ := filepath.Glob(filepath.Join(paths.New(node.root).BackupFSDir(), clusterID, node.id, "*.json"))
		if len(matches) > 1 {
			t.Fatalf("node %s has duplicate snapshots %v", node.id, matches)
		}
	}
}

func assertSinglePolicyRun(t *testing.T, addr, policyID, runID string) {
	t.Helper()
	resp, err := clusterBackupClient(t, addr).ListRuns(context.Background(), connect.NewRequest(&procmeshv1.ListClusterBackupRunsRequest{PolicyId: policyID, Limit: 20}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetRuns()) != 1 || resp.Msg.GetRuns()[0].GetRunId() != runID {
		t.Fatalf("want single run %s, got %+v", runID, resp.Msg.GetRuns())
	}
}

func remainingBackupEntry(t *testing.T, nodes []backupAcceptNode) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, node := range nodes {
			_, err := procmeshv1connect.NewClusterServiceClient(&http.Client{Timeout: 3 * time.Second}, "http://"+node.addr, testConnectOpts()...).Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
			if err == nil {
				return node.addr
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(nodes) == 0 {
		t.Fatal("no remaining agents")
	}
	return nodes[0].addr
}

func waitStableSingleRun(t *testing.T, addr, policyID, runID string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := clusterBackupClient(t, addr).ListRuns(context.Background(), connect.NewRequest(&procmeshv1.ListClusterBackupRunsRequest{PolicyId: policyID, Limit: 20}))
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if len(resp.Msg.GetRuns()) != 1 || resp.Msg.GetRuns()[0].GetRunId() != runID {
			t.Fatalf("want single run %s, got %+v", runID, resp.Msg.GetRuns())
		}
		lastErr = nil
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("list runs after leader loss: %v", lastErr)
	}
	assertSinglePolicyRun(t, addr, policyID, runID)
}

func taskStatuses(run *procmeshv1.ClusterBackupRun) []string {
	out := make([]string, 0, len(run.GetTasks()))
	for _, task := range run.GetTasks() {
		out = append(out, task.GetNodeId()+":"+task.GetStatus()+":"+task.GetErrorCode()+":"+task.GetErrorSummary())
	}
	return out
}

func terminalRunStatus(status string) bool {
	return status == "SUCCEEDED" || status == "SUCCESS" || status == "PARTIAL" || status == "FAILED" || status == "CANCELED"
}

func uniqueID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func snapSHA(t *testing.T, payload []byte) string {
	t.Helper()
	snap, err := backup.Decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	_, sha, err := backup.Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func assertNoSecret(t *testing.T, blob string, secrets ...string) {
	t.Helper()
	lower := strings.ToLower(blob)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(lower, strings.ToLower(secret)) {
			t.Fatalf("secret leaked in %s", blob)
		}
	}
}

type acceptFakeS3 struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
}

func newAcceptFakeS3(t *testing.T) *acceptFakeS3 {
	t.Helper()
	f := &acceptFakeS3{objects: make(map[string][]byte)}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *acceptFakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}

func (f *acceptFakeS3) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	_, key, _ := strings.Cut(trimmed, "/")
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		prefix := r.URL.Query().Get("prefix")
		f.mu.Lock()
		var keys []string
		for k := range f.objects {
			if prefix == "" || strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		f.mu.Unlock()
		var b strings.Builder
		b.WriteString("<ListBucketResult>")
		for _, k := range keys {
			b.WriteString("<Contents><Key>")
			b.WriteString(k)
			b.WriteString("</Key></Contents>")
		}
		b.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, b.String())
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.objects[key] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		f.mu.Lock()
		body, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func TestClusterBackup_S3_ControlPlaneOmitsSecrets(t *testing.T) {
	// Compile-time companion for the three-agent S3 redaction test: policy proto
	// descriptors used by Raft snapshots must not grow secret fields.
	policy := &procmeshv1.ClusterBackupPolicy{PolicyId: "bp", Sink: "s3", DestinationProfile: "archive"}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "secret") || strings.Contains(string(raw), "access_key") {
		t.Fatalf("policy json leaked secret fields: %s", raw)
	}
}
