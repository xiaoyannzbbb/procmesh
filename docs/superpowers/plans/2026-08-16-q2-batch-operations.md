# Q2 Batch Operations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** 已完成

**Goal:** 任意入口可发起 Batch（start/stop/restart/config update），任务只存在该入口本地 SQLite；部分成功可见，TIMEOUT 不得画成成功或从列表消失；Case 7 可脚本化验收。

**Architecture:** `CreateBatch` 在入口校验 + 展开 + 落库后立即返回 `PENDING`。后台 worker 并发扇出到各 Owner（V1.0 Write-to-Owner + 每 target 独立 `operation_id`）。记录不进 Raft / Gossip，不跨入口认领。Process Group 用 Gossip 初筛、执行前 RPC 读 Owner spec 核对 `group`。入口崩溃后只恢复尚未终态的 `PENDING`/`RUNNING` target，且必须复用原 `operation_id`。

**Tech Stack:** 现有 Go + SQLite（modernc.org/sqlite）+ ConnectRPC + Vue3 + `make proto` / `make proto-ts`。不新增二进制、不新增 REST 资源。

---

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- 强制 TDD：先红后绿；每任务先写失败测试
- `process` 不得 import `cluster` 或 `control`；`process` 不得依赖 `batch`
- `batch` / `api` / `agent` 可编排 batch；不得让 `process` 反过来依赖它们
- Raft **禁止**写入 batch 记录；Gossip **禁止**携带 batch 进度
- 入口崩溃后禁止在另一节点「接管」同一 `batch_id` 并重放已 SUCCESS 的 target
- `version.Protocol` 保持 `1`
- 错误码：`INVALID` / `CONFLICT` / `DENIED` / `NOT_FOUND` / `UNAVAILABLE` / `TIMEOUT`
- TIMEOUT 表示结果未知，不是 FAILED；UI 与 Case 7 必须单独暴露；禁止改成 SUCCESS 或从列表消失
- 所有 Mutation 必须带非空 `operation_id`（UUID）
- 无 `expected_revision` 的 config batch 必须拒绝
- `command.execute.batch` 保持关闭，本阶段不做任意命令 batch
- 测试与代码同目录；覆盖率：`internal/batch` ≥ 80%；`internal/control`、`internal/auth`、`internal/process` 保持 ≥ 80%
- 文档与计划用中文；API 错误消息用英文
- 生成的 proto Go / TS 文件禁止手改；改完 proto 必须 `make proto` 与 `make proto-ts`
- 本阶段不做：AlertService、BackupService、历史指标、跨入口全局 Batch 浏览
- 工作目录是本 worktree，提交写在 `feat/q2-batch-operations`

## 规格解读（Q2 边界）

来源：`docs/superpowers/specs/2026-08-16-v1.1-architecture-design.md` §6、§10、§11、§12 Q2、§13.1–§13.2、§15；PRD §50–52、§92 Case 7。冲突以架构 spec 为准。

1. **所有权：** 记录只存在创建该 batch 的入口 Agent SQLite。`List`/`Get` 默认本机。看其他入口用 `--server` / 连那个 Agent 的 Web。
2. **类型：** `START` | `STOP` | `RESTART` | `CONFIG_UPDATE`。四类都针对进程。
3. **结束态：** 全部 target 离开 PENDING/RUNNING 之后：全 SUCCESS → `COMPLETED`；零 SUCCESS 且零 TIMEOUT → `FAILED`；其余（含任何 TIMEOUT）→ `PARTIAL`。禁止 All-or-Nothing 与跨 target 回滚。没有独立 UNKNOWN；未知一律 `TIMEOUT`。
4. **零 target：** 创建时 `INVALID`，不插 `batches` 行。
5. **展开快照：** 创建时拍快照；之后组成员变更不影响这个 batch。入口 RBAC 不足的 target 标 `DENIED` 并保留。Process Group：Gossip 初筛 + Owner spec 核对，不匹配标 `INVALID` 并保留，不得静默丢掉。
6. **执行：** `CreateBatch` 立即返回。worker 默认并发 16（1–64），单 target 超时默认 30s。RPC 上下文超时 → `TIMEOUT`；连接拒绝 / 无路由 / 证书拒绝 → `UNAVAILABLE`。每 target 走 Write-to-Owner。
7. **重试：** `RetryFailed` 只挑 FAILED/DENIED/CONFLICT/UNAVAILABLE/INVALID，生成**新** `operation_id`。`ReplayTimeout` 只挑 TIMEOUT，**复用**原 `operation_id`。SUCCESS 永不重放。
8. **恢复：** Agent 重启：终态不动。PENDING/RUNNING 且无终态：用原 `operation_id` 再发。已标 TIMEOUT 的不自动重放。
9. **权限：** 需要 `batch.execute`，外加每个 target 的 `process.start|stop|restart` 或 `process.config.update`。入口写一条 batch audit；Owner 走既有 execution audit。
10. **可演示：** 从 A 批量重启一组进程，部分超时可见且可 Retry/Replay。

## File map

```text
internal/store/schema.sql
internal/store/store_test.go
internal/store/batch.go                 # 新建
internal/store/batch_test.go            # 新建
internal/batch/types.go                 # 新建
internal/batch/types_test.go            # 新建
internal/batch/engine.go                # 新建
internal/batch/engine_test.go           # 新建
internal/batch/expand.go                # 新建
internal/batch/expand_test.go           # 新建
internal/agentcfg/load.go
internal/agentcfg/load_test.go
proto/procmesh/v1/api.proto
internal/api/batch.go                   # 新建
internal/api/batch_test.go              # 新建
internal/api/server.go
internal/api/metrics.go
internal/api/metrics_test.go
internal/agent/run.go
internal/cli/root.go
internal/cli/client.go
internal/cli/batch.go                   # 新建
internal/cli/root_test.go
web/src/lib/rpc.ts
web/src/router.ts
web/src/components/AppShell.vue
web/src/pages/BatchesPage.vue           # 新建
web/src/pages/BatchesPage.test.ts       # 新建
web/src/pages/OverviewPage.vue
web/src/pages/OverviewPage.test.ts
web/public/locales/en/common.json
web/public/locales/zh/common.json
web/e2e/batch.spec.ts                   # 新建
internal/agent/q2_accept_test.go        # 新建
docs/superpowers/plans/2026-08-16-v1.1.md
```

生成（改 proto 后执行，不要手改）：`proto/procmesh/v1/api.pb.go`、`proto/procmesh/v1/procmeshv1connect/api.connect.go`、`web/src/gen/procmesh/v1/api_pb.ts`

---

## 本阶段锁定的模型

### 状态与汇总

```go
package batch

type Type string

const (
	TypeStart        Type = "START"
	TypeStop         Type = "STOP"
	TypeRestart      Type = "RESTART"
	TypeConfigUpdate Type = "CONFIG_UPDATE"
)

type Status string

const (
	StatusPending   Status = "PENDING"
	StatusRunning   Status = "RUNNING"
	StatusCompleted Status = "COMPLETED"
	StatusPartial   Status = "PARTIAL"
	StatusFailed    Status = "FAILED"
)

type TargetStatus string

const (
	TargetPending     TargetStatus = "PENDING"
	TargetRunning     TargetStatus = "RUNNING"
	TargetSuccess     TargetStatus = "SUCCESS"
	TargetFailed      TargetStatus = "FAILED"
	TargetTimeout     TargetStatus = "TIMEOUT"
	TargetDenied      TargetStatus = "DENIED"
	TargetConflict    TargetStatus = "CONFLICT"
	TargetUnavailable TargetStatus = "UNAVAILABLE"
	TargetInvalid     TargetStatus = "INVALID"
)
```

