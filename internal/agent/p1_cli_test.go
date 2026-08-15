package agent

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/cli"
)

func TestP1_CLIAgainstLiveAgent(t *testing.T) {
	addr := startLiveAgent(t)
	spec := writeSleepSpec(t)
	name := "sleep"

	code, out, errb := runP1CLI("--server", addr, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	code, _, errb = runP1CLI("--server", addr, "process", "start", name)
	if code != 0 {
		t.Fatalf("start exit=%d stderr=%q", code, errb)
	}

	deadline := time.Now().Add(8 * time.Second)
	var listOut string
	for time.Now().Before(deadline) {
		code, listOut, errb = runP1CLI("--server", addr, "process", "list")
		if code != 0 {
			t.Fatalf("list exit=%d stderr=%q", code, errb)
		}
		if listHasRunningOrStarting(listOut) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !listHasRunningOrStarting(listOut) {
		t.Fatalf("list want RUNNING or STARTING, got %q", listOut)
	}

	code, _, errb = runP1CLI("--server", addr, "logs", name)
	if code != 0 {
		t.Fatalf("logs exit=%d stderr=%q", code, errb)
	}

	code, _, errb = runP1CLI("--server", addr, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 1 {
		t.Fatalf("second apply exit=%d stderr=%q want 1", code, errb)
	}
	if !strings.Contains(errb, "CONFLICT") {
		t.Fatalf("second apply stderr want CONFLICT: %q", errb)
	}

	opID := "op-p1-restart"
	code, _, errb = runP1CLI("--server", addr, "--operation-id", opID, "process", "restart", name)
	if code != 0 {
		t.Fatalf("restart exit=%d stderr=%q", code, errb)
	}
	code, _, errb = runP1CLI("--server", addr, "--operation-id", opID, "restart", name)
	if code != 0 {
		t.Fatalf("second restart same operation-id exit=%d stderr=%q", code, errb)
	}

	code, _, errb = runP1CLI("--server", addr, "process", "stop", name)
	if code != 0 {
		t.Fatalf("stop exit=%d stderr=%q", code, errb)
	}
	code, listOut, errb = runP1CLI("--server", addr, "process", "list")
	if code != 0 {
		t.Fatalf("list after stop exit=%d stderr=%q", code, errb)
	}
	if !listDesiredStopped(listOut, name) {
		t.Fatalf("after stop want desired=STOPPED, got %q", listOut)
	}

	res, err := http.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("readyz want 200 got %d", res.StatusCode)
	}
}

func startLiveAgent(t *testing.T) string {
	t.Helper()
	// macOS unix socket path cap is ~104 bytes; t.TempDir() is too long.
	root, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	got := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		ensureTestSession(t)
		errCh <- Run(ctx, Options{
			DataDir:       root,
			Listen:        "127.0.0.1:0",
			ControlListen: "127.0.0.1:0",
			ShimBin:       testShimBin,
			OnListen:      func(addr string) { got <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-got:
	case err := <-errCh:
		t.Fatalf("agent run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("agent listen timeout")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
		}
		cleanupDataDir(root)
	})
	return addr
}

func writeSleepSpec(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sleep.yaml")
	body := "name: sleep\nprocess_id: slp\ncommand: /bin/sleep\nargs:\n  - \"60\"\ninstances: 1\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runP1CLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.Main(args, bytes.NewReader(nil), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func listHasRunningOrStarting(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "RUNNING") || strings.Contains(line, "STARTING") {
			return true
		}
	}
	return false
}

func listDesiredStopped(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		if fields[0] == name && fields[2] == "STOPPED" {
			return true
		}
	}
	return false
}
