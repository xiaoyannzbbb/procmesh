package control

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func TestValidateRaftAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:18685", "raft.internal:18685", "[2001:db8::1]:18685"} {
		t.Run("valid_"+strings.ReplaceAll(addr, ":", "_"), func(t *testing.T) {
			if err := ValidateRaftAddress(addr); err != nil {
				t.Fatalf("ValidateRaftAddress(%q): %v", addr, err)
			}
		})
	}
	for _, addr := range []string{"", "raft.internal", ":18685", "0.0.0.0:18685", "[::]:18685", "raft.internal:0", "raft.internal:bad"} {
		t.Run("invalid_"+strings.ReplaceAll(addr, ":", "_"), func(t *testing.T) {
			if err := ValidateRaftAddress(addr); err == nil {
				t.Fatalf("ValidateRaftAddress(%q) succeeded", addr)
			}
		})
	}
}

func TestIsUnspecifiedRaftAddress(t *testing.T) {
	for _, addr := range []string{":18685", "0.0.0.0:18685", "[::]:18685"} {
		if !IsUnspecifiedRaftAddress(addr) {
			t.Fatalf("IsUnspecifiedRaftAddress(%q)=false", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:18685", "raft.internal:18685", "legacy-inmem-address"} {
		if IsUnspecifiedRaftAddress(addr) {
			t.Fatalf("IsUnspecifiedRaftAddress(%q)=true", addr)
		}
	}
}

func TestRaftTransport_UnspecifiedPersistedPeerDoesNotDisruptLeader(t *testing.T) {
	n, err := Start(RaftConfig{
		Dir:    t.TempDir(),
		Bind:   "127.0.0.1:0",
		NodeID: "seed",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLocalRaftLeader(t, n)

	_, port, err := net.SplitHostPort(n.Advertise())
	if err != nil {
		t.Fatal(err)
	}
	// Bypass ProcMesh admission to reproduce a wildcard address persisted by
	// an older version.
	if err := n.raft.AddNonvoter("bad-peer", raft.ServerAddress(net.JoinHostPort("0.0.0.0", port)), 0, time.Second).Error(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !n.IsLeader() {
			t.Fatal("leader stepped down after dialing wildcard peer address")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !n.HasQuorum() {
		t.Fatal("leader lost quorum after dialing wildcard peer address")
	}
}

func waitLocalRaftLeader(t *testing.T, n *Node) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !n.IsLeader() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for raft leader")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
