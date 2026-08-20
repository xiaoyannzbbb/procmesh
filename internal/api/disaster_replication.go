package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.DisasterReplicationServiceHandler = (*DisasterReplicationAPI)(nil)

type DisasterReplicationForwarder interface {
	DisasterReplication(context.Context, Route) (procmeshv1connect.DisasterReplicationServiceClient, error)
}

// DisasterReplicationAPI implements DisasterReplicationService for disaster recovery replication.
type DisasterReplicationAPI struct {
	ClusterID   string
	ClusterIDFn func() string
	NodeID      string
	LocalID     string
	Auth        *auth.Service
	StateFn     func() control.State
	ApplyFn     func(control.Command, time.Duration) error
	IsLeader    func() bool
	LeaderTerm  func() uint64
	LeaderAddr  func() string
	LeaderRoute func() (Route, bool)
	Forward     any
	Router      *Router
	LocalOnly   bool
	Now         func() time.Time
	PeerStore   *backup.PeerStore
	Members     func() []cluster.NodeSummary
}

// GetTopology returns the cluster topology for replication planning.
func (d *DisasterReplicationAPI) GetTopology(ctx context.Context, req *connect.Request[procmeshv1.GetTopologyRequest]) (*connect.Response[procmeshv1.GetTopologyResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationRead, d.NodeID, false, true); err != nil {
		return nil, err
	}

	if d.Members == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "cluster membership unavailable"))
	}

	st := d.StateFn()
	nodes := d.Members()
	topologyNodes := make([]*procmeshv1.AgentTopologyNode, 0, len(nodes))

	for _, n := range nodes {
		admitted := false
		if member, ok := st.Members[n.NodeID]; ok {
			admitted = member.Status == control.MemberAdmitted
		}

		alive := n.State == cluster.StateAlive

		topologyNodes = append(topologyNodes, &procmeshv1.AgentTopologyNode{
			NodeId:         n.NodeID,
			Host:           n.Labels["host"],
			Rack:           n.Labels["rack"],
			Zone:           n.Labels["zone"],
			CapacityWeight: 1.0, // Default weight
			Admitted:       admitted,
			Alive:          alive,
		})
	}

	return connect.NewResponse(&procmeshv1.GetTopologyResponse{
		Nodes:     topologyNodes,
		ClusterId: d.clusterID(),
	}), nil
}

