package alert

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

func sampleAlert() store.AlertRecord {
	ts := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	return store.AlertRecord{
		AlertID:     "a1",
		Fingerprint: "PROCESS_EXIT:p1",
		Type:        "PROCESS_EXIT",
		Severity:    "WARNING",
		State:       "FIRING",
		NodeID:      "n1",
		ProcessID:   "p1",
		PayloadJSON: `{}`,
		FirstAt:     ts,
		LastAt:      ts,
	}
}

type countDoer struct {
	inner HTTPDoer
	n     int
}

func (c *countDoer) Do(req *http.Request) (*http.Response, error) {
	c.n++
	return c.inner.Do(req)
}

func TestChannel_DefaultHTTPTimeout(t *testing.T) {
	s := &ChannelSender{}
	c, ok := s.http().(*http.Client)
	if !ok {
		t.Fatalf("http() %T", s.http())
	}
	if c.Timeout != 10*time.Second {
		t.Fatalf("timeout %s want 10s", c.Timeout)
	}
	if c == http.DefaultClient {
		t.Fatal("must not use http.DefaultClient")
	}
}

func TestChannel_WebhookHMAC(t *testing.T) {
	const secret = "s3cret"
	var gotBody []byte
	var gotSig, gotCT, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotSig = r.Header.Get("X-ProcMesh-Signature")
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	s := &ChannelSender{HTTP: srv.Client(), Sleep: func(time.Duration) {}}
	ch := control.AlertChannel{
		ChannelID:  "c1",
		Type:       "WEBHOOK",
		Enabled:    true,
		ConfigJSON: `{"url":"` + srv.URL + `","hmac_secret":"` + secret + `"}`,
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method %s", gotMethod)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Fatalf("content-type %s", gotCT)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	wantSig := hex.EncodeToString(mac.Sum(nil))
	if gotSig != wantSig {
		t.Fatalf("sig %s want %s", gotSig, wantSig)
	}
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"alert_id":    "a1",
		"fingerprint": "PROCESS_EXIT:p1",
		"type":        "PROCESS_EXIT",
		"severity":    "WARNING",
		"state":       "FIRING",
		"node_id":     "n1",
		"process_id":  "p1",
		"first_at":    "2026-08-16T00:00:00Z",
		"last_at":     "2026-08-16T00:00:00Z",
	} {
		if got, _ := payload[k].(string); got != want {
			t.Fatalf("%s=%v want %s", k, payload[k], want)
		}
	}
	raw, _ := json.Marshal(payload["payload"])
	if string(raw) != "{}" {
		t.Fatalf("payload %s", raw)
	}
}

