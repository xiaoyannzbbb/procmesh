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
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
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

func TestCLI_BreakGlassModeIsExplicitAndRestricted(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "server",
			args: []string{"--break-glass=/tmp/procmesh.sock", "--server", "127.0.0.1:18680", "process", "list"},
			want: "cannot be combined with --server",
		},
		{
			name: "remote node",
			args: []string{"--break-glass=/tmp/procmesh.sock", "--node", "node-b", "process", "list"},
			want: "does not accept --node",
		},
		{
			name: "cluster credential",
			args: []string{"--break-glass=/tmp/procmesh.sock", "--auth-token", "secret", "process", "list"},
			want: "does not accept cluster credentials",
		},
		{
			name: "apply",
			args: []string{"--break-glass=/tmp/procmesh.sock", "--operation-id", "op-apply", "--reason", "recover service", "process", "apply"},
			want: "only supports process list, get, logs, start, stop, restart, and kill",
		},
		{
			name: "delete",
			args: []string{"--break-glass=/tmp/procmesh.sock", "process", "delete", "worker"},
			want: "only supports process list, get, logs, start, stop, restart, and kill",
		},
		{
			name: "adopt",
			args: []string{"--break-glass=/tmp/procmesh.sock", "process", "adopt", "worker:0"},
			want: "only supports process list, get, logs, start, stop, restart, and kill",
		},
		{
			name: "configuration",
			args: []string{"--break-glass=/tmp/procmesh.sock", "process", "history", "worker"},
			want: "only supports process list, get, logs, start, stop, restart, and kill",
		},
		{
			name: "backup",
			args: []string{"--break-glass=/tmp/procmesh.sock", "backup", "list"},
			want: "only supports process list, get, logs, start, stop, restart, and kill",
		},
		{
			name: "restore",
			args: []string{"--break-glass=/tmp/procmesh.sock", "backup", "restore", "snapshot"},
			want: "only supports process list, get, logs, start, stop, restart, and kill",
		},
		{
			name: "batch",
			args: []string{"--break-glass=/tmp/procmesh.sock", "batch", "list"},
			want: "only supports process list, get, logs, start, stop, restart, and kill",
		},
		{
			name: "control plane",
			args: []string{"--break-glass=/tmp/procmesh.sock", "cluster", "init"},
			want: "only supports process list, get, logs, start, stop, restart, and kill",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errb := runCLI(tc.args...)
			if code != 2 || !strings.Contains(errb, tc.want) {
				t.Fatalf("exit=%d stderr=%q, want usage error containing %q", code, errb, tc.want)
			}
		})
	}
}

func TestCLI_BreakGlassLifecycleRequiresExplicitOperationIDAndReason(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "kill"} {
		t.Run(action+" missing operation ID", func(t *testing.T) {
			code, _, errb := runCLI(
				"--break-glass=/tmp/procmesh.sock",
				"--reason", "recover service",
				"process", action, "worker",
			)
			if code != 2 || !strings.Contains(errb, "requires --operation-id") {
				t.Fatalf("exit=%d stderr=%q", code, errb)
			}
		})

		t.Run(action+" missing reason", func(t *testing.T) {
			code, _, errb := runCLI(
				"--break-glass=/tmp/procmesh.sock",
				"--operation-id", "op-"+action,
				"process", action, "worker",
			)
			if code != 2 || !strings.Contains(errb, "requires --reason") {
				t.Fatalf("exit=%d stderr=%q", code, errb)
			}
		})

		t.Run(action+" accepts required fields", func(t *testing.T) {
			code, _, errb := runCLI(
				"--break-glass=/tmp/procmesh-missing.sock",
				"--operation-id", "op-"+action,
				"--reason", "recover service",
				"process", action, "worker",
			)
			if code == 2 {
				t.Fatalf("valid lifecycle command rejected as usage error: %q", errb)
			}
		})
	}
}

