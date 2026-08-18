# 历史指标停写状态提示 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让历史指标写入遵守节点的 `emergency_percent` 与 `emergency_stop_writes` 配置，并在节点详情页显示真实停写状态、影响和恢复条件。

**Architecture:** Agent 层提供唯一的磁盘停写判定，并同时供指标记录器和本机节点摘要使用。摘要通过 Gossip 与 Node API 传播明确的暂停状态和阈值，前端只消费该状态，不根据取整后的磁盘百分比推断。

**Tech Stack:** Go 1.25、ConnectRPC/protobuf、memberlist JSON state、Vue 3 Composition API、TypeScript、TanStack Vue Query、i18next、Lucide。

**Spec:** `docs/superpowers/specs/2026-08-18-history-write-pause-status-design.md`

## Global Constraints

- 停写条件必须是 `emergency_stop_writes == true && disk_used_percent > emergency_percent`。
- 等于阈值时继续写入；开关关闭时无论磁盘占用多高都继续尝试写入。
- 指标缺口不回填，条件解除后在下一次采样周期自动恢复。
- UI 必须消费后端明确状态，不能根据取整后的 `disk_percent` 二次推断。
- 新 protobuf 字段只能追加，旧节点字段缺失时不显示提示。
- 前端不新增或修改自动化测试，这是用户明确指定的 TDD 例外。
- 前端可见文案必须同时提供中文和英文翻译。
- 最终必须通过 Go 测试、protobuf 生成校验、前端类型构建、lint、i18n 检查和浏览器桌面/移动验证。

## File Map

```text
internal/metrics/recorder.go             # 接受外部停写判定，移除固定 95% 行为
internal/metrics/recorder_test.go        # 记录器停写/恢复行为测试
internal/agent/disk_protect.go           # 基于 logmgr.Policy 的唯一历史停写判定
internal/agent/disk_protect_test.go      # 阈值、开关、边界测试
internal/agent/run.go                    # 将配置判定注入 Recorder 与 liveSource
internal/agent/summary.go                # 上报暂停状态和配置阈值
internal/agent/summary_test.go           # 精确值判定与摘要测试
internal/cluster/summary.go              # Gossip ResourceSummary 新字段
internal/cluster/codec_test.go           # JSON 状态往返测试
proto/procmesh/v1/api.proto              # ConnectRPC ResourceSummary 新字段
proto/procmesh/v1/api.pb.go              # make proto 生成
proto/procmesh/v1/procmeshv1connect/*    # make proto 生成（若生成器有变化）
internal/api/clusterapi.go               # ResourceSummary 到 protobuf 转换
internal/api/node_test.go                # Node API 字段传播测试
web/src/gen/procmesh/v1/api_pb.ts        # make proto-ts 生成
web/src/pages/clusterView.ts             # 前端资源视图映射新增字段
web/src/pages/NodeDetailPage.vue         # 历史区块警告提示
web/public/locales/en/common.json         # 英文提示
web/public/locales/zh/common.json         # 中文提示
web/src/types/i18n.d.ts                   # npm i18n:types 生成
```

---

### Task 1: 让历史记录器遵守磁盘保护配置

**Files:**
- Modify: `internal/metrics/recorder_test.go`
- Modify: `internal/metrics/recorder.go`
- Modify: `internal/agent/disk_protect_test.go`
- Modify: `internal/agent/disk_protect.go`
- Modify: `internal/agent/run.go`

**Interfaces:**
- Produces: `func historyWritesPaused(policy logmgr.Policy, diskPercent float64) bool`
- Produces: `metrics.Recorder.PauseWrites func(diskPercent float64) bool`
- Consumes: `agentcfg.Config.Disk logmgr.Policy`

- [ ] **Step 1: 写 Agent 纯函数失败测试**

在 `internal/agent/disk_protect_test.go` 增加表驱动测试：

```go
func TestHistoryWritesPaused(t *testing.T) {
	cases := []struct {
		name string
		policy logmgr.Policy
		used float64
		want bool
	}{
		{"below", logmgr.Policy{EmergencyPercent: 93, EmergencyStopWrites: true}, 92.9, false},
		{"equal", logmgr.Policy{EmergencyPercent: 93, EmergencyStopWrites: true}, 93, false},
		{"above", logmgr.Policy{EmergencyPercent: 93, EmergencyStopWrites: true}, 93.1, true},
		{"disabled", logmgr.Policy{EmergencyPercent: 93, EmergencyStopWrites: false}, 99, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := historyWritesPaused(tc.policy, tc.used); got != tc.want {
				t.Fatalf("got %t want %t", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认 RED**

Run: `go test ./internal/agent -run TestHistoryWritesPaused -count=1`

Expected: FAIL，`historyWritesPaused` 未定义。

- [ ] **Step 3: 实现唯一判定函数**

在 `internal/agent/disk_protect.go` 增加：

```go
func historyWritesPaused(policy logmgr.Policy, diskPercent float64) bool {
	return policy.EmergencyStopWrites && diskPercent > float64(policy.EmergencyPercent)
}
```

- [ ] **Step 4: 运行纯函数测试确认 GREEN**

Run: `go test ./internal/agent -run TestHistoryWritesPaused -count=1`

Expected: PASS。

- [ ] **Step 5: 写 Recorder 失败测试**

在 `internal/metrics/recorder_test.go` 增加三个行为测试，均使用内存 SampleStore：

```go
func TestRecorder_FlushUsesInjectedPauseDecision(t *testing.T) {
	mem := newMemSamples()
	r := metrics.NewRecorder(mem, "n1")
	r.DiskPercent = func() float64 { return 93.1 }
	r.PauseWrites = func(used float64) bool { return used > 93 }
	err := r.Flush(context.Background())
	if err == nil || !strings.Contains(err.Error(), "history writes paused") {
		t.Fatalf("err=%v", err)
	}
}