func TestChannel_WebhookNoHMAC(t *testing.T) {
	var gotSig string
	var had bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, had = r.Header[http.CanonicalHeaderKey("X-ProcMesh-Signature")]
		gotSig = r.Header.Get("X-ProcMesh-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	s := &ChannelSender{HTTP: srv.Client(), Sleep: func(time.Duration) {}}
	ch := control.AlertChannel{
		Type: "WEBHOOK", Enabled: true,
		ConfigJSON: `{"url":"` + srv.URL + `"}`,
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err != nil {
		t.Fatal(err)
	}
	if had || gotSig != "" {
		t.Fatalf("signature header present: had=%v val=%q", had, gotSig)
	}
}

func TestChannel_WecomAndSlack(t *testing.T) {
	var wecom, slack []byte
	wecomSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wecom, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(wecomSrv.Close)
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slack, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slackSrv.Close)

	s := &ChannelSender{HTTP: http.DefaultClient, Sleep: func(time.Duration) {}}
	rec := sampleAlert()
	if err := s.Send(context.Background(), control.AlertChannel{
		Type: "WECOM", Enabled: true, ConfigJSON: `{"webhook_url":"` + wecomSrv.URL + `"}`,
	}, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), control.AlertChannel{
		Type: "SLACK", Enabled: true, ConfigJSON: `{"webhook_url":"` + slackSrv.URL + `"}`,
	}, rec); err != nil {
		t.Fatal(err)
	}

	var wbody struct {
		MsgType string `json:"msgtype"`
		Text    struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	if err := json.Unmarshal(wecom, &wbody); err != nil {
		t.Fatal(err)
	}
	if wbody.MsgType != "text" || wbody.Text.Content != "PROCESS_EXIT WARNING PROCESS_EXIT:p1 FIRING" {
		t.Fatalf("wecom %+v body=%s", wbody, wecom)
	}

	var sbody struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(slack, &sbody); err != nil {
		t.Fatal(err)
	}
	if sbody.Text != "PROCESS_EXIT WARNING PROCESS_EXIT:p1 FIRING" {
		t.Fatalf("slack %s", slack)
	}
}

func TestChannel_WecomHTTP200BusinessError(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid webhook url"}`))
	}))
	t.Cleanup(srv.Close)

	s := &ChannelSender{HTTP: srv.Client(), Sleep: func(time.Duration) {}}
	err := s.Send(context.Background(), control.AlertChannel{
		Type: "WECOM", Enabled: true,
		ConfigJSON: `{"webhook_url":"` + srv.URL + `"}`,
	}, sampleAlert())
	if err == nil || !strings.Contains(err.Error(), "invalid webhook url") {
		t.Fatalf("got %v, want WeCom business error", err)
	}
	if hits != 1 {
		t.Fatalf("hits=%d want 1 for non-retryable business error", hits)
	}
}

func TestChannel_DingTalkSign(t *testing.T) {
	const secret = "SECabc"
	var gotURL string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	s := &ChannelSender{HTTP: srv.Client(), Sleep: func(time.Duration) {}}
	ch := control.AlertChannel{
		Type: "DINGTALK", Enabled: true,
		ConfigJSON: `{"webhook_url":"` + srv.URL + `/robot/send?access_token=tok","secret":"` + secret + `"}`,
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err != nil {
		t.Fatal(err)
	}
	reqURL, err := http.NewRequest(http.MethodGet, "http://x"+gotURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := reqURL.URL.Query()
	ts := q.Get("timestamp")
	sign := q.Get("sign")
	if ts == "" || sign == "" {
		t.Fatalf("missing timestamp/sign in %s", gotURL)
	}
	if q.Get("access_token") != "tok" {
		t.Fatalf("lost access_token in %s", gotURL)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if sign != want {
		t.Fatalf("sign=%s want=%s url=%s", sign, want, gotURL)
	}
	var body struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.MsgType != "markdown" || body.Markdown.Title != "[WARNING] 进程异常退出" || !strings.Contains(body.Markdown.Text, "进程: `p1`") {
		t.Fatalf("dingtalk body %s", gotBody)
	}
}

func TestChannel_DingTalkDiskHighMarkdown(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	rec := sampleAlert()
	rec.Type = "DISK_HIGH"
	rec.Fingerprint = "DISK_HIGH:a0ba0978-70ed-4664-8d80-133c6c862f86"
	rec.NodeID = "a0ba0978-70ed-4664-8d80-133c6c862f86"
	rec.PayloadJSON = `{"current_value_percent":91.4,"threshold_percent":85,"consecutive_minutes":3}`

	s := &ChannelSender{HTTP: srv.Client(), Sleep: func(time.Duration) {}}
	if err := s.Send(context.Background(), control.AlertChannel{
		Type: "DINGTALK", Enabled: true, ConfigJSON: `{"webhook_url":"` + srv.URL + `"}`,
	}, rec); err != nil {
		t.Fatal(err)
	}

	var body struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.MsgType != "markdown" {
		t.Fatalf("msgtype=%q want markdown; body=%s", body.MsgType, gotBody)
	}
	if body.Markdown.Title != "[WARNING] 磁盘使用率过高" {
		t.Fatalf("title=%q", body.Markdown.Title)
	}
	for _, want := range []string{
		"节点: `a0ba0978-70ed-4664-8d80-133c6c862f86`",
		"当前值: **91.4%**",
		"阈值: **85%**",
		"首次发生: 2026-08-16 00:00:00 UTC",
		"最近发生: 2026-08-16 00:00:00 UTC",
	} {
		if !strings.Contains(body.Markdown.Text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, body.Markdown.Text)
		}
	}
}

func TestChannel_DingTalkHTTP200BusinessError(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":310000,"errmsg":"keywords not in content"}`))
	}))
	t.Cleanup(srv.Close)

	s := &ChannelSender{HTTP: srv.Client(), Sleep: func(time.Duration) {}}
	err := s.Send(context.Background(), control.AlertChannel{
		Type: "DINGTALK", Enabled: true,
		ConfigJSON: `{"webhook_url":"` + srv.URL + `"}`,
	}, sampleAlert())
	if err == nil || !strings.Contains(err.Error(), "keywords not in content") {
		t.Fatalf("got %v, want DingTalk business error", err)
	}
	if hits != 1 {
		t.Fatalf("hits=%d want 1 for non-retryable business error", hits)
	}
}

