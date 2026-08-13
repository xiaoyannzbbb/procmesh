package process_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var testShimBin string

func TestMain(m *testing.M) {
	os.Exit(runProcessTests(m))
}

func runProcessTests(m *testing.M) int {
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
	return m.Run()
}
