# Resource Metrics Implementation Design

**Date:** 2026-08-16  
**Status:** Approved  
**Author:** Claude Code

## Overview

实现 ProcMesh 的资源监控功能，包括节点级（整机 CPU/Memory/Disk）和进程级（进程 CPU/Memory）监控。当前所有平台返回 `-1`（unknown），本设计将提供真实的资源数据采集。

## Goals

1. **节点级监控**：采集整机 CPU、内存、磁盘使用率，通过 Gossip 传播
2. **进程级监控**：采集单个进程的 CPU 和内存使用，通过 API 返回
3. **跨平台支持**：Linux（生产）+ macOS（开发）
4. **性能优化**：后台定期采集（5 秒间隔），API 读取缓存，避免阻塞
5. **优雅降级**：采集失败时返回 `-1`，不影响核心功能

## Non-Goals

- Windows 平台支持（未来可扩展）
- 网络 I/O、文件句柄等其他指标（V1.1+）
- 历史指标存储（V1.1 Historical Metrics）
- 多磁盘/分区详细监控（仅监控数据目录所在分区）

## Background

当前状态分析：
- ✅ **protobuf 定义完整**：`api.proto` 已定义 `cpu_percent/memory_percent/disk_percent` 字段
- ✅ **Gossip 传播就绪**：`cluster.ResourceSummary` 结构已存在
- ❌ **采集逻辑缺失**：`summary.go:49-53` 硬编码返回 `-1`
- ❌ **macOS 不支持**：`procstat_other.go` 始终返回 `false`

Linux 的进程监控已通过 `/proc/{pid}/stat` 实现，但：
1. 不支持 macOS（无 `/proc` 文件系统）
2. 没有节点级监控逻辑

## Design

### Architecture

采用**集中式 Collector** 架构：

```
┌─────────────────────────────────────────────────────┐
│ Agent                                               │
│  ├─ Collector (metrics package)                    │
│  │   ├─ Background goroutine (5s interval)         │
│  │   ├─ Node metrics cache (CPU/Mem/Disk)         │
│  │   └─ Process metrics (on-demand)                │
│  ├─ Cluster LiveSource                             │
│  │   └─ reads node metrics → Gossip                │
│  └─ MetricsAPI                                      │
│      └─ reads process metrics → ConnectRPC         │
└─────────────────────────────────────────────────────┘
```

**职责划分：**
- `internal/metrics`：封装所有采集逻辑，提供统一接口
- `internal/cluster`：消费节点指标，通过 Gossip 传播
- `internal/api`：消费进程指标，通过 API 返回
- 使用 `gopsutil` 库实现跨平台采集

### Package Structure

```
internal/metrics/
├── collector.go      # Collector 主逻辑：后台协程、缓存管理、API
├── node.go           # 节点资源采集（CPU/Memory/Disk）
├── process.go        # 进程资源采集（CPU/Memory）
├── types.go          # 数据结构定义
├── collector_test.go # Collector 测试
├── node_test.go      # 节点采集测试
└── process_test.go   # 进程采集测试
```

### Data Structures

```go
// types.go
package metrics

import "time"

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
    PID           int       // 进程 ID
    CPUPercent    float64   // 进程 CPU 使用率 0-100（单核）
    MemoryBytes   uint64    // 进程内存（RSS）
    Timestamp     time.Time // 采集时间
}

// Collector 资源采集器
type Collector struct {
    interval   time.Duration      // 采集间隔（默认 5 秒）
    dataDir    string             // 数据目录路径（用于磁盘监控）
    cancel     context.CancelFunc // 停止信号
    mu         sync.RWMutex       // 保护缓存
    node       *NodeMetrics       // 缓存的节点指标
    nodeErr    error              // 最近一次采集错误
    lastUpdate time.Time          // 最后更新时间
}
```

### Core API

