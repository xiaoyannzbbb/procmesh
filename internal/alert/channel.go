package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type EmailConfig struct {
	SMTPHost string   `json:"smtp_host"`
	SMTPPort int      `json:"smtp_port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	StartTLS bool     `json:"starttls"`
}

type smtpSendCloser interface {
	Mail(from string) error
	Rcpt(to string) error
	Data() (io.WriteCloser, error)
	Close() error
}

type ChannelSender struct {
	HTTP     HTTPDoer
	DialSMTP func(host string, cfg EmailConfig) (smtpSendCloser, error)
	Sleep    func(time.Duration)
	Attempts int
}

var _ Sender = (*ChannelSender)(nil)

type webhookConfig struct {
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	HMACSecret string            `json:"hmac_secret"`
}

type hookURLConfig struct {
	WebhookURL string `json:"webhook_url"`
	Secret     string `json:"secret"`
}

type webhookBody struct {
	AlertID     string          `json:"alert_id"`
	Fingerprint string          `json:"fingerprint"`
	Type        string          `json:"type"`
	Severity    string          `json:"severity"`
	State       string          `json:"state"`
	NodeID      string          `json:"node_id"`
	ProcessID   string          `json:"process_id"`
	FirstAt     string          `json:"first_at"`
	LastAt      string          `json:"last_at"`
	Payload     json.RawMessage `json:"payload"`
}

type httpStatusError struct {
	code int
}

func (e httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.code)
}

func (s *ChannelSender) Send(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	err := s.doSend(ctx, ch, rec)
	if ch.Enabled && ch.Type != "" && ch.Type != "WEB" {
		result := "ok"
		if err != nil {
			result = "error"
		}
		RecordSend(ch.Type, result)
	}
	return err
}

func (s *ChannelSender) doSend(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	if !ch.Enabled {
		return nil
	}
	switch ch.Type {
	case "WEB":
		return nil
	case "WEBHOOK":
		return s.sendWebhook(ctx, ch, rec)
	case "EMAIL":
		return s.sendEmail(ctx, ch, rec)
	case "WECOM":
		return s.sendWecom(ctx, ch, rec)
	case "DINGTALK":
		return s.sendDingTalk(ctx, ch, rec)
	case "SLACK":
		return s.sendSlack(ctx, ch, rec)
	default:
		return errcode.E(errcode.INVALID, "unknown channel type")
	}
}

func (s *ChannelSender) sendWebhook(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	var cfg webhookConfig
	if err := decodeConfig(ch.ConfigJSON, &cfg); err != nil {
		return err
	}
	if cfg.URL == "" {
		return errcode.E(errcode.INVALID, "webhook url")
	}
	body, err := marshalWebhook(rec)
	if err != nil {
		return err
	}
	headers := map[string]string{}
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	if cfg.HMACSecret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.HMACSecret))
		mac.Write(body)
		headers["X-ProcMesh-Signature"] = hex.EncodeToString(mac.Sum(nil))
	}
	return s.postJSON(ctx, cfg.URL, headers, body)
}

func (s *ChannelSender) sendWecom(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	var cfg hookURLConfig
	if err := decodeConfig(ch.ConfigJSON, &cfg); err != nil {
		return err
	}
	if cfg.WebhookURL == "" {
		return errcode.E(errcode.INVALID, "webhook_url")
	}
	body, err := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": textContent(rec)},
	})
	if err != nil {
		return err
	}
	return s.postJSONValidated(ctx, cfg.WebhookURL, nil, body, validateWecomResponse)
}

func (s *ChannelSender) sendSlack(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	var cfg hookURLConfig
	if err := decodeConfig(ch.ConfigJSON, &cfg); err != nil {
		return err
	}
	if cfg.WebhookURL == "" {
		return errcode.E(errcode.INVALID, "webhook_url")
	}
	body, err := json.Marshal(map[string]string{"text": textContent(rec)})
	if err != nil {
		return err
	}
	return s.postJSON(ctx, cfg.WebhookURL, nil, body)
}

func (s *ChannelSender) sendDingTalk(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	var cfg hookURLConfig
	if err := decodeConfig(ch.ConfigJSON, &cfg); err != nil {
		return err
	}
	if cfg.WebhookURL == "" {
		return errcode.E(errcode.INVALID, "webhook_url")
	}
	target := cfg.WebhookURL
	if cfg.Secret != "" {
		signed, err := signDingTalk(cfg.WebhookURL, cfg.Secret, time.Now())
		if err != nil {
			return err
		}
		target = signed
	}
	body, err := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": dingTalkMarkdown(rec),
	})
	if err != nil {
		return err
	}
	return s.postJSONValidated(ctx, target, nil, body, validateDingTalkResponse)
}

func (s *ChannelSender) sendEmail(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	var cfg EmailConfig
	if err := decodeConfig(ch.ConfigJSON, &cfg); err != nil {
		return err
	}
	if cfg.From == "" || len(cfg.To) == 0 {
		return errcode.E(errcode.INVALID, "email from/to")
	}
	body, err := marshalWebhook(rec)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("[%s] %s node=%s", rec.Severity, rec.Type, rec.NodeID)
	msg := formatRFC822(cfg.From, cfg.To, subject, body)
	host := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	return s.withRetry(ctx, func() error {
		dial := s.DialSMTP
		if dial == nil {
			dial = defaultDialSMTP
		}
		c, err := dial(host, cfg)
		if err != nil {
			return err
		}
		defer c.Close()
		if err := c.Mail(cfg.From); err != nil {
			return err
		}
		for _, to := range cfg.To {
			if err := c.Rcpt(to); err != nil {
				return err
			}
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write(msg); err != nil {
			_ = w.Close()
			return err
		}
		return w.Close()
	})
}

func (s *ChannelSender) postJSON(ctx context.Context, rawURL string, headers map[string]string, body []byte) error {
	return s.postJSONValidated(ctx, rawURL, headers, body, nil)
}

func (s *ChannelSender) postJSONValidated(ctx context.Context, rawURL string, headers map[string]string, body []byte, validate func([]byte) error) error {
	return s.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := s.http().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if validate != nil {
				return validate(responseBody)
			}
			return nil
		}
		return httpStatusError{code: resp.StatusCode}
	})
}

func validateDingTalkResponse(body []byte) error {
	return validateRobotResponse("DingTalk", body)
}

func validateWecomResponse(body []byte) error {
	return validateRobotResponse("WeCom", body)
}

func validateRobotResponse(provider string, body []byte) error {
	var response struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decode %s response: %w", provider, err)
	}
	if response.ErrCode == nil {
		return fmt.Errorf("decode %s response: missing errcode", provider)
	}
	if *response.ErrCode != 0 {
		return errcode.E(errcode.INVALID, fmt.Sprintf("%s error %d: %s", provider, *response.ErrCode, response.ErrMsg))
	}
	return nil
}

func (s *ChannelSender) withRetry(ctx context.Context, fn func() error) error {
	n := s.Attempts
	if n <= 0 {
		n = 3
	}
	var last error
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = fn()
		if last == nil {
			return nil
		}
		if !retryable(last) {
			return last
		}
		if i+1 < n {
			s.sleep(retryBackoff(i))
		}
	}
	return last
}

const defaultSendTimeout = 10 * time.Second

var defaultHTTPClient = &http.Client{Timeout: defaultSendTimeout}

func (s *ChannelSender) http() HTTPDoer {
	if s != nil && s.HTTP != nil {
		return s.HTTP
	}
	return defaultHTTPClient
}

func (s *ChannelSender) sleep(d time.Duration) {
	if s != nil && s.Sleep != nil {
		s.Sleep(d)
		return
	}
	time.Sleep(d)
}

func retryBackoff(failedAttempt int) time.Duration {
	if failedAttempt <= 0 {
		return 50 * time.Millisecond
	}
	return 100 * time.Millisecond
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errcode.Is(err, errcode.INVALID) {
		return false
	}
	var hs httpStatusError
	if errors.As(err, &hs) {
		return hs.code >= 500
	}
	return true
}

// MapDeliveryError converts transport failures into stable API-facing codes
// while preserving provider and configuration errors already classified by a sender.
func MapDeliveryError(err error) error {
	if err == nil {
		return nil
	}
	var coded *errcode.Error
	if errors.As(err, &coded) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &errcode.Error{Code: errcode.TIMEOUT, Msg: err.Error(), Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &errcode.Error{Code: errcode.TIMEOUT, Msg: err.Error(), Err: err}
	}
	return &errcode.Error{Code: errcode.UNAVAILABLE, Msg: err.Error(), Err: err}
}

func decodeConfig(raw string, dest any) error {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		return errcode.E(errcode.INVALID, "config_json")
	}
	return nil
}

func marshalWebhook(rec store.AlertRecord) ([]byte, error) {
	payload := json.RawMessage(rec.PayloadJSON)
	if len(bytes.TrimSpace(payload)) == 0 || !json.Valid(payload) {
		payload = json.RawMessage("{}")
	}
	return json.Marshal(webhookBody{
		AlertID:     rec.AlertID,
		Fingerprint: rec.Fingerprint,
		Type:        rec.Type,
		Severity:    rec.Severity,
		State:       rec.State,
		NodeID:      rec.NodeID,
		ProcessID:   rec.ProcessID,
		FirstAt:     formatAlertTime(rec.FirstAt),
		LastAt:      formatAlertTime(rec.LastAt),
		Payload:     payload,
	})
}

func formatAlertTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func textContent(rec store.AlertRecord) string {
	if rec.Type == "CHANNEL_TEST" {
		return "[ProcMesh] Notification channel test\nChannel: " + strings.TrimPrefix(rec.Fingerprint, "channel-test:")
	}
	return rec.Type + " " + rec.Severity + " " + rec.Fingerprint + " " + rec.State
}

type dingTalkMarkdownBody struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type thresholdPayload struct {
	Hostname            string   `json:"hostname"`
	ProcessName         string   `json:"process_name"`
	CurrentValuePercent *float64 `json:"current_value_percent"`
	ThresholdPercent    *float64 `json:"threshold_percent"`
	ConsecutiveMinutes  *int     `json:"consecutive_minutes"`
}

func dingTalkMarkdown(rec store.AlertRecord) dingTalkMarkdownBody {
	name := alertDisplayName(rec.Type)
	lines := []string{
		"### ProcMesh " + alertStateLabel(rec.State),
		"",
		"> **" + name + "** · " + rec.Severity + " · " + alertStateLabel(rec.State),
		"",
		"- 告警类型: `" + rec.Type + "`",
	}
	var payload thresholdPayload
	payloadDecoded := json.Unmarshal([]byte(rec.PayloadJSON), &payload) == nil
	if rec.NodeID != "" {
		lines = append(lines, dingTalkNamedID("节点", payload.Hostname, rec.NodeID))
	}
	if rec.ProcessID != "" {
		lines = append(lines, dingTalkNamedID("进程", payload.ProcessName, rec.ProcessID))
	}

	if payloadDecoded {
		if payload.CurrentValuePercent != nil {
			lines = append(lines, "- 当前值: **"+formatPercent(*payload.CurrentValuePercent)+"**")
		}
		if payload.ThresholdPercent != nil {
			lines = append(lines, "- 阈值: **"+formatPercent(*payload.ThresholdPercent)+"**")
		}
		if payload.ConsecutiveMinutes != nil && *payload.ConsecutiveMinutes > 0 {
			lines = append(lines, fmt.Sprintf("- 持续条件: 连续 %d 分钟", *payload.ConsecutiveMinutes))
		}
	}
	if !rec.FirstAt.IsZero() {
		lines = append(lines, "- 首次发生: "+formatDingTalkTime(rec.FirstAt))
	}
	if !rec.LastAt.IsZero() {
		lines = append(lines, "- 最近发生: "+formatDingTalkTime(rec.LastAt))
	}
	if rec.Fingerprint != "" {
		lines = append(lines, "- 指纹: `"+rec.Fingerprint+"`")
	}

	return dingTalkMarkdownBody{
		Title: fmt.Sprintf("[%s] %s", rec.Severity, name),
		Text:  strings.Join(lines, "\n"),
	}
}

func alertDisplayName(typ string) string {
	if name, ok := map[string]string{
		"PROCESS_EXIT":           "进程异常退出",
		"PROCESS_FATAL":          "进程启动失败",
		"PROCESS_CRASH_LOOP":     "进程反复崩溃",
		"HEALTH_FAILED":          "进程健康检查失败",
		"CPU_HIGH":               "CPU 使用率过高",
		"MEMORY_HIGH":            "内存使用率过高",
		"DISK_HIGH":              "磁盘使用率过高",
		"LOCAL_DB_ERROR":         "本地数据库异常",
		"AGENT_FAILED":           "节点不可用",
		"AGENT_SUSPECT_TOO_LONG": "节点疑似失联",
		"CONTROL_NO_QUORUM":      "控制面失去法定人数",
		"CERT_EXPIRING":          "证书即将过期",
		"VERSION_MISMATCH":       "版本不兼容",
		"CHANNEL_TEST":           "通知通道测试",
	}[typ]; ok {
		return name
	}
	return typ
}

func alertStateLabel(state string) string {
	if state == string(StateResolved) {
		return "已恢复"
	}
	if state == "TEST" {
		return "通知测试"
	}
	return "告警"
}

func dingTalkNamedID(label, name, id string) string {
	if name != "" {
		return "- " + label + ": **" + name + "** (`" + id + "`)"
	}
	return "- " + label + ": `" + id + "`"
}

func formatPercent(value float64) string {
	formatted := strings.TrimRight(strconv.FormatFloat(value, 'f', 2, 64), "0")
	formatted = strings.TrimRight(formatted, ".")
	return formatted + "%"
}

func formatDingTalkTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func signDingTalk(rawURL, secret string, now time.Time) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errcode.E(errcode.INVALID, "webhook_url")
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func formatRFC822(from string, to []string, subject string, body []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=UTF-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.Write(body)
	return b.Bytes()
}

func defaultDialSMTP(host string, cfg EmailConfig) (smtpSendCloser, error) {
	d := net.Dialer{Timeout: defaultSendTimeout}
	conn, err := d.Dial("tcp", host)
	if err != nil {
		return nil, err
	}
	name, _, splitErr := net.SplitHostPort(host)
	if splitErr != nil || name == "" {
		name = cfg.SMTPHost
	}
	c, err := smtp.NewClient(conn, name)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if cfg.StartTLS {
		tlsCfg := &tls.Config{ServerName: cfg.SMTPHost}
		if err := c.StartTLS(tlsCfg); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.SMTPHost)
		if err := c.Auth(auth); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return c, nil
}
