# Q3 Historical Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Owner 本地保留 7 天历史指标（现有 5 条序列、两层分辨率），入口按需 RPC 到 Owner 读曲线；节点/进程详情页可切换 24h / 7d 趋势，缺口不插 0，不可达标 STALE 且禁止用 Gossip 摘要冒充曲线。

**Architecture:** Collector 仍 5 秒刷新即时缓存。`metrics.Recorder` 把有效样本按分钟聚合成 `raw_min`，每满 5 分钟再写成 `down_5m`。数据落在 Owner 现有 SQLite 同库新表 `metric_samples`，不进 Raft / Gossip。`MetricsService.GetNodeHistory` / `GetProcessHistory` 在入口 hop 到 Owner；读失败返回 `UNAVAILABLE`。Web 在已有 P5 详情页上画 SVG 分段折线。

**Tech Stack:** 现有 Go + `modernc.org/sqlite` + ConnectRPC + Vue3 + Vitest。不新增时序库、不新增 chart.js / echarts 大依赖、不新增二进制、不新增 REST 资源。

---

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- 强制 TDD：先红后绿；每任务先写失败测试
- `process` 不得 import `cluster`、`control`、`rpc`、`auth` 或 `web`
- `internal/metrics` 不得 import `cluster` / `control`
- Raft **禁止**写入历史点；Gossip **禁止**携带历史点或当曲线用
- 禁止用 Gossip 即时摘要冒充历史曲线
- 禁止把缺口插成 0；`-1` / 采集失败的分钟不写点
- `version.Protocol` 保持 `1`
- 错误码：`INVALID` / `NOT_FOUND` / `DENIED` / `UNAVAILABLE` / `DEGRADED`
- 磁盘 `>95%` 停写历史（读继续）；`>90%` 先删 `down_5m` 最老行
- 所有 Mutation 必须带非空 `operation_id`（本阶段 History RPC 是只读，不生成 mutation）
- 生成的 proto Go / TS 文件禁止手改；改完 proto 必须 `make proto` 与 `make proto-ts`
- 测试与代码同目录；`internal/metrics` 历史读写与降采样必须有测试；`internal/store` / `internal/process` 覆盖率保持 ≥ 80%
- 文档与计划用中文；API 错误消息用英文
- 新文案进 `web/public/locales/{en,zh}`，跑 `npm run i18n:check`
- 本阶段不做：Alert、Backup、Network/Load/FD/IO 采集、集群时序库、Overview 用历史替换 Gossip 百分比
- 工作目录是本 worktree，提交写在 `feat/q3-historical-metrics`

## 规格解读（Q3 边界）

来源：`docs/superpowers/specs/2026-08-16-v1.1-architecture-design.md` §8、§10、§11、§12 Q3、§13、§14、§15；PRD §59–60、§89 Historical Metrics。冲突以架构 spec 为准。V1.0 合同：`docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。P5 计划明确「V1.0 不做历史指标」——本阶段补上，不重做 P5 登录/Overview/启停。

1. **只存已有 5 序列：** 节点 `cpu_percent` / `memory_percent` / `disk_percent`；进程 `cpu_percent` / `memory_bytes`。不采集 Network / Load / FD / IO。
2. **两层：** `raw_min` 保留 24h；`down_5m` 保留 7 天。CPU/Mem **均值**；Disk **窗口最大**。两层都持续写：每分钟写 `raw_min`，每满 5 分钟把该窗的 `raw_min` 聚成一点 `down_5m`（这样 range>24h 的单层查询仍覆盖最近窗口）。
3. **权威：** 历史只在 Owner 本地 SQLite。入口不得缓存成权威，不得把 Gossip 摘要画成曲线。
4. **读：** `resolution` 省略时 range ≤ 24h 用 `raw_min`，否则 `down_5m`。非 Owner hop；不可达 `UNAVAILABLE`，UI STALE。
5. **磁盘：** `>95%` 跳过 insert（Agent 侧记 DEGRADED 语义，读继续）；`>90%` 每次 flush 先删最多 256 行最老 `down_5m`。
6. **进程消失：** 不再写新点；旧点按 TTL 删。
7. **P5 页面合同：** Overview 计数与资源百分比仍用 Gossip 即时摘要。趋势只出现在 Node / Process **详情页**。STALE 禁止绿色。视觉 token 沿用 P5（`--bg #F7F7F8`、`--accent #10A37F` 不作状态绿）。
8. **可演示：** 节点/进程页 24h 与 7d 曲线，缺口不插 0。

## File map

```text
internal/store/schema.sql
internal/store/metrics.go                 # 新建
internal/store/metrics_test.go            # 新建
internal/metrics/history.go               # 新建
internal/metrics/history_test.go          # 新建
internal/metrics/recorder.go              # 新建
internal/metrics/recorder_test.go         # 新建
proto/procmesh/v1/api.proto
internal/api/proto_gen_test.go
internal/api/metricsapi.go
internal/api/metricsapi_test.go
internal/api/metrics.go
internal/api/metrics_test.go
internal/api/server.go
internal/agent/run.go
internal/agent/rpc.go
internal/cli/root.go
internal/cli/client.go
internal/cli/metrics.go                   # 新建
internal/cli/root_test.go
web/src/lib/rpc.ts
web/src/lib/historyChart.ts               # 新建
web/src/lib/historyChart.test.ts          # 新建
web/src/components/HistoryChart.vue       # 新建
web/src/components/HistoryChart.test.ts   # 新建
web/src/pages/NodeDetailPage.vue
web/src/pages/NodeDetailPage.test.ts
web/src/pages/ProcessDetailPage.vue
web/src/pages/ProcessDetailPage.test.ts
web/public/locales/en/common.json
web/public/locales/zh/common.json
web/e2e/history.spec.ts                   # 新建
internal/agent/q3_accept_test.go          # 新建
docs/superpowers/plans/2026-08-16-v1.1.md
```

生成（改 proto 后执行，不要手改）：`proto/procmesh/v1/api.pb.go`、`proto/procmesh/v1/procmeshv1connect/api.connect.go`、`web/src/gen/procmesh/v1/api_pb.ts`

---

## 本阶段锁定的模型

### 序列与层

