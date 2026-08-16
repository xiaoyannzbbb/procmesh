package agent

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
	"golang.org/x/sys/unix"
)

func TestP5_Playwright_LoginListFreshness409(t *testing.T) {
	if _, err := exec.LookPath("npx"); err != nil {
		t.Fatalf("npx required for P5 playwright")
	}

	addr, _ := startClusterAgent(t, "")
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	pw := parseKV(out, "admin_password")
	if pw == "" {
		t.Fatalf("missing admin_password in %q", out)
	}

	// 等待集群初始化完成（认证拦截器启用）
	waitClusterInited(t, addr)

	loginAdmin(t, addr, pw)

	spec := writeSleepSpec(t)
	code, out, errb = runP1CLI("--server", addr, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	waitGossipName(t, addr, "sleep")

	t.Setenv("PROCMESH_E2E_URL", "http://"+addr)
	t.Setenv("PROCMESH_E2E_USER", "admin")
	t.Setenv("PROCMESH_E2E_PASSWORD", pw)

	dir := playwrightWebDir(t)
	outb, err := runPlaywright(dir)
	if err != nil && playwrightMissingBrowser(outb) {
		install := exec.Command("npx", "playwright", "install", "chromium")
		install.Dir = dir
		if ib, ierr := install.CombinedOutput(); ierr != nil {
			t.Fatalf("npx playwright install chromium: %v\n%s", ierr, ib)
		}
		outb, err = runPlaywright(dir)
	}
	if err != nil {
		t.Fatalf("playwright test: %v\n%s", err, outb)
	}
}

func TestP5_Case1_WebAgentCrash(t *testing.T) {
	addrA, _, stopA := startClusterAgentCtl(t, "")
	addrB, _ := startClusterAgent(t, "")
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

	deadline := time.Now().Add(8 * time.Second)
	var listOut string
	for time.Now().Before(deadline) {
		code, listOut, errb = runP1CLI("--server", addrB, "process", "list")
		if code != 0 {
			t.Fatalf("process list on B after A crash exit=%d stderr=%q stdout=%q", code, errb, listOut)
		}
		if strings.Contains(listOut, "sleep") {
			t.Fatalf("B must not create A's process locally: %q", listOut)
		}
		time.Sleep(50 * time.Millisecond)
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

func playwrightWebDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "web"))
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		t.Fatalf("web dir %s: %v", dir, err)
	}
	return dir
}

func runPlaywright(dir string) ([]byte, error) {
	cmd := exec.Command("npx", "playwright", "test")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

func playwrightMissingBrowser(out []byte) bool {
	return bytes.Contains(out, []byte("Executable doesn't exist")) ||
		bytes.Contains(out, []byte("browserType.launch"))
}
