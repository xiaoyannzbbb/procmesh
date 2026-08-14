package control_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestNewBundle_URIContainsClusterAndNode(t *testing.T) {
	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	b, err := control.NewBundle("cid-1", "nid-1", now)
	if err != nil {
		t.Fatal(err)
	}
	cid, nid, err := control.ParseIDs(b.AgentCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if cid != "cid-1" || nid != "nid-1" {
		t.Fatalf("san=%s/%s", cid, nid)
	}
	if err := control.VerifyAgent(b.CACertPEM, b.AgentCertPEM, "cid-1", "nid-1", now); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAgent_WrongCluster(t *testing.T) {
	now := time.Now()
	b, err := control.NewBundle("cid-1", "nid-1", now)
	if err != nil {
		t.Fatal(err)
	}
	err = control.VerifyAgent(b.CACertPEM, b.AgentCertPEM, "other", "nid-1", now)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("got %v", err)
	}
}

func TestSignCSR_RoundTrip(t *testing.T) {
	now := time.Now()
	ca, err := control.NewBundle("cid", "seed", now)
	if err != nil {
		t.Fatal(err)
	}
	csr, key, err := control.NewCSR("cid", "joiner")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := control.SignCSR(ca.CACertPEM, ca.CAKeyPEM, csr, "cid", "joiner", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.VerifyAgent(ca.CACertPEM, cert, "cid", "joiner", now); err != nil {
		t.Fatal(err)
	}
	_ = key
}

func TestWriteBundle_KeyPerm0600(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	b, err := control.NewBundle("c", "n", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.WriteBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.key", "agent.key"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s perm=%o", name, perm)
		}
	}
	loaded, err := control.LoadBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.VerifyAgent(loaded.CACertPEM, loaded.AgentCertPEM, "c", "n", now); err != nil {
		t.Fatal(err)
	}
}

func TestSignCSR_RejectsMismatchedNode(t *testing.T) {
	now := time.Now()
	ca, _ := control.NewBundle("cid", "seed", now)
	csr, _, err := control.NewCSR("join", "other")
	if err != nil {
		t.Fatal(err)
	}
	_, err = control.SignCSR(ca.CACertPEM, ca.CAKeyPEM, csr, "cid", "joiner", now)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestSignCSR_RewritesPlaceholderClusterURI(t *testing.T) {
	now := time.Now()
	ca, _ := control.NewBundle("cid", "seed", now)
	csr, _, err := control.NewCSR("join", "joiner")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := control.SignCSR(ca.CACertPEM, ca.CAKeyPEM, csr, "cid", "joiner", now)
	if err != nil {
		t.Fatal(err)
	}
	cid, nid, err := control.ParseIDs(cert)
	if err != nil {
		t.Fatal(err)
	}
	if cid != "cid" || nid != "joiner" {
		t.Fatalf("rewritten uri=%s/%s", cid, nid)
	}
}
