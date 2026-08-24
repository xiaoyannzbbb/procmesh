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
	"golang.org/x/sys/unix"
)

func TestP5_Case1_WebAgentCrash(t *testing.T) {
	addrA, rootA, stopA := startClusterAgentCtl(t, "")
	triggers := newManualRunTriggers()
	rootB := t.TempDir()
	addrB, _ := startClusterAgentAtOpts(t, rootB, Options{triggers: triggers.hooks})
	joinTwo(t, addrA, addrB)

	spec := writeSleepSpec(t)
	code, out, errb := runP1CLI("--server", addrA, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply on A: %d %q %q", code, errb, out)
	}
	code, _, errb = runP1CLI("--server", addrA, "process", "start", "sleep")
	if code != 0 {
		t.Fatalf("start on A: %q", errb)
	}
	waitObserved(t, addrA, "sleep", "RUNNING")
	pid := waitProcessPID(t, addrA, "sleep")
	waitGossipName(t, addrB, "sleep")

	stopA()

	if err := unix.Kill(int(pid), 0); err != nil {
		t.Fatalf("sleep pid %d died after A crash: %v", pid, err)
	}

	waitNodeState(t, addrB, readNodeID(t, rootA), "LEFT", 10*time.Second)
	for range 3 {
		triggers.triggerAgentLoop(t)
	}
	code, listOut, errb := runP1CLI("--server", addrB, "process", "list")
	if code != 0 {
		t.Fatalf("process list on B after A crash exit=%d stderr=%q stdout=%q", code, errb, listOut)
	}
	if strings.Contains(listOut, "sleep") {
		t.Fatalf("B must not create A's process locally: %q", listOut)
	}

	hc := &http.Client{Timeout: 5 * time.Second}
	nodes := procmeshv1connect.NewNodeServiceClient(hc, "http://"+addrB, testConnectOpts()...)
	if _, err := nodes.ListNodes(context.Background(), connect.NewRequest(&procmeshv1.ListNodesRequest{})); err != nil {
		t.Fatalf("ListNodes on B after A crash: %v", err)
	}
	overview := procmeshv1connect.NewClusterServiceClient(hc, "http://"+addrB, testConnectOpts()...)
	if _, err := overview.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{})); err != nil {
		t.Fatalf("Overview on B after A crash: %v", err)
	}

	resp, err := http.Get("http://" + addrB + "/")
	if err != nil {
		t.Fatalf("GET / on B: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / on B want 200 got %d", resp.StatusCode)
	}

	if err := unix.Kill(int(pid), 0); err != nil {
		t.Fatalf("sleep pid %d died after B checks: %v", pid, err)
	}
}
