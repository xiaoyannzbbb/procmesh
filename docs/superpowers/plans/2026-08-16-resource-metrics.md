# Resource Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现节点级和进程级资源监控，支持 Linux 和 macOS，采集 CPU/Memory/Disk 指标并通过 Gossip 和 API 暴露。

**Architecture:** 创建独立的 `internal/metrics` 包，使用 gopsutil 库跨平台采集资源数据。Collector 在后台每 5 秒采集一次节点指标并缓存，进程指标按需实时采集。Agent 启动 Collector，Cluster 和 API 消费指标数据。

**Tech Stack:** Go 1.25+, gopsutil v3.23.12, testing/testify

## Global Constraints

- Go 1.25+ required
- gopsutil v3.23.12 dependency
- 80%+ test coverage for internal/metrics package
- TDD workflow: write tests first, then implementation
- 支持 Linux 和 macOS 平台
- 采集失败时优雅降级返回 -1
- 磁盘监控仅针对数据目录所在分区

---

### Task 1: 添加 gopsutil 依赖

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: 无
- Produces: gopsutil v3 库可用于导入

- [ ] **Step 1: 添加 gopsutil 依赖**

```bash
go get github.com/shirou/gopsutil/v3@v3.23.12
```

- [ ] **Step 2: 验证依赖安装**

Run: `go mod tidy && go mod verify`
Expected: 无错误，go.mod 和 go.sum 更新

- [ ] **Step 3: 提交依赖变更**

```bash
git add go.mod go.sum
git commit -m "chore: add gopsutil v3.23.12 for resource metrics"
```

---

### Task 2: 实现数据结构和类型定义

**Files:**
- Create: `internal/metrics/types.go`
- Create: `internal/metrics/types_test.go`

**Interfaces:**
- Consumes: 无
- Produces: 
  - `type NodeMetrics struct` - 节点指标结构
  - `type ProcessMetrics struct` - 进程指标结构
  - `type Collector struct` - 采集器结构

- [ ] **Step 1: 编写 NodeMetrics 结构测试**

```go
// internal/metrics/types_test.go
package metrics

import (
	"testing"
	"time"
)

func TestNodeMetrics_ValidValues(t *testing.T) {
	nm := &NodeMetrics{
		CPUPercent:    45.5,
		MemoryPercent: 67.2,
		MemoryUsed:    8 * 1024 * 1024 * 1024,  // 8GB
		MemoryTotal:   16 * 1024 * 1024 * 1024, // 16GB
		DiskPercent:   30.0,
		DiskUsed:      100 * 1024 * 1024 * 1024,  // 100GB
		DiskTotal:     500 * 1024 * 1024 * 1024,  // 500GB
		Timestamp:     time.Now(),
	}

	if nm.CPUPercent < 0 || nm.CPUPercent > 100 {
		t.Errorf("invalid CPU percent: %f", nm.CPUPercent)
	}
	if nm.MemoryPercent < 0 || nm.MemoryPercent > 100 {
		t.Errorf("invalid memory percent: %f", nm.MemoryPercent)
	}
	if nm.DiskPercent < 0 || nm.DiskPercent > 100 {
		t.Errorf("invalid disk percent: %f", nm.DiskPercent)
	}
}

func TestProcessMetrics_ValidValues(t *testing.T) {
	pm := &ProcessMetrics{
		PID:         12345,
		CPUPercent:  25.5,
		MemoryBytes: 512 * 1024 * 1024, // 512MB
		Timestamp:   time.Now(),
	}

	if pm.PID <= 0 {
		t.Errorf("invalid PID: %d", pm.PID)
	}
	if pm.CPUPercent < 0 {
		t.Errorf("invalid CPU percent: %f", pm.CPUPercent)
	}
	if pm.MemoryBytes == 0 {
		t.Error("memory bytes should not be zero")
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/metrics -v`
Expected: FAIL - package metrics 不存在

- [ ] **Step 3: 实现数据结构**

```go
// internal/metrics/types.go
package metrics

import (
	"context"
	"sync"
	"time"
)

// NodeMetrics 节点级资源指标
type NodeMetrics struct {
	CPUPercent    float64   // 整机 CPU 使用率 0-100
	MemoryPercent float64   // 内存使用率 0-100
	MemoryUsed    uint64    // 已用内存（字节）
	MemoryTotal   uint64    // 总内存（字节）
	DiskPercent   float64   // 磁盘使用率 0-100（数据目录所在分区）
	DiskUsed      uint64    // 已用磁盘（字节）
	DiskTotal     uint64    // 总磁盘（字节）
	Timestamp     time.Time // 采集时间
}

// ProcessMetrics 进程级资源指标
type ProcessMetrics struct {
	PID         int       // 进程 ID
	CPUPercent  float64   // 进程 CPU 使用率 0-100（单核）
	MemoryBytes uint64    // 进程内存（RSS）
	Timestamp   time.Time // 采集时间
}

// Collector 资源采集器
type Collector struct {
	interval   time.Duration      // 采集间隔
	dataDir    string             // 数据目录路径（用于磁盘监控）
	cancel     context.CancelFunc // 停止信号
	mu         sync.RWMutex       // 保护缓存
	node       *NodeMetrics       // 缓存的节点指标
	nodeErr    error              // 最近一次采集错误
	lastUpdate time.Time          // 最后更新时间
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/metrics -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/metrics/types.go internal/metrics/types_test.go
git commit -m "feat(metrics): add data structures for node and process metrics"
```

---

### Task 3: 实现节点资源采集

**Files:**
- Create: `internal/metrics/node.go`
- Create: `internal/metrics/node_test.go`

**Interfaces:**
- Consumes: `type NodeMetrics` from Task 2
- Produces: `func collectNode(dataDir string) (*NodeMetrics, error)` - 采集节点指标

- [ ] **Step 1: 编写节点采集测试**

