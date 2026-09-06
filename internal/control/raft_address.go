package control

import (
	"net"
	"strconv"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// ValidateRaftAddress validates an address that peers will dial. Wildcard
// addresses are valid listeners but can never be advertised to a Raft peer.
func ValidateRaftAddress(addr string) error {
	if addr == "" || addr != strings.TrimSpace(addr) {
		return errcode.E(errcode.INVALID, "raft_address must be a dialable host:port")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return errcode.E(errcode.INVALID, "raft_address must be a dialable host:port")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return errcode.E(errcode.INVALID, "raft_address must use a non-zero port")
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return errcode.E(errcode.INVALID, "raft_address must not use a wildcard host")
	}
	return nil
}

// IsUnspecifiedRaftAddress identifies the persisted address form that can be
// interpreted by the kernel as the local Raft listener and destabilize a
// leader. Malformed legacy test transports are left to their own dialer.
func IsUnspecifiedRaftAddress(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}
