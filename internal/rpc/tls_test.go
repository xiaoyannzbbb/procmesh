package rpc_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
)

func TestClientTLS_AcceptsSameCluster(t *testing.T) {
	now := time.Now()
	seed, err := control.NewBundle("cid", "seed", now)
	if err != nil {
		t.Fatal(err)
	}
	peer := signPeer(t, seed, "cid", "owner", now)
	cfg, err := rpc.ClientTLS(control.AgentCreds{
		CACertPEM:    seed.CACertPEM,
		AgentCertPEM: seed.AgentCertPEM,
		AgentKeyPEM:  seed.AgentKeyPEM,
	}, "cid", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLeaf(cfg, peer.AgentCertPEM); err != nil {
		t.Fatal(err)
	}
}

func TestClientTLS_RejectsOtherCluster(t *testing.T) {
	now := time.Now()
	a, err := control.NewBundle("cid-a", "a", now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := control.NewBundle("cid-b", "b", now)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := rpc.ClientTLS(control.AgentCreds{
		CACertPEM:    a.CACertPEM,
		AgentCertPEM: a.AgentCertPEM,
		AgentKeyPEM:  a.AgentKeyPEM,
	}, "cid-a", "")
	if err != nil {
		t.Fatal(err)
	}
	err = verifyLeaf(cfg, b.AgentCertPEM)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("got %v", err)
	}
}

func TestServerTLS_RevokedSerialDenied(t *testing.T) {
	now := time.Now()
	seed, err := control.NewBundle("cid", "seed", now)
	if err != nil {
		t.Fatal(err)
	}
	okPeer := signPeer(t, seed, "cid", "ok", now)
	badPeer := signPeer(t, seed, "cid", "bad", now)
	badSerial, err := control.CertSerial(badPeer.AgentCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := rpc.ServerTLS(credsOf(seed), "cid", func(s string) bool {
		return s == badSerial
	})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyLeaf(cfg, badPeer.AgentCertPEM)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("want revoked: %v", err)
	}
	if err := verifyLeaf(cfg, okPeer.AgentCertPEM); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := rpc.NewServer(ln.Addr().String(), credsOf(seed), "cid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), func(s string) bool { return s == badSerial })
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	badClient := &http.Client{Transport: &http.Transport{TLSClientConfig: mustClientTLS(t, badPeer, "cid", "")}}
	_, err = badClient.Get("https://" + ln.Addr().String() + "/")
	if err == nil {
		t.Fatal("expected revoked dial to fail")
	}
	if !strings.Contains(err.Error(), "revoked") && !strings.Contains(err.Error(), "DENIED") &&
		!strings.Contains(err.Error(), "certificate") {
		t.Fatalf("dial err=%v", err)
	}
}

func signPeer(t *testing.T, ca control.Bundle, clusterID, nodeID string, now time.Time) control.Bundle {
	t.Helper()
	csr, key, err := control.NewCSR(clusterID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := control.SignCSR(ca.CACertPEM, ca.CAKeyPEM, csr, clusterID, nodeID, now)
	if err != nil {
		t.Fatal(err)
	}
	return control.Bundle{CACertPEM: ca.CACertPEM, AgentCertPEM: cert, AgentKeyPEM: key}
}

func verifyLeaf(cfg *tls.Config, leafPEM []byte) error {
	block, _ := pem.Decode(leafPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if cfg.VerifyPeerCertificate == nil {
		return fmt.Errorf("VerifyPeerCertificate required")
	}
	return cfg.VerifyPeerCertificate([][]byte{cert.Raw}, nil)
}
