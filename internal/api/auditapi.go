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

type auditPageResult struct {
	entries []*procmeshv1.AuditEntry
	total   int64
}

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
	page := normalizeAuditPage(req.Msg.GetPage())
	offset := (page - 1) * limit
	now := s.now()
	resource := req.Msg.GetResource()
	target := req.Msg.GetTargetNode()

	if !s.LocalOnly && target != "" && !s.isLocalNode(target) {
		result := s.listTarget(ctx, req, target, resource, limit, page, now)
		return auditPageResponse(result, page, limit), nil
	}

	if !s.LocalOnly && target == "" {
		result, err := s.listAggregate(ctx, req, resource, limit, page, now)
		if err != nil {
			return nil, ToConnect(err)
		}
		return auditPageResponse(result, page, limit), nil
	}

	result, err := s.listLocal(ctx, resource, limit, offset, now)
	if err != nil {
		return nil, ToConnect(err)
	}
	return auditPageResponse(result, page, limit), nil
}

func (s *AuditAPI) listTarget(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest], target, resource string, limit, page int, now time.Time) auditPageResult {
	h := req.Header().Clone()
	rpc.SetTarget(h, target)
	local, rt, err := hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, h, "", "")
	if err != nil || local {
		// target is already known non-local; local here means no router to hop.
		return placeholderPage(s.placeholder(target, now), page)
	}
	result, herr := s.hopNode(ctx, req, rt, resource, limit, page)
	if herr != nil {
		nodeID := rt.NodeID
		if nodeID == "" {
			nodeID = target
		}
		return placeholderPage(s.placeholder(nodeID, now), page)
	}
	return result
}

func (s *AuditAPI) listAggregate(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest], resource string, limit, page int, now time.Time) (auditPageResult, error) {
	needed := page * limit
	local, err := s.listLocal(ctx, resource, needed, 0, now)
	if err != nil {
		return auditPageResult{}, err
	}
	remote := s.aggregateRemotes(ctx, req, resource, needed, now)
	entries := append(local.entries, remote.entries...)
	sortAuditEntries(entries)
	offset := (page - 1) * limit
	return auditPageResult{
		entries: sliceAuditPage(entries, offset, limit),
		total:   local.total + remote.total,
	}, nil
}

func (s *AuditAPI) aggregateRemotes(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest], resource string, needed int, now time.Time) auditPageResult {
	var (
		out   auditPageResult
		alive []cluster.NodeSummary
		mu    sync.Mutex
		g     errgroup.Group
	)
	for _, m := range s.memberList() {
		if m.NodeID == "" || m.NodeID == s.LocalID {
			continue
		}
		switch m.State {
		case cluster.StateFailed, cluster.StateSuspect:
			out.entries = append(out.entries, unavailableEntry(m, now))
			out.total++
		case cluster.StateAlive:
			alive = append(alive, m)
		}
	}
	for _, m := range alive {
		m := m
		g.Go(func() error {
			hopCtx, cancel := context.WithTimeout(ctx, auditHopTimeout)
			defer cancel()
			rt := Route{NodeID: m.NodeID, RPC: m.RPCAddress}
			result, err := s.hopNodePrefix(hopCtx, req, rt, resource, needed)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.entries = append(out.entries, unavailableEntry(m, now))
				out.total++
				return nil
			}
			out.entries = append(out.entries, result.entries...)
			out.total += result.total
			return nil
		})
	}
	_ = g.Wait()
	return out
}

func (s *AuditAPI) hopNodePrefix(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest], rt Route, resource string, needed int) (auditPageResult, error) {
	chunkSize := needed
	if chunkSize > 200 {
		chunkSize = 200
	}
	result := auditPageResult{entries: []*procmeshv1.AuditEntry{}}
	for page := 1; len(result.entries) < needed; page++ {
		chunk, err := s.hopNode(ctx, req, rt, resource, chunkSize, page)
		if err != nil {
			return auditPageResult{}, err
		}
		result.entries = append(result.entries, chunk.entries...)
		result.total = chunk.total
		if len(chunk.entries) < chunkSize || int64(len(result.entries)) >= chunk.total || page >= 100 {
			break
		}
	}
	if len(result.entries) > needed {
		result.entries = result.entries[:needed]
	}
	if result.total == 0 && len(result.entries) > 0 {
		result.total = int64(len(result.entries))
	}
	return result, nil
}

