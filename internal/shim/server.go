package shim

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	shimpb "github.com/qleelulu/procmesh/proto/shim/v1"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

const (
	defaultStopTimeout = 10 * time.Second
	logFileMode        = 0o640
)

// Serve listens on a unix socket and handles shim envelopes sequentially
// until ctx is cancelled.
func Serve(ctx context.Context, socketPath string) error {
	if _, err := unix.Setsid(); err != nil {
		log.Printf("setsid: %v", err)
	}
	if socketPath == "" {
		return fmt.Errorf("socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	srv := newServer()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept: %w", err)
		}
		go func() {
			handleConn(ctx, conn, srv)
			_ = conn.Close()
		}()
	}
}

type server struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	startedUnix int64
	done        chan struct{}
	exitCode    int32
}

func newServer() *server {
	return &server{}
}

func handleConn(ctx context.Context, conn net.Conn, srv *server) {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-connCtx.Done()
		_ = conn.Close()
	}()

	for {
		payload, err := ReadFrame(conn)
		if err != nil {
			return
		}
		var env shimpb.Envelope
		if err := proto.Unmarshal(payload, &env); err != nil {
			return
		}
		resp := srv.handle(connCtx, &env)
		if resp == nil {
			continue
		}
		raw, err := proto.Marshal(resp)
		if err != nil {
			return
		}
		if err := WriteFrame(conn, raw); err != nil {
			return
		}
	}
}

func (s *server) handle(ctx context.Context, env *shimpb.Envelope) *shimpb.Envelope {
	switch {
	case env.GetStart() != nil:
		return &shimpb.Envelope{Body: &shimpb.Envelope_StartOk{StartOk: s.start(env.GetStart())}}
	case env.GetStop() != nil:
		return &shimpb.Envelope{Body: &shimpb.Envelope_StopOk{StopOk: s.stop(ctx, env.GetStop())}}
	case env.GetSignal() != nil:
		return &shimpb.Envelope{Body: &shimpb.Envelope_SignalOk{SignalOk: s.signal(env.GetSignal())}}
	case env.GetStatus() != nil:
		return &shimpb.Envelope{Body: &shimpb.Envelope_StatusOk{StatusOk: s.status()}}
	case env.GetWait() != nil:
		return &shimpb.Envelope{Body: &shimpb.Envelope_WaitOk{WaitOk: s.wait(ctx)}}
	default:
		return nil
	}
}

func (s *server) start(req *shimpb.StartRequest) *shimpb.StartResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isAliveLocked() {
		return &shimpb.StartResponse{Error: "already started"}
	}
	if req.GetCommand() == "" {
		return &shimpb.StartResponse{Error: "command is required"}
	}

	cmd := exec.Command(req.GetCommand(), req.GetArgs()...)
	if cwd := req.GetCwd(); cwd != "" {
		cmd.Dir = cwd
	}
	if env := req.GetEnv(); len(env) > 0 {
		cmd.Env = envSlice(env)
	}
	attr := &syscall.SysProcAttr{Setsid: true}
	if runAs := req.GetRunAsUser(); runAs != "" {
		cred, err := lookupCredential(runAs)
		if err != nil {
			return &shimpb.StartResponse{Error: err.Error()}
		}
		attr.Credential = cred
	}
	cmd.SysProcAttr = attr

	stdout, stderr, closer, err := openStdio(req.GetStdoutPath(), req.GetStderrPath())
	if err != nil {
		return &shimpb.StartResponse{Error: err.Error()}
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		closer()
		return &shimpb.StartResponse{Error: err.Error()}
	}
	closer()

	s.cmd = cmd
	s.startedUnix = time.Now().Unix()
	s.exitCode = 0
	s.done = make(chan struct{})
	done := s.done
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		if cmd.ProcessState != nil {
			s.exitCode = int32(cmd.ProcessState.ExitCode())
		}
		s.mu.Unlock()
		close(done)
	}()
	return &shimpb.StartResponse{Pid: int32(cmd.Process.Pid)}
}

