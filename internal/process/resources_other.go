//go:build !linux

package process

import "github.com/qleelulu/procmesh/internal/errcode"

func applyOpenFiles(pid int, n int64) error {
	_ = pid
	_ = n
	return errcode.E(errcode.INVALID, "resource_limit")
}
