package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
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
	waitClusterNoQuorum(t, followerAddr)
	code, out, errb := runP1CLI("--server", followerAddr, "login", "--user", "admin", "--password", password)
	if code != 1 {
		t.Fatalf("login without quorum exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	if !strings.Contains(errb, "CONTROL_QUORUM_UNAVAILABLE") {
		t.Fatalf("login without quorum must expose stable detail: %q", errb)
	}
	if strings.Contains(errb, "not raft leader") || strings.Contains(errb, "127.0.0.1") {
		t.Fatalf("login without quorum leaked internal details: %q", errb)
	}
}

func waitClusterNoQuorum(t *testing.T, addr string) {
	t.Helper()
	waitClusterOverview(t, addr, 15*time.Second, func(resp *procmeshv1.ClusterOverviewResponse) bool {
		return !resp.GetControlQuorum()
	}, "control quorum still available")
}

func TestAccept_LoginRejectsSecondAgentHop(t *testing.T) {
	leaderAddr, _ := startClusterAgent(t, "")
	followerAddr, _ := startClusterAgent(t, "")
	_ = joinTwoAndPassword(t, leaderAddr, followerAddr)

	client := procmeshv1connect.NewAuthServiceClient(&http.Client{Timeout: 5 * time.Second}, "http://"+followerAddr, testConnectOpts()...)
	req := connect.NewRequest(&procmeshv1.LoginRequest{Username: "admin", Password: "unused"})
	rpc.SetLoginHop(req.Header(), "2")
	_, err := client.Login(context.Background(), req)
	code, detail := agentConnectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "LOGIN_FORWARD_HOP_LIMIT" {
		t.Fatalf("code=%v detail=%q err=%v", code, detail, err)
	}
}

func TestAccept_LoginThroughFollowerWithUnreachableLeader(t *testing.T) {
	deadRPC := freeLocalAddr(t)
	leaderAddr, _ := startClusterAgentAtOpts(t, t.TempDir(), Options{RPCAdvertise: deadRPC})
	followerAddr, _ := startClusterAgent(t, "")
	password := joinTwoAndPassword(t, leaderAddr, followerAddr)

	code, out, errb := runP1CLI("--server", followerAddr, "login", "--user", "admin", "--password", password)
	if code != 1 || !strings.Contains(errb, "LEADER_UNREACHABLE") {
		t.Fatalf("login with unreachable leader exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	if strings.Contains(errb, deadRPC) || strings.Contains(errb, "dial tcp") {
		t.Fatalf("unreachable leader details leaked: %q", errb)
	}
}

func TestAccept_LoginThroughOriginalFollowerAfterLeaderChange(t *testing.T) {
	seedAddr, _, stopSeed := startClusterAgentCtl(t, "")
	voterBAddr, voterBRoot := startClusterAgent(t, "")
	voterCAddr, voterCRoot := startClusterAgent(t, "")
	entryAddr, _ := startClusterAgent(t, "")

	password := joinThree(t, seedAddr, voterBAddr, voterCAddr)
	mustCLI(t, seedAddr, "node", "promote", readNodeID(t, voterBRoot))
	mustCLI(t, seedAddr, "node", "promote", readNodeID(t, voterCRoot))

	token := createJoinToken(t, seedAddr)
	code, out, errb := runP1CLI("--server", entryAddr, "agent", "join", "--seed", seedAddr, "--token", token)
	if code != 0 {
		t.Fatalf("entry join exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		code, out, errb = runP1CLI("--server", entryAddr, "node", "list")
		if code == 0 && len(parseNodeIDs(out)) == 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code != 0 || len(parseNodeIDs(out)) != 4 {
		t.Fatalf("entry did not observe four members: code=%d stdout=%q stderr=%q", code, out, errb)
	}

	loginAdmin(t, entryAddr, password)
	previousLeader := controlLeaderAt(t, entryAddr)
	stopSeed()
	waitClusterLeaderChange(t, entryAddr, previousLeader)

	code, out, errb = runP1CLI("--server", entryAddr, "login", "--user", "admin", "--password", password)
	if code != 0 {
		t.Fatalf("login through original follower after leader change exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	if !strings.Contains(out, "username=admin") {
		t.Fatalf("login response missing admin identity: %q", out)
	}

	tokenAfterFailover := createJoinToken(t, entryAddr)
	newAddr, _ := startClusterAgent(t, "")
	code, out, errb = runP1CLI("--server", newAddr, "agent", "join", "--seed", entryAddr, "--token", tokenAfterFailover)
	if code != 0 {
		t.Fatalf("join through original follower after leader change exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		code, out, errb = runP1CLI("--server", entryAddr, "node", "list")
		if code == 0 && len(parseNodeIDs(out)) == 5 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("new node did not join after failover: code=%d stdout=%q stderr=%q", code, out, errb)
}

func controlLeaderAt(t *testing.T, addr string) string {
	t.Helper()
	client := procmeshv1connect.NewClusterServiceClient(&http.Client{Timeout: 5 * time.Second}, "http://"+addr, testConnectOpts()...)
	resp, err := client.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if leader := resp.Msg.GetControlLeader(); leader != "" {
		return leader
	}
	t.Fatal("control leader is empty")
	return ""
}

func waitClusterLeaderChange(t *testing.T, addr, previous string) {
	t.Helper()
	waitClusterOverview(t, addr, 20*time.Second, func(resp *procmeshv1.ClusterOverviewResponse) bool {
		return resp.GetControlQuorum() && resp.GetControlLeader() != "" && resp.GetControlLeader() != previous
	}, "control leader did not change from "+previous)
}

func waitClusterOverview(t *testing.T, addr string, timeout time.Duration, ready func(*procmeshv1.ClusterOverviewResponse) bool, failure string) {
	t.Helper()
	client := procmeshv1connect.NewClusterServiceClient(&http.Client{Timeout: 5 * time.Second}, "http://"+addr, testConnectOpts()...)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
		if err == nil && ready(resp.Msg) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(failure)
}

func agentConnectDetail(t *testing.T, err error) (connect.Code, string) {
	t.Helper()
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected connect error, got %T: %v", err, err)
	}
	for _, detail := range connectErr.Details() {
		message, detailErr := detail.Value()
		if detailErr != nil {
			continue
		}
		if info, ok := message.(*procmeshv1.ErrorInfo); ok {
			return connectErr.Code(), info.GetCode()
		}
	}
	return connectErr.Code(), ""
}
