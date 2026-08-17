package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/alert"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
	"golang.org/x/sync/errgroup"
)

var _ procmeshv1connect.AlertServiceHandler = (*AlertAPI)(nil)

const alertHopTimeout = 2 * time.Second

type AlertAPI struct {
	Store     *store.Store
	Auth      *auth.Service
	Engine    *alert.Engine // 可空；List 不需要
	LocalOnly bool
	LocalID   string
	Router    *Router
	Forward   Forwarder
	Members   func() []cluster.NodeSummary
	Now       func() time.Time
}

func (s *AlertAPI) ListAlerts(ctx context.Context, req *connect.Request[procmeshv1.ListAlertsRequest]) (*connect.Response[procmeshv1.ListAlertsResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermAlertRead, "", false, true); err != nil {
		return nil, err
	}
	limit := normalizeAuditLimit(req.Msg.GetLimit())
	now := s.now()
	state := req.Msg.GetState()
	target := req.Msg.GetTargetNode()

	if !s.LocalOnly && target != "" && !s.isLocalNode(target) {
		entries := s.listTarget(ctx, req, target, state, limit, now)
		return connect.NewResponse(&procmeshv1.ListAlertsResponse{Entries: entries}), nil
	}

	entries, err := s.listLocal(ctx, state, limit, now)
	if err != nil {
		return nil, ToConnect(err)
	}
	if !s.LocalOnly && target == "" {
		entries = append(entries, s.aggregateRemotes(ctx, req, state, limit, now)...)
		entries = filterAlertEntries(entries, state)
		sortAlertEntries(entries)
		entries = applyAlertLimit(entries, limit)
	}
	return connect.NewResponse(&procmeshv1.ListAlertsResponse{Entries: entries}), nil
}

func (s *AlertAPI) listTarget(ctx context.Context, req *connect.Request[procmeshv1.ListAlertsRequest], target, state string, limit int, now time.Time) []*procmeshv1.AlertEntry {
	h := req.Header().Clone()
	rpc.SetTarget(h, target)
	local, rt, err := hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, h, "", "")
	if err != nil || local {
		return []*procmeshv1.AlertEntry{s.placeholder(target, now)}
	}
	entries, herr := s.hopNode(ctx, req, rt, state, limit)
	if herr != nil {
		nodeID := rt.NodeID
		if nodeID == "" {
			nodeID = target
		}
		return []*procmeshv1.AlertEntry{s.placeholder(nodeID, now)}
	}
	return entries
}

func (s *AlertAPI) aggregateRemotes(ctx context.Context, req *connect.Request[procmeshv1.ListAlertsRequest], state string, limit int, now time.Time) []*procmeshv1.AlertEntry {
	var (
		out   []*procmeshv1.AlertEntry
		alive []cluster.NodeSummary
		mu    sync.Mutex
		g     errgroup.Group
	)
	for _, m := range s.memberList() {
		if m.NodeID == "" || m.NodeID == s.LocalID {
			continue
		}
		switch m.State {
		case cluster.StateAlive:
			alive = append(alive, m)
		default:
			// LEFT/FAILED/SUSPECT/REMOVED: still emit STALE so a departed
			// peer cannot look like an empty inbox.
			out = append(out, unavailableAlertEntry(m, now))
		}
	}
	for _, m := range alive {
		m := m
		g.Go(func() error {
			hopCtx, cancel := context.WithTimeout(ctx, alertHopTimeout)
			defer cancel()
			rt := Route{NodeID: m.NodeID, RPC: m.RPCAddress}
			ents, err := s.hopNode(hopCtx, req, rt, state, limit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out = append(out, unavailableAlertEntry(m, now))
				return nil
			}
			out = append(out, ents...)
			return nil
		})
	}
	_ = g.Wait()
	return out
}

func (s *AlertAPI) hopNode(ctx context.Context, req *connect.Request[procmeshv1.ListAlertsRequest], rt Route, state string, limit int) ([]*procmeshv1.AlertEntry, error) {
	if s.Forward == nil || rt.RPC == "" {
		return nil, unavailableOwner()
	}
	fwd := connect.NewRequest(&procmeshv1.ListAlertsRequest{
		State: state,
		Limit: int32(limit),
	})
	for k, vs := range req.Header() {
		for _, v := range vs {
			fwd.Header().Add(k, v)
		}
	}
	stampHop(fwd.Header(), s.LocalID, rt.NodeID)
	stampIdentity(fwd.Header(), ctx)
	cli, err := s.Forward.Alert(ctx, rt)
	if err != nil {
		return nil, err
	}
	out, err := cli.ListAlerts(ctx, fwd)
	if err != nil {
		return nil, err
	}
	if out == nil || out.Msg == nil {
		return []*procmeshv1.AlertEntry{}, nil
	}
	return out.Msg.GetEntries(), nil
}