```go
package metrics

const (
	LayerRawMin = "raw_min"
	LayerDown5m = "down_5m"

	SeriesNodeCPU  = "node.cpu_percent"
	SeriesNodeMem  = "node.memory_percent"
	SeriesNodeDisk = "node.disk_percent"
	SeriesProcCPU  = "process.cpu_percent"
	SeriesProcMem  = "process.memory_bytes"

	RawMinRetention = 24 * time.Hour
	Down5mRetention = 7 * 24 * time.Hour
	FlushDeleteCap  = 256
)

func MinuteUnix(t time.Time) int64 { return t.UTC().Truncate(time.Minute).Unix() }
func FiveMinUnix(t time.Time) int64 { return t.UTC().Truncate(5 * time.Minute).Unix() }

func SelectLayer(since, until time.Time) string {
	if !until.After(since) {
		return LayerRawMin
	}
	if until.Sub(since) <= 24*time.Hour {
		return LayerRawMin
	}
	return LayerDown5m
}

type AggKind int

const (
	AggMean AggKind = iota
	AggMax
)

func KindOf(series string) AggKind {
	if series == SeriesNodeDisk {
		return AggMax
	}
	return AggMean
}

func ValidSample(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

func Aggregate(kind AggKind, values []float64) (float64, bool)
```

`Aggregate`：过滤后空切片 → `(0, false)`。`AggMean` 算术平均；`AggMax` 取最大。

查询默认：`until<=0` → `now`；`since<=0` → `until-24h`。显式 `resolution` 只能是 `""` / `raw_min` / `down_5m`，其它 → `INVALID`。

### 表

```sql
CREATE TABLE IF NOT EXISTS metric_samples (
    series TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    layer TEXT NOT NULL,
    ts_unix INTEGER NOT NULL,
    value REAL NOT NULL,
    PRIMARY KEY (series, subject_id, layer, ts_unix)
);
CREATE INDEX IF NOT EXISTS metric_samples_query
    ON metric_samples(subject_id, layer, series, ts_unix);
```

`subject_id`：节点序列用 `node_id`；进程序列用稳定 `process_id`（不是 name，不是 instance_id）。

### Store API

```go
package store

type MetricSample struct {
	Series    string
	SubjectID string
	Layer     string
	TSUnix    int64
	Value     float64
}

func (s *Store) InsertMetricSamples(ctx context.Context, samples []MetricSample) error
// 空切片 no-op。使用 INSERT OR REPLACE。单事务。

func (s *Store) ListMetricSamples(ctx context.Context, series, subjectID, layer string, fromUnix, toUnix int64) ([]MetricSample, error)
// 含端点；按 ts_unix ASC。series 空 → INVALID 语义由调用方保证，本函数要求三键非空否则 error。

func (s *Store) DeleteMetricSamplesBefore(ctx context.Context, layer string, tsUnix int64) (int64, error)
// DELETE WHERE layer=? AND ts_unix < ?

func (s *Store) DeleteOldestMetricSamples(ctx context.Context, layer string, limit int) (int64, error)
// 按 ts_unix ASC 最多删 limit 行。limit<=0 当 256。

func (s *Store) CountMetricSamples(ctx context.Context) (int64, error)
```

### Recorder

```go
type ProcessRef struct {
	ProcessID string
	PID       int
}

type SampleStore interface {
	InsertMetricSamples(ctx context.Context, samples []store.MetricSample) error
	ListMetricSamples(ctx context.Context, series, subjectID, layer string, fromUnix, toUnix int64) ([]store.MetricSample, error)
	DeleteMetricSamplesBefore(ctx context.Context, layer string, tsUnix int64) (int64, error)
	DeleteOldestMetricSamples(ctx context.Context, layer string, limit int) (int64, error)
	CountMetricSamples(ctx context.Context) (int64, error)
}

type Recorder struct {
	Store          SampleStore
	NodeID         string
	CollectNode    func() (*NodeMetrics, error)
	CollectProcess func(pid int) (*ProcessMetrics, error)
	ListProcesses  func() []ProcessRef
	DiskPercent    func() float64
	Now            func() time.Time
}

func NewRecorder(store SampleStore, nodeID string) *Recorder
func (r *Recorder) Sample(ctx context.Context) error
func (r *Recorder) Flush(ctx context.Context) error
func (r *Recorder) Start(ctx context.Context) error // 5s ticker：Sample；分钟翻转时 Flush
func (r *Recorder) Stop()
func (r *Recorder) Rows() int64 // CountMetricSamples，失败当 0
```

`Sample`：把当前有效节点/进程样本推进「当前分钟」内存桶。分钟变化时先 `Flush` 旧桶。采集失败或 `ValidSample==false` 的 5s 点丢弃。

`Flush`：

1. `DiskPercent()>=95` → 不 insert、不写 down_5m；仍执行 prune（删过期行）。返回 `errcode.E(errcode.DEGRADED, "disk usage at or above 95 percent; history writes paused")`。
2. `DiskPercent()>=90` → `DeleteOldestMetricSamples(LayerDown5m, 256)`。
3. 每个非空桶 `Aggregate` 成功则 insert `raw_min`。
4. 若被 flush 的分钟 `MinuteUnix%300==240`（即该分钟是 5 分钟窗的最后一分钟，例如 :04/:09），对每个 (series,subject) 读该窗 5 个 `raw_min`（`FiveMinUnix` 到 `+299`），`Aggregate` 后 insert `down_5m`（窗内 0 个有效 raw 点则不写）。
5. `DeleteMetricSamplesBefore(raw_min, now-24h)` 与 `DeleteMetricSamplesBefore(down_5m, now-7d)`。
6. 清空已 flush 的桶。

### Proto（追加到现有 `MetricsService`）

```protobuf
message MetricPoint {
  int64 ts_unix = 1;
  double value = 2;
}

message MetricSeries {
  string name = 1;   // cpu_percent | memory_percent | disk_percent | memory_bytes
  string layer = 2;  // raw_min | down_5m
  repeated MetricPoint points = 3; // ASC；缺口省略
}

message GetNodeHistoryRequest {
  string node_id = 1;
  int64 since_unix = 2;
  int64 until_unix = 3;
  string resolution = 4; // empty | raw_min | down_5m
}

message GetNodeHistoryResponse {
  string node_id = 1;
  string layer = 2;
  repeated MetricSeries series = 3;
}

message GetProcessHistoryRequest {
  string id_or_name = 1;
  int64 since_unix = 2;
  int64 until_unix = 3;
  string resolution = 4;
}

message GetProcessHistoryResponse {
  string process_id = 1;
  string layer = 2;
  repeated MetricSeries series = 3;
}
```

`MetricsService` 增加：

```protobuf
rpc GetNodeHistory(GetNodeHistoryRequest) returns (GetNodeHistoryResponse);
rpc GetProcessHistory(GetProcessHistoryRequest) returns (GetProcessHistoryResponse);
```

响应 `series.name` 用短名（去掉 `node.` / `process.` 前缀），与即时字段对齐。

### API 行为

