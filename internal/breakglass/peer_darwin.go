//go:build darwin

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
		cred    *unix.Xucred
		credErr error
	)
	if err := rawConn.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return Peer{}, fmt.Errorf("inspect unix peer credentials: %w", err)
	}
	if credErr != nil {
		return Peer{}, fmt.Errorf("read unix peer credentials: %w", credErr)
	}
	gid := -1
	if cred.Ngroups > 0 {
		gid = int(cred.Groups[0])
	}
	return Peer{PID: -1, UID: int(cred.Uid), GID: gid}, nil
}
