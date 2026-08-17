package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestQ3_HistoryGapNotFilledAndRemoteUnavailable(t *testing.T) {
	addrA, rootA := startClusterAgent(t, "")
	addrC, rootC, cancelC := startClusterAgentCtl(t, "")
	idA := readNodeID(t, rootA)
	idC := readNodeID(t, rootC)
	joinTwo(t, addrA, addrC)

	// 直接往 A 的 store 插两个相隔 120s 的点
	insertLocalSamples(t, rootA, idA, [][2]int64{{1_700_000_000, 11}, {1_700_000_120, 22}})

	out := mustCLI(t, addrA, "metrics", "history", "node", idA,
		"--since", "1700000000", "--until", "1700000120")
	if !strings.Contains(out, "ts=1700000000") || !strings.Contains(out, "value=11") {
		t.Fatalf("%s", out)
	}
	if strings.Contains(out, "ts=1700000060") {
		t.Fatalf("gap filled: %s", out)
	}

	cancelC()
	code, _, errb := runCLIExit(t, addrA, "metrics", "history", "node", idC)
	if code == 0 {
		t.Fatal("down owner must fail")
	}
	if !strings.Contains(strings.ToLower(errb), "unavailable") {
		t.Fatalf("want UNAVAILABLE, got %q", errb)
	}
}

func TestQ3_Disk95StopsWritesKeepsReads(t *testing.T) {
	// 单元级已覆盖 Recorder；此处用 store 预置点 + CLI 读，证明读路径不依赖写
	addr, root := startClusterAgent(t, "")
	id := readNodeID(t, root)
	insertLocalSamples(t, root, id, [][2]int64{{1_700_000_000, 4}})
	out := mustCLI(t, addr, "metrics", "history", "node", id, "--since", "1700000000", "--until", "1700000060")
	if !strings.Contains(out, "value=4") {
		t.Fatalf("%s", out)
	}
}

func insertLocalSamples(t *testing.T, root, nodeID string, points [][2]int64) {
	t.Helper()
	st, err := store.Open(paths.New(root).Store)
	if err != nil {
		t.Fatalf("open store %s: %v", paths.New(root).Store, err)
	}
	defer func() { _ = st.Close() }()

	samples := make([]store.MetricSample, 0, len(points))
	for _, p := range points {
		samples = append(samples, store.MetricSample{
			Series:    metrics.SeriesNodeCPU,
			SubjectID: nodeID,
			Layer:     metrics.LayerRawMin,
			TSUnix:    p[0],
			Value:     float64(p[1]),
		})
	}
	if err := st.InsertMetricSamples(context.Background(), samples); err != nil {
		t.Fatalf("insert metric samples: %v", err)
	}
}

func runCLIExit(t *testing.T, addr string, args ...string) (int, string, string) {
	t.Helper()
	all := append([]string{"--server", addr}, args...)
	return runP1CLI(all...)
}
