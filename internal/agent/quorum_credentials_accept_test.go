package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
	"golang.org/x/sys/unix"
)

const quorumLossLogMarker = "procmesh-quorum-loss-ready"

type quorumCredential struct {
	token string
}

func (c quorumCredential) cliArgs(addr string, args ...string) []string {
	base := []string{"--server", addr}
	if c.token != "" {
		base = append(base, "--auth-token", c.token)
	}
	return append(base, args...)
}

func (c quorumCredential) connectOpts() []connect.ClientOption {
	if c.token == "" {
		return testConnectOpts()
	}
	return []connect.ClientOption{connect.WithInterceptors(bearerTestInterceptor(c.token))}
}

func TestAccept_ReplicatedSessionManagesOwnerProcessesWithoutQuorum(t *testing.T) {
	configPath := writeQuorumCredentialAgentConfig(t)
	leaderAddr, _, stopLeader := startClusterAgentOptsCtl(t, Options{ConfigPath: configPath})
	ownerAddr, _, _ := startClusterAgentOptsCtl(t, Options{ConfigPath: configPath})
	password := joinTwoAndPassword(t, leaderAddr, ownerAddr)

	loginAdmin(t, ownerAddr, password)
	credential := quorumCredential{}
	name := "session-quorum-loss"
	initialPID := prepareLoggedProcess(t, credential, ownerAddr, name)

	stopLeader()
	waitCredentialNoQuorum(t, credential, ownerAddr)
	assertPIDAlive(t, initialPID, "control quorum loss stopped business process")
	exerciseLocalProcessPlane(t, credential, ownerAddr, name, initialPID)
}

func TestAccept_ReplicatedAPITokenManagesOwnerProcessesWithoutQuorum(t *testing.T) {
	configPath := writeQuorumCredentialAgentConfig(t)
	leaderAddr, _, stopLeader := startClusterAgentOptsCtl(t, Options{ConfigPath: configPath})
	ownerAddr, _, _ := startClusterAgentOptsCtl(t, Options{ConfigPath: configPath})
	password := joinTwoAndPassword(t, leaderAddr, ownerAddr)

	loginAdmin(t, ownerAddr, password)
	authClient := procmeshv1connect.NewAuthServiceClient(
		&http.Client{Timeout: 5 * time.Second},
		"http://"+leaderAddr,
		testConnectOpts()...,
	)
	token, _, _ := createAPIToken(t, authClient, "quorum-loss", 3600)
	credential := quorumCredential{token: token}
	waitCredentialReady(t, credential, ownerAddr)

	name := "token-quorum-loss"
	initialPID := prepareLoggedProcess(t, credential, ownerAddr, name)

	stopLeader()
	waitCredentialNoQuorum(t, credential, ownerAddr)
	assertPIDAlive(t, initialPID, "control quorum loss stopped business process")
	exerciseLocalProcessPlane(t, credential, ownerAddr, name, initialPID)
}

func TestAccept_NoQuorumPreservesCredentialRestrictions(t *testing.T) {
	leaderAddr, _, stopLeader := startClusterAgentCtl(t, "")
	ownerAddr, _ := startClusterAgent(t, "")
	password := joinTwoAndPassword(t, leaderAddr, ownerAddr)
	loginAdmin(t, ownerAddr, password)

	userOut := mustCLI(t, leaderAddr, "user", "create", "--user", "viewer-loss", "--password", "viewer-pass-ok")
	viewerID := parseKV(userOut, "user_id")
	mustCLI(t, leaderAddr, "role", "grant", "--user-id", viewerID, "--role-id", "viewer")
	viewerLogin := procmeshv1connect.NewAuthServiceClient(&http.Client{Timeout: 5 * time.Second}, "http://"+ownerAddr)
	viewerResp, err := viewerLogin.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username: "viewer-loss", Password: "viewer-pass-ok",
	}))
	if err != nil {
		t.Fatal(err)
	}
	viewer := quorumCredential{token: viewerResp.Msg.GetSessionId()}

	adminAuth := procmeshv1connect.NewAuthServiceClient(
		&http.Client{Timeout: 5 * time.Second}, "http://"+leaderAddr, testConnectOpts()...,
	)
	revokedToken, revokedID, _ := createAPIToken(t, adminAuth, "revoked", 3600)
	expiredToken, _, expiresUnix := createAPIToken(t, adminAuth, "expired", 2)
	revoked := quorumCredential{token: revokedToken}
	expired := quorumCredential{token: expiredToken}
	waitCredentialReady(t, revoked, ownerAddr)
	waitCredentialReady(t, expired, ownerAddr)
	if _, err := adminAuth.RevokeAPIToken(context.Background(), connect.NewRequest(&procmeshv1.RevokeAPITokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-revoke")}, TokenId: revokedID,
	})); err != nil {
		t.Fatal(err)
	}
	waitCredentialDenied(t, revoked, ownerAddr)

	admin := quorumCredential{}
	name := "restricted-quorum-loss"
	prepareLoggedProcess(t, admin, ownerAddr, name)
	stopLeader()
	waitCredentialNoQuorum(t, admin, ownerAddr)

	mustCredentialCLI(t, viewer, ownerAddr, "process", "list")
	assertCredentialCLIDenied(t, viewer, ownerAddr, "process", "restart", name)
	waitUntilExpired(expiresUnix)
	assertCredentialCLIDenied(t, revoked, ownerAddr, "process", "list")
	assertCredentialCLIDenied(t, expired, ownerAddr, "process", "list")
}

