package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestAccept_BreakGlassLifecycleWithoutQuorumOrClusterCredentials(t *testing.T) {
	leaderAddr, _, stopLeader := startClusterAgentCtl(t, "")
	ownerRoot, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ownerRoot) })
	socketPath := filepath.Join(ownerRoot, "break-glass.sock")
	ownerAddr, _ := startClusterAgentAtOpts(t, ownerRoot, Options{BreakGlassSocket: socketPath})
	password := joinTwoAndPassword(t, leaderAddr, ownerAddr)
	loginAdmin(t, ownerAddr, password)

	name := "break-glass-lifecycle"
	initialPID := prepareLoggedProcess(t, quorumCredential{}, ownerAddr, name)
	stopLeader()
	waitCredentialNoQuorum(t, quorumCredential{}, ownerAddr)
	t.Setenv("PROCMESH_SESSION", filepath.Join(ownerRoot, "missing-session"))

	runBreakGlassLifecycle(t, socketPath, "stop", name, "op-bg-stop")
	waitBreakGlassObserved(t, socketPath, name, "STOPPED", 0)
	runBreakGlassLifecycle(t, socketPath, "start", name, "op-bg-start")
	startedPID := waitBreakGlassObserved(t, socketPath, name, "RUNNING", initialPID)
	runBreakGlassLifecycle(t, socketPath, "restart", name, "op-bg-restart")
	restartedPID := waitBreakGlassObserved(t, socketPath, name, "RUNNING", startedPID)
	runBreakGlassLifecycle(t, socketPath, "restart", name, "op-bg-restart")
	time.Sleep(200 * time.Millisecond)
	if replayPID := waitBreakGlassObserved(t, socketPath, name, "RUNNING", 0); replayPID != restartedPID {
		t.Fatalf("idempotent replay restarted process: pid=%d want=%d", replayPID, restartedPID)
	}
	runBreakGlassLifecycle(t, socketPath, "kill", name, "op-bg-kill")
	waitBreakGlassObserved(t, socketPath, name, "STOPPED", 0)
	waitPIDGone(t, restartedPID)

	auditStore, err := store.Open(paths.New(ownerRoot).Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auditStore.Close() })
	events, err := auditStore.ListAuditAll(context.Background(), name, 50)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"break_glass.process.stop":    "op-bg-stop",
		"break_glass.process.start":   "op-bg-start",
		"break_glass.process.restart": "op-bg-restart",
		"break_glass.process.kill":    "op-bg-kill",
	}
	for _, event := range events {
		opID, ok := want[event.Action]
		if !ok || event.OperationID != opID || event.Result != "success" || event.TargetAgent == "" || event.UserID == "" || event.Username == "" || event.Timestamp.IsZero() {
			continue
		}
		var metadata map[string]any
		if json.Unmarshal(event.Metadata, &metadata) != nil {
			continue
		}
		processID, _ := metadata["process_id"].(string)
		if metadata["reason"] == "recover local service" && metadata["process_name"] == name && processID != "" && event.Resource == processID && metadata["error_code"] == "" && metadata["os_uid"] == float64(os.Geteuid()) {
			delete(want, event.Action)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing complete lifecycle audits %v in %+v", want, events)
	}
}