func TestCLI_BreakGlassUserEnableRequiresExplicitOperationIDAndReason(t *testing.T) {
	code, _, errb := runCLI(
		"--break-glass=/tmp/procmesh.sock",
		"--reason", "recover administrator",
		"user", "enable", "user-admin",
	)
	if code != 2 || !strings.Contains(errb, "requires --operation-id") {
		t.Fatalf("missing operation ID exit=%d stderr=%q", code, errb)
	}

	code, _, errb = runCLI(
		"--break-glass=/tmp/procmesh.sock",
		"--operation-id", "op-enable-admin",
		"user", "enable", "user-admin",
	)
	if code != 2 || !strings.Contains(errb, "requires --reason") {
		t.Fatalf("missing reason exit=%d stderr=%q", code, errb)
	}

	code, _, errb = runCLI(
		"--break-glass=/tmp/procmesh-missing.sock",
		"--operation-id", "op-enable-admin",
		"--reason", "recover administrator",
		"user", "enable", "user-admin",
	)
	if code == 2 {
		t.Fatalf("valid recovery command rejected as usage error: %q", errb)
	}
}

func TestCLI_ReasonIsBreakGlassOnly(t *testing.T) {
	code, _, errb := runCLI("--reason", "recover service", "process", "list")
	if code != 2 || !strings.Contains(errb, "only valid with --break-glass") {
		t.Fatalf("exit=%d stderr=%q", code, errb)
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

func TestCLI_ParseTTLDuration(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{raw: "12h", want: 12 * time.Hour},
		{raw: "7d", want: 7 * 24 * time.Hour},
		{raw: "7d12h", want: 7*24*time.Hour + 12*time.Hour},
		{raw: "90m", want: 90 * time.Minute},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			opt, err := parseArgs([]string{"login", "--ttl", tc.raw})
			if err != nil {
				t.Fatal(err)
			}
			if !opt.ttlSet || opt.ttl != tc.want {
				t.Fatalf("ttlSet=%v ttl=%s want %s", opt.ttlSet, opt.ttl, tc.want)
			}
		})
	}

	if _, err := parseArgs([]string{"login", "--ttl", "nope"}); err == nil || !strings.Contains(err.Error(), "invalid --ttl") {
		t.Fatalf("err=%v", err)
	}
	opt, err := parseArgs([]string{"login", "--user", "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.ttlSet {
		t.Fatal("ttl must be unset by default")
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

func TestCLI_AlertUsageAndParse(t *testing.T) {
	if !strings.Contains(usageText, "alert list") {
		t.Fatal("usage missing alert list")
	}
	if !strings.Contains(usageText, "alert get ALERT_ID") {
		t.Fatal("usage missing alert get")
	}
	if !strings.Contains(usageText, "alert channel list") {
		t.Fatal("usage missing alert channel list")
	}
	if !strings.Contains(usageText, "alert channel put") {
		t.Fatal("usage missing alert channel put")
	}
	if !strings.Contains(usageText, "alert policy get") {
		t.Fatal("usage missing alert policy get")
	}
	if !strings.Contains(usageText, "alert policy put") {
		t.Fatal("usage missing alert policy put")
	}

	code, _, errb := runCLI("alert")
	if code != 2 {
		t.Fatalf("alert without subcommand exit=%d stderr=%q", code, errb)
	}
	if strings.Contains(errb, "unknown command") {
		t.Fatalf("alert must not be unknown command: %q", errb)
	}

	opt, err := parseArgs([]string{"alert", "list", "--state", "FIRING"})
	if err != nil {
		t.Fatalf("alert list parse: %v", err)
	}
	if len(opt.args) != 2 || opt.args[0] != "alert" || opt.args[1] != "list" {
		t.Fatalf("args=%v", opt.args)
	}
	if opt.state != "FIRING" {
		t.Fatalf("state=%q", opt.state)
	}

	opt, err = parseArgs([]string{
		"alert", "channel", "put",
		"--type", "WEBHOOK",
		"--name", "hook",
		"--id", "ch1",
		"--enabled", "true",
		"--config", `{"url":"https://example.com"}`,
	})
	if err != nil {
		t.Fatalf("channel put parse: %v", err)
	}
	if opt.batchType != "WEBHOOK" || opt.name != "hook" || opt.id != "ch1" {
		t.Fatalf("type=%q name=%q id=%q", opt.batchType, opt.name, opt.id)
	}
	if !opt.enabled || !opt.enabledSet {
		t.Fatalf("enabled=%v set=%v", opt.enabled, opt.enabledSet)
	}
	if opt.config != `{"url":"https://example.com"}` {
		t.Fatalf("config=%q", opt.config)
	}

	opt, err = parseArgs([]string{
		"alert", "policy", "put",
		"--dedup-window-sec", "600",
		"--notify-on-resolve", "false",
		"--cpu", "80",
		"--memory", "85",
		"--disk", "90",
		"--consecutive", "3",
		"--suspect-too-long-sec", "120",
	})
	if err != nil {
		t.Fatalf("policy put parse: %v", err)
	}
	if opt.dedupWindowSec != 600 || !opt.dedupWindowSet {
		t.Fatalf("dedup=%d set=%v", opt.dedupWindowSec, opt.dedupWindowSet)
	}
	if opt.notifyOnResolve || !opt.notifyOnResolveSet {
		t.Fatalf("notify=%v set=%v", opt.notifyOnResolve, opt.notifyOnResolveSet)
	}
	if opt.cpuHigh != 80 || opt.memoryHigh != 85 || opt.diskHigh != 90 {
		t.Fatalf("cpu=%d mem=%d disk=%d", opt.cpuHigh, opt.memoryHigh, opt.diskHigh)
	}
	if opt.consecutive != 3 || opt.suspectTooLongSec != 120 {
		t.Fatalf("consecutive=%d suspect=%d", opt.consecutive, opt.suspectTooLongSec)
	}
}

func TestCLI_AlertChannelPutEnabledDefault(t *testing.T) {
	opt, err := parseArgs([]string{"alert", "channel", "put", "--type", "WEBHOOK", "--name", "hook"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opt.enabledSet {
		t.Fatal("expected --enabled omitted")
	}
	if !channelPutEnabled(opt) {
		t.Fatal("omitted --enabled must send Enabled=true")
	}

	opt, err = parseArgs([]string{"alert", "channel", "put", "--type", "WEBHOOK", "--name", "hook", "--enabled", "false"})
	if err != nil {
		t.Fatalf("parse false: %v", err)
	}
	if !opt.enabledSet || opt.enabled {
		t.Fatalf("enabled=%v set=%v", opt.enabled, opt.enabledSet)
	}
	if channelPutEnabled(opt) {
		t.Fatal("explicit --enabled false must send Enabled=false")
	}

	opt, err = parseArgs([]string{"alert", "channel", "put", "--type", "WEBHOOK", "--name", "hook", "--enabled", "true"})
	if err != nil {
		t.Fatalf("parse true: %v", err)
	}
	if !channelPutEnabled(opt) {
		t.Fatal("explicit --enabled true must send Enabled=true")
	}
}

func TestMain_BackupUsage(t *testing.T) {
	wantLines := []string{
		"backup create --sink=fs|s3|peer [--process-id ID]... [--peer-node ID]...",
		"backup list [--sink S] [--peer-node ID]... [--include-s3]",
		"backup get SNAPSHOT_ID [--sink S] [--source-node ID] [--payload]",
		"backup delete SNAPSHOT_ID --sink S",
		"backup restore SNAPSHOT_ID --sink S --process-id ID --expected-revision N [--source-node ID]",
	}
	for _, line := range wantLines {
		if !strings.Contains(usageText, line) {
			t.Fatalf("usage missing %q", line)
		}
	}

	code, _, errb := runCLI("backup")
	if code != 2 {
		t.Fatalf("backup without subcommand exit=%d stderr=%q", code, errb)
	}
	if strings.Contains(errb, "unknown command") {
		t.Fatalf("backup must not be unknown command: %q", errb)
	}

	code, _, errb = runCLI("backup", "nope")
	if code != 2 {
		t.Fatalf("unknown backup subcommand exit=%d stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "unknown backup command") {
		t.Fatalf("stderr=%q", errb)
	}
}

func TestParseArgs_BackupFlags(t *testing.T) {
	opt, err := parseArgs([]string{
		"backup", "create",
		"--sink", "peer",
		"--process-id", "p1",
		"--process-id", "p2",
		"--peer-node", "n1",
		"--peer-node", "n2",
	})
	if err != nil {
		t.Fatalf("create parse: %v", err)
	}
	if len(opt.args) != 2 || opt.args[0] != "backup" || opt.args[1] != "create" {
		t.Fatalf("args=%v", opt.args)
	}
	if opt.sink != "peer" {
		t.Fatalf("sink=%q", opt.sink)
	}
	if len(opt.processIDs) != 2 || opt.processIDs[0] != "p1" || opt.processIDs[1] != "p2" {
		t.Fatalf("processIDs=%v", opt.processIDs)
	}
	if len(opt.peerNodes) != 2 || opt.peerNodes[0] != "n1" || opt.peerNodes[1] != "n2" {
		t.Fatalf("peerNodes=%v", opt.peerNodes)
	}

	opt, err = parseArgs([]string{"backup", "list", "--sink", "fs", "--include-s3", "--peer-node", "peer-a"})
	if err != nil {
		t.Fatalf("list parse: %v", err)
	}
	if !opt.includeS3 {
		t.Fatal("include-s3 presence flag")
	}
	if opt.sink != "fs" || len(opt.peerNodes) != 1 || opt.peerNodes[0] != "peer-a" {
		t.Fatalf("sink=%q peerNodes=%v", opt.sink, opt.peerNodes)
	}

	opt, err = parseArgs([]string{"backup", "get", "snap-1", "--source-node", "node-b", "--payload"})
	if err != nil {
		t.Fatalf("get parse: %v", err)
	}
	if opt.sourceNode != "node-b" || !opt.payload {
		t.Fatalf("sourceNode=%q payload=%v", opt.sourceNode, opt.payload)
	}

	opt, err = parseArgs([]string{"backup", "get", "snap-1", "--payload=false"})
	if err != nil {
		t.Fatalf("payload=false parse: %v", err)
	}
	if opt.payload {
		t.Fatal("payload=false must clear payload")
	}

	opt, err = parseArgs([]string{
		"backup", "restore", "snap-1",
		"--sink", "fs",
		"--process-id", "p1",
		"--expected-revision", "3",
		"--source-node", "s3",
	})
	if err != nil {
		t.Fatalf("restore parse: %v", err)
	}
	if opt.sink != "fs" || !opt.expectedSet || opt.expectedRevision != 3 {
		t.Fatalf("sink=%q expected=%d set=%v", opt.sink, opt.expectedRevision, opt.expectedSet)
	}
	if len(opt.processIDs) != 1 || opt.processIDs[0] != "p1" {
		t.Fatalf("processIDs=%v", opt.processIDs)
	}
	if backupSourceNodeID(opt) != "" {
		t.Fatalf("source-node s3 must be omitted for hop, got %q", backupSourceNodeID(opt))
	}
}

func TestPrintBackupEntry_STALEPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	printBackupEntry(&buf, &procmeshv1.BackupEntry{
		SourceNode: "peer-x",
		Freshness:  "STALE",
	})
	line := buf.String()
	if !strings.Contains(line, "STALE") {
		t.Fatalf("want STALE uppercase, got %q", line)
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
	c := newClient("127.0.0.1:18680", "op", "t", "", "")
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
					APIAddress:      "127.0.0.1:18680",
					GossipAddress:   "127.0.0.1:18689",
				}
			},
			GossipAddr: func() string { return "127.0.0.1:18689" },
			Now:        time.Now,
			NodeID:     nodeID,
			Hostname:   "test-host",
			BootID:     boot,
			APIAddr:    "127.0.0.1:18680",
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