`GetNodeHistory`：

1. `node_id` 空 → `INVALID` `node_id is required`
2. `requirePerm(..., auth.PermClusterRead, "", false, true)`
3. hop：`Router.Resolve(ctx, "", "", node_id)`（`ownerAgentID=node_id`）。非本机则 `Forward.Metrics` 转发同一请求；拨号/调用失败 → `UNAVAILABLE`（`mapForwardErr` / `rpc.MapDialError`）
4. 本机：规范化 since/until/layer；查 `node.cpu_percent` / `node.memory_percent` / `node.disk_percent`；缺行就是缺口
5. `:18683` `LocalOnly` 不二次 hop

`GetProcessHistory`：与 `GetProcessMetrics` 相同 hop + `process.read` + `authorizeProcessSpec`；本机 `Mgr.Resolve` 得 `process_id`；查 `process.cpu_percent` / `process.memory_bytes`。未知进程 `NOT_FOUND`。

`MetricsAPI` 增加字段：`Store *store.Store`（读历史）。写路径不走 API。

### 图表缺口算法（前后端同语义）

```ts
export type ChartPoint = { t: number; v: number };

export function splitSegments(points: ChartPoint[], stepSec: number): ChartPoint[][] {
  // stepSec: raw_min=60, down_5m=300
  // 若 next.t - prev.t > stepSec * 1.5，断开
  // 禁止插入 {t, v:0}
}
```

---

### Task 1: Store `metric_samples`

**Files:**
- Modify: `internal/store/schema.sql`
- Create: `internal/store/metrics.go`
- Create: `internal/store/metrics_test.go`
- Modify: `internal/store/store_test.go`（打开已有库后再 Open，新表必须存在——`CREATE TABLE IF NOT EXISTS` 已覆盖）

**Interfaces:**
- Consumes: 现有 `Store.db`、`Open`/`applySchema`
- Produces: 上文 `MetricSample` 与 5 个方法

- [ ] **Step 1: 写失败测试**

`internal/store/metrics_test.go`：

```go
package store_test

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMetricSamples_InsertListRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	err := s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 100, Value: 10},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 160, Value: 20},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 220, Value: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListMetricSamples(ctx, "node.cpu_percent", "n1", "raw_min", 160, 220)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TSUnix != 160 || got[0].Value != 20 || got[1].TSUnix != 220 {
		t.Fatalf("%+v", got)
	}
}

func TestMetricSamples_ReplaceSamePrimaryKey(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.disk_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 60, Value: 40},
	})
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.disk_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 60, Value: 55},
	})
	got, _ := s.ListMetricSamples(ctx, "node.disk_percent", "n1", "raw_min", 0, 1000)
	if len(got) != 1 || got[0].Value != 55 {
		t.Fatalf("%+v", got)
	}
}

func TestMetricSamples_DoesNotInventMissingMinutes(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 0, Value: 1},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 120, Value: 2},
	})
	got, _ := s.ListMetricSamples(ctx, "node.cpu_percent", "n1", "raw_min", 0, 120)
	if len(got) != 2 {
		t.Fatalf("gap must remain a gap: %+v", got)
	}
	for _, p := range got {
		if p.TSUnix == 60 {
			t.Fatal("must not invent ts=60")
		}
		if p.Value == 0 && p.TSUnix != 0 {
			t.Fatal("must not insert 0 for gap")
		}
	}
}

func TestMetricSamples_DeleteBeforeAndOldest(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "down_5m", TSUnix: 10, Value: 1},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "down_5m", TSUnix: 20, Value: 2},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "down_5m", TSUnix: 30, Value: 3},
		{Series: "node.cpu_percent", SubjectID: "n1", Layer: "raw_min", TSUnix: 10, Value: 9},
	})
	n, err := s.DeleteMetricSamplesBefore(ctx, "down_5m", 20)
	if err != nil || n != 1 {
		t.Fatalf("before n=%d err=%v", n, err)
	}
	n, err = s.DeleteOldestMetricSamples(ctx, "down_5m", 1)
	if err != nil || n != 1 {
		t.Fatalf("oldest n=%d err=%v", n, err)
	}
	got, _ := s.ListMetricSamples(ctx, "node.cpu_percent", "n1", "down_5m", 0, 100)
	if len(got) != 1 || got[0].TSUnix != 30 {
		t.Fatalf("%+v", got)
	}
	raw, _ := s.ListMetricSamples(ctx, "node.cpu_percent", "n1", "raw_min", 0, 100)
	if len(raw) != 1 {
		t.Fatalf("raw must be untouched: %+v", raw)
	}
}

func TestMetricSamples_CountAndEmptyInsert(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.InsertMetricSamples(ctx, nil); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountMetricSamples(ctx)
	if err != nil || n != 0 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	_ = s.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: "process.cpu_percent", SubjectID: "p1", Layer: "raw_min", TSUnix: 1, Value: 3},
	})
	n, _ = s.CountMetricSamples(ctx)
	if n != 1 {
		t.Fatalf("count=%d", n)
	}
}

func TestMetricSamples_ListRequiresKeys(t *testing.T) {
	s := openStore(t)
	if _, err := s.ListMetricSamples(context.Background(), "", "n1", "raw_min", 0, 1); err == nil {
		t.Fatal("empty series must error")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store -run TestMetricSamples_ -count=1`

Expected: FAIL（符号不存在）

- [ ] **Step 3: 加表并实现 5 个方法**

`schema.sql` 追加上文 DDL。`InsertMetricSamples` 单事务 `INSERT OR REPLACE`。`DeleteOldestMetricSamples`：

```sql
DELETE FROM metric_samples WHERE rowid IN (
  SELECT rowid FROM metric_samples WHERE layer = ? ORDER BY ts_unix ASC LIMIT ?
)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store -count=1`

Expected: PASS。覆盖率不被破坏。

- [ ] **Step 5: Commit**

```bash
git add internal/store/schema.sql internal/store/metrics.go internal/store/metrics_test.go
git commit -m "$(cat <<'EOF'
feat(store): add metric_samples table and history queries

EOF
)"
```

---

### Task 2: 聚合函数 + Recorder

**Files:**
- Create: `internal/metrics/history.go`
- Create: `internal/metrics/history_test.go`
- Create: `internal/metrics/recorder.go`
- Create: `internal/metrics/recorder_test.go`

**Interfaces:**
- Consumes: Task 1 `store.MetricSample` / `SampleStore`；现有 `NodeMetrics` / `ProcessMetrics`
- Produces: 上文 `MinuteUnix` / `FiveMinUnix` / `SelectLayer` / `Aggregate` / `KindOf` / `ValidSample` / `Recorder`

