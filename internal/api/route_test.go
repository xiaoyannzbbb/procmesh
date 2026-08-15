package api

import (
	"context"
	"testing"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/version"
)

func TestRouter_TargetHeaderWins(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "aaa", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9001", ProtocolVersion: version.Protocol},
				{NodeID: "ccc", Hostname: "host-c", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9003", ProtocolVersion: version.Protocol},
			}
		},
	}
	got, err := r.Resolve(context.Background(), "ccc", "nginx", "aaa")
	if err != nil {
		t.Fatal(err)
	}
	if got.Local || got.NodeID != "ccc" || got.RPC != "127.0.0.1:9003" {
		t.Fatalf("%+v", got)
	}
}

func TestRouter_FailedOwnerUnavailable(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{
				NodeID: "ccc", State: cluster.StateFailed, RPCAddress: "127.0.0.1:9003",
				ProtocolVersion: version.Protocol,
				Processes:       []cluster.ProcessSummary{{Name: "nginx"}},
			}}
		},
		LocalHasName: func(context.Context, string) bool { return false },
	}
	_, err := r.Resolve(context.Background(), "ccc", "nginx", "")
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("%v", err)
	}
}

func TestRouter_MissingHintIsLocalCreate(t *testing.T) {
	r := Router{
		LocalID:      "aaa",
		Members:      func() []cluster.NodeSummary { return nil },
		LocalHasName: func(context.Context, string) bool { return false },
	}
	got, err := r.Resolve(context.Background(), "", "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Local || got.NodeID != "aaa" {
		t.Fatalf("%+v", got)
	}
}

func TestRouter_GossipNameFindsOwner(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{
				NodeID: "ccc", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9",
				ProtocolVersion: version.Protocol,
				Processes:       []cluster.ProcessSummary{{Name: "nginx"}},
			}}
		},
		LocalHasName: func(context.Context, string) bool { return false },
	}
	got, err := r.Resolve(context.Background(), "", "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Local || got.NodeID != "ccc" {
		t.Fatalf("%+v", got)
	}
}

func TestRouter_HostnameHintMatches(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{
				NodeID: "ccc", Hostname: "host-c", State: cluster.StateAlive,
				RPCAddress: "127.0.0.1:9003", ProtocolVersion: version.Protocol,
			}}
		},
	}
	got, err := r.Resolve(context.Background(), "host-c", "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Local || got.NodeID != "ccc" || got.RPC != "127.0.0.1:9003" {
		t.Fatalf("%+v", got)
	}
}

func TestRouter_IncompatibleVersion(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{
				NodeID: "ccc", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9003",
				ProtocolVersion: version.Protocol + 1,
			}}
		},
	}
	_, err := r.Resolve(context.Background(), "ccc", "", "")
	if !errcode.Is(err, errcode.INCOMPATIBLE_VERSION) {
		t.Fatalf("%v", err)
	}
}

func TestRouter_AmbiguousProcessOwner(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{
					NodeID: "bbb", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9002",
					ProtocolVersion: version.Protocol,
					Processes:       []cluster.ProcessSummary{{Name: "nginx"}},
				},
				{
					NodeID: "ccc", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9003",
					ProtocolVersion: version.Protocol,
					Processes:       []cluster.ProcessSummary{{Name: "nginx"}},
				},
			}
		},
		LocalHasName: func(context.Context, string) bool { return false },
	}
	_, err := r.Resolve(context.Background(), "", "nginx", "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("%v", err)
	}
}

func TestRouter_LocalHostnameIsLocal(t *testing.T) {
	r := Router{
		LocalID:   "aaa",
		LocalHost: "myhost",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{
				NodeID: "aaa", Hostname: "myhost", State: cluster.StateAlive,
				RPCAddress: "127.0.0.1:9001", ProtocolVersion: version.Protocol,
			}}
		},
	}
	got, err := r.Resolve(context.Background(), "myhost", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Local || got.NodeID != "aaa" {
		t.Fatalf("%+v", got)
	}
}
