package agent

import (
	"strings"
	"testing"
)

func TestAccept_LoginThroughFollowerImmediatelyManagesLocalProcess(t *testing.T) {
	leaderAddr, _ := startClusterAgent(t, "")
	followerAddr, _ := startClusterAgent(t, "")
	password := joinTwoAndPassword(t, leaderAddr, followerAddr)

	loginAdmin(t, followerAddr, password)
	spec := writeSleepSpecNamed(t, "follower-sleep")
	if code, out, errb := runP1CLI("--server", followerAddr, "process", "apply", "--file", spec, "--expected-revision", "0"); code != 0 {
		t.Fatalf("apply through follower exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	if code, out, errb := runP1CLI("--server", followerAddr, "process", "start", "follower-sleep"); code != 0 {
		t.Fatalf("start through follower exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	waitObserved(t, followerAddr, "follower-sleep", "RUNNING")

	code, out, errb := runP1CLI("--server", followerAddr, "process", "list")
	if code != 0 {
		t.Fatalf("list through follower exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	if !strings.Contains(out, "follower-sleep") {
		t.Fatalf("follower local process missing from list: %q", out)
	}
	code, leaderOut, errb := runP1CLI("--server", leaderAddr, "process", "list")
	if code != 0 {
		t.Fatalf("leader list exit=%d stdout=%q stderr=%q", code, leaderOut, errb)
	}
	if strings.Contains(leaderOut, "follower-sleep") {
		t.Fatalf("leader unexpectedly owns follower process: %q", leaderOut)
	}

	if code, out, errb := runP1CLI("--server", followerAddr, "process", "stop", "follower-sleep"); code != 0 {
		t.Fatalf("stop through follower exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	waitObserved(t, followerAddr, "follower-sleep", "STOPPED")
}

func TestAccept_LoginThroughFollowerWithoutQuorumIsUnavailable(t *testing.T) {
	leaderAddr, _, stopLeader := startClusterAgentCtl(t, "")
	followerAddr, _ := startClusterAgent(t, "")
	password := joinTwoAndPassword(t, leaderAddr, followerAddr)

	stopLeader()
	code, out, errb := runP1CLI("--server", followerAddr, "login", "--user", "admin", "--password", password)
	if code != 1 {
		t.Fatalf("login without quorum exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	if !strings.Contains(errb, "UNAVAILABLE") {
		t.Fatalf("login without quorum must be unavailable: %q", errb)
	}
}
