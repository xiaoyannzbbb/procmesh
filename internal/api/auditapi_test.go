package api

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
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

func TestAuditAPI_ViewerDenied(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	putViewerUser(t, svc)
	api := &AuditAPI{Auth: svc}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-view", Username: "viewer"})
	_, err := api.ListAudit(ctx, connect.NewRequest(&procmeshv1.ListAuditRequest{}))
	assertDenied(t, err)
}
