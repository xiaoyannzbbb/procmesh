package rpc

import (
	"net/http"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

// DialConfig configures an mTLS HTTP client for Agent-to-Agent RPC.
type DialConfig struct {
	Creds        control.AgentCreds
	ClusterID    string
	ExpectNodeID string
	Address      string
	Timeout      time.Duration
}

// Dial builds an mTLS HTTP client and https base URL for the given address.
// Address may be host:port or already https://...; base URL is always https://host:port.
// Timeout defaults to 5s when zero.
func Dial(cfg DialConfig) (*http.Client, string, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	tlsCfg, err := ClientTLS(cfg.Creds, cfg.ClusterID, cfg.ExpectNodeID)
	if err != nil {
		return nil, "", err
	}
	hc := &http.Client{
		Timeout:   timeout,
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

func baseURL(addr string) string {
	if strings.HasPrefix(addr, "https://") {
		return addr
	}
	if strings.HasPrefix(addr, "http://") {
		return "https://" + strings.TrimPrefix(addr, "http://")
	}
	return "https://" + addr
}