```go
// collector.go
package metrics

// New 创建 Collector（未启动）
func New(interval time.Duration) *Collector

// Start 启动后台采集协程
func (c *Collector) Start(ctx context.Context) error

// Stop 停止采集
func (c *Collector) Stop()

// NodeMetrics 获取缓存的节点指标（非阻塞）
func (c *Collector) NodeMetrics() (*NodeMetrics, error)

// ProcessMetrics 获取进程指标（实时采集，轻量）
func (c *Collector) ProcessMetrics(pid int) (*ProcessMetrics, error)
```

### Implementation Details

#### 1. Node Metrics Collection

```go
// node.go
func collectNode(dataDir string) (*NodeMetrics, error) {
    // CPU：采样 1 秒（阻塞）
    cpuPct, err := cpu.Percent(time.Second, false) // false = 总体
    if err != nil {
        return nil, fmt.Errorf("cpu percent: %w", err)
    }
    
    // Memory
    vmem, err := mem.VirtualMemory()
    if err != nil {
        return nil, fmt.Errorf("memory: %w", err)
    }
    
    // Disk：数据目录所在分区
    diskStat, err := disk.Usage(dataDir)
    if err != nil {
        return nil, fmt.Errorf("disk usage: %w", err)
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

**要点：**
- `cpu.Percent()` 阻塞 ~1 秒（需要采样窗口），在后台协程运行
- 磁盘监控仅针对数据目录所在分区（`/var/lib/procmesh` 或 `~/Library/Application Support/procmesh`）
- 返回 `float64` 百分比，调用方负责转换为 `int32`

#### 2. Process Metrics Collection

```go
// process.go
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

**要点：**
- 不阻塞，直接返回瞬时值
- 不缓存（进程随时可能退出）
- 进程不存在时返回 error

#### 3. Background Collection

```go
// collector.go
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
```

**要点：**
- 启动时立即采集一次（避免前 5 秒返回空数据）
- 使用读写锁（`sync.RWMutex`）保护缓存
- 返回副本防止数据竞争

### Integration

#### 1. Agent Initialization

```go
// internal/agent/agent.go
type Agent struct {
    // ... 现有字段
    metrics *metrics.Collector  // 新增
}

func (a *Agent) Start(ctx context.Context) error {
    // ... 现有启动逻辑
    
    // 启动 metrics collector
    a.metrics = metrics.New(5 * time.Second)
    a.metrics.SetDataDir(a.cfg.DataDir) // 设置数据目录路径
    if err := a.metrics.Start(ctx); err != nil {
        return fmt.Errorf("start metrics collector: %w", err)
    }
    
    // ... 其他组件启动
}

func (a *Agent) Stop() {
    if a.metrics != nil {
        a.metrics.Stop()
    }
    // ... 其他清理
}
```

#### 2. Cluster Integration

```go
// internal/cluster/livesource.go
type liveSource struct {
    // ... 现有字段
    metrics *metrics.Collector  // 从 Agent 传入
}

func (s *liveSource) Snapshot() LocalSnapshot {
    var res ResourceSummary
    
    if s.metrics == nil {
        // Collector 未初始化（降级模式）
        res = ResourceSummary{CPUPercent: -1, MemoryPercent: -1, DiskPercent: -1}
    } else {
        node, err := s.metrics.NodeMetrics()
        if err != nil {
            // 采集失败
            res = ResourceSummary{CPUPercent: -1, MemoryPercent: -1, DiskPercent: -1}
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
        // ... 其他字段
    }
}
```

#### 3. API Integration