```go
// internal/metrics/node_test.go
package metrics

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestCollectNode(t *testing.T) {
	tmpDir := t.TempDir()

	node, err := collectNode(tmpDir)
	if err != nil {
		t.Fatalf("collectNode failed: %v", err)
	}

	// 验证 CPU 范围
	if node.CPUPercent < 0 || node.CPUPercent > 100 {
		t.Errorf("invalid CPU percent: %f", node.CPUPercent)
	}

	// 验证内存范围
	if node.MemoryPercent < 0 || node.MemoryPercent > 100 {
		t.Errorf("invalid memory percent: %f", node.MemoryPercent)
	}
	if node.MemoryUsed == 0 || node.MemoryTotal == 0 {
		t.Error("memory values should not be zero")
	}
	if node.MemoryUsed > node.MemoryTotal {
		t.Errorf("memory used (%d) > total (%d)", node.MemoryUsed, node.MemoryTotal)
	}

	// 验证磁盘范围
	if node.DiskPercent < 0 || node.DiskPercent > 100 {
		t.Errorf("invalid disk percent: %f", node.DiskPercent)
	}
	if node.DiskUsed == 0 || node.DiskTotal == 0 {
		t.Error("disk values should not be zero")
	}
	if node.DiskUsed > node.DiskTotal {
		t.Errorf("disk used (%d) > total (%d)", node.DiskUsed, node.DiskTotal)
	}

	// 验证时间戳
	if time.Since(node.Timestamp) > 2*time.Second {
		t.Errorf("stale timestamp: %v", node.Timestamp)
	}

	t.Logf("%s metrics: CPU=%.1f%% Mem=%.1f%% (%.1fGB/%.1fGB) Disk=%.1f%% (%.1fGB/%.1fGB)",
		runtime.GOOS,
		node.CPUPercent,
		node.MemoryPercent,
		float64(node.MemoryUsed)/(1024*1024*1024),
		float64(node.MemoryTotal)/(1024*1024*1024),
		node.DiskPercent,
		float64(node.DiskUsed)/(1024*1024*1024),
		float64(node.DiskTotal)/(1024*1024*1024))
}

func TestCollectNode_InvalidPath(t *testing.T) {
	_, err := collectNode("/nonexistent/path/12345/impossible")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
	t.Logf("got expected error: %v", err)
}

func TestCollectNode_CrossPlatform(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping platform test in short mode")
	}

	// 使用当前目录测试
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}

	node, err := collectNode(wd)
	if err != nil {
		t.Fatalf("%s: collectNode failed: %v", runtime.GOOS, err)
	}

	t.Logf("%s: CPU=%.1f%% Memory=%.1f%% Disk=%.1f%%",
		runtime.GOOS, node.CPUPercent, node.MemoryPercent, node.DiskPercent)
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/metrics -v -run TestCollectNode`
Expected: FAIL - collectNode 函数未定义

- [ ] **Step 3: 实现节点采集逻辑**

```go
// internal/metrics/node.go
package metrics

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// collectNode 采集节点级资源指标
// dataDir: 数据目录路径，用于确定监控哪个磁盘分区
// 注意：此函数会阻塞约 1 秒（CPU 采样需要）
func collectNode(dataDir string) (*NodeMetrics, error) {
	// CPU：采样 1 秒，获取总体使用率
	cpuPct, err := cpu.Percent(time.Second, false) // false = 总体，不按核
	if err != nil {
		return nil, fmt.Errorf("cpu percent: %w", err)
	}
	if len(cpuPct) == 0 {
		return nil, fmt.Errorf("cpu percent returned empty array")
	}

	// Memory
	vmem, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}

	// Disk：数据目录所在分区
	diskStat, err := disk.Usage(dataDir)
	if err != nil {
		return nil, fmt.Errorf("disk usage for %s: %w", dataDir, err)
	}

	return &NodeMetrics{
		CPUPercent:    cpuPct[0],
		MemoryPercent: vmem.UsedPercent,
		MemoryUsed:    vmem.Used,
		MemoryTotal:   vmem.Total,
		DiskPercent:   diskStat.UsedPercent,
		DiskUsed:      diskStat.Used,
		DiskTotal:     diskStat.Total,
		Timestamp:     time.Now(),
	}, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/metrics -v -run TestCollectNode`
Expected: PASS（所有三个测试都通过）

- [ ] **Step 5: 提交**

```bash
git add internal/metrics/node.go internal/metrics/node_test.go
git commit -m "feat(metrics): implement node resource collection with gopsutil"
```

---

### Task 4: 实现进程资源采集

**Files:**
- Create: `internal/metrics/process.go`
- Create: `internal/metrics/process_test.go`

**Interfaces:**
- Consumes: `type ProcessMetrics` from Task 2
- Produces: `func collectProcess(pid int) (*ProcessMetrics, error)` - 采集进程指标

- [ ] **Step 1: 编写进程采集测试**

```go
// internal/metrics/process_test.go
package metrics

import (
	"os"
	"testing"
	"time"
)

func TestCollectProcess_Self(t *testing.T) {
	// 测试采集当前进程
	pid := os.Getpid()

	pm, err := collectProcess(pid)
	if err != nil {
		t.Fatalf("collectProcess failed: %v", err)
	}

	if pm.PID != pid {
		t.Errorf("wrong PID: got %d, want %d", pm.PID, pid)
	}

	// CPU 可能是 0（瞬时值），但不应该是负数
	if pm.CPUPercent < 0 {
		t.Errorf("invalid CPU percent: %f", pm.CPUPercent)
	}

	// 内存不应该为 0
	if pm.MemoryBytes == 0 {
		t.Error("memory bytes should not be zero")
	}

	// 验证时间戳
	if time.Since(pm.Timestamp) > time.Second {
		t.Errorf("stale timestamp: %v", pm.Timestamp)
	}

	t.Logf("process %d: CPU=%.1f%% Memory=%.1fMB",
		pid, pm.CPUPercent, float64(pm.MemoryBytes)/(1024*1024))
}

func TestCollectProcess_NotFound(t *testing.T) {
	// 使用不存在的 PID
	_, err := collectProcess(999999)
	if err == nil {
		t.Fatal("expected error for non-existent process")
	}
	t.Logf("got expected error: %v", err)
}

func TestCollectProcess_InvalidPID(t *testing.T) {
	testCases := []struct {
		name string
		pid  int
	}{
		{"zero", 0},
		{"negative", -1},
		{"large negative", -999},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := collectProcess(tc.pid)
			if err == nil {
				t.Fatalf("expected error for PID %d", tc.pid)
			}
			t.Logf("PID %d: got expected error: %v", tc.pid, err)
		})
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/metrics -v -run TestCollectProcess`
Expected: FAIL - collectProcess 函数未定义

