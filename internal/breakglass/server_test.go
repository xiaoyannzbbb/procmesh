package breakglass

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestServer_DeniesUnauthorizedPeerAndAuditsCompleteProcessIdentity(t *testing.T) {
	root := shortTempDir(t)
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := process.NewManager(process.Deps{Store: st, Layout: layout, Now: time.Now})
	if _, err := mgr.ApplySpec(context.Background(), process.ProcessSpec{
		ProcessID: "worker-id", Name: "worker", OwnerAgentID: "agent-local", Command: "/bin/true", Instances: 1,
	}, 0, "op-seed-worker", "test", ""); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "break-glass.sock")
	srv, err := New(Config{
		SocketPath: socketPath,
		LocalID:    "agent-local",
		Manager:    mgr,
		Audit:      st,
		PeerLookup: func(net.Conn) (Peer, error) {
			return Peer{PID: 9001, UID: os.Geteuid() + 1, GID: os.Getegid() + 1, Username: "intruder"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-serveErr
	})
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode=%#o want 0600", got)
	}

	hc := unixHTTPClient(socketPath)
	client := procmeshv1connect.NewProcessServiceClient(hc, "http://procmesh.local")
	request := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-denied"}, IdOrName: "worker",
	})
	request.Header().Set(rpc.HeaderBreakGlassReason, "recover service")
	_, err = client.StopProcess(context.Background(), request)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
	}
	if strings.Contains(err.Error(), "intruder") || strings.Contains(err.Error(), socketPath) {
		t.Fatalf("denial leaked peer or socket details: %v", err)
	}

	events, err := st.ListAuditAll(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var denied *store.AuditEvent
	for i := range events {
		if events[i].Action == "break_glass.process.stop" && events[i].OperationID == "op-denied" {
			denied = &events[i]
			break
		}
	}
	if denied == nil || denied.Resource != "worker-id" || denied.Result != "denied" || denied.UserID != "uid:"+strconv.Itoa(os.Geteuid()+1) || denied.Username == "" || denied.Timestamp.IsZero() {
		t.Fatalf("denied audit=%+v", events)
	}
	var metadata map[string]any
	if err := json.Unmarshal(denied.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["process_id"] != "worker-id" || metadata["process_name"] != "worker" || metadata["error_code"] != "DENIED" {
		t.Fatalf("denied audit metadata=%v", metadata)
	}
}

func TestServer_ConfiguredGroupCanInspectOverUnixSocket(t *testing.T) {
	group, err := user.LookupGroupId(strconv.Itoa(os.Getegid()))
	if err != nil {
		t.Skipf("current OS group unavailable: %v", err)
	}
	root := shortTempDir(t)
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := process.NewManager(process.Deps{Store: st, Layout: layout, Now: time.Now})
	socketPath := filepath.Join(root, "break-glass.sock")
	srv, err := New(Config{
		SocketPath: socketPath,
		Group:      group.Name,
		LocalID:    "agent-local",
		Manager:    mgr,
		Audit:      st,
		PeerLookup: func(net.Conn) (Peer, error) {
			return Peer{PID: 42, UID: os.Geteuid() + 1, GID: os.Getegid() + 1, Groups: []string{group.Gid}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-serveErr
	})
	if srv.listener.Addr().Network() != "unix" {
		t.Fatalf("listener network=%q", srv.listener.Addr().Network())
	}
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o660 {
		t.Fatalf("socket mode=%#o want 0660", got)
	}

	client := procmeshv1connect.NewProcessServiceClient(unixHTTPClient(socketPath), "http://procmesh.local")
	if _, err := client.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{})); err != nil {
		t.Fatalf("configured group member denied: %v", err)
	}
	hc := unixHTTPClient(socketPath)
	base := "http://procmesh.local"
	for name, call := range map[string]func() error{
		"reset failure": func() error {
			_, err := client.ResetFailure(context.Background(), connect.NewRequest(&procmeshv1.ProcessRefRequest{IdOrName: "worker"}))
			return err
		},
		"apply": func() error {
			_, err := client.ApplyProcess(context.Background(), connect.NewRequest(&procmeshv1.ApplyProcessRequest{}))
			return err
		},
		"delete": func() error {
			_, err := client.DeleteProcess(context.Background(), connect.NewRequest(&procmeshv1.DeleteProcessRequest{IdOrName: "worker"}))
			return err
		},
		"adopt": func() error {
			_, err := client.AdoptInstance(context.Background(), connect.NewRequest(&procmeshv1.AdoptRequest{InstanceId: "worker:0"}))
			return err
		},
		"configuration": func() error {
			_, err := procmeshv1connect.NewConfigServiceClient(hc, base).GetConfig(context.Background(), connect.NewRequest(&procmeshv1.GetConfigRequest{IdOrName: "worker"}))
			return err
		},
		"backup": func() error {
			_, err := procmeshv1connect.NewBackupServiceClient(hc, base).ListBackups(context.Background(), connect.NewRequest(&procmeshv1.ListBackupsRequest{}))
			return err
		},
		"batch": func() error {
			_, err := procmeshv1connect.NewBatchServiceClient(hc, base).ListBatches(context.Background(), connect.NewRequest(&procmeshv1.ListBatchesRequest{}))
			return err
		},
		"control plane": func() error {
			_, err := procmeshv1connect.NewClusterServiceClient(hc, base).Overview(context.Background(), connect.NewRequest(&procmeshv1.ClusterOverviewRequest{}))
			return err
		},
	} {
		if err := call(); err == nil {
			t.Fatalf("%s unexpectedly allowed", name)
		}
	}
	events, err := st.ListAuditAll(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var rejected bool
	for _, event := range events {
		if event.Action == "break_glass.reject" && event.Resource == "worker" && event.Result == "denied" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("missing rejected write audit: %+v", events)
	}
}

func TestServer_LifecycleRequiresRecoveryMetadataAndWritesCompleteAudit(t *testing.T) {
	root := shortTempDir(t)
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	mgr := process.NewManager(process.Deps{Store: st, Layout: layout, Now: time.Now})
	for _, fixture := range []struct {
		spec process.ProcessSpec
		opID string
	}{
		{spec: process.ProcessSpec{ProcessID: "worker-id", Name: "worker", OwnerAgentID: "agent-local", Command: "/bin/true", Instances: 1}, opID: "op-seed-worker"},
		{spec: process.ProcessSpec{ProcessID: "remote-id", Name: "remote", OwnerAgentID: "agent-remote", Command: "/bin/true", Instances: 1}, opID: "op-seed-remote"},
	} {
		if _, err := mgr.ApplySpec(ctx, fixture.spec, 0, fixture.opID, "test", ""); err != nil {
			t.Fatal(err)
		}
		if err := mgr.SetDesired(ctx, fixture.spec.ProcessID, process.DesiredRunning, fixture.opID+"-start", "test"); err != nil {
			t.Fatal(err)
		}
	}

	socketPath := filepath.Join(root, "break-glass.sock")
	srv, err := New(Config{
		SocketPath: socketPath,
		LocalID:    "agent-local",
		Manager:    mgr,
		Audit:      st,
		PeerLookup: func(net.Conn) (Peer, error) {
			return Peer{PID: 42, UID: os.Geteuid(), GID: os.Getegid(), Username: "trusted"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-serveErr
	})
	client := procmeshv1connect.NewProcessServiceClient(unixHTTPClient(socketPath), "http://procmesh.local")

	missingOperation := connect.NewRequest(&procmeshv1.ProcessRefRequest{IdOrName: "worker"})
	missingOperation.Header().Set(rpc.HeaderBreakGlassReason, "recover service")
	if _, err := client.StopProcess(ctx, missingOperation); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing operation ID code=%v err=%v", connect.CodeOf(err), err)
	}
	missingReason := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-missing-reason", Operator: "spoofed"}, IdOrName: "worker",
	})
	if _, err := client.StopProcess(ctx, missingReason); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing reason code=%v err=%v", connect.CodeOf(err), err)
	}
	remote := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-remote"}, IdOrName: "remote",
	})
	remote.Header().Set(rpc.HeaderBreakGlassReason, "recover service")
	if _, err := client.StopProcess(ctx, remote); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("remote owner code=%v err=%v", connect.CodeOf(err), err)
	}
	remoteTarget := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-remote-target"}, IdOrName: "worker",
	})
	remoteTarget.Header().Set(rpc.HeaderBreakGlassReason, "recover service")
	remoteTarget.Header().Set(rpc.HeaderTargetNode, "agent-remote")
	if _, err := client.StopProcess(ctx, remoteTarget); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("remote target code=%v err=%v", connect.CodeOf(err), err)
	}
	remoteInstances, err := st.ListInstances(ctx, "remote-id")
	if err != nil || len(remoteInstances) != 1 || remoteInstances[0].Desired != process.DesiredRunning {
		t.Fatalf("remote lifecycle state changed: %+v err=%v", remoteInstances, err)
	}
	before, err := st.ListInstances(ctx, "worker-id")
	if err != nil || len(before) != 1 || before[0].Desired != process.DesiredRunning {
		t.Fatalf("invalid requests changed lifecycle state: %+v err=%v", before, err)
	}

	valid := connect.NewRequest(&procmeshv1.ProcessRefRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-stop", Operator: "spoofed"}, IdOrName: "worker",
	})
	valid.Header().Set(rpc.HeaderBreakGlassReason, "recover service")
	if _, err := client.StopProcess(ctx, valid); err != nil {
		t.Fatalf("valid stop: %v", err)
	}
	after, err := st.ListInstances(ctx, "worker-id")
	if err != nil || len(after) != 1 || after[0].Desired != process.DesiredStopped {
		t.Fatalf("stop did not change lifecycle state: %+v err=%v", after, err)
	}

	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	op, err := st.GetOperation(ctx, "op-stop")
	if err != nil || op.Operator != account.Username || op.Type != "set_desired" || op.Target != "worker-id" || op.Status != store.OpSuccess {
		t.Fatalf("journal=%+v err=%v", op, err)
	}
	events, err := st.ListAuditAll(ctx, "worker-id", 20)
	if err != nil {
		t.Fatal(err)
	}
	var lifecycle *store.AuditEvent
	var remoteTargetDenial *store.AuditEvent
	for i := range events {
		if events[i].Action == "break_glass.process.stop" && events[i].OperationID == "op-stop" {
			lifecycle = &events[i]
		}
		if events[i].Action == "break_glass.process.stop" && events[i].OperationID == "op-remote-target" {
			remoteTargetDenial = &events[i]
		}
	}
	if lifecycle == nil {
		t.Fatalf("missing lifecycle audit: %+v", events)
	}
	if lifecycle.OperationID != "op-stop" || lifecycle.TargetAgent != "agent-local" || lifecycle.Username != account.Username || lifecycle.Result != "success" || lifecycle.Timestamp.IsZero() {
		t.Fatalf("incomplete lifecycle audit: %+v", lifecycle)
	}
	var metadata map[string]any
	if err := json.Unmarshal(lifecycle.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["reason"] != "recover service" || metadata["process_id"] != "worker-id" || metadata["process_name"] != "worker" || metadata["error_code"] != "" || metadata["os_uid"] != float64(os.Geteuid()) {
		t.Fatalf("audit metadata=%v", metadata)
	}
	if remoteTargetDenial == nil || remoteTargetDenial.Resource != "worker-id" || remoteTargetDenial.Result != "denied" {
		t.Fatalf("missing remote target denial audit: %+v", events)
	}
	if err := json.Unmarshal(remoteTargetDenial.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["process_id"] != "worker-id" || metadata["process_name"] != "worker" || metadata["error_code"] != "DENIED" {
		t.Fatalf("remote target denial metadata=%v", metadata)
	}
}

func TestDefaultSocketPath_ShortenLongDataDirectory(t *testing.T) {
	dataDir := filepath.Join(os.TempDir(), strings.Repeat("long-data-directory-", 8))
	first := DefaultSocketPath(dataDir)
	second := DefaultSocketPath(dataDir)
	if first != second {
		t.Fatalf("default socket path is not stable: %q != %q", first, second)
	}
	if len(first) > portableSocketPathLimit {
		t.Fatalf("default socket path remains too long: %d %q", len(first), first)
	}
}

func TestServer_PeerLookupFailureIsDeniedAndAuditedAsUnknown(t *testing.T) {
	root := shortTempDir(t)
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	socketPath := filepath.Join(root, "break-glass.sock")
	srv, err := New(Config{
		SocketPath: socketPath,
		LocalID:    "agent-local",
		Manager:    process.NewManager(process.Deps{Store: st, Layout: layout, Now: time.Now}),
		Audit:      st,
		PeerLookup: func(net.Conn) (Peer, error) { return Peer{}, errors.New("peer unavailable") },
	})
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-serveErr
	})
	client := procmeshv1connect.NewProcessServiceClient(unixHTTPClient(socketPath), "http://procmesh.local")
	_, err = client.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
	}
	events, err := st.ListAuditAll(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].UserID != "uid:-1" || events[0].Username != "unknown" || events[0].Result != "denied" {
		t.Fatalf("unknown peer audit=%+v", events)
	}
}

type failingAudit struct{}

func (failingAudit) AppendAudit(ctx context.Context, _ store.AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("sensitive storage failure")
}

func TestServer_AuditFailureFailsClosedWithoutLeakingCause(t *testing.T) {
	root := shortTempDir(t)
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	socketPath := filepath.Join(root, "break-glass.sock")
	srv, err := New(Config{
		SocketPath: socketPath,
		LocalID:    "agent-local",
		Manager:    process.NewManager(process.Deps{Store: st, Layout: layout, Now: time.Now}),
		Audit:      failingAudit{},
		PeerLookup: func(net.Conn) (Peer, error) {
			return Peer{UID: os.Geteuid(), GID: os.Getegid()}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-serveErr
	})
	client := procmeshv1connect.NewProcessServiceClient(unixHTTPClient(socketPath), "http://procmesh.local")
	_, err = client.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
	}
	if strings.Contains(err.Error(), "sensitive storage failure") {
		t.Fatalf("audit failure leaked cause: %v", err)
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "pm-bg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func unixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}},
	}
}
