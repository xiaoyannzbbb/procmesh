package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func newClusterBackupAgentClient(t *testing.T, api *ClusterBackupAgentAPI, tlsState *tls.ConnectionState) procmeshv1connect.ClusterBackupAgentServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	h, handlers := procmeshv1connect.NewClusterBackupAgentServiceHandler(api)
	mux.Handle(h, handlers)

	// Wrap handler to inject TLS state into context
	wrappedMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tlsState != nil {
			ctx := rpc.WithTLSState(r.Context(), *tlsState)
			r = r.WithContext(ctx)
		}
		mux.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(wrappedMux)
	t.Cleanup(srv.Close)

	return procmeshv1connect.NewClusterBackupAgentServiceClient(srv.Client(), srv.URL)
}

func TestClusterBackupAgent_RequiresAgentMTLS(t *testing.T) {
	engine := &fakeBackupEngine{}
	api := &ClusterBackupAgentAPI{
		Engine:    engine,
		ClusterID: "cluster-1",
		NodeID:    "node-a",
	}

	// No TLS state - should fail
	client := newClusterBackupAgentClient(t, api, nil)
	_, err := client.RunTask(context.Background(), connect.NewRequest(&procmeshv1.RunClusterBackupTaskRequest{
		RunId:   "run-1",
		TaskId:  "task-1",
		NodeId:  "node-a",
	}))
	if err == nil {
		t.Fatal("expected error without mTLS")
	}
}

func TestClusterBackupAgent_RejectsClusterIDMismatch(t *testing.T) {
	wrongCreds := genAgentCreds(t, "cluster-2", "node-b")

	engine := &fakeBackupEngine{}
	api := &ClusterBackupAgentAPI{
		Engine:    engine,
		ClusterID: "cluster-1",
		NodeID:    "node-a",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{wrongCreds.Cert},
	}

	client := newClusterBackupAgentClient(t, api, tlsState)
	_, err := client.RunTask(context.Background(), connect.NewRequest(&procmeshv1.RunClusterBackupTaskRequest{
		RunId:   "run-1",
		TaskId:  "task-1",
		NodeId:  "node-a",
	}))
	if err == nil {
		t.Fatal("expected error with cluster ID mismatch")
	}
}