- [ ] **Step 3: 实现进程采集逻辑**

```go
// internal/metrics/process.go
package metrics

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// collectProcess 采集单个进程的资源指标
// 此函数轻量且不阻塞，返回瞬时值
func collectProcess(pid int) (*ProcessMetrics, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid pid: %d", pid)
	}

	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return nil, fmt.Errorf("process not found: %w", err)
	}

	// CPU（瞬时值，不阻塞）
	cpuPct, err := p.CPUPercent()
	if err != nil {
		return nil, fmt.Errorf("cpu percent: %w", err)
	}

	// Memory（RSS）
	memInfo, err := p.MemoryInfo()
	if err != nil {
		return nil, fmt.Errorf("memory info: %w", err)
	}

	return &ProcessMetrics{
		PID:         pid,
		CPUPercent:  cpuPct,
		MemoryBytes: memInfo.RSS,
		Timestamp:   time.Now(),
	}, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/metrics -v -run TestCollectProcess`
Expected: PASS（所有三个测试都通过）

- [ ] **Step 5: 提交**

```bash
git add internal/metrics/process.go internal/metrics/process_test.go
git commit -m "feat(metrics): implement process resource collection"
```

---

### Task 5: 实现 Collector 后台采集逻辑

**Files:**
- Create: `internal/metrics/collector.go`
- Create: `internal/metrics/collector_test.go`

**Interfaces:**
- Consumes: 
  - `type Collector` from Task 2
  - `func collectNode(dataDir string) (*NodeMetrics, error)` from Task 3
  - `func collectProcess(pid int) (*ProcessMetrics, error)` from Task 4
- Produces:
  - `func New(dataDir string, interval time.Duration) *Collector`
  - `func (c *Collector) Start(ctx context.Context) error`
  - `func (c *Collector) Stop()`
  - `func (c *Collector) NodeMetrics() (*NodeMetrics, error)`
  - `func (c *Collector) ProcessMetrics(pid int) (*ProcessMetrics, error)`

- [ ] **Step 1: 编写 Collector 测试**

```go
// internal/metrics/collector_test.go
package metrics

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestCollector_New(t *testing.T) {
	c := New("/tmp", 5*time.Second)
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.interval != 5*time.Second {
		t.Errorf("wrong interval: got %v, want %v", c.interval, 5*time.Second)
	}
	if c.dataDir != "/tmp" {
		t.Errorf("wrong dataDir: got %s, want /tmp", c.dataDir)
	}
}

func TestCollector_StartStop(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 100*time.Millisecond)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 等待至少一次采集
	time.Sleep(50 * time.Millisecond)

	// 验证已采集数据
	node, err := c.NodeMetrics()
	if err != nil {
		t.Fatalf("NodeMetrics failed: %v", err)
	}
	if node == nil {
		t.Fatal("node metrics is nil")
	}

	// 停止
	c.Stop()

	// 再次读取应该仍然返回缓存数据
	node2, err := c.NodeMetrics()
	if err != nil {
		t.Errorf("NodeMetrics after Stop failed: %v", err)
	}
	if node2 == nil {
		t.Error("node metrics is nil after Stop")
	}
}

func TestCollector_BackgroundUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer c.Stop()

	// 等待第一次采集
	time.Sleep(50 * time.Millisecond)

	c.mu.RLock()
	firstUpdate := c.lastUpdate
	c.mu.RUnlock()

	if firstUpdate.IsZero() {
		t.Fatal("first update timestamp is zero")
	}

	// 等待第二次采集
	time.Sleep(150 * time.Millisecond)

	c.mu.RLock()
	secondUpdate := c.lastUpdate
	c.mu.RUnlock()

	if !secondUpdate.After(firstUpdate) {
		t.Error("metrics not updating in background")
	}

	t.Logf("updates: first=%v, second=%v, diff=%v",
		firstUpdate, secondUpdate, secondUpdate.Sub(firstUpdate))
}

func TestCollector_ConcurrentRead(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 50*time.Millisecond)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer c.Stop()

	// 等待初始采集
	time.Sleep(100 * time.Millisecond)

	// 并发读取
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 20; j++ {
				node, err := c.NodeMetrics()
				if err != nil {
					t.Errorf("goroutine %d: NodeMetrics failed: %v", id, err)
					break
				}
				if node == nil {
					t.Errorf("goroutine %d: node is nil", id)
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			done <- true
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestCollector_ProcessMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 5*time.Second)

	// ProcessMetrics 不需要 Start
	pm, err := c.ProcessMetrics(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessMetrics failed: %v", err)
	}
	if pm == nil {
		t.Fatal("process metrics is nil")
	}
	if pm.PID != os.Getpid() {
		t.Errorf("wrong PID: got %d, want %d", pm.PID, os.Getpid())
	}
}

func TestCollector_NodeMetrics_BeforeStart(t *testing.T) {
	tmpDir := t.TempDir()
	c := New(tmpDir, 5*time.Second)

	// 未启动前读取应该返回 error
	_, err := c.NodeMetrics()
	if err == nil {
		t.Fatal("expected error before Start")
	}
	t.Logf("got expected error: %v", err)
}

func TestCollector_InvalidDataDir(t *testing.T) {
	c := New("/nonexistent/impossible/path/12345", 5*time.Second)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		// Start 不应该失败，只是采集会失败
		t.Fatalf("Start should not fail: %v", err)
	}
	defer c.Stop()

	// 等待采集尝试
	time.Sleep(100 * time.Millisecond)

	// 读取应该返回错误
	_, err := c.NodeMetrics()
	if err == nil {
		t.Fatal("expected error for invalid dataDir")
	}
	t.Logf("got expected error: %v", err)
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/metrics -v -run TestCollector`
Expected: FAIL - Collector 方法未实现

