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

	if err := a.Admit("joiner", "127.0.0.1:9002", "abcd"); err != nil {
		t.Fatal(err)
	}
	m, ok := n.View().Members["joiner"]
	if !ok || m.Status != control.MemberAdmitted || m.RaftAddr != "127.0.0.1:9002" || m.CertSerial != "ABCD" {
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