func TestChannel_RobotResponseRequiresErrCode(t *testing.T) {
	cases := []string{
		`{}`,
		`null`,
		`{"errmsg":"ok"}`,
		`{"errcode":null,"errmsg":"ok"}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			if err := validateDingTalkResponse([]byte(body)); err == nil {
				t.Fatalf("validateDingTalkResponse(%s) succeeded without errcode", body)
			}
		})
	}
}

func TestChannel_TestMessageTextIsHumanReadable(t *testing.T) {
	rec := sampleAlert()
	rec.Type = "CHANNEL_TEST"
	rec.Fingerprint = "channel-test:ding-1"
	rec.State = "TEST"

	got := textContent(rec)
	if !strings.Contains(got, "[ProcMesh] Notification channel test") || !strings.Contains(got, "ding-1") {
		t.Fatalf("test message %q", got)
	}
}

type recordSMTP struct {
	from string
	to   []string
	msg  []byte
}

func (r *recordSMTP) Mail(from string) error {
	r.from = from
	return nil
}

func (r *recordSMTP) Rcpt(to string) error {
	r.to = append(r.to, to)
	return nil
}

func (r *recordSMTP) Data() (io.WriteCloser, error) {
	return &smtpCapture{rec: r}, nil
}

func (r *recordSMTP) Close() error { return nil }

type smtpCapture struct {
	rec *recordSMTP
	buf bytes.Buffer
}

func (c *smtpCapture) Write(p []byte) (int, error) { return c.buf.Write(p) }

func (c *smtpCapture) Close() error {
	c.rec.msg = append([]byte(nil), c.buf.Bytes()...)
	return nil
}

func TestChannel_EmailDialSMTP(t *testing.T) {
	recSMTP := &recordSMTP{}
	var gotHost string
	var gotCfg EmailConfig
	s := &ChannelSender{
		Sleep: func(time.Duration) {},
		DialSMTP: func(host string, cfg EmailConfig) (smtpSendCloser, error) {
			gotHost = host
			gotCfg = cfg
			return recSMTP, nil
		},
	}
	ch := control.AlertChannel{
		Type: "EMAIL", Enabled: true,
		ConfigJSON: `{"smtp_host":"127.0.0.1","smtp_port":2525,"from":"a@b","to":["c@d","e@f"],"starttls":false}`,
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err != nil {
		t.Fatal(err)
	}
	if gotHost != "127.0.0.1:2525" {
		t.Fatalf("host %s", gotHost)
	}
	if gotCfg.From != "a@b" || gotCfg.StartTLS || gotCfg.SMTPPort != 2525 {
		t.Fatalf("cfg %+v", gotCfg)
	}
	if recSMTP.from != "a@b" {
		t.Fatalf("from %s", recSMTP.from)
	}
	if len(recSMTP.to) != 2 || recSMTP.to[0] != "c@d" || recSMTP.to[1] != "e@f" {
		t.Fatalf("to %v", recSMTP.to)
	}
	wantSubj := "[WARNING] PROCESS_EXIT node=n1"
	if !strings.Contains(string(recSMTP.msg), "Subject: "+wantSubj) {
		t.Fatalf("subject missing in %q", recSMTP.msg)
	}
}

func TestChannel_Retry500Then200(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var slept []time.Duration
	doer := &countDoer{inner: srv.Client()}
	s := &ChannelSender{
		HTTP:  doer,
		Sleep: func(d time.Duration) { slept = append(slept, d) },
	}
	ch := control.AlertChannel{
		Type: "WEBHOOK", Enabled: true,
		ConfigJSON: `{"url":"` + srv.URL + `"}`,
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err != nil {
		t.Fatal(err)
	}
	if doer.n != 3 || hits != 3 {
		t.Fatalf("Do=%d hits=%d want 3", doer.n, hits)
	}
	if len(slept) != 2 || slept[0] != 50*time.Millisecond || slept[1] != 100*time.Millisecond {
		t.Fatalf("sleeps %v", slept)
	}
}

func TestChannel_RetryAllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	doer := &countDoer{inner: srv.Client()}
	s := &ChannelSender{HTTP: doer, Sleep: func(time.Duration) {}}
	ch := control.AlertChannel{
		Type: "WEBHOOK", Enabled: true,
		ConfigJSON: `{"url":"` + srv.URL + `"}`,
	}
	err := s.Send(context.Background(), ch, sampleAlert())
	if err == nil {
		t.Fatal("want error")
	}
	if doer.n != 3 {
		t.Fatalf("Do=%d want 3", doer.n)
	}
}

func TestChannel_WEBNoIO(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	t.Cleanup(srv.Close)
	s := &ChannelSender{HTTP: srv.Client()}
	if err := s.Send(context.Background(), control.AlertChannel{
		Type: "WEB", Enabled: true, ConfigJSON: `{"url":"` + srv.URL + `"}`,
	}, sampleAlert()); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("WEB must not perform I/O, hits=%d", hits)
	}
}

func TestChannel_UnknownType(t *testing.T) {
	s := &ChannelSender{}
	err := s.Send(context.Background(), control.AlertChannel{Type: "SMS", Enabled: true}, sampleAlert())
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestChannel_Disabled(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	t.Cleanup(srv.Close)
	s := &ChannelSender{HTTP: srv.Client()}
	if err := s.Send(context.Background(), control.AlertChannel{
		Type: "WEBHOOK", Enabled: false, ConfigJSON: `{"url":"` + srv.URL + `"}`,
	}, sampleAlert()); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("disabled must not send, hits=%d", hits)
	}
}

func TestChannel_HTTP4xxNoRetry(t *testing.T) {
	doer := &countDoer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	doer.inner = srv.Client()
	s := &ChannelSender{HTTP: doer, Sleep: func(time.Duration) {}}
	err := s.Send(context.Background(), control.AlertChannel{
		Type: "WEBHOOK", Enabled: true, ConfigJSON: `{"url":"` + srv.URL + `"}`,
	}, sampleAlert())
	if err == nil {
		t.Fatal("want error")
	}
	if doer.n != 1 {
		t.Fatalf("Do=%d want 1 (no retry on 4xx)", doer.n)
	}
}

func TestChannel_InvalidConfigAndMissingFields(t *testing.T) {
	s := &ChannelSender{Sleep: func(time.Duration) {}}
	rec := sampleAlert()
	cases := []control.AlertChannel{
		{Type: "WEBHOOK", Enabled: true, ConfigJSON: `{`},
		{Type: "WEBHOOK", Enabled: true, ConfigJSON: `{}`},
		{Type: "WECOM", Enabled: true, ConfigJSON: `{}`},
		{Type: "SLACK", Enabled: true, ConfigJSON: ``},
		{Type: "DINGTALK", Enabled: true, ConfigJSON: `{"webhook_url":""}`},
		{Type: "EMAIL", Enabled: true, ConfigJSON: `{"from":"","to":[]}`},
		{Type: "DINGTALK", Enabled: true, ConfigJSON: `{"webhook_url":"://bad","secret":"s"}`},
	}
	for i, ch := range cases {
		if err := s.Send(context.Background(), ch, rec); !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("case %d: got %v", i, err)
		}
	}
}

func TestChannel_WebhookHeadersAndBadPayload(t *testing.T) {
	var gotCustom, firstAt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-Custom")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		firstAt, _ = body["first_at"].(string)
		raw, _ := json.Marshal(body["payload"])
		if string(raw) != "{}" {
			t.Errorf("payload %s", raw)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rec := sampleAlert()
	rec.PayloadJSON = `not-json`
	rec.FirstAt = time.Time{}
	s := &ChannelSender{Sleep: func(time.Duration) {}} // default HTTP
	ch := control.AlertChannel{
		Type: "WEBHOOK", Enabled: true,
		ConfigJSON: `{"url":"` + srv.URL + `","headers":{"X-Custom":"v"}}`,
	}
	if err := s.Send(context.Background(), ch, rec); err != nil {
		t.Fatal(err)
	}
	if gotCustom != "v" {
		t.Fatalf("custom header %q", gotCustom)
	}
	if firstAt != "" {
		t.Fatalf("zero first_at %q", firstAt)
	}
}

func TestChannel_DingTalkNoSecret(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	s := &ChannelSender{HTTP: srv.Client(), Sleep: func(time.Duration) {}}
	if err := s.Send(context.Background(), control.AlertChannel{
		Type: "DINGTALK", Enabled: true,
		ConfigJSON: `{"webhook_url":"` + srv.URL + `/robot/send?access_token=tok"}`,
	}, sampleAlert()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawQuery, "timestamp=") || strings.Contains(rawQuery, "sign=") {
		t.Fatalf("unexpected sign query %q", rawQuery)
	}
}

func TestChannel_NetworkErrorRetries(t *testing.T) {
	doer := &failDoer{}
	s := &ChannelSender{HTTP: doer, Sleep: func(time.Duration) {}}
	err := s.Send(context.Background(), control.AlertChannel{
		Type: "WEBHOOK", Enabled: true, ConfigJSON: `{"url":"http://example.invalid"}`,
	}, sampleAlert())
	if err == nil {
		t.Fatal("want error")
	}
	if doer.n != 3 {
		t.Fatalf("Do=%d want 3", doer.n)
	}
	if err.Error() == "" {
		t.Fatal("empty error")
	}
}

type failDoer struct{ n int }

func (f *failDoer) Do(*http.Request) (*http.Response, error) {
	f.n++
	return nil, errors.New("net down")
}

func TestChannel_ContextCanceled(t *testing.T) {
	s := &ChannelSender{Sleep: func(time.Duration) {}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Send(ctx, control.AlertChannel{
		Type: "WEBHOOK", Enabled: true, ConfigJSON: `{"url":"http://127.0.0.1"}`,
	}, sampleAlert())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestChannel_EmailSMTPErrorsRetry(t *testing.T) {
	s := &ChannelSender{
		Sleep: func(time.Duration) {},
		DialSMTP: func(host string, cfg EmailConfig) (smtpSendCloser, error) {
			return nil, errors.New("dial")
		},
	}
	err := s.Send(context.Background(), control.AlertChannel{
		Type: "EMAIL", Enabled: true,
		ConfigJSON: `{"smtp_host":"127.0.0.1","smtp_port":2525,"from":"a@b","to":["c@d"]}`,
	}, sampleAlert())
	if err == nil || err.Error() != "dial" {
		t.Fatalf("got %v", err)
	}
}

func TestChannel_EmailMailRcptDataErrors(t *testing.T) {
	s := &ChannelSender{Sleep: func(time.Duration) {}}
	ch := control.AlertChannel{
		Type: "EMAIL", Enabled: true,
		ConfigJSON: `{"smtp_host":"127.0.0.1","smtp_port":2525,"from":"a@b","to":["c@d"]}`,
	}
	s.DialSMTP = func(string, EmailConfig) (smtpSendCloser, error) {
		return &errSMTP{mail: errors.New("mail")}, nil
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err == nil || err.Error() != "mail" {
		t.Fatalf("mail: %v", err)
	}
	s.DialSMTP = func(string, EmailConfig) (smtpSendCloser, error) {
		return &errSMTP{rcpt: errors.New("rcpt")}, nil
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err == nil || err.Error() != "rcpt" {
		t.Fatalf("rcpt: %v", err)
	}
	s.DialSMTP = func(string, EmailConfig) (smtpSendCloser, error) {
		return &errSMTP{data: errors.New("data")}, nil
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err == nil || err.Error() != "data" {
		t.Fatalf("data: %v", err)
	}
	s.DialSMTP = func(string, EmailConfig) (smtpSendCloser, error) {
		return &errSMTP{write: errors.New("write")}, nil
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err == nil || err.Error() != "write" {
		t.Fatalf("write: %v", err)
	}
}

type errSMTP struct {
	mail, rcpt, data, write error
}

func (e *errSMTP) Mail(string) error { return e.mail }
func (e *errSMTP) Rcpt(string) error { return e.rcpt }
func (e *errSMTP) Data() (io.WriteCloser, error) {
	if e.data != nil {
		return nil, e.data
	}
	return &errWriter{err: e.write}, nil
}
func (e *errSMTP) Close() error { return nil }

type errWriter struct{ err error }

func (w *errWriter) Write([]byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	return 0, nil
}
func (w *errWriter) Close() error { return nil }

func TestChannel_DefaultDialSMTP(t *testing.T) {
	addr := startFakeSMTP(t)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	s := &ChannelSender{Sleep: func(time.Duration) {}}
	ch := control.AlertChannel{
		Type: "EMAIL", Enabled: true,
		ConfigJSON: fmt.Sprintf(`{"smtp_host":%q,"smtp_port":%s,"from":"a@b","to":["c@d"],"starttls":false}`, host, port),
	}
	if err := s.Send(context.Background(), ch, sampleAlert()); err != nil {
		t.Fatal(err)
	}
}

func TestChannel_DefaultDialSMTPStartTLSAndAuthFail(t *testing.T) {
	addr := startFakeSMTP(t)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	s := &ChannelSender{Sleep: func(time.Duration) {}, Attempts: 1}
	tlsCh := control.AlertChannel{
		Type: "EMAIL", Enabled: true,
		ConfigJSON: fmt.Sprintf(`{"smtp_host":%q,"smtp_port":%s,"from":"a@b","to":["c@d"],"starttls":true}`, host, port),
	}
	if err := s.Send(context.Background(), tlsCh, sampleAlert()); err == nil {
		t.Fatal("want starttls error")
	}
	addr2 := startFakeSMTP(t)
	host2, port2, err := net.SplitHostPort(addr2)
	if err != nil {
		t.Fatal(err)
	}
	authCh := control.AlertChannel{
		Type: "EMAIL", Enabled: true,
		ConfigJSON: fmt.Sprintf(`{"smtp_host":%q,"smtp_port":%s,"username":"u","password":"p","from":"a@b","to":["c@d"]}`, host2, port2),
	}
	if err := s.Send(context.Background(), authCh, sampleAlert()); err == nil {
		t.Fatal("want auth error")
	}
}

func startFakeSMTP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleFakeSMTP(c)
		}
	}()
	return ln.Addr().String()
}

func handleFakeSMTP(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	fmt.Fprintf(c, "220 localhost ESMTP\r\n")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			fmt.Fprintf(c, "250-localhost\r\n250 OK\r\n")
		case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
			fmt.Fprintf(c, "250 OK\r\n")
		case strings.HasPrefix(line, "DATA"):
			fmt.Fprintf(c, "354 End data with <CR><LF>.<CR><LF>\r\n")
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
			}
			fmt.Fprintf(c, "250 OK\r\n")
		case strings.HasPrefix(line, "QUIT"):
			fmt.Fprintf(c, "221 bye\r\n")
			return
		default:
			fmt.Fprintf(c, "502 unused\r\n")
		}
	}
}