- [ ] **Step 3: 实现 Collector 逻辑（第一部分）**

```go
// internal/metrics/collector.go
package metrics

import (
	"context"
	"fmt"
	"log"
	"time"
)

// New 创建新的 Collector 实例（未启动）
func New(dataDir string, interval time.Duration) *Collector {
	return &Collector{
		interval: interval,
		dataDir:  dataDir,
	}
}

// Start 启动后台采集协程
func (c *Collector) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	// 立即采集一次（避免启动时返回空数据）
	c.collect()

	// 启动后台协程
	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				c.collect()
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

// Stop 停止后台采集
func (c *Collector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
}

// collect 执行一次采集（内部方法）
func (c *Collector) collect() {
	node, err := collectNode(c.dataDir)

	c.mu.Lock()
	c.node = node
	c.nodeErr = err
	c.lastUpdate = time.Now()
	c.mu.Unlock()

	if err != nil {
		log.Printf("metrics collection failed: %v", err)
	}
}

// NodeMetrics 获取缓存的节点指标（非阻塞）
func (c *Collector) NodeMetrics() (*NodeMetrics, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.nodeErr != nil {
		return nil, c.nodeErr
	}
	if c.node == nil {
		return nil, fmt.Errorf("no metrics available")
	}

	// 返回副本，避免外部修改
	nm := *c.node
	return &nm, nil
}

// ProcessMetrics 获取进程指标（实时采集，轻量）
func (c *Collector) ProcessMetrics(pid int) (*ProcessMetrics, error) {
	return collectProcess(pid)
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/metrics -v`
Expected: PASS（所有测试通过，覆盖率 >80%）

- [ ] **Step 5: 检查测试覆盖率**

Run: `go test ./internal/metrics -cover`
Expected: coverage >80%

- [ ] **Step 6: 提交**

```bash
git add internal/metrics/collector.go internal/metrics/collector_test.go
git commit -m "feat(metrics): implement Collector with background collection"
```

---

### Task 6: 集成 Collector 到 Agent

**Files:**
- Modify: `internal/agent/agent.go`
- Create: `internal/agent/metrics_integration_test.go`

**Interfaces:**
- Consumes: 
  - `func New(dataDir string, interval time.Duration) *Collector` from Task 5
  - `func (c *Collector) Start(ctx context.Context) error` from Task 5
  - `func (c *Collector) Stop()` from Task 5
- Produces: Agent.metrics 字段可用于 Cluster 和 API

- [ ] **Step 1: 查看 Agent 结构**

Run: `grep -n "type Agent struct" internal/agent/agent.go`
Expected: 找到 Agent 结构定义位置

- [ ] **Step 2: 编写集成测试**

```go
// internal/agent/metrics_integration_test.go
package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgent_MetricsCollector(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// 创建测试 Agent（使用现有的测试辅助函数）
	a, cleanup := setupTestAgent(t)
	defer cleanup()

	// 验证 Collector 已启动
	require.NotNil(t, a.metrics, "metrics collector should be initialized")

	// 等待至少一次采集
	time.Sleep(100 * time.Millisecond)

	// 验证可以读取节点指标
	node, err := a.metrics.NodeMetrics()
	require.NoError(t, err, "should get node metrics")
	require.NotNil(t, node, "node metrics should not be nil")

	assert.GreaterOrEqual(t, node.CPUPercent, 0.0, "CPU percent should be >= 0")
	assert.LessOrEqual(t, node.CPUPercent, 100.0, "CPU percent should be <= 100")
	assert.Greater(t, node.MemoryTotal, uint64(0), "memory total should be > 0")
	assert.Greater(t, node.DiskTotal, uint64(0), "disk total should be > 0")

	t.Logf("Node metrics: CPU=%.1f%% Mem=%.1f%% Disk=%.1f%%",
		node.CPUPercent, node.MemoryPercent, node.DiskPercent)
}
```

- [ ] **Step 3: 运行测试验证失败**

Run: `go test ./internal/agent -v -run TestAgent_MetricsCollector`
Expected: FAIL - setupTestAgent 未定义或 a.metrics 不存在

- [ ] **Step 4: 修改 Agent 结构添加 metrics 字段**

在 `internal/agent/agent.go` 中找到 `type Agent struct` 定义，添加 metrics 字段：

```go
import (
	// ... 现有 imports
	"github.com/your-org/procmesh/internal/metrics"
)

type Agent struct {
	// ... 现有字段
	metrics *metrics.Collector  // 新增：资源监控采集器
}
```

- [ ] **Step 5: 修改 Agent.Start 方法启动 Collector**

在 `internal/agent/agent.go` 的 `Start` 方法中，在启动其他组件之前添加：

```go
func (a *Agent) Start(ctx context.Context) error {
	// ... 现有启动逻辑

	// 启动 metrics collector
	a.metrics = metrics.New(a.cfg.DataDir, 5*time.Second)
	if err := a.metrics.Start(ctx); err != nil {
		return fmt.Errorf("start metrics collector: %w", err)
	}

	// ... 其他组件启动
}
```

- [ ] **Step 6: 修改 Agent.Stop 方法停止 Collector**

在 `internal/agent/agent.go` 的 `Stop` 方法中添加：

```go
func (a *Agent) Stop() {
	// 停止 metrics collector
	if a.metrics != nil {
		a.metrics.Stop()
	}

	// ... 其他清理逻辑
}
```

