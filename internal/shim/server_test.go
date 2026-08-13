package shim_test

import (
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/shim"
	shimpb "github.com/qleelulu/procmesh/proto/shim/v1"
)

func TestServe_StartAndStopTrue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := shortTemp(t)
	sock := filepath.Join(dir, "s.sock")
	errCh := make(chan error, 1)
	go func() { errCh <- shim.Serve(ctx, sock) }()
	waitSock(t, sock)
	c, err := shim.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/sleep", Args: []string{"30"}, StdoutPath: out, StderrPath: out})
	if err != nil || resp.GetPid() <= 0 {
		t.Fatalf("%v %+v", err, resp)
	}
	st, err := c.Status(ctx)
	if err != nil || !st.GetAlive() {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err := c.Stop(ctx, &shimpb.StopRequest{Signal: "SIGTERM", TimeoutMs: 2000, KillSignal: "SIGKILL"}); err != nil {
		t.Fatal(err)
	}
}

func TestServe_AlreadyStarted(t *testing.T) {
	ctx, c, dir := serveClient(t)
	out := filepath.Join(dir, "out.log")
	first, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/sleep", Args: []string{"30"}, StdoutPath: out, StderrPath: out})
	if err != nil || first.GetPid() <= 0 || first.GetError() != "" {
		t.Fatalf("%v %+v", err, first)
	}
	second, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/sleep", Args: []string{"30"}, StdoutPath: out, StderrPath: out})
	if err != nil {
		t.Fatal(err)
	}
	if second.GetError() != "already started" {
		t.Fatalf("got %+v", second)
	}
	if _, err := c.Stop(ctx, &shimpb.StopRequest{Signal: "SIGTERM", TimeoutMs: 2000, KillSignal: "SIGKILL"}); err != nil {
		t.Fatal(err)
	}
}

func TestServe_UnknownUser(t *testing.T) {
	ctx, c, dir := serveClient(t)
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{
		Command:    "/bin/sleep",
		Args:       []string{"30"},
		RunAsUser:  "procmesh-no-such-user-xyz",
		StdoutPath: out,
		StderrPath: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetError() == "" || resp.GetPid() > 0 {
		t.Fatalf("want user error, got %+v", resp)
	}
	st, err := c.Status(ctx)
	if err != nil || st.GetAlive() {
		t.Fatalf("process started despite user lookup failure: %v %+v", err, st)
	}
}

func TestServe_WaitAndRestart(t *testing.T) {
	ctx, c, dir := serveClient(t)
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/echo", Args: []string{"hello-shim"}, StdoutPath: out, StderrPath: out})
	if err != nil || resp.GetPid() <= 0 {
		t.Fatalf("%v %+v", err, resp)
	}
	wr, err := c.Wait(ctx)
	if err != nil || wr.GetError() != "" || wr.GetExitCode() != 0 {
		t.Fatalf("%v %+v", err, wr)
	}
	st, err := c.Status(ctx)
	if err != nil || st.GetAlive() {
		t.Fatalf("still alive after wait: %v %+v", err, st)
	}
	body, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(body), "hello-shim") {
		t.Fatalf("stdout %q err %v", body, err)
	}
	again, err := c.Start(ctx, &shimpb.StartRequest{Command: "/usr/bin/true", StdoutPath: out, StderrPath: out})
	if err != nil || again.GetPid() <= 0 || again.GetError() != "" {
		t.Fatalf("restart after exit: %v %+v", err, again)
	}
	if _, err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestServe_SignalAndReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	dir := shortTemp(t)
	sock := filepath.Join(dir, "s.sock")
	errCh := make(chan error, 1)
	go func() { errCh <- shim.Serve(ctx, sock) }()
	waitSock(t, sock)

	c, err := shim.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/sleep", Args: []string{"30"}, StdoutPath: out, StderrPath: out})
	if err != nil || resp.GetPid() <= 0 {
		c.Close()
		t.Fatalf("%v %+v", err, resp)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2, err := shim.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	st, err := c2.Status(ctx)
	if err != nil || !st.GetAlive() || st.GetPid() != resp.GetPid() {
		t.Fatalf("reconnect status: %v %+v", err, st)
	}
	sig, err := c2.Signal(ctx, &shimpb.SignalRequest{Signal: "SIGTERM"})
	if err != nil || sig.GetError() != "" {
		t.Fatalf("%v %+v", err, sig)
	}
	wr, err := c2.Wait(ctx)
	if err != nil || wr.GetError() != "" {
		t.Fatalf("wait after signal: %v %+v", err, wr)
	}
}

func TestServe_StopNoChild(t *testing.T) {
	ctx, c, _ := serveClient(t)
	st, err := c.Status(ctx)
	if err != nil || st.GetAlive() {
		t.Fatalf("%v %+v", err, st)
	}
	stop, err := c.Stop(ctx, &shimpb.StopRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if stop.GetError() == "" {
		t.Fatalf("want error, got %+v", stop)
	}
}

func TestServe_ChildGoneAfterStop(t *testing.T) {
	ctx, c, dir := serveClient(t)
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/sleep", Args: []string{"30"}, StdoutPath: out, StderrPath: out})
	if err != nil || resp.GetPid() <= 0 {
		t.Fatalf("%v %+v", err, resp)
	}
	if _, err := c.Stop(ctx, &shimpb.StopRequest{Signal: "SIGKILL", TimeoutMs: 1000, KillSignal: "SIGKILL"}); err != nil {
		t.Fatal(err)
	}
	p, err := os.FindProcess(int(resp.GetPid()))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(syscall.Signal(0)); err == nil {
		t.Fatal("child still alive after stop")
	}
}

func TestServe_SignalInvalid(t *testing.T) {
	ctx, c, dir := serveClient(t)
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/sleep", Args: []string{"30"}, StdoutPath: out, StderrPath: out})
	if err != nil || resp.GetPid() <= 0 {
		t.Fatalf("%v %+v", err, resp)
	}
	sig, err := c.Signal(ctx, &shimpb.SignalRequest{Signal: "NOTASIG"})
	if err != nil {
		t.Fatal(err)
	}
	if sig.GetError() == "" {
		t.Fatalf("want error, got %+v", sig)
	}
	if _, err := c.Stop(ctx, &shimpb.StopRequest{Signal: "SIGKILL", TimeoutMs: 1000, KillSignal: "SIGKILL"}); err != nil {
		t.Fatal(err)
	}
}

func TestServe_WaitNoChild(t *testing.T) {
	ctx, c, _ := serveClient(t)
	wr, err := c.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if wr.GetError() == "" {
		t.Fatalf("want error, got %+v", wr)
	}
}

func TestServe_EnvCwdAndSplitLogs(t *testing.T) {
	ctx, c, dir := serveClient(t)
	out := filepath.Join(dir, "out.log")
	errp := filepath.Join(dir, "err.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{
		Command:    "/bin/echo",
		Args:       []string{"env-ok"},
		Env:        map[string]string{"PROCMESH_SHIM": "1"},
		Cwd:        dir,
		StdoutPath: out,
		StderrPath: errp,
	})
	if err != nil || resp.GetPid() <= 0 || resp.GetError() != "" {
		t.Fatalf("%v %+v", err, resp)
	}
	if _, err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(body), "env-ok") {
		t.Fatalf("stdout %q err %v", body, err)
	}
}

