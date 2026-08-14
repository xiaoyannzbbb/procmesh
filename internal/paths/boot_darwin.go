//go:build darwin

package paths

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func readBootID() string {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return "unknown"
	}
	return fmt.Sprintf("%d.%d", tv.Sec, tv.Usec)
}