本任务 **不要** 改 Collector 的 5s 即时路径，不要 import `cluster`/`control`。

- [ ] **Step 1: 写失败测试**

`history_test.go`：

```go
func TestMinuteAndFiveMinUnix(t *testing.T) {
	tm := time.Date(2026, 8, 16, 12, 7, 42, 0, time.UTC)
	if metrics.MinuteUnix(tm) != tm.Truncate(time.Minute).Unix() {
		t.Fatal("minute")
	}
	if metrics.FiveMinUnix(tm) != time.Date(2026, 8, 16, 12, 5, 0, 0, time.UTC).Unix() {
		t.Fatal("five")
	}
}

func TestSelectLayer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	if metrics.SelectLayer(now.Add(-24*time.Hour), now) != metrics.LayerRawMin {
		t.Fatal("24h inclusive uses raw_min")
	}
	if metrics.SelectLayer(now.Add(-24*time.Hour-time.Second), now) != metrics.LayerDown5m {
		t.Fatal(">24h uses down_5m")
	}
}

func TestAggregate_MeanAndMaxAndEmpty(t *testing.T) {
	v, ok := metrics.Aggregate(metrics.AggMean, []float64{10, 20, 30})
	if !ok || v != 20 {
		t.Fatalf("mean %v %v", v, ok)
	}
	v, ok = metrics.Aggregate(metrics.AggMax, []float64{10, 40, 30})
	if !ok || v != 40 {
		t.Fatalf("max %v %v", v, ok)
	}
	if _, ok = metrics.Aggregate(metrics.AggMean, nil); ok {
		t.Fatal("empty")
	}
	if metrics.KindOf(metrics.SeriesNodeDisk) != metrics.AggMax {
		t.Fatal("disk max")
	}
	if metrics.KindOf(metrics.SeriesNodeCPU) != metrics.AggMean {
		t.Fatal("cpu mean")
	}
	if metrics.ValidSample(-1) || metrics.ValidSample(math.NaN()) {
		t.Fatal("invalid")
	}
}
```

`recorder_test.go` 用内存 fake store（map + 切片，实现 `SampleStore`）：

```go
func TestRecorder_FlushWritesMinuteMeanAndSkipsInvalid(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 10 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 10, MemoryPercent: 20, DiskPercent: 30}, nil
	}
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 30, MemoryPercent: 20, DiskPercent: 50}, nil
	}
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: -1}, errors.New("fail")
	}
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	if err := r.Sample(ctx); err != nil { // 分钟翻转触发 Flush
		t.Fatal(err)
	}
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerRawMin, 0, 1<<30)
	if len(got) != 1 || got[0].Value != 20 {
		t.Fatalf("cpu mean want 20 got %+v", got)
	}
	disk, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeDisk, "n1", metrics.LayerRawMin, 0, 1<<30)
	if len(disk) != 1 || disk[0].Value != 50 {
		t.Fatalf("disk max want 50 got %+v", disk)
	}
}

func TestRecorder_FailedMinuteWritesNoPoint(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 10 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) { return nil, errors.New("down") }
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	_ = r.Sample(ctx)
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	_ = r.Sample(ctx)
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerRawMin, 0, 1<<30)
	if len(got) != 0 {
		t.Fatalf("failed minute must leave a gap: %+v", got)
	}
}

func TestRecorder_DownsampleFiveRawMin(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	r := metrics.NewRecorder(mem, "n1")
	r.DiskPercent = func() float64 { return 10 }
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	// 10:00–10:04 每分钟一个 raw 点，最后一分钟 flush 应写 down_5m @ 10:00
	base := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		now := base.Add(time.Duration(i)*time.Minute + 10*time.Second)
		r.Now = func() time.Time { return now }
		cpu := float64(10 + i*10)
		r.CollectNode = func() (*metrics.NodeMetrics, error) {
			return &metrics.NodeMetrics{CPUPercent: cpu, MemoryPercent: 1, DiskPercent: float64(i + 1)}, nil
		}
		if err := r.Sample(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r.Now = func() time.Time { return base.Add(5 * time.Minute) }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 1, MemoryPercent: 1, DiskPercent: 1}, nil
	}
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerDown5m, 0, 1<<30)
	if len(got) != 1 || got[0].TSUnix != base.Unix() || got[0].Value != 30 {
		t.Fatalf("down_5m cpu want 30 @10:00 got %+v", got)
	}
	disk, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeDisk, "n1", metrics.LayerDown5m, 0, 1<<30)
	if len(disk) != 1 || disk[0].Value != 5 {
		t.Fatalf("down_5m disk max want 5 got %+v", disk)
	}
}

func TestRecorder_Disk95SkipsInsert(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 96 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 10, MemoryPercent: 10, DiskPercent: 10}, nil
	}
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	_ = r.Sample(ctx)
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	err := r.Sample(ctx)
	if err == nil || !strings.Contains(err.Error(), "DEGRADED") {
		t.Fatalf("want DEGRADED, got %v", err)
	}
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerRawMin, 0, 1<<30)
	if len(got) != 0 {
		t.Fatalf("95%% must not insert: %+v", got)
	}
}

func TestRecorder_Disk90DeletesOldestDown5m(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	_ = mem.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerDown5m, TSUnix: 100, Value: 1},
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerDown5m, TSUnix: 200, Value: 2},
	})
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 91 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 10, MemoryPercent: 10, DiskPercent: 10}, nil
	}
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	_ = r.Sample(ctx)
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	if err := r.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerDown5m, 0, 1<<30)
	if len(got) != 1 || got[0].TSUnix != 200 {
		t.Fatalf("oldest down_5m must go: %+v", got)
	}
}

func TestRecorder_ProcessSamplesUseProcessID(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 10, 0, 10, 0, time.UTC)
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 10 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) {
		return &metrics.NodeMetrics{CPUPercent: 1, MemoryPercent: 1, DiskPercent: 1}, nil
	}
	r.ListProcesses = func() []metrics.ProcessRef {
		return []metrics.ProcessRef{{ProcessID: "proc-1", PID: 4242}}
	}
	r.CollectProcess = func(pid int) (*metrics.ProcessMetrics, error) {
		if pid != 4242 {
			t.Fatalf("pid %d", pid)
		}
		return &metrics.ProcessMetrics{PID: pid, CPUPercent: 8, MemoryBytes: 4096}, nil
	}
	_ = r.Sample(ctx)
	now = time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC)
	_ = r.Sample(ctx)
	got, _ := mem.ListMetricSamples(ctx, metrics.SeriesProcMem, "proc-1", metrics.LayerRawMin, 0, 1<<30)
	if len(got) != 1 || got[0].Value != 4096 {
		t.Fatalf("%+v", got)
	}
}

func TestRecorder_PrunesExpiredLayers(t *testing.T) {
	ctx := context.Background()
	mem := newMemSamples()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	_ = mem.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerRawMin, TSUnix: now.Add(-25 * time.Hour).Unix(), Value: 1},
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerDown5m, TSUnix: now.Add(-8 * 24 * time.Hour).Unix(), Value: 2},
	})
	r := metrics.NewRecorder(mem, "n1")
	r.Now = func() time.Time { return now }
	r.DiskPercent = func() float64 { return 10 }
	r.CollectNode = func() (*metrics.NodeMetrics, error) { return nil, errors.New("skip") }
	r.ListProcesses = func() []metrics.ProcessRef { return nil }
	if err := r.Flush(ctx); err != nil && !strings.Contains(err.Error(), "DEGRADED") {
		// flush with empty buckets is ok
	}
	raw, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerRawMin, 0, now.Unix())
	down, _ := mem.ListMetricSamples(ctx, metrics.SeriesNodeCPU, "n1", metrics.LayerDown5m, 0, now.Unix())
	if len(raw) != 0 || len(down) != 0 {
		t.Fatalf("ttl prune failed raw=%+v down=%+v", raw, down)
	}
}
```