func TestClusterBackupAgent_RejectsNodeIDMismatch(t *testing.T) {
	creds := genAgentCreds(t, "cluster-1", "node-a")

	engine := &fakeBackupEngine{}
	api := &ClusterBackupAgentAPI{
		Engine:    engine,
		ClusterID: "cluster-1",
		NodeID:    "node-a",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newClusterBackupAgentClient(t, api, tlsState)
	_, err := client.RunTask(context.Background(), connect.NewRequest(&procmeshv1.RunClusterBackupTaskRequest{
		RunId:   "run-1",
		TaskId:  "task-1",
		NodeId:  "node-b", // Request targets node-b, but API is on node-a
	}))
	if err == nil {
		t.Fatal("expected error with node ID mismatch")
	}
}

func TestClusterBackupAgent_RejectsUserToken(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	sid, _, _, _, err := svc.Login("admin", testAdminPass)
	if err != nil {
		t.Fatal(err)
	}

	engine := &fakeBackupEngine{}
	api := &ClusterBackupAgentAPI{
		Engine:    engine,
		ClusterID: "cluster-1",
		NodeID:    "node-a",
		Auth:      svc,
	}

	client := newClusterBackupAgentClient(t, api, nil)
	req := bearerReq(sid, &procmeshv1.RunClusterBackupTaskRequest{
		RunId:   "run-1",
		TaskId:  "task-1",
		NodeId:  "node-a",
	})

	_, err = client.RunTask(context.Background(), req)
	if err == nil {
		t.Fatal("expected error with user token")
	}
}

func TestClusterBackupAgent_ExecutesLocalTask(t *testing.T) {
	creds := genAgentCreds(t, "cluster-1", "node-a")

	engine := &fakeBackupEngine{
		runResult: &backup.TaskResult{
			SnapshotID: "snap-123",
			SHA256:     "abc123",
			Bytes:      1024,
			Status:     "SUCCESS",
		},
	}
	api := &ClusterBackupAgentAPI{
		Engine:    engine,
		ClusterID: "cluster-1",
		NodeID:    "node-a",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newClusterBackupAgentClient(t, api, tlsState)
	resp, err := client.RunTask(context.Background(), connect.NewRequest(&procmeshv1.RunClusterBackupTaskRequest{
		RunId:              "run-1",
		TaskId:             "task-1",
		NodeId:             "node-a",
		PolicyRevision:     5,
		Sink:               "s3",
		DestinationProfile: "prod",
	}))
	if err != nil {
		t.Fatal(err)
	}

	task := resp.Msg.GetTask()
	if task.GetSnapshotId() != "snap-123" || task.GetStatus() != "SUCCESS" {
		t.Fatalf("unexpected task: %+v", task)
	}

	if engine.runCalls != 1 {
		t.Fatalf("expected 1 run call, got %d", engine.runCalls)
	}
}

func TestClusterBackupAgent_ReturnsTaskFailureOnly(t *testing.T) {
	creds := genAgentCreds(t, "cluster-1", "node-a")

	engine := &fakeBackupEngine{
		runResult: &backup.TaskResult{
			Status:       "FAILED",
			ErrorCode:    "DISK_FULL",
			ErrorSummary: "insufficient disk space",
		},
	}
	api := &ClusterBackupAgentAPI{
		Engine:    engine,
		ClusterID: "cluster-1",
		NodeID:    "node-a",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newClusterBackupAgentClient(t, api, tlsState)
	resp, err := client.RunTask(context.Background(), connect.NewRequest(&procmeshv1.RunClusterBackupTaskRequest{
		RunId:   "run-1",
		TaskId:  "task-1",
		NodeId:  "node-a",
	}))
	if err != nil {
		t.Fatal(err)
	}

	task := resp.Msg.GetTask()
	if task.GetStatus() != "FAILED" || task.GetErrorCode() != "DISK_FULL" {
		t.Fatalf("unexpected task: %+v", task)
	}
}

func TestClusterBackupAgent_IdempotentReturnsExisting(t *testing.T) {
	creds := genAgentCreds(t, "cluster-1", "node-a")

	engine := &fakeBackupEngine{
		runResult: &backup.TaskResult{
			SnapshotID: "snap-456",
			Status:     "SUCCESS",
			Bytes:      2048,
		},
		idempotent: true,
	}
	api := &ClusterBackupAgentAPI{
		Engine:    engine,
		ClusterID: "cluster-1",
		NodeID:    "node-a",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newClusterBackupAgentClient(t, api, tlsState)

	// First call
	resp1, err := client.RunTask(context.Background(), connect.NewRequest(&procmeshv1.RunClusterBackupTaskRequest{
		RunId:   "run-1",
		TaskId:  "task-1",
		NodeId:  "node-a",
	}))
	if err != nil {
		t.Fatal(err)
	}

	// Second call with same IDs
	resp2, err := client.RunTask(context.Background(), connect.NewRequest(&procmeshv1.RunClusterBackupTaskRequest{
		RunId:   "run-1",
		TaskId:  "task-1",
		NodeId:  "node-a",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if resp1.Msg.GetTask().GetSnapshotId() != resp2.Msg.GetTask().GetSnapshotId() {
		t.Fatal("idempotent calls returned different results")
	}

	if engine.runCalls != 1 {
		t.Fatalf("expected 1 run call (idempotent), got %d", engine.runCalls)
	}
}

func TestClusterBackupAgent_GetTaskReturnsStatus(t *testing.T) {
	creds := genAgentCreds(t, "cluster-1", "node-a")

	engine := &fakeBackupEngine{
		getResult: &backup.TaskResult{
			SnapshotID: "snap-789",
			Status:     "IN_PROGRESS",
			Bytes:      512,
		},
	}
	api := &ClusterBackupAgentAPI{
		Engine:    engine,
		ClusterID: "cluster-1",
		NodeID:    "node-a",
	}

	tlsState := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{creds.Cert},
	}

	client := newClusterBackupAgentClient(t, api, tlsState)
	resp, err := client.GetTask(context.Background(), connect.NewRequest(&procmeshv1.GetClusterBackupTaskRequest{
		RunId:  "run-1",
		TaskId: "task-1",
	}))
	if err != nil {
		t.Fatal(err)
	}

	task := resp.Msg.GetTask()
	if task.GetStatus() != "IN_PROGRESS" || task.GetSnapshotId() != "snap-789" {
		t.Fatalf("unexpected task: %+v", task)
	}
}

type fakeBackupEngine struct {
	runResult  *backup.TaskResult
	getResult  *backup.TaskResult
	runCalls   int
	getCalls   int
	idempotent bool
}

func (f *fakeBackupEngine) RunClusterTask(ctx context.Context, req backup.ClusterTaskRequest) (*backup.TaskResult, error) {
	if f.idempotent && f.runCalls > 0 {
		return f.runResult, nil
	}
	f.runCalls++
	return f.runResult, nil
}

func (f *fakeBackupEngine) GetClusterTask(ctx context.Context, runID, taskID string) (*backup.TaskResult, error) {
	f.getCalls++
	return f.getResult, nil
}

type testAgentCreds struct {
	Cert *x509.Certificate
}

func genAgentCreds(t *testing.T, clusterID, nodeID string) testAgentCreds {
	t.Helper()

	// Generate a private key
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Create certificate template with URI SAN
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: nodeID,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Add URI SAN with procmesh URI format
	uri, err := url.Parse("procmesh://" + clusterID + "/" + nodeID)
	if err != nil {
		t.Fatal(err)
	}
	template.URIs = []*url.URL{uri}

	// Self-sign the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}

	return testAgentCreds{Cert: cert}
}
