package agent

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
)

func TestEnsureLocalMemberAdmitted_ExistingClusterMissingMember(t *testing.T) {
	clusterDir := t.TempDir()
	now := time.Now()
	res, err := control.Init(clusterDir, "node-init", "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	n, err := control.Start(control.RaftConfig{
		Dir:    filepath.Join(clusterDir, "raft"),
		Bind:   "127.0.0.1:0",
		NodeID: "node-init",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := waitRaftLeader(n, raftStartTO); err != nil {
		t.Fatal(err)
	}
	_, hash, err := control.LoadAdminBootstrap(clusterDir)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := control.EncodeCommand(control.CmdBootstrap, control.BootstrapBody{
		ClusterID:    res.ClusterID,
		AdminUser:    "admin",
		PasswordHash: hash,
		AdminUserID:  adminUserID,
		NowUnix:      now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Apply(cmd, raftApplyTO); err != nil {
		t.Fatal(err)
	}
	view := n.View()
	if view.ClusterID == "" {
		t.Fatal("precondition: ClusterID must be set")
	}
	if _, ok := view.Member("node-init"); ok {
		t.Fatal("precondition: local node must not already be a member")
	}

	r := &rpcRuntime{dir: clusterDir, node: n, logger: slog.New(slog.DiscardHandler)}
	if err := r.ensureLocalMemberAdmitted(); err != nil {
		t.Fatal(err)
	}
	view = n.View()
	m, ok := view.Member("node-init")
	if !ok || m.Status != control.MemberAdmitted {
		t.Fatalf("want ADMITTED member, got %+v ok=%v", m, ok)
	}
	capability, ok := view.AdmissionCapability.Nodes["node-init"]
	if !ok || capability.Status != control.CapabilityReady {
		t.Fatalf("want READY admission capability, got %+v ok=%v", capability, ok)
	}
	if err := r.ensureLocalMemberAdmitted(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureLocalMemberAdmitted_KeylessLegacyVoterStillStarts(t *testing.T) {
	clusterDir := t.TempDir()
	now := time.Now()
	res, err := control.Init(clusterDir, "node-init", "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	n, err := control.Start(control.RaftConfig{
		Dir:    filepath.Join(clusterDir, "raft"),
		Bind:   "127.0.0.1:0",
		NodeID: "node-init",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := waitRaftLeader(n, raftStartTO); err != nil {
		t.Fatal(err)
	}
	_, hash, err := control.LoadAdminBootstrap(clusterDir)
	if err != nil {
		t.Fatal(err)
	}
	command, err := control.EncodeCommand(control.CmdBootstrap, control.BootstrapBody{
		ClusterID:    res.ClusterID,
		AdminUser:    "admin",
		PasswordHash: hash,
		AdminUserID:  adminUserID,
		NowUnix:      now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Apply(command, raftApplyTO); err != nil {
		t.Fatal(err)
	}
	if err := admitBootstrapMember(n, clusterDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(clusterDir, "ca.key")); err != nil {
		t.Fatal(err)
	}

	r := &rpcRuntime{dir: clusterDir, node: n, logger: slog.New(slog.DiscardHandler)}
	if err := r.ensureLocalMemberAdmitted(); err != nil {
		t.Fatalf("keyless legacy voter must still start: %v", err)
	}
	if got := n.View().AdmissionCapability.CAFingerprint; got != "" {
		t.Fatalf("capability fingerprint=%q, want uninitialized", got)
	}
}