// GeneratePolicyDraft generates a policy draft without writing to Raft.
// Returns routes, warnings, inbound load, and topology health.
func (d *DisasterReplicationAPI) GeneratePolicyDraft(ctx context.Context, req *connect.Request[procmeshv1.GeneratePolicyDraftRequest]) (*connect.Response[procmeshv1.GeneratePolicyDraftResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationManage, d.NodeID, false, true); err != nil {
		return nil, err
	}

	if d.Members == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "cluster membership unavailable"))
	}

	// Build topology from membership
	st := d.StateFn()
	nodes := d.Members()
	topologyNodes := make([]backup.AgentTopology, 0, len(nodes))
	for _, n := range nodes {
		admitted := false
		if member, ok := st.Members[n.NodeID]; ok {
			admitted = member.Status == control.MemberAdmitted
		}
		alive := n.State == cluster.StateAlive

		topologyNodes = append(topologyNodes, backup.AgentTopology{
			NodeID:         n.NodeID,
			Host:           n.Labels["host"],
			Rack:           n.Labels["rack"],
			Zone:           n.Labels["zone"],
			CapacityWeight: 1.0,
			Admitted:       admitted,
			Alive:          alive,
		})
	}

	// Generate routes
	result, err := backup.GenerateRoutes(topologyNodes, int(req.Msg.ReplicaFactor), backup.TopologyConstraints{})
	if err != nil {
		return nil, ToConnect(err)
	}

	// Convert routes
	routes := make([]*procmeshv1.ReplicationRoute, 0, len(result.Routes))
	inboundLoad := make(map[string]int32)
	for _, r := range result.Routes {
		routes = append(routes, &procmeshv1.ReplicationRoute{
			SourceNodeId:  r.SourceNodeID,
			TargetNodeIds: r.TargetNodeIDs,
			Warnings:      r.Warnings,
		})
		for _, target := range r.TargetNodeIDs {
			inboundLoad[target]++
		}
	}

	// Compute topology health
	topologyHealth := "HEALTHY"
	if len(result.Warnings) > 0 {
		topologyHealth = "DEGRADED"
	}

	// Compute draft hash
	draftHash := computeDraftHash(req.Msg, routes)

	draft := &procmeshv1.PolicyDraft{
		Name:                req.Msg.Name,
		Enabled:             req.Msg.Enabled,
		SourceSelector:      req.Msg.SourceSelector,
		SourceIds:           req.Msg.SourceIds,
		ReplicaFactor:       req.Msg.ReplicaFactor,
		Routes:              routes,
		Trigger:             req.Msg.Trigger,
		PrimaryPolicyIds:    req.Msg.PrimaryPolicyIds,
		ScheduleCron:        req.Msg.ScheduleCron,
		Timezone:            req.Msg.Timezone,
		RetentionKeepLast:   req.Msg.RetentionKeepLast,
		RetentionKeepDays:   req.Msg.RetentionKeepDays,
		RetentionMaxBytes:   req.Msg.RetentionMaxBytes,
		MaxConcurrency:      req.Msg.MaxConcurrency,
		VerifyAfterCopy:     req.Msg.VerifyAfterCopy,
		BandwidthLimit:      req.Msg.BandwidthLimit,
		TopologyConstraints: req.Msg.TopologyConstraints,
		DraftRevision:       1,
		DraftHash:           draftHash,
		GlobalWarnings:      result.Warnings,
		InboundLoad:         inboundLoad,
		TopologyHealth:      topologyHealth,
	}

	return connect.NewResponse(&procmeshv1.GeneratePolicyDraftResponse{
		Draft: draft,
	}), nil
}