func TestAccept_NoQuorumRejectsControlWritesAndPasswordLogin(t *testing.T) {
	leaderAddr, _, stopLeader := startClusterAgentCtl(t, "")
	ownerAddr, ownerRoot := startClusterAgent(t, "")
	password := joinTwoAndPassword(t, leaderAddr, ownerAddr)
	loginAdmin(t, ownerAddr, password)

	groupOut := mustCLI(t, leaderAddr, "group", "create", "--name", "before-loss")
	groupID := parseKV(groupOut, "group_id")
	authClient := procmeshv1connect.NewAuthServiceClient(
		&http.Client{Timeout: 5 * time.Second}, "http://"+leaderAddr, testConnectOpts()...,
	)
	_, revocationID, _ := createAPIToken(t, authClient, "revoke-after-loss", 3600)

	stopLeader()
	waitCredentialNoQuorum(t, quorumCredential{}, ownerAddr)
	ownerAuthClient := procmeshv1connect.NewAuthServiceClient(
		&http.Client{Timeout: 5 * time.Second}, "http://"+ownerAddr, testConnectOpts()...,
	)

	for _, args := range [][]string{
		{"user", "create", "--user", "after-loss", "--password", "after-loss-pass"},
		{"role", "create", "--name", "after-loss", "--perm", "process.read"},
		{"group", "add-member", "--group-id", groupID, "--node-id", readNodeID(t, ownerRoot)},
		{"node", "token", "create"},
	} {
		assertCredentialCLIUnavailable(t, quorumCredential{}, ownerAddr, args...)
	}

	_, err := ownerAuthClient.RevokeAPIToken(context.Background(), connect.NewRequest(&procmeshv1.RevokeAPITokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-revoke-no-quorum")}, TokenId: revocationID,
	}))
	assertConnectUnavailable(t, err)

	alertClient := procmeshv1connect.NewAlertServiceClient(
		&http.Client{Timeout: 5 * time.Second}, "http://"+ownerAddr, testConnectOpts()...,
	)
	_, err = alertClient.PutAlertPolicy(context.Background(), connect.NewRequest(&procmeshv1.PutAlertPolicyRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-policy-no-quorum")}, Policy: &procmeshv1.AlertPolicy{},
	}))
	assertConnectUnavailable(t, err)

	loginClient := procmeshv1connect.NewAuthServiceClient(&http.Client{Timeout: 5 * time.Second}, "http://"+ownerAddr)
	for _, candidate := range []string{password, "wrong-password"} {
		_, err = loginClient.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
			Username: "admin", Password: candidate,
		}))
		code, detail := agentConnectDetail(t, err)
		if code != connect.CodeUnavailable || detail != "CONTROL_QUORUM_UNAVAILABLE" {
			t.Fatalf("login without quorum code=%v detail=%q err=%v", code, detail, err)
		}
	}
}

func prepareLoggedProcess(t *testing.T, credential quorumCredential, addr, name string) int32 {
	t.Helper()
	mustCredentialCLI(t, credential, addr, "process", "apply", "--file", writeLoggedSleepSpec(t, name), "--expected-revision", "0")
	mustCredentialCLI(t, credential, addr, "process", "start", name)
	return waitCredentialObserved(t, credential, addr, name, "RUNNING", 0)
}

func exerciseLocalProcessPlane(t *testing.T, credential quorumCredential, addr, name string, initialPID int32) {
	t.Helper()
	if out := mustCredentialCLI(t, credential, addr, "process", "list"); !strings.Contains(out, name+"\t") {
		t.Fatalf("process list omitted %q: %q", name, out)
	}
	if out := mustCredentialCLI(t, credential, addr, "process", "get", name); !strings.Contains(out, "name\t"+name+"\n") {
		t.Fatalf("process get omitted %q: %q", name, out)
	}
	waitCredentialLogs(t, credential, addr, name, quorumLossLogMarker)

	mustCredentialCLI(t, credential, addr, "process", "restart", name)
	restartedPID := waitCredentialObserved(t, credential, addr, name, "RUNNING", initialPID)
	mustCredentialCLI(t, credential, addr, "process", "stop", name)
	waitCredentialObserved(t, credential, addr, name, "STOPPED", 0)
	mustCredentialCLI(t, credential, addr, "process", "start", name)
	killPID := waitCredentialObserved(t, credential, addr, name, "RUNNING", restartedPID)
	mustCredentialCLI(t, credential, addr, "process", "kill", name)
	waitPIDGone(t, killPID)
}

func mustCredentialCLI(t *testing.T, credential quorumCredential, addr string, args ...string) string {
	t.Helper()
	code, out, errb := runP1CLI(credential.cliArgs(addr, args...)...)
	if code != 0 {
		t.Fatalf("%s: exit=%d stderr=%q stdout=%q", strings.Join(args, " "), code, errb, out)
	}
	return out
}

