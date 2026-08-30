//go:build !linux && !darwin

package shim

import (
	"context"
	"fmt"
	"syscall"
)

// SignalPeer refuses PID-based cleanup where the platform cannot bind a
// signal to the verified Unix socket peer.
func (c *Client) SignalPeer(context.Context, int, syscall.Signal) error {
	return fmt.Errorf("verified shim peer signaling is unsupported on this platform")
}

func (c *Client) peerPID() (int, error) {
	return 0, fmt.Errorf("shim peer credentials are unsupported on this platform")
}
