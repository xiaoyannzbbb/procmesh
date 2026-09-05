package api

import (
	"context"
	"errors"
	"net/http/httptrace"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.NodeServiceHandler = (*NodeAPI)(nil)

type NodeForwarder interface {
	Node(context.Context, Route) (procmeshv1connect.NodeServiceClient, error)
}

type CapabilityForwarder interface {
	PromoteCapability(context.Context, Route, control.CapabilityTransferRequest) (control.CapabilityTransferResponse, error)
}

type NodeAPI struct {
	Deps        ClusterDeps
	Auth        *auth.Service
	Degraded    func() bool
	LocalOnly   bool
	LocalID     string
	IsLeader    func() bool
	LeaderRoute func() (Route, error)
	Forward     NodeForwarder
	Capability  CapabilityForwarder
}

func (s *NodeAPI) ListNodes(ctx context.Context, _ *connect.Request[procmeshv1.ListNodesRequest]) (*connect.Response[procmeshv1.ListNodesResponse], error) {
	if err := requireAnyPerm(ctx, s.Auth, auth.PermNodeRead); err != nil {
		return nil, err
	}
	members := s.Deps.members()
	out := &procmeshv1.ListNodesResponse{Nodes: make([]*procmeshv1.Node, 0, len(members))}
	p, hasP := PrincipalFrom(ctx)
	for _, n := range members {
		if s.Auth != nil && hasP {
			if err := s.Auth.AllowOn(p, auth.PermNodeRead, control.CheckTarget{NodeID: n.NodeID}); err != nil {
				if errcode.Is(err, errcode.DENIED) {
					continue
				}
				return nil, ToConnect(err)
			}
		}
		out.Nodes = append(out.Nodes, nodeToProto(n, s.Auth))
	}
	view := readRaftMembership(s.Deps.raftMembershipReader())
	for _, node := range out.Nodes {
		applyRaftMembership(node, view)
	}
	return connect.NewResponse(out), nil
}

func (s *NodeAPI) GetNode(ctx context.Context, req *connect.Request[procmeshv1.GetNodeRequest]) (*connect.Response[procmeshv1.GetNodeResponse], error) {
	n, ok := findNode(s.Deps.members(), req.Msg.GetIdOrHostname())
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "node not found"))
	}
	if err := requirePermOn(ctx, s.Auth, auth.PermNodeRead, control.CheckTarget{NodeID: n.NodeID}, false, true); err != nil {
		return nil, err
	}
	node := nodeToProto(n, s.Auth)
	applyRaftMembership(node, readRaftMembership(s.Deps.raftMembershipReader()))
	return connect.NewResponse(&procmeshv1.GetNodeResponse{Node: node}), nil
}

func (s *NodeAPI) CreateJoinToken(ctx context.Context, req *connect.Request[procmeshv1.CreateJoinTokenRequest]) (*connect.Response[procmeshv1.CreateJoinTokenResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true, true); err != nil {
		return nil, err
	}
	if err := rejectDegraded(s.Degraded); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := requireCluster(s.Deps); err != nil {
		return nil, err
	}
	if !s.LocalOnly && s.IsLeader != nil && !s.IsLeader() {
		return s.forwardCreateJoinToken(ctx, req)
	}
	controlNode := s.Deps.controlNode()
	if controlNode != nil && controlNode.View().ClusterID != "" {
		if err := (control.CapabilityManager{Node: controlNode, Dir: s.Deps.Dir, NodeID: s.LocalID}).EnsureInitialized(); err != nil {
			return nil, ToConnect(err)
		}
	} else if err := requireCanIssueTokens(s.Deps.Dir); err != nil {
		return nil, err
	}
	ttl := time.Duration(req.Msg.GetTtlSeconds()) * time.Second
	var (
		plain string
		info  control.TokenInfo
		err   error
	)
	if n := controlNode; n != nil {
		adm := control.Admission{Node: n}
		plain, info, err = adm.CreateToken(ttl, int(req.Msg.GetUses()), s.Deps.now())
	} else {
		plain, info, err = control.CreateToken(s.Deps.Dir, ttl, int(req.Msg.GetUses()), s.Deps.now())
	}
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.CreateJoinTokenResponse{
		TokenId:     info.ID,
		Token:       plain,
		ExpiresUnix: info.ExpiresAt.Unix(),
		Uses:        int32(info.Remaining),
	}), nil
}

func (s *NodeAPI) forwardCreateJoinToken(ctx context.Context, req *connect.Request[procmeshv1.CreateJoinTokenRequest]) (*connect.Response[procmeshv1.CreateJoinTokenResponse], error) {
	if s.Forward == nil || s.LeaderRoute == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control leader unavailable"))
	}
	route, err := s.LeaderRoute()
	if err != nil || route.NodeID == "" || route.RPC == "" || route.Local {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control leader unavailable"))
	}
	stampHop(req.Header(), s.LocalID, route.NodeID)
	stampIdentity(req.Header(), ctx)
	req.Header().Del("Cookie")
	client, err := s.Forward.Node(ctx, route)
	if err != nil {
		return nil, ToConnect(errcode.Wrap(errcode.UNAVAILABLE, "control leader unavailable", rpc.MapDialError(err)))
	}
	var dispatched atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
		dispatched.Store(true)
	}}
	resp, err := client.CreateJoinToken(httptrace.WithClientTrace(ctx, trace), req)
	if err != nil {
		return nil, mapCreateJoinTokenForwardErr(err, dispatched.Load())
	}
	return resp, nil
}