// ApplyPolicyDraft applies a policy draft to Raft with CAS semantics.
func (d *DisasterReplicationAPI) ApplyPolicyDraft(ctx context.Context, req *connect.Request[procmeshv1.ApplyPolicyDraftRequest]) (*connect.Response[procmeshv1.ApplyPolicyDraftResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationManage, d.NodeID, true, true); err != nil {
		return nil, err
	}

	if req.Msg.Meta == nil || req.Msg.Meta.OperationId == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "operation_id required"))
	}

	if req.Msg.Draft == nil {
		return nil, ToConnect(errcode.E(errcode.INVALID, "draft required"))
	}
	if local, cli, err := d.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		return cli.ApplyPolicyDraft(ctx, req)
	}
	if d.ApplyFn == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control plane unavailable"))
	}

	// Verify draft hash matches
	expectedHash := computeDraftHash(&procmeshv1.GeneratePolicyDraftRequest{
		Name:                req.Msg.Draft.Name,
		Enabled:             req.Msg.Draft.Enabled,
		SourceSelector:      req.Msg.Draft.SourceSelector,
		SourceIds:           req.Msg.Draft.SourceIds,
		ReplicaFactor:       req.Msg.Draft.ReplicaFactor,
		Trigger:             req.Msg.Draft.Trigger,
		PrimaryPolicyIds:    req.Msg.Draft.PrimaryPolicyIds,
		ScheduleCron:        req.Msg.Draft.ScheduleCron,
		Timezone:            req.Msg.Draft.Timezone,
		RetentionKeepLast:   req.Msg.Draft.RetentionKeepLast,
		RetentionKeepDays:   req.Msg.Draft.RetentionKeepDays,
		RetentionMaxBytes:   req.Msg.Draft.RetentionMaxBytes,
		MaxConcurrency:      req.Msg.Draft.MaxConcurrency,
		VerifyAfterCopy:     req.Msg.Draft.VerifyAfterCopy,
		BandwidthLimit:      req.Msg.Draft.BandwidthLimit,
		TopologyConstraints: req.Msg.Draft.TopologyConstraints,
	}, req.Msg.Draft.Routes)

	if expectedHash != req.Msg.DraftHash {
		return nil, ToConnect(errcode.E(errcode.CONFLICT, "draft hash mismatch"))
	}

	// Convert routes
	routes := make([]control.ReplicationRoute, 0, len(req.Msg.Draft.Routes))
	for _, r := range req.Msg.Draft.Routes {
		routes = append(routes, control.ReplicationRoute{
			SourceNodeID:  r.SourceNodeId,
			TargetNodeIDs: r.TargetNodeIds,
		})
	}

	// Convert topology constraints
	constraints := make(map[string]string)
	for k, v := range req.Msg.Draft.TopologyConstraints {
		constraints[k] = v
	}

	body := control.ReplicationPolicyPutBody{
		OperationID:         req.Msg.Meta.OperationId,
		PolicyID:            req.Msg.PolicyId,
		Name:                req.Msg.Draft.Name,
		Enabled:             req.Msg.Draft.Enabled,
		SourceSelector:      req.Msg.Draft.SourceSelector,
		SourceIDs:           req.Msg.Draft.SourceIds,
		ReplicaFactor:       int(req.Msg.Draft.ReplicaFactor),
		Routes:              routes,
		Trigger:             req.Msg.Draft.Trigger,
		PrimaryPolicyIDs:    req.Msg.Draft.PrimaryPolicyIds,
		ScheduleCron:        req.Msg.Draft.ScheduleCron,
		Timezone:            req.Msg.Draft.Timezone,
		RetentionKeepLast:   int(req.Msg.Draft.RetentionKeepLast),
		RetentionKeepDays:   int(req.Msg.Draft.RetentionKeepDays),
		RetentionMaxBytes:   req.Msg.Draft.RetentionMaxBytes,
		MaxConcurrency:      int(req.Msg.Draft.MaxConcurrency),
		VerifyAfterCopy:     req.Msg.Draft.VerifyAfterCopy,
		BandwidthLimit:      req.Msg.Draft.BandwidthLimit,
		TopologyConstraints: constraints,
		ExpectedRevision:    req.Msg.ExpectedRevision,
	}

	cmd, err := control.EncodeCommand(control.CmdReplicationPolicyPut, body)
	if err != nil {
		return nil, ToConnect(err)
	}

	if err := d.ApplyFn(cmd, 5*time.Second); err != nil {
		return nil, ToConnect(err)
	}

	// Read back the policy to get the revision
	st := d.StateFn()
	policy, ok := st.ReplicationPolicies[req.Msg.PolicyId]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "policy not found after apply"))
	}

	return connect.NewResponse(&procmeshv1.ApplyPolicyDraftResponse{
		PolicyId: req.Msg.PolicyId,
		Revision: policy.Revision,
	}), nil
}

// ListPolicies lists all replication policies.
func (d *DisasterReplicationAPI) ListPolicies(ctx context.Context, req *connect.Request[procmeshv1.ListPoliciesRequest]) (*connect.Response[procmeshv1.ListPoliciesResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationRead, d.NodeID, false, true); err != nil {
		return nil, err
	}

	if d.StateFn == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control plane unavailable"))
	}

	st := d.StateFn()
	policies := make([]*procmeshv1.ReplicationPolicy, 0, len(st.ReplicationPolicies))
	for _, p := range st.ReplicationPolicies {
		policies = append(policies, replicationPolicyToProto(p))
	}

	// Sort by policy_id for stable output
	sort.Slice(policies, func(i, j int) bool {
		return policies[i].PolicyId < policies[j].PolicyId
	})

	return connect.NewResponse(&procmeshv1.ListPoliciesResponse{
		Policies: policies,
	}), nil
}

// GetPolicy retrieves a specific replication policy.
func (d *DisasterReplicationAPI) GetPolicy(ctx context.Context, req *connect.Request[procmeshv1.GetPolicyRequest]) (*connect.Response[procmeshv1.GetPolicyResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationRead, d.NodeID, false, true); err != nil {
		return nil, err
	}

	if d.StateFn == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control plane unavailable"))
	}

	st := d.StateFn()
	policy, ok := st.ReplicationPolicies[req.Msg.PolicyId]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "policy not found"))
	}

	return connect.NewResponse(&procmeshv1.GetPolicyResponse{
		Policy: replicationPolicyToProto(policy),
	}), nil
}

