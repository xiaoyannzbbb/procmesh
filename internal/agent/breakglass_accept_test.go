package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestAccept_BreakGlassReadsOnlyLocalOwnerWithoutClusterSession(t *testing.T) {
	root, err := os.MkdirTemp("", "pm-bg-")
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
	nodeID, err := st.GetOrCreateNodeID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []process.ProcessSpec{
		{ProcessID: "local-worker", Name: "local-worker", OwnerAgentID: nodeID, Command: "/bin/true", Instances: 1},
		{ProcessID: "remote-worker", Name: "remote-worker", OwnerAgentID: "another-agent", Command: "/bin/false", Instances: 1},
	} {
		if _, err := st.PutSpec(context.Background(), spec, 0, "seed", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutInstance(context.Background(), process.Instance{
		InstanceID: "local-worker:0",
		ProcessID:  "local-worker",
		Ordinal:    0,
		Desired:    process.DesiredStopped,
		Observed:   process.ObservedStopped,
		Health:     process.HealthUnknown,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(layout.LogDir, "local-worker", "local-worker:0", "stdout.log")
	if err := os.MkdirAll(filepath.Dir(stdoutPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdoutPath, []byte("break-glass-log-marker\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(root, "break-glass.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ready := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir:            root,
			Listen:             "127.0.0.1:0",
			GossipListen:       "127.0.0.1:0",
			RPCListen:          "127.0.0.1:0",
			ControlListen:      "127.0.0.1:0",
			BreakGlassSocket:   socketPath,
			OnBreakGlassListen: func(path string) { ready <- path },
		})
	}()
	select {
	case got := <-ready:
		if got != socketPath {
			t.Fatalf("socket=%q want %q", got, socketPath)
		}
	case err := <-errCh:
		t.Fatalf("agent exited before break-glass listen: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("break-glass listen timeout")
	}
	t.Setenv("PROCMESH_SESSION", filepath.Join(root, "missing-session"))

	code, listOut, errOut := runP1CLI("--break-glass="+socketPath, "process", "list")
	if code != 0 || !strings.Contains(listOut, "local-worker\t") || strings.Contains(listOut, "remote-worker") {
		t.Fatalf("list exit=%d stdout=%q stderr=%q", code, listOut, errOut)
	}
	code, getOut, errOut := runP1CLI("--break-glass="+socketPath, "process", "get", "local-worker")
	if code != 0 || !strings.Contains(getOut, "name\tlocal-worker\n") {
		t.Fatalf("get exit=%d stdout=%q stderr=%q", code, getOut, errOut)
	}
	code, logsOut, errOut := runP1CLI("--break-glass="+socketPath, "process", "logs", "local-worker", "--lines", "10")
	if code != 0 || !strings.Contains(logsOut, "break-glass-log-marker") {
		t.Fatalf("logs exit=%d stdout=%q stderr=%q", code, logsOut, errOut)
	}
	code, _, errOut = runP1CLI("--break-glass="+socketPath, "process", "get", "remote-worker")
	if code == 0 || !strings.Contains(errOut, "NOT_FOUND") {
		t.Fatalf("remote get exit=%d stderr=%q", code, errOut)
	}
	code, _, errOut = runP1CLI("--break-glass="+socketPath, "process", "logs", "remote-worker")
	if code == 0 || !strings.Contains(errOut, "NOT_FOUND") {
		t.Fatalf("remote logs exit=%d stderr=%q", code, errOut)
	}

	auditStore, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auditStore.Close() })
	events, err := auditStore.ListAuditAll(context.Background(), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	wantOutcomes := map[string]bool{
		"break_glass.process.list/success": false,
		"break_glass.process.get/success":  false,
		"break_glass.process.get/error":    false,
		"break_glass.process.logs/success": false,
		"break_glass.process.logs/error":   false,
	}
	for _, event := range events {
		key := event.Action + "/" + event.Result
		if _, ok := wantOutcomes[key]; ok && event.UserID != "" && event.Username != "" && event.Resource != "" && !event.Timestamp.IsZero() {
			wantOutcomes[key] = true
		}
	}
	for outcome, found := range wantOutcomes {
		if !found {
			t.Fatalf("missing break-glass audit outcome %s in %+v", outcome, events)
		}
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
	}
}