func (s *server) stop(ctx context.Context, req *shimpb.StopRequest) *shimpb.StopResponse {
	s.mu.Lock()
	if s.cmd == nil || s.cmd.Process == nil {
		s.mu.Unlock()
		return &shimpb.StopResponse{Error: "no child"}
	}
	pid := s.cmd.Process.Pid
	done := s.done
	s.mu.Unlock()

	sigName := req.GetSignal()
	if sigName == "" {
		sigName = "SIGTERM"
	}
	sig, err := parseSignal(sigName)
	if err != nil {
		return &shimpb.StopResponse{Error: err.Error()}
	}
	if err := unix.Kill(pid, sig); err != nil && err != unix.ESRCH {
		return &shimpb.StopResponse{Error: err.Error()}
	}

	timeout := defaultStopTimeout
	if req.GetTimeoutMs() > 0 {
		timeout = time.Duration(req.GetTimeoutMs()) * time.Millisecond
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return s.waitResult()
	case <-ctx.Done():
		return &shimpb.StopResponse{Error: ctx.Err().Error()}
	case <-timer.C:
	}

	killName := req.GetKillSignal()
	if killName == "" {
		killName = "SIGKILL"
	}
	killSig, err := parseSignal(killName)
	if err != nil {
		return &shimpb.StopResponse{Error: err.Error()}
	}
	if err := unix.Kill(pid, killSig); err != nil && err != unix.ESRCH {
		return &shimpb.StopResponse{Error: err.Error()}
	}

	select {
	case <-done:
		return s.waitResult()
	case <-ctx.Done():
		return &shimpb.StopResponse{Error: ctx.Err().Error()}
	}
}

func (s *server) signal(req *shimpb.SignalRequest) *shimpb.SignalResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isAliveLocked() {
		return &shimpb.SignalResponse{Error: "no child"}
	}
	name := req.GetSignal()
	if name == "" {
		name = "SIGTERM"
	}
	sig, err := parseSignal(name)
	if err != nil {
		return &shimpb.SignalResponse{Error: err.Error()}
	}
	if err := unix.Kill(s.cmd.Process.Pid, sig); err != nil && err != unix.ESRCH {
		return &shimpb.SignalResponse{Error: err.Error()}
	}
	return &shimpb.SignalResponse{}
}

func (s *server) status() *shimpb.StatusResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &shimpb.StatusResponse{StartedUnix: s.startedUnix, ExitCode: s.exitCode}
	if s.cmd != nil && s.cmd.Process != nil {
		resp.Pid = int32(s.cmd.Process.Pid)
	}
	resp.Alive = s.isAliveLocked()
	return resp
}

func (s *server) wait(ctx context.Context) *shimpb.WaitResponse {
	s.mu.Lock()
	if s.cmd == nil {
		s.mu.Unlock()
		return &shimpb.WaitResponse{Error: "no child"}
	}
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return s.waitWaitResponse()
	}
	select {
	case <-done:
		return s.waitWaitResponse()
	case <-ctx.Done():
		return &shimpb.WaitResponse{Error: ctx.Err().Error()}
	}
}

func (s *server) waitResult() *shimpb.StopResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &shimpb.StopResponse{ExitCode: s.exitCode}
}

func (s *server) waitWaitResponse() *shimpb.WaitResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &shimpb.WaitResponse{ExitCode: s.exitCode}
}

func (s *server) isAliveLocked() bool {
	if s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	// Treat a closed done channel as ProcessState != nil without racing Wait().
	if s.done != nil {
		select {
		case <-s.done:
			return false
		default:
		}
	}
	err := unix.Kill(s.cmd.Process.Pid, 0)
	return err == nil
}

func openStdio(stdoutPath, stderrPath string) (stdout, stderr *os.File, closer func(), err error) {
	open := func(path string) (*os.File, error) {
		if path == "" {
			return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		}
		return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, logFileMode)
	}
	stdout, err = open(stdoutPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open stdout: %w", err)
	}
	if stderrPath != "" && stderrPath == stdoutPath {
		stderr = stdout
	} else {
		stderr, err = open(stderrPath)
		if err != nil {
			_ = stdout.Close()
			return nil, nil, nil, fmt.Errorf("open stderr: %w", err)
		}
	}
	closer = func() {
		_ = stdout.Close()
		if stderr != stdout {
			_ = stderr.Close()
		}
	}
	return stdout, stderr, closer, nil
}

func lookupCredential(name string) (*syscall.Credential, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("lookup user %q: %w", name, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parse uid for %q: %w", name, err)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parse gid for %q: %w", name, err)
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}

func parseSignal(name string) (syscall.Signal, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return 0, fmt.Errorf("empty signal")
	}
	if num, err := strconv.Atoi(n); err == nil {
		if num <= 0 {
			return 0, fmt.Errorf("unknown signal %q", name)
		}
		return syscall.Signal(num), nil
	}
	upper := strings.ToUpper(n)
	if !strings.HasPrefix(upper, "SIG") {
		upper = "SIG" + upper
	}
	if sig := unix.SignalNum(upper); sig != 0 {
		return sig, nil
	}
	return 0, fmt.Errorf("unknown signal %q", name)
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