// UpdatePolicy updates an existing replication policy.
func (d *DisasterReplicationAPI) UpdatePolicy(ctx context.Context, req *connect.Request[procmeshv1.UpdatePolicyRequest]) (*connect.Response[procmeshv1.UpdatePolicyResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationManage, d.NodeID, true, true); err != nil {
		return nil, err
	}

	if req.Msg.Meta == nil || req.Msg.Meta.OperationId == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "operation_id required"))
	}
	if local, cli, err := d.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		return cli.UpdatePolicy(ctx, req)
	}
	if d.ApplyFn == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control plane unavailable"))
	}

	// Convert routes
	routes := make([]control.ReplicationRoute, 0, len(req.Msg.Routes))
	for _, r := range req.Msg.Routes {
		routes = append(routes, control.ReplicationRoute{
			SourceNodeID:  r.SourceNodeId,
			TargetNodeIDs: r.TargetNodeIds,
		})
	}

	// Convert topology constraints
	constraints := make(map[string]string)
	for k, v := range req.Msg.TopologyConstraints {
		constraints[k] = v
	}

	body := control.ReplicationPolicyPutBody{
		OperationID:         req.Msg.Meta.OperationId,
		PolicyID:            req.Msg.PolicyId,
		Name:                req.Msg.Name,
		Enabled:             req.Msg.Enabled,
		SourceSelector:      req.Msg.SourceSelector,
		SourceIDs:           req.Msg.SourceIds,
		ReplicaFactor:       int(req.Msg.ReplicaFactor),
		Routes:              routes,
		Trigger:             req.Msg.Trigger,
		PrimaryPolicyIDs:    req.Msg.PrimaryPolicyIds,
		ScheduleCron:        req.Msg.ScheduleCron,
		Timezone:            req.Msg.Timezone,
		RetentionKeepLast:   int(req.Msg.RetentionKeepLast),
		RetentionKeepDays:   int(req.Msg.RetentionKeepDays),
		RetentionMaxBytes:   req.Msg.RetentionMaxBytes,
		MaxConcurrency:      int(req.Msg.MaxConcurrency),
		VerifyAfterCopy:     req.Msg.VerifyAfterCopy,
		BandwidthLimit:      req.Msg.BandwidthLimit,
		TopologyConstraints: constraints,
		ExpectedRevision:    req.Msg.ExpectedRevision,
	}

	cmd, err := control.EncodeCommand(control.CmdReplicationPolicyPut, body)
	if err != nil {
		return nil, ToConnect(err)
	}

	if err := d.ApplyFn(cmd, 5*time.Second); err != nil {
		return nil, ToConnect(err)
	}

	// Read back the policy to get the revision
	st := d.StateFn()
	policy, ok := st.ReplicationPolicies[req.Msg.PolicyId]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "policy not found after apply"))
	}

	return connect.NewResponse(&procmeshv1.UpdatePolicyResponse{
		PolicyId: req.Msg.PolicyId,
		Revision: policy.Revision,
	}), nil
}

// DeletePolicy deletes a replication policy.
func (d *DisasterReplicationAPI) DeletePolicy(ctx context.Context, req *connect.Request[procmeshv1.DeletePolicyRequest]) (*connect.Response[procmeshv1.DeletePolicyResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationManage, d.NodeID, true, true); err != nil {
		return nil, err
	}

	if req.Msg.Meta == nil || req.Msg.Meta.OperationId == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "operation_id required"))
	}
	if local, cli, err := d.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		return cli.DeletePolicy(ctx, req)
	}
	if d.ApplyFn == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control plane unavailable"))
	}

	body := control.ReplicationPolicyDeleteBody{
		OperationID: req.Msg.Meta.OperationId,
		PolicyID:    req.Msg.PolicyId,
	}

	cmd, err := control.EncodeCommand(control.CmdReplicationPolicyDelete, body)
	if err != nil {
		return nil, ToConnect(err)
	}

	if err := d.ApplyFn(cmd, 5*time.Second); err != nil {
		return nil, ToConnect(err)
	}

	return connect.NewResponse(&procmeshv1.DeletePolicyResponse{
		Deleted: true,
	}), nil
}

