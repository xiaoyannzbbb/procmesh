package api

import (
	"context"
	"errors"
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
	return nil, errors.New("unavailable")
}
