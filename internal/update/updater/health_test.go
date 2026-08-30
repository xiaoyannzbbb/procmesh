package updater_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/update/updater"
)

type pathReader struct {
	path string
	err  error
}

func (r pathReader) RunningAgentPath(context.Context) (string, error) { return r.path, r.err }

func TestHTTPHealthRequiresReadyEndpointsAndTargetExecutable(t *testing.T) {
	directory := t.TempDir()
	agentPath := filepath.Join(directory, "procmesh-agent")
	if err := os.WriteFile(agentPath, []byte("agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz", "/readyz":
			writer.WriteHeader(http.StatusOK)
		case "/updatez":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"version":"v1.2.1","store_ready":true,"shim_recovery_complete":true}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	checker := updater.HTTPHealth{
		Address: parsed.Host, Timeout: 10 * time.Second, PollInterval: time.Millisecond,
		Client: server.Client(), Agent: pathReader{path: agentPath},
	}
	if err := checker.Check(context.Background(), updater.HealthExpectation{Version: "v1.2.1", AgentPath: agentPath}); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestHTTPHealthRejectsWrongVersionOrIncompleteShimRecovery(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "wrong version", body: `{"version":"v1.2.0","store_ready":true,"shim_recovery_complete":true}`},
		{name: "store unavailable", body: `{"version":"v1.2.1","store_ready":false,"shim_recovery_complete":true}`},
		{name: "shim recovery incomplete", body: `{"version":"v1.2.1","store_ready":true,"shim_recovery_complete":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			agentPath := filepath.Join(directory, "procmesh-agent")
			if err := os.WriteFile(agentPath, []byte("agent"), 0o755); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/updatez" {
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(tt.body))
					return
				}
				writer.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			parsed, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			checker := updater.HTTPHealth{
				Address: parsed.Host, Timeout: 10 * time.Second, PollInterval: time.Millisecond,
				Client: server.Client(), Agent: pathReader{path: agentPath},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := checker.Check(ctx, updater.HealthExpectation{Version: "v1.2.1", AgentPath: agentPath}); err == nil {
				t.Fatal("Check() error = nil")
			}
		})
	}
}

func TestHTTPHealthRejectsNonLoopbackAddress(t *testing.T) {
	checker := updater.HTTPHealth{Address: "192.0.2.1:18680", Timeout: 10 * time.Second, Agent: pathReader{}}
	if err := checker.Check(context.Background(), updater.HealthExpectation{}); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestHTTPHealthRejectsWrongRunningExecutable(t *testing.T) {
	checker := updater.HTTPHealth{
		Address: "127.0.0.1:18680", Timeout: 10 * time.Second, PollInterval: time.Millisecond,
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("should not reach network while executable is irrelevant")
		})}, Agent: pathReader{err: errors.New("wrong executable")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checker.Check(ctx, updater.HealthExpectation{}); err == nil {
		t.Fatal("Check() error = nil")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