// StartRun initiates a replication run.
func (d *DisasterReplicationAPI) StartRun(ctx context.Context, req *connect.Request[procmeshv1.StartRunRequest]) (*connect.Response[procmeshv1.StartRunResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationManage, d.NodeID, true, true); err != nil {
		return nil, err
	}

	operationID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if local, cli, err := d.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		return cli.StartRun(ctx, req)
	}
	if d.ApplyFn == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "control plane unavailable"))
	}
	term := d.leaderTerm()
	if term == 0 {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader term unavailable"))
	}

	st := d.state()
	runID := replicationRunID(operationID)
	if existing, ok := st.ReplicationRuns[runID]; ok {
		if existing.PolicyID != req.Msg.GetPolicyId() {
			return nil, ToConnect(errcode.E(errcode.CONFLICT, "operation already used for another replication policy"))
		}
		return startRunResponse(existing), nil
	}

	policy, ok := st.ReplicationPolicies[req.Msg.GetPolicyId()]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "replication policy not found"))
	}
	now := d.now()
	sources := replicationSources(policy)
	if len(sources) == 0 || len(policy.Routes) == 0 {
		return nil, ToConnect(errcode.E(errcode.INVALID, "replication routes required"))
	}

	run := control.ClusterBackupRun{
		RunID:          runID,
		PolicyID:       policy.PolicyID,
		PolicyRevision: policy.Revision,
		TargetNodeIDs:  sources,
		Status:         "RUNNING",
		CreatedUnix:    now.Unix(),
		StartedUnix:    now.Unix(),
		MaxConcurrency: policy.MaxConcurrency,
		LeaseUntilUnix: now.Add(30 * time.Second).Unix(),
	}
	tasks := make([]control.ClusterBackupTask, 0)
	for _, route := range policy.Routes {
		for _, targetID := range route.TargetNodeIDs {
			tasks = append(tasks, control.ClusterBackupTask{
				RunID:        runID,
				TaskID:       replicationTaskID(runID, route.SourceNodeID, targetID),
				NodeID:       targetID,
				SourceNodeID: route.SourceNodeID,
				Status:       "PENDING",
				UpdatedUnix:  now.Unix(),
			})
		}
	}
	cmd, err := control.EncodeCommand(control.CmdBackupRunCreate, control.CreateRunBody{
		OperationID: operationID,
		Run:         run,
		Tasks:       tasks,
		LeaderTerm:  term,
		Replication: true,
	})
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := d.ApplyFn(cmd, 5*time.Second); err != nil {
		if existing, ok := d.state().ReplicationRuns[runID]; ok && existing.PolicyID == policy.PolicyID {
			return startRunResponse(existing), nil
		}
		return nil, ToConnect(err)
	}
	return startRunResponse(run), nil
}

// GetRun retrieves a replication run.
func (d *DisasterReplicationAPI) GetRun(ctx context.Context, req *connect.Request[procmeshv1.GetRunRequest]) (*connect.Response[procmeshv1.GetRunResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationRead, d.NodeID, false, true); err != nil {
		return nil, err
	}

	st := d.StateFn()

	// Find the run
	run, ok := st.ReplicationRuns[req.Msg.RunId]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "replication run not found"))
	}

	// Collect tasks for this run
	tasks := make([]*procmeshv1.ReplicationTask, 0)
	for _, task := range st.ReplicationTasks {
		if task.RunID == run.RunID {
			tasks = append(tasks, &procmeshv1.ReplicationTask{
				TaskId:        task.TaskID,
				RunId:         task.RunID,
				SourceNodeId:  task.SourceNodeID,
				TargetNodeIds: []string{task.NodeID},
				SnapshotId:    task.SnapshotID,
				Status:        task.Status,
				Sha256:        task.SHA256,
				Bytes:         task.Bytes,
				ErrorCode:     task.ErrorCode,
				ErrorSummary:  task.ErrorSummary,
				StartedAt:     0, // Not tracked
				FinishedAt:    task.UpdatedUnix,
			})
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskId < tasks[j].TaskId })

	// Build run response
	protoRun := &procmeshv1.ReplicationRun{
		RunId:          run.RunID,
		PolicyId:       run.PolicyID,
		PolicyRevision: run.PolicyRevision,
		Status:         run.Status,
		Tasks:          tasks,
		StartedAt:      run.StartedUnix,
		FinishedAt:     run.FinishedUnix,
	}

	return connect.NewResponse(&procmeshv1.GetRunResponse{
		Run: protoRun,
	}), nil
}

