package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

type AgentPathReader interface {
	RunningAgentPath(context.Context) (string, error)
}

type HTTPHealth struct {
	Address      string
	Timeout      time.Duration
	PollInterval time.Duration
	Client       *http.Client
	Agent        AgentPathReader
}

func (h HTTPHealth) Check(ctx context.Context, expectation HealthExpectation) error {
	address := h.Address
	if expectation.Address != "" {
		address = expectation.Address
	}
	if address == "" {
		address = "127.0.0.1:18680"
	}
	if err := requireLoopbackAddress(address); err != nil {
		return err
	}
	timeout := h.Timeout
	if expectation.Timeout != 0 {
		timeout = expectation.Timeout
	}
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	if timeout < 10*time.Second || timeout > 5*time.Minute || h.Agent == nil {
		return errors.New("invalid health checker configuration")
	}
	interval := h.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("health endpoint redirects are not allowed")
		}}
	}
	checkContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastError error
	for {
		lastError = h.checkOnce(checkContext, client, address, expectation)
		if lastError == nil {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-checkContext.Done():
			timer.Stop()
			return fmt.Errorf("agent health timeout: %w", errors.Join(lastError, checkContext.Err()))
		case <-timer.C:
		}
	}
}

func (h HTTPHealth) checkOnce(ctx context.Context, client *http.Client, address string, expectation HealthExpectation) error {
	for _, endpoint := range []string{"/healthz", "/readyz", "/updatez"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+endpoint, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (4<<10)+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("%s returned status %d", endpoint, response.StatusCode)
		}
		if endpoint == "/updatez" {
			if len(body) > 4<<10 {
				return errors.New("update health response is too large")
			}
			var updateStatus struct {
				Version              string `json:"version"`
				StoreReady           bool   `json:"store_ready"`
				ShimRecoveryComplete bool   `json:"shim_recovery_complete"`
			}
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&updateStatus); err != nil {
				return fmt.Errorf("decode update health response: %w", err)
			}
			var extra any
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				return errors.New("update health response contains multiple JSON values")
			}
			if updateStatus.Version != expectation.Version {
				return fmt.Errorf("running agent version %q does not match target %q", updateStatus.Version, expectation.Version)
			}
			if !updateStatus.StoreReady || !updateStatus.ShimRecoveryComplete {
				return errors.New("agent store or shim recovery is incomplete")
			}
		}
	}
	runningPath, err := h.Agent.RunningAgentPath(ctx)
	if err != nil {
		return err
	}
	runningPath, err = filepath.EvalSymlinks(runningPath)
	if err != nil {
		return err
	}
	expectedPath, err := filepath.EvalSymlinks(expectation.AgentPath)
	if err != nil {
		return err
	}
	if runningPath != expectedPath {
		return errors.New("systemd agent executable does not match target version")
	}
	return nil
}

func requireLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("health address must be a loopback host and port")
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("health address must be loopback")
	}
	return nil
}
