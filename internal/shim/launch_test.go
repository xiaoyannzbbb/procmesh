package shim_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/shim"
	"golang.org/x/sys/unix"
)

var testShimBin string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "shim-bin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tempdir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "procmesh-shim")
	// tests that need the binary build ./cmd/procmesh-shim in TestMain
	// and pass that path to Launch (prefer this over Skip so CI is deterministic)
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/procmesh-shim")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build shim: %v\n%s", err, out)
		return 1
	}
	testShimBin = bin
	code := m.Run()
	reapTestShims(testShimBin)
	return code
}

func reapTestShims(bin string) {
	if bin == "" {
		return
	}
	out, err := exec.Command("pgrep", "-f", bin).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		if kids, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output(); err == nil {
			for _, kline := range strings.Split(string(kids), "\n") {
				kpid, kerr := strconv.Atoi(strings.TrimSpace(kline))
				if kerr != nil || kpid <= 1 {
					continue
				}
				_ = unix.Kill(kpid, unix.SIGKILL)
			}
		}
		_ = unix.Kill(pid, unix.SIGKILL)
	}
}

func TestLaunch_ThenReconnectAfterClientDrop(t *testing.T) {
	dir := shortTemp(t)
	sock := filepath.Join(dir, "i1.sock")
	pid, err := shim.Launch(context.Background(), testShimBin, sock, "p:1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killPID(pid) })
	if pid <= 0 {
		t.Fatalf("pid=%d", pid)
	}
	if sid, err := unix.Getsid(pid); err != nil || sid != pid {
		t.Fatalf("want session leader pid=%d sid=%d err=%v", pid, sid, err)
	}
	if _, err := os.Stat(sock + ".shim.log"); err != nil {
		t.Fatalf("shim log: %v", err)
	}
	c1, st, err := shim.Reconnect(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = st
	c1.Close()
	c2, _, err := shim.Reconnect(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	c2.Close()
}

func TestDiscover_MapsSanitizedInstanceID(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "abc_0.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := shim.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if found["abc_0"] != sock {
		t.Fatalf("got %#v", found)
	}
	if _, ok := found["ignore"]; ok {
		t.Fatalf("non-sock mapped: %#v", found)
	}
}

func TestLookPath_PATHThenSibling(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "procmesh-shim")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := shim.LookPath()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		resolved = got
	}
	want, err := filepath.EvalSymlinks(bin)
	if err != nil {
		want = bin
	}
	if resolved != want {
		t.Fatalf("PATH hit got %q want %q", got, bin)
	}

	empty := t.TempDir()
	t.Setenv("PATH", empty)
	agentDir := t.TempDir()
	sibling := filepath.Join(agentDir, "procmesh-shim")
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldArgs0 := os.Args[0]
	os.Args[0] = filepath.Join(agentDir, "procmesh-agent")
	defer func() { os.Args[0] = oldArgs0 }()
	got, err = shim.LookPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != sibling {
		t.Fatalf("sibling hit got %q want %q", got, sibling)
	}

	os.Args[0] = filepath.Join(t.TempDir(), "procmesh-agent")
	if _, err := shim.LookPath(); err == nil {
		t.Fatal("expected missing binary error")
	}
}

func TestLaunch_RejectsEmptyArgsAndCanceled(t *testing.T) {
	ctx := context.Background()
	if _, err := shim.Launch(ctx, "", "s", "i"); err == nil {
		t.Fatal("empty bin")
	}
	if _, err := shim.Launch(ctx, testShimBin, "", "i"); err == nil {
		t.Fatal("empty socket")
	}
	if _, err := shim.Launch(ctx, testShimBin, "s", ""); err == nil {
		t.Fatal("empty instance")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := shim.Launch(canceled, testShimBin, filepath.Join(t.TempDir(), "s.sock"), "i"); err == nil {
		t.Fatal("canceled ctx")
	}
}

func TestDiscover_MissingDir(t *testing.T) {
	_, err := shim.Discover(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestClient_CloseNil(t *testing.T) {
	var c *shim.Client
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (&shim.Client{}).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReconnect_MissingSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := shim.Reconnect(ctx, filepath.Join(t.TempDir(), "no.sock"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func killPID(pid int) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = p.Signal(syscall.SIGKILL)
}
