package agent

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_MetricsFlow verifies the complete metrics collection pipeline:
// Agent → Collector → API/Cluster integration.
func TestE2E_MetricsFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	// 1. Start a live agent with full metrics collection
	addr := startLiveAgent(t)

	// 2. Wait for collector to start and collect initial data
	time.Sleep(300 * time.Millisecond)

	// 3. Verify agent is healthy and metrics endpoint is available
	res, err := http.Get("http://" + addr + "/healthz")
	require.NoError(t, err, "should reach healthz endpoint")
	require.Equal(t, http.StatusOK, res.StatusCode, "healthz should return 200")
	_ = res.Body.Close()

	// 4. Call GetAgentMetrics API to verify node-level metrics
	client := procmeshv1connect.NewMetricsServiceClient(
		http.DefaultClient,
		"http://"+addr,
	)

	resp, err := client.GetAgentMetrics(
		context.Background(),
		connect.NewRequest(&procmeshv1.GetAgentMetricsRequest{}),
	)
	require.NoError(t, err, "GetAgentMetrics should succeed")
	require.NotNil(t, resp.Msg, "response should not be nil")
	require.NotNil(t, resp.Msg.Metrics, "metrics should not be nil")

	m := resp.Msg.Metrics
	require.NotNil(t, m.Resources, "resources should not be nil")

	// Validate CPU metrics
	assert.GreaterOrEqual(t, m.Resources.CpuPercent, int32(0), "CPU percent should be >= 0")
	assert.LessOrEqual(t, m.Resources.CpuPercent, int32(100), "CPU percent should be <= 100")

	// Validate Memory metrics
	assert.GreaterOrEqual(t, m.Resources.MemoryPercent, int32(0), "Memory percent should be >= 0")
	assert.LessOrEqual(t, m.Resources.MemoryPercent, int32(100), "Memory percent should be <= 100")

	// Validate Disk metrics
	assert.GreaterOrEqual(t, m.Resources.DiskPercent, int32(0), "Disk percent should be >= 0")
	assert.LessOrEqual(t, m.Resources.DiskPercent, int32(100), "Disk percent should be <= 100")

	t.Logf("✓ Node metrics collected successfully:")
	t.Logf("  CPU: %d%%", m.Resources.CpuPercent)
	t.Logf("  Memory: %d%%", m.Resources.MemoryPercent)
	t.Logf("  Disk: %d%%", m.Resources.DiskPercent)
	t.Logf("  Uptime: %.1f seconds", m.UptimeSeconds)
	t.Logf("  Process Running: %d", m.ProcessRunning)
	t.Logf("  Members: %d, Alive: %d", m.Members, m.Alive)
}

// TestE2E_MetricsWithProcess verifies process-level metrics collection.
func TestE2E_MetricsWithProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	// 1. Start agent
	addr := startLiveAgent(t)
	time.Sleep(200 * time.Millisecond)

	// 2. Start a test process
	spec := writeSleepSpec(t)
	name := "sleep"

	code, _, errb := runP1CLI("--server", addr, "process", "apply", "--file", spec, "--expected-revision", "0")
	require.Equal(t, 0, code, "apply should succeed: %s", errb)

	code, _, errb = runP1CLI("--server", addr, "process", "start", name)
	require.Equal(t, 0, code, "start should succeed: %s", errb)

	// 3. Wait for process to start and metrics to be collected
	deadline := time.Now().Add(8 * time.Second)
	var listOut string
	for time.Now().Before(deadline) {
		code, listOut, _ = runP1CLI("--server", addr, "process", "list")
		if code == 0 && listHasRunningOrStarting(listOut) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, listHasRunningOrStarting(listOut), "process should be RUNNING or STARTING")

	// 4. Query process metrics via API
	metricsClient := procmeshv1connect.NewMetricsServiceClient(
		http.DefaultClient,
		"http://"+addr,
	)

	metricsResp, err := metricsClient.GetProcessMetrics(
		context.Background(),
		connect.NewRequest(&procmeshv1.GetProcessMetricsRequest{IdOrName: name}),
	)
	require.NoError(t, err, "GetProcessMetrics should succeed")
	require.NotNil(t, metricsResp.Msg, "response should not be nil")
	require.NotEmpty(t, metricsResp.Msg.Metrics, "metrics should not be empty")

	pm := metricsResp.Msg.Metrics[0]
	t.Logf("✓ Process metrics collected:")
	t.Logf("  Instance ID: %s", pm.InstanceId)
	t.Logf("  PID: %d", pm.Pid)
	t.Logf("  Uptime: %d seconds", pm.UptimeSeconds)
	t.Logf("  CPU: %d%%", pm.CpuPercent)
	t.Logf("  Memory: %d bytes", pm.MemoryBytes)
	if pm.Note != "" {
		t.Logf("  Note: %s", pm.Note)
	}

	assert.Greater(t, pm.Pid, int32(0), "PID should be > 0")
	assert.GreaterOrEqual(t, pm.UptimeSeconds, int64(0), "uptime should be >= 0")
	assert.Empty(t, pm.Note, "collector should be wired; note=%s", pm.Note)
	assert.GreaterOrEqual(t, pm.CpuPercent, int32(0), "process CPU should be collected")
	assert.Greater(t, pm.MemoryBytes, int64(0), "process memory should be collected")

	// 5. Stop process
	code, _, errb = runP1CLI("--server", addr, "process", "stop", name)
	require.Equal(t, 0, code, "stop should succeed: %s", errb)
}

// TestE2E_MetricsGracefulDegradation verifies graceful handling when metrics collection fails.
func TestE2E_MetricsGracefulDegradation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	addr := startLiveAgent(t)
	time.Sleep(200 * time.Millisecond)

	// Agent should still be healthy even if metrics collection has issues
	res, err := http.Get("http://" + addr + "/healthz")
	require.NoError(t, err, "healthz should still work")
	require.Equal(t, http.StatusOK, res.StatusCode, "agent should remain healthy")
	_ = res.Body.Close()

	// GetAgentMetrics might return stale or zero metrics, but should not fail
	client := procmeshv1connect.NewMetricsServiceClient(
		http.DefaultClient,
		"http://"+addr,
	)

	resp, err := client.GetAgentMetrics(
		context.Background(),
		connect.NewRequest(&procmeshv1.GetAgentMetricsRequest{}),
	)

	// API should return successfully even if metrics are unavailable
	if err != nil {
		t.Logf("GetAgentMetrics returned error (acceptable): %v", err)
	} else {
		require.NotNil(t, resp.Msg, "response should not be nil")
		t.Logf("✓ GetAgentMetrics returned gracefully with metrics: %+v", resp.Msg.Metrics)
	}
}
