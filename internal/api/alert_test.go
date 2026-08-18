package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/alert"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type captureAlertSender struct {
	channel control.AlertChannel
	record  store.AlertRecord
	err     error
	calls   int
}

func (s *captureAlertSender) Send(_ context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	s.calls++
	s.channel = ch
	s.record = rec
	return s.err
}

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

func TestAlertAPI_TestChannelUsesStoredSecretAndIgnoresEnabledState(t *testing.T) {
	e := newRBACEnv(t)
	applyAuthCmd(t, e.svc, control.CmdAlertChannelPut, control.AlertChannelPutBody{
		ChannelID: "ding-1", Type: "DINGTALK", Name: "ops", Enabled: false,
		ConfigJSON: `{"webhook_url":"https://example.invalid/robot","secret":"SEC-stored"}`,
	})
	sender := &captureAlertSender{}
	api := &AlertAPI{Auth: e.svc, Sender: sender}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})

	_, err := api.TestAlertChannel(ctx, connect.NewRequest(&procmeshv1.TestAlertChannelRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-test-ding", Operator: "admin"},
		ChannelId: "ding-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatalf("calls=%d want 1", sender.calls)
	}
	if !sender.channel.Enabled || !strings.Contains(sender.channel.ConfigJSON, "SEC-stored") {
		t.Fatalf("test channel did not use enabled full config: %+v", sender.channel)
	}
	if sender.record.Type != "CHANNEL_TEST" || sender.record.State != "TEST" {
		t.Fatalf("test record=%+v", sender.record)
	}
}

func TestAlertAPI_TestChannelMapsDeliveryErrors(t *testing.T) {
	cases := []struct {
		name       string
		sendErr    error
		wantCode   connect.Code
		wantDetail string
	}{
		{name: "network", sendErr: errors.New("dial tcp: connection refused"), wantCode: connect.CodeUnavailable, wantDetail: "UNAVAILABLE"},
		{name: "timeout", sendErr: context.DeadlineExceeded, wantCode: connect.CodeDeadlineExceeded, wantDetail: "TIMEOUT"},
		{name: "provider business error", sendErr: errcode.E(errcode.INVALID, "DingTalk error 310000"), wantCode: connect.CodeInvalidArgument, wantDetail: "INVALID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRBACEnv(t)
			applyAuthCmd(t, e.svc, control.CmdAlertChannelPut, control.AlertChannelPutBody{
				ChannelID: "ding-1", Type: "DINGTALK", Name: "ops", Enabled: true,
				ConfigJSON: `{"webhook_url":"https://example.invalid/robot"}`,
			})
			api := &AlertAPI{Auth: e.svc, Sender: &captureAlertSender{err: tc.sendErr}}
			ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})

			_, err := api.TestAlertChannel(ctx, connect.NewRequest(&procmeshv1.TestAlertChannelRequest{
				Meta:      &procmeshv1.MutationMeta{OperationId: "op-test-error", Operator: "admin"},
				ChannelId: "ding-1",
			}))
			code, detail := connectDetail(t, err)
			if code != tc.wantCode || detail != tc.wantDetail {
				t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
			}
		})
	}
}

