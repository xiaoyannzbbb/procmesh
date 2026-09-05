package cluster_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/version"
)

func TestCheckJoin_DuplicateDifferentBoot(t *testing.T) {
	err := cluster.CheckJoin([]cluster.NodeSummary{{
		NodeID: "n", BootID: "b1", State: cluster.StateAlive, ProtocolVersion: version.Protocol,
	}}, cluster.JoinIdentity{NodeID: "n", BootID: "b2", ProtocolVersion: version.Protocol})
	if !errcode.Is(err, errcode.DUPLICATE_NODE_ID) {
		t.Fatalf("got %v", err)
	}
}

func TestCheckJoin_SameBootRejoinOK(t *testing.T) {
	err := cluster.CheckJoin([]cluster.NodeSummary{{
		NodeID: "n", BootID: "b1", State: cluster.StateAlive, ProtocolVersion: version.Protocol,
	}}, cluster.JoinIdentity{NodeID: "n", BootID: "b1", ProtocolVersion: version.Protocol})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckJoin_LeftAllowsNewBoot(t *testing.T) {
	err := cluster.CheckJoin([]cluster.NodeSummary{{
		NodeID: "n", BootID: "b1", State: cluster.StateLeft, ProtocolVersion: version.Protocol,
	}}, cluster.JoinIdentity{NodeID: "n", BootID: "b2", ProtocolVersion: version.Protocol})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckJoin_MissingOSArchStillAllowed(t *testing.T) {
	err := cluster.CheckJoin([]cluster.NodeSummary{{
		NodeID: "old", BootID: "b1", State: cluster.StateAlive, ProtocolVersion: version.Protocol,
	}}, cluster.JoinIdentity{NodeID: "new", BootID: "b2", ProtocolVersion: version.Protocol})
	if err != nil {
		t.Fatalf("members without os/arch must still join: %v", err)
	}
}

func TestCheckJoin_IncompatibleVersion(t *testing.T) {
	err := cluster.CheckJoin(nil, cluster.JoinIdentity{NodeID: "n", BootID: "b", ProtocolVersion: version.Protocol + 1})
	if !errcode.Is(err, errcode.INCOMPATIBLE_VERSION) {
		t.Fatalf("got %v", err)
	}
}

func TestCheckJoin_EmptyNodeID(t *testing.T) {
	err := cluster.CheckJoin(nil, cluster.JoinIdentity{BootID: "b", ProtocolVersion: version.Protocol})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestCheckJoin_RemovedAndRevokedDenied(t *testing.T) {
	for _, st := range []cluster.State{cluster.StateRemoved, cluster.StateRevoked} {
		err := cluster.CheckJoin([]cluster.NodeSummary{{
			NodeID: "n", BootID: "b1", State: st, ProtocolVersion: 1,
		}}, cluster.JoinIdentity{NodeID: "n", BootID: "b2", ProtocolVersion: version.Protocol})
		if !errcode.Is(err, errcode.DENIED) {
			t.Fatalf("state %s: got %v", st, err)
		}
		if err == nil || err.Error() != "DENIED: node removed" {
			t.Fatalf("state %s: want node removed: %v", st, err)
		}
	}
}

func TestMesh_DoesNotOverwriteLocalOnDuplicateNodeID(t *testing.T) {
	src := &staticSource{s: cluster.NodeSummary{NodeID: "n", BootID: "b-local", Hostname: "me", State: cluster.StateAlive, ProtocolVersion: 1}}
	m, err := cluster.Start(cluster.Config{NodeID: "n", BindAddr: "127.0.0.1", Source: src, Protocol: 1, TestFast: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown() })
	m.MergeForTest(cluster.EncodeState(cluster.NodeSummary{NodeID: "n", BootID: "b-clone", Hostname: "clone", ProtocolVersion: 1}))
	locals := m.Members()
	var self cluster.NodeSummary
	for _, x := range locals {
		if x.NodeID == "n" && x.BootID == "b-local" {
			self = x
		}
	}
	if self.Hostname != "me" {
		t.Fatalf("local overwritten: %+v", locals)
	}
	if len(m.DuplicateConflicts()) == 0 {
		t.Fatal("expected duplicate conflict")
	}
}
