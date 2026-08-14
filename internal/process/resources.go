package process

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const (
	cgroupRoot   = "/sys/fs/cgroup"
	cgroupParent = "/sys/fs/cgroup/procmesh"
	cpuPeriodUs  = 100000
)

// ResourceLimitSet reports whether any resource field is non-zero.
func ResourceLimitSet(r ResourceLimit) bool {
	return r.CPUQuotaMillis != 0 || r.MemoryBytes != 0 || r.OpenFiles != 0
}

// ApplyResourceLimit applies r to pid via cgroup v2 on Linux.
// Non-Linux or any apply error returns INVALID; P0 callers must not fail start.
func ApplyResourceLimit(pid int, r ResourceLimit) error {
	if runtime.GOOS != "linux" || pid <= 0 {
		return errcode.E(errcode.INVALID, "resource_limit")
	}
	dir := filepath.Join(cgroupParent, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errcode.E(errcode.INVALID, "resource_limit")
	}
	// Best-effort: enable memory/cpu for children of the root and procmesh parent.
	_ = os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("+memory +cpu"), 0o644)
	_ = os.WriteFile(filepath.Join(cgroupParent, "cgroup.subtree_control"), []byte("+memory +cpu"), 0o644)

	if r.MemoryBytes > 0 {
		if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(r.MemoryBytes, 10)), 0o644); err != nil {
			return errcode.E(errcode.INVALID, "resource_limit")
		}
	}
	if r.CPUQuotaMillis > 0 {
		quota := r.CPUQuotaMillis * 1000
		if err := os.WriteFile(filepath.Join(dir, "cpu.max"), []byte(fmt.Sprintf("%d %d", quota, cpuPeriodUs)), 0o644); err != nil {
			return errcode.E(errcode.INVALID, "resource_limit")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return errcode.E(errcode.INVALID, "resource_limit")
	}
	return nil
}
