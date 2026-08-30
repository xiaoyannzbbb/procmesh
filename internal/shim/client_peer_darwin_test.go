//go:build darwin

package shim

import (
	"context"
	"strings"
	"syscall"
	"testing"
)

func TestSignalPeerRefusesBarePIDSignaling(t *testing.T) {
	var client Client
	err := client.SignalPeer(context.Background(), 4242, syscall.SIGTERM)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("SignalPeer() error = %v, want unsupported without inspecting or signaling PID", err)
	}
}
