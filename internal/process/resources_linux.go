//go:build linux

package process

import (
	"github.com/qleelulu/procmesh/internal/errcode"
	"golang.org/x/sys/unix"
)

func applyOpenFiles(pid int, n int64) error {
	cur, max := NofileLimit(n)
	lim := unix.Rlimit{Cur: cur, Max: max}
	if err := unix.Prlimit(pid, unix.RLIMIT_NOFILE, &lim, nil); err != nil {
		return errcode.E(errcode.INVALID, "resource_limit")
	}
	return nil
}
