package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestP3_RestartOnOwnerFromEntry(t *testing.T) {
	addrA, _ := startClusterAgent(t, "")
	addrC, rootC := startClusterAgent(t, "")
	idC := readNodeID(t, rootC)
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

	// Gossip 把 C 的 process 摘要传到 A
	waitGossipName(t, addrA, "sleep")

	code, _, errb = runP1CLI("--server", addrA, "--node", idC, "process", "restart", "sleep")
	if code != 0 {
		t.Fatalf("restart via A: %q", errb)
	}

	// A 本机不得出现 sleep
	code, listA, errb := runP1CLI("--server", addrA, "process", "list")
	if code != 0 {
		t.Fatal(errb)
	}
	if strings.Contains(listA, "sleep") {
		t.Fatalf("entry must not own sleep: %q", listA)
	}

	code, listC, errb := runP1CLI("--server", addrC, "process", "list")
	if code != 0 {
		t.Fatal(errb)
	}
	if !strings.Contains(listC, "sleep") {
		t.Fatalf("owner lost sleep: %q", listC)
	}
}

func TestP3_SameOperationIDDoesNotReplay(t *testing.T) {
	addrA, _ := startClusterAgent(t, "")
	addrC, rootC := startClusterAgent(t, "")
	idC := readNodeID(t, rootC)
	joinTwo(t, addrA, addrC)
	spec := writeSleepSpec(t)
	if code, _, errb := runP1CLI("--server", addrC, "process", "apply", "--file", spec, "--expected-revision", "0"); code != 0 {
		t.Fatal(errb)
	}
	if code, _, errb := runP1CLI("--server", addrC, "process", "start", "sleep"); code != 0 {
		t.Fatal(errb)
	}
	waitObserved(t, addrC, "sleep", "RUNNING")
	waitGossipName(t, addrA, "sleep")
	beforePID := waitProcessPID(t, addrC, "sleep")

	op := "op-p3-restart-once"
	if code, _, errb := runP1CLI("--server", addrA, "--node", idC, "--operation-id", op, "process", "restart", "sleep"); code != 0 {
		t.Fatal(errb)
	}
	waitObserved(t, addrC, "sleep", "RUNNING")
	firstPID := waitProcessPID(t, addrC, "sleep")
	if firstPID == beforePID {
		t.Fatalf("first restart did not replace pid %d", beforePID)
	}
	firstCount := restartCount(t, addrC, "sleep")
	if code, _, errb := runP1CLI("--server", addrA, "--node", idC, "--operation-id", op, "restart", "sleep"); code != 0 {
		t.Fatal(errb)
	}
	waitObserved(t, addrC, "sleep", "RUNNING")
	secondPID := waitProcessPID(t, addrC, "sleep")
	if secondPID != firstPID {
		t.Fatalf("replayed restart: pid first=%d second=%d", firstPID, secondPID)
	}
	secondCount := restartCount(t, addrC, "sleep")
	if secondCount != firstCount {
		t.Fatalf("replayed restart: restart_count first=%d second=%d", firstCount, secondCount)
	}
}

func TestP3_FailedOwnerDoesNotMigrate(t *testing.T) {
	addrA, _ := startClusterAgent(t, "")
	addrC, rootC, cancelC := startClusterAgentCtl(t, "")
	idC := readNodeID(t, rootC)
	joinTwo(t, addrA, addrC)
	spec := writeSleepSpec(t)
	if code, _, errb := runP1CLI("--server", addrC, "process", "apply", "--file", spec, "--expected-revision", "0"); code != 0 {
		t.Fatal(errb)
	}
	waitGossipName(t, addrA, "sleep")
	cancelC() // C 下线，A 侧应变 FAILED 或至少 RPC 不可达

	code, _, errb := runP1CLI("--server", addrA, "--node", idC, "process", "restart", "sleep")
	if code == 0 {
		t.Fatal("restart must fail when owner is down")
	}
	if !strings.Contains(errb, "UNAVAILABLE") && !strings.Contains(errb, "TIMEOUT") {
		t.Fatalf("want UNAVAILABLE or TIMEOUT, got %q", errb)
	}
	code, listA, errb := runP1CLI("--server", addrA, "process", "list")
	if code != 0 {
		t.Fatal(errb)
	}
	if strings.Contains(listA, "sleep") {
		t.Fatalf("must not migrate process to entry: %q", listA)
	}
}

func waitObserved(t *testing.T, addr, name, observed string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var listOut, errb string
	for time.Now().Before(deadline) {
		var code int
		code, listOut, errb = runP1CLI("--server", addr, "process", "list")
		if code == 0 && listHasObserved(listOut, name, observed) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wait observed %s %s: list=%q stderr=%q", name, observed, listOut, errb)
}

func listHasObserved(out, name, observed string) bool {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			continue
		}
		if fields[0] == name && fields[3] == observed {
			return true
		}
	}
	return false
}

func waitGossipName(t *testing.T, addr, name string) {
	t.Helper()
	hc := &http.Client{Timeout: 5 * time.Second}
	cli := procmeshv1connect.NewNodeServiceClient(hc, "http://"+addr)
	deadline := time.Now().Add(8 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := cli.ListNodes(context.Background(), connect.NewRequest(&procmeshv1.ListNodesRequest{}))
		if err == nil {
			var names []string
			for _, n := range resp.Msg.GetNodes() {
				for _, p := range n.GetProcesses() {
					if p.GetName() == name {
						return
					}
					if p.GetName() != "" {
						names = append(names, n.GetNodeId()+":"+p.GetName())
					}
				}
			}
			last = strings.Join(names, ",")
		} else {
			last = err.Error()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("gossip never showed process %q on %s (last=%q)", name, addr, last)
}

func waitProcessPID(t *testing.T, addr, name string) int32 {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var lastObs string
	var lastPID int32
	for time.Now().Before(deadline) {
		inst, ok := getProcessInstance(t, addr, name)
		if ok && inst.GetObserved() == "RUNNING" && inst.GetPid() > 0 {
			return inst.GetPid()
		}
		if ok {
			lastObs, lastPID = inst.GetObserved(), inst.GetPid()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wait pid %s: observed=%s pid=%d", name, lastObs, lastPID)
	return 0
}

func restartCount(t *testing.T, addr, name string) int32 {
	t.Helper()
	inst, ok := getProcessInstance(t, addr, name)
	if !ok {
		t.Fatalf("GetProcess %q: no instances", name)
	}
	return inst.GetRestartCount()
}

func getProcessInstance(t *testing.T, addr, name string) (*procmeshv1.Instance, bool) {
	t.Helper()
	hc := &http.Client{Timeout: 5 * time.Second}
	cli := procmeshv1connect.NewProcessServiceClient(hc, "http://"+addr)
	resp, err := cli.GetProcess(context.Background(), connect.NewRequest(&procmeshv1.GetProcessRequest{IdOrName: name}))
	if err != nil {
		return nil, false
	}
	insts := resp.Msg.GetProcess().GetInstances()
	if len(insts) == 0 {
		return nil, false
	}
	return insts[0], true
}
