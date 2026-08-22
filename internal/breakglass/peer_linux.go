//go:build linux

package breakglass

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func lookupPeer(conn net.Conn) (Peer, error) {
	raw, ok := conn.(syscall.Conn)
	if !ok {
		return Peer{}, fmt.Errorf("unix connection does not expose peer credentials")
	}
	rawConn, err := raw.SyscallConn()
	if err != nil {
		return Peer{}, fmt.Errorf("access unix socket fd: %w", err)
	}
	var (
		cred    *unix.Ucred
		credErr error
	)
	if err := rawConn.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, fmt.Errorf("inspect unix peer credentials: %w", err)
	}
	if credErr != nil {
		return Peer{}, fmt.Errorf("read unix peer credentials: %w", credErr)
	}
	return Peer{PID: int(cred.Pid), UID: int(cred.Uid), GID: int(cred.Gid)}, nil
}
