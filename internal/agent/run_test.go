package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/localhttp"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestRun_LogsLifecycle(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	listened := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:       dir,
			Listen:        "127.0.0.1:0",
			GossipListen:  "127.0.0.1:0",
			RPCListen:     "127.0.0.1:0",
			ControlListen: "127.0.0.1:0",
			Logger:        logger,
			OnListen:      func(addr string) { listened <- addr },
		})
	}()
	var httpAddr string
	select {
	case httpAddr = <-listened:
	case err := <-errCh:
		t.Fatalf("Run exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for listen")
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	got := out.String()
	for _, want := range []string{
		"agent starting",
		"gossip listening",
		"http listening",
		"agent started",
		"agent stopping",
		"agent stopped",
		httpAddr,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestAgentLoopCadence_ThrottlesMaintenance(t *testing.T) {
	base := time.Unix(2_000_000, 0)
	var cadence agentLoopCadence

	first := cadence.due(base)
	if !first.diskProtect || !first.logRotate || !first.backupSchedule {
		t.Fatalf("first tick must run all maintenance: %+v", first)
	}
	second := cadence.due(base.Add(time.Second))
	if second.diskProtect || second.logRotate || second.backupSchedule {
		t.Fatalf("1s tick repeated maintenance: %+v", second)
	}
	fiveSeconds := cadence.due(base.Add(5 * time.Second))
	if !fiveSeconds.diskProtect || fiveSeconds.logRotate || !fiveSeconds.backupSchedule {
		t.Fatalf("5s cadence mismatch: %+v", fiveSeconds)
	}
	tenSeconds := cadence.due(base.Add(10 * time.Second))
	if !tenSeconds.diskProtect || !tenSeconds.logRotate || !tenSeconds.backupSchedule {
		t.Fatalf("10s cadence mismatch: %+v", tenSeconds)
	}
}

func TestResolveAdvertiseAddr_InheritsListenPort(t *testing.T) {
	tests := []struct {
		name      string
		listen    string
		advertise string
		want      string
	}{
		{name: "ipv4", listen: "0.0.0.0:18689", advertise: "209.50.255.237", want: "209.50.255.237:18689"},
		{name: "hostname", listen: "0.0.0.0:18683", advertise: "agent.example.com", want: "agent.example.com:18683"},
		{name: "ipv6", listen: "[::]:18685", advertise: "2001:db8::1", want: "[2001:db8::1]:18685"},
		{name: "explicit port", listen: "0.0.0.0:18689", advertise: "209.50.255.237:30123", want: "209.50.255.237:30123"},
		{name: "dynamic port", listen: "127.0.0.1:0", advertise: "127.0.0.2:0", want: "127.0.0.2:0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAdvertiseAddr(tt.listen, tt.advertise)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("resolveAdvertiseAddr(%q, %q) = %q, want %q", tt.listen, tt.advertise, got, tt.want)
			}
		})
	}
}

func TestResolveAdvertiseAddr_RejectsMalformedHostPort(t *testing.T) {
	if _, err := resolveAdvertiseAddr("0.0.0.0:18689", "209.50.255.237:bad"); err == nil {
		t.Fatal("expected malformed advertise address error")
	}
}

func TestResolveAPIAdvertise(t *testing.T) {
	tests := []struct {
		name         string
		bound        string
		configured   string
		rpcAdvertise string
		want         string
	}{
		{
			name:         "explicit address wins",
			bound:        "[::]:18680",
			configured:   "api.example.com",
			rpcAdvertise: "10.0.0.1:18683",
			want:         "api.example.com:18680",
		},
		{
			name:         "wildcard inherits rpc host",
			bound:        "[::]:18680",
			rpcAdvertise: "10.0.0.1:18683",
			want:         "10.0.0.1:18680",
		},
		{
			name:         "concrete bind remains advertised",
			bound:        "127.0.0.1:18680",
			rpcAdvertise: "10.0.0.1:18683",
			want:         "127.0.0.1:18680",
		},
		{
			name:  "wildcard without fallback remains explicit",
			bound: "0.0.0.0:18680",
			want:  "0.0.0.0:18680",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAPIAdvertise(tt.bound, tt.configured, tt.rpcAdvertise)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("resolveAPIAdvertise(%q, %q, %q) = %q, want %q", tt.bound, tt.configured, tt.rpcAdvertise, got, tt.want)
			}
		})
	}
}

func TestLookUser_RejectsOtherUserWithoutRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	me := os.Getuid()
	var name string
	for _, cand := range []string{"nobody", "daemon", "www-data"} {
		u, err := user.Lookup(cand)
		if err != nil {
			continue
		}
		uid, err := strconv.Atoi(u.Uid)
		if err != nil || uid == me {
			continue
		}
		name = cand
		break
	}
	if name == "" {
		t.Skip("no other existing user")
	}
	err := lookupUser(name)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("LookUser(%q) want INVALID, got %v", name, err)
	}
}

func TestCheckListen_LoopbackOK(t *testing.T) {
	if err := CheckListen("127.0.0.1:18680", false); err != nil {
		t.Fatal(err)
	}
	if err := CheckListen("localhost:18680", false); err != nil {
		t.Fatal(err)
	}
}

func TestCheckListen_NonLoopbackRequiresFlag(t *testing.T) {
	err := CheckListen("0.0.0.0:18680", false)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
	if err := CheckListen("0.0.0.0:18680", true); err != nil {
		t.Fatal(err)
	}
}

func TestRun_CorruptDBAtOpenStillServesReadyz503(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "store.db"), []byte("not-a-sqlite-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	got := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:       root,
			Listen:        "127.0.0.1:0",
			GossipListen:  "127.0.0.1:0",
			RPCListen:     "127.0.0.1:0",
			ControlListen: "127.0.0.1:0",
			OnListen:      func(addr string) { got <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-got:
	case err := <-errCh:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for listen")
	}
	res, err := http.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 503 || string(body) != "DEGRADED" {
		t.Fatalf("readyz want 503 DEGRADED, got %d %q", res.StatusCode, body)
	}
	res, err = http.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz %v %v", err, res)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRun_ReconcilesImmediatelyAfterRecover(t *testing.T) {
	root, err := os.MkdirTemp("", "pm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	ctxSeed := context.Background()
	if _, err := st.GetOrCreateNodeID(ctxSeed); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBootID(ctxSeed, paths.CurrentBootID()); err != nil {
		t.Fatal(err)
	}
	spec := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "sleep",
		Command:   "/bin/sleep",
		Args:      []string{"60"},
		Instances: 1,
		Autostart: true,
	}
	if _, err := st.PutSpec(ctxSeed, spec, 0, "t", ""); err != nil {
		t.Fatal(err)
	}
	inst := process.Instance{
		InstanceID: process.MakeInstanceID("p1", 0),
		ProcessID:  "p1",
		Ordinal:    0,
		Desired:    process.DesiredRunning,
		Observed:   process.ObservedStopped,
		Health:     process.HealthUnknown,
		BootID:     paths.CurrentBootID(),
	}
	if err := st.PutInstance(ctxSeed, inst); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	got := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:       root,
			Listen:        "127.0.0.1:0",
			GossipListen:  "127.0.0.1:0",
			RPCListen:     "127.0.0.1:0",
			ControlListen: "127.0.0.1:0",
			ShimBin:       testShimBin,
			OnListen:      func(addr string) { got <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-got:
	case err := <-errCh:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("timeout waiting for listen")
	}
	res, err := http.Get("http://" + addr + "/v1/processes")
	if err != nil {
		t.Fatal(err)
	}
	var listed localhttp.ListProcessesResponse
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	var pid int
	for _, p := range listed.Processes {
		for _, in := range p.Instances {
			if in.PID > 0 {
				pid = in.PID
			}
		}
	}
	if pid <= 0 {
		t.Fatalf("Run must Reconcile before ticker, got %+v", listed)
	}
	t.Cleanup(func() { cleanupDataDir(root) })
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRun_BlocksUntilCancelAndServesHealthz(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	got := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:       dir,
			Listen:        "127.0.0.1:0",
			GossipListen:  "127.0.0.1:0",
			RPCListen:     "127.0.0.1:0",
			ControlListen: "127.0.0.1:0",
			OnListen:      func(addr string) { got <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-got:
	case err := <-errCh:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for listen")
	}
	res, err := http.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz %v %v", err, res)
	}
	res, err = http.Get("http://" + addr + "/readyz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("readyz %v %v", err, res)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
