package control_test

import (
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestAdmission_CreateConsumeRevoke(t *testing.T) {
	n, err := control.Start(control.RaftConfig{
		Dir:    t.TempDir(),
		Bind:   "127.0.0.1:0",
		NodeID: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{n}, 10*time.Second)

	now := time.Now()
	a := control.Admission{Node: n}
	plain, info, err := a.CreateToken(time.Hour, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "pmj_") || info.ID == "" || info.Remaining != 1 {
		t.Fatalf("plain=%q info=%+v", plain, info)
	}
	tok, ok := n.View().JoinTokens[info.ID]
	if !ok || tok.Remaining != 1 || tok.Revoked {
		t.Fatalf("stored=%+v ok=%v", tok, ok)
	}
	if err := a.ConsumeToken(plain, now); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsumeToken(plain, now); !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("second consume: %v", err)
	}

	plain2, info2, err := a.CreateToken(time.Hour, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeToken(info2.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsumeToken(plain2, now); !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("revoked consume: %v", err)
	}
	if !n.View().JoinTokens[info2.ID].Revoked {
		t.Fatal("expected revoked")
	}

	if err := a.Admit("joiner", "127.0.0.1:18685", "abcd"); err != nil {
		t.Fatal(err)
	}
	m, ok := n.View().Members["joiner"]
	if !ok || m.Status != control.MemberAdmitted || m.RaftAddr != "127.0.0.1:18685" || m.CertSerial != "ABCD" {
		t.Fatalf("member=%+v ok=%v", m, ok)
	}
	if a.IsRevoked("joiner") {
		t.Fatal("admitted node should not be revoked")
	}
	cmd, err := control.EncodeCommand(control.CmdMemberRemove, control.MemberRemoveBody{NodeID: "joiner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Apply(cmd, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if !a.IsRevoked("joiner") {
		t.Fatal("expected revoked after member_remove")
	}
}

func TestAdmission_PrepareJoinIsAtomicAndIdempotent(t *testing.T) {
	n, err := control.Start(control.RaftConfig{
		Dir:    t.TempDir(),
		Bind:   "127.0.0.1:0",
		NodeID: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{n}, 10*time.Second)

	a := control.Admission{Node: n}
	plain, info, err := a.CreateToken(time.Hour, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bad := control.JoinPrepare{
		OperationID: "op-join",
		Token:       plain,
		RaftAddr:    "127.0.0.1:18685",
		CSRHash:     "csr-sha256",
		CertPEM:     []byte("certificate"),
		CertSerial:  "abcd",
	}
	if _, err := a.PrepareJoin(bad); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("invalid prepare err=%v", err)
	}
	if got := n.View().JoinTokens[info.ID].Remaining; got != 1 {
		t.Fatalf("invalid prepare consumed token: remaining=%d", got)
	}

	prepare := bad
	prepare.NodeID = "joiner"
	attempt, err := a.PrepareJoin(prepare)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.OperationID != prepare.OperationID || attempt.NodeID != prepare.NodeID || attempt.Status != control.JoinPreparing {
		t.Fatalf("attempt=%+v", attempt)
	}
	view := n.View()
	if got := view.JoinTokens[info.ID].Remaining; got != 0 {
		t.Fatalf("remaining=%d want 0", got)
	}
	member, ok := view.Member(prepare.NodeID)
	if !ok || member.Status != control.MemberJoining || member.CertSerial != "ABCD" {
		t.Fatalf("member=%+v ok=%v", member, ok)
	}

	replayed, err := a.PrepareJoin(prepare)
	if err != nil {
		t.Fatalf("idempotent prepare: %v", err)
	}
	if replayed.CertSerial != attempt.CertSerial || string(replayed.CertPEM) != string(attempt.CertPEM) {
		t.Fatalf("replayed=%+v want=%+v", replayed, attempt)
	}
	if got := n.View().JoinTokens[info.ID].Remaining; got != 0 {
		t.Fatalf("replay consumed token again: remaining=%d", got)
	}

	if err := a.CompleteJoin(prepare.OperationID, prepare.NodeID); err != nil {
		t.Fatal(err)
	}
	view = n.View()
	if view.Members[prepare.NodeID].Status != control.MemberAdmitted || view.JoinAttempts[prepare.NodeID].Status != control.JoinCompleted {
		t.Fatalf("completed member=%+v attempt=%+v", view.Members[prepare.NodeID], view.JoinAttempts[prepare.NodeID])
	}
	remove, err := control.EncodeCommand(control.CmdMemberRemove, control.MemberRemoveBody{NodeID: prepare.NodeID})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Apply(remove, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareJoin(prepare); !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("replay after removal err=%v want DENIED", err)
	}
}

func TestAdmission_PrepareJoinOperationIDIsUniqueAcrossNodes(t *testing.T) {
	n, err := control.Start(control.RaftConfig{
		Dir:    t.TempDir(),
		Bind:   "127.0.0.1:0",
		NodeID: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{n}, 10*time.Second)

	a := control.Admission{Node: n}
	plain, info, err := a.CreateToken(time.Hour, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first := control.JoinPrepare{
		OperationID: "op-shared", Token: plain, NodeID: "node-a", RaftAddr: "node-a-raft",
		CSRHash: "csr-a", CertPEM: []byte("cert-a"), CertSerial: "AA",
	}
	if _, err := a.PrepareJoin(first); err != nil {
		t.Fatal(err)
	}
	second := control.JoinPrepare{
		OperationID: "op-shared", Token: plain, NodeID: "node-b", RaftAddr: "node-b-raft",
		CSRHash: "csr-b", CertPEM: []byte("cert-b"), CertSerial: "BB",
	}
	if _, err := a.PrepareJoin(second); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("second node with reused operation_id err=%v want CONFLICT", err)
	}
	view := n.View()
	if got := view.JoinTokens[info.ID].Remaining; got != 1 {
		t.Fatalf("reused operation_id consumed token: remaining=%d want 1", got)
	}
	if _, exists := view.JoinAttempts[second.NodeID]; exists {
		t.Fatal("reused operation_id created a second join attempt")
	}
}
