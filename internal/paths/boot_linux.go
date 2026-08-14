//go:build linux

package paths

import (
	"os"
	"strings"
)

func readBootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "unknown"
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "unknown"
	}
	return id
}