`Rollup(targets)`：若仍有 PENDING/RUNNING → 返回 `RUNNING`（创建刚落库、worker 尚未启动时调用方保持 `PENDING`）。全部离开进行态后：全 SUCCESS → `COMPLETED`；`success==0 && timeout==0` → `FAILED`；否则 `PARTIAL`。

`Summary` JSON：

```json
{"success":0,"failed":0,"timeout":0,"denied":0,"conflict":0,"unavailable":0,"invalid":0}
```

只统计终态；PENDING/RUNNING 不计入这七个计数。

### Selector

```go
type Selector struct {
	ProcessIDs   []string         `json:"process_ids,omitempty"`
	ProcessNames []ProcessNameRef `json:"process_names,omitempty"`
	AgentGroupID string           `json:"agent_group_id,omitempty"`
	ProcessGroup string           `json:"process_group,omitempty"`
}

type ProcessNameRef struct {
	NodeID      string `json:"node_id"`
	ProcessName string `json:"process_name"`
}
```

- 至少一个字段非空，否则 `INVALID`（empty selector）。
- 多个字段同时设置 = **并集**，按 `(node_id, process_id)` 去重。
- `process_group` 非空时 trim 后必须匹配 `^[A-Za-z0-9._-]{1,64}$`，否则创建即 `INVALID`。
- 展开后零 target → `INVALID`，不写库。

### CONFIG_UPDATE overlay

`CreateBatch` 的 `config`（`ProcessSpec`）在 `type=CONFIG_UPDATE` 时必填。展开时对每个 target RPC 读 Owner 当前 spec：

1. 把 `config` 中**非零/非空**字段覆盖到当前 spec（禁止覆盖 `process_id`、`name`、`owner_agent_id`、`latest_revision`）。
2. `expected_revision` = Owner 当时的 `latest_revision`，写入该 target 的 `payload_json`。
3. 执行时走 Owner `UpdateConfig` / `ApplySpec` + CAS。中途被改 → 该 target `CONFLICT`。
4. `type=CONFIG_UPDATE` 但 `config` 为空 → `INVALID`。
5. 非 CONFIG_UPDATE 却带 `config` → `INVALID`。

`payload_json` 形状：

```json
{"expected_revision":3,"spec":{...overlay applied spec...}}
```

非 CONFIG_UPDATE 的 target：`expected_revision` 为空/`0`，`payload_json` 可空。

### 错误映射（worker）

```text
errcode.TIMEOUT / context.DeadlineExceeded / connect DeadlineExceeded → TIMEOUT
errcode.DENIED → DENIED
errcode.CONFLICT → CONFLICT
errcode.INVALID → INVALID
errcode.UNAVAILABLE / 拨号失败 / 无路由 / 证书拒绝 → UNAVAILABLE
其余 → FAILED
```

使用已有 `rpc.MapCallError` / `rpc.MapDialError`，不要另造一套超时判定。

### Proto（Task 7 写入 `api.proto` 末尾）

```protobuf
message ProcessNameRef {
  string node_id = 1;
  string process_name = 2;
}

message BatchSelector {
  repeated string process_ids = 1;
  repeated ProcessNameRef process_names = 2;
  string agent_group_id = 3;
  string process_group = 4;
}

message BatchSummary {
  int32 success = 1;
  int32 failed = 2;
  int32 timeout = 3;
  int32 denied = 4;
  int32 conflict = 5;
  int32 unavailable = 6;
  int32 invalid = 7;
}

message BatchTarget {
  string operation_id = 1;
  string node_id = 2;
  string process_id = 3;
  string process_name = 4;
  string status = 5; // PENDING|RUNNING|SUCCESS|FAILED|TIMEOUT|DENIED|CONFLICT|UNAVAILABLE|INVALID
  string error = 6;
  int64 expected_revision = 7;
  int64 started_unix_ms = 8;
  int64 finished_unix_ms = 9;
}

message Batch {
  string batch_id = 1;
  string operator = 2;
  string source_agent = 3;
  string type = 4; // START|STOP|RESTART|CONFIG_UPDATE
  BatchSelector selector = 5;
  string status = 6; // PENDING|RUNNING|COMPLETED|PARTIAL|FAILED
  BatchSummary summary = 7;
  int64 created_unix_ms = 8;
  repeated BatchTarget targets = 9; // List 可省略；Get 必填
}

message CreateBatchRequest {
  MutationMeta meta = 1;
  string type = 2;
  BatchSelector selector = 3;
  ProcessSpec config = 4; // CONFIG_UPDATE required
  string comment = 5;
}
message CreateBatchResponse { Batch batch = 1; }

message GetBatchRequest { string batch_id = 1; }
message GetBatchResponse { Batch batch = 1; }

message ListBatchesRequest { int32 limit = 1; } // 0 = 50；封顶 200
message ListBatchesResponse { repeated Batch batches = 1; } // 不含 targets，最新在前

message RetryBatchRequest { MutationMeta meta = 1; string batch_id = 2; }
message RetryBatchResponse { Batch batch = 1; }

message ExportBatchRequest {
  string batch_id = 1;
  string format = 2; // json（默认）| csv
}
message ExportBatchResponse {
  bytes content = 1;
  string content_type = 2;
  string filename = 3;
}

service BatchService {
  rpc CreateBatch(CreateBatchRequest) returns (CreateBatchResponse);
  rpc GetBatch(GetBatchRequest) returns (GetBatchResponse);
  rpc ListBatches(ListBatchesRequest) returns (ListBatchesResponse);
  rpc RetryFailed(RetryBatchRequest) returns (RetryBatchResponse);
  rpc ReplayTimeout(RetryBatchRequest) returns (RetryBatchResponse);
  rpc ExportBatch(ExportBatchRequest) returns (ExportBatchResponse);
}
```

### UI 颜色（TIMEOUT 不得为绿）

| 状态 | 背景 | 文字 | 禁止 |
|------|------|------|------|
| SUCCESS | `#D1FAE5` | `#065F46` | — |
| TIMEOUT | `#FEF3C7` | `#92400E` | **禁止绿色** |
| FAILED / DENIED / CONFLICT / UNAVAILABLE / INVALID | `#FEE2E2` | `#991B1B` | 禁止当成功 |
| PENDING / RUNNING | `#E5E7EB` | `#374151` | — |

PARTIAL 批次徽章用琥珀（与 TIMEOUT 同色），不得用 LIVE 绿。

---

### Task 1: SQLite batches / batch_targets

**Files:**
- Modify: `internal/store/schema.sql`
- Modify: `internal/store/store_test.go`（表清单）
- Create: `internal/store/batch.go`
- Test: `internal/store/batch_test.go`

**Interfaces:**
- Consumes: 现有 `store.Open`、`formatTime`、`parseTime`（`journal.go` 未导出的 parse 若没有，本任务在 `batch.go` 用 `time.RFC3339Nano` 自行解析）
- Produces:
  - `store.BatchRecord`、`store.BatchTargetRecord`
  - `func (s *Store) InsertBatch(ctx, rec BatchRecord, targets []BatchTargetRecord) error` — 单事务
  - `func (s *Store) GetBatch(ctx, id string) (BatchRecord, []BatchTargetRecord, error)` — 缺行 `NOT_FOUND`
  - `func (s *Store) ListBatches(ctx, limit int) ([]BatchRecord, error)` — `created_at DESC`
  - `func (s *Store) UpdateBatchStatus(ctx, id, status, summaryJSON string) error`
  - `func (s *Store) UpdateTarget(ctx, batchID, opID string, rec BatchTargetRecord) error`
  - `func (s *Store) ListIncompleteTargets(ctx) ([]BatchTargetRecord, error)` — status 为 PENDING 或 RUNNING

