package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestAuditAPI_LocalEntriesLive(t *testing.T) {
	ctx := context.Background()
	st := openStoreAt(t, t.TempDir()+"/audit.db")
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	if err := st.AppendAudit(ctx, store.AuditEvent{
		AuditID: "a1", Timestamp: now.Add(-2 * time.Second),
		Resource: "nginx", Action: "process.start", Result: "SUCCESS",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, store.AuditEvent{
		AuditID: "a2", Timestamp: now.Add(-time.Second),
		Resource: "api", Action: "process.stop", Result: "SUCCESS",
	}); err != nil {
		t.Fatal(err)
	}

	api := &AuditAPI{Store: st, LocalID: "node-a", Now: func() time.Time { return now }}
	resp, err := api.ListAudit(ctx, connect.NewRequest(&procmeshv1.ListAuditRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetEntries()) != 2 {
		t.Fatalf("entries=%d want 2: %+v", len(resp.Msg.GetEntries()), resp.Msg.GetEntries())
	}
	for i, e := range resp.Msg.GetEntries() {
		if e.GetFreshness() != freshness.LIVE {
			t.Fatalf("entry[%d] freshness=%q want LIVE", i, e.GetFreshness())
		}
		if e.GetSourceNode() != "node-a" {
			t.Fatalf("entry[%d] source_node=%q", i, e.GetSourceNode())
		}
		if e.GetLastUpdatedUnixMs() != now.UnixMilli() {
			t.Fatalf("entry[%d] last_updated=%d want %d", i, e.GetLastUpdatedUnixMs(), now.UnixMilli())
		}
		if e.GetEvent() == nil {
			t.Fatalf("entry[%d] missing event", i)
		}
	}
}

func TestAuditAPI_FailedMemberPlaceholder(t *testing.T) {
	ctx := context.Background()
	st := openStoreAt(t, t.TempDir()+"/audit-fail.db")
	now := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	if err := st.AppendAudit(ctx, store.AuditEvent{
		AuditID: "local-1", Timestamp: now.Add(-time.Second),
		Resource: "nginx", Action: "process.start", Result: "SUCCESS",
	}); err != nil {
		t.Fatal(err)
	}

	api := &AuditAPI{
		Store:   st,
		LocalID: "node-a",
		Now:     func() time.Time { return now },
		Router: &Router{
			LocalID: "node-a",
			Members: func() []cluster.NodeSummary {
				return []cluster.NodeSummary{
					{NodeID: "node-a", State: cluster.StateAlive, LastUpdatedUnixMs: now.UnixMilli()},
					{NodeID: "node-b", State: cluster.StateFailed, LastUpdatedUnixMs: now.Add(-time.Minute).UnixMilli()},
				}
			},
		},
	}
	resp, err := api.ListAudit(ctx, connect.NewRequest(&procmeshv1.ListAuditRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var realN, placeholderN int
	for _, e := range resp.Msg.GetEntries() {
		ev := e.GetEvent()
		if ev != nil && ev.GetResult() == "UNAVAILABLE" && ev.GetAction() == "unavailable" {
			placeholderN++
			if e.GetFreshness() == freshness.LIVE {
				t.Fatalf("placeholder freshness must not be LIVE: %+v", e)
			}
			if e.GetSourceNode() != "node-b" {
				t.Fatalf("placeholder source_node=%q", e.GetSourceNode())
			}
			continue
		}
		if ev != nil && ev.GetAuditId() == "local-1" {
			realN++
		}
	}
	if realN < 1 || placeholderN < 1 {
		t.Fatalf("real=%d placeholder=%d entries=%+v", realN, placeholderN, resp.Msg.GetEntries())
	}
}

func TestAuditAPI_AggregateFailedAndAliveConcurrent(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	started := make(chan struct{})
	release := make(chan struct{})
	fwd := &blockingAuditForwarder{started: started, release: release, err: errors.New("dial failed")}
	api := &AuditAPI{
		LocalID: "node-a",
		Now:     func() time.Time { return now },
		Forward: fwd,
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "node-a", State: cluster.StateAlive, LastUpdatedUnixMs: now.UnixMilli()},
				{NodeID: "node-b", State: cluster.StateFailed, LastUpdatedUnixMs: now.Add(-time.Minute).UnixMilli()},
				{NodeID: "node-c", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9003", LastUpdatedUnixMs: now.UnixMilli()},
			}
		},
	}

	errCh := make(chan error, 1)
	var resp *connect.Response[procmeshv1.ListAuditResponse]
	go func() {
		var err error
		resp, err = api.ListAudit(context.Background(), connect.NewRequest(&procmeshv1.ListAuditRequest{}))
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("ALIVE hop did not start")
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, e := range resp.Msg.GetEntries() {
		ev := e.GetEvent()
		if ev == nil || ev.GetResult() != "UNAVAILABLE" {
			continue
		}
		got[e.GetSourceNode()] = e.GetFreshness()
		if e.GetFreshness() == freshness.LIVE {
			t.Fatalf("placeholder %s freshness must not be LIVE", e.GetSourceNode())
		}
	}
	if _, ok := got["node-b"]; !ok {
		t.Fatalf("missing FAILED placeholder: %+v", resp.Msg.GetEntries())
	}
	if _, ok := got["node-c"]; !ok {
		t.Fatalf("missing ALIVE hop-fail placeholder: %+v", resp.Msg.GetEntries())
	}
}

func TestAuditAPI_AliveHopFailPlaceholderNotLive(t *testing.T) {
	now := time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)
	api := &AuditAPI{
		LocalID: "node-a",
		Now:     func() time.Time { return now },
		Forward: &blockingAuditForwarder{err: errors.New("unavailable")},
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "node-a", State: cluster.StateAlive, LastUpdatedUnixMs: now.UnixMilli()},
				{NodeID: "node-c", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9003", LastUpdatedUnixMs: now.UnixMilli()},
			}
		},
	}
	resp, err := api.ListAudit(context.Background(), connect.NewRequest(&procmeshv1.ListAuditRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var placeholder *procmeshv1.AuditEntry
	for _, e := range resp.Msg.GetEntries() {
		if e.GetSourceNode() == "node-c" {
			placeholder = e
			break
		}
	}
	if placeholder == nil || placeholder.GetEvent() == nil || placeholder.GetEvent().GetResult() != "UNAVAILABLE" {
		t.Fatalf("want ALIVE hop-fail placeholder, got %+v", resp.Msg.GetEntries())
	}
	if placeholder.GetFreshness() == freshness.LIVE {
		t.Fatalf("placeholder freshness must not be LIVE: %+v", placeholder)
	}
	if placeholder.GetFreshness() != freshness.STALE && placeholder.GetFreshness() != freshness.UNKNOWN {
		t.Fatalf("placeholder freshness=%q want STALE or UNKNOWN", placeholder.GetFreshness())
	}
}

func TestAuditAPI_PlaceholderSurvivesLimitTruncation(t *testing.T) {
	ctx := context.Background()
	st := openStoreAt(t, t.TempDir()+"/audit-limit.db")
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		if err := st.AppendAudit(ctx, store.AuditEvent{
			AuditID:   fmt.Sprintf("local-%02d", i),
			Timestamp: now.Add(-time.Duration(i+1) * time.Second),
			Resource:  "nginx",
			Action:    "process.start",
			Result:    "SUCCESS",
		}); err != nil {
			t.Fatal(err)
		}
	}

	api := &AuditAPI{
		Store:   st,
		LocalID: "node-a",
		Now:     func() time.Time { return now },
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "node-a", State: cluster.StateAlive, LastUpdatedUnixMs: now.UnixMilli()},
				{NodeID: "node-b", State: cluster.StateFailed, LastUpdatedUnixMs: now.Add(-time.Minute).UnixMilli()},
			}
		},
	}
	resp, err := api.ListAudit(ctx, connect.NewRequest(&procmeshv1.ListAuditRequest{Limit: 50}))
	if err != nil {
		t.Fatal(err)
	}
	entries := resp.Msg.GetEntries()
	if len(entries) != 50 {
		t.Fatalf("entries=%d want 50", len(entries))
	}
	var placeholder *procmeshv1.AuditEntry
	for _, e := range entries {
		if isUnavailablePlaceholder(e) {
			placeholder = e
			break
		}
	}
	if placeholder == nil {
		t.Fatalf("FAILED placeholder dropped after limit truncation: %+v", entries[0])
	}
	if placeholder.GetSourceNode() != "node-b" {
		t.Fatalf("placeholder source_node=%q", placeholder.GetSourceNode())
	}
	if placeholder.GetFreshness() == freshness.LIVE {
		t.Fatalf("placeholder freshness must not be LIVE: %+v", placeholder)
	}
	if ev := placeholder.GetEvent(); ev == nil || ev.GetTimestampUnixMs() != now.UnixMilli() {
		t.Fatalf("placeholder timestamp=%v want now", ev)
	}
}