func TestServe_EmptyStdioAndCurrentUser(t *testing.T) {
	ctx, c, _ := serveClient(t)
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Start(ctx, &shimpb.StartRequest{
		Command:   "/usr/bin/true",
		RunAsUser: u.Username,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.GetError(), "lookup user") {
		t.Fatalf("lookup failed for current user: %+v", resp)
	}
	if resp.GetError() == "" {
		if resp.GetPid() <= 0 {
			t.Fatalf("got %+v", resp)
		}
		if _, err := c.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServe_StopInvalidThenNumericKill(t *testing.T) {
	ctx, c, dir := serveClient(t)
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{
		Command:    "/bin/sh",
		Args:       []string{"-c", `trap "" TERM; sleep 30`},
		StdoutPath: out,
		StderrPath: out,
	})
	if err != nil || resp.GetPid() <= 0 {
		t.Fatalf("%v %+v", err, resp)
	}
	bad, err := c.Stop(ctx, &shimpb.StopRequest{Signal: "NOTASIG", TimeoutMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	if bad.GetError() == "" {
		t.Fatalf("want invalid signal error, got %+v", bad)
	}
	stop, err := c.Stop(ctx, &shimpb.StopRequest{Signal: "TERM", TimeoutMs: 200, KillSignal: "9"})
	if err != nil {
		t.Fatal(err)
	}
	if stop.GetError() != "" {
		t.Fatalf("got %+v", stop)
	}
}

func TestServe_EmptySocket(t *testing.T) {
	if err := shim.Serve(context.Background(), ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestServe_SignalZeroAndEmptyCommand(t *testing.T) {
	ctx, c, dir := serveClient(t)
	empty, err := c.Start(ctx, &shimpb.StartRequest{Command: ""})
	if err != nil {
		t.Fatal(err)
	}
	if empty.GetError() == "" {
		t.Fatalf("want command error, got %+v", empty)
	}
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/sleep", Args: []string{"30"}, StdoutPath: out, StderrPath: out})
	if err != nil || resp.GetPid() <= 0 {
		t.Fatalf("%v %+v", err, resp)
	}
	sig, err := c.Signal(ctx, &shimpb.SignalRequest{Signal: "0"})
	if err != nil {
		t.Fatal(err)
	}
	if sig.GetError() == "" {
		t.Fatalf("want invalid signal, got %+v", sig)
	}
	if _, err := c.Stop(ctx, &shimpb.StopRequest{Signal: "SIGKILL", TimeoutMs: 1000, KillSignal: "SIGKILL"}); err != nil {
		t.Fatal(err)
	}
}

func serveClient(t *testing.T) (context.Context, *shim.Client, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	dir := shortTemp(t)
	sock := filepath.Join(dir, "s.sock")
	errCh := make(chan error, 1)
	go func() { errCh <- shim.Serve(ctx, sock) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not return")
		}
	})
	waitSock(t, sock)
	c, err := shim.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return ctx, c, dir
}

func shortTemp(t *testing.T) string {
	t.Helper()
	// Darwin sun_path is ~104 bytes; t.TempDir() plus long test names overflows Listen.
	dir, err := os.MkdirTemp("", "pmsh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func waitSock(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s not ready", path)
}
