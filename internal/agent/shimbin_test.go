package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

var testShimBin string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "shim-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "procmesh-shim")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/procmesh-shim")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build shim: %v\n%s", err, out)
		return 1
	}
	testShimBin = bin
	code := m.Run()
	reapTestShims(testShimBin)
	return code
}

func reapTestShims(bin string) {
	if bin == "" {
		return
	}
	out, err := exec.Command("pgrep", "-f", bin).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		if kids, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output(); err == nil {
			for _, kline := range strings.Split(string(kids), "\n") {
				kpid, kerr := strconv.Atoi(strings.TrimSpace(kline))
				if kerr != nil || kpid <= 1 {
					continue
				}
				_ = unix.Kill(kpid, unix.SIGKILL)
			}
		}
		_ = unix.Kill(pid, unix.SIGKILL)
	}
}
