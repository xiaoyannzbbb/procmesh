package api

import (
	"context"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/version"
)

// Route is the Write-to-Owner destination for a mutation.
type Route struct {
	Local  bool
	NodeID string
	RPC    string
}

// Membership exposes cluster members for route resolution.
type Membership interface {
	Members() []cluster.NodeSummary
}

// Router resolves the owner node for process/config writes.
// LocalHasName nil is treated as always false. Members nil yields no members.
type Router struct {
	LocalID      string
	LocalHost    string
	Members      func() []cluster.NodeSummary
	LocalHasName func(ctx context.Context, idOrName string) bool
}

// Resolve picks the owner route without issuing RPC.
//
// Order:
//  1. targetHint: node_id exact, else unique hostname
//  2. else ownerAgentID: node_id
//  3. else LocalHasName → local
//  4. else Gossip Processes[].Name (1→that node, many→INVALID, 0→local create)
//  5. target is LocalID / LocalHost → local
//  6. FAILED or empty RPC → UNAVAILABLE
//  7. ProtocolVersion mismatch → INCOMPATIBLE_VERSION
//  8. targetHint given but not in membership → UNAVAILABLE
func (r Router) Resolve(ctx context.Context, targetHint, idOrName, ownerAgentID string) (Route, error) {
	members := r.members()

	if targetHint != "" {
		if r.isLocalIdentity(targetHint) {
			return r.localRoute(), nil
		}
		n, ok := findMemberByNodeID(members, targetHint)
		if !ok {
			n, ok = findMemberByUniqueHostname(members, targetHint)
		}
		if !ok {
			return Route{}, errcode.E(errcode.UNAVAILABLE, "owner not found")
		}
		return r.routeForNode(n)
	}

	if ownerAgentID != "" {
		if r.isLocalIdentity(ownerAgentID) {
			return r.localRoute(), nil
		}
		n, ok := findMemberByNodeID(members, ownerAgentID)
		if !ok {
			return Route{}, errcode.E(errcode.UNAVAILABLE, "owner not found")
		}
		return r.routeForNode(n)
	}

	if idOrName != "" && r.localHasName(ctx, idOrName) {
		return r.localRoute(), nil
	}

	if idOrName != "" {
		owners := findMembersByProcessName(members, idOrName)
		switch len(owners) {
		case 0:
			return r.localRoute(), nil
		case 1:
			return r.routeForNode(owners[0])
		default:
			return Route{}, errcode.E(errcode.INVALID, "ambiguous process owner")
		}
	}

	return r.localRoute(), nil
}

func (r Router) routeForNode(n cluster.NodeSummary) (Route, error) {
	if n.NodeID == r.LocalID || (r.LocalHost != "" && n.Hostname == r.LocalHost) {
		return r.localRoute(), nil
	}
	if n.State == cluster.StateFailed || n.RPCAddress == "" {
		return Route{}, errcode.E(errcode.UNAVAILABLE, "owner unreachable")
	}
	if n.ProtocolVersion != version.Protocol {
		return Route{}, errcode.E(errcode.INCOMPATIBLE_VERSION, "incompatible protocol version")
	}
	return Route{Local: false, NodeID: n.NodeID, RPC: n.RPCAddress}, nil
}

func (r Router) localRoute() Route {
	return Route{Local: true, NodeID: r.LocalID}
}

func (r Router) isLocalIdentity(idOrHost string) bool {
	if idOrHost == "" {
		return false
	}
	if idOrHost == r.LocalID {
		return true
	}
	return r.LocalHost != "" && idOrHost == r.LocalHost
}

func (r Router) members() []cluster.NodeSummary {
	if r.Members == nil {
		return nil
	}
	return r.Members()
}

func (r Router) localHasName(ctx context.Context, idOrName string) bool {
	if r.LocalHasName == nil {
		return false
	}
	return r.LocalHasName(ctx, idOrName)
}

func findMemberByNodeID(members []cluster.NodeSummary, nodeID string) (cluster.NodeSummary, bool) {
	for _, n := range members {
		if n.NodeID == nodeID {
			return n, true
		}
	}
	return cluster.NodeSummary{}, false
}

func findMemberByUniqueHostname(members []cluster.NodeSummary, host string) (cluster.NodeSummary, bool) {
	var found cluster.NodeSummary
	count := 0
	for _, n := range members {
		if n.Hostname == host {
			found = n
			count++
			if count > 1 {
				return cluster.NodeSummary{}, false
			}
		}
	}
	if count == 1 {
		return found, true
	}
	return cluster.NodeSummary{}, false
}

func findMembersByProcessName(members []cluster.NodeSummary, name string) []cluster.NodeSummary {
	var out []cluster.NodeSummary
	for _, n := range members {
		for _, p := range n.Processes {
			if p.Name == name {
				out = append(out, n)
				break
			}
		}
	}
	return out
}
