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
		if request.URL.Path != "/healthz" && request.URL.Path != "/readyz" {
			http.NotFound(writer, request)
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
	if err := checker.Check(context.Background(), updater.HealthExpectation{Version: "v1.2.1", AgentPath: agentPath}); err != nil {
		t.Fatalf("Check() error = %v", err)
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
