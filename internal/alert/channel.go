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
	return s.postJSON(ctx, cfg.WebhookURL, nil, body)
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
		"msgtype": "text",
		"text":    map[string]string{"content": textContent(rec)},
	})
	if err != nil {
		return err
	}
	return s.postJSON(ctx, target, nil, body)
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
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		return httpStatusError{code: resp.StatusCode}
	})
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

func (s *ChannelSender) http() HTTPDoer {
	if s != nil && s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
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
	return rec.Type + " " + rec.Severity + " " + rec.Fingerprint + " " + rec.State
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
	c, err := smtp.Dial(host)
	if err != nil {
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
