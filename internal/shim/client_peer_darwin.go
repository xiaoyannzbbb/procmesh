//go:build darwin

package shim

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// SignalPeer fails closed because Darwin has no pidfd equivalent. Signaling a
// PID obtained from peer credentials would race with process exit and PID reuse.
func (c *Client) SignalPeer(context.Context, int, syscall.Signal) error {
	return fmt.Errorf("verified shim peer signaling is unsupported on Darwin")
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
