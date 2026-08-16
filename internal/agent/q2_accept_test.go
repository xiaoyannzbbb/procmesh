package agent

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQ2_Case7_PartialTimeoutVisible(t *testing.T) {
	addrA, rootA := startClusterAgent(t, "")
	addrC, rootC, cancelC := startClusterAgentCtl(t, "")
	idA := readNodeID(t, rootA)
	idC := readNodeID(t, rootC)
	joinTwo(t, addrA, addrC)

	mustCLI(t, addrA, "process", "apply", "--file", writeSleepSpecNamed(t, "local"), "--expected-revision", "0")
	mustCLI(t, addrC, "process", "apply", "--file", writeSleepSpecNamed(t, "remote"), "--expected-revision", "0")
	mustCLI(t, addrA, "process", "start", "local")
	mustCLI(t, addrC, "process", "start", "remote")
	waitObserved(t, addrA, "local", "RUNNING")
	waitObserved(t, addrC, "remote", "RUNNING")
	waitGossipName(t, addrA, "remote")

	cancelC()

	out := mustCLI(t, addrA, "batch", "create", "--type", "restart",
		"--process-name", idA+":local",
		"--process-name", idC+":remote")
	bid := parseKV(out, "batch_id")
	if bid == "" {
		t.Fatalf("missing batch_id in %q", out)
	}
	waitCLIBatch(t, addrA, bid, "PARTIAL")

	got := mustCLI(t, addrA, "batch", "get", bid)
	if !strings.Contains(got, "timeout=") && !strings.Contains(got, "unavailable=") {
		t.Fatalf("Case 7 must expose timeout/unavailable: %s", got)
	}
	if kvCount(got, "timeout") == 0 && kvCount(got, "unavailable") == 0 {
		t.Fatalf("Case 7 must expose timeout/unavailable: %s", got)
	}
	if strings.Contains(got, "status=COMPLETED") {
		t.Fatalf("must not hide partial: %s", got)
	}
	if !strings.Contains(got, "remote") {
		t.Fatalf("target disappeared: %s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "name=remote") && strings.Contains(line, "status=SUCCESS") {
			t.Fatalf("remote target must not be SUCCESS: %s", got)
		}
	}
}

func TestQ2_ResumeDoesNotReplaySuccess(t *testing.T) {
	addr, root, cancel := startClusterAgentCtl(t, "q2-resume")
	initOut := mustCLI(t, addr, "cluster", "init")
	adminPW := parseKV(initOut, "admin_password")
	if adminPW == "" {
		t.Fatalf("missing admin_password in %q", initOut)
	}
	loginAdmin(t, addr, adminPW)

	mustCLI(t, addr, "process", "apply", "--file", writeSleepSpecNamed(t, "sleep"), "--expected-revision", "0")
	mustCLI(t, addr, "process", "start", "sleep")
	waitObserved(t, addr, "sleep", "RUNNING")

	out := mustCLI(t, addr, "batch", "create", "--type", "restart",
		"--process-name", currentNodeID(t, addr)+":sleep")
	bid := parseKV(out, "batch_id")
	if bid == "" {
		t.Fatalf("missing batch_id in %q", out)
	}
	waitCLIBatch(t, addr, bid, "COMPLETED")
	pid := waitProcessPID(t, addr, "sleep")

	cancel()

	addr2 := startClusterAgentAt(t, root, "q2-resume")
	if !waitProcessReady(t, addr2, "sleep") {
		waitClusterInited(t, addr2)
		loginAdmin(t, addr2, adminPW)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		inst, ok := getProcessInstance(t, addr2, "sleep")
		if ok && inst.GetObserved() == "RUNNING" && inst.GetPid() > 0 && inst.GetPid() != pid {
			t.Fatalf("Resume replayed SUCCESS: pid %d -> %d", pid, inst.GetPid())
		}
		time.Sleep(50 * time.Millisecond)
	}
	gotPID := waitProcessPID(t, addr2, "sleep")
	if gotPID != pid {
		t.Fatalf("Resume replayed SUCCESS: pid %d -> %d", pid, gotPID)
	}
	got := mustCLI(t, addr2, "batch", "get", bid)
	if !strings.Contains(got, "status=COMPLETED") {
		t.Fatalf("batch after resume want COMPLETED: %s", got)
	}
}

func waitProcessReady(t *testing.T, addr, name string) bool {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		inst, ok := getProcessInstance(t, addr, name)
		if ok && inst.GetObserved() == "RUNNING" && inst.GetPid() > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func waitCLIBatch(t *testing.T, addr, batchID, status string) string {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var code int
	var out, errb string
	for time.Now().Before(deadline) {
		code, out, errb = runP1CLI("--server", addr, "batch", "get", batchID)
		if code == 0 && strings.Contains(out, "status="+status) {
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wait batch %s %s: exit=%d stdout=%q stderr=%q", batchID, status, code, out, errb)
	return out
}

func kvCount(out, key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(parseKV(out, key)))
	if err != nil {
		return 0
	}
	return n
}