func mapCreateJoinTokenForwardErr(err error, dispatched bool) error {
	var coded *errcode.Error
	if errors.As(err, &coded) {
		return ToConnect(err)
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		for _, detail := range connectErr.Details() {
			if value, detailErr := detail.Value(); detailErr == nil {
				if _, ok := value.(*procmeshv1.ErrorInfo); ok {
					return err
				}
			}
		}
	}
	if !dispatched {
		return ToConnect(errcode.Wrap(errcode.UNAVAILABLE, "control leader unavailable", err))
	}
	return ToConnect(errcode.Wrap(errcode.TIMEOUT, "join token result unknown", err))
}

func (s *NodeAPI) RevokeJoinToken(ctx context.Context, req *connect.Request[procmeshv1.RevokeJoinTokenRequest]) (*connect.Response[procmeshv1.RevokeJoinTokenResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true, true); err != nil {
		return nil, err
	}
	if err := rejectDegraded(s.Degraded); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := requireCluster(s.Deps); err != nil {
		return nil, err
	}
	if err := requireInited(s.Deps.Dir); err != nil {
		return nil, err
	}
	var err error
	if n := s.Deps.controlNode(); n != nil {
		adm := control.Admission{Node: n}
		err = adm.RevokeToken(req.Msg.GetTokenId())
	} else {
		err = control.RevokeToken(s.Deps.Dir, req.Msg.GetTokenId())
	}
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.RevokeJoinTokenResponse{}), nil
}

func (s *NodeAPI) RemoveNode(ctx context.Context, req *connect.Request[procmeshv1.RemoveNodeRequest]) (*connect.Response[procmeshv1.RemoveNodeResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermNodeRemove, req.Msg.GetNodeId(), true, true); err != nil {
		return nil, err
	}
	if err := rejectDegraded(s.Degraded); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := requireCluster(s.Deps); err != nil {
		return nil, err
	}
	ctrl := s.Deps.controlNode()
	if ctrl == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft control not configured"))
	}
	nodeID := req.Msg.GetNodeId()
	if nodeID == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "node_id required"))
	}
	self, err := s.Deps.localNodeID(ctx)
	if err != nil {
		return nil, ToConnect(err)
	}
	if nodeID == self {
		return nil, ToConnect(errcode.E(errcode.INVALID, "cannot remove self"))
	}
	err = ctrl.WithMembershipOp(func() error {
		cmd, encodeErr := control.EncodeCommand(control.CmdMemberRemove, control.MemberRemoveBody{NodeID: nodeID})
		if encodeErr != nil {
			return encodeErr
		}
		if applyErr := ctrl.Apply(cmd, authApplyTimeout); applyErr != nil {
			return applyErr
		}
		if removeErr := ctrl.RemoveServer(nodeID); removeErr != nil && !ignoreRemoveServerErr(removeErr) {
			return removeErr
		}
		return nil
	})
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.RemoveNodeResponse{}), nil
}

func (s *NodeAPI) PromoteNode(ctx context.Context, req *connect.Request[procmeshv1.PromoteNodeRequest]) (*connect.Response[procmeshv1.PromoteNodeResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterManage, req.Msg.GetNodeId(), true, true); err != nil {
		return nil, err
	}
	if err := rejectDegraded(s.Degraded); err != nil {
		return nil, err
	}
	operationID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if err := requireCluster(s.Deps); err != nil {
		return nil, err
	}
	ctrl := s.Deps.controlNode()
	if ctrl == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft control not configured"))
	}
	nodeID := req.Msg.GetNodeId()
	if nodeID == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "node_id required"))
	}
	if s.Capability == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "capability target unavailable"))
	}
	err = ctrl.WithMembershipOp(func() error {
		m, admitErr := admittedPromoteMember(ctrl.View(), nodeID)
		if admitErr != nil {
			return admitErr
		}
		target, ok := findNode(s.Deps.members(), nodeID)
		if !ok || target.RPCAddress == "" || target.State != cluster.StateAlive {
			return errcode.E(errcode.UNAVAILABLE, "capability target unavailable")
		}
		manager := control.CapabilityManager{Node: ctrl, Dir: s.Deps.Dir, NodeID: s.LocalID, Now: s.Deps.Now}
		if promoteErr := manager.Promote(ctx, operationID, nodeID, func(ctx context.Context, request control.CapabilityTransferRequest) (control.CapabilityTransferResponse, error) {
			return s.Capability.PromoteCapability(ctx, Route{NodeID: nodeID, RPC: target.RPCAddress}, request)
		}); promoteErr != nil {
			return promoteErr
		}
		m, admitErr = admittedPromoteMember(ctrl.View(), nodeID)
		if admitErr != nil {
			return admitErr
		}
		ready := ctrl.View().AdmissionCapability.Nodes[nodeID]
		if ready.Status != control.CapabilityReady || !strings.EqualFold(ready.CertSerial, m.CertSerial) {
			return errcode.E(errcode.CONFLICT, "capability target not ready")
		}
		if err := ctrl.VerifyLeaderTerm(ready.LeaderTerm); err != nil {
			return err
		}
		return ctrl.AddVoter(nodeID, m.RaftAddr)
	})
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.PromoteNodeResponse{}), nil
}

