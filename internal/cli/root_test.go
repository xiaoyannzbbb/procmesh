package cli

import (
	"bytes"
	"context"
	"net/http"
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

func TestCLI_NodeHeaderSent(t *testing.T) {
	var got string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Procmesh-Target-Node")
		http.Error(w, "no", 404)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	code, _, _ := runCLI("--server", srv.URL, "--node", "node-c", "process", "list")
	if code == 2 {
		t.Fatalf("P3 must accept --node, exit=%d", code)
	}
	if got != "node-c" {
		t.Fatalf("header=%q", got)
	}

	got = "sentinel"
	code, _, _ = runCLI("--server", srv.URL, "process", "list")
	if code == 2 {
		t.Fatalf("empty --node must be accepted, exit=%d", code)
	}
	if got != "" {
		t.Fatalf("empty --node must not set header, got=%q", got)
	}
}

func TestCLI_NodeRemoveUsage(t *testing.T) {
	if !strings.Contains(usageText, "node remove NODE_ID") {
		t.Fatal("usage missing node remove")
	}
	if !strings.Contains(usageText, "node promote NODE_ID") {
		t.Fatal("usage missing node promote")
	}
	code, _, errb := runCLI("node", "remove")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "node remove") {
		t.Fatalf("stderr=%q", errb)
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

func TestCLI_UsageIncludesMetricsHistory(t *testing.T) {
	if !strings.Contains(usageText, "metrics history node") {
		t.Fatal("usage")
	}
	if !strings.Contains(usageText, "metrics history process") {
		t.Fatal("usage process")
	}
}

func TestCLI_ParseSinceUntil(t *testing.T) {
	opt, err := parseArgs([]string{"--since", "1700000000", "--until", "2026-08-16T00:00:00Z", "metrics", "history", "node", "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.sinceUnix != 1700000000 {
		t.Fatalf("since %d", opt.sinceUnix)
	}
	if opt.untilUnix == 0 {
		t.Fatal("until")
	}
}

func TestCLI_MetricsHistory(t *testing.T) {
	url, st := newTestServerWithStore(t)
	ctx := context.Background()
	if err := st.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 1_700_000_000, Value: 11},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 1_700_000_120, Value: 22},
	}); err != nil {
		t.Fatal(err)
	}

	code, out, errb := runCLI("--server", url, "metrics", "history", "node", "n1", "--since", "1700000000", "--until", "1700000120")
	if code != 0 {
		t.Fatalf("history exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	if !strings.Contains(out, "ts=1700000000") {
		t.Fatalf("missing ts=1700000000: %q", out)
	}
	if !strings.Contains(out, "value=11") {
		t.Fatalf("missing value=11: %q", out)
	}
	if strings.Contains(out, "ts=1700000060") {
		t.Fatalf("gap printed: %q", out)
	}
}

func TestCLI_GroupUsageAndParse(t *testing.T) {
	if !strings.Contains(usageText, "group list") {
		t.Fatal("usage missing group list")
	}
	if !strings.Contains(usageText, "group create --name NAME") {
		t.Fatal("usage missing group create")
	}
	if !strings.Contains(usageText, "CLUSTER|AGENT|AGENT_GROUP|PROCESS_GROUP") {
		t.Fatal("usage missing expanded role grant scopes")
	}

	opt, err := parseArgs([]string{"group", "create", "--name", "finance"})
	if err != nil {
		t.Fatalf("group create parse: %v", err)
	}
	if len(opt.args) != 2 || opt.args[0] != "group" || opt.args[1] != "create" {
		t.Fatalf("args=%v", opt.args)
	}
	if opt.name != "finance" {
		t.Fatalf("name=%q", opt.name)
	}

	opt, err = parseArgs([]string{"group", "add-member", "--group-id", "GID", "--node-id", "NID"})
	if err != nil {
		t.Fatalf("group add-member parse: %v", err)
	}
	if opt.groupID != "GID" || opt.nodeID != "NID" {
		t.Fatalf("groupID=%q nodeID=%q", opt.groupID, opt.nodeID)
	}

	opt, err = parseArgs([]string{"role", "grant", "--scope", "AGENT_GROUP", "--scope-id", "GID"})
	if err != nil {
		t.Fatalf("role grant parse: %v", err)
	}
	if opt.scope != "AGENT_GROUP" || opt.scopeID != "GID" {
		t.Fatalf("scope=%q scopeID=%q", opt.scope, opt.scopeID)
	}
}

func TestCLI_BatchUsageAndParse(t *testing.T) {
	if !strings.Contains(usageText, "batch create") {
		t.Fatal("usage missing batch create")
	}
	if !strings.Contains(usageText, "batch replay-timeout") {
		t.Fatal("usage missing batch replay-timeout")
	}

	opt, err := parseArgs([]string{"batch", "create", "--type", "restart", "--process-id", "p1"})
	if err != nil {
		t.Fatalf("batch create parse: %v", err)
	}
	if len(opt.args) != 2 || opt.args[0] != "batch" || opt.args[1] != "create" {
		t.Fatalf("args=%v", opt.args)
	}
	if opt.batchType != "restart" {
		t.Fatalf("type=%q", opt.batchType)
	}
	if len(opt.processIDs) != 1 || opt.processIDs[0] != "p1" {
		t.Fatalf("processIDs=%v", opt.processIDs)
	}

	opt, err = parseArgs([]string{
		"batch", "export", "BID",
		"--format", "csv",
		"--process-group", "finance",
		"--agent-group-id", "g1",
	})
	if err != nil {
		t.Fatalf("batch export parse: %v", err)
	}
	if opt.format != "csv" {
		t.Fatalf("format=%q", opt.format)
	}
	if opt.processGroup != "finance" {
		t.Fatalf("processGroup=%q", opt.processGroup)
	}
	if opt.agentGroupID != "g1" {
		t.Fatalf("agentGroupID=%q", opt.agentGroupID)
	}

	opt, err = parseArgs([]string{
		"batch", "create", "--type", "start",
		"--process-name", "node-a:web",
		"--process-name", "node-b:api",
		"--process-id", "p1",
		"--process-id", "p2",
	})
	if err != nil {
		t.Fatalf("batch multi selector parse: %v", err)
	}
	if len(opt.processNames) != 2 || opt.processNames[0] != "node-a:web" || opt.processNames[1] != "node-b:api" {
		t.Fatalf("processNames=%v", opt.processNames)
	}
	if len(opt.processIDs) != 2 || opt.processIDs[0] != "p1" || opt.processIDs[1] != "p2" {
		t.Fatalf("processIDs=%v", opt.processIDs)
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
	c := newClient("127.0.0.1:9000", "op", "t", "", "")
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
	url, _ := startTestServer(t, false)
	return url
}

func newTestServerWithStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	return startTestServer(t, false)
}

func newClusterTestServer(t *testing.T) string {
	t.Helper()
	url, _ := startTestServer(t, true)
	return url
}

func startTestServer(t *testing.T, withCluster bool) (string, *store.Store) {
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
	return hs.URL, st
}
