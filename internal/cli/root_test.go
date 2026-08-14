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
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/version"
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

func TestCLI_ClusterInitAndNodeList(t *testing.T) {
	url := newClusterTestServer(t)
	code, out, errb := runCLI("--server", url, "cluster", "init")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	if !strings.Contains(out, "cluster_id=") || !strings.Contains(out, "admin_password=") {
		t.Fatalf("stdout=%q", out)
	}
	code, out, errb = runCLI("--server", url, "node", "list")
	if code != 0 {
		t.Fatalf("list exit=%d %q %q", code, errb, out)
	}
	if !strings.Contains(out, "ALIVE") && !strings.Contains(out, "JOINING") {
		t.Fatalf("list=%q", out)
	}
}

func TestCLI_TokenCreate(t *testing.T) {
	url := newClusterTestServer(t)
	if code, _, errb := runCLI("--server", url, "cluster", "init"); code != 0 {
		t.Fatalf("init %s", errb)
	}
	code, out, errb := runCLI("--server", url, "node", "token", "create")
	if code != 0 {
		t.Fatalf("%d %q %q", code, errb, out)
	}
	if !strings.Contains(out, "token=pmj_") {
		t.Fatalf("%q", out)
	}
}

func TestCLI_UnknownStillUsage(t *testing.T) {
	code, _, errb := runCLI("foobar")
	if code != 2 || errb == "" {
		t.Fatalf("%d %q", code, errb)
	}
}

func TestCLI_TokenRevoke(t *testing.T) {
	url := newClusterTestServer(t)
	if code, _, errb := runCLI("--server", url, "cluster", "init"); code != 0 {
		t.Fatalf("init %s", errb)
	}
	code, out, errb := runCLI("--server", url, "node", "token", "create")
	if code != 0 {
		t.Fatalf("create %d %q %q", code, errb, out)
	}
	id := kvField(out, "token_id")
	if id == "" {
		t.Fatalf("no token_id in %q", out)
	}
	code, _, errb = runCLI("--server", url, "node", "token", "revoke", id)
	if code != 0 {
		t.Fatalf("revoke exit=%d stderr=%q", code, errb)
	}
	code, _, errb = runCLI("--server", url, "node", "token", "revoke", "missing-token")
	if code != 1 {
		t.Fatalf("missing revoke exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "NOT_FOUND") {
		t.Fatalf("stderr want NOT_FOUND: %q", errb)
	}
}

func TestCLI_NodeStatus(t *testing.T) {
	url := newClusterTestServer(t)
	if code, _, errb := runCLI("--server", url, "cluster", "init"); code != 0 {
		t.Fatalf("init %s", errb)
	}
	code, out, errb := runCLI("--server", url, "node", "list")
	if code != 0 {
		t.Fatalf("list exit=%d %q", code, errb)
	}
	fields := strings.Split(strings.TrimSpace(out), "\t")
	if len(fields) < 1 || fields[0] == "" {
		t.Fatalf("list=%q", out)
	}
	id := fields[0]
	code, out, errb = runCLI("--server", url, "node", "status", id)
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	if !strings.Contains(out, "ALIVE") && !strings.Contains(out, "JOINING") {
		t.Fatalf("status=%q", out)
	}
	code, _, errb = runCLI("--server", url, "node", "status", "no-such-node")
	if code != 1 {
		t.Fatalf("missing status exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "NOT_FOUND") {
		t.Fatalf("stderr want NOT_FOUND: %q", errb)
	}
}

func TestCLI_ClusterInitAdminUser(t *testing.T) {
	url := newClusterTestServer(t)
	code, out, errb := runCLI("--server", url, "cluster", "init", "--admin-user", "root")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	if !strings.Contains(out, "admin_user=root") {
		t.Fatalf("stdout=%q", out)
	}
}

func TestCLI_AgentJoinRequiresFlags(t *testing.T) {
	url := newClusterTestServer(t)
	code, _, errb := runCLI("--server", url, "agent", "join")
	if code != 2 {
		t.Fatalf("missing flags exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "--seed") && !strings.Contains(errb, "--token") {
		t.Fatalf("stderr=%q", errb)
	}
}

func TestCLI_AgentJoinAlreadyInited(t *testing.T) {
	url := newClusterTestServer(t)
	if code, _, errb := runCLI("--server", url, "cluster", "init"); code != 0 {
		t.Fatalf("init %s", errb)
	}
	code, _, errb := runCLI("--server", url, "agent", "join", "--seed", url, "--token", "pmj_x")
	if code != 1 {
		t.Fatalf("join exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "CONFLICT") {
		t.Fatalf("stderr want CONFLICT: %q", errb)
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

func kvField(out, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func newTestServer(t *testing.T) string {
	t.Helper()
	return startTestServer(t, false)
}

func newClusterTestServer(t *testing.T) string {
	t.Helper()
	return startTestServer(t, true)
}

func startTestServer(t *testing.T, withCluster bool) string {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	nodeID, err := st.GetOrCreateNodeID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	boot, err := st.GetBootID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if boot == "" {
		boot, err = st.RotateBootID(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	mgr := process.NewManager(process.Deps{Store: st, Layout: layout, Now: time.Now})
	opts := api.Options{Mgr: mgr, Store: st, Started: time.Now()}
	if withCluster {
		opts.Cluster = api.ClusterDeps{
			Dir:   layout.ClusterDir,
			Store: st,
			Local: func() cluster.NodeSummary {
				cid := ""
				if id, err := st.GetClusterID(context.Background()); err == nil {
					cid = id
				}
				return cluster.NodeSummary{
					NodeID:          nodeID,
					ClusterID:       cid,
					Hostname:        "test-host",
					BootID:          boot,
					State:           cluster.StateAlive,
					ProtocolVersion: version.Protocol,
					APIAddress:      "127.0.0.1:9000",
					GossipAddress:   "127.0.0.1:7946",
				}
			},
			GossipAddr: func() string { return "127.0.0.1:7946" },
			Now:        time.Now,
			NodeID:     nodeID,
			Hostname:   "test-host",
			BootID:     boot,
			APIAddr:    "127.0.0.1:9000",
		}
	}
	srv, err := api.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Engine)
	t.Cleanup(hs.Close)
	return hs.URL
}