- [ ] **Step 7: 运行测试验证通过**

Run: `go test ./internal/agent -v -run TestAgent_MetricsCollector`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/agent/agent.go internal/agent/metrics_integration_test.go
git commit -m "feat(agent): integrate metrics collector into agent lifecycle"
```

---

### Task 7: 集成节点指标到 Cluster

**Files:**
- Modify: `internal/cluster/livesource.go`
- Modify: `internal/cluster/types.go` (如果需要)

**Interfaces:**
- Consumes: 
  - `Agent.metrics *metrics.Collector` from Task 6
  - `func (c *Collector) NodeMetrics() (*NodeMetrics, error)` from Task 5
- Produces: Cluster Gossip 传播真实的资源数据（替换硬编码的 -1）

- [ ] **Step 1: 查看当前 livesource 实现**

Run: `grep -n "type liveSource struct" internal/cluster/livesource.go`
Expected: 找到 liveSource 结构定义

Run: `grep -n "func.*Snapshot" internal/cluster/livesource.go`
Expected: 找到 Snapshot 方法位置

- [ ] **Step 2: 修改 liveSource 结构添加 metrics 字段**

在 `internal/cluster/livesource.go` 中找到 `type liveSource struct`，添加：

```go
import (
	"math"
	// ... 现有 imports
	"github.com/your-org/procmesh/internal/metrics"
)

type liveSource struct {
	// ... 现有字段
	metrics *metrics.Collector  // 从 Agent 注入
}
```

- [ ] **Step 3: 修改 liveSource 的构造函数接受 metrics 参数**

找到创建 liveSource 的函数（可能是 `NewLiveSource` 或类似），添加 metrics 参数：

```go
func NewLiveSource(/* 现有参数 */, metrics *metrics.Collector) *liveSource {
	return &liveSource{
		// ... 现有字段初始化
		metrics: metrics,
	}
}
```

- [ ] **Step 4: 修改 Snapshot 方法使用真实数据**

找到 `Snapshot()` 方法中硬编码 `-1` 的部分，替换为：

```go
func (s *liveSource) Snapshot() LocalSnapshot {
	var res ResourceSummary

	if s.metrics == nil {
		// Collector 未初始化（降级模式）
		res = ResourceSummary{
			CPUPercent:    -1,
			MemoryPercent: -1,
			DiskPercent:   -1,
		}
	} else {
		node, err := s.metrics.NodeMetrics()
		if err != nil {
			// 采集失败
			res = ResourceSummary{
				CPUPercent:    -1,
				MemoryPercent: -1,
				DiskPercent:   -1,
			}
		} else {
			res = ResourceSummary{
				CPUPercent:    int32(math.Round(node.CPUPercent)),
				MemoryPercent: int32(math.Round(node.MemoryPercent)),
				DiskPercent:   int32(math.Round(node.DiskPercent)),
			}
		}
	}

	return LocalSnapshot{
		Resources: res,
		// ... 其他字段保持不变
	}
}
```

- [ ] **Step 5: 修改 Agent 传递 metrics 到 liveSource**

在 `internal/agent/agent.go` 中，找到创建 liveSource 的地方，传入 `a.metrics`：

```go
// 示例（实际代码可能不同）
liveSource := cluster.NewLiveSource(/* 现有参数 */, a.metrics)
```

- [ ] **Step 6: 构建验证无编译错误**

Run: `go build ./internal/cluster`
Expected: 无错误

- [ ] **Step 7: 运行 cluster 包测试**

Run: `go test ./internal/cluster -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/cluster/livesource.go internal/agent/agent.go
git commit -m "feat(cluster): integrate real resource metrics into gossip"
```

---

### Task 8: 集成进程指标到 API

**Files:**
- Modify: `internal/api/metricsapi.go`
- Delete: `internal/api/procstat_linux.go`
- Delete: `internal/api/procstat_other.go`

**Interfaces:**
- Consumes:
  - `Agent.metrics *metrics.Collector` from Task 6
  - `func (c *Collector) ProcessMetrics(pid int) (*ProcessMetrics, error)` from Task 5
- Produces: API 返回真实的进程 CPU/Memory 数据

- [ ] **Step 1: 查看当前 MetricsAPI 结构**

Run: `grep -n "type MetricsAPI struct" internal/api/metricsapi.go`
Expected: 找到 MetricsAPI 结构定义

Run: `grep -n "processMetricsOf" internal/api/metricsapi.go`
Expected: 找到 processMetricsOf 函数

- [ ] **Step 2: 备份旧的 procstat 文件（验证后删除）**

Run: `ls -la internal/api/procstat*.go`
Expected: 列出 procstat_linux.go 和 procstat_other.go

- [ ] **Step 3: 修改 MetricsAPI 结构添加 Metrics 字段**

在 `internal/api/metricsapi.go` 中：

```go
import (
	"math"
	// ... 现有 imports
	"github.com/your-org/procmesh/internal/metrics"
)