func TestRecorder_FlushWritesWhenPauseDecisionAllows(t *testing.T) {
	mem := newMemSamples()
	r := metrics.NewRecorder(mem, "n1")
	r.DiskPercent = func() float64 { return 99 }
	r.PauseWrites = func(float64) bool { return false }
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRecorder_FlushWithoutPauseDecisionDoesNotInventThreshold(t *testing.T) {
	r := metrics.NewRecorder(newMemSamples(), "n1")
	r.DiskPercent = func() float64 { return 100 }
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 6: 运行 Recorder 测试确认 RED**

Run: `go test ./internal/metrics -run 'TestRecorder_Flush(UsesInjectedPauseDecision|WritesWhenPauseDecisionAllows|WithoutPauseDecision)' -count=1`

Expected: FAIL，`Recorder.PauseWrites` 未定义，且旧固定 95% 行为与期望冲突。

- [ ] **Step 7: 实现 Recorder 注入点并接入生产配置**

在 `metrics.Recorder` 增加：

```go
PauseWrites func(diskPercent float64) bool
```

`flushLocked` 使用：

```go
pause := r.PauseWrites != nil && r.PauseWrites(disk)
if pause {
	writeErr = errcode.E(errcode.DEGRADED, "history writes paused by disk protection")
} else {
	// 保留现有 raw/downsample 写入流程
}
```

在 `agent.Run` 创建 Recorder 后注入：

```go
rec.PauseWrites = func(used float64) bool {
	return historyWritesPaused(cfg.Disk, used)
}
```

- [ ] **Step 8: 运行相关测试确认 GREEN**

Run: `go test ./internal/metrics ./internal/agent -count=1`

Expected: PASS。

---

### Task 2: 通过 Gossip 与 Node API 传播真实状态

**Files:**
- Modify: `internal/agent/summary_test.go`
- Modify: `internal/agent/summary.go`
- Modify: `internal/agent/run.go`
- Modify: `internal/cluster/codec_test.go`
- Modify: `internal/cluster/summary.go`
- Modify: `proto/procmesh/v1/api.proto`
- Generate: `proto/procmesh/v1/api.pb.go`
- Generate: `proto/procmesh/v1/procmeshv1connect/api.connect.go`
- Modify: `internal/api/node_test.go`
- Modify: `internal/api/clusterapi.go`
- Generate: `web/src/gen/procmesh/v1/api_pb.ts`

**Interfaces:**
- Produces: `cluster.ResourceSummary.HistoryWritesPaused bool`
- Produces: `cluster.ResourceSummary.HistoryPausePercent int`
- Produces protobuf fields `history_writes_paused = 4` and `history_pause_percent = 5`
- Consumes: Task 1 `historyWritesPaused`

- [ ] **Step 1: 写摘要、Codec 与 API 失败测试**

`internal/agent/summary_test.go` 使用可控 Collector 与策略断言精确值 93.1 在阈值 93 时上报暂停，摘要展示磁盘仍按现有规则取整：

```go
if !sum.Resources.HistoryWritesPaused || sum.Resources.HistoryPausePercent != 93 {
	t.Fatalf("resources=%+v", sum.Resources)
}
```

`internal/cluster/codec_test.go` 构造带两个新字段的 `NodeSummary`，经 `EncodeState` / `DecodeState` 后断言字段保持。

`internal/api/node_test.go` 构造 `cluster.ResourceSummary` 并断言：

```go
res := got.Msg.GetNode().GetResources()
if !res.GetHistoryWritesPaused() || res.GetHistoryPausePercent() != 93 {
	t.Fatalf("resources=%+v", res)
}
```

- [ ] **Step 2: 运行测试确认 RED**

Run: `go test ./internal/agent ./internal/cluster ./internal/api -run 'TestLiveSource_ReportsHistoryPause|TestEncodeState_KeepsHistoryPause|TestGetNode_KeepsHistoryPause' -count=1`

Expected: FAIL，新字段尚未定义。

- [ ] **Step 3: 扩展内部摘要并复用判定函数**

在 `cluster.ResourceSummary` 追加：

```go
HistoryWritesPaused bool `json:"history_writes_paused,omitempty"`
HistoryPausePercent int  `json:"history_pause_percent,omitempty"`
```

`liveSource` 增加 `diskPolicy logmgr.Policy`，在采集成功时使用未取整的 `node.DiskPercent` 调用 `historyWritesPaused`，并始终写入配置阈值。`agent.Run` 创建 `liveSource` 时传入 `cfg.Disk`。

- [ ] **Step 4: 追加 protobuf 字段并重新生成**

在 `ResourceSummary` 追加：

```proto
bool history_writes_paused = 4;
int32 history_pause_percent = 5;
```

Run: `make proto && make proto-ts`

Expected: Go 与 TypeScript 生成文件更新成功。

- [ ] **Step 5: 更新 API 转换**

`protoResources` 在资源已采集和未采集分支都复制新增字段：

```go
HistoryWritesPaused: r.HistoryWritesPaused,
HistoryPausePercent: int32(r.HistoryPausePercent),
```

- [ ] **Step 6: 运行传播测试确认 GREEN**

Run: `go test ./internal/agent ./internal/cluster ./internal/api -count=1`

Expected: PASS。

---

### Task 3: 节点详情页显示可恢复的停写提示

**Files:**
- Modify: `web/src/pages/clusterView.ts`
- Modify: `web/src/pages/NodeDetailPage.vue`
- Modify: `web/public/locales/en/common.json`
- Modify: `web/public/locales/zh/common.json`
- Generate: `web/src/types/i18n.d.ts`

**Interfaces:**
- Consumes: protobuf `ResourceSummary.historyWritesPaused` / `historyPausePercent`
- Produces: `ResourceView.historyWritesPaused: boolean`
- Produces: `ResourceView.historyPausePercent: number`

- [ ] **Step 1: 扩展前端资源视图映射**

`ResourceView` 增加：

```ts
historyWritesPaused: boolean;
historyPausePercent: number;
```

`mapNode` 同时兼容 camelCase 与 snake_case：

```ts
historyWritesPaused: toBool(pick(resources, "historyWritesPaused", "history_writes_paused")),
historyPausePercent: toNum(pick(resources, "historyPausePercent", "history_pause_percent")),
```

- [ ] **Step 2: 增加中英文文案**

在 `nodeDetail.historyPause` 下增加：

```json
{
  "title": "History metric writes paused",
  "description": "Disk usage is {{current}}%, above this node's configured emergency threshold of {{threshold}}%. ProcMesh paused history metric writes to protect data. Free space to {{threshold}}% or below to resume automatically. Charts only show data from before the pause; the gap is not backfilled."
}
```

中文对应：

```json
{
  "title": "历史指标写入已暂停",
  "description": "当前磁盘使用率为 {{current}}%，已超过该节点配置的紧急水位 {{threshold}}%。ProcMesh 已暂停写入历史指标以保护数据。释放空间至 {{threshold}}% 或以下后将自动恢复；图表仅显示暂停前的数据，缺口不会回填。"
}
```

- [ ] **Step 3: 实现警告条**

在 `NodeDetailPage.vue` 导入 `TriangleAlert`，并在历史标题行与加载/图表区域之间增加：

```vue
<div
  v-if="node.resources.historyWritesPaused && node.resources.historyPausePercent > 0"
  class="history-pause-notice"
  role="status"
  aria-live="polite"
>
  <TriangleAlert class="history-pause-icon" :size="20" aria-hidden="true" />
  <div>
    <strong>{{ t("nodeDetail.historyPause.title") }}</strong>
    <p>{{ t("nodeDetail.historyPause.description", {
      current: node.resources.diskPercent,
      threshold: node.resources.historyPausePercent,
    }) }}</p>
  </div>
</div>
```

样式使用 `--color-stale`、`--color-stale-fg` 和 `--color-border`，采用 `grid-template-columns: auto minmax(0, 1fr)`，正文 `overflow-wrap: anywhere`，数字 `font-variant-numeric: tabular-nums`。

- [ ] **Step 4: 生成类型并验证前端**

Run:

```bash
cd web
npm run i18n:types
npm run i18n:check
npm run lint
npm run build:check
```

Expected: 全部退出码 0。按用户要求不运行或新增 NodeDetailPage 前端测试。

- [ ] **Step 5: 浏览器验证**

在本机节点详情页验证：

- 真实未暂停状态不显示警告。
- 注入暂停状态进行视觉验证时，桌面宽度和 375px 宽度均无横向滚动、遮挡或文本截断。
- Accessibility tree 包含“历史指标写入已暂停”状态内容。
- 图标不单独进入可访问性树，状态信息不依赖颜色。

---

### Task 4: 全量回归与交付

**Files:**
- Verify only

**Interfaces:**
- Consumes: Tasks 1-3 的完整实现
- Produces: 可交付验证证据

- [ ] **Step 1: 运行 Go 全量测试**

Run: `go test ./... -count=1`

Expected: PASS，无失败包。

- [ ] **Step 2: 检查生成文件与工作区**

Run:

```bash
git diff --check
git status --short
```

Expected: 无 whitespace 错误；仅包含本计划范围内的文件。

- [ ] **Step 3: 复核需求**

逐项确认：自定义阈值、开关关闭、严格大于边界、远程状态传播、旧节点兼容、动态 UI 文案、桌面/移动可访问布局全部有实现或验证证据。