func (s *AlertAPI) listLocal(ctx context.Context, state string, limit int, now time.Time) ([]*procmeshv1.AlertEntry, error) {
	if s.Store == nil {
		return []*procmeshv1.AlertEntry{}, nil
	}
	recs, err := s.Store.ListAlerts(ctx, limit, state)
	if err != nil {
		return nil, err
	}
	nowMs := now.UnixMilli()
	out := make([]*procmeshv1.AlertEntry, 0, len(recs))
	for _, rec := range recs {
		out = append(out, &procmeshv1.AlertEntry{
			Alert:             alertRecordToProto(rec),
			SourceNode:        s.LocalID,
			Freshness:         freshness.LIVE,
			LastUpdatedUnixMs: nowMs,
		})
	}
	return out, nil
}

func (s *AlertAPI) placeholder(nodeID string, now time.Time) *procmeshv1.AlertEntry {
	var lastMs int64
	state := ""
	for _, m := range s.memberList() {
		if m.NodeID == nodeID {
			lastMs = m.LastUpdatedUnixMs
			state = string(m.State)
			break
		}
	}
	return &procmeshv1.AlertEntry{
		SourceNode:        nodeID,
		Freshness:         placeholderFreshness(now, lastMs, state),
		LastUpdatedUnixMs: lastMs,
	}
}

func unavailableAlertEntry(m cluster.NodeSummary, now time.Time) *procmeshv1.AlertEntry {
	return &procmeshv1.AlertEntry{
		SourceNode:        m.NodeID,
		Freshness:         placeholderFreshness(now, m.LastUpdatedUnixMs, string(m.State)),
		LastUpdatedUnixMs: m.LastUpdatedUnixMs,
	}
}

func (s *AlertAPI) GetAlert(ctx context.Context, req *connect.Request[procmeshv1.GetAlertRequest]) (*connect.Response[procmeshv1.GetAlertResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermAlertRead, "", false, true); err != nil {
		return nil, err
	}
	if s.Store == nil {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "alert"))
	}
	rec, err := s.Store.GetAlert(ctx, req.Msg.GetAlertId())
	if err != nil {
		return nil, ToConnect(err)
	}
	now := s.now()
	return connect.NewResponse(&procmeshv1.GetAlertResponse{
		Entry: &procmeshv1.AlertEntry{
			Alert:             alertRecordToProto(rec),
			SourceNode:        s.LocalID,
			Freshness:         freshness.LIVE,
			LastUpdatedUnixMs: now.UnixMilli(),
		},
	}), nil
}

func (s *AlertAPI) ListAlertChannels(ctx context.Context, _ *connect.Request[procmeshv1.ListAlertChannelsRequest]) (*connect.Response[procmeshv1.ListAlertChannelsResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermAlertRead, "", false, true); err != nil {
		return nil, err
	}
	st := s.Auth.Store().View()
	ids := make([]string, 0, len(st.AlertChannels))
	for id := range st.AlertChannels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := &procmeshv1.ListAlertChannelsResponse{}
	for _, id := range ids {
		out.Channels = append(out.Channels, alertChannelToProto(st.AlertChannels[id]))
	}
	return connect.NewResponse(out), nil
}

func (s *AlertAPI) PutAlertChannel(ctx context.Context, req *connect.Request[procmeshv1.PutAlertChannelRequest]) (*connect.Response[procmeshv1.PutAlertChannelResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermAlertManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.Msg.GetChannelId())
	if id == "" {
		var err error
		id, err = newAuthID()
		if err != nil {
			return nil, ToConnect(err)
		}
	}
	old := ""
	if ch, ok := s.Auth.Store().View().AlertChannels[id]; ok {
		old = ch.ConfigJSON
	}
	if err := applyAuth(s.Auth, control.CmdAlertChannelPut, control.AlertChannelPutBody{
		ChannelID:  id,
		Type:       req.Msg.GetType(),
		Name:       req.Msg.GetName(),
		Enabled:    req.Msg.GetEnabled(),
		ConfigJSON: mergeChannelConfig(old, req.Msg.GetConfigJson()),
		NowUnix:    time.Now().Unix(),
	}); err != nil {
		return nil, err
	}
	ch := s.Auth.Store().View().AlertChannels[id]
	return connect.NewResponse(&procmeshv1.PutAlertChannelResponse{Channel: alertChannelToProto(ch)}), nil
}