type MetricsAPI struct {
	// ... 现有字段
	Metrics *metrics.Collector  // 新增：从 Agent 注入
}
```

- [ ] **Step 4: 修改 processMetricsOf 函数使用 Collector**

替换现有的 `processMetricsOf` 或相关函数：

```go
func processMetricsOf(c *metrics.Collector, inst process.Instance, now time.Time) *procmeshv1.ProcessMetrics {
	out := &procmeshv1.ProcessMetrics{
		InstanceId: inst.InstanceID,
		Pid:        int32(inst.PID),
	}

	// 计算 uptime
	if inst.StartedAt != nil && !inst.StartedAt.IsZero() {
		u := now.Sub(*inst.StartedAt).Seconds()
		if u < 0 {
			u = 0
		}
		out.UptimeSeconds = int64(u)
	}

	// 采集资源指标
	if c == nil {
		// Collector 未初始化
		out.CpuPercent = -1
		out.MemoryBytes = -1
		out.Note = "metrics collector unavailable"
		return out
	}

	pm, err := c.ProcessMetrics(inst.PID)
	if err != nil {
		out.CpuPercent = -1
		out.MemoryBytes = -1
		out.Note = fmt.Sprintf("metrics unavailable: %v", err)
		return out
	}

	out.CpuPercent = int32(math.Round(pm.CPUPercent))
	out.MemoryBytes = int64(pm.MemoryBytes)
	return out
}
```

- [ ] **Step 5: 修改使用 processMetricsOf 的地方传入 Metrics**

找到调用 `processMetricsOf` 的地方，传入 `m.Metrics`（假设 m 是 MetricsAPI）：

```go
// 示例
metrics := processMetricsOf(m.Metrics, instance, time.Now())
```

- [ ] **Step 6: 修改 Agent 传递 metrics 到 MetricsAPI**

在 `internal/agent/agent.go` 中，找到创建 MetricsAPI 的地方：

```go
metricsAPI := &api.MetricsAPI{
	// ... 现有字段
	Metrics: a.metrics,
}
```

- [ ] **Step 7: 删除旧的 procstat 文件**

```bash
git rm internal/api/procstat_linux.go internal/api/procstat_other.go
```

- [ ] **Step 8: 构建验证无编译错误**

Run: `go build ./internal/api`
Expected: 无错误

- [ ] **Step 9: 运行 api 包测试**

Run: `go test ./internal/api -v`
Expected: PASS

- [ ] **Step 10: 提交**

```bash
git add internal/api/metricsapi.go internal/agent/agent.go
git commit -m "feat(api): integrate real process metrics, remove old procstat files"
```

---

### Task 9: 实现磁盘保护联动（可选但推荐）

**Files:**
- Create: `internal/agent/disk_protect.go`
- Create: `internal/agent/disk_protect_test.go`

**Interfaces:**
- Consumes:
  - `Agent.metrics *metrics.Collector` from Task 6
  - `func (c *Collector) NodeMetrics() (*NodeMetrics, error)` from Task 5
- Produces: 磁盘使用率 >85% 时触发告警和保护措施

- [ ] **Step 1: 编写磁盘保护测试**

```go
// internal/agent/disk_protect_test.go
package agent

import (
	"testing"

	"github.com/your-org/procmesh/internal/metrics"
)

func TestCheckDiskUsage_Normal(t *testing.T) {
	node := &metrics.NodeMetrics{
		DiskPercent: 50.0,
	}

	action := determineDiskAction(node.DiskPercent)
	if action != diskActionNone {
		t.Errorf("expected no action for 50%%, got %v", action)
	}
}

func TestCheckDiskUsage_Warning(t *testing.T) {
	node := &metrics.NodeMetrics{
		DiskPercent: 86.0,
	}

	action := determineDiskAction(node.DiskPercent)
	if action != diskActionWarning {
		t.Errorf("expected warning for 86%%, got %v", action)
	}
}

func TestCheckDiskUsage_High(t *testing.T) {
	node := &metrics.NodeMetrics{
		DiskPercent: 92.0,
	}

	action := determineDiskAction(node.DiskPercent)
	if action != diskActionHigh {
		t.Errorf("expected high for 92%%, got %v", action)
	}
}

