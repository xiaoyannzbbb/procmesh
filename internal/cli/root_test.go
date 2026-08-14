package cli

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestCLI_ApplyAndList(t *testing.T) {
	url := newTestServer(t)
	spec := writeSpec(t, "web")

	code, out, errb := runCLI("--server", url, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	if !strings.Contains(out, "revision=1") {
		t.Fatalf("apply stdout=%q", out)
	}

	code, out, errb = runCLI("process", "list", "--server", url)
	if code != 0 {
		t.Fatalf("list exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(out, "web") {
		t.Fatalf("list missing name: %q", out)
	}
}

func TestCLI_StartAliasOK(t *testing.T) {
	url := newTestServer(t)
	spec := writeSpec(t, "true")
	if code, out, errb := runCLI("--server", url, "process", "apply", "--file", spec, "--expected-revision", "0"); code != 0 {
		t.Fatalf("apply exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	code, _, errb := runCLI("--server", url, "start", "true")
	if code != 0 {
		t.Fatalf("start alias exit=%d stderr=%q", code, errb)
	}
}

func TestCLI_ApplyConflictRevision(t *testing.T) {
	url := newTestServer(t)
	spec := writeSpec(t, "web")
	if code, out, errb := runCLI("--server", url, "process", "apply", "--file", spec, "--expected-revision", "0"); code != 0 {
		t.Fatalf("apply exit=%d stderr=%q stdout=%q", code, errb, out)
	}

	code, _, errb := runCLI("--server", url, "process", "apply", "--file", spec, "--expected-revision", "99")
	if code != 1 {
		t.Fatalf("conflict exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "CONFLICT") {
		t.Fatalf("stderr want CONFLICT: %q", errb)
	}
}

func TestCLI_NodeRejected(t *testing.T) {
	code, _, errb := runCLI("--node", "x", "status")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "remote --node is not supported until P3") {
		t.Fatalf("stderr=%q", errb)
	}

	code, _, errb = runCLI("status", "--node", "agent-a")
	if code != 2 {
		t.Fatalf("after-command exit=%d stderr=%q", code, errb)
	}
}

func TestCLI_UnknownCommand(t *testing.T) {
	code, _, errb := runCLI("foobar")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
	if errb == "" {
		t.Fatal("expected usage on stderr")
	}
}

func TestClient_HTTPTimeout(t *testing.T) {
	c := newClient("127.0.0.1:9000", "op", "t")
	if c.http == nil {
		t.Fatal("nil http client")
	}
	if c.http.Timeout != httpTimeout {
		t.Fatalf("timeout=%v want %v", c.http.Timeout, httpTimeout)
	}
	if httpTimeout != 5*time.Second {
		t.Fatalf("httpTimeout=%v want 5s", httpTimeout)
	}
}

func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Main(args, bytes.NewReader(nil), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeSpec(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	body := "name: " + name + "\ncommand: /bin/true\nargs:\n  - x\nrestart:\n  mode: never\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestServer(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.GetOrCreateNodeID(context.Background()); err != nil {
		t.Fatal(err)
	}
	boot, err := st.GetBootID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if boot == "" {
		if _, err := st.RotateBootID(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	mgr := process.NewManager(process.Deps{Store: st, Layout: layout, Now: time.Now})
	srv, err := api.NewServer(api.Options{Mgr: mgr, Store: st, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)
	return hs.URL
}
