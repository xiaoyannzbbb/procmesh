package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type staticWorkloadSource struct {
	summary cluster.NodeSummary
}

func (s staticWorkloadSource) Snapshot() cluster.NodeSummary { return s.summary }

func newWorkloadSummaryClient(t *testing.T, api *WorkloadSummaryAPI, tlsState *tls.ConnectionState) procmeshv1connect.WorkloadSummaryServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := procmeshv1connect.NewWorkloadSummaryServiceHandler(api)
	mux.Handle(path, handler)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tlsState != nil {
			r = r.WithContext(rpc.WithTLSState(r.Context(), *tlsState))
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return procmeshv1connect.NewWorkloadSummaryServiceClient(server.Client(), server.URL)
}

func TestWorkloadSummaryAPI_FetchStreamsCompleteSnapshot(t *testing.T) {
	processes := make([]cluster.ProcessSummary, 300)
	for i := range processes {
		processes[i] = cluster.ProcessSummary{
			ProcessID: "process-" + strconv.Itoa(i), Name: "worker-" + strconv.Itoa(i),
			Desired: "RUNNING", Observed: "RUNNING", Health: "HEALTHY",
			LatestRevision: 2, ActiveRevision: 2, FreshnessUnixMs: 1_700_000_000_000,
		}
	}
	api := &WorkloadSummaryAPI{
		ClusterID: "cluster-1", NodeID: "owner-1",
		Source: staticWorkloadSource{summary: cluster.NodeSummary{
			NodeID: "owner-1", WorkloadSyncVersion: 1, WorkloadEpoch: "epoch-1",
			WorkloadVersion: 9, WorkloadObservationSeq: 14, Processes: processes,
		}},
	}
	creds := genAgentCreds(t, "cluster-1", "observer-1")
	tlsState := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{creds.Cert}}
	client := newWorkloadSummaryClient(t, api, tlsState)
	stream, err := client.FetchWorkloadSnapshot(context.Background(), connect.NewRequest(&procmeshv1.FetchWorkloadSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var got []*procmeshv1.ProcessSummary
	chunks := 0
	for stream.Receive() {
		chunk := stream.Msg()
		chunks++
		if chunk.GetNodeId() != "owner-1" || chunk.GetEpoch() != "epoch-1" || chunk.GetVersion() != 9 || chunk.GetObservationSeq() != 14 {
			t.Fatalf("chunk identity/version = %+v", chunk)
		}
		got = append(got, chunk.GetProcesses()...)
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if chunks < 2 || len(got) != 300 {
		t.Fatalf("chunks/processes = %d/%d, want multiple chunks and 300 processes", chunks, len(got))
	}
}

func TestWorkloadSummaryAPI_FetchRequiresMTLS(t *testing.T) {
	client := newWorkloadSummaryClient(t, &WorkloadSummaryAPI{
		ClusterID: "cluster-1", NodeID: "owner-1",
		Source: staticWorkloadSource{summary: cluster.NodeSummary{NodeID: "owner-1"}},
	}, nil)
	stream, err := client.FetchWorkloadSnapshot(context.Background(), connect.NewRequest(&procmeshv1.FetchWorkloadSnapshotRequest{}))
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code = %s, want permission_denied (err=%v)", connect.CodeOf(err), err)
	}
}