func TestCheckDiskUsage_Critical(t *testing.T) {
	node := &metrics.NodeMetrics{
		DiskPercent: 96.0,
	}

	action := determineDiskAction(node.DiskPercent)
	if action != diskActionCritical {
		t.Errorf("expected critical for 96%%, got %v", action)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/agent -v -run TestCheckDiskUsage`
Expected: FAIL - determineDiskAction 未定义

- [ ] **Step 3: 实现磁盘保护逻辑**

```go
// internal/agent/disk_protect.go
package agent

import (
	"log"
)

type diskAction int

const (
	diskActionNone diskAction = iota
	diskActionWarning
	diskActionHigh
	diskActionCritical
)

// determineDiskAction 根据磁盘使用率确定应采取的行动
func determineDiskAction(diskPercent float64) diskAction {
	switch {
	case diskPercent >= 95:
		return diskActionCritical
	case diskPercent >= 90:
		return diskActionHigh
	case diskPercent >= 85:
		return diskActionWarning
	default:
		return diskActionNone
	}
}

// checkDiskUsage 检查磁盘使用率并采取相应措施
// 应该在 Collector 的后台协程中定期调用，或在 Agent 主循环中调用
func (a *Agent) checkDiskUsage() {
	if a.metrics == nil {
		return
	}

	node, err := a.metrics.NodeMetrics()
	if err != nil {
		// 采集失败，不触发保护
		return
	}

	action := determineDiskAction(node.DiskPercent)

	switch action {
	case diskActionCritical:
		log.Printf("CRITICAL: disk usage at %.1f%%, pausing log writes", node.DiskPercent)
		// TODO: 停止新日志/metrics 写入
		// a.logmgr.Pause()
		// a.audit.EmitLocal("disk_critical", node.DiskPercent)

	case diskActionHigh:
		log.Printf("HIGH: disk usage at %.1f%%, aggressive cleanup", node.DiskPercent)
		// TODO: 积极删除旧日志
		// a.logmgr.AggressiveCleanup()
		// a.audit.EmitLocal("disk_high", node.DiskPercent)

	case diskActionWarning:
		log.Printf("WARNING: disk usage at %.1f%%", node.DiskPercent)
		// TODO: 发送告警
		// a.audit.EmitLocal("disk_warning", node.DiskPercent)

	case diskActionNone:
		// 正常，无需操作
	}
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/agent -v -run TestCheckDiskUsage`
Expected: PASS

- [ ] **Step 5: 在 Collector 采集成功后调用检查（可选实现）**

在 `internal/metrics/collector.go` 的 `collect()` 方法中，采集成功后可以触发回调：

```go
// 方案 A：在 Collector 中添加回调（更解耦）
type CollectorCallback func(node *NodeMetrics)

type Collector struct {
	// ... 现有字段
	onCollected CollectorCallback  // 采集成功后的回调
}

func (c *Collector) SetCallback(cb CollectorCallback) {
	c.mu.Lock()
	c.onCollected = cb
	c.mu.Unlock()
}

func (c *Collector) collect() {
	node, err := collectNode(c.dataDir)

	c.mu.Lock()
	c.node = node
	c.nodeErr = err
	c.lastUpdate = time.Now()
	callback := c.onCollected
	c.mu.Unlock()

	if err != nil {
		log.Printf("metrics collection failed: %v", err)
		return
	}

	// 调用回调
	if callback != nil && node != nil {
		callback(node)
	}
}
```

然后在 Agent.Start 中设置回调：

```go
a.metrics.SetCallback(func(node *metrics.NodeMetrics) {
	a.checkDiskUsage()
})
```

- [ ] **Step 6: 提交**

```bash
git add internal/agent/disk_protect.go internal/agent/disk_protect_test.go
git commit -m "feat(agent): add disk protection based on metrics"
```

---

### Task 10: 端到端测试和文档

**Files:**
- Create: `internal/agent/e2e_metrics_test.go`
- Modify: `README.md` (可选)
- Modify: `docs/v2-prd/v2-prd.md` (可选)

**Interfaces:**
- Consumes: 所有之前任务的组件
- Produces: 完整的端到端测试，验证整个流程

- [ ] **Step 1: 编写端到端测试**

```go
// internal/agent/e2e_metrics_test.go
package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	procmeshv1 "github.com/your-org/procmesh/proto/procmesh/v1"
	"connectrpc.com/connect"
)

func TestE2E_MetricsFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	// 1. 启动 Agent
	a, cleanup := setupTestAgent(t)
	defer cleanup()

	// 2. 等待 Collector 启动并采集数据
	time.Sleep(200 * time.Millisecond)

	// 3. 验证节点指标通过 Cluster 可见
	node, err := a.metrics.NodeMetrics()
	require.NoError(t, err)
	require.NotNil(t, node)

	assert.GreaterOrEqual(t, node.CPUPercent, 0.0)
	assert.LessOrEqual(t, node.CPUPercent, 100.0)
	assert.Greater(t, node.MemoryTotal, uint64(0))
	assert.Greater(t, node.DiskTotal, uint64(0))

	t.Logf("Node metrics collected: CPU=%.1f%% Memory=%.1f%% Disk=%.1f%%",
		node.CPUPercent, node.MemoryPercent, node.DiskPercent)

	// 4. 验证通过 API 可以获取节点指标
	if a.metricsAPI != nil {
		resp, err := a.metricsAPI.GetAgentMetrics(
			context.Background(),
			connect.NewRequest(&procmeshv1.GetAgentMetricsRequest{}))
		require.NoError(t, err)

		m := resp.Msg.Metrics
		assert.NotEqual(t, int32(-1), m.Resources.CpuPercent)
		assert.NotEqual(t, int32(-1), m.Resources.MemoryPercent)
		assert.NotEqual(t, int32(-1), m.Resources.DiskPercent)

		t.Logf("API returned: CPU=%d%% Memory=%d%% Disk=%d%%",
			m.Resources.CpuPercent, m.Resources.MemoryPercent, m.Resources.DiskPercent)
	}

	// 5. 启动一个测试进程并验证进程指标
	// TODO: 根据实际 process manager API 实现
	// 这里假设有 startTestProcess() 辅助函数
	// pid := startTestProcess(t, a)
	// pm, err := a.metrics.ProcessMetrics(pid)
	// require.NoError(t, err)
	// assert.Greater(t, pm.MemoryBytes, uint64(0))
}

func TestE2E_MetricsGracefulDegradation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	// 测试采集失败时的降级行为
	a, cleanup := setupTestAgent(t)
	defer cleanup()

	// 停止 Collector
	if a.metrics != nil {
		a.metrics.Stop()
	}

	// 尝试读取应该返回旧数据或错误，但不应 panic
	_, err := a.metrics.NodeMetrics()
	// 可能返回错误或最后一次缓存数据，但不应崩溃
	t.Logf("After stop, NodeMetrics returned: %v", err)
}
```

- [ ] **Step 2: 运行端到端测试**

Run: `go test ./internal/agent -v -run TestE2E_Metrics`
Expected: PASS

- [ ] **Step 3: 运行所有 metrics 测试验证覆盖率**

Run: `go test ./internal/metrics -cover`
Expected: coverage >= 80%

- [ ] **Step 4: 运行完整测试套件**

Run: `go test ./... -short`
Expected: PASS（所有快速测试通过）

- [ ] **Step 5: 在 Linux 和 macOS 上测试（如果可行）**

```bash
# Linux
GOOS=linux go test ./internal/metrics -v

# macOS
GOOS=darwin go test ./internal/metrics -v
```

Expected: 两个平台都通过

- [ ] **Step 6: 更新文档（可选）**

如果需要更新用户文档，在 README.md 或相关文档中添加：

```markdown
## 资源监控

ProcMesh Agent 自动采集以下资源指标：

### 节点级指标
- CPU 使用率（整机）
- 内存使用率和使用量
- 磁盘使用率（数据目录所在分区）

### 进程级指标
- 进程 CPU 使用率
- 进程内存使用（RSS）

指标每 5 秒采集一次，通过 Gossip 在集群中传播。

### 平台支持
- ✅ Linux（生产环境）
- ✅ macOS（开发环境）
- ❌ Windows（未来版本）

