package agent

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/rpc"
	"golang.org/x/sys/unix"
)

func TestAgent_InitClosesUnauth(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	code, out, errb = runP1CLI("--server", addr, "process", "list")
	if code == 0 {
		t.Fatalf("unauth process list should fail after init, stdout=%q", out)
	}
}

func TestP4_LoginRequiredAfterInit(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	pw := parseKV(out, "admin_password")
	if pw == "" {
		t.Fatalf("missing admin_password in %q", out)
	}

	code, out, errb = runP1CLI("--server", addr, "process", "list")
	if code == 0 {
		t.Fatalf("unauth process list should fail after init, stdout=%q", out)
	}
	if !strings.Contains(errb, "DENIED") {
		t.Fatalf("unauth process list want DENIED, stdout=%q stderr=%q", out, errb)
	}

	loginAdmin(t, addr, pw)
	code, out, errb = runP1CLI("--server", addr, "process", "list")
	if code != 0 {
		t.Fatalf("process list after login exit=%d stderr=%q stdout=%q", code, errb, out)
	}
}

// Cluster identity files can disappear while store.cluster_id and raft.db remain.
// Auth then requires a session, but Raft never starts → Login returns UNAVAILABLE.
func TestP4_LoginAfterRestartMissingClusterJSON(t *testing.T) {
	root, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	controlAddr := freeLocalAddr(t)
	addr, stop := startClusterAgentAtOpts(t, root, Options{
		ControlListen:    controlAddr,
		ControlAdvertise: controlAddr,
	})
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	pw := parseKV(out, "admin_password")
	if pw == "" {
		t.Fatalf("missing admin_password in %q", out)
	}
	loginAdmin(t, addr, pw)
	stop()

	clusterDir := filepath.Join(root, "cluster")
	entries, err := os.ReadDir(clusterDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "secret" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(clusterDir, e.Name())); err != nil {
			t.Fatalf("remove %s: %v", e.Name(), err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "raft", "raft.db")); err != nil {
		t.Fatalf("precondition: raft.db must remain: %v", err)
	}

	addr, _ = startClusterAgentAtOpts(t, root, Options{
		ControlListen:    controlAddr,
		ControlAdvertise: controlAddr,
	})
	loginAdmin(t, addr, pw)
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestP4_ViewerCannotRestart(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	initAndLogin(t, addr)

	spec := writeSleepSpec(t)
	code, out, errb := runP1CLI("--server", addr, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	code, out, errb = runP1CLI("--server", addr, "user", "create", "--user", "view1", "--password", "view1-pass1")
	if code != 0 {
		t.Fatalf("user create exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	uid := parseKV(out, "user_id")
	if uid == "" {
		t.Fatalf("missing user_id in %q", out)
	}
	code, out, errb = runP1CLI("--server", addr, "role", "grant", "--user-id", uid, "--role-id", "viewer")
	if code != 0 {
		t.Fatalf("role grant exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	code, out, errb = runP1CLI("--server", addr, "login", "--user", "view1", "--password", "view1-pass1")
	if code != 0 {
		t.Fatalf("view1 login exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	code, out, errb = runP1CLI("--server", addr, "process", "restart", "sleep")
	if code == 0 {
		t.Fatalf("viewer restart should fail, stdout=%q", out)
	}
	if !strings.Contains(errb, "DENIED") {
		t.Fatalf("viewer restart want DENIED, stdout=%q stderr=%q", out, errb)
	}
}

func TestP4_Case8_RemoveThenRejoinDenied(t *testing.T) {
	addrA, rootA := startClusterAgent(t, "")
	addrB, rootB := startClusterAgent(t, "")
	idA := readNodeID(t, rootA)
	idB := readNodeID(t, rootB)
	joinTwo(t, addrA, addrB)

	rpcA := waitRPCAddr(t, addrA, idA)
	credsB, err := control.LoadAgentCreds(filepath.Join(rootB, "cluster"))
	if err != nil {
		t.Fatalf("load B creds: %v", err)
	}
	clusterID, _, err := control.ParseIDs(credsB.AgentCertPEM)
	if err != nil {
		t.Fatalf("parse B cert: %v", err)
	}

	code, out, errb := runP1CLI("--server", addrA, "node", "remove", idB)
	if code != 0 {
		t.Fatalf("node remove exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	code, out, errb = runP1CLI("--server", addrA, "--node", idB, "process", "restart", "sleep")
	if code == 0 {
		t.Fatalf("restart --node removed must not hop, stdout=%q", out)
	}
	blob := errb + out
	if !strings.Contains(blob, "UNAVAILABLE") {
		t.Fatalf("removed node hop want UNAVAILABLE, stdout=%q stderr=%q", out, errb)
	}

	token := createJoinToken(t, addrA)

	// B 仍在 gossip 时，用同一 node_id、新 boot 再加入必须 DENIED，不能先撞 DUPLICATE_NODE_ID。
	rootB2, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rootB2) })
	if err := os.WriteFile(filepath.Join(rootB2, "node_id"), []byte(idB+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	addrB2 := startClusterAgentAt(t, rootB2, "rejoin-"+idB)
	code, out, errb = runP1CLI("--server", addrB2, "agent", "join", "--seed", addrA, "--token", token)
	assertRejoinDenied(t, code, out, errb)

	// 原 B 清掉 cluster.json 后再 join（新 token）同样必须拒绝。
	token2 := createJoinToken(t, addrA)
	if err := os.Remove(filepath.Join(rootB, "cluster", "cluster.json")); err != nil {
		t.Fatalf("wipe B cluster.json: %v", err)
	}
	code, out, errb = runP1CLI("--server", addrB, "agent", "join", "--seed", addrA, "--token", token2)
	assertRejoinDenied(t, code, out, errb)

	tlsCfg, err := rpc.ClientTLS(credsB, clusterID, idA, nil)
	if err != nil {
		t.Fatalf("client tls: %v", err)
	}
	hc := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg, DisableKeepAlives: true},
	}
	resp, err := hc.Get("https://" + rpcA + "/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("revoked B cert must not dial A RPC %s", rpcA)
	}
	if !strings.Contains(err.Error(), "revoked") &&
		!strings.Contains(err.Error(), "DENIED") &&
		!strings.Contains(err.Error(), "certificate") &&
		!strings.Contains(err.Error(), "handshake") {
		t.Fatalf("revoked dial err=%v rpc=%s", err, rpcA)
	}
}

func TestP4_Case9_ControlDownLocalProcessContinues(t *testing.T) {
	addrA, _, cancelA := startClusterAgentCtl(t, "")
	addrC, _ := startClusterAgent(t, "")
	joinTwo(t, addrA, addrC)

	spec := writeSleepSpec(t)
	code, out, errb := runP1CLI("--server", addrC, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply on C: %d %q %q", code, errb, out)
	}
	code, _, errb = runP1CLI("--server", addrC, "process", "start", "sleep")
	if code != 0 {
		t.Fatalf("start on C: %q", errb)
	}
	waitObserved(t, addrC, "sleep", "RUNNING")
	pid := waitProcessPID(t, addrC, "sleep")

	cancelA()

	deadline := time.Now().Add(8 * time.Second)
	var listOut string
	for time.Now().Before(deadline) {
		code, listOut, errb = runP1CLI("--server", addrC, "process", "list")
		if code == 0 && listHasObserved(listOut, "sleep", "RUNNING") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code != 0 {
		t.Fatalf("process list on C after A down exit=%d stderr=%q stdout=%q", code, errb, listOut)
	}
	if !listHasObserved(listOut, "sleep", "RUNNING") {
		t.Fatalf("C process list want RUNNING after A down: %q", listOut)
	}

	code, out, errb = runP1CLI("--server", addrC, "user", "create", "--user", "afterdown", "--password", "afterdown1")
	if code == 0 {
		t.Fatalf("user create on C must fail after control down, stdout=%q", out)
	}
	blob := errb + out
	if !strings.Contains(blob, "UNAVAILABLE") && !strings.Contains(blob, "control quorum lost") {
		t.Fatalf("user create want UNAVAILABLE or control quorum lost, stdout=%q stderr=%q", out, errb)
	}

	code, listOut, errb = runP1CLI("--server", addrC, "process", "list")
	if code != 0 {
		t.Fatalf("process list after user create fail: %q", errb)
	}
	if !listHasObserved(listOut, "sleep", "RUNNING") {
		t.Fatalf("business process must keep RUNNING: %q", listOut)
	}
	if err := unix.Kill(int(pid), 0); err != nil {
		t.Fatalf("sleep pid %d died after control down: %v", pid, err)
	}
}

func createJoinToken(t *testing.T, addr string) string {
	t.Helper()
	code, tokOut, errb := runP1CLI("--server", addr, "node", "token", "create")
	if code != 0 {
		t.Fatalf("token create exit=%d stderr=%q stdout=%q", code, errb, tokOut)
	}
	token := parseKV(tokOut, "token")
	if token == "" {
		t.Fatalf("missing token in %q", tokOut)
	}
	return token
}

func assertRejoinDenied(t *testing.T, code int, out, errb string) {
	t.Helper()
	if code == 0 {
		t.Fatalf("rejoin must fail, stdout=%q", out)
	}
	blob := errb + out
	if strings.Contains(blob, "DUPLICATE_NODE_ID") {
		t.Fatalf("rejoin must be DENIED not DUPLICATE_NODE_ID: stdout=%q stderr=%q", out, errb)
	}
	if !strings.Contains(blob, "DENIED") &&
		!strings.Contains(blob, "node removed") &&
		!strings.Contains(blob, "certificate revoked") {
		t.Fatalf("want DENIED / node removed / certificate revoked: stdout=%q stderr=%q", out, errb)
	}
}

func waitRPCAddr(t *testing.T, addr, nodeID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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
	t.Fatalf("missing rpc address for %s on %s: %q", nodeID, addr, last)
	return ""
}

func rpcAddrForNode(out, nodeID string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) >= 7 && fields[0] == nodeID && fields[6] != "" {
			return fields[6]
		}
	}
	return ""
}
