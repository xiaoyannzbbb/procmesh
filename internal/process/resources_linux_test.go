//go:build linux

package process_test

import (
	"os/exec"
	"testing"

	"github.com/qleelulu/procmesh/internal/process"
	"golang.org/x/sys/unix"
)

func TestApplyResourceLimit_OpenFiles(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := process.ApplyResourceLimit(cmd.Process.Pid, process.ResourceLimit{OpenFiles: 64}); err != nil {
		t.Fatal(err)
	}
	var got unix.Rlimit
	if err := unix.Prlimit(cmd.Process.Pid, unix.RLIMIT_NOFILE, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cur != 64 {
		t.Fatalf("nofile cur=%d want 64", got.Cur)
	}
}
