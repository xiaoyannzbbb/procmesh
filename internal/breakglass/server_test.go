package breakglass

import (
	"context"
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
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestServer_DeniesUnauthorizedPeerAndAuditsAttempt(t *testing.T) {
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
	_, err = client.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
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
	if len(events) != 1 || events[0].Action != "break_glass.process.list" || events[0].Resource != "local-agent" || events[0].Result != "denied" || events[0].UserID != "uid:"+strconv.Itoa(os.Geteuid()+1) || events[0].Username == "" || events[0].Timestamp.IsZero() {
		t.Fatalf("denied audit=%+v", events)
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
	_, err = client.StartProcess(context.Background(), connect.NewRequest(&procmeshv1.ProcessRefRequest{IdOrName: "worker"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("write procedure code=%v err=%v", connect.CodeOf(err), err)
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