`newMemSamples` 放在测试文件：实现 `SampleStore`，行为与 SQL 版一致（含 oldest delete）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/metrics -run 'TestMinute|TestSelectLayer|TestAggregate_|TestRecorder_' -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 history.go 与 recorder.go**

桶 key：`series + "\x00" + subjectID`。`Start` 用 `time.NewTicker(5*time.Second)`，可测路径以 `Sample`/`Flush` 为准。`CollectProcess==nil` 或 `PID<=0` 跳过该进程。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/metrics -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/metrics
git commit -m "$(cat <<'EOF'
feat(metrics): aggregate minute samples and downsample to 5m

EOF
)"
```

---

### Task 3: Proto History RPC + 生成代码

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Generate: `proto/procmesh/v1/api.pb.go`
- Generate: `proto/procmesh/v1/procmeshv1connect/api.connect.go`
- Generate: `web/src/gen/procmesh/v1/api_pb.ts`
- Modify: `internal/api/proto_gen_test.go`
- Modify: `internal/api/metricsapi.go`（本任务只加方法桩，返回 `unimplemented()`）
- Modify: `internal/api/metricsapi_test.go`（`fakeMetricsClient` 补两个方法，满足接口编译）

**Interfaces:**
- Consumes: 现有 `MetricsService`
- Produces: `GetNodeHistory` / `GetProcessHistory` 消息与 RPC；handler 本任务回 `not implemented`

- [ ] **Step 1: 写失败测试**

`proto_gen_test.go` 追加：

```go
func TestProto_Q3HistoryRPCsGenerated(t *testing.T) {
	if procmeshv1connect.MetricsServiceGetNodeHistoryProcedure == "" {
		t.Fatal("missing GetNodeHistory")
	}
	if procmeshv1connect.MetricsServiceGetProcessHistoryProcedure == "" {
		t.Fatal("missing GetProcessHistory")
	}
	_ = (&procmeshv1.GetNodeHistoryRequest{}).GetNodeId
	_ = (&procmeshv1.GetNodeHistoryRequest{}).GetSinceUnix
	_ = (&procmeshv1.GetNodeHistoryRequest{}).GetUntilUnix
	_ = (&procmeshv1.GetNodeHistoryRequest{}).GetResolution
	_ = (&procmeshv1.GetProcessHistoryRequest{}).GetIdOrName
	_ = (&procmeshv1.MetricPoint{}).GetTsUnix
	_ = (&procmeshv1.MetricSeries{}).GetName
	var _ procmeshv1connect.MetricsServiceHandler = (*MetricsAPI)(nil)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestProto_Q3HistoryRPCsGenerated -count=1`

Expected: FAIL

- [ ] **Step 3: 改 proto、`make proto`、`make proto-ts`、桩方法、补 fake 客户端**

`GetNodeHistory` / `GetProcessHistory` 暂时：

```go
func (s *MetricsAPI) GetNodeHistory(context.Context, *connect.Request[procmeshv1.GetNodeHistoryRequest]) (*connect.Response[procmeshv1.GetNodeHistoryResponse], error) {
	return nil, unimplemented()
}
```

`unimplemented` 已在 `authapi.go`。`fakeMetricsClient` 与所有实现 `MetricsServiceClient` 的测试假对象都要加两个方法，否则编译失败。

工作树若无 `web/node_modules`：`cd web && npm ci` 后再 `make proto-ts`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api ./internal/agent ./internal/cli ./internal/rpc -count=1 -timeout 180s`

Expected: PASS（编译期假客户端齐全）

- [ ] **Step 5: Commit**

```bash
git add proto/procmesh/v1 internal/api/proto_gen_test.go internal/api/metricsapi.go internal/api/metricsapi_test.go web/src/gen
git commit -m "$(cat <<'EOF'
feat(proto): add GetNodeHistory and GetProcessHistory RPCs

EOF
)"
```

---

### Task 4: 实现 History RPC（本机查询 + hop）

**Files:**
- Modify: `internal/api/metricsapi.go`
- Modify: `internal/api/metricsapi_test.go`
- Modify: `internal/api/server.go`（`MetricsAPI.Store = opts.Store`，已有 Store）

**Interfaces:**
- Consumes: Task 1 Store、Task 2 `SelectLayer`、Task 3 proto、现有 `hopRoute` / `Forward.Metrics` / `authorizeProcessRoute`
- Produces: 真实 `GetNodeHistory` / `GetProcessHistory`

规范化：

```go
func normalizeHistoryRange(now time.Time, since, until int64, resolution string) (time.Time, time.Time, string, error) {
	u := until
	if u <= 0 {
		u = now.Unix()
	}
	s := since
	if s <= 0 {
		s = u - int64((24 * time.Hour).Seconds())
	}
	st := time.Unix(s, 0).UTC()
	ut := time.Unix(u, 0).UTC()
	if !ut.After(st) {
		return time.Time{}, time.Time{}, "", errcode.E(errcode.INVALID, "until must be after since")
	}
	switch resolution {
	case "", metrics.LayerRawMin, metrics.LayerDown5m:
	default:
		return time.Time{}, time.Time{}, "", errcode.E(errcode.INVALID, "invalid resolution")
	}
	layer := resolution
	if layer == "" {
		layer = metrics.SelectLayer(st, ut)
	}
	return st, ut, layer, nil
}
```

- [ ] **Step 1: 写失败测试**

```go
func TestMetrics_GetNodeHistory_RequiresNodeID(t *testing.T) {
	api := &MetricsAPI{Store: openAPIStore(t), LocalID: "n1"}
	_, err := api.GetNodeHistory(context.Background(), connect.NewRequest(&procmeshv1.GetNodeHistoryRequest{}))
	if err == nil {
		t.Fatal("expected INVALID")
	}
}

func TestMetrics_GetNodeHistory_GapNotFilledWithZero(t *testing.T) {
	st := openAPIStore(t)
	ctx := context.Background()
	_ = st.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerRawMin, TSUnix: 1_700_000_000, Value: 11},
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerRawMin, TSUnix: 1_700_000_120, Value: 22},
	})
	api := &MetricsAPI{Store: st, LocalID: "n1", LocalOnly: true}
	resp, err := api.GetNodeHistory(ctx, connect.NewRequest(&procmeshv1.GetNodeHistoryRequest{
		NodeId: "n1", SinceUnix: 1_700_000_000, UntilUnix: 1_700_000_120, Resolution: "raw_min",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var cpu *procmeshv1.MetricSeries
	for _, s := range resp.Msg.GetSeries() {
		if s.GetName() == "cpu_percent" {
			cpu = s
		}
	}
	if cpu == nil || len(cpu.Points) != 2 {
		t.Fatalf("%+v", resp.Msg)
	}
	if cpu.Points[0].GetValue() == 0 && cpu.Points[0].GetTsUnix() != 1_700_000_000 {
		t.Fatal("gap filled")
	}
	if len(cpu.Points) == 3 {
		t.Fatal("invented midpoint")
	}
}

func TestMetrics_GetNodeHistory_DefaultLayerByRange(t *testing.T) {
	st := openAPIStore(t)
	ctx := context.Background()
	_ = st.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: metrics.SeriesNodeCPU, SubjectID: "n1", Layer: metrics.LayerDown5m, TSUnix: 1_700_000_000, Value: 7},
	})
	api := &MetricsAPI{Store: st, LocalID: "n1", LocalOnly: true}
	resp, err := api.GetNodeHistory(ctx, connect.NewRequest(&procmeshv1.GetNodeHistoryRequest{
		NodeId: "n1", SinceUnix: 1_700_000_000, UntilUnix: 1_700_000_000 + 25*3600,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetLayer() != "down_5m" {
		t.Fatalf("layer=%s", resp.Msg.GetLayer())
	}
}

func TestMetrics_GetNodeHistory_HopsAndUnavailable(t *testing.T) {
	api := &MetricsAPI{
		LocalID: "aaa",
		Router:  &Router{LocalID: "aaa", Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{NodeID: "ccc", State: cluster.StateFailed, RPCAddress: "127.0.0.1:9"}}
		}},
	}
	_, err := api.GetNodeHistory(context.Background(), connect.NewRequest(&procmeshv1.GetNodeHistoryRequest{NodeId: "ccc"}))
	if err == nil {
		t.Fatal("FAILED owner must be UNAVAILABLE")
	}
}

func TestMetrics_GetProcessHistory_NotFound(t *testing.T) {
	m, st, _ := newTestManager(t)
	api := &MetricsAPI{Mgr: m, Store: st, LocalID: "n1", LocalOnly: true}
	_, err := api.GetProcessHistory(context.Background(), connect.NewRequest(&procmeshv1.GetProcessHistoryRequest{IdOrName: "nope"}))
	if err == nil {
		t.Fatal("NOT_FOUND")
	}
}

func TestMetrics_GetProcessHistory_LocalSeries(t *testing.T) {
	m, st, _ := newTestManager(t)
	ctx := context.Background()
	spec := applyTestSpec(t, m, "web") // 使用 metricsapi_test 已有夹具；若无则复制 process 测试里 ApplySpec 最小用法
	_ = st.InsertMetricSamples(ctx, []store.MetricSample{
		{Series: metrics.SeriesProcCPU, SubjectID: spec.ProcessID, Layer: metrics.LayerRawMin, TSUnix: 50, Value: 3},
	})
	api := &MetricsAPI{Mgr: m, Store: st, LocalID: "n1", LocalOnly: true}
	resp, err := api.GetProcessHistory(ctx, connect.NewRequest(&procmeshv1.GetProcessHistoryRequest{
		IdOrName: "web", SinceUnix: 0, UntilUnix: 100, Resolution: "raw_min",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetProcessId() != spec.ProcessID {
		t.Fatalf("%s", resp.Msg.GetProcessId())
	}
	found := false
	for _, s := range resp.Msg.GetSeries() {
		if s.GetName() == "cpu_percent" && len(s.Points) == 1 && s.Points[0].GetValue() == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", resp.Msg)
	}
}
```

`openAPIStore` / `applyTestSpec`：若测试文件已有 store/manager 夹具就复用，不要复制第二套 Open 逻辑。没有 `applyTestSpec` 时，用现有 `newTestManager` + `m.ApplySpec`（看同包其它测试）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run 'TestMetrics_GetNodeHistory_|TestMetrics_GetProcessHistory_' -count=1`

Expected: FAIL（仍是 unimplemented 或空）

- [ ] **Step 3: 实现查询与 hop**

抽 `loadSeries(ctx, subject, layer, since, until, fullSeriesName, shortName) *MetricSeries`。节点固定 3 条短名，进程固定 2 条。Store==nil → `UNAVAILABLE` `history store unavailable`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/metricsapi.go internal/api/metricsapi_test.go internal/api/server.go
git commit -m "$(cat <<'EOF'
feat(api): serve node and process metric history via Owner hop

EOF
)"
```

---

### Task 5: Agent 接线 + `/metrics` 行数

**Files:**
- Modify: `internal/agent/run.go`
- Modify: `internal/agent/rpc.go`（LocalOnly MetricsAPI 注入同一 Store）
- Modify: `internal/api/metrics.go`（`renderMetrics` 增加 `sampleRows int64`）
- Modify: `internal/api/metrics_test.go`
- Modify: `internal/api/server.go`（`/metrics` 读 `opts.Store.CountMetricSamples`）

**Interfaces:**
- Consumes: Task 2 `Recorder`、现有 `metrics.Collector`、`process.Manager.ListSpecs`/`ListInstances`、`collector.NodeMetrics()` 的 DiskPercent
- Produces: Agent 启动后 Recorder 与 Collector 并行；`procmesh_metric_samples_rows`

接线规则：

```go
rec := metrics.NewRecorder(st, nodeID)
rec.CollectNode = collector.NodeMetrics
rec.CollectProcess = collector.ProcessMetrics
rec.ListProcesses = func() []metrics.ProcessRef {
    // ListSpecs + ListInstances；仅 PID>0 的实例
}
rec.DiskPercent = func() float64 {
    nm, err := collector.NodeMetrics()
    if err != nil || nm == nil {
        return 0
    }
    return nm.DiskPercent
}
_ = rec.Start(ctx)
```

`ListProcesses` 放在 `internal/agent`，不要让 `metrics` import `process`。

- [ ] **Step 1: 写失败测试**

`metrics_test.go` 追加：

```go
func TestMetrics_SampleRowsGauge(t *testing.T) {
	m, st, _ := newTestManager(t)
	_ = st.InsertMetricSamples(context.Background(), []store.MetricSample{
		{Series: "node.cpu_percent", SubjectID: "n", Layer: "raw_min", TSUnix: 1, Value: 1},
		{Series: "node.cpu_percent", SubjectID: "n", Layer: "raw_min", TSUnix: 2, Value: 2},
	})
	srv, err := NewServer(Options{Mgr: m, Store: st, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "procmesh_metric_samples_rows 2") {
		t.Fatalf("%s", rec.Body.String())
	}
}
```

现有 `assertBatchMetricsPresent` 的用例在改 `renderMetrics` 签名后必须仍编译：给 `renderMetrics` 最后加参数，调用处补 `sampleRows`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestMetrics_SampleRowsGauge -count=1`

Expected: FAIL

- [ ] **Step 3: 改 renderMetrics、server、run.go / rpc.go**

`renderMetrics` 追加：

```
# HELP procmesh_metric_samples_rows Local historical metric sample rows.
# TYPE procmesh_metric_samples_rows gauge
procmesh_metric_samples_rows %d
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api ./internal/agent ./internal/metrics -count=1 -timeout 180s`

Expected: PASS。不要改 Q2 验收语义。

- [ ] **Step 5: Commit**

```bash
git add internal/agent/run.go internal/agent/rpc.go internal/api/metrics.go internal/api/metrics_test.go internal/api/server.go
git commit -m "$(cat <<'EOF'
feat(agent): record local history and export sample row gauge

EOF
)"
```

---

### Task 6: CLI `metrics history`

**Files:**
- Create: `internal/cli/metrics.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/client.go`
- Modify: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: Task 3/4 RPC
- Produces:

```text
procmesh metrics history node [NODE_ID] [--since RFC3339|unix] [--until RFC3339|unix]
procmesh metrics history process <id-or-name> [--since ...] [--until ...]
```

`NODE_ID` 省略则请求空 `node_id` 非法——CLI 必须要求参数，或在省略时让服务端 INVALID。锁定：**node 必须带 NODE_ID**。

输出（纯文本，便于测试）：

```
node_id=n1 layer=raw_min
series=cpu_percent
ts=1700000000 value=11
ts=1700000120 value=22
series=memory_percent
...
```

进程：`process_id=... layer=...`。缺口不打行。

`--since`/`--until` 解析：全数字 → unix 秒；否则 `time.RFC3339`。非法 → usageError。

`client` 增加 `metrics procmeshv1connect.MetricsServiceClient`。

- [ ] **Step 1: 写失败测试**

`root_test.go`：

```go
func TestCLI_UsageIncludesMetricsHistory(t *testing.T) {
	if !strings.Contains(usageText, "metrics history node") {
		t.Fatal("usage")
	}
	if !strings.Contains(usageText, "metrics history process") {
		t.Fatal("usage process")
	}
}