// ListRuns lists replication runs.
func (d *DisasterReplicationAPI) ListRuns(ctx context.Context, req *connect.Request[procmeshv1.ListRunsRequest]) (*connect.Response[procmeshv1.ListRunsResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationRead, d.NodeID, false, true); err != nil {
		return nil, err
	}

	st := d.StateFn()

	// Filter runs by policy_id if specified
	runs := make([]control.ClusterBackupRun, 0)
	for _, run := range st.ReplicationRuns {
		if req.Msg.PolicyId == "" || run.PolicyID == req.Msg.PolicyId {
			runs = append(runs, run)
		}
	}

	// Sort by creation time, most recent first
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedUnix > runs[j].CreatedUnix
	})

	// Build task map for efficient lookup
	tasksByRun := make(map[string][]*procmeshv1.ReplicationTask)
	for _, task := range st.ReplicationTasks {
		tasksByRun[task.RunID] = append(tasksByRun[task.RunID], &procmeshv1.ReplicationTask{
			TaskId:        task.TaskID,
			RunId:         task.RunID,
			SourceNodeId:  task.SourceNodeID,
			TargetNodeIds: []string{task.NodeID},
			SnapshotId:    task.SnapshotID,
			Status:        task.Status,
			Sha256:        task.SHA256,
			Bytes:         task.Bytes,
			ErrorCode:     task.ErrorCode,
			ErrorSummary:  task.ErrorSummary,
			FinishedAt:    task.UpdatedUnix,
		})
	}
	for runID := range tasksByRun {
		tasks := tasksByRun[runID]
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskId < tasks[j].TaskId })
	}

	// Convert to proto
	protoRuns := make([]*procmeshv1.ReplicationRun, 0, len(runs))
	for _, run := range runs {
		protoRuns = append(protoRuns, &procmeshv1.ReplicationRun{
			RunId:          run.RunID,
			PolicyId:       run.PolicyID,
			PolicyRevision: run.PolicyRevision,
			Status:         run.Status,
			Tasks:          tasksByRun[run.RunID],
			StartedAt:      run.StartedUnix,
			FinishedAt:     run.FinishedUnix,
		})
	}

	return connect.NewResponse(&procmeshv1.ListRunsResponse{
		Runs: protoRuns,
	}), nil
}

// RetryFailedRoutes retries failed replication routes.
func (d *DisasterReplicationAPI) RetryFailedRoutes(ctx context.Context, req *connect.Request[procmeshv1.RetryFailedRoutesRequest]) (*connect.Response[procmeshv1.RetryFailedRoutesResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationManage, d.NodeID, true, true); err != nil {
		return nil, err
	}

	operationID, _, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if local, cli, err := d.forwardMutation(ctx, req.Header()); !local {
		if err != nil {
			return nil, err
		}
		return cli.RetryFailedRoutes(ctx, req)
	}
	term := d.leaderTerm()
	if term == 0 {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader term unavailable"))
	}
	st := d.state()

	// Find the run
	run, ok := st.ReplicationRuns[req.Msg.RunId]
	if !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "replication run not found"))
	}

	// Apply retry command to Raft
	cmd, err := control.EncodeCommand(control.CmdBackupRetryFailedTasks, control.RetryFailedTasksBody{
		OperationID: operationID,
		RunID:       run.RunID,
		LeaderTerm:  term,
		UpdatedUnix: d.now().Unix(),
		Replication: true,
	})
	if err != nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "failed to encode command"))
	}

	if err := d.ApplyFn(cmd, 5*time.Second); err != nil {
		return nil, ToConnect(err)
	}

	// Count failed tasks that were retried
	retriedCount := 0
	for _, task := range st.ReplicationTasks {
		if task.RunID == run.RunID && (task.Status == "FAILED" || task.Status == "TIMEOUT" || task.Status == "UNAVAILABLE") {
			retriedCount++
		}
	}

	return connect.NewResponse(&procmeshv1.RetryFailedRoutesResponse{
		RetriedCount: int32(retriedCount),
	}), nil
}