func TestAuditAPI_LocalPaginationMetadata(t *testing.T) {
	ctx := context.Background()
	st := openStoreAt(t, t.TempDir()+"/audit-page.db")
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := st.AppendAudit(ctx, store.AuditEvent{
			AuditID:   fmt.Sprintf("local-%d", i),
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Resource:  "process/api",
			Action:    "process.start",
			Result:    "SUCCESS",
		}); err != nil {
			t.Fatal(err)
		}
	}

	api := &AuditAPI{Store: st, LocalID: "node-a", Now: func() time.Time { return now }}
	resp, err := api.ListAudit(ctx, connect.NewRequest(&procmeshv1.ListAuditRequest{
		Resource: "process/api", Limit: 2, Page: 2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetTotal() != 5 || resp.Msg.GetPage() != 2 || resp.Msg.GetPageSize() != 2 || !resp.Msg.GetHasMore() {
		t.Fatalf("pagination=%+v", resp.Msg)
	}
	entries := resp.Msg.GetEntries()
	if len(entries) != 2 || entries[0].GetEvent().GetAuditId() != "local-2" || entries[1].GetEvent().GetAuditId() != "local-1" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestAuditAPI_AggregatePaginationUsesGlobalOrder(t *testing.T) {
	ctx := context.Background()
	st := openStoreAt(t, t.TempDir()+"/audit-global-page.db")
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id     string
		second int
	}{
		{id: "local-10", second: 10},
		{id: "local-08", second: 8},
		{id: "local-06", second: 6},
	} {
		if err := st.AppendAudit(ctx, store.AuditEvent{
			AuditID: item.id, Timestamp: base.Add(time.Duration(item.second) * time.Second),
			Resource: "process/api", Action: "process.start", Result: "SUCCESS",
		}); err != nil {
			t.Fatal(err)
		}
	}

	remote := []*procmeshv1.AuditEntry{
		auditTestEntry("remote-09", "node-b", base.Add(9*time.Second)),
		auditTestEntry("remote-07", "node-b", base.Add(7*time.Second)),
		auditTestEntry("remote-05", "node-b", base.Add(5*time.Second)),
	}
	api := &AuditAPI{
		Store: st, LocalID: "node-a", Now: func() time.Time { return base.Add(20 * time.Second) },
		Forward: &blockingAuditForwarder{client: &pagedAuditClient{entries: remote}},
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "node-a", State: cluster.StateAlive},
				{NodeID: "node-b", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9003"},
			}
		},
	}
	resp, err := api.ListAudit(ctx, connect.NewRequest(&procmeshv1.ListAuditRequest{
		Resource: "process/api", Limit: 2, Page: 2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	entries := resp.Msg.GetEntries()
	if resp.Msg.GetTotal() != 6 || len(entries) != 2 {
		t.Fatalf("total=%d entries=%+v", resp.Msg.GetTotal(), entries)
	}
	if entries[0].GetEvent().GetAuditId() != "local-08" || entries[1].GetEvent().GetAuditId() != "remote-07" {
		t.Fatalf("global page=%+v want local-08,remote-07", entries)
	}
}

func TestAuditAPI_AggregatePaginationFetchesRemoteChunks(t *testing.T) {
	ctx := context.Background()
	remote := make([]*procmeshv1.AuditEntry, 250)
	base := time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)
	for i := range remote {
		remote[i] = auditTestEntry(fmt.Sprintf("remote-%03d", i), "node-b", base.Add(-time.Duration(i)*time.Second))
	}
	remoteClient := &pagedAuditClient{entries: remote}
	api := &AuditAPI{
		LocalID: "node-a", Now: func() time.Time { return base },
		Forward: &blockingAuditForwarder{client: remoteClient},
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "node-a", State: cluster.StateAlive},
				{NodeID: "node-b", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9003"},
			}
		},
	}
	resp, err := api.ListAudit(ctx, connect.NewRequest(&procmeshv1.ListAuditRequest{Limit: 100, Page: 3}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetTotal() != 250 || len(resp.Msg.GetEntries()) != 50 || resp.Msg.GetHasMore() {
		t.Fatalf("response=%+v", resp.Msg)
	}
	if got := resp.Msg.GetEntries()[0].GetEvent().GetAuditId(); got != "remote-200" {
		t.Fatalf("first audit=%q want remote-200", got)
	}
	if len(remoteClient.requests) != 2 || remoteClient.requests[0] != [2]int{1, 200} || remoteClient.requests[1] != [2]int{2, 200} {
		t.Fatalf("remote requests=%v want [[1 200] [2 200]]", remoteClient.requests)
	}
}

func auditTestEntry(id, node string, timestamp time.Time) *procmeshv1.AuditEntry {
	return &procmeshv1.AuditEntry{
		Event: &procmeshv1.AuditEvent{
			AuditId: id, TimestampUnixMs: timestamp.UnixMilli(), Resource: "process/api",
			Action: "process.start", Result: "SUCCESS",
		},
		SourceNode: node, Freshness: freshness.LIVE, LastUpdatedUnixMs: timestamp.UnixMilli(),
	}
}

func TestAuditAPI_ViewerDenied(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	putViewerUser(t, svc)
	api := &AuditAPI{Auth: svc}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-view", Username: "viewer"})
	_, err := api.ListAudit(ctx, connect.NewRequest(&procmeshv1.ListAuditRequest{}))
	assertDenied(t, err)
}

type blockingAuditForwarder struct {
	started chan struct{}
	release chan struct{}
	err     error
	client  procmeshv1connect.AuditServiceClient
}

type pagedAuditClient struct {
	entries  []*procmeshv1.AuditEntry
	requests [][2]int
}

func (c *pagedAuditClient) ListAudit(_ context.Context, req *connect.Request[procmeshv1.ListAuditRequest]) (*connect.Response[procmeshv1.ListAuditResponse], error) {
	limit := normalizeAuditLimit(req.Msg.GetLimit())
	page := normalizeAuditPage(req.Msg.GetPage())
	c.requests = append(c.requests, [2]int{page, limit})
	start := (page - 1) * limit
	end := start + limit
	if start > len(c.entries) {
		start = len(c.entries)
	}
	if end > len(c.entries) {
		end = len(c.entries)
	}
	return connect.NewResponse(&procmeshv1.ListAuditResponse{
		Entries: c.entries[start:end], Total: int64(len(c.entries)), Page: int32(page),
		PageSize: int32(limit), HasMore: end < len(c.entries),
	}), nil
}

func (f *blockingAuditForwarder) Process(context.Context, Route) (procmeshv1connect.ProcessServiceClient, error) {
	return nil, errors.New("unused")
}

func (f *blockingAuditForwarder) Config(context.Context, Route) (procmeshv1connect.ConfigServiceClient, error) {
	return nil, errors.New("unused")
}

func (f *blockingAuditForwarder) Log(context.Context, Route) (procmeshv1connect.LogServiceClient, error) {
	return nil, errors.New("unused")
}

func (f *blockingAuditForwarder) Metrics(context.Context, Route) (procmeshv1connect.MetricsServiceClient, error) {
	return nil, errors.New("unused")
}

func (f *blockingAuditForwarder) Alert(ctx context.Context, rt Route) (procmeshv1connect.AlertServiceClient, error) {
	_, err := f.Audit(ctx, rt)
	if err != nil {
		return nil, err
	}
	return nil, errors.New("unavailable")
}

func (f *blockingAuditForwarder) Backup(ctx context.Context, rt Route) (procmeshv1connect.BackupServiceClient, error) {
	_, err := f.Audit(ctx, rt)
	if err != nil {
		return nil, err
	}
	return nil, errors.New("unavailable")
}

func (f *blockingAuditForwarder) Audit(ctx context.Context, _ Route) (procmeshv1connect.AuditServiceClient, error) {
	if f.started != nil {
		select {
		case <-f.started:
		default:
			close(f.started)
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.client != nil {
		return f.client, nil
	}
	return nil, errors.New("unavailable")
}
