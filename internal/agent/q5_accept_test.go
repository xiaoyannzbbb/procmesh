package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQ5_RestoreWrongRevisionConflict(t *testing.T) {
	addr, _ := startQ5Agent(t, 10)
	initAndLogin(t, addr)

	mustCLI(t, addr, "process", "apply", "--file", writeBackupSpec(t, "q5p", "/bin/true"), "--expected-revision", "0")
	snapID := mustBackupCreateFS(t, addr, "q5p")

	mustCLI(t, addr, "process", "apply", "--file", writeBackupSpec(t, "q5p", "/bin/sleep"), "--expected-revision", "1")
	if cmd, rev := mustProcessCommandRev(t, addr, "q5p"); cmd != "/bin/sleep" || rev != 2 {
		t.Fatalf("pre-restore want command=/bin/sleep rev=2, got %s rev=%d", cmd, rev)
	}

	code, out, errb := runCLIExit(t, addr, "backup", "restore", snapID,
		"--sink", "fs", "--process-id", "q5p", "--expected-revision", "1")
	if code == 0 {
		t.Fatalf("restore with stale expected-revision must fail, stdout=%q", out)
	}
	if !strings.Contains(strings.ToUpper(out+errb), "CONFLICT") {
		t.Fatalf("want CONFLICT, stdout=%q stderr=%q", out, errb)
	}

	cmd, rev := mustProcessCommandRev(t, addr, "q5p")
	if cmd != "/bin/sleep" {
		t.Fatalf("command must stay changed, not snapshot rewrite, got %q", cmd)
	}
	if rev != 2 {
		t.Fatalf("revision must not roll back, got %d", rev)
	}
}

func TestQ5_RestoreAppliesNewRevision(t *testing.T) {
	addr, _ := startQ5Agent(t, 10)
	initAndLogin(t, addr)

	mustCLI(t, addr, "process", "apply", "--file", writeBackupSpec(t, "q5r", "/bin/true"), "--expected-revision", "0")
	snapID := mustBackupCreateFS(t, addr, "q5r")

	mustCLI(t, addr, "process", "apply", "--file", writeBackupSpec(t, "q5r", "/bin/sleep"), "--expected-revision", "1")
	if _, rev := mustProcessCommandRev(t, addr, "q5r"); rev != 2 {
		t.Fatalf("pre-restore want rev=2, got %d", rev)
	}

	out := mustCLI(t, addr, "backup", "restore", snapID,
		"--sink", "fs", "--process-id", "q5r", "--expected-revision", "2")
	if !strings.Contains(out, "SUCCESS") {
		t.Fatalf("want SUCCESS restore, got %s", out)
	}

	cmd, rev := mustProcessCommandRev(t, addr, "q5r")
	if cmd != "/bin/true" {
		t.Fatalf("command must return to snapshot, got %q", cmd)
	}
	if rev != 3 {
		t.Fatalf("latest must become 3, got %d", rev)
	}
}

