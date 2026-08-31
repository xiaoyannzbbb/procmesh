package rpc_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestMapDialError_Timeout(t *testing.T) {
	err := rpc.MapDialError(context.DeadlineExceeded)
	if !errcode.Is(err, errcode.TIMEOUT) {
		t.Fatalf("%v", err)
	}
}

func TestMapDialError_Refused(t *testing.T) {
	err := rpc.MapDialError(errors.New("connection refused"))
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("%v", err)
	}
}

func TestMapDialError_PassesThroughErrcode(t *testing.T) {
	in := errcode.E(errcode.TIMEOUT, "rpc timed out")
	got := rpc.MapDialError(in)
	if !errcode.Is(got, errcode.TIMEOUT) {
		t.Fatalf("got %v", got)
	}
	if got != in {
		t.Fatalf("want same error, got %v", got)
	}
}

func TestMapCallError_PassesThroughErrcode(t *testing.T) {
	in := errcode.E(errcode.TIMEOUT, "rpc timed out")
	got := rpc.MapCallError(in)
	if !errcode.Is(got, errcode.TIMEOUT) {
		t.Fatalf("got %v", got)
	}
}

func TestDial_TimeoutZeroMeansNoClientTimeout(t *testing.T) {
	seed := newSeed(t)
	hc, _, err := rpc.Dial(rpc.DialConfig{
		Creds:     credsOf(seed),
		ClusterID: "cid",
		Address:   "127.0.0.1:1",
		Timeout:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hc.Timeout != 0 {
		t.Fatalf("Timeout=%v want 0", hc.Timeout)
	}
	_ = rpc.NewLogClient(hc, "https://127.0.0.1:1")
	_ = rpc.NewAuditClient(hc, "https://127.0.0.1:1")
	_ = rpc.NewMetricsClient(hc, "https://127.0.0.1:1")
	_ = rpc.NewAlertClient(hc, "https://127.0.0.1:1")
	_ = rpc.NewUpdateClient(hc, "https://127.0.0.1:1")
}

func TestDial_MutationTimeout(t *testing.T) {
	seed := newSeed(t)
	hc, _, err := rpc.Dial(rpc.DialConfig{
		Creds:     credsOf(seed),
		ClusterID: "cid",
		Address:   "127.0.0.1:1",
		Timeout:   rpc.MutationTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hc.Timeout != rpc.MutationTimeout {
		t.Fatalf("Timeout=%v want %v", hc.Timeout, rpc.MutationTimeout)
	}
	if rpc.MutationTimeout < 30*time.Second {
		t.Fatalf("MutationTimeout=%v want >=30s", rpc.MutationTimeout)
	}
	if rpc.UnaryTimeout != 5*time.Second {
		t.Fatalf("UnaryTimeout=%v want 5s", rpc.UnaryTimeout)
	}
}

func TestDial_CallsOwnerProcess(t *testing.T) {
	seed := newSeed(t)
	owner := signPeer(t, seed, "cid", "owner", time.Now())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	path, h := procmeshv1connect.NewProcessServiceHandler(&stubProcess{})
	mux.Handle(path, h)
	srv, err := rpc.NewServer(ln.Addr().String(), credsOf(owner), "cid", mux, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	hc, base, err := rpc.Dial(rpc.DialConfig{
		Creds:        credsOf(seed),
		ClusterID:    "cid",
		ExpectNodeID: "owner",
		Address:      ln.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cli := rpc.NewProcessClient(hc, base)
	_, err = cli.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
}

type stubProcess struct {
	procmeshv1connect.UnimplementedProcessServiceHandler
}

func (stubProcess) ListProcesses(context.Context, *connect.Request[procmeshv1.ListProcessesRequest]) (*connect.Response[procmeshv1.ListProcessesResponse], error) {
	return connect.NewResponse(&procmeshv1.ListProcessesResponse{}), nil
}