func TestCLI_ParseSinceUntil(t *testing.T) {
	opt, err := parseArgs([]string{"--since", "1700000000", "--until", "2026-08-16T00:00:00Z", "metrics", "history", "node", "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.sinceUnix != 1700000000 {
		t.Fatalf("since %d", opt.sinceUnix)
	}
	if opt.untilUnix == 0 {
		t.Fatal("until")
	}
}
```

另加一个带 httptest 的 history 输出测试：用 `NewServer` + 预插 store 点，`runCLI("--server", url, "metrics", "history", "node", "n1", "--since", "1700000000", "--until", "1700000120")`，断言含 `ts=1700000000`、`value=11`，**不含** `ts=1700000060`。

若 `newTestServer` 不暴露 Store，在本测试自建 `api.NewServer`（与 `newTestServer` 同样不启集群即可，LocalOnly 默认）。需要把预置点写进同一个 Store。可在 `root_test.go` 旁加 helper `newTestServerWithStore`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli -run 'TestCLI_UsageIncludesMetricsHistory|TestCLI_ParseSinceUntil|TestCLI_MetricsHistory' -count=1`

Expected: FAIL

- [ ] **Step 3: 实现命令**

`usageText` 增加两行。`applyFlag` 增加 `since`/`until`。`Main` switch 增加 `metrics`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cli -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "$(cat <<'EOF'
feat(cli): add metrics history node and process commands

EOF
)"
```

---

### Task 7: Web 趋势图（补齐 P5 详情页）

**Files:**
- Create: `web/src/lib/historyChart.ts`
- Create: `web/src/lib/historyChart.test.ts`
- Create: `web/src/components/HistoryChart.vue`
- Create: `web/src/components/HistoryChart.test.ts`
- Modify: `web/src/lib/rpc.ts`（`MetricsClient` 增加 `getNodeHistory` / `getProcessHistory`）
- Modify: `web/src/pages/NodeDetailPage.vue`
- Modify: `web/src/pages/NodeDetailPage.test.ts`
- Modify: `web/src/pages/ProcessDetailPage.vue`
- Modify: `web/src/pages/ProcessDetailPage.test.ts`
- Modify: `web/public/locales/en/common.json`
- Modify: `web/public/locales/zh/common.json`
- Create: `web/e2e/history.spec.ts`

**Interfaces:**
- Consumes: Task 3/4 RPC、现有 `FreshnessBadge`、P5 token
- Produces: 详情页 24h/7d 切换；缺口断开；UNAVAILABLE → STALE 文案，**不**回退 Gossip 数字当曲线

i18n（en / zh 必须成对，键路径一致）：

```json
"metricsHistory": {
  "title": "History",
  "range24h": "24h",
  "range7d": "7d",
  "cpu": "CPU %",
  "memory": "Memory",
  "disk": "Disk %",
  "empty": "No samples in this range",
  "stale": "History unavailable (STALE). Live gossip summary is not a chart.",
  "loading": "Loading history…"
}
```

中文：

```json
"metricsHistory": {
  "title": "历史",
  "range24h": "24 小时",
  "range7d": "7 天",
  "cpu": "CPU %",
  "memory": "内存",
  "disk": "磁盘 %",
  "empty": "该时间范围内无样本",
  "stale": "历史不可用（STALE）。禁止把 Gossip 即时摘要当成曲线。",
  "loading": "正在加载历史…"
}
```

`historyChart.ts`：

```ts
export type ChartPoint = { t: number; v: number };

