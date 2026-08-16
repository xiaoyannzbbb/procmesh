package batch

import (
	"context"
	"regexp"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
)

var processGroupRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// NodeView is a gossip snapshot of one agent and its advertised processes.
type NodeView struct {
	NodeID    string
	Processes []ProcView
}

// ProcView is a gossip process summary used only for candidate prefilter.
type ProcView struct {
	ProcessID      string
	Name           string
	Group          string
	LatestRevision int64
}

// OwnerSpec is the owner-authoritative process record (Get).
type OwnerSpec struct {
	ProcessID      string
	Name           string
	NodeID         string
	Group          string
	LatestRevision int64
	SpecJSON       string
}

// ClusterView is the current gossip membership snapshot.
type ClusterView interface {
	Nodes() []NodeView
}

// GroupMembers resolves agent-group membership. Unknown groups return INVALID.
type GroupMembers interface {
	Members(groupID string) ([]string, error)
}

// SpecReader loads owner-authoritative process spec by node + id or name.
type SpecReader interface {
	Get(ctx context.Context, nodeID, idOrName string) (OwnerSpec, error)
}

// Authorizer checks per-target process permission. DENIED must be returned as errcode.DENIED.
type Authorizer interface {
	Allow(nodeID, processGroup, perm string) error
}

// RealExpander snapshots selector candidates against gossip + owner Get.
type RealExpander struct {
	Cluster       ClusterView
	Groups        GroupMembers
	Specs         SpecReader
	Auth          Authorizer
	ConfigOverlay func(OwnerSpec) (payloadJSON string, expected int64, err error)
}

type lookupHint struct {
	nodeID       string
	processID    string
	processName  string
	requireGroup string
	dropMissing  bool
}

type targetAcc struct {
	out  []Target
	seen map[string]struct{}
}

// Expand resolves sel into concrete targets. DENIED/INVALID stay in the result.
// Illegal process_group names and unknown agent groups fail Expand.
func (x *RealExpander) Expand(ctx context.Context, sel Selector, typ Type) ([]Target, error) {
	pg := strings.TrimSpace(sel.ProcessGroup)
	if sel.ProcessGroup != "" && !processGroupRE.MatchString(pg) {
		return nil, errcode.E(errcode.INVALID, "process group")
	}

	var members []string
	if sel.AgentGroupID != "" {
		gid := strings.TrimSpace(sel.AgentGroupID)
		if gid == "" || x.Groups == nil {
			return nil, errcode.E(errcode.INVALID, "agent group")
		}
		var err error
		members, err = x.Groups.Members(gid)
		if err != nil {
			return nil, err
		}
	}

	acc := &targetAcc{seen: make(map[string]struct{})}
	nodes := x.nodes()

	if err := x.expandProcessIDs(ctx, sel.ProcessIDs, typ, nodes, acc); err != nil {
		return nil, err
	}
	if err := x.expandProcessNames(ctx, sel.ProcessNames, typ, acc); err != nil {
		return nil, err
	}
	if sel.AgentGroupID != "" {
		if err := x.expandAgentGroup(ctx, members, typ, nodes, acc); err != nil {
			return nil, err
		}
	}
	if pg != "" {
		if err := x.expandProcessGroup(ctx, pg, typ, nodes, acc); err != nil {
			return nil, err
		}
	}
	return acc.out, nil
}

func (x *RealExpander) expandProcessIDs(ctx context.Context, ids []string, typ Type, nodes []NodeView, acc *targetAcc) error {
	ownerOf := map[string]string{}
	for _, n := range nodes {
		for _, p := range n.Processes {
			if p.ProcessID == "" {
				continue
			}
			if _, ok := ownerOf[p.ProcessID]; !ok {
				ownerOf[p.ProcessID] = n.NodeID
			}
		}
	}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if nodeID, ok := ownerOf[id]; ok {
			if err := x.addFromGet(ctx, nodeID, id, typ, acc, lookupHint{nodeID: nodeID, processID: id}); err != nil {
				return err
			}
			continue
		}
		found := false
		for _, n := range nodes {
			spec, err := x.getSpec(ctx, n.NodeID, id)
			if err == nil {
				if spec.NodeID == "" {
					spec.NodeID = n.NodeID
				}
				if spec.ProcessID == "" {
					spec.ProcessID = id
				}
				t, err := x.finish(spec, typ, "", "")
				if err != nil {
					return err
				}
				acc.add(t)
				found = true
				break
			}
			if !errcode.Is(err, errcode.NOT_FOUND) {
				return err
			}
		}
		if !found {
			acc.add(Target{ProcessID: id, Status: TargetInvalid, Error: "process not found"})
		}
	}
	return nil
}

