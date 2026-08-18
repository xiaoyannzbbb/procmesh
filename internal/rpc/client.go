package rpc

import (
	"net/http"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

const (
	// UnaryTimeout is the client bound for list/get hops.
	UnaryTimeout = 5 * time.Second
	// MutationTimeout covers stop_timeout (default 10s) plus margin.
	MutationTimeout = 30 * time.Second
)

// DialConfig configures an mTLS HTTP client for Agent-to-Agent RPC.
type DialConfig struct {
	Creds        control.AgentCreds
	ClusterID    string
	ExpectNodeID string
	Address      string
	Timeout      time.Duration
	Revoked      func(serial string) bool
}

// Dial builds an mTLS HTTP client and https base URL for the given address.
// Address may be host:port or already https://...; base URL is always https://host:port.
// Timeout 0 means no client-wide timeout (context cancellation still applies).
// Unary hops should set UnaryTimeout or MutationTimeout; Stream/Download must use 0.
func Dial(cfg DialConfig) (*http.Client, string, error) {
	tlsCfg, err := ClientTLS(cfg.Creds, cfg.ClusterID, cfg.ExpectNodeID, cfg.Revoked)
	if err != nil {
		return nil, "", err
	}
	hc := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	return hc, baseURL(cfg.Address), nil
}

// NewProcessClient returns a ProcessService client over the given HTTP client and base URL.
func NewProcessClient(hc *http.Client, base string) procmeshv1connect.ProcessServiceClient {
	return procmeshv1connect.NewProcessServiceClient(hc, base)
}

// NewConfigClient returns a ConfigService client over the given HTTP client and base URL.
func NewConfigClient(hc *http.Client, base string) procmeshv1connect.ConfigServiceClient {
	return procmeshv1connect.NewConfigServiceClient(hc, base)
}

// NewLogClient returns a LogService client over the given HTTP client and base URL.
func NewLogClient(hc *http.Client, base string) procmeshv1connect.LogServiceClient {
	return procmeshv1connect.NewLogServiceClient(hc, base)
}

// NewAuditClient returns an AuditService client over the given HTTP client and base URL.
func NewAuditClient(hc *http.Client, base string) procmeshv1connect.AuditServiceClient {
	return procmeshv1connect.NewAuditServiceClient(hc, base)
}

// NewMetricsClient returns a MetricsService client over the given HTTP client and base URL.
func NewMetricsClient(hc *http.Client, base string) procmeshv1connect.MetricsServiceClient {
	return procmeshv1connect.NewMetricsServiceClient(hc, base)
}

// NewAlertClient returns an AlertService client over the given HTTP client and base URL.
func NewAlertClient(hc *http.Client, base string) procmeshv1connect.AlertServiceClient {
	return procmeshv1connect.NewAlertServiceClient(hc, base)
}

// NewBackupClient returns a BackupService client over the given HTTP client and base URL.
func NewBackupClient(hc *http.Client, base string) procmeshv1connect.BackupServiceClient {
	return procmeshv1connect.NewBackupServiceClient(hc, base)
}

func baseURL(addr string) string {
	if strings.HasPrefix(addr, "https://") {
		return addr
	}
	if strings.HasPrefix(addr, "http://") {
		return "https://" + strings.TrimPrefix(addr, "http://")
	}
	return "https://" + addr
}
