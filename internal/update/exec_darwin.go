//go:build darwin

package update

import "syscall"

func defaultSelfExec(argv0 string, argv, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}