```go
// internal/api/metricsapi.go
type MetricsAPI struct {
    // ... 现有字段
    Metrics *metrics.Collector  // 新增，从 Agent 传入
}

func processMetricsOf(c *metrics.Collector, inst process.Instance, now time.Time) *procmeshv1.ProcessMetrics {
    out := &procmeshv1.ProcessMetrics{
        InstanceId: inst.InstanceID,
        Pid:        int32(inst.PID),
    }
    
    if inst.StartedAt != nil && !inst.StartedAt.IsZero() {
        u := now.Sub(*inst.StartedAt).Seconds()
        if u < 0 {
            u = 0
        }
        out.UptimeSeconds = int64(u)
    }
    
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

**清理旧代码：**
- 删除 `internal/api/procstat_linux.go`（不再需要）
- 删除 `internal/api/procstat_other.go`（不再需要）
- 统一使用 Collector

### Error Handling

#### 错误场景分类

| 场景 | 处理策略 | 返回值 |
|------|---------|--------|
| gopsutil 初始化失败 | 记录日志，返回 error | `-1` (unknown) |
| 数据目录路径无效 | 记录日志，返回 error | `-1` (unknown) |
| 进程不存在 | 返回 error | `-1` + `"process not found"` |
| 部分指标失败 | 整个采集视为失败（保持一致性） | `-1` (all) |
| Collector 未启动 | 返回 error | `-1` (unknown) |

#### 降级行为

1. **采集失败**：返回 `-1`，不影响核心功能（进程管理、集群通信）
2. **Collector 未初始化**：Agent 降级模式（`Degraded() == true`）可能不启动 Collector
3. **日志策略**：
   - 启动时：`INFO: metrics collector started, interval=5s, dataDir=/var/lib/procmesh`
   - 采集失败：`WARN: metrics collection failed: cpu percent: permission denied`（每 5 秒最多一次）
   - 恢复正常：`INFO: metrics collection recovered`（首次成功时）

### Disk Protection Integration

根据 CLAUDE.md §磁盘保护规则，磁盘监控数据将联动磁盘保护机制：

```go
// internal/agent/disk_protect.go（新文件或集成到现有逻辑）
func (a *Agent) checkDiskUsage() {
    node, err := a.metrics.NodeMetrics()
    if err != nil {
        return  // 采集失败，不触发保护
    }
    
    switch {
    case node.DiskPercent >= 95:
        // 停止新日志/metrics 写入
        a.logmgr.Pause()
        a.audit.EmitLocal("disk_critical", node.DiskPercent)
    case node.DiskPercent >= 90:
        // 积极删除旧日志
        a.logmgr.AggressiveCleanup()
        a.audit.EmitLocal("disk_high", node.DiskPercent)
    case node.DiskPercent >= 85:
        // 告警
        a.audit.EmitLocal("disk_warning", node.DiskPercent)
    }
}
```

**调用时机：**
- 在 Collector 的后台协程中，每次采集成功后调用
- 或在 Agent 的主循环中定期检查（与采集频率同步）

## Testing Strategy

### Unit Tests

**覆盖率目标：80%+**（符合 CLAUDE.md 要求）

```go
// node_test.go
func TestCollectNode(t *testing.T)              // 正常采集，验证范围
func TestCollectNode_InvalidPath(t *testing.T)  // 无效路径，期望失败

// process_test.go
func TestCollectProcess_Self(t *testing.T)      // 采集当前进程
func TestCollectProcess_NotFound(t *testing.T)  // 不存在的进程