func (d *DisasterReplicationAPI) state() control.State {
	if d.StateFn == nil {
		return *control.NewState()
	}
	return d.StateFn()
}

func (d *DisasterReplicationAPI) clusterID() string {
	if d.ClusterIDFn != nil {
		return d.ClusterIDFn()
	}
	return d.ClusterID
}

func (d *DisasterReplicationAPI) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *DisasterReplicationAPI) leaderTerm() uint64 {
	if d.LeaderTerm == nil {
		return 0
	}
	return d.LeaderTerm()
}

func (d *DisasterReplicationAPI) isLeader() bool {
	return d.IsLeader == nil || d.IsLeader()
}

func (d *DisasterReplicationAPI) leaderRoute() (Route, bool) {
	if d.LeaderRoute != nil {
		return d.LeaderRoute()
	}
	if d.Router == nil {
		return Route{}, false
	}
	leaderAddr := ""
	if d.LeaderAddr != nil {
		leaderAddr = d.LeaderAddr()
	}
	if leaderAddr == "" {
		return Route{}, false
	}
	state := d.state()
	for _, node := range d.Router.members() {
		member, ok := state.Members[node.NodeID]
		if !ok || member.RaftAddr != leaderAddr || node.NodeID == d.LocalID {
			continue
		}
		route, err := d.Router.routeForNode(node)
		if err == nil {
			return route, true
		}
	}
	return Route{}, false
}

func (d *DisasterReplicationAPI) forwardMutation(ctx context.Context, header http.Header) (bool, procmeshv1connect.DisasterReplicationServiceClient, error) {
	if d.LocalOnly || d.isLeader() {
		return true, nil, nil
	}
	forwarder, ok := d.Forward.(DisasterReplicationForwarder)
	if !ok || forwarder == nil {
		return false, nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader unavailable"))
	}
	route, ok := d.leaderRoute()
	if !ok || route.Local {
		return false, nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader unavailable"))
	}
	stampHop(header, d.LocalID, route.NodeID)
	stampIdentity(header, ctx)
	client, err := forwarder.DisasterReplication(ctx, route)
	if err != nil {
		return false, nil, ToConnect(rpc.MapDialError(err))
	}
	return false, client, nil
}

func replicationSources(policy control.ReplicationPolicy) []string {
	if policy.SourceSelector == "EXPLICIT_NODES" {
		return append([]string(nil), policy.SourceIDs...)
	}
	seen := make(map[string]struct{}, len(policy.Routes))
	sources := make([]string, 0, len(policy.Routes))
	for _, route := range policy.Routes {
		if _, ok := seen[route.SourceNodeID]; ok {
			continue
		}
		seen[route.SourceNodeID] = struct{}{}
		sources = append(sources, route.SourceNodeID)
	}
	sort.Strings(sources)
	return sources
}

func replicationRunID(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return "run-" + hex.EncodeToString(sum[:12])
}

func replicationTaskID(runID, sourceNodeID, targetNodeID string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + sourceNodeID + "\x00" + targetNodeID))
	return "task-" + hex.EncodeToString(sum[:12])
}

func startRunResponse(run control.ClusterBackupRun) *connect.Response[procmeshv1.StartRunResponse] {
	return connect.NewResponse(&procmeshv1.StartRunResponse{
		RunId:          run.RunID,
		PolicyId:       run.PolicyID,
		PolicyRevision: run.PolicyRevision,
		StartedAt:      run.StartedUnix,
	})
}

