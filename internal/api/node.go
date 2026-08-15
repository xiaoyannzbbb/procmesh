package api

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.NodeServiceHandler = (*NodeAPI)(nil)

type NodeAPI struct {
	Deps     ClusterDeps
	Auth     *auth.Service
	Degraded func() bool
}

func (s *NodeAPI) ListNodes(ctx context.Context, _ *connect.Request[procmeshv1.ListNodesRequest]) (*connect.Response[procmeshv1.ListNodesResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermNodeRead, "", false); err != nil {
		return nil, err
	}
	members := s.Deps.members()
	out := &procmeshv1.ListNodesResponse{Nodes: make([]*procmeshv1.Node, 0, len(members))}
	for _, n := range members {
		out.Nodes = append(out.Nodes, nodeToProto(n))
	}
	return connect.NewResponse(out), nil
}

func (s *NodeAPI) GetNode(ctx context.Context, req *connect.Request[procmeshv1.GetNodeRequest]) (*connect.Response[procmeshv1.GetNodeResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermNodeRead, "", false); err != nil {
		return nil, err
	}
	n, ok := findNode(s.Deps.members(), req.Msg.GetIdOrHostname())
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "node not found"))
	}
	return connect.NewResponse(&procmeshv1.GetNodeResponse{Node: nodeToProto(n)}), nil
}

func (s *NodeAPI) CreateJoinToken(ctx context.Context, req *connect.Request[procmeshv1.CreateJoinTokenRequest]) (*connect.Response[procmeshv1.CreateJoinTokenResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true); err != nil {
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
	if err := requireCanIssueTokens(s.Deps.Dir); err != nil {
		return nil, err
	}
	ttl := time.Duration(req.Msg.GetTtlSeconds()) * time.Second
	var (
		plain string
		info  control.TokenInfo
		err   error
	)
	if s.Deps.Control != nil {
		adm := control.Admission{Node: s.Deps.Control}
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

func (s *NodeAPI) RevokeJoinToken(ctx context.Context, req *connect.Request[procmeshv1.RevokeJoinTokenRequest]) (*connect.Response[procmeshv1.RevokeJoinTokenResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true); err != nil {
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
	if s.Deps.Control != nil {
		adm := control.Admission{Node: s.Deps.Control}
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
	if err := requirePerm(ctx, s.Auth, auth.PermNodeRemove, req.Msg.GetNodeId(), true); err != nil {
		return nil, err
	}
	return nil, unimplemented()
}

func (s *NodeAPI) PromoteNode(ctx context.Context, req *connect.Request[procmeshv1.PromoteNodeRequest]) (*connect.Response[procmeshv1.PromoteNodeResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterManage, req.Msg.GetNodeId(), true); err != nil {
		return nil, err
	}
	return nil, unimplemented()
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

func nodeToProto(n cluster.NodeSummary) *procmeshv1.Node {
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
		})
	}
	return &procmeshv1.Node{
		NodeId:          n.NodeID,
		ClusterId:       n.ClusterID,
		Hostname:        n.Hostname,
		BootId:          n.BootID,
		State:           string(n.State),
		AgentVersion:    n.AgentVersion,
		ProtocolVersion: int32(n.ProtocolVersion),
		ApiAddress:      n.APIAddress,
		RpcAddress:      n.RPCAddress,
		GossipAddress:   n.GossipAddress,
		Labels:          n.Labels,
		Resources: &procmeshv1.ResourceSummary{
			CpuPercent:    int32(n.Resources.CPUPercent),
			MemoryPercent: int32(n.Resources.MemoryPercent),
			DiskPercent:   int32(n.Resources.DiskPercent),
		},
		Processes:         procs,
		LastUpdatedUnixMs: n.LastUpdatedUnixMs,
	}
}