func assertCredentialCLIDenied(t *testing.T, credential quorumCredential, addr string, args ...string) {
	t.Helper()
	code, out, errb := runP1CLI(credential.cliArgs(addr, args...)...)
	if code == 0 || !strings.Contains(errb, "DENIED") {
		t.Fatalf("%s want DENIED: exit=%d stderr=%q stdout=%q", strings.Join(args, " "), code, errb, out)
	}
}

func assertCredentialCLIUnavailable(t *testing.T, credential quorumCredential, addr string, args ...string) {
	t.Helper()
	code, out, errb := runP1CLI(credential.cliArgs(addr, args...)...)
	if code == 0 || !strings.Contains(errb, "UNAVAILABLE") {
		t.Fatalf("%s want UNAVAILABLE: exit=%d stderr=%q stdout=%q", strings.Join(args, " "), code, errb, out)
	}
}

func waitCredentialReady(t *testing.T, credential quorumCredential, addr string) {
	t.Helper()
	client := procmeshv1connect.NewAuthServiceClient(
		&http.Client{Timeout: 5 * time.Second}, "http://"+addr, credential.connectOpts()...,
	)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.GetMe(context.Background(), connect.NewRequest(&procmeshv1.GetMeRequest{})); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("credential was not replicated to owner")
}

func waitCredentialDenied(t *testing.T, credential quorumCredential, addr string) {
	t.Helper()
	client := procmeshv1connect.NewAuthServiceClient(
		&http.Client{Timeout: 5 * time.Second}, "http://"+addr, credential.connectOpts()...,
	)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, err := client.GetMe(context.Background(), connect.NewRequest(&procmeshv1.GetMeRequest{}))
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("credential revocation was not replicated to owner")
}

func waitCredentialNoQuorum(t *testing.T, credential quorumCredential, addr string) {
	t.Helper()
	client := procmeshv1connect.NewClusterServiceClient(
		&http.Client{Timeout: 5 * time.Second}, "http://"+addr, credential.connectOpts()...,
	)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
		if err == nil && !resp.Msg.GetControlQuorum() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("control quorum still available")
}

func waitCredentialObserved(t *testing.T, credential quorumCredential, addr, name, observed string, differentPID int32) int32 {
	t.Helper()
	client := procmeshv1connect.NewProcessServiceClient(
		&http.Client{Timeout: 5 * time.Second}, "http://"+addr, credential.connectOpts()...,
	)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.GetProcess(context.Background(), connect.NewRequest(&procmeshv1.GetProcessRequest{IdOrName: name}))
		if err == nil && len(resp.Msg.GetProcess().GetInstances()) > 0 {
			inst := resp.Msg.GetProcess().GetInstances()[0]
			if inst.GetObserved() == observed && (observed != "RUNNING" || inst.GetPid() > 0 && inst.GetPid() != differentPID) {
				return inst.GetPid()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %s did not reach %s with a new pid", name, observed)
	return 0
}

func waitCredentialLogs(t *testing.T, credential quorumCredential, addr, name, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, out, _ := runP1CLI(credential.cliArgs(addr, "logs", name)...)
		if code == 0 && strings.Contains(out, marker) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("logs for %s omitted %q", name, marker)
}

func createAPIToken(t *testing.T, client procmeshv1connect.AuthServiceClient, name string, ttlSeconds int64) (token, tokenID string, expiresUnix int64) {
	t.Helper()
	resp, err := client.CreateAPIToken(context.Background(), connect.NewRequest(&procmeshv1.CreateAPITokenRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: uniqueID("op-token")}, Name: name, TtlSeconds: ttlSeconds,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetToken() == "" || resp.Msg.GetTokenId() == "" {
		t.Fatalf("incomplete api token response: %+v", resp.Msg)
	}
	return resp.Msg.GetToken(), resp.Msg.GetTokenId(), resp.Msg.GetExpiresUnix()
}

func writeLoggedSleepSpec(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".yaml")
	body := fmt.Sprintf("name: %s\nprocess_id: %s\ncommand: /bin/sh\nargs:\n  - -c\n  - 'echo %s; exec /bin/sleep 60'\ninstances: 1\n", name, name, quorumLossLogMarker)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeQuorumCredentialAgentConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("disk:\n  emergency_stop_writes: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitUntilExpired(expiresUnix int64) {
	for time.Now().Unix() <= expiresUnix {
		time.Sleep(20 * time.Millisecond)
	}
}

func assertPIDAlive(t *testing.T, pid int32, message string) {
	t.Helper()
	if err := unix.Kill(int(pid), 0); err != nil {
		t.Fatalf("%s %d: %v", message, pid, err)
	}
}

func waitPIDGone(t *testing.T, pid int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(int(pid), 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("killed process %d is still alive", pid)
}

func assertConnectUnavailable(t *testing.T, err error) {
	t.Helper()
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("want unavailable, got %v", err)
	}
}

func bearerTestInterceptor(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	})
}