func TestAlertAPI_TestChannelMapsHTTP5xxToUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	e := newRBACEnv(t)
	applyAuthCmd(t, e.svc, control.CmdAlertChannelPut, control.AlertChannelPutBody{
		ChannelID: "hook-1", Type: "WEBHOOK", Name: "ops", Enabled: true,
		ConfigJSON: `{"url":"` + srv.URL + `"}`,
	})
	api := &AlertAPI{
		Auth:   e.svc,
		Sender: &alert.ChannelSender{HTTP: srv.Client(), Attempts: 1},
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})

	_, err := api.TestAlertChannel(ctx, connect.NewRequest(&procmeshv1.TestAlertChannelRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-test-503", Operator: "admin"},
		ChannelId: "hook-1",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestAlertAPI_ViewerCannotTestChannel(t *testing.T) {
	e := newRBACEnv(t)
	applyAuthCmd(t, e.svc, control.CmdAlertChannelPut, control.AlertChannelPutBody{
		ChannelID: "hook-1", Type: "WEBHOOK", Name: "ops", Enabled: true,
		ConfigJSON: `{"url":"https://example.invalid/hook"}`,
	})
	sender := &captureAlertSender{}
	api := &AlertAPI{Auth: e.svc, Sender: sender}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-view", Username: "viewer"})

	_, err := api.TestAlertChannel(ctx, connect.NewRequest(&procmeshv1.TestAlertChannelRequest{
		Meta:      &procmeshv1.MutationMeta{OperationId: "op-test-viewer", Operator: "viewer"},
		ChannelId: "hook-1",
	}))
	assertDenied(t, err)
	if sender.calls != 0 {
		t.Fatalf("calls=%d want 0", sender.calls)
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

func TestAlertAPI_PutChannelEmptyConfigKeepsURL(t *testing.T) {
	ctx := context.Background()
	e := newRBACEnv(t)
	adminSid := e.loginAs(t, "admin", testAdminPass)
	cli := procmeshv1connect.NewAlertServiceClient(e.http, e.url)
	created, err := cli.PutAlertChannel(ctx, bearerReq(adminSid, &procmeshv1.PutAlertChannelRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-ch-empty-1", Operator: "admin"},
		Type:       "WEBHOOK",
		Name:       "hook",
		Enabled:    true,
		ConfigJson: `{"url":"https://hooks.example.com","hmac_secret":"s3cret"}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetChannel().GetChannelId()
	updated, err := cli.PutAlertChannel(ctx, bearerReq(adminSid, &procmeshv1.PutAlertChannelRequest{
		Meta:       &procmeshv1.MutationMeta{OperationId: "op-ch-empty-2", Operator: "admin"},
		ChannelId:  id,
		Type:       "WEBHOOK",
		Name:       "hook",
		Enabled:    true,
		ConfigJson: `{}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg := updated.Msg.GetChannel().GetConfigJson(); !strings.Contains(cfg, "https://hooks.example.com") {
		t.Fatalf("empty config wiped url in response: %s", cfg)
	}
	ch, ok := e.svc.Store().View().AlertChannels[id]
	if !ok {
		t.Fatal("channel missing from FSM")
	}
	if !strings.Contains(ch.ConfigJSON, "https://hooks.example.com") {
		t.Fatalf("empty config wiped url: %s", ch.ConfigJSON)
	}
	if !strings.Contains(ch.ConfigJSON, "s3cret") {
		t.Fatalf("empty config dropped hmac: %s", ch.ConfigJSON)
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

func TestAlertAPI_LeftMemberStalePlaceholder(t *testing.T) {
	now := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	api := &AlertAPI{
		LocalID: "node-a",
		Now:     func() time.Time { return now },
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "node-a", State: cluster.StateAlive, LastUpdatedUnixMs: now.UnixMilli()},
				{NodeID: "node-c", State: cluster.StateLeft, LastUpdatedUnixMs: now.UnixMilli()},
			}
		},
	}
	resp, err := api.ListAlerts(context.Background(), connect.NewRequest(&procmeshv1.ListAlertsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetEntries()) == 0 {
		t.Fatal("LEFT peer must not look like an empty inbox")
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

func TestAlertAPI_StateFilterKeepsOlderFiringAndStalePlaceholder(t *testing.T) {
	ctx := context.Background()
	st := openStoreAt(t, t.TempDir()+"/alert-state.db")
	now := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	firingAt := now.Add(-2 * time.Hour)
	if err := st.UpsertAlert(ctx, store.AlertRecord{
		AlertID: "old-fire", Fingerprint: "PROCESS_FATAL:old", Type: "PROCESS_FATAL",
		Severity: "CRITICAL", NodeID: "node-a", ProcessID: "old", PayloadJSON: `{}`,
		State: "FIRING", FirstAt: firingAt, LastAt: firingAt,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		at := now.Add(-time.Duration(i+1) * time.Minute)
		if err := st.UpsertAlert(ctx, store.AlertRecord{
			AlertID: fmt.Sprintf("r%02d", i), Fingerprint: fmt.Sprintf("PROCESS_EXIT:p%02d", i),
			Type: "PROCESS_EXIT", Severity: "WARNING", NodeID: "node-a",
			ProcessID: fmt.Sprintf("p%02d", i), PayloadJSON: `{}`,
			State: "RESOLVED", FirstAt: at, LastAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}

	api := &AlertAPI{
		Store:   st,
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
	resp, err := api.ListAlerts(ctx, connect.NewRequest(&procmeshv1.ListAlertsRequest{State: "FIRING", Limit: 50}))
	if err != nil {
		t.Fatal(err)
	}
	var firing, placeholder *procmeshv1.AlertEntry
	for _, e := range resp.Msg.GetEntries() {
		if e.GetAlert() != nil && e.GetAlert().GetAlertId() == "old-fire" {
			firing = e
		}
		if e.GetSourceNode() == "node-c" && e.GetAlert() == nil {
			placeholder = e
		}
	}
	if firing == nil || firing.GetFreshness() != freshness.LIVE || firing.GetAlert().GetState() != "FIRING" {
		t.Fatalf("older FIRING dropped by newest-N-then-filter: %+v", resp.Msg.GetEntries())
	}
	if placeholder == nil || placeholder.GetFreshness() != freshness.STALE {
		t.Fatalf("state=FIRING emptied inbox; missing STALE placeholder: %+v", resp.Msg.GetEntries())
	}
}

func TestAlertAPI_StateFilterHopFailKeepsStaleWithZeroFiring(t *testing.T) {
	ctx := context.Background()
	st := openStoreAt(t, t.TempDir()+"/alert-zero-firing.db")
	now := time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC)
	if err := st.UpsertAlert(ctx, store.AlertRecord{
		AlertID: "resolved-1", Fingerprint: "PROCESS_EXIT:p1", Type: "PROCESS_EXIT",
		Severity: "WARNING", NodeID: "node-a", ProcessID: "p1", PayloadJSON: `{}`,
		State: "RESOLVED", FirstAt: now.Add(-time.Minute), LastAt: now.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	api := &AlertAPI{
		Store:   st,
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
	resp, err := api.ListAlerts(ctx, connect.NewRequest(&procmeshv1.ListAlertsRequest{State: "FIRING"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetEntries()) == 0 {
		t.Fatal("state=FIRING + hop fail must not look like an empty inbox")
	}
	var placeholder *procmeshv1.AlertEntry
	for _, e := range resp.Msg.GetEntries() {
		if e.GetAlert() != nil && e.GetAlert().GetState() == "FIRING" {
			t.Fatalf("unexpected FIRING row: %+v", e.GetAlert())
		}
		if e.GetSourceNode() == "node-c" {
			placeholder = e
		}
	}
	if placeholder == nil || placeholder.GetFreshness() != freshness.STALE {
		t.Fatalf("want STALE placeholder, got %+v", resp.Msg.GetEntries())
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
