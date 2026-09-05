package control_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestInstallCAKeyAtomicAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	bundle, err := control.NewBundle("cluster", "node", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), bundle.CACertPEM, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := control.InstallCAKey(dir, bundle.CAKeyPEM); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "ca.key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := control.InstallCAKey(dir, bundle.CAKeyPEM); err != nil {
		t.Fatalf("idempotent install: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}

	other, err := control.NewBundle("other", "node", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := control.InstallCAKey(dir, other.CAKeyPEM); !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("different key err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bundle.CAKeyPEM) {
		t.Fatal("existing CA key was overwritten")
	}
}

func TestCapabilityProofBindsChallengeAndCA(t *testing.T) {
	bundle, err := control.NewBundle("cluster", "node", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := control.CASPKIFingerprint(bundle.CACertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if len(fingerprint) != 64 || strings.ToLower(fingerprint) != fingerprint {
		t.Fatalf("fingerprint=%q", fingerprint)
	}
	challenge := control.CapabilityChallenge{
		OperationID: "op", NodeID: "node", CertSerial: "AB", CAFingerprint: fingerprint,
		Epoch: 2, LeaderTerm: 8, Nonce: "nonce", ExpiresUnix: 1_700_000_100,
	}
	proof, err := control.SignCapabilityProof(bundle.CAKeyPEM, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.VerifyCapabilityProof(bundle.CACertPEM, challenge, proof); err != nil {
		t.Fatal(err)
	}
	challenge.Nonce = "changed"
	if err := control.VerifyCapabilityProof(bundle.CACertPEM, challenge, proof); !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("changed challenge err=%v", err)
	}
}

func TestCapabilityManagerPreparesTransfersAndMarksReady(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	dir := t.TempDir()
	bundle, err := control.NewBundle("cluster", "leader", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.WriteBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	node, err := control.Start(control.RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: "leader", ClusterID: "cluster"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Shutdown() })
	if err := node.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{node}, 10*time.Second)
	apply := func(typ string, body any) {
		t.Helper()
		if err := node.Apply(mustEncode(t, typ, body), 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	apply(control.CmdBootstrap, control.BootstrapBody{ClusterID: "cluster", AdminUser: "admin", PasswordHash: "hash", AdminUserID: "admin", NowUnix: now.Unix()})
	leaderSerial, err := control.CertSerial(bundle.AgentCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "leader", CertSerial: leaderSerial, Status: control.MemberAdmitted})
	targetCSR, _, err := control.NewCSR("cluster", "target")
	if err != nil {
		t.Fatal(err)
	}
	targetCert, err := control.SignCSR(bundle.CACertPEM, bundle.CAKeyPEM, targetCSR, "cluster", "target", now)
	if err != nil {
		t.Fatal(err)
	}
	targetSerial, err := control.CertSerial(targetCert)
	if err != nil {
		t.Fatal(err)
	}
	apply(control.CmdMemberPut, control.MemberPutBody{NodeID: "target", CertSerial: targetSerial, Status: control.MemberAdmitted})

	targetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(targetDir, "ca.crt"), bundle.CACertPEM, 0o640); err != nil {
		t.Fatal(err)
	}
	manager := control.CapabilityManager{Node: node, Dir: dir, NodeID: "leader", Now: func() time.Time { return now }}
	var nonces []string
	transfer := func(_ context.Context, request control.CapabilityTransferRequest) (control.CapabilityTransferResponse, error) {
		nonces = append(nonces, request.Prepare.Nonce)
		if err := control.InstallCAKey(targetDir, request.CAKeyPEM); err != nil {
			return control.CapabilityTransferResponse{}, err
		}
		key, err := os.ReadFile(filepath.Join(targetDir, "ca.key"))
		if err != nil {
			return control.CapabilityTransferResponse{}, err
		}
		proof, err := control.SignCapabilityProof(key, control.CapabilityChallenge(request.Prepare))
		return control.CapabilityTransferResponse{Proof: proof}, err
	}
	if err := manager.Promote(context.Background(), "op-promote", "target", transfer); err != nil {
		t.Fatal(err)
	}
	ready := node.View().AdmissionCapability.Nodes["target"]
	if ready.Status != control.CapabilityReady || ready.OperationID != "op-promote" {
		t.Fatalf("target capability=%+v", ready)
	}
	if err := os.Remove(filepath.Join(targetDir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := manager.Promote(context.Background(), "op-promote", "target", transfer); err != nil {
		t.Fatalf("ready retry: %v", err)
	}
	if len(nonces) != 2 || nonces[0] == nonces[1] {
		t.Fatalf("ready retry must use a fresh proof challenge, nonces=%v", nonces)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "ca.key")); err != nil {
		t.Fatalf("ready retry did not restore target CA key: %v", err)
	}
	ready = node.View().AdmissionCapability.Nodes["target"]
	if ready.Status != control.CapabilityReady || ready.Nonce != nonces[1] {
		t.Fatalf("ready retry did not commit fresh proof: %+v", ready)
	}
	if err := os.Remove(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckReady(); !errcode.Is(err, errcode.DEGRADED) {
		t.Fatalf("missing local CA key readiness err=%v", err)
	}
}
