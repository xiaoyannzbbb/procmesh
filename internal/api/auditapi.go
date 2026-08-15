package api

import (
	"context"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
	"golang.org/x/sync/errgroup"
)

var _ procmeshv1connect.AuditServiceHandler = (*AuditAPI)(nil)

const auditHopTimeout = 2 * time.Second

type AuditAPI struct {
	Store     *store.Store
	Auth      *auth.Service
	LocalOnly bool
	LocalID   string
	Router    *Router
	Forward   Forwarder
	Members   func() []cluster.NodeSummary
	Now       func() time.Time
}

func (s *AuditAPI) ListAudit(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest]) (*connect.Response[procmeshv1.ListAuditResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermAuditRead, "", false, true); err != nil {
		return nil, err
	}
	limit := normalizeAuditLimit(req.Msg.GetLimit())
	now := s.now()
	resource := req.Msg.GetResource()
	target := req.Msg.GetTargetNode()

	if !s.LocalOnly && target != "" && !s.isLocalNode(target) {
		entries := s.listTarget(ctx, req, target, resource, limit, now)
		return connect.NewResponse(&procmeshv1.ListAuditResponse{Entries: entries}), nil
	}

	entries, err := s.listLocal(ctx, resource, limit, now)
	if err != nil {
		return nil, ToConnect(err)
	}
	if !s.LocalOnly && target == "" {
		entries = append(entries, s.aggregateRemotes(ctx, req, resource, limit, now)...)
		sortAuditEntries(entries)
		if len(entries) > limit {
			entries = entries[:limit]
		}
	}
	return connect.NewResponse(&procmeshv1.ListAuditResponse{Entries: entries}), nil
}

func (s *AuditAPI) listTarget(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest], target, resource string, limit int, now time.Time) []*procmeshv1.AuditEntry {
	h := req.Header().Clone()
	rpc.SetTarget(h, target)
	local, rt, err := hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, h, "", "")
	if err != nil || local {
		// target is already known non-local; local here means no router to hop.
		return []*procmeshv1.AuditEntry{s.placeholder(target, now)}
	}
	entries, herr := s.hopNode(ctx, req, rt, resource, limit)
	if herr != nil {
		nodeID := rt.NodeID
		if nodeID == "" {
			nodeID = target
		}
		return []*procmeshv1.AuditEntry{s.placeholder(nodeID, now)}
	}
	return entries
}

func (s *AuditAPI) aggregateRemotes(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest], resource string, limit int, now time.Time) []*procmeshv1.AuditEntry {
	var (
		out []*procmeshv1.AuditEntry
		mu  sync.Mutex
		g   errgroup.Group
	)
	for _, m := range s.memberList() {
		if m.NodeID == "" || m.NodeID == s.LocalID {
			continue
		}
		switch m.State {
		case cluster.StateFailed, cluster.StateSuspect:
			out = append(out, unavailableEntry(m, now))
		case cluster.StateAlive:
			m := m
			g.Go(func() error {
				hopCtx, cancel := context.WithTimeout(ctx, auditHopTimeout)
				defer cancel()
				rt := Route{NodeID: m.NodeID, RPC: m.RPCAddress}
				ents, err := s.hopNode(hopCtx, req, rt, resource, limit)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					out = append(out, unavailableEntry(m, now))
					return nil
				}
				out = append(out, ents...)
				return nil
			})
		}
	}
	_ = g.Wait()
	return out
}

func (s *AuditAPI) hopNode(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest], rt Route, resource string, limit int) ([]*procmeshv1.AuditEntry, error) {
	if s.Forward == nil || rt.RPC == "" {
		return nil, unavailableOwner()
	}
	fwd := connect.NewRequest(&procmeshv1.ListAuditRequest{
		Resource: resource,
		Limit:    int32(limit),
	})
	for k, vs := range req.Header() {
		for _, v := range vs {
			fwd.Header().Add(k, v)
		}
	}
	stampHop(fwd.Header(), s.LocalID, rt.NodeID)
	stampIdentity(fwd.Header(), ctx)
	cli, err := s.Forward.Audit(ctx, rt)
	if err != nil {
		return nil, err
	}
	out, err := cli.ListAudit(ctx, fwd)
	if err != nil {
		return nil, err
	}
	if out == nil || out.Msg == nil {
		return []*procmeshv1.AuditEntry{}, nil
	}
	return out.Msg.GetEntries(), nil
}