func (x *RealExpander) expandProcessNames(ctx context.Context, refs []ProcessNameRef, typ Type, acc *targetAcc) error {
	for _, ref := range refs {
		node := strings.TrimSpace(ref.NodeID)
		name := strings.TrimSpace(ref.ProcessName)
		if node == "" && name == "" {
			continue
		}
		if err := x.addFromGet(ctx, node, name, typ, acc, lookupHint{nodeID: node, processName: name}); err != nil {
			return err
		}
	}
	return nil
}

func (x *RealExpander) expandAgentGroup(ctx context.Context, members []string, typ Type, nodes []NodeView, acc *targetAcc) error {
	want := make(map[string]struct{}, len(members))
	for _, id := range members {
		want[id] = struct{}{}
	}
	for _, n := range nodes {
		if _, ok := want[n.NodeID]; !ok {
			continue
		}
		for _, p := range n.Processes {
			if p.ProcessID == "" {
				continue
			}
			if err := x.addFromGet(ctx, n.NodeID, p.ProcessID, typ, acc, lookupHint{
				nodeID: n.NodeID, processID: p.ProcessID, dropMissing: true,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (x *RealExpander) expandProcessGroup(ctx context.Context, name string, typ Type, nodes []NodeView, acc *targetAcc) error {
	for _, n := range nodes {
		for _, p := range n.Processes {
			if p.Group != name || p.ProcessID == "" {
				continue
			}
			if err := x.addFromGet(ctx, n.NodeID, p.ProcessID, typ, acc, lookupHint{
				nodeID: n.NodeID, processID: p.ProcessID, processName: p.Name, requireGroup: name,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (x *RealExpander) addFromGet(ctx context.Context, node, idOrName string, typ Type, acc *targetAcc, h lookupHint) error {
	spec, err := x.getSpec(ctx, node, idOrName)
	if err != nil {
		if errcode.Is(err, errcode.NOT_FOUND) {
			if h.dropMissing {
				return nil
			}
			acc.add(Target{
				NodeID:      h.nodeID,
				ProcessID:   h.processID,
				ProcessName: h.processName,
				Status:      TargetInvalid,
				Error:       "process not found",
			})
			return nil
		}
		return err
	}
	if spec.NodeID == "" {
		spec.NodeID = node
	}
	if spec.ProcessID == "" {
		spec.ProcessID = h.processID
	}
	status := TargetStatus("")
	errMsg := ""
	if h.requireGroup != "" && spec.Group != h.requireGroup {
		status = TargetInvalid
		errMsg = "group mismatch"
	}
	t, err := x.finish(spec, typ, status, errMsg)
	if err != nil {
		return err
	}
	acc.add(t)
	return nil
}

func (x *RealExpander) finish(spec OwnerSpec, typ Type, status TargetStatus, errMsg string) (Target, error) {
	t := Target{
		NodeID:      spec.NodeID,
		ProcessID:   spec.ProcessID,
		ProcessName: spec.Name,
		Status:      status,
		Error:       errMsg,
	}
	if t.Status == TargetInvalid {
		return t, nil
	}
	if x.Auth != nil {
		if err := x.Auth.Allow(spec.NodeID, spec.Group, permForType(typ)); err != nil {
			if errcode.Is(err, errcode.DENIED) {
				t.Status = TargetDenied
				t.Error = err.Error()
				return t, nil
			}
			return Target{}, err
		}
	}
	if typ != TypeConfigUpdate {
		return t, nil
	}
	if x.ConfigOverlay == nil {
		t.Status = TargetInvalid
		t.Error = "config overlay"
		return t, nil
	}
	payload, rev, err := x.ConfigOverlay(spec)
	if err != nil {
		t.Status = TargetInvalid
		t.Error = err.Error()
		return t, nil
	}
	t.PayloadJSON = payload
	t.ExpectedRevision = rev
	return t, nil
}

func (x *RealExpander) getSpec(ctx context.Context, node, id string) (OwnerSpec, error) {
	if x.Specs == nil {
		return OwnerSpec{}, errcode.E(errcode.NOT_FOUND, "process")
	}
	return x.Specs.Get(ctx, node, id)
}

func (x *RealExpander) nodes() []NodeView {
	if x.Cluster == nil {
		return nil
	}
	return x.Cluster.Nodes()
}

func (a *targetAcc) add(t Target) {
	key := t.NodeID + "\x00" + t.ProcessID
	if t.ProcessID == "" {
		key += "\x00" + t.ProcessName
	}
	if _, ok := a.seen[key]; ok {
		return
	}
	a.seen[key] = struct{}{}
	a.out = append(a.out, t)
}

func permForType(typ Type) string {
	switch typ {
	case TypeStart:
		return "process.start"
	case TypeStop:
		return "process.stop"
	case TypeRestart:
		return "process.restart"
	case TypeConfigUpdate:
		return "process.config.update"
	default:
		return ""
	}
}