// collector_test.go
func TestCollector_BackgroundUpdate(t *testing.T)  // 验证后台定期更新
func TestCollector_Stop(t *testing.T)              // 验证优雅停止
func TestCollector_ConcurrentRead(t *testing.T)    // 验证并发安全
```

### Integration Tests

```go
// internal/agent/metrics_integration_test.go
func TestAgent_MetricsIntegration(t *testing.T) {
    // 启动测试 Agent
    a := setupTestAgent(t)
    defer a.Stop()
    
    // 等待 Collector 启动
    time.Sleep(100 * time.Millisecond)
    
    // 测试 API
    api := a.metricsAPI
    resp, err := api.GetAgentMetrics(context.Background(), 
        connect.NewRequest(&procmeshv1.GetAgentMetricsRequest{}))
    require.NoError(t, err)
    
    m := resp.Msg.Metrics
    assert.NotEqual(t, int32(-1), m.Resources.CpuPercent, "CPU should be available")
    assert.NotEqual(t, int32(-1), m.Resources.MemoryPercent, "Memory should be available")
    assert.NotEqual(t, int32(-1), m.Resources.DiskPercent, "Disk should be available")
}
```

### Platform Compatibility Tests

在 CI 中运行：
- **Linux**：`go test ./internal/metrics/...`
- **macOS**：`go test ./internal/metrics/...`

```go
func TestCollectNode_CrossPlatform(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping platform test in short mode")
    }
    
    tmpDir := t.TempDir()
    node, err := collectNode(tmpDir)
    require.NoError(t, err, "%s: collectNode should succeed", runtime.GOOS)
    
    t.Logf("%s metrics: CPU=%.1f%% Mem=%.1f%% Disk=%.1f%%",
        runtime.GOOS, node.CPUPercent, node.MemoryPercent, node.DiskPercent)
}
```

### Manual Verification

完成后需手动验证：
1. **Linux 生产环境**：启动 Agent，检查 Web UI 显示资源数据
2. **macOS 开发环境**：同上，验证跨平台一致性
3. **磁盘压力测试**：填充磁盘到 >85%，验证告警触发
4. **进程监控**：启动/停止业务进程，验证 CPU/Memory 实时更新

## Dependencies

新增外部依赖：

```go
// go.mod
require (
    github.com/shirou/gopsutil/v3 v3.23.12  // 跨平台资源监控
)
```

**gopsutil 选型理由：**
- 成熟稳定，被 Docker、Prometheus、Kubernetes 等广泛使用
- 统一 API，支持 Linux/macOS/Windows
- 轻量（~200KB），零额外系统依赖
- 活跃维护，Go 社区标准方案

## Migration Path

1. **Phase 1：实现 Collector**
   - 创建 `internal/metrics/` 包
   - 实现节点和进程采集逻辑
   - 编写单元测试（达到 80%+ 覆盖率）

2. **Phase 2：集成到 Agent**
   - 修改 `internal/agent/agent.go`，启动 Collector
   - 修改 `internal/cluster/livesource.go`，读取节点指标
   - 修改 `internal/api/metricsapi.go`，读取进程指标
   - 删除 `procstat_*.go` 旧代码

3. **Phase 3：磁盘保护联动**
   - 实现 `checkDiskUsage()` 逻辑
   - 集成到 Collector 或 Agent 主循环

4. **Phase 4：验证与发布**
   - 集成测试（Linux + macOS）
   - 手动验证（压力测试）
   - 更新文档

## Risks & Mitigation

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| gopsutil 跨平台兼容性问题 | 某些平台采集失败 | 单元测试覆盖两平台，降级返回 `-1` |
| CPU 采样阻塞影响性能 | Agent 响应延迟 | 后台协程采集，API 读缓存 |
| 磁盘监控路径错误 | 无法启动 Collector | 启动时验证路径，记录日志 |
| 进程频繁退出导致采集失败 | API 返回大量 `-1` | 正常行为，API 返回 `"process not found"` |

## Future Work (V1.1+)

- **历史指标存储**：保留最近 1 小时的指标数据，支持趋势图
- **网络 I/O 监控**：进程和节点级别的网络流量
- **文件句柄监控**：进程打开的文件数量
- **多磁盘详细监控**：返回所有挂载点的使用率列表
- **自定义采集间隔**：允许用户配置采集频率（1-60 秒）

## References

- CLAUDE.md §磁盘保护
- `internal/api/procstat_linux.go`（现有进程监控实现）
- `proto/procmesh/v1/api.proto`（ResourceSummary/ProcessMetrics 定义）
- [gopsutil Documentation](https://github.com/shirou/gopsutil)