export function splitSegments(points: ChartPoint[], stepSec: number): ChartPoint[][] {
  const out: ChartPoint[][] = [];
  let cur: ChartPoint[] = [];
  for (const p of points) {
    if (cur.length && p.t - cur[cur.length - 1].t > stepSec * 1.5) {
      out.push(cur);
      cur = [];
    }
    cur.push(p);
  }
  if (cur.length) out.push(cur);
  return out;
}

export function stepSecForLayer(layer: string): number {
  return layer === "down_5m" ? 300 : 60;
}
```

`HistoryChart.vue`：props `title`, `points: ChartPoint[]`, `stepSec`, `stale: boolean`。SVG `viewBox="0 0 600 160"`，每个 segment 一条 `polyline`，**不**用直线连缺口。STALE 时不画线，展示 `t('metricsHistory.stale')`。空点且非 STALE 展示 empty。STALE 徽章 class 不得含 `green` / `#D1FAE5` / `#10A37F`。

详情页：默认 range `24h`（`since = now-24h`）；点 `7d` 则 `now-7d`。`refetchInterval: 60_000`。进程页用 `withTarget(ownerNodeId)`。节点页 `getNodeHistory({ nodeId: node.nodeId, sinceUnix, untilUnix })`。

- [ ] **Step 1: 写失败测试**