- [ ] **Step 1: Write the failing test**

`internal/store/store_test.go` 的表清单追加 `"batches"`、`"batch_targets"`。

`internal/store/batch_test.go`：

```go
package store_test

func TestBatch_InsertGetListAndUpdate(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	rec := store.BatchRecord{
		BatchID: "b1", Operator: "admin", SourceAgent: "n1",
		Type: "RESTART", SelectorJSON: `{"process_ids":["p1"]}`,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(), Status: "PENDING",
		SummaryJSON: `{"success":0,"failed":0,"timeout":0,"denied":0,"conflict":0,"unavailable":0,"invalid":0}`,
	}
	targets := []store.BatchTargetRecord{{
		BatchID: "b1", OperationID: "op-1", NodeID: "n1", ProcessID: "p1",
		ProcessName: "nginx", Status: "PENDING",
	}}
	if err := s.InsertBatch(ctx, rec, targets); err != nil {
		t.Fatal(err)
	}
	got, ts, err := s.GetBatch(ctx, "b1")
	if err != nil || got.Type != "RESTART" || len(ts) != 1 || ts[0].OperationID != "op-1" {
		t.Fatalf("got %+v %+v %v", got, ts, err)
	}
	if _, _, err := s.GetBatch(ctx, "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
	ts[0].Status = "SUCCESS"
	if err := s.UpdateTarget(ctx, "b1", "op-1", ts[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateBatchStatus(ctx, "b1", "COMPLETED", `{"success":1,"failed":0,"timeout":0,"denied":0,"conflict":0,"unavailable":0,"invalid":0}`); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListBatches(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Status != "COMPLETED" {
		t.Fatalf("list %+v %v", list, err)
	}
}

func TestBatch_ListIncompleteTargets(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rec := store.BatchRecord{BatchID: "b1", Operator: "a", SourceAgent: "n", Type: "START", SelectorJSON: `{}`, CreatedAt: time.Now().UTC(), Status: "RUNNING", SummaryJSON: `{}`}
	targets := []store.BatchTargetRecord{
		{BatchID: "b1", OperationID: "op-p", NodeID: "n", ProcessID: "p", Status: "PENDING"},
		{BatchID: "b1", OperationID: "op-s", NodeID: "n", ProcessID: "s", Status: "SUCCESS"},
	}
	if err := s.InsertBatch(ctx, rec, targets); err != nil {
		t.Fatal(err)
	}
	inc, err := s.ListIncompleteTargets(ctx)
	if err != nil || len(inc) != 1 || inc[0].OperationID != "op-p" {
		t.Fatalf("%+v %v", inc, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store -run 'TestOpen_CreatesSchemaAndSchemaVersion|TestBatch_' -count=1`

Expected: FAIL（缺表或缺方法）

- [ ] **Step 3: Write minimal implementation**

`schema.sql` 追加（`schema_version` 保持 `"1"`，`applySchema` 的 `IF NOT EXISTS` 会给旧库加表）：

```sql
CREATE TABLE IF NOT EXISTS batches (
    batch_id TEXT PRIMARY KEY,
    operator TEXT NOT NULL,
    source_agent TEXT NOT NULL,
    type TEXT NOT NULL,
    selector_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL,
    summary_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS batch_targets (
    batch_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    process_id TEXT NOT NULL,
    process_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    expected_revision INTEGER NOT NULL DEFAULT 0,
    payload_json TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    finished_at TEXT,
    PRIMARY KEY (batch_id, operation_id)
);

CREATE INDEX IF NOT EXISTS batch_targets_batch ON batch_targets(batch_id);
CREATE INDEX IF NOT EXISTS batch_targets_incomplete ON batch_targets(status);
```

`InsertBatch` 用 `s.db.BeginTx`，先插 `batches` 再插 targets，失败 Rollback。`GetBatch` 无行返回 `errcode.E(errcode.NOT_FOUND, "batch")`。时间字段沿用 `formatTime`（RFC3339Nano）。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): add batches and batch_targets tables"
```

---

### Task 2: batch 类型与 Rollup

**Files:**
- Create: `internal/batch/types.go`
- Test: `internal/batch/types_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `Type` / `Status` / `TargetStatus` / `Selector` / `ProcessNameRef` / `Summary` / `Batch` / `Target` / `func Rollup(targets []Target) Status` / `func CountSummary(targets []Target) Summary` / `func (s Summary) MarshalJSON() ([]byte, error)` 或普通结构体 json tag

`Target` 字段：`OperationID, NodeID, ProcessID, ProcessName, Status, Error, ExpectedRevision, PayloadJSON, StartedAt, FinishedAt`。

`Batch` 字段：`BatchID, Operator, SourceAgent, Type, Selector, CreatedAt, Status, Summary, Targets`。

- [ ] **Step 1: Write the failing test**

```go
func TestRollup_AllSuccessCompleted(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetSuccess}, {Status: batch.TargetSuccess}}); g != batch.StatusCompleted {
		t.Fatalf("%s", g)
	}
}

func TestRollup_AllFailedNoTimeout(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetFailed}, {Status: batch.TargetDenied}}); g != batch.StatusFailed {
		t.Fatalf("%s", g)
	}
}

func TestRollup_TimeoutIsPartialEvenIfOthersFailed(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetFailed}, {Status: batch.TargetTimeout}}); g != batch.StatusPartial {
		t.Fatalf("%s", g)
	}
}

func TestRollup_MixedSuccessFailurePartial(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetSuccess}, {Status: batch.TargetFailed}}); g != batch.StatusPartial {
		t.Fatalf("%s", g)
	}
}

func TestRollup_StillRunning(t *testing.T) {
	if g := batch.Rollup([]batch.Target{{Status: batch.TargetSuccess}, {Status: batch.TargetPending}}); g != batch.StatusRunning {
		t.Fatalf("%s", g)
	}
}

func TestCountSummary_IgnoresInFlight(t *testing.T) {
	s := batch.CountSummary([]batch.Target{
		{Status: batch.TargetSuccess},
		{Status: batch.TargetTimeout},
		{Status: batch.TargetPending},
	})
	if s.Success != 1 || s.Timeout != 1 || s.Failed != 0 {
		t.Fatalf("%+v", s)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/batch -count=1`

Expected: FAIL（包不存在）

- [ ] **Step 3: Write minimal implementation**

按「本阶段锁定的模型」实现。`Rollup` 先扫一遍：有 PENDING/RUNNING 则 `StatusRunning`。否则按 success/timeout 计数套三条结束态规则。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/batch -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/batch
git commit -m "feat(batch): add types and status rollup"
```

---

### Task 3: Engine Create / Get / List / Export（注入 Expander）

**Files:**
- Create: `internal/batch/engine.go`
- Test: `internal/batch/engine_test.go`

**Interfaces:**
- Consumes: `store.Store` 的 Task 1 API；Task 2 类型
- Produces:
  - `type Expander interface { Expand(ctx context.Context, sel Selector, typ Type) ([]Target, error) }`
  - `type Executor interface { Execute(ctx context.Context, t Target, typ Type) error }`
  - `type Engine struct { DB *store.Store; Expand Expander; Exec Executor; Concurrency int; TargetTimeout time.Duration; SourceAgent string; NewID func() string }`
  - `func (e *Engine) Create(ctx, operator string, typ Type, sel Selector, comment string) (Batch, error)`
  - `func (e *Engine) Get(ctx, id string) (Batch, error)`
  - `func (e *Engine) List(ctx, limit int) ([]Batch, error)` — 不含 Targets
  - `func (e *Engine) Export(ctx, id, format string) (content []byte, contentType, filename string, err error)`
  - 本任务 **不** 启动 worker（Create 落库后保持 PENDING，不调用 Exec）

Create 规则：
- `operator` 空、`typ` 非法、selector 全空 → `INVALID`
- 调用 `Expand`；返回空切片或只含零值 → `INVALID`，**不写库**
- 每个 target：若 `OperationID` 空则 `NewID()`；若 `Status` 空则 PENDING
- 插入后 `Get` 返回含 targets 的 Batch

Export：`json`（默认）为整个 Batch JSON；`csv` 表头 `operation_id,node_id,process_id,process_name,status,error`；其它 format → `INVALID`。

- [ ] **Step 1: Write the failing test**

```go
type stubExpand struct{ targets []batch.Target; err error }