func (s *AuditAPI) listLocal(ctx context.Context, resource string, limit int, now time.Time) ([]*procmeshv1.AuditEntry, error) {
	if s.Store == nil {
		return []*procmeshv1.AuditEntry{}, nil
	}
	evs, err := s.Store.ListAuditAll(ctx, resource, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*procmeshv1.AuditEntry, 0, len(evs))
	nowMs := now.UnixMilli()
	for _, ev := range evs {
		out = append(out, &procmeshv1.AuditEntry{
			Event:             auditEventToProto(ev),
			SourceNode:        s.LocalID,
			Freshness:         freshness.LIVE,
			LastUpdatedUnixMs: nowMs,
		})
	}
	return out, nil
}

func (s *AuditAPI) placeholder(nodeID string, now time.Time) *procmeshv1.AuditEntry {
	var lastMs int64
	state := ""
	for _, m := range s.memberList() {
		if m.NodeID == nodeID {
			lastMs = m.LastUpdatedUnixMs
			state = string(m.State)
			break
		}
	}
	return &procmeshv1.AuditEntry{
		Event:             &procmeshv1.AuditEvent{Action: "unavailable", Result: "UNAVAILABLE"},
		SourceNode:        nodeID,
		Freshness:         freshness.Classify(now, lastMs, state),
		LastUpdatedUnixMs: lastMs,
	}
}

func unavailableEntry(m cluster.NodeSummary, now time.Time) *procmeshv1.AuditEntry {
	return &procmeshv1.AuditEntry{
		Event:             &procmeshv1.AuditEvent{Action: "unavailable", Result: "UNAVAILABLE"},
		SourceNode:        m.NodeID,
		Freshness:         freshness.Classify(now, m.LastUpdatedUnixMs, string(m.State)),
		LastUpdatedUnixMs: m.LastUpdatedUnixMs,
	}
}

func (s *AuditAPI) memberList() []cluster.NodeSummary {
	if s.Members != nil {
		return s.Members()
	}
	if s.Router != nil && s.Router.Members != nil {
		return s.Router.Members()
	}
	return nil
}

func (s *AuditAPI) isLocalNode(id string) bool {
	if id == "" || id == s.LocalID {
		return true
	}
	if s.Router != nil {
		return s.Router.isLocalIdentity(id)
	}
	return false
}

func (s *AuditAPI) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func normalizeAuditLimit(limit int32) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return int(limit)
}

func sortAuditEntries(entries []*procmeshv1.AuditEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return auditTimestamp(entries[i]) > auditTimestamp(entries[j])
	})
}

func auditTimestamp(e *procmeshv1.AuditEntry) int64 {
	if e == nil || e.GetEvent() == nil {
		return 0
	}
	return e.GetEvent().GetTimestampUnixMs()
}

func auditEventToProto(ev store.AuditEvent) *procmeshv1.AuditEvent {
	return &procmeshv1.AuditEvent{
		AuditId:         ev.AuditID,
		TimestampUnixMs: ev.Timestamp.UTC().UnixMilli(),
		UserId:          ev.UserID,
		Username:        ev.Username,
		SourceIp:        ev.SourceIP,
		SourceAgent:     ev.SourceAgent,
		TargetAgent:     ev.TargetAgent,
		Resource:        ev.Resource,
		Action:          ev.Action,
		OperationId:     ev.OperationID,
		Result:          ev.Result,
		MetadataJson:    string(ev.Metadata),
	}
}
