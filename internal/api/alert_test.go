package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestAlertAPI_PutChannelMissingOperationID(t *testing.T) {
	e := newRBACEnv(t)
	adminSid := e.loginAs(t, "admin", testAdminPass)
	cli := procmeshv1connect.NewAlertServiceClient(e.http, e.url)
	_, err := cli.PutAlertChannel(context.Background(), bearerReq(adminSid, &procmeshv1.PutAlertChannelRequest{
		Type:       "WEBHOOK",
		Name:       "hook",
		Enabled:    true,
		ConfigJson: `{"url":"https://hooks.example.com"}`,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestAlertAPI_ViewerPutChannelDenied(t *testing.T) {
	e := newRBACEnv(t)
	viewSid := e.loginAs(t, "viewer", testAdminPass)
	cli := procmeshv1connect.NewAlertServiceClient(e.http, e.url)
	_, err := cli.PutAlertChannel(context.Background(), bearerReq(viewSid, &procmeshv1.PutAlertChannelRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-view-ch", Operator: "viewer"},
		Type:       "WEBHOOK",
		Name:       "hook",
		Enabled:    true,
		ConfigJson: `{"url":"https://hooks.example.com"}`,
	}))
	assertDenied(t, err)
}

func TestAlertAPI_PutChannelRedactsAndKeepsSecret(t *testing.T) {
	ctx := context.Background()
	e := newRBACEnv(t)
	adminSid := e.loginAs(t, "admin", testAdminPass)
	cli := procmeshv1connect.NewAlertServiceClient(e.http, e.url)
	created, err := cli.PutAlertChannel(ctx, bearerReq(adminSid, &procmeshv1.PutAlertChannelRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-ch-1", Operator: "admin"},
		Type:       "WEBHOOK",
		Name:       "hook",
		Enabled:    true,
		ConfigJson: `{"url":"https://hooks.example.com","hmac_secret":"s3cret"}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	cfg := created.Msg.GetChannel().GetConfigJson()
	if strings.Contains(cfg, "s3cret") || strings.Contains(cfg, "hmac_secret") {
		t.Fatalf("put response leaked secret: %s", cfg)
	}
	id := created.Msg.GetChannel().GetChannelId()
	if id == "" {
		t.Fatal("empty channel_id")
	}

	list, err := cli.ListAlertChannels(ctx, bearerReq(adminSid, &procmeshv1.ListAlertChannelsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.GetChannels()) != 1 {
		t.Fatalf("channels=%d", len(list.Msg.GetChannels()))
	}
	listed := list.Msg.GetChannels()[0].GetConfigJson()
	if strings.Contains(listed, "s3cret") || strings.Contains(listed, "hmac_secret") {
		t.Fatalf("list leaked secret: %s", listed)
	}

	_, err = cli.PutAlertChannel(ctx, bearerReq(adminSid, &procmeshv1.PutAlertChannelRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-ch-2", Operator: "admin"},
		ChannelId:  id,
		Type:       "WEBHOOK",
		Name:       "hook",
		Enabled:    true,
		ConfigJson: `{"url":"https://hooks.example.com","hmac_secret":""}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	ch, ok := e.svc.Store().View().AlertChannels[id]
	if !ok {
		t.Fatal("channel missing from FSM")
	}
	if !strings.Contains(ch.ConfigJSON, "s3cret") {
		t.Fatalf("empty secret put dropped hmac: %s", ch.ConfigJSON)
	}
}

func TestAlertAPI_ListAlertsLocalLive(t *testing.T) {
	ctx := context.Background()
	st := openStoreAt(t, t.TempDir()+"/alert-live.db")
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	if err := st.UpsertAlert(ctx, store.AlertRecord{
		AlertID: "a1", Fingerprint: "PROCESS_EXIT:p1", Type: "PROCESS_EXIT",
		Severity: "WARNING", NodeID: "node-a", ProcessID: "p1", PayloadJSON: `{}`,
		State: "FIRING", FirstAt: now.Add(-time.Minute), LastAt: now.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	api := &AlertAPI{Store: st, LocalID: "node-a", Now: func() time.Time { return now }}
	resp, err := api.ListAlerts(ctx, connect.NewRequest(&procmeshv1.ListAlertsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetEntries()) != 1 {
		t.Fatalf("entries=%d want 1: %+v", len(resp.Msg.GetEntries()), resp.Msg.GetEntries())
	}
	e := resp.Msg.GetEntries()[0]
	if e.GetFreshness() != freshness.LIVE {
		t.Fatalf("freshness=%q want LIVE", e.GetFreshness())
	}
	if e.GetSourceNode() != "node-a" {
		t.Fatalf("source_node=%q", e.GetSourceNode())
	}
	if e.GetAlert() == nil || e.GetAlert().GetAlertId() != "a1" {
		t.Fatalf("alert %+v", e.GetAlert())
	}
}

func TestAlertAPI_AliveHopFailStalePlaceholder(t *testing.T) {
	now := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	api := &AlertAPI{
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
	resp, err := api.ListAlerts(context.Background(), connect.NewRequest(&procmeshv1.ListAlertsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetEntries()) == 0 {
		t.Fatal("want STALE placeholder even with 0 local rows")
	}
	var placeholder *procmeshv1.AlertEntry
	for _, e := range resp.Msg.GetEntries() {
		if e.GetSourceNode() == "node-c" {
			placeholder = e
			break
		}
	}
	if placeholder == nil {
		t.Fatalf("missing node-c entry: %+v", resp.Msg.GetEntries())
	}
	if placeholder.GetFreshness() != freshness.STALE {
		t.Fatalf("freshness=%q want STALE", placeholder.GetFreshness())
	}
}

func TestAlertAPI_PutChannelNoQuorumUnavailable(t *testing.T) {
	e := newRBACEnv(t)
	adminSid := e.loginAs(t, "admin", testAdminPass)
	mem, ok := e.svc.Store().(*memAuthStore)
	if !ok {
		t.Fatalf("store %T", e.svc.Store())
	}
	mem.quorum = false
	cli := procmeshv1connect.NewAlertServiceClient(e.http, e.url)
	_, err := cli.PutAlertChannel(context.Background(), bearerReq(adminSid, &procmeshv1.PutAlertChannelRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-nq", Operator: "admin"},
		Type:       "WEBHOOK",
		Name:       "hook",
		Enabled:    true,
		ConfigJson: `{"url":"https://hooks.example.com"}`,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}