func (s *AlertAPI) DeleteAlertChannel(ctx context.Context, req *connect.Request[procmeshv1.DeleteAlertChannelRequest]) (*connect.Response[procmeshv1.DeleteAlertChannelResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermAlertManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := applyAuth(s.Auth, control.CmdAlertChannelDelete, control.AlertChannelDeleteBody{
		ChannelID: req.Msg.GetChannelId(),
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.DeleteAlertChannelResponse{}), nil
}

func (s *AlertAPI) GetAlertPolicy(ctx context.Context, _ *connect.Request[procmeshv1.GetAlertPolicyRequest]) (*connect.Response[procmeshv1.GetAlertPolicyResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermAlertRead, "", false, true); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.GetAlertPolicyResponse{
		Policy: alertPolicyToProto(s.Auth.Store().View().AlertPolicy),
	}), nil
}

func (s *AlertAPI) PutAlertPolicy(ctx context.Context, req *connect.Request[procmeshv1.PutAlertPolicyRequest]) (*connect.Response[procmeshv1.PutAlertPolicyResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermAlertManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	p := req.Msg.GetPolicy()
	if p == nil {
		return nil, ToConnect(errcode.E(errcode.INVALID, "policy required"))
	}
	if err := applyAuth(s.Auth, control.CmdAlertPolicyPut, control.AlertPolicyPutBody{
		DedupWindowSec:      p.GetDedupWindowSec(),
		NotifyOnResolve:     p.GetNotifyOnResolve(),
		CPUHighPercent:      int(p.GetCpuHighPercent()),
		MemoryHighPercent:   int(p.GetMemoryHighPercent()),
		DiskHighPercent:     int(p.GetDiskHighPercent()),
		HighConsecutiveMins: int(p.GetHighConsecutiveMins()),
		SuspectTooLongSec:   p.GetSuspectTooLongSec(),
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&procmeshv1.PutAlertPolicyResponse{
		Policy: alertPolicyToProto(s.Auth.Store().View().AlertPolicy),
	}), nil
}

func (s *AlertAPI) memberList() []cluster.NodeSummary {
	if s.Members != nil {
		return s.Members()
	}
	if s.Router != nil && s.Router.Members != nil {
		return s.Router.Members()
	}
	return nil
}

func (s *AlertAPI) isLocalNode(id string) bool {
	if id == "" || id == s.LocalID {
		return true
	}
	if s.Router != nil {
		return s.Router.isLocalIdentity(id)
	}
	return false
}

func (s *AlertAPI) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func filterAlertEntries(entries []*procmeshv1.AlertEntry, state string) []*procmeshv1.AlertEntry {
	if state == "" {
		return entries
	}
	out := make([]*procmeshv1.AlertEntry, 0, len(entries))
	for _, e := range entries {
		if isAlertPlaceholder(e) || (e.GetAlert() != nil && e.GetAlert().GetState() == state) {
			out = append(out, e)
		}
	}
	return out
}

func sortAlertEntries(entries []*procmeshv1.AlertEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return alertLastUnixMs(entries[i]) > alertLastUnixMs(entries[j])
	})
}

func applyAlertLimit(entries []*procmeshv1.AlertEntry, limit int) []*procmeshv1.AlertEntry {
	if limit <= 0 || len(entries) <= limit {
		return entries
	}
	var placeholders, rest []*procmeshv1.AlertEntry
	for _, e := range entries {
		if isAlertPlaceholder(e) {
			placeholders = append(placeholders, e)
			continue
		}
		rest = append(rest, e)
	}
	keep := limit - len(placeholders)
	if keep < 0 {
		return placeholders[:limit]
	}
	if keep > len(rest) {
		keep = len(rest)
	}
	out := make([]*procmeshv1.AlertEntry, 0, keep+len(placeholders))
	out = append(out, rest[:keep]...)
	out = append(out, placeholders...)
	return out
}

func isAlertPlaceholder(e *procmeshv1.AlertEntry) bool {
	return e != nil && e.GetAlert() == nil
}

func alertLastUnixMs(e *procmeshv1.AlertEntry) int64 {
	if e == nil {
		return 0
	}
	if a := e.GetAlert(); a != nil {
		return a.GetLastUnixMs()
	}
	return e.GetLastUpdatedUnixMs()
}

func alertRecordToProto(rec store.AlertRecord) *procmeshv1.Alert {
	return &procmeshv1.Alert{
		AlertId:        rec.AlertID,
		Fingerprint:    rec.Fingerprint,
		Type:           rec.Type,
		Severity:       rec.Severity,
		NodeId:         rec.NodeID,
		ProcessId:      rec.ProcessID,
		PayloadJson:    rec.PayloadJSON,
		State:          rec.State,
		FirstUnixMs:    timeUnixMilli(rec.FirstAt),
		LastUnixMs:     timeUnixMilli(rec.LastAt),
		NotifiedUnixMs: timeUnixMilli(rec.NotifiedAt),
		ResolvedUnixMs: timeUnixMilli(rec.ResolvedAt),
		LastError:      rec.LastError,
	}
}

func timeUnixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

func alertChannelToProto(ch control.AlertChannel) *procmeshv1.AlertChannel {
	return &procmeshv1.AlertChannel{
		ChannelId:   ch.ChannelID,
		Type:        ch.Type,
		Name:        ch.Name,
		Enabled:     ch.Enabled,
		ConfigJson:  redactChannelConfig(ch.ConfigJSON),
		CreatedUnix: ch.CreatedUnix,
		UpdatedUnix: ch.UpdatedUnix,
	}
}

func alertPolicyToProto(p control.AlertPolicy) *procmeshv1.AlertPolicy {
	return &procmeshv1.AlertPolicy{
		DedupWindowSec:      p.DedupWindowSec,
		NotifyOnResolve:     p.NotifyOnResolve,
		CpuHighPercent:      int32(p.CPUHighPercent),
		MemoryHighPercent:   int32(p.MemoryHighPercent),
		DiskHighPercent:     int32(p.DiskHighPercent),
		HighConsecutiveMins: int32(p.HighConsecutiveMins),
		SuspectTooLongSec:   p.SuspectTooLongSec,
	}
}

func mergeChannelConfig(oldJSON, newJSON string) string {
	old := parseJSONObject(oldJSON)
	neu := parseJSONObject(newJSON)
	for _, k := range []string{"hmac_secret", "password", "secret"} {
		if jsonEmpty(neu[k]) {
			if v, ok := old[k]; ok && !jsonEmpty(v) {
				neu[k] = v
			}
		}
	}
	if _, hasHeaders := neu["headers"]; !hasHeaders {
		if v, ok := old["headers"]; ok {
			neu["headers"] = v
		}
	} else {
		neu["headers"] = mergeAuthHeader(old["headers"], neu["headers"])
	}
	b, err := json.Marshal(neu)
	if err != nil {
		return newJSON
	}
	return string(b)
}

func mergeAuthHeader(oldH, newH json.RawMessage) json.RawMessage {
	if jsonEmpty(newH) {
		if !jsonEmpty(oldH) {
			return oldH
		}
		return json.RawMessage(`{}`)
	}
	oldM := parseJSONObject(string(oldH))
	newM := parseJSONObject(string(newH))
	for _, k := range []string{"Authorization", "authorization"} {
		if jsonEmpty(newM[k]) {
			if v, ok := oldM[k]; ok && !jsonEmpty(v) {
				newM[k] = v
			}
		}
	}
	b, err := json.Marshal(newM)
	if err != nil {
		return newH
	}
	return b
}

func redactChannelConfig(raw string) string {
	obj := parseJSONObject(raw)
	delete(obj, "hmac_secret")
	delete(obj, "password")
	delete(obj, "secret")
	if h, ok := obj["headers"]; ok {
		hm := parseJSONObject(string(h))
		delete(hm, "Authorization")
		delete(hm, "authorization")
		if b, err := json.Marshal(hm); err == nil {
			obj["headers"] = b
		}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseJSONObject(raw string) map[string]json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]json.RawMessage{}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

func jsonEmpty(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s == "" || s == "null" || s == `""`
}
