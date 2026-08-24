package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestAccept_RPCAddressAfterInit(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	initAndLogin(t, addr)
	deadline := time.Now().Add(3 * time.Second)
	var code int
	var out, errb string
	for time.Now().Before(deadline) {
		code, out, errb = runP1CLI("--server", addr, "node", "list")
		if code == 0 && strings.Contains(out, "127.0.0.1:") && strings.Count(out, "127.0.0.1:") >= 1 {
			// node list 已打印 rpc 或至少成员仍在；改为解析 rpc 列/字段
			if rpcAddrFromNodeList(out) != "" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("missing rpc_address after init: %q stderr=%q", out, errb)
}

func TestAccept_NodeListAfterInit(t *testing.T) {
	addr, root := startClusterAgent(t, "")
	nodeID := readNodeID(t, root)

	initAndLogin(t, addr)

	code, out, errb := runP1CLI("--server", addr, "node", "list")
	if code != 0 {
		t.Fatalf("node list exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	if !strings.Contains(out, nodeID) {
		t.Fatalf("node list missing local node_id %q: %q", nodeID, out)
	}
}

func TestAccept_JoinTwoAgents(t *testing.T) {
	addrA, rootA := startClusterAgent(t, "")
	addrB, rootB := startClusterAgent(t, "")
	idA := readNodeID(t, rootA)
	idB := readNodeID(t, rootB)
	if idA == idB {
		t.Fatalf("agents unexpectedly share node_id %q", idA)
	}
	joinTwo(t, addrA, addrB)
	_, listA, errb := runP1CLI("--server", addrA, "node", "list")
	_, listB, errbB := runP1CLI("--server", addrB, "node", "list")
	if !containsAll(parseNodeIDs(listA), idA, idB) || !containsAll(parseNodeIDs(listB), idA, idB) {
		t.Fatalf("want both ids on both agents; A=%q (%q) B=%q (%q)", listA, errb, listB, errbB)
	}
}

func joinTwo(t *testing.T, addrA, addrC string) {
	t.Helper()
	_ = joinTwoAndPassword(t, addrA, addrC)
}

func joinTwoAndPassword(t *testing.T, addrA, addrC string) string {
	t.Helper()
	code, initOut, errb := runP1CLI("--server", addrA, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, initOut)
	}
	password := parseKV(initOut, "admin_password")
	if password == "" {
		t.Fatalf("missing admin_password in %q", initOut)
	}
	loginAdmin(t, addrA, password)
	code, tokOut, errb := runP1CLI("--server", addrA, "node", "token", "create")
	if code != 0 {
		t.Fatalf("token create exit=%d stderr=%q stdout=%q", code, errb, tokOut)
	}
	token := parseKV(tokOut, "token")
	if token == "" {
		t.Fatalf("missing token in %q", tokOut)
	}

	code, out, errb := runP1CLI("--server", addrC, "agent", "join", "--seed", addrA, "--token", token)
	if code != 0 {
		t.Fatalf("agent join exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	deadline := time.Now().Add(15 * time.Second)
	var listA, listC string
	for time.Now().Before(deadline) {
		code, listA, errb = runP1CLI("--server", addrA, "node", "list")
		if code != 0 {
			t.Fatalf("node list A exit=%d stderr=%q", code, errb)
		}
		code, listC, errb = runP1CLI("--server", addrC, "node", "list")
		if code != 0 {
			t.Fatalf("node list C exit=%d stderr=%q", code, errb)
		}
		idsA := parseNodeIDs(listA)
		idsC := parseNodeIDs(listC)
		if len(idsA) == 2 && len(idsC) == 2 && distinctIDs(idsA) && distinctIDs(idsC) {
			return password
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("want 2 distinct members on both agents; A=%q C=%q", listA, listC)
	return ""
}

func TestAccept_DuplicateNodeIDRejected(t *testing.T) {
	addrA, rootA := startClusterAgent(t, "")
	initAndLogin(t, addrA)

	spec := writeNeverSpec(t)
	var code int
	var out, errb string
	code, out, errb = runP1CLI("--server", addrA, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply never exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	nodeID := readNodeID(t, rootA)
	rootC, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rootC) })
	if err := os.WriteFile(filepath.Join(rootC, "node_id"), []byte(nodeID+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	addrC := startClusterAgentAt(t, rootC, "clone-boot-1")

	code, tokOut, errb := runP1CLI("--server", addrA, "node", "token", "create")
	if code != 0 {
		t.Fatalf("token create exit=%d stderr=%q stdout=%q", code, errb, tokOut)
	}
	token := parseKV(tokOut, "token")
	if token == "" {
		t.Fatalf("missing token in %q", tokOut)
	}

	code, out, errb = runP1CLI("--server", addrC, "agent", "join", "--seed", addrA, "--token", token)
	if code == 0 {
		t.Fatalf("duplicate join should fail, stdout=%q", out)
	}
	if !strings.Contains(errb, "DUPLICATE_NODE_ID") {
		t.Fatalf("want DUPLICATE_NODE_ID, stdout=%q stderr=%q", out, errb)
	}

	code, listOut, errb := runP1CLI("--server", addrA, "process", "list")
	if code != 0 {
		t.Fatalf("process list exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(listOut, "never") {
		t.Fatalf("seed process plane lost never spec: %q", listOut)
	}
}

func startClusterAgent(t *testing.T, bootID string) (addr, root string) {
	addr, root, _ = startClusterAgentCtl(t, bootID)
	return addr, root
}

func startClusterAgentCtl(t *testing.T, bootID string) (addr, root string, cancel context.CancelFunc) {
	return startClusterAgentOptsCtl(t, Options{BootID: bootID})
}

func startClusterAgentOptsCtl(t *testing.T, extra Options) (addr, root string, cancel context.CancelFunc) {
	t.Helper()
	root, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	addr, cancel = startClusterAgentAtOpts(t, root, extra)
	return addr, root, cancel
}

func startClusterAgentAt(t *testing.T, root, bootID string) string {
	addr, _ := startClusterAgentAtCtl(t, root, bootID)
	return addr
}

func startClusterAgentAtCtl(t *testing.T, root, bootID string) (string, context.CancelFunc) {
	return startClusterAgentAtOpts(t, root, Options{BootID: bootID})
}

func startClusterAgentAtOpts(t *testing.T, root string, extra Options) (string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	got := make(chan string, 1)
	errCh := make(chan error, 1)
	rpcListen, gossipListen, controlListen := extra.RPCListen, extra.GossipListen, extra.ControlListen
	if rpcListen == "" {
		rpcListen = "127.0.0.1:0"
	}
	if gossipListen == "" {
		gossipListen = "127.0.0.1:0"
	}
	if controlListen == "" {
		controlListen = "127.0.0.1:0"
	}
	go func() {
		ensureTestSession(t)
		errCh <- Run(ctx, Options{
			DataDir:            root,
			ConfigPath:         extra.ConfigPath,
			Listen:             "127.0.0.1:0",
			GossipListen:       gossipListen,
			RPCListen:          rpcListen,
			ControlListen:      controlListen,
			RPCAdvertise:       extra.RPCAdvertise,
			ControlAdvertise:   extra.ControlAdvertise,
			BreakGlassSocket:   extra.BreakGlassSocket,
			BreakGlassGroup:    extra.BreakGlassGroup,
			ShimBin:            testShimBin,
			BootID:             extra.BootID,
			OnListen:           func(addr string) { got <- addr },
			OnRPCListen:        extra.OnRPCListen,
			OnControlListen:    extra.OnControlListen,
			OnBreakGlassListen: extra.OnBreakGlassListen,
			DiskPercent:        extra.DiskPercent,
			Backup:             extra.Backup,
			Now:                extra.Now,
			triggers:           extra.triggers,
		})
	}()
	var addr string
	select {
	case addr = <-got:
	case err := <-errCh:
		t.Fatalf("agent run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("agent listen timeout")
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-errCh:
			case <-time.After(5 * time.Second):
			}
		})
	}
	t.Cleanup(func() {
		stop()
		cleanupDataDir(root)
	})
	return addr, stop
}

func ensureTestSession(t *testing.T) {
	t.Helper()
	if os.Getenv("PROCMESH_SESSION") != "" {
		return
	}
	t.Setenv("PROCMESH_SESSION", filepath.Join(t.TempDir(), "session"))
}

func waitClusterInited(t *testing.T, addr string) {
	t.Helper()
	hc := &http.Client{Timeout: 5 * time.Second}
	cli := procmeshv1connect.NewAuthServiceClient(hc, "http://"+addr, testConnectOpts()...)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		_, err := cli.GetMe(context.Background(), connect.NewRequest(&procmeshv1.GetMeRequest{}))
		// 集群未初始化时，认证拦截器会放行请求（err == nil 或其他错误）
		// 集群已初始化时，未认证请求会返回 DENIED 错误
		if err != nil && connect.CodeOf(err) == connect.CodePermissionDenied {
			// 认证拦截器已启用，集群已初始化
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("cluster never inited on %s (auth interceptor not enabled)", addr)
}

func loginAdmin(t *testing.T, server, password string) {
	t.Helper()
	ensureTestSession(t)
	code, out, errb := runP1CLI("--server", server, "login", "--user", "admin", "--password", password)
	if code != 0 {
		t.Fatalf("login exit=%d stderr=%q stdout=%q", code, errb, out)
	}
}

func initAndLogin(t *testing.T, addr string) {
	t.Helper()
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	pw := parseKV(out, "admin_password")
	if pw == "" {
		t.Fatalf("missing admin_password in %q", out)
	}
	loginAdmin(t, addr, pw)
}

func sessionBearer() string {
	path := os.Getenv("PROCMESH_SESSION")
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var sess struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &sess) != nil {
		return ""
	}
	return sess.SessionID
}

func readNodeID(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "node_id"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		t.Fatal("empty node_id")
	}
	return id
}

func writeNeverSpec(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "never.yaml")
	body := "name: never\ncommand: /bin/true\ninstances: 1\nrestart:\n  mode: never\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseKV(out, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func rpcAddrFromNodeList(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) >= 7 && fields[6] != "" {
			return fields[6]
		}
	}
	return ""
}

func parseNodeIDs(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if fields[0] != "" {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

func waitNodeState(t *testing.T, addr, nodeID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var out, errb string
	for time.Now().Before(deadline) {
		code := 0
		code, out, errb = runP1CLI("--server", addr, "node", "list")
		if code == 0 {
			for _, line := range strings.Split(out, "\n") {
				fields := strings.Split(strings.TrimSpace(line), "\t")
				if len(fields) >= 3 && fields[0] == nodeID && fields[2] == want {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("node %s did not reach %s: stdout=%q stderr=%q", nodeID, want, out, errb)
}

func distinctIDs(ids []string) bool {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func containsAll(ids []string, want ...string) bool {
	have := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		have[id] = struct{}{}
	}
	for _, w := range want {
		if _, ok := have[w]; !ok {
			return false
		}
	}
	return true
}