func (s stubExpand) Expand(context.Context, batch.Selector, batch.Type) ([]batch.Target, error) {
	return s.targets, s.err
}

func TestEngine_CreateRejectsEmptySelectorAndZeroTargets(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e := &batch.Engine{DB: st, Expand: stubExpand{}, NewID: func() string { return "id" }, SourceAgent: "n1"}
	_, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{}, "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty selector: %v", err)
	}
	e.Expand = stubExpand{targets: nil}
	_, err = e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p1"}}, "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("zero targets: %v", err)
	}
	if list, _ := e.List(ctx, 10); len(list) != 0 {
		t.Fatalf("must not insert: %+v", list)
	}
}

func TestEngine_CreateGetListExport(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-1"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n1", ProcessID: "p1", ProcessName: "nginx"}}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	b, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p1"}}, "")
	if err != nil || b.BatchID != "b1" || b.Status != batch.StatusPending || len(b.Targets) != 1 || b.Targets[0].OperationID != "op-1" {
		t.Fatalf("%+v %v", b, err)
	}
	got, err := e.Get(ctx, "b1")
	if err != nil || got.Targets[0].ProcessName != "nginx" {
		t.Fatalf("%+v %v", got, err)
	}
	raw, ct, name, err := e.Export(ctx, "b1", "csv")
	if err != nil || ct != "text/csv" || !strings.Contains(string(raw), "op-1") || name == "" {
		t.Fatalf("%s %s %s %v", raw, ct, name, err)
	}
}
```

`openStore` 用 `store.Open(tempfile)`。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/batch -run TestEngine_ -count=1`

Expected: FAIL（无 Engine）

- [ ] **Step 3: Write minimal implementation**

`Create` 校验 type 属于四个常量。`limit<=0` 当 50，`>200` 截成 200。`Get` 把 `store` 行映射为 `batch.Batch`（`json.Unmarshal` selector）。本任务不要启动 goroutine。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/batch -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/batch
git commit -m "feat(batch): create get list and export on entry store"
```

---

### Task 4: Worker 执行、超时与并发

**Files:**
- Modify: `internal/batch/engine.go`
- Modify: `internal/batch/engine_test.go`

**Interfaces:**
- Consumes: Task 3 Engine
- Produces:
  - `func (e *Engine) Start(ctx context.Context)` — 可多次调用，内部保证只跑一个调度循环或按 batch 启动
  - `Create` 成功后把该 batch 交给 worker（`status=RUNNING`），`Create` **立即返回**，不等待全部 target
  - `func MapExecError(err error) TargetStatus` — 导出以便单测
  - 默认 `Concurrency=16`（clamp 1–64）、`TargetTimeout=30s`

Worker：
- 每个未终态 target 设 RUNNING，用 `context.WithTimeout(ctx, TargetTimeout)` 调 `Exec.Execute`
- `MapExecError`：nil → SUCCESS；否则按锁定表映射
- 全部完成后 `UpdateBatchStatus` 为 `Rollup` 结果
- 并发用 semaphore（buffered chan 或 `errgroup.SetLimit`）

- [ ] **Step 1: Write the failing test**

```go
type stubExec struct {
	fn func(ctx context.Context, t batch.Target) error
}

func (s stubExec) Execute(ctx context.Context, t batch.Target, _ batch.Type) error {
	return s.fn(ctx, t)
}