### 磁盘保护
当磁盘使用率超过阈值时，Agent 会自动采取保护措施：
- ≥85%: 告警
- ≥90%: 积极清理旧日志
- ≥95%: 停止新日志写入
```

- [ ] **Step 7: 提交**

```bash
git add internal/agent/e2e_metrics_test.go README.md
git commit -m "test: add e2e tests for metrics collection and update docs"
```

---

### Task 11: 最终验证和清理

**Files:**
- 验证所有测试
- 检查代码质量
- 运行完整构建

**Interfaces:**
- Consumes: 所有之前的任务
- Produces: 可发布的功能

- [ ] **Step 1: 运行完整测试套件**

```bash
go test ./... -v
```

Expected: 所有测试通过

- [ ] **Step 2: 检查测试覆盖率**

```bash
go test ./internal/metrics -coverprofile=coverage.out
go tool cover -func=coverage.out | grep total
```

Expected: total coverage >= 80%

- [ ] **Step 3: 运行 golangci-lint（如果配置）**

```bash
golangci-lint run ./internal/metrics/...
golangci-lint run ./internal/agent/...
golangci-lint run ./internal/cluster/...
golangci-lint run ./internal/api/...
```

Expected: 无错误或警告

- [ ] **Step 4: 构建所有二进制文件**

```bash
make bin
```

Expected: 成功构建 procmesh-agent, procmesh-shim, procmesh

- [ ] **Step 5: 手动验证（Linux）**

如果在 Linux 环境：

```bash
# 启动 Agent
./bin/procmesh-agent --config test-config.yaml

# 观察日志
tail -f /var/log/procmesh/agent.log | grep metrics

# 验证 Web UI 显示资源数据
curl http://localhost:18680/api/metrics
```

Expected: 看到真实的 CPU/Memory/Disk 数据，不是 -1

- [ ] **Step 6: 手动验证（macOS）**

如果在 macOS 环境：

```bash
# 启动 Agent
./bin/procmesh-agent --config test-config.yaml

# 验证日志
tail -f ~/Library/Logs/procmesh/agent.log | grep metrics
```

Expected: 看到真实的资源数据

- [ ] **Step 7: 验证磁盘保护（可选）**

如果实现了磁盘保护：

```bash
# 人为填充磁盘到 >85%
dd if=/dev/zero of=/tmp/bigfile bs=1M count=10000

# 观察日志
tail -f agent.log | grep "disk usage"
```

Expected: 看到告警日志

- [ ] **Step 8: 清理测试文件**

```bash
rm -f coverage.out /tmp/bigfile
```

- [ ] **Step 9: 最终提交**

```bash
git add .
git commit -m "feat: complete resource metrics implementation

- Add gopsutil v3.23.12 dependency
- Implement node and process metrics collection
- Integrate metrics collector into agent lifecycle
- Update cluster to use real resource data in gossip
- Update API to return real process metrics
- Add disk protection based on metrics
- Add comprehensive tests (80%+ coverage)
- Support Linux and macOS platforms

Closes #XXX"
```

- [ ] **Step 10: 推送并创建 PR（如果需要）**

```bash
git push origin feature/resource-metrics
# 使用 gh CLI 创建 PR
gh pr create --title "feat: Resource Metrics Implementation" --body "See commit message for details"
```

---

## 验证清单

完成所有任务后，验证以下内容：

- [ ] ✅ gopsutil 依赖已添加
- [ ] ✅ `internal/metrics` 包已创建，包含所有文件
- [ ] ✅ 单元测试覆盖率 >= 80%
- [ ] ✅ Agent 集成 Collector 并在启动时自动启动
- [ ] ✅ Cluster Gossip 传播真实的节点资源数据（不再是 -1）
- [ ] ✅ API 返回真实的进程资源数据（不再是 -1）
- [ ] ✅ 删除了旧的 `procstat_*.go` 文件
- [ ] ✅ 磁盘保护逻辑已实现（可选）
- [ ] ✅ 端到端测试通过
- [ ] ✅ Linux 和 macOS 平台测试通过
- [ ] ✅ 所有测试通过 `go test ./...`
- [ ] ✅ 构建成功 `make bin`
- [ ] ✅ 文档已更新（如需要）
- [ ] ✅ 代码已提交并推送

---

## 实施注意事项

1. **TDD 严格遵守**：每个任务都是先写测试，验证失败，再实现，验证通过
2. **频繁提交**：每个任务完成后立即提交，便于回滚和审查
3. **平台测试**：确保在 Linux 和 macOS 上都运行测试
4. **降级行为**：所有错误场景都要优雅处理，返回 -1 而不是 panic
5. **并发安全**：Collector 使用读写锁保护缓存，返回副本避免数据竞争
6. **依赖注入**：Collector 通过构造函数注入到 Agent、Cluster、API
7. **日志记录**：采集失败记录日志，但不阻塞核心功能
8. **覆盖率目标**：`internal/metrics` 包必须达到 80%+ 覆盖率

---

## 故障排查

### 测试失败

**问题：**`go test ./internal/metrics` 失败
**排查：**
1. 检查 gopsutil 是否正确安装：`go list -m github.com/shirou/gopsutil/v3`
2. 检查平台兼容性：`echo $GOOS`
3. 查看详细错误：`go test ./internal/metrics -v -run TestCollectNode`

### 集成问题

**问题：** Agent 启动后节点指标仍然是 -1
**排查：**
1. 检查 Agent.metrics 是否正确初始化：添加日志 `log.Printf("metrics collector: %v", a.metrics)`
2. 检查 Collector 是否启动：`a.metrics.NodeMetrics()` 是否返回错误
3. 检查数据目录路径：`ls -la $DATA_DIR`

### 平台差异

**问题：** macOS 测试失败但 Linux 通过
**排查：**
1. 检查是否使用了 Linux 特定的系统调用
2. 检查路径分隔符：使用 `filepath.Join` 而不是硬编码 `/`
3. 运行平台测试：`go test ./internal/metrics -v -run CrossPlatform`

---

## 参考资料

- [gopsutil 文档](https://github.com/shirou/gopsutil)
- [设计文档](docs/superpowers/specs/2026-08-16-resource-metrics-design.md)
- [CLAUDE.md §磁盘保护](CLAUDE.md)
- [Go Testing 最佳实践](https://go.dev/doc/tutorial/add-a-test)
