package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQ1_FinanceOperatorCannotTouchAds(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	initAndLogin(t, addr)

	fin := writeSpecWithGroup(t, "pay", "finance")
	ads := writeSpecWithGroup(t, "ad", "ads")
	mustCLI(t, addr, "process", "apply", "--file", fin, "--expected-revision", "0")
	mustCLI(t, addr, "process", "apply", "--file", ads, "--expected-revision", "0")

	out := mustCLI(t, addr, "group", "create", "--name", "finance")
	gid := parseKV(out, "group_id")
	if gid == "" {
		t.Fatalf("missing group_id in %q", out)
	}
	nodeID := currentNodeID(t, addr)
	mustCLI(t, addr, "group", "add-member", "--group-id", gid, "--node-id", nodeID)

	out = mustCLI(t, addr, "user", "create", "--user", "finop", "--password", "finop-pass1")
	uid := parseKV(out, "user_id")
	if uid == "" {
		t.Fatalf("missing user_id in %q", out)
	}
	mustCLI(t, addr, "role", "grant", "--user-id", uid, "--role-id", "operator",
		"--scope", "PROCESS_GROUP", "--scope-id", "finance")

	mustCLI(t, addr, "login", "--user", "finop", "--password", "finop-pass1")

	code, out, errb := runP1CLI("--server", addr, "process", "list")
	if code != 0 {
		t.Fatalf("list: %s %s", errb, out)
	}
	if !listHasProcessName(out, "pay") || listHasProcessName(out, "ad") {
		t.Fatalf("list leaked ads: %s", out)
	}

	code, _, errb = runP1CLI("--server", addr, "process", "restart", "ad")
	if code == 0 || !strings.Contains(errb, "DENIED") {
		t.Fatalf("restart ads want DENIED, stderr=%q", errb)
	}

	code, _, errb = runP1CLI("--server", addr, "process", "restart", "pay")
	if code != 0 {
		t.Fatalf("restart pay: %s", errb)
	}
}

func TestQ1_AgentGroupScope(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	out := mustCLI(t, addr, "cluster", "init")
	adminPW := parseKV(out, "admin_password")
	if adminPW == "" {
		t.Fatalf("missing admin_password in %q", out)
	}
	loginAdmin(t, addr, adminPW)

	pay := writeSpecWithGroup(t, "pay", "finance")
	mustCLI(t, addr, "process", "apply", "--file", pay, "--expected-revision", "0")

	out = mustCLI(t, addr, "group", "create", "--name", "finance")
	gid := parseKV(out, "group_id")
	if gid == "" {
		t.Fatalf("missing group_id in %q", out)
	}
	nodeID := currentNodeID(t, addr)

	out = mustCLI(t, addr, "user", "create", "--user", "agop", "--password", "agop-pass1")
	uid := parseKV(out, "user_id")
	if uid == "" {
		t.Fatalf("missing user_id in %q", out)
	}
	mustCLI(t, addr, "role", "grant", "--user-id", uid, "--role-id", "operator",
		"--scope", "AGENT_GROUP", "--scope-id", gid)

	mustCLI(t, addr, "login", "--user", "agop", "--password", "agop-pass1")

	code, _, errb := runP1CLI("--server", addr, "process", "restart", "pay")
	if code == 0 || !strings.Contains(errb, "DENIED") {
		t.Fatalf("restart pay without member want DENIED, stderr=%q", errb)
	}

	// operator has no node.manage; add-member must run as admin
	loginAdmin(t, addr, adminPW)
	mustCLI(t, addr, "group", "add-member", "--group-id", gid, "--node-id", nodeID)

	mustCLI(t, addr, "login", "--user", "agop", "--password", "agop-pass1")
	code, _, errb = runP1CLI("--server", addr, "process", "restart", "pay")
	if code != 0 {
		t.Fatalf("restart pay after add-member: %s", errb)
	}
}

func writeSpecWithGroup(t *testing.T, name, group string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".yaml")
	body := fmt.Sprintf("name: %s\ncommand: /bin/sleep\nargs:\n  - \"60\"\ninstances: 1\ngroup: %s\n", name, group)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustCLI(t *testing.T, addr string, args ...string) string {
	t.Helper()
	all := append([]string{"--server", addr}, args...)
	code, out, errb := runP1CLI(all...)
	if code != 0 {
		t.Fatalf("%s: exit=%d stderr=%q stdout=%q", strings.Join(args, " "), code, errb, out)
	}
	return out
}

func currentNodeID(t *testing.T, addr string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var code int
	var out, errb string
	for time.Now().Before(deadline) {
		code, out, errb = runP1CLI("--server", addr, "node", "list")
		if code == 0 {
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				fields := strings.Split(line, "\t")
				if fields[0] != "" {
					return fields[0]
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("currentNodeID: exit=%d stderr=%q stdout=%q", code, errb, out)
	return ""
}

func listHasProcessName(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if fields[0] == name {
			return true
		}
	}
	return false
}