func (s *AuditAPI) hopNode(ctx context.Context, req *connect.Request[procmeshv1.ListAuditRequest], rt Route, resource string, limit, page int) (auditPageResult, error) {
	if s.Forward == nil || rt.RPC == "" {
		return auditPageResult{}, unavailableOwner()
	}
	fwd := connect.NewRequest(&procmeshv1.ListAuditRequest{
		Resource: resource,
		Limit:    int32(limit),
		Page:     int32(page),
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
		return auditPageResult{}, err
	}
	out, err := cli.ListAudit(ctx, fwd)
	if err != nil {
		return auditPageResult{}, err
	}
	if out == nil || out.Msg == nil {
		return auditPageResult{entries: []*procmeshv1.AuditEntry{}}, nil
	}
	return auditPageResult{entries: out.Msg.GetEntries(), total: out.Msg.GetTotal()}, nil
}

func (s *AuditAPI) listLocal(ctx context.Context, resource string, limit, offset int, now time.Time) (auditPageResult, error) {
	if s.Store == nil {
		return auditPageResult{entries: []*procmeshv1.AuditEntry{}}, nil
	}
	evs, total, err := s.Store.ListAuditPage(ctx, resource, limit, offset)
	if err != nil {
		return auditPageResult{}, err
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
	return auditPageResult{entries: out, total: total}, nil
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
		Event: &procmeshv1.AuditEvent{
			Action:          "unavailable",
			Result:          "UNAVAILABLE",
			TimestampUnixMs: now.UnixMilli(),
		},
		SourceNode:        nodeID,
		Freshness:         placeholderFreshness(now, lastMs, state),
		LastUpdatedUnixMs: lastMs,
	}
}

func unavailableEntry(m cluster.NodeSummary, now time.Time) *procmeshv1.AuditEntry {
	return &procmeshv1.AuditEntry{
		Event: &procmeshv1.AuditEvent{
			Action:          "unavailable",
			Result:          "UNAVAILABLE",
			TimestampUnixMs: now.UnixMilli(),
		},
		SourceNode:        m.NodeID,
		Freshness:         placeholderFreshness(now, m.LastUpdatedUnixMs, string(m.State)),
		LastUpdatedUnixMs: m.LastUpdatedUnixMs,
	}
}

func placeholderFreshness(now time.Time, lastMs int64, state string) string {
	f := freshness.Classify(now, lastMs, state)
	if f == freshness.LIVE {
		return freshness.STALE
	}
	return f
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

func normalizeAuditPage(page int32) int {
	if page <= 0 {
		return 1
	}
	if page > 100 {
		return 100
	}
	return int(page)
}

func auditPageResponse(result auditPageResult, page, limit int) *connect.Response[procmeshv1.ListAuditResponse] {
	offset := (page - 1) * limit
	return connect.NewResponse(&procmeshv1.ListAuditResponse{
		Entries:  result.entries,
		Total:    result.total,
		Page:     int32(page),
		PageSize: int32(limit),
		HasMore:  int64(offset+len(result.entries)) < result.total,
	})
}

func placeholderPage(entry *procmeshv1.AuditEntry, page int) auditPageResult {
	result := auditPageResult{total: 1, entries: []*procmeshv1.AuditEntry{}}
	if page == 1 {
		result.entries = append(result.entries, entry)
	}
	return result
}

func sortAuditEntries(entries []*procmeshv1.AuditEntry) {
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if auditTimestamp(left) != auditTimestamp(right) {
			return auditTimestamp(left) > auditTimestamp(right)
		}
		if left.GetSourceNode() != right.GetSourceNode() {
			return left.GetSourceNode() < right.GetSourceNode()
		}
		return left.GetEvent().GetAuditId() < right.GetEvent().GetAuditId()
	})
}

func sliceAuditPage(entries []*procmeshv1.AuditEntry, offset, limit int) []*procmeshv1.AuditEntry {
	if offset >= len(entries) {
		return []*procmeshv1.AuditEntry{}
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	return entries[offset:end]
}

func isUnavailablePlaceholder(e *procmeshv1.AuditEntry) bool {
	if e == nil {
		return false
	}
	ev := e.GetEvent()
	return ev != nil && ev.GetAction() == "unavailable" && ev.GetResult() == "UNAVAILABLE"
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
