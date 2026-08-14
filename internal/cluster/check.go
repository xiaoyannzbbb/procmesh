package cluster

import (
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/version"
)

type JoinIdentity struct {
	NodeID, BootID  string
	ProtocolVersion int
}

func CheckJoin(existing []NodeSummary, req JoinIdentity) error {
	if req.NodeID == "" {
		return errcode.E(errcode.INVALID, "node_id required")
	}
	if req.ProtocolVersion != version.Protocol {
		return errcode.E(errcode.INCOMPATIBLE_VERSION, "incompatible protocol version")
	}
	for _, n := range existing {
		if n.NodeID != req.NodeID || n.BootID == req.BootID {
			continue
		}
		switch n.State {
		case StateLeft, StateRemoved, StateRevoked:
			continue
		default:
			return errcode.E(errcode.DUPLICATE_NODE_ID, "duplicate node_id")
		}
	}
	return nil
}
