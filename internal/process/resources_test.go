package process_test

import (
	"os"
	"runtime"
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
)

func TestResourceLimitSet(t *testing.T) {
	if process.ResourceLimitSet(process.ResourceLimit{}) {
		t.Fatal("empty should be unset")
	}
	if !process.ResourceLimitSet(process.ResourceLimit{MemoryBytes: 1 << 20}) {
		t.Fatal("memory set")
	}
	if !process.ResourceLimitSet(process.ResourceLimit{CPUQuotaMillis: 50}) {
		t.Fatal("cpu set")
	}
	if !process.ResourceLimitSet(process.ResourceLimit{OpenFiles: 256}) {
		t.Fatal("open files set")
	}
}

func TestApplyResourceLimit_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux applies cgroup v2")
	}
	err := process.ApplyResourceLimit(os.Getpid(), process.ResourceLimit{MemoryBytes: 1 << 20})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}

func TestApplyResourceLimit_InvalidPID(t *testing.T) {
	err := process.ApplyResourceLimit(0, process.ResourceLimit{MemoryBytes: 1 << 20})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}