// VerifyReplica verifies replica integrity without executing apply.
func (d *DisasterReplicationAPI) VerifyReplica(ctx context.Context, req *connect.Request[procmeshv1.VerifyReplicaRequest]) (*connect.Response[procmeshv1.VerifyReplicaResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationManage, d.NodeID, true, true); err != nil {
		return nil, err
	}

	if d.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}

	meta, err := d.PeerStore.GetReplicaMetadata(ctx, req.Msg.SourceNodeId, d.clusterID(), req.Msg.SnapshotId)
	if err != nil {
		return nil, ToConnect(err)
	}

	// Verify checksum is present
	valid := meta.SHA256 != ""

	return connect.NewResponse(&procmeshv1.VerifyReplicaResponse{
		Valid:        valid,
		Sha256:       meta.SHA256,
		ProcessCount: int32(len(meta.ProcessIDs)),
		ProcessIds:   meta.ProcessIDs,
		Errors:       []string{}, // No errors if valid
	}), nil
}

// ListRecoverableSnapshots lists snapshots available for recovery.
// Returns source Owner and checksum.
func (d *DisasterReplicationAPI) ListRecoverableSnapshots(ctx context.Context, req *connect.Request[procmeshv1.ListRecoverableSnapshotsRequest]) (*connect.Response[procmeshv1.ListRecoverableSnapshotsResponse], error) {
	if err := requirePerm(ctx, d.Auth, auth.PermReplicationRead, d.NodeID, false, true); err != nil {
		return nil, err
	}

	if d.PeerStore == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "peer store unavailable"))
	}

	// TODO: Implement listing all snapshots across all sources
	// For now, return empty list
	return connect.NewResponse(&procmeshv1.ListRecoverableSnapshotsResponse{
		Snapshots: []*procmeshv1.ReplicaSnapshot{},
	}), nil
}

// Helper functions

func replicationPolicyToProto(p control.ReplicationPolicy) *procmeshv1.ReplicationPolicy {
	routes := make([]*procmeshv1.ReplicationRoute, 0, len(p.Routes))
	for _, r := range p.Routes {
		routes = append(routes, &procmeshv1.ReplicationRoute{
			SourceNodeId:  r.SourceNodeID,
			TargetNodeIds: r.TargetNodeIDs,
		})
	}

	constraints := make(map[string]string)
	for k, v := range p.TopologyConstraints {
		constraints[k] = v
	}

	return &procmeshv1.ReplicationPolicy{
		PolicyId:            p.PolicyID,
		Name:                p.Name,
		Enabled:             p.Enabled,
		SourceSelector:      p.SourceSelector,
		SourceIds:           p.SourceIDs,
		ReplicaFactor:       int32(p.ReplicaFactor),
		Routes:              routes,
		Trigger:             p.Trigger,
		PrimaryPolicyIds:    p.PrimaryPolicyIDs,
		ScheduleCron:        p.ScheduleCron,
		Timezone:            p.Timezone,
		RetentionKeepLast:   int32(p.RetentionKeepLast),
		RetentionKeepDays:   int32(p.RetentionKeepDays),
		RetentionMaxBytes:   p.RetentionMaxBytes,
		MaxConcurrency:      int32(p.MaxConcurrency),
		VerifyAfterCopy:     p.VerifyAfterCopy,
		BandwidthLimit:      p.BandwidthLimit,
		TopologyConstraints: constraints,
		Revision:            p.Revision,
	}
}

func computeDraftHash(req *procmeshv1.GeneratePolicyDraftRequest, routes []*procmeshv1.ReplicationRoute) string {
	// Sort routes for deterministic hash
	sortedRoutes := make([]*procmeshv1.ReplicationRoute, len(routes))
	copy(sortedRoutes, routes)
	sort.Slice(sortedRoutes, func(i, j int) bool {
		return sortedRoutes[i].SourceNodeId < sortedRoutes[j].SourceNodeId
	})

	// Create a canonical representation
	data := map[string]interface{}{
		"name":            req.Name,
		"enabled":         req.Enabled,
		"source_selector": req.SourceSelector,
		"source_ids":      req.SourceIds,
		"replica_factor":  req.ReplicaFactor,
		"routes":          sortedRoutes,
		"trigger":         req.Trigger,
	}

	jsonData, _ := json.Marshal(data)
	hash := sha256.Sum256(jsonData)
	return hex.EncodeToString(hash[:])
}