func TestAccept_BreakGlassEnablesDisabledUserWithoutClusterCredentials(t *testing.T) {
	root, err := os.MkdirTemp("", "pm-bg-user-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socketPath := filepath.Join(root, "break-glass.sock")
	addr, _ := startClusterAgentAtOpts(t, root, Options{BreakGlassSocket: socketPath})
	initAndLogin(t, addr)

	const username = "recovery-user"
	const password = "recovery-pass-ok"
	code, out, errOut := runP1CLI("--server", addr, "user", "create", "--user", username, "--password", password)
	if code != 0 {
		t.Fatalf("create user exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	userID := parseKV(out, "user_id")
	if userID == "" {
		t.Fatalf("missing user_id in %q", out)
	}
	code, out, errOut = runP1CLI("--server", addr, "user", "disable", userID)
	if code != 0 {
		t.Fatalf("disable user exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	code, _, errOut = runP1CLI("--server", addr, "login", "--user", username, "--password", password)
	if code == 0 || !strings.Contains(errOut, "user disabled") {
		t.Fatalf("disabled login exit=%d stderr=%q", code, errOut)
	}

	t.Setenv("PROCMESH_SESSION", filepath.Join(root, "missing-session"))
	code, out, errOut = runP1CLI(
		"--break-glass", socketPath,
		"--operation-id", "op-bg-enable-user",
		"--reason", "recover disabled user",
		"user", "enable", userID,
	)
	if code != 0 || !strings.Contains(out, "status=ACTIVE") {
		t.Fatalf("break-glass enable exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = runP1CLI("--server", addr, "login", "--user", username, "--password", password)
	if code != 0 {
		t.Fatalf("recovered login exit=%d stdout=%q stderr=%q", code, out, errOut)
	}

	auditStore, err := store.Open(paths.New(root).Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auditStore.Close() })
	events, err := auditStore.ListAuditAll(context.Background(), userID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Action == "break_glass.user.enable" && event.OperationID == "op-bg-enable-user" && event.Result == "success" {
			return
		}
	}
	t.Fatalf("missing break-glass user enable audit in %+v", events)
}

func runBreakGlassLifecycle(t *testing.T, socketPath, action, name, operationID string) {
	t.Helper()
	code, out, errOut := runP1CLI(
		"--break-glass", socketPath,
		"--operation-id", operationID,
		"--reason", "recover local service",
		"process", action, name,
	)
	if code != 0 {
		t.Fatalf("break-glass %s exit=%d stdout=%q stderr=%q", action, code, out, errOut)
	}
}

func waitBreakGlassObserved(t *testing.T, socketPath, name, observed string, differentPID int32) int32 {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		code, out, errOut := runP1CLI("--break-glass", socketPath, "process", "get", name)
		last = out + errOut
		if code == 0 {
			for _, line := range strings.Split(out, "\n") {
				fields := strings.Split(line, "\t")
				if len(fields) != 7 || fields[0] != "instance" || fields[4] != observed {
					continue
				}
				pid, err := strconv.ParseInt(fields[6], 10, 32)
				if err == nil && (observed != "RUNNING" || pid > 0 && int32(pid) != differentPID) {
					return int32(pid)
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("break-glass process %s did not reach %s: %q", name, observed, last)
	return 0
}

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
	code, _, errOut = runP1CLI(
		"--break-glass="+socketPath,
		"--operation-id", "op-remote-owner-denied",
		"--reason", "recover local service",
		"process", "stop", "remote-worker",
	)
	if code == 0 || !strings.Contains(errOut, "NOT_FOUND") {
		t.Fatalf("remote stop exit=%d stderr=%q", code, errOut)
	}
	code, _, errOut = runP1CLI("--break-glass="+socketPath, "process", "delete", "local-worker")
	if code != 2 || !strings.Contains(errOut, "only supports process list, get, logs, start, stop, restart, and kill") {
		t.Fatalf("forbidden delete exit=%d stderr=%q", code, errOut)
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
		"break_glass.process.stop/error":   false,
	}
	var remoteStopAudited bool
	for _, event := range events {
		key := event.Action + "/" + event.Result
		if _, ok := wantOutcomes[key]; ok && event.UserID != "" && event.Username != "" && event.Resource != "" && !event.Timestamp.IsZero() {
			wantOutcomes[key] = true
		}
		if event.Action == "break_glass.process.stop" && event.OperationID == "op-remote-owner-denied" && event.Resource == "remote-worker" {
			var metadata map[string]any
			if json.Unmarshal(event.Metadata, &metadata) == nil && metadata["process_id"] == "remote-worker" && metadata["process_name"] == "remote-worker" && metadata["error_code"] == "NOT_FOUND" {
				remoteStopAudited = true
			}
		}
	}
	for outcome, found := range wantOutcomes {
		if !found {
			t.Fatalf("missing break-glass audit outcome %s in %+v", outcome, events)
		}
	}
	if !remoteStopAudited {
		t.Fatalf("missing complete remote stop denial audit in %+v", events)
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
