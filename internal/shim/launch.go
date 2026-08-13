package shim

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	shimpb "github.com/qleelulu/procmesh/proto/shim/v1"
)

const (
	shimLogMode     = 0o640
	socketDirMode   = 0o750
	reconnectWait   = 2 * time.Second
	socketPollEvery = 10 * time.Millisecond
)

// LookPath finds procmesh-shim in PATH, then next to the agent binary.
func LookPath() (string, error) {
	if p, err := exec.LookPath("procmesh-shim"); err == nil {
		return p, nil
	}
	candidate := filepath.Join(filepath.Dir(os.Args[0]), "procmesh-shim")
	st, err := os.Stat(candidate)
	if err == nil && !st.IsDir() {
		return candidate, nil
	}
	return "", fmt.Errorf("procmesh-shim not found in PATH or %s", filepath.Dir(os.Args[0]))
}

// Launch starts procmesh-shim as a session-leader sibling process.
// Extra files are not inherited. stdout/stderr go to socket+".shim.log".
func Launch(ctx context.Context, bin, socket, instanceID string) (int, error) {
	if bin == "" {
		return 0, fmt.Errorf("shim binary is required")
	}
	if socket == "" {
		return 0, fmt.Errorf("socket path is required")
	}
	if instanceID == "" {
		return 0, fmt.Errorf("instance id is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(socket), socketDirMode); err != nil {
		return 0, fmt.Errorf("create socket dir: %w", err)
	}

	logPath := socket + ".shim.log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, shimLogMode)
	if err != nil {
		return 0, fmt.Errorf("open shim log: %w", err)
	}

	cmd := exec.Command(bin, "--socket", socket, "--instance-id", instanceID)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.ExtraFiles = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start shim: %w", err)
	}
	_ = logFile.Close()

	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()

	if err := waitSocket(ctx, socket, pid); err != nil {
		_ = cmd.Process.Kill()
		return pid, err
	}
	return pid, nil
}

// Discover maps sanitized instance IDs to *.sock paths in dir.
func Discover(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("discover shims: %w", err)
	}
	found := make(map[string]string)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sock") {
			continue
		}
		id := strings.TrimSuffix(name, ".sock")
		found[id] = filepath.Join(dir, name)
	}
	return found, nil
}

// Reconnect dials a shim socket and returns Status.
func Reconnect(ctx context.Context, socket string) (*Client, *shimpb.StatusResponse, error) {
	deadline := time.Now().Add(reconnectWait)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	var last error
	for {
		if err := ctx.Err(); err != nil {
			if last != nil {
				return nil, nil, fmt.Errorf("reconnect shim: %w", last)
			}
			return nil, nil, err
		}
		c, err := Dial(socket)
		if err == nil {
			st, stErr := c.Status(ctx)
			if stErr == nil {
				return c, st, nil
			}
			_ = c.Close()
			last = stErr
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("reconnect shim: %w", last)
		}
		select {
		case <-ctx.Done():
			if last != nil {
				return nil, nil, fmt.Errorf("reconnect shim: %w", last)
			}
			return nil, nil, ctx.Err()
		case <-time.After(socketPollEvery):
		}
	}
}

func waitSocket(ctx context.Context, socket string, pid int) error {
	deadline := time.Now().Add(reconnectWait)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for shim socket: %w", err)
		}
		if processGone(pid) {
			return fmt.Errorf("shim pid %d exited before listening on %s", pid, socket)
		}
		c, err := Dial(socket)
		if err == nil {
			_ = c.Close()
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for shim socket: %w", last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for shim socket: %w", ctx.Err())
		case <-time.After(socketPollEvery):
		}
	}
}

func processGone(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	return p.Signal(syscall.Signal(0)) != nil
}
