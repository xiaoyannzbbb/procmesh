package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAccept_RPCAddressAfterInit(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	deadline := time.Now().Add(3 * time.Second)
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

	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	code, out, errb = runP1CLI("--server", addr, "node", "list")
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
	code, out, errb := runP1CLI("--server", addrA, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	code, tokOut, errb := runP1CLI("--server", addrA, "node", "token", "create")
	if code != 0 {
		t.Fatalf("token create exit=%d stderr=%q stdout=%q", code, errb, tokOut)
	}
	token := parseKV(tokOut, "token")
	if token == "" {
		t.Fatalf("missing token in %q", tokOut)
	}

	code, out, errb = runP1CLI("--server", addrC, "agent", "join", "--seed", addrA, "--token", token)
	if code != 0 {
		t.Fatalf("agent join exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	deadline := time.Now().Add(5 * time.Second)
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
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("want 2 distinct members on both agents; A=%q C=%q", listA, listC)
}

func TestAccept_DuplicateNodeIDRejected(t *testing.T) {
	addrA, rootA := startClusterAgent(t, "")
	code, out, errb := runP1CLI("--server", addrA, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	spec := writeNeverSpec(t)
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
	t.Helper()
	root, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	addr, cancel = startClusterAgentAtCtl(t, root, bootID)
	return addr, root, cancel
}

func startClusterAgentAt(t *testing.T, root, bootID string) string {
	addr, _ := startClusterAgentAtCtl(t, root, bootID)
	return addr
}

func startClusterAgentAtCtl(t *testing.T, root, bootID string) (string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	got := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:      root,
			Listen:       "127.0.0.1:0",
			GossipListen: "127.0.0.1:0",
			RPCListen:    "127.0.0.1:0",
			ShimBin:      testShimBin,
			BootID:       bootID,
			OnListen:     func(addr string) { got <- addr },
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