func admittedPromoteMember(view control.State, nodeID string) (control.Member, error) {
	m, ok := view.Member(nodeID)
	if !ok {
		return control.Member{}, errcode.E(errcode.NOT_FOUND, "node not found")
	}
	if m.Status != control.MemberAdmitted || m.RaftAddr == "" {
		return control.Member{}, errcode.E(errcode.INVALID, "node not admitted")
	}
	if view.SerialRevoked(m.CertSerial) {
		return control.Member{}, errcode.E(errcode.DENIED, "certificate revoked")
	}
	return m, nil
}

func ignoreRemoveServerErr(err error) bool {
	if err == nil || errcode.Is(err, errcode.NOT_FOUND) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown server") || strings.Contains(msg, "not found")
}

func findNode(members []cluster.NodeSummary, idOrHost string) (cluster.NodeSummary, bool) {
	for _, n := range members {
		if n.NodeID == idOrHost {
			return n, true
		}
	}
	for _, n := range members {
		if n.Hostname == idOrHost {
			return n, true
		}
	}
	return cluster.NodeSummary{}, false
}

func nodeToProto(n cluster.NodeSummary, svc *auth.Service) *procmeshv1.Node {
	procs := make([]*procmeshv1.ProcessSummary, 0, len(n.Processes))
	for _, p := range n.Processes {
		procs = append(procs, &procmeshv1.ProcessSummary{
			Name:            p.Name,
			Desired:         p.Desired,
			Observed:        p.Observed,
			Health:          p.Health,
			LatestRevision:  p.LatestRevision,
			ActiveRevision:  p.ActiveRevision,
			FreshnessUnixMs: p.FreshnessUnixMs,
			ProcessId:       p.ProcessID,
			Group:           p.Group,
		})
	}
	return &procmeshv1.Node{
		NodeId:              n.NodeID,
		ClusterId:           n.ClusterID,
		Hostname:            n.Hostname,
		BootId:              n.BootID,
		State:               string(n.State),
		AgentVersion:        n.AgentVersion,
		ProtocolVersion:     int32(n.ProtocolVersion),
		ApiAddress:          n.APIAddress,
		RpcAddress:          n.RPCAddress,
		GossipAddress:       n.GossipAddress,
		Labels:              n.Labels,
		Resources:           protoResources(n.Resources),
		Processes:           procs,
		LastUpdatedUnixMs:   n.LastUpdatedUnixMs,
		AgentGroupIds:       agentGroupIDsFor(svc, n.NodeID),
		RaftRole:            "UNKNOWN",
		RaftRoleFreshness:   "UNKNOWN",
		DisableRemoteCreate: n.DisableRemoteCreate,
		DisableRemoteUpdate: n.DisableRemoteUpdate,
		DisableRemoteDelete: n.DisableRemoteDelete,
		Os:                  n.OS,
		Arch:                n.Arch,
	}
}

func readRaftMembership(reader control.RaftMembershipReader) *control.RaftMembershipView {
	if reader == nil {
		return nil
	}
	view, err := reader.RaftMembershipView()
	if err != nil {
		return nil
	}
	return &view
}

func applyRaftMembership(node *procmeshv1.Node, view *control.RaftMembershipView) {
	node.RaftRole = "UNKNOWN"
	node.RaftRoleFreshness = "UNKNOWN"
	if view == nil {
		return
	}
	freshness := "STALE"
	if view.HasQuorum {
		freshness = "LIVE"
	}
	suffrage, ok := view.Members[node.GetNodeId()]
	if !ok {
		node.RaftRole = "NOT_MEMBER"
		node.RaftRoleFreshness = freshness
		return
	}
	switch suffrage {
	case control.RaftVoter:
		node.RaftRole = "VOTER"
		if view.HasQuorum && view.LeaderID != "" && view.LeaderID == node.GetNodeId() {
			node.RaftRole = "LEADER"
		}
	case control.RaftNonVoter:
		node.RaftRole = "NON_VOTER"
	default:
		return
	}
	node.RaftRoleFreshness = freshness
}

func agentGroupIDsFor(svc *auth.Service, nodeID string) []string {
	if svc == nil {
		return nil
	}
	st := svc.Store()
	if st == nil {
		return nil
	}
	view := st.View()
	var ids []string
	for id := range view.AgentGroups {
		if view.NodeInGroup(nodeID, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
