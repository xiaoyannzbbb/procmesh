package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/version"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func TestRemoveNode_AddsCRLAndDeniesRejoin(t *testing.T) {
	ctx := context.Background()
	raftNode := startTestRaft(t, "seed")
	e := newClusterEnvFull(t, clusterEnvCfg{
		withMesh: true,
		control:  raftNode,
		onAdmit: func(nodeID, raftAddr string) error {
			return raftNode.AddNonvoter(nodeID, raftAddr)
		},
	})
	e.init(t)
	adm := control.Admission{Node: raftNode}
	plain, _, err := adm.CreateToken(time.Hour, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const joinerID = "peer-1"
	csr, _, err := control.NewCSR("join", joinerID)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-join-peer", Operator: "t"},
		Token:           plain,
		NodeId:          joinerID,
		Hostname:        "peer-host",
		BootId:          "boot-peer",
		ProtocolVersion: int32(version.Protocol),
		RaftAddress:     "127.0.0.1:19012",
		CsrPem:          csr,
	}))
	if err != nil {
		t.Fatal(err)
	}
	serial, err := control.CertSerial(joined.Msg.GetCertPem())
	if err != nil {
		t.Fatal(err)
	}
	if raftNode.View().SerialRevoked(serial) {
		t.Fatal("serial should not be revoked before remove")
	}

	if _, err := e.node.RemoveNode(ctx, connect.NewRequest(&procmeshv1.RemoveNodeRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-rm-peer", Operator: "t"},
		NodeId: joinerID,
	})); err != nil {
		t.Fatal(err)
	}
	if !raftNode.View().SerialRevoked(serial) {
		t.Fatal("expected serial on CRL")
	}
	view := raftNode.View()
	m, ok := view.Member(joinerID)
	if !ok || m.Status != control.MemberRevoked {
		t.Fatalf("member=%+v ok=%v", m, ok)
	}

	_, err = e.node.RemoveNode(ctx, connect.NewRequest(&procmeshv1.RemoveNodeRequest{
		Meta:   &procmeshv1.MutationMeta{OperationId: "op-rm-self", Operator: "t"},
		NodeId: e.nodeID,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("self-remove code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "cannot remove self") {
		t.Fatalf("want cannot remove self: %v", err)
	}

	csr2, _, err := control.NewCSR("join", joinerID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cluster.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            &procmeshv1.MutationMeta{OperationId: "op-rejoin", Operator: "t"},
		Token:           plain,
		NodeId:          joinerID,
		BootId:          "boot-peer-2",
		ProtocolVersion: int32(version.Protocol),
		CsrPem:          csr2,
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodePermissionDenied || detail != "DENIED" {
		t.Fatalf("rejoin code=%v detail=%s err=%v", code, detail, err)
	}
	if !strings.Contains(err.Error(), "node removed") {
		t.Fatalf("want node removed: %v", err)
	}
}