func TestQ5_PeerPutDoesNotApplyOnReceiver(t *testing.T) {
	addrA, rootA := startQ5Agent(t, 10)
	addrC, rootC := startQ5Agent(t, 10)
	joinTwo(t, addrA, addrC)
	idA := readNodeID(t, rootA)
	idC := readNodeID(t, rootC)

	mustCLI(t, addrA, "process", "apply", "--file", writeBackupSpec(t, "q5peer", "/bin/true"), "--expected-revision", "0")
	mustPeerBackupCreate(t, addrA, idC, "q5peer")

	matches, err := filepath.Glob(filepath.Join(rootC, "backup", "peer", idA, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatalf("want backup/peer/%s/*.json on C under %s", idA, rootC)
	}

	listC := mustCLI(t, addrC, "process", "list")
	if listHasProcessName(listC, "q5peer") {
		t.Fatalf("C must not apply A's process, list=%s", listC)
	}
}

func TestQ5_ListMarksUnreachablePeerSTALE(t *testing.T) {
	addrA, _ := startQ5Agent(t, 10)
	addrC, rootC, cancelC := startQ5AgentCtl(t, 10)
	joinTwo(t, addrA, addrC)
	nidC := readNodeID(t, rootC)
	cancelC()
	out := mustCLI(t, addrA, "backup", "list", "--peer-node", nidC)
	if !strings.Contains(strings.ToUpper(out), "STALE") {
		t.Fatalf("want STALE, got %s", out)
	}
}

func TestQ5_Disk95StopsBackupWrites(t *testing.T) {
	// Inject DiskPercent=95 (host disk may already be 95%; Engine hook must
	// still reject). Unit coverage: go test ./internal/backup -run TestEngine_Disk95
	// (TestEngine_Disk95RejectsCreate).
	addr, _ := startQ5Agent(t, 95)
	initAndLogin(t, addr)
	mustCLI(t, addr, "process", "apply", "--file", writeBackupSpec(t, "q5disk", "/bin/true"), "--expected-revision", "0")
	code, out, errb := runCLIExit(t, addr, "backup", "create", "--sink", "fs", "--process-id", "q5disk")
	if code == 0 {
		t.Fatalf("create at disk 95 must fail, stdout=%q", out)
	}
	if !strings.Contains(strings.ToUpper(errb+out), "DEGRADED") {
		t.Fatalf("want DEGRADED, stdout=%q stderr=%q", out, errb)
	}

	// Same Engine path with disk <95: CLI create succeeds (run-loop wiring).
	addrOK, _ := startQ5Agent(t, 10)
	initAndLogin(t, addrOK)
	mustCLI(t, addrOK, "process", "apply", "--file", writeBackupSpec(t, "q5ok", "/bin/true"), "--expected-revision", "0")
	ok := mustCLI(t, addrOK, "backup", "create", "--sink", "fs", "--process-id", "q5ok")
	if parseKV(ok, "snapshot_id") == "" {
		t.Fatalf("want successful create below 95, got %s", ok)
	}
}

func startQ5Agent(t *testing.T, disk float64) (addr, root string) {
	addr, root, _ = startQ5AgentCtl(t, disk)
	return addr, root
}

func startQ5AgentCtl(t *testing.T, disk float64) (addr, root string, cancel context.CancelFunc) {
	t.Helper()
	root, err := os.MkdirTemp("", "pm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	addr, cancel = startClusterAgentAtOpts(t, root, Options{
		DiskPercent: func() float64 { return disk },
	})
	return addr, root, cancel
}

func writeBackupSpec(t *testing.T, name, command string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".yaml")
	body := "name: " + name + "\nprocess_id: " + name + "\ncommand: " + command + "\ninstances: 1\nrestart:\n  mode: never\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustBackupCreateFS(t *testing.T, addr, processID string) string {
	t.Helper()
	out := mustCLI(t, addr, "backup", "create", "--sink", "fs", "--process-id", processID)
	id := parseKV(out, "snapshot_id")
	if id == "" {
		t.Fatalf("missing snapshot_id in %q", out)
	}
	return id
}

func mustPeerBackupCreate(t *testing.T, addr, peerNode, processID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var code int
	var out, errb string
	for time.Now().Before(deadline) {
		code, out, errb = runCLIExit(t, addr, "backup", "create", "--sink", "peer", "--peer-node", peerNode, "--process-id", processID)
		if code == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("peer create: exit=%d stderr=%q stdout=%q", code, errb, out)
}

func mustProcessCommandRev(t *testing.T, addr, name string) (command string, revision int64) {
	t.Helper()
	got := mustCLI(t, addr, "process", "get", name)
	command = processGetField(got, "command")
	rev := processGetField(got, "revision")
	n, err := strconv.ParseInt(rev, 10, 64)
	if err != nil {
		t.Fatalf("revision %q in %s", rev, got)
	}
	return command, n
}

func processGetField(out, key string) string {
	prefix := key + "\t"
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}
