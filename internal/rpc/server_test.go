package rpc_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/rpc"
)

func TestServer_RequiresClientCert(t *testing.T) {
	seed := newSeed(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := rpc.NewServer(ln.Addr().String(), credsOf(seed), "cid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// no client cert
	plain := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	_, err = plain.Get("https://" + ln.Addr().String() + "/")
	if err == nil {
		t.Fatal("expected handshake failure without client cert")
	}

	okClient := &http.Client{Transport: &http.Transport{TLSClientConfig: mustClientTLS(t, seed, "cid", "")}}
	resp, err := okClient.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestServer_InjectsTLSStateIntoRequestContext(t *testing.T) {
	seed := newSeed(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := rpc.NewServer(ln.Addr().String(), credsOf(seed), "cid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, err := rpc.TLSStateFromContext(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		clusterID, nodeID, err := rpc.PeerIdentity(state)
		if err != nil || clusterID != "cid" || nodeID != "seed" {
			http.Error(w, "unexpected peer identity", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: mustClientTLS(t, seed, "cid", "")}}
	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestServer_RejectsForeignCA(t *testing.T) {
	seed := newSeed(t)
	other, err := control.NewBundle("other", "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := rpc.NewServer(ln.Addr().String(), credsOf(seed), "cid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	bad := &http.Client{Transport: &http.Transport{TLSClientConfig: mustClientTLS(t, other, "other", "")}}
	_, err = bad.Get("https://" + ln.Addr().String() + "/")
	if err == nil {
		t.Fatal("expected foreign CA to fail")
	}
}

func newSeed(t *testing.T) control.Bundle {
	t.Helper()
	b, err := control.NewBundle("cid", "seed", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func credsOf(b control.Bundle) control.AgentCreds {
	return control.AgentCreds{
		CACertPEM:    b.CACertPEM,
		AgentCertPEM: b.AgentCertPEM,
		AgentKeyPEM:  b.AgentKeyPEM,
	}
}

func mustClientTLS(t *testing.T, b control.Bundle, clusterID, expectNodeID string) *tls.Config {
	t.Helper()
	cfg, err := rpc.ClientTLS(credsOf(b), clusterID, expectNodeID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
