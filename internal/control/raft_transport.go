package control

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/hashicorp/raft"
)

type guardedRaftStream struct {
	net.Listener
	advertise net.Addr
}

func newRaftTCPTransport(bind string, advertise net.Addr) (*raft.NetworkTransport, error) {
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, err
	}
	stream := &guardedRaftStream{Listener: listener, advertise: advertise}
	if err := ValidateRaftAddress(stream.Addr().String()); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("local raft address: %w", err)
	}
	return raft.NewNetworkTransport(stream, 3, 10*time.Second, io.Discard), nil
}

func (s *guardedRaftStream) Addr() net.Addr {
	if s.advertise != nil {
		return s.advertise
	}
	return s.Listener.Addr()
}

func (s *guardedRaftStream) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	if err := ValidateRaftAddress(string(address)); err != nil {
		return nil, fmt.Errorf("refuse unsafe raft peer address: %w", err)
	}
	return net.DialTimeout("tcp", string(address), timeout)
}
