//go:build linux

package shim

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// SignalPeer signals the process owning this Unix socket after binding the
// signal to a pidfd. The status round trip proves the peer still owns the
// connection after pidfd_open, closing the PID-reuse race.
func (c *Client) SignalPeer(ctx context.Context, expectedPID int, signal syscall.Signal) error {
	peerPID, err := c.peerPID()
	if err != nil {
		return err
	}
	if peerPID != expectedPID {
		return fmt.Errorf("shim peer pid %d does not match stored pid %d", peerPID, expectedPID)
	}
	pidfd, err := unix.PidfdOpen(peerPID, 0)
	if err != nil {
		return fmt.Errorf("open shim pidfd: %w", err)
	}
	defer unix.Close(pidfd)
	if _, err := c.Status(ctx); err != nil {
		return fmt.Errorf("verify shim peer after pidfd open: %w", err)
	}
	if err := unix.PidfdSendSignal(pidfd, signal, nil, 0); err != nil {
		return fmt.Errorf("signal shim pidfd: %w", err)
	}
	timeout := 2000
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx.Err()
		}
		timeout = int(remaining.Milliseconds())
		if timeout < 1 {
			timeout = 1
		}
	}
	poll := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	n, err := unix.Poll(poll, timeout)
	if err != nil {
		return fmt.Errorf("wait for shim peer exit: %w", err)
	}
	if n == 0 || poll[0].Revents&unix.POLLIN == 0 {
		return fmt.Errorf("wait for shim peer exit: %w", context.DeadlineExceeded)
	}
	return nil
}

func (c *Client) peerPID() (int, error) {
	conn, ok := c.conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("shim connection is not a Unix socket")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("access shim socket: %w", err)
	}
	var (
		cred    *unix.Ucred
		peerErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("inspect shim socket: %w", err)
	}
	if peerErr != nil {
		return 0, fmt.Errorf("read shim peer credentials: %w", peerErr)
	}
	return int(cred.Pid), nil
}
