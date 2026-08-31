//go:build !linux && !darwin

package update

import "github.com/qleelulu/procmesh/internal/errcode"

func defaultSelfExec(string, []string, []string) error {
	return errcode.E(errcode.UNAVAILABLE, "self-exec is not supported on this platform")
}