`historyChart.test.ts`：

```ts
it("breaks a 60s series when a minute is missing", () => {
  const segs = splitSegments(
    [
      { t: 0, v: 10 },
      { t: 60, v: 11 },
      { t: 180, v: 12 },
    ],
    60,
  );
  expect(segs).toHaveLength(2);
  expect(segs.flat().some((p) => p.t === 120 || p.v === 0)).toBe(false);
});
```

`HistoryChart.test.ts`：STALE 渲染文案且无 `polyline`；有缺口的 points 渲染 **2** 条 polyline。

`NodeDetailPage.test.ts` / `ProcessDetailPage.test.ts`：mock client 返回含缺口的 series，页面出现 `24h`/`7d`；点 7d 后调用 `until-since > 86400`。另测：history 抛 `UNAVAILABLE` 时出现 stale 文案，页面 **不得** 把 Gossip CPU 数字放进 SVG。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test`

Expected: FAIL

- [ ] **Step 3: 实现组件与页面，补 i18n**

不要引入 chart 库。不要改 Overview 的百分比来源。

- [ ] **Step 4: 跑测试、i18n、build**

Run:

```bash
cd web && npm test && npm run i18n:check && npm run build
```

Expected: PASS。提交 `internal/web/dist`（Vite outDir）。

`web/e2e/history.spec.ts`：若现有 e2e 能登录，则打开一个 node 详情断言 `24h` 按钮可见。无进程夹具则只断言文案/按钮，不要依赖真实点。

- [ ] **Step 5: Commit**

```bash
git add web/src web/public/locales web/e2e internal/web/dist
git commit -m "$(cat <<'EOF'
feat(web): draw 24h/7d history charts with gap-preserving strokes

EOF
)"
```

---

### Task 8: Q3 验收 + 索引

**Files:**
- Create: `internal/agent/q3_accept_test.go`
- Modify: `docs/superpowers/plans/2026-08-16-v1.1.md`

**Interfaces:**
- Consumes: 全阶段
- Produces: 可脚本化验收：缺口不插 0；Owner 不可达 `UNAVAILABLE`；95% 停写仍可读

复用 `q2_accept_test.go` 的 `startClusterAgent` / `mustCLI` / `joinTwo`。

```go
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
```

`insertLocalSamples`：`store.Open(filepath.Join(root, ...实际 db 相对路径...))`。先读 `internal/paths` 与现有 agent 测试如何打开同一把库（搜 `store.Open` in `internal/agent/*_test.go`）。路径必须用真实 layout，禁止猜错导致测了空库。

`v1.1.md` 把 Q3 一行改成 **已完成**，并写上计划文件链接。

- [ ] **Step 1: 写失败测试（先提交测试文件，实现 helper 后应能跑红/绿）**

若 db 路径不明，先在本任务 Step 1 用现有测试打开方式写 `insertLocalSamples`，测试应因 CLI 已存在而偏绿——若 CLI 已通，本任务验证集成。若 helper 路径错误会 FAIL，按真实路径修。

- [ ] **Step 2: 跑测试**

Run: `go test ./internal/agent -run TestQ3_ -count=1 -timeout 180s`

Expected: 第一次可能 FAIL（db 路径 / CLI 输出格式）；修好 helper 后 PASS。

- [ ] **Step 3: 更新索引文档**

- [ ] **Step 4: 回归**

Run:

```bash
go test ./internal/store ./internal/metrics ./internal/api ./internal/cli ./internal/agent -count=1 -timeout 300s
cd web && npm test && npm run i18n:check
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/q3_accept_test.go docs/superpowers/plans/2026-08-16-v1.1.md
git commit -m "$(cat <<'EOF'
test(agent): Q3 history gaps and unavailable owner

EOF
)"
```

---

## 自检（撰写后）

| Spec 要求 | 任务 |
|-----------|------|
| 只存 5 条已有序列 | 2, 4 |
| raw_min 24h + down_5m 7d；CPU/Mem 均值；Disk 最大 | 2 |
| 失败分钟不写点 / 查询不插 0 | 1, 2, 4, 6, 7, 8 |
| Owner SQLite，不进 Raft/Gossip | 1, 5 |
| 读 hop Owner；失败 UNAVAILABLE | 4, 8 |
| 禁止 Gossip 冒充曲线 | 7 |
| 磁盘 95% 停写、90% 删最老 down_5m | 2, 8 |
| GetNodeHistory / GetProcessHistory | 3, 4 |
| CLI `metrics history node\|process` | 6 |
| 详情页 24h/7d | 7 |
| `metric_samples_rows` | 5 |
| P5 详情页增强而非重做 P5 | 7 |
| 不做 Alert/Backup/扩采集面 | 全局约束 |

无 TBD/TODO 占位。后续任务使用的名字与 Task 1–2 的 `MetricSample` / `Recorder` / `SelectLayer` 一致。
