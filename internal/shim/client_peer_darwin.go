//go:build darwin

package shim

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// SignalPeer validates the Unix socket peer immediately before signaling it.
// Darwin has no pidfd equivalent, so this is an explicit best-effort fallback.
func (c *Client) SignalPeer(ctx context.Context, expectedPID int, signal syscall.Signal) error {
	peerPID, err := c.peerPID()
	if err != nil {
		return err
	}
	if peerPID != expectedPID {
		return fmt.Errorf("shim peer pid %d does not match stored pid %d", peerPID, expectedPID)
	}
	if _, err := c.Status(ctx); err != nil {
		return fmt.Errorf("verify shim peer: %w", err)
	}
	if err := unix.Kill(peerPID, signal); err != nil {
		return fmt.Errorf("signal verified shim peer: %w", err)
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
	var peerPID int
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		peerPID, peerErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, fmt.Errorf("inspect shim socket: %w", err)
	}
	if peerErr != nil {
		return 0, fmt.Errorf("read shim peer credentials: %w", peerErr)
	}
	return peerPID, nil
}