func TestEngine_WorkerPartialTimeout(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-ok", "op-to"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1", Concurrency: 2, TargetTimeout: 50 * time.Millisecond,
		Expand: stubExpand{targets: []batch.Target{
			{NodeID: "n1", ProcessID: "p-ok", ProcessName: "ok"},
			{NodeID: "n2", ProcessID: "p-to", ProcessName: "to"},
		}},
		Exec: stubExec{fn: func(ctx context.Context, t batch.Target) error {
			if t.ProcessID == "p-to" {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p-ok", "p-to"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusPartial)
	got, _ := e.Get(ctx, b.BatchID)
	var sawTO, sawOK bool
	for _, tg := range got.Targets {
		if tg.Status == batch.TargetTimeout {
			sawTO = true
		}
		if tg.Status == batch.TargetSuccess {
			sawOK = true
		}
	}
	if !sawTO || !sawOK || got.Summary.Timeout != 1 || got.Summary.Success != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestMapExecError(t *testing.T) {
	if batch.MapExecError(errcode.E(errcode.TIMEOUT, "x")) != batch.TargetTimeout {
		t.Fatal("timeout")
	}
	if batch.MapExecError(errcode.E(errcode.UNAVAILABLE, "x")) != batch.TargetUnavailable {
		t.Fatal("unavailable")
	}
	if batch.MapExecError(errcode.E(errcode.DENIED, "x")) != batch.TargetDenied {
		t.Fatal("denied")
	}
	if batch.MapExecError(errcode.E(errcode.CONFLICT, "x")) != batch.TargetConflict {
		t.Fatal("conflict")
	}
}
```

`waitBatch`：最多 2s 轮询 `Get`，直到 `Status` 匹配。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/batch -run 'TestEngine_WorkerPartialTimeout|TestMapExecError' -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`Start` 启动内部 `jobs chan string`（batch_id）。`Create` 在 `InsertBatch` 成功后非阻塞投递 `batch_id`。worker 设置 RUNNING，按 Concurrency 跑 targets。`TargetTimeout<=0` 当 30s。不要把 TIMEOUT 写成 FAILED。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/batch -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/batch
git commit -m "feat(batch): run targets with timeout and partial rollup"
```

---

### Task 5: RetryFailed、ReplayTimeout、Resume

**Files:**
- Modify: `internal/batch/engine.go`
- Modify: `internal/batch/engine_test.go`

**Interfaces:**
- Produces:
  - `func (e *Engine) RetryFailed(ctx, id, operator string) (Batch, error)`
  - `func (e *Engine) ReplayTimeout(ctx, id, operator string) (Batch, error)`
  - `func (e *Engine) Resume(ctx) error` — 列出所有 PENDING/RUNNING target，按所属 batch 重新投递，**不**改已有 `operation_id`

RetryFailed：挑 FAILED/DENIED/CONFLICT/UNAVAILABLE/INVALID；每个生成**新** `operation_id`，把旧行更新为新 op（或删旧插新，但 Get 只保留一行/target：以 `(batch_id, process_id, node_id)` 为逻辑身份）。推荐：更新该行 `operation_id`+`status=PENDING`+清空 error/finished。SUCCESS / TIMEOUT 不动。

ReplayTimeout：只挑 TIMEOUT；**保持**原 `operation_id`，status 改回 PENDING，清空 finished。SUCCESS 不动。

两者：若没有任何可挑 target → `INVALID`（`nothing to retry` / `nothing to replay`）。有则置 batch `RUNNING` 并投递。

Resume：`ListIncompleteTargets`，按 batch_id 去重投递。已 TIMEOUT 不在 incomplete 里，不得重放。

- [ ] **Step 1: Write the failing test**

```go
func TestEngine_RetryFailedNewOperationID(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-old"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1",
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n", ProcessID: "p", ProcessName: "x"}}},
		Exec:   stubExec{fn: func(context.Context, batch.Target) error { return errcode.E(errcode.INVALID, "boom") }},
		NewID:  func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeStart, batch.Selector{ProcessIDs: []string{"p"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusFailed)
	ids = []string{"op-new"}
	e.Exec = stubExec{fn: func(context.Context, batch.Target) error { return nil }}
	got, err := e.RetryFailed(ctx, b.BatchID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusCompleted)
	got, _ = e.Get(ctx, b.BatchID)
	if got.Targets[0].OperationID != "op-new" {
		t.Fatalf("want new op, got %s", got.Targets[0].OperationID)
	}
}

func TestEngine_ReplayTimeoutReusesOperationID(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ids := []string{"b1", "op-to"}
	e := &batch.Engine{
		DB: st, SourceAgent: "n1", TargetTimeout: 20 * time.Millisecond,
		Expand: stubExpand{targets: []batch.Target{{NodeID: "n", ProcessID: "p", ProcessName: "x"}}},
		Exec: stubExec{fn: func(ctx context.Context, _ batch.Target) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	e.Start(ctx)
	b, _ := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p"}}, "")
	waitBatch(t, e, b.BatchID, batch.StatusPartial)
	before, _ := e.Get(ctx, b.BatchID)
	old := before.Targets[0].OperationID
	e.Exec = stubExec{fn: func(context.Context, batch.Target) error { return nil }}
	if _, err := e.ReplayTimeout(ctx, b.BatchID, "admin"); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, b.BatchID, batch.StatusCompleted)
	after, _ := e.Get(ctx, b.BatchID)
	if after.Targets[0].OperationID != old {
		t.Fatalf("replay must reuse %s, got %s", old, after.Targets[0].OperationID)
	}
}

func TestEngine_ResumeDoesNotReplaySuccessOrTimeout(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	// 手工插入：一个 SUCCESS、一个 TIMEOUT、一个 PENDING
	_ = st.InsertBatch(ctx, store.BatchRecord{
		BatchID: "b1", Operator: "a", SourceAgent: "n", Type: "RESTART",
		SelectorJSON: `{}`, CreatedAt: time.Now().UTC(), Status: "RUNNING", SummaryJSON: `{}`,
	}, []store.BatchTargetRecord{
		{BatchID: "b1", OperationID: "op-s", NodeID: "n", ProcessID: "s", Status: "SUCCESS"},
		{BatchID: "b1", OperationID: "op-t", NodeID: "n", ProcessID: "t", Status: "TIMEOUT"},
		{BatchID: "b1", OperationID: "op-p", NodeID: "n", ProcessID: "p", Status: "PENDING"},
	})
	var ran []string
	e := &batch.Engine{DB: st, SourceAgent: "n", Exec: stubExec{fn: func(_ context.Context, t batch.Target) error {
		ran = append(ran, t.OperationID)
		return nil
	}}}
	e.Start(ctx)
	if err := e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e, "b1", batch.StatusPartial)
	if len(ran) != 1 || ran[0] != "op-p" {
		t.Fatalf("ran %v", ran)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/batch -run 'TestEngine_RetryFailedNewOperationID|TestEngine_ReplayTimeoutReusesOperationID|TestEngine_ResumeDoesNotReplaySuccessOrTimeout' -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`UpdateTarget` 若需要改 `operation_id`：先实现 `store.ReplaceTargetOp(ctx, batchID, oldOp, rec)` 或删+插同一事务。不要在另一条行上留旧 op。SUCCESS 行绝对不要改。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/batch ./internal/store -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/batch internal/store
git commit -m "feat(batch): retry failed, replay timeout, resume incomplete"
```

---

### Task 6: 展开器（显式 / Agent Group / Process Group）

**Files:**
- Create: `internal/batch/expand.go`
- Test: `internal/batch/expand_test.go`

**Interfaces:**
- Consumes: Task 2 Selector；`cluster.NodeSummary` / `cluster.ProcessSummary` 仅作 DTO（`batch` 可以定义自己的 `NodeView` 避免 import 环，推荐本地 DTO）
- Produces:
  - `type NodeView struct { NodeID string; Processes []ProcView }`
  - `type ProcView struct { ProcessID, Name, Group string; LatestRevision int64 }`
  - `type OwnerSpec struct { ProcessID, Name, NodeID, Group string; LatestRevision int64; SpecJSON string }` — SpecJSON 仅 CONFIG_UPDATE 需要
  - `type ClusterView interface { Nodes() []NodeView }`
  - `type GroupMembers interface { Members(groupID string) ([]string, error) }` — 未知组返回 `INVALID`
  - `type SpecReader interface { Get(ctx, nodeID, idOrName string) (OwnerSpec, error) }`
  - `type Authorizer interface { Allow(nodeID, processGroup, perm string) error }` — DENIED 时返回 `errcode.DENIED`
  - `type RealExpander struct { Cluster ClusterView; Groups GroupMembers; Specs SpecReader; Auth Authorizer; ConfigOverlay func(OwnerSpec) (payloadJSON string, expected int64, err error) }`
  - `func (x *RealExpander) Expand(ctx, sel, typ) ([]Target, error)`

展开规则（必须按此实现）：

1. **显式 `process_ids`：** 在 `Cluster.Nodes()` 里找 `ProcessID`；找到则 `SpecReader.Get(node, processID)` 核对存在。Gossip 没有但 Get 成功也可以（以 Owner 为准）。Get `NOT_FOUND` → 该 id 作为 target，`INVALID`（`process not found`），**保留**。
2. **`process_names`：** 对每个 `(node_id, process_name)` 调 `Get(node, name)`。失败同上。
3. **`agent_group_id`：** `Groups.Members`；对每个 member 的 Gossip 进程列表作为候选，再 `Get` 确认存在。组成员变更不影响已展开结果（本函数只读当前快照）。
4. **`process_group`：** Gossip `ProcView.Group == name` 初筛；每个候选必须 `Get`；`OwnerSpec.Group != name` → target `INVALID`（`group mismatch`），不得丢弃。
5. **权限：** `perm` 按 type：`process.start` / `process.stop` / `process.restart` / `process.config.update`。`Allow` 返回 DENIED → target `DENIED`，仍保留。
6. **去重** `(node_id, process_id)`。
7. CONFIG_UPDATE：`ConfigOverlay` 填 `PayloadJSON` + `ExpectedRevision`。overlay 失败该 target `INVALID`。

- [ ] **Step 1: Write the failing test**

```go
type memCluster struct{ nodes []batch.NodeView }

func (m memCluster) Nodes() []batch.NodeView { return m.nodes }

type memGroups map[string][]string

func (m memGroups) Members(id string) ([]string, error) {
	v, ok := m[id]
	if !ok {
		return nil, errcode.E(errcode.INVALID, "agent group")
	}
	return v, nil
}

type memSpecs map[string]batch.OwnerSpec // key nodeID+"/"+idOrName

func (m memSpecs) Get(_ context.Context, node, id string) (batch.OwnerSpec, error) {
	s, ok := m[node+"/"+id]
	if !ok {
		return batch.OwnerSpec{}, errcode.E(errcode.NOT_FOUND, "process")
	}
	return s, nil
}

type denyAuth struct{}

func (denyAuth) Allow(_, _, _ string) error { return errcode.E(errcode.DENIED, "permission denied") }

type allowAuth struct{}

func (allowAuth) Allow(_, _, _ string) error { return nil }

func TestExpand_ProcessGroupVerifiesOwnerAndKeepsMismatch(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{
			NodeID: "n1",
			Processes: []batch.ProcView{
				{ProcessID: "p-pay", Name: "pay", Group: "finance"},
				{ProcessID: "p-stale", Name: "ad", Group: "finance"}, // gossip 过期
			},
		}}},
		Specs: memSpecs{
			"n1/p-pay":   {ProcessID: "p-pay", Name: "pay", NodeID: "n1", Group: "finance"},
			"n1/p-stale": {ProcessID: "p-stale", Name: "ad", NodeID: "n1", Group: "ads"},
		},
		Auth: allowAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessGroup: "finance"}, batch.TypeRestart)
	if err != nil || len(ts) != 2 {
		t.Fatalf("%+v %v", ts, err)
	}
	var pay, stale batch.Target
	for _, t0 := range ts {
		if t0.ProcessID == "p-pay" {
			pay = t0
		}
		if t0.ProcessID == "p-stale" {
			stale = t0
		}
	}
	if pay.Status != "" && pay.Status != batch.TargetPending {
		t.Fatalf("pay %s", pay.Status)
	}
	if stale.Status != batch.TargetInvalid {
		t.Fatalf("mismatch must be INVALID, got %s", stale.Status)
	}
}

func TestExpand_DeniedKept(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{NodeID: "n1", Processes: []batch.ProcView{{ProcessID: "p1", Name: "x"}}}}},
		Specs:   memSpecs{"n1/p1": {ProcessID: "p1", Name: "x", NodeID: "n1"}},
		Auth:    denyAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessIDs: []string{"p1"}}, batch.TypeStart)
	if err != nil || len(ts) != 1 || ts[0].Status != batch.TargetDenied {
		t.Fatalf("%+v %v", ts, err)
	}
}
```

再补：非法 `process_group` 名（含空格）→ Expand 返回 `INVALID` error（不是 target）。未知 `agent_group_id` → Expand 返回 `INVALID`。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/batch -run TestExpand_ -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

按规则实现。Expand 返回的「可执行」target Status 留空或 PENDING；DENIED/INVALID 在 Expand 阶段就写好，worker 见到这两种终态必须**跳过 Exec**（Task 4 worker 应只执行 PENDING；若还没做，本任务改 worker：DENIED/INVALID/SUCCESS/... 终态不跑）。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/batch -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/batch
git commit -m "feat(batch): expand selectors with owner group verify"
```

---

### Task 7: Proto BatchService + 代码生成

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Generated: `proto/procmesh/v1/api.pb.go`、`proto/procmesh/v1/procmeshv1connect/api.connect.go`、`web/src/gen/procmesh/v1/api_pb.ts`

**Interfaces:**
- Consumes: 上文锁定的 proto
- Produces: `procmeshv1connect.BatchServiceHandler` / `BatchServiceClient`

- [ ] **Step 1: Write the failing test**

`internal/api/proto_gen_test.go` 若已有「服务名列表」测试，追加 `BatchService`。没有则在 `internal/api/batch_test.go` 写：

```go
var _ procmeshv1connect.BatchServiceHandler = (*BatchAPI)(nil)
```

本任务先只加 proto + generate；`BatchAPI` 空壳可在本任务末尾加一个 `type BatchAPI struct{}` 五个方法全部 `connect.NewError(connect.CodeUnimplemented, errcode.E(errcode.UNAVAILABLE, "not implemented"))`，让编译通过。完整逻辑在 Task 9。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestProto_ -count=1`

Expected: FAIL 或编译失败（无 BatchService）

- [ ] **Step 3: Write proto and generate**

把锁定的 message/service 追加到 `api.proto`（`GroupService` 之后、`AuditService` 之前或文件末尾均可，保持现有风格）。

```bash
make proto
cd web && npm ci && cd .. && make proto-ts
```

不要手改生成文件。

- [ ] **Step 4: Run tests**

Run: `go test ./proto/... ./internal/api -count=1`

Expected: 编译通过；现有测试 PASS

- [ ] **Step 5: Commit**

```bash
git add proto web/src/gen internal/api
git commit -m "feat(proto): add BatchService"
```

---

### Task 8: agent.yaml `batch.*` 配置

**Files:**
- Modify: `internal/agentcfg/load.go`
- Test: `internal/agentcfg/load_test.go`

**Interfaces:**
- Produces:
  - `type Batch struct { MaxConcurrency int; TargetTimeout time.Duration }`
  - `Config.Batch`
  - 默认 `MaxConcurrency=16`、`TargetTimeout=30s`
  - `max_concurrency` 必须在 1–64，否则 `INVALID`
  - `target_timeout` 必须 `>0`，否则 `INVALID`

YAML：

```yaml
batch:
  max_concurrency: 16
  target_timeout: 30s
```

缺省块保持默认。部分字段只覆盖有值的。

- [ ] **Step 1: Write the failing test**

```go
func TestLoadAll_BatchDefaultsAndOverride(t *testing.T) {
	cfg, err := agentcfg.LoadAll(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil || cfg.Batch.MaxConcurrency != 16 || cfg.Batch.TargetTimeout != 30*time.Second {
		t.Fatalf("%+v %v", cfg.Batch, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte("batch:\n  max_concurrency: 4\n  target_timeout: 2s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = agentcfg.LoadAll(path, true)
	if err != nil || cfg.Batch.MaxConcurrency != 4 || cfg.Batch.TargetTimeout != 2*time.Second {
		t.Fatalf("%+v %v", cfg.Batch, err)
	}
}

func TestLoadAll_BatchRejectsOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("batch:\n  max_concurrency: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.LoadAll(path, true); err == nil {
		t.Fatal("expected INVALID")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agentcfg -run TestLoadAll_Batch -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`file` 增加 `Batch *batchFile`。`batchFile` 用 `*int` / `string` 以区分未设。解析 duration 用 `time.ParseDuration`。`LoadAll` 在无文件时也填 Batch 默认值（现在缺文件只填 Disk——必须补上，否则 `LoadAll(..., false)` 的默认测试失败）。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agentcfg -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agentcfg
git commit -m "feat(agentcfg): add batch concurrency and target timeout"
```

---

### Task 9: BatchAPI + RBAC + audit + 指标 + Agent 接线

**Files:**
- Create: `internal/api/batch.go`
- Test: `internal/api/batch_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/metrics.go`、`internal/api/metrics_test.go`
- Modify: `internal/agent/run.go`

**Interfaces:**
- Consumes: `batch.Engine`、`auth.Service`、`store.AppendAudit`、`Router.Members`、control `View().AgentGroups`
- Produces: 可用的 `BatchService`；`/metrics` 增加：

```
procmesh_batch_running
procmesh_batch_targets_total{status="success"}
... 对 failed/timeout/denied/conflict/unavailable/invalid 同样
```

`batch_running`：当前 `status=RUNNING` 的 batch 数（gauge）。`batch_targets_total`：本机所有 batch 的 summary 累加（gauge，按 status）。

API 规则：
- 所有方法：已入群时需登录。读（Get/List/Export）需要 `batch.execute`（与 Operator 角色一致；Viewer 无此 perm → DENIED）。写需要 `batch.execute` + 非空 `operation_id`。
- Create：`metaOf`；构造 `RealExpander`：Cluster=`Members()` 映射 `NodeView`；Groups=Raft `AgentGroups[id].MemberNodeIDs`；Specs=经 ProcessAPI hop 的 `GetProcess`（本地 `Mgr.Resolve` / 远程 `Forward.Process`）；Auth=`AllowOn` 对应 process perm。CONFIG_UPDATE overlay：读 Owner spec，覆盖非空字段，序列化进 payload。
- Get 缺 id → `NOT_FOUND`。List limit 交给 Engine。
- Retry/Replay 调 Engine。
- Create/Retry/Replay 成功后 `AppendAudit`：`action=batch.create|batch.retry_failed|batch.replay_timeout`，`resource=batch:<id>`，`operation_id` 为请求 meta 的 id。
- 入口 worker 对已是 DENIED/INVALID 的 target 不 Exec。可执行 target 调 ProcessAPI 等价路径：`StartProcess`/`StopProcess`/`RestartProcess`/`UpdateConfig`，header 带该 target 的 `operation_id`，`Procmesh-Target-Node=node_id`，并 `stampIdentity`。把返回错误 `MapCallError` 后交给 Engine。

Agent 接线（`run.go`）：
- `eng := &batch.Engine{DB: st, Concurrency: cfg.Batch.MaxConcurrency, TargetTimeout: cfg.Batch.TargetTimeout, SourceAgent: nodeID, NewID: uuid}`
- Expander/Executor 在 `NewServer` 之后也能设；更干净：`api.Options.Batch *batch.Engine`，`BatchAPI` 在 handler 里把 Expand/Exec 补全（需要 Auth/Router/Mgr/Forward）。
- `eng.Start(ctx)`；启动后 `eng.Resume(ctx)`。
- store 损坏/DEGRADED：不要启动 worker；Create 返回 `DEGRADED`。

- [ ] **Step 1: Write the failing test**

`internal/api/batch_test.go`：用内存 store + stub expander/exec 的 Engine（与 Task 3–4 相同），挂 `BatchAPI{Auth, Engine, Store}`，不走真集群。

```go
func TestBatchAPI_CreateRequiresOperationIDAndPerm(t *testing.T) {
	// newBootstrappedAuth + Operator 用户
	// 无 operation_id → INVALID
	// Viewer 登录 Create → DENIED
	// Operator Create 空 selector → INVALID
	// Operator + stub expand 一个 target → 200，batch_id 非空，status PENDING 或 RUNNING
}

func TestBatchAPI_ListIsLocalOnly(t *testing.T) {
	// Create 一条后 List 能见到；Get missing → NOT_FOUND
}
```

`metrics_test.go`：渲染结果必须包含 `procmesh_batch_running` 与 `procmesh_batch_targets_total`。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run 'TestBatchAPI_|TestMetrics_' -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`NewServer` 增加：

```go
if opts.Batch != nil {
    bp, bh := procmeshv1connect.NewBatchServiceHandler(&BatchAPI{
        Auth: opts.Auth, Engine: opts.Batch, Store: batchAuditStore(opts), LocalID: opts.LocalID,
        Mgr: opts.Mgr, Router: opts.Router, Forward: opts.Forward,
    }, intercept)
    mountConnect(engine, bp, bh)
}
```

`renderMetrics` 增加 batch 参数或从 `opts.Batch` 读。保持旧测试：可给零值。

`run.go` 在 `NewServer` 前构造 Engine，传入 `Options.Batch`，`Start`+`Resume`。

Executor 实现放 `internal/api/batch_exec.go`：调用本包已有 `hop` / `remoteProcess` / `Mgr.SetDesired|Restart` / Config Update。不要复制一份新的 CAS。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api ./internal/batch ./internal/store ./internal/agentcfg -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/agent internal/batch
git commit -m "feat(api): BatchService with RBAC audit and metrics"
```

---

### Task 10: CLI

**Files:**
- Create: `internal/cli/batch.go`
- Modify: `internal/cli/root.go`、`internal/cli/client.go`、`internal/cli/root_test.go`

**Interfaces:**
- Produces 命令：

```text
procmesh batch create --type start|stop|restart|apply [--process-id ID]... [--process-name NODE:NAME]... [--agent-group-id ID] [--process-group NAME] [--file spec.yaml]
procmesh batch get BATCH_ID
procmesh batch list
procmesh batch retry BATCH_ID
procmesh batch replay-timeout BATCH_ID
procmesh batch export BATCH_ID [--format json|csv]
```

`--type apply` 映射 `CONFIG_UPDATE`，必须 `--file`（复用现有 spec 解析）。输出纯文本 `batch_id=` `status=` `success=` `failed=` `timeout=` ... 以及 get 时每行 `target process= name= node= status= op=`。

- [ ] **Step 1: Write the failing test**

`root_test.go`：`usageText` 含 `batch create`、`batch replay-timeout`；`parseArgs` 识别 `--type restart --process-id p1`、`--format csv`、`--process-group finance`、`--agent-group-id g1`。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -count=1`

Expected: FAIL（unknown command batch 或 usage 缺行）

- [ ] **Step 3: Write minimal implementation**

`client.batch = NewBatchServiceClient`。`Main` 加 `case "batch":`。flags：`--type`、`--process-id`（可重复）、`--process-name`（可重复 `node:name`）、`--agent-group-id`、`--process-group`、`--format`。`--file` 已有。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cli -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): batch create get list retry replay-timeout export"
```

---

### Task 11: Web Batches 页 + Overview Recent Batches + i18n

**Files:**
- Modify: `web/src/lib/rpc.ts`、`web/src/router.ts`、`web/src/components/AppShell.vue`
- Create: `web/src/pages/BatchesPage.vue`、`web/src/pages/BatchesPage.test.ts`
- Modify: `web/src/pages/OverviewPage.vue`、`web/src/pages/OverviewPage.test.ts`
- Modify: `web/public/locales/en/common.json`、`web/public/locales/zh/common.json`
- Create: `web/e2e/batch.spec.ts`

**Interfaces:**
- Consumes: 生成的 `BatchService`
- Produces: `/batches` 列表+创建+详情；Overview「Recent Batches（仅当前入口）」；TIMEOUT 琥珀而非绿

页面要求：
1. 导航：`batch.execute` 才显示 Batches（lucide `ListTodo` 或 `Layers2`），在 Groups 后、Users 前。
2. 列表：batch_id、type、status 徽章、summary 四计数（至少 success/failed/timeout）、created。页顶横幅：`batch.localOnly`（中英都要写明「只显示本入口创建的任务」）。
3. 创建：type 下拉；selector（process ids 逗号分隔 / process group / agent group id）；CONFIG_UPDATE 显示「V1.1 config overlay 走 CLI `--file`」可先只支持 start/stop/restart 表单，**必须**仍能从详情页操作已有 CONFIG_UPDATE 任务。若实现表单 overlay 过重，创建 UI 只做 START/STOP/RESTART，CONFIG_UPDATE 仍走 CLI——但详情/重试/导出必须支持四种 type。
4. 详情：target 表；status 用锁定色；TIMEOUT 行 `data-status="TIMEOUT"`；按钮 Retry Failed / Replay Timeout / Export JSON。
5. Overview：`listBatches({limit:5})` 卡片，标题 `overview.recentBatches`，副文案 `overview.recentBatchesHint`（仅当前入口）。TIMEOUT 计数可见。
6. Mutation 带 `operationId` UUID。
7. `npm run i18n:check` 必须过。

Playwright `web/e2e/batch.spec.ts`：
- 若 E2E 环境有登录：进 `/batches`，mock 或真实视现有 e2e 模式。看 `list.spec.ts` 怎么打到页面。
- 最低：mount 级 Vitest 已覆盖 TIMEOUT 非绿；Playwright 断言 Batches 导航存在、横幅可见、若能注入 list mock 则 TIMEOUT 徽章 class 不含 success 绿。若 e2e 只能打真实 agent，则断言登录后 `/batches` 空列表+横幅，不 404。

- [ ] **Step 1: Write the failing test**

`BatchesPage.test.ts`：mock `listBatches` 返回一条 `PARTIAL`，targets 含 SUCCESS 与 TIMEOUT；断言 TIMEOUT 元素存在且计算样式/class 不是 success 绿（用 `data-status="TIMEOUT"` + class `status-timeout`）。有 `batch.execute` 才渲染创建区。

`OverviewPage.test.ts`：提供 `batchClient.listBatches` 返回一条含 `timeout: 2` 的 batch，断言 hint 文案与 timeout 可见。

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- BatchesPage OverviewPage AppShell`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`rpc.ts` 增加 `BatchService` + `useBatchClient()`。`router.ts`：`batches`、`batches/:id` 都用 `BatchesPage`（用 route param 切列表/详情）。locales 键：`nav.batches`、`batch.title`、`batch.localOnly`、`batch.retryFailed`、`batch.replayTimeout`、`batch.export`、`batch.timeout`、`overview.recentBatches`、`overview.recentBatchesHint`。中英都写。

- [ ] **Step 4: Run tests**

Run:

```bash
cd web && npm test && npm run i18n:check
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web
git commit -m "feat(web): Batches page, TIMEOUT colors, recent batches"
```

---

### Task 12: Case 7 验收、崩溃恢复、embed、索引

**Files:**
- Create: `internal/agent/q2_accept_test.go`
- Modify: `docs/superpowers/plans/2026-08-16-v1.1.md`
- Modify: `docs/superpowers/plans/2026-08-16-q2-batch-operations.md`（本文件 Status）
- Rebuild: `internal/web/dist`（`make web` 后确保 embed 源更新）

**Interfaces:**
- Consumes: 全部前序任务 + 现有 `startClusterAgent` / `joinTwo` / `mustCLI` / `waitGossipName`

- [ ] **Step 1: Write the failing tests**

```go
func TestQ2_Case7_PartialTimeoutVisible(t *testing.T) {
	addrA, _ := startClusterAgent(t, "")
	addrC, rootC := startClusterAgent(t, "")
	idC := readNodeID(t, rootC)
	joinTwo(t, addrA, addrC)
	initAndLogin(t, addrA)

	// 两边各一个进程
	mustCLI(t, addrA, "process", "apply", "--file", writeSleepSpecNamed(t, "local"), "--expected-revision", "0")
	mustCLI(t, addrC, "process", "apply", "--file", writeSleepSpecNamed(t, "remote"), "--expected-revision", "0")
	mustCLI(t, addrA, "process", "start", "local")
	mustCLI(t, addrC, "process", "start", "remote")
	waitGossipName(t, addrA, "remote")

	// 让 C 不可达：取消 C 的 agent（shim 可留；RPC 必须断）
	stopAgentC(t) // 用 startClusterAgentAtCtl 的 cancel；若 helper 没有，本测试改用 startClusterAgentCtl

	out := mustCLI(t, addrA, "batch", "create", "--type", "restart",
		"--process-name", currentNodeID(t, addrA)+":local",
		"--process-name", idC+":remote")
	bid := parseKV(out, "batch_id")
	waitCLIBatch(t, addrA, bid, "PARTIAL")

	got := mustCLI(t, addrA, "batch", "get", bid)
	if !strings.Contains(got, "timeout=") && !strings.Contains(got, "unavailable=") {
		t.Fatalf("Case 7 must expose timeout/unavailable: %s", got)
	}
	if strings.Contains(got, "status=COMPLETED") {
		t.Fatalf("must not hide partial: %s", got)
	}
	// TIMEOUT 或 UNAVAILABLE 行必须仍在 target 列表
	if !strings.Contains(got, "remote") {
		t.Fatalf("target disappeared: %s", got)
	}
}

func TestQ2_ResumeDoesNotReplaySuccess(t *testing.T) {
	// 单节点：create restart 一个进程至 COMPLETED
	// 再对同一入口重启 agent（同一 data-dir startClusterAgentAt）
	// 进程 PID 不得因 Resume 再变（operation_id 幂等 / SUCCESS 不重放）
}
```

若「停 C」只能得到 UNAVAILABLE：测试允许 `timeout=` 或 `unavailable=` > 0，且 batch `PARTIAL`，且 UI/CLI 不把该 target 标 SUCCESS。另在同文件加一个**单进程 + 不可路由 Owner** 的用例：selector 显式 `process-name` 指向 `192.0.2.1` 不可用节点难以走通（gossip 没有进程）。更稳：在 `q2_accept_test.go` 之外保留 Task 4 单测作为 TIMEOUT 法定证据；本 Case 7 集成测「部分不可达 → PARTIAL + 非成功状态可见」。

崩溃恢复：`startClusterAgentCtl` 拿到 cancel，Create 后等 COMPLETED，记录 PID，cancel，`startClusterAgentAt` 同 root，等 HTTP 起来，再 `process get` PID 不变。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent -run TestQ2_ -count=1 -timeout 180s`

Expected: FAIL（无 batch CLI 或未接线）

- [ ] **Step 3: Implement helpers + embed + index**

补 `writeSleepSpecNamed`、`waitCLIBatch`（轮询 `batch get`）。修复集成中发现的接线缺口（只修 Q2 范围）。

```bash
make web
# 确认 internal/web/dist 随构建更新后纳入提交
```

更新 `docs/superpowers/plans/2026-08-16-v1.1.md`：Q2 一行改为指向本文件并标「实施中/已完成」。本 plan 文首加 `**Status:** 已完成`（仅当测试全绿）。

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/batch ./internal/store ./internal/api ./internal/cli ./internal/agentcfg ./internal/agent -run 'TestQ2_|TestBatch|TestEngine_|TestExpand_' -count=1 -timeout 180s
cd web && npm test && npm run i18n:check
go test ./internal/batch -cover
```

Expected: PASS；`internal/batch` coverage ≥ 80%。

- [ ] **Step 5: Commit**

```bash
git add internal/agent internal/web/dist docs/superpowers/plans
git commit -m "test(agent): Q2 Case 7 partial batch and resume idempotency"
```

---

## 自检（撰写后）

| Spec 要求 | 任务 |
|-----------|------|
| 入口本地 SQLite，不进 Raft/Gossip | 1, 3, 9 |
| Create 立即返回 PENDING + worker | 3, 4 |
| 结束态 COMPLETED/PARTIAL/FAILED；TIMEOUT≠FAILED | 2, 4 |
| 零 target INVALID 不插行 | 3 |
| Process Group Gossip 初筛 + Owner 核对，不匹配保留 INVALID | 6 |
| Agent Group 读 Raft 成员快照 | 6, 9 |
| DENIED 保留 | 6 |
| 并发 16、超时 30s、可配 1–64 | 4, 8 |
| TIMEOUT vs UNAVAILABLE 映射 | 4, 9 |
| RetryFailed 新 op；ReplayTimeout 复用 op；SUCCESS 不重放 | 5 |
| Resume 只恢复 PENDING/RUNNING | 5, 12 |
| batch.execute + 每 target process perm | 9 |
| Batch audit | 9 |
| CLI 全套 | 10 |
| Batches 页 + Overview 本入口 + TIMEOUT 非绿 | 11 |
| Case 7 脚本 | 12 |
| 指标 `batch_running` / `batch_targets_total` | 9 |
| 不做 command.execute.batch / 全局 batch 总线 | 全局约束 |

无 TBD/TODO 占位。后续任务使用的名字与 Task 2–3 的 `Engine`/`Expander`/`Executor`/`Rollup` 一致。
