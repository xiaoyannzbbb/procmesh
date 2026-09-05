package control_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

type capabilityManagerFixture struct {
	now    time.Time
	dir    string
	bundle control.Bundle
	node   *control.Node
}

func newCapabilityManagerFixture(t *testing.T) *capabilityManagerFixture {
	t.Helper()
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
	return &capabilityManagerFixture{now: now, dir: dir, bundle: bundle, node: node}
}

func (f *capabilityManagerFixture) manager() control.CapabilityManager {
	return control.CapabilityManager{Node: f.node, Dir: f.dir, NodeID: "leader", Now: func() time.Time { return f.now }}
}

func (f *capabilityManagerFixture) targetDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), f.bundle.CACertPEM, 0o640); err != nil {
		t.Fatal(err)
	}
	return dir
}

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

func TestInstallCAKeyContextCanceledDoesNotPersistKey(t *testing.T) {
	dir := t.TempDir()
	bundle, err := control.NewBundle("cluster", "node", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), bundle.CACertPEM, 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := control.InstallCAKeyContext(ctx, dir, bundle.CAKeyPEM); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled install err=%v want context canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); !os.IsNotExist(err) {
		t.Fatalf("canceled install persisted CA key: %v", err)
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
	fixture := newCapabilityManagerFixture(t)
	targetDir := fixture.targetDir(t)
	manager := fixture.manager()
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
	ready := fixture.node.View().AdmissionCapability.Nodes["target"]
	if ready.Status != control.CapabilityReady || ready.OperationID != "op-promote" {
		t.Fatalf("target capability=%+v", ready)
	}
	if err := os.Remove(filepath.Join(targetDir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(2 * time.Minute)
	if err := manager.Promote(context.Background(), "op-promote", "target", transfer); err != nil {
		t.Fatalf("ready retry: %v", err)
	}
	if len(nonces) != 2 || nonces[0] == nonces[1] {
		t.Fatalf("ready retry must use a fresh proof challenge, nonces=%v", nonces)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "ca.key")); err != nil {
		t.Fatalf("ready retry did not restore target CA key: %v", err)
	}
	ready = fixture.node.View().AdmissionCapability.Nodes["target"]
	if ready.Status != control.CapabilityReady || ready.Nonce != nonces[1] {
		t.Fatalf("ready retry did not commit fresh proof: %+v", ready)
	}
	if err := os.Remove(filepath.Join(fixture.dir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckReady(); !errcode.Is(err, errcode.DEGRADED) {
		t.Fatalf("missing local CA key readiness err=%v", err)
	}
}

func TestCapabilityManagerLeadershipLossCancelsKeyTransfer(t *testing.T) {
	fixture := newCapabilityManagerFixture(t)
	targetDir := fixture.targetDir(t)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- fixture.manager().Promote(context.Background(), "op-leadership-loss", "target", func(ctx context.Context, request control.CapabilityTransferRequest) (control.CapabilityTransferResponse, error) {
			close(started)
			select {
			case <-ctx.Done():
				installErr := control.InstallCAKeyContext(ctx, targetDir, request.CAKeyPEM)
				return control.CapabilityTransferResponse{}, installErr
			case <-time.After(300 * time.Millisecond):
			}
			if err := control.InstallCAKey(targetDir, request.CAKeyPEM); err != nil {
				return control.CapabilityTransferResponse{}, err
			}
			key, err := os.ReadFile(filepath.Join(targetDir, "ca.key"))
			if err != nil {
				return control.CapabilityTransferResponse{}, err
			}
			proof, err := control.SignCapabilityProof(key, control.CapabilityChallenge(request.Prepare))
			return control.CapabilityTransferResponse{Proof: proof}, err
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("capability transfer did not start")
	}
	if err := fixture.node.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("promote after leadership loss err=%v want CONFLICT", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "ca.key")); !os.IsNotExist(err) {
		t.Fatalf("leadership loss installed CA key: %v", err)
	}
}
