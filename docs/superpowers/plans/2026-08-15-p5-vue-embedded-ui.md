# P5 Vue Embedded UI + Freshness + Remote Config/Logs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在已完成的 P4 登录与 RBAC 之上，交付嵌入 Agent 的 Vue3 管理后台：任意节点打开都能看集群，Gossip 字段带 LIVE/STALE/UNKNOWN，远程配置/日志走 Write-to-Owner，浏览器可管集群。

**Architecture:** `:18680` 用 `go:embed` 提供 SPA。浏览器只打入口 Agent 的 ConnectRPC（cookie 会话 + CSRF）。集群列表与 Overview 读本机 Gossip 视图；Process 详情、配置、日志、启停经已有 hop 到 Owner。Audit 本机列出并按需 RPC 聚合，缺失节点标 STALE。`internal/web` 无业务逻辑。`process` 不得 import `cluster` / `control` / `rpc` / `auth` / `web`。

**Tech Stack:** Go 1.23、已有 ConnectRPC + Gin、Vue 3.5 + Vite 6 + TypeScript 5.7 + Tailwind CSS v4 + shadcn-vue + TanStack Vue Query 5 + Connect-Web、Vitest、Playwright、`go:embed`。Node.js ≥ 20。

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- Go 版本下限：`1.23`
- CGO-free SQLite only：`modernc.org/sqlite`（禁止 `mattn/go-sqlite3`）
- Linux 是生产保证面；macOS 必须能编译并跑单测 + 非 cgroup 集成
- `process` 不得 import `cluster`、`control`、`rpc`、`auth` 或 `web`
- `cluster` 与 `process` 只交换 summary DTO（经接口/回调）
- `internal/web` **无业务逻辑**，只 embed 构建产物并提供 `http.Handler`
- 日志正文只在文件里，不进 SQLite
- **禁止把 Process runtime / logs / 全量 spec 写入 Raft**
- 所有 Mutation 必须带非空 `operation_id`（UUID）；Web 客户端自己生成
- 无 `operation_id` 的远程写必须拒绝（`INVALID`）
- 无 `expected_revision` 的配置写必须拒绝
- 错误码沿用 `internal/errcode`：`OK`、`CONFLICT`、`UNAVAILABLE`、`TIMEOUT`、`DENIED`、`DEGRADED`、`DUPLICATE_NODE_ID`、`INCOMPATIBLE_VERSION`、`NOT_FOUND`、`INVALID`
- 应用错误码放在 Connect error detail（`ErrorInfo.code`），消息为英文
- 对外主协议是 ConnectRPC；REST 仅 `/healthz`、`/readyz`、`/metrics`，以及嵌入式 Web 静态资源
- 监听默认 `127.0.0.1:18680`、`127.0.0.1:18683`、`127.0.0.1:18685`、`127.0.0.1:18689`；非环回必须 `--insecure-listen`
- **`cluster init` 成功后必须关闭环回无认证。** 禁止在已入群节点上保留无认证入口
- Agent RPC 必须 mTLS。入口不改权威副本。Owner 不信任入口的「已授权」声明
- 远程 Mutation 必须 Direct RPC 到 Owner，禁止「改本地副本再 Gossip」
- 禁止因对端 FAILED 而在本机创建对方的 Process
- Session：Cookie `procmesh_session` HttpOnly + SameSite=Lax + TTL 12h；Mutation 必须带 `X-CSRF-Token`
- 所有来自 Gossip 的字段必须显示 `last_updated_at` 与新鲜度徽章：`LIVE` / `STALE` / `UNKNOWN`
- **禁止把 STALE 画成绿色「正常」**
- LIVE 阈值：`age <= 10s` 且节点 `ALIVE`（PRD §87 Alive Agent Summary 95% < 10s）
- Dashboard 必须同时展示「ProcMesh 自身」与「业务 Process」
- macOS 降级必须在 UI 显式说明，禁止假装 Linux 语义
- V1.0 不做：批量操作、Agent/Process Group、告警通道、配置备份、历史指标、MFA、`command.execute` 入口、Adopt UI
- 不实现第二套 REST 资源模型
- 测试与代码同目录（Go）；前端单测在 `web/src/**/*.test.ts`；Playwright 在 `web/e2e/`
- 强制 TDD：先红后绿
- P0 覆盖率门槛保持：`internal/process`、`internal/shim`、`internal/store` ≥ 80%
- `internal/control` 与 `internal/auth` 覆盖率门槛 ≥ 80%
- 文档与本计划使用中文；API 错误码与错误消息、UI 英文文案使用英文
- 生成的 proto Go / TS 文件禁止手改；改完 proto 必须 `make proto` 与 `make proto-ts`
- 工作目录是本 worktree，提交写在 `feat/p5-embedded-web`

## 规格解读（P5 边界）

来源：`docs/v2-prd/v2-prd.md` 与 `docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。冲突以架构 spec 为准。

1. **P5 可演示出口**（spec §13）：浏览器管集群。任意健康 Agent 打开 Web，能看 Overview / Node / Process，对 Owner 启停与改配置，看远程日志。
2. **页面**（spec §11.3 / PRD §42–46）：登录、Cluster Overview、Node 详情、Process 列表与详情（desired/observed/health、revision、启停、配置编辑、历史 diff、日志、即时指标）、Users / Roles、Audit。
3. **新鲜度**（spec §11.3 / PRD §44 / §94）：Gossip 字段带 `last_updated_at` + LIVE/STALE/UNKNOWN。FAILED 节点上的进程显示 Last Known + STALE，禁止绿色 RUNNING。
4. **自身健康**（spec §15 / PRD §83）：Control / Gossip / RPC、版本分布、证书到期。`/readyz` 降级 ≠ 业务进程故障。
5. **远程读写**（PRD §4.5 / §47 / §56）：读可用 Gossip 或 hop；写必须 hop 到 Owner。日志不复制到入口。
6. **配置 CAS**（PRD §34–36）：`expected_revision`；冲突 `CONFLICT` + Connect `FailedPrecondition` + HTTP 409。Rollback 写新 revision。`latest != active` 显示 `Configuration changed. Restart required.`
7. **审计**（spec §11.1 / PRD §72–74）：本机 + 按需 RPC 聚合。缺失节点标 STALE。不实时全量复制。
8. **Case 1**（PRD §92）：当前 Web Agent crash → 业务 Process 不受影响，用户可访问其他 Agent。
9. **明确不做：** 批量、Group、告警通道 UI、历史指标图、command.execute、Adopt、第二套 REST。

## 新鲜度算法（本阶段锁定）

包：`internal/freshness`（Go）与 `web/src/lib/freshness.ts`（TS）必须同语义。

```text
LIVE_MAX_AGE = 10s

classify(now, lastUpdatedUnixMs, nodeState) -> LIVE | STALE | UNKNOWN

if lastUpdatedUnixMs <= 0: UNKNOWN
if nodeState in {REMOVED, REVOKED}: UNKNOWN
age = now - lastUpdated
if nodeState == ALIVE && age <= 10s: LIVE
if nodeState in {FAILED, SUSPECT, LEFT}: STALE   # 即使 age <= 10s
if age > 10s: STALE
else: STALE
```

`JOINING` 且有时间戳：STALE（尚未稳定观测）。无时间戳：UNKNOWN。

UI 颜色（禁止对调）：

| 徽章 | 背景 | 文字 | 禁止 |
|------|------|------|------|
| LIVE | `#D1FAE5` | `#065F46` | — |
| STALE | `#FEF3C7` | `#92400E` | **禁止绿色** |
| UNKNOWN | `#E5E7EB` | `#374151` | 禁止当健康 |

## HTTP / Cookie（本阶段锁定）

```text
Cookie: procmesh_session=<session_id>     # HttpOnly, SameSite=Lax, Path=/
X-CSRF-Token: <csrf>                      # Cookie 会话的 Mutation 必填
Authorization: Bearer ...                 # Web 不用；CLI 已有
Procmesh-Target-Node: <node_id>           # 看远程 Owner 时由 Web 设置
```

`AuthService.GetMe` 是读，不需 CSRF。`Login` 仍公开。静态 `GET /` 与 `/assets/*` 公开。

Web 把 `csrf_token` 存 `sessionStorage` 键 `procmesh_csrf`。刷新后 `GetMe` 成功则保留；401/DENIED 则清掉并跳转 `/login`。

## 视觉（ChatGPT Web 管理后台）

浅色中性、左侧导航、主内容区。

```text
--bg:        #F7F7F8
--sidebar:   #FFFFFF
--border:    #E5E5E5
--text:      #0D0D0D
--muted:     #6B6B6B
--accent:    #10A37F        # 仅主按钮 / 当前导航指示，不用作状态绿
--danger:    #C43C30
--card:      #FFFFFF
--radius:    16px
sidebar width: 244px
content max-width: 1080px
font: ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif
```

左侧：ProcMesh 字标、Overview / Nodes / Processes / Users / Roles / Audit、底部当前用户 + Logout。macOS 在 Overview 顶栏显示 Amber 条：`macOS: resource_limit ignored (no cgroup); Host reboot recovery depends on how the Agent is started.`

## File map（本阶段创建/修改）

```text
proto/procmesh/v1/api.proto
proto/procmesh/v1/api.pb.go
proto/procmesh/v1/procmeshv1connect/api.connect.go
internal/freshness/freshness.go
internal/freshness/freshness_test.go
internal/store/audit.go
internal/store/audit_test.go
internal/api/clusterapi.go
internal/api/clusterapi_test.go
internal/api/authapi.go
internal/api/authapi_test.go
internal/api/authn.go
internal/api/convert.go
internal/api/convert_test.go
internal/api/auditapi.go
internal/api/auditapi_test.go
internal/api/metricsapi.go
internal/api/metricsapi_test.go
internal/api/server.go
internal/api/proto_gen_test.go
internal/api/process.go                    # Forwarder.Audit / Metrics
internal/rpc/server.go                     # :18683 挂 Audit/Metrics LocalOnly
internal/agent/rpc.go
internal/web/embed.go
internal/web/embed_test.go
internal/web/dist/index.html               # stub，随后被 Vite 覆盖
web/                                       # Vue 源码
Makefile
docs/superpowers/plans/2026-08-13-v1-mvp.md
internal/agent/p5_accept_test.go
```

---

### Task 1: Proto — Overview 扩展、GetMe、Instance 启动时间、Audit/Metrics 服务桩

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Generate: `proto/procmesh/v1/api.pb.go`
- Generate: `proto/procmesh/v1/procmeshv1connect/api.connect.go`
- Create: `internal/freshness/freshness.go`
- Create: `internal/freshness/freshness_test.go`
- Modify: `internal/api/proto_gen_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/authn.go`（`GetMe` 已是 Get 前缀，无需改 mutation 列表；确认 `Me` 若命名不是 Get 则加入只读前缀）
- Create: `internal/api/auditapi.go`
- Create: `internal/api/metricsapi.go`
- Modify: `internal/api/convert.go`
- Modify: `internal/api/authapi.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: 现有 `ClusterOverviewResponse` 字段 1–5、`AuthService`、`Instance` 字段 1–10、`Forwarder`
- Produces: 下列 RPC / 消息；本任务 handler **只回桩**：Audit/Metrics 未实现 → `UNAVAILABLE` 英文 `not implemented`；`GetMe` 在 Auth 未注入时同样 `not implemented`；`Overview` 新字段本任务先填零值（现有字段行为不变）；`Instance.started_unix_ms` 由 convert 填入

在 `Instance` **追加**（不要改已有编号）：

```protobuf
  int64 started_unix_ms = 11;
```

`ClusterOverviewResponse` **追加**：

```protobuf
  int32 suspect = 6;
  int32 failed = 7;
  int32 process_total = 8;
  int32 process_running = 9;
  int32 process_unhealthy = 10;
  int32 process_fatal = 11;
  int32 cpu_percent = 12;     // ALIVE 成员平均值；无 ALIVE = 0 且 overview_freshness=UNKNOWN
  int32 memory_percent = 13;
  int32 disk_percent = 14;
  bool gossip_healthy = 15;
  bool rpc_healthy = 16;
  bool agent_degraded = 17;
  int64 cert_expires_unix = 18;
  int64 ca_expires_unix = 19;
  int64 view_unix_ms = 20;
  string platform_note = 21;  // 非 linux 必须非空
  map<string, int32> version_counts = 22;
```

`AuthService` 追加：

```protobuf
message GetMeRequest {}
message GetMeResponse {
  string user_id = 1;
  string username = 2;
  string csrf_token = 3;
  int64 expires_unix = 4;
  repeated string permissions = 5;
}

// service AuthService 增加
rpc GetMe(GetMeRequest) returns (GetMeResponse);
```

文件末尾追加：

```protobuf
message AuditEvent {
  string audit_id = 1;
  int64 timestamp_unix_ms = 2;
  string user_id = 3;
  string username = 4;
  string source_ip = 5;
  string source_agent = 6;
  string target_agent = 7;
  string resource = 8;
  string action = 9;
  string operation_id = 10;
  string result = 11;
  string metadata_json = 12;
}

message AuditEntry {
  AuditEvent event = 1;
  string source_node = 2;
  string freshness = 3; // LIVE | STALE | UNKNOWN
  int64 last_updated_unix_ms = 4;
}

message ListAuditRequest {
  string resource = 1;     // 空 = 全部
  int32 limit = 2;         // 0 = 50；封顶 200
  string target_node = 3;  // 空 = 聚合；非空 = 只查该 node
}
message ListAuditResponse { repeated AuditEntry entries = 1; }

service AuditService {
  rpc ListAudit(ListAuditRequest) returns (ListAuditResponse);
}

message GetAgentMetricsRequest {}
message AgentMetrics {
  double uptime_seconds = 1;
  int32 process_running = 2;
  int32 members = 3;
  int32 alive = 4;
  bool control_quorum = 5;
  ResourceSummary resources = 6;
}
message GetAgentMetricsResponse { AgentMetrics metrics = 1; }

message GetProcessMetricsRequest {
  string id_or_name = 1;
  string instance_id = 2;
}
message ProcessMetrics {
  string instance_id = 1;
  int32 pid = 2;
  int64 uptime_seconds = 3;
  int32 cpu_percent = 4;   // -1 = unknown
  int64 memory_bytes = 5;  // -1 = unknown
  string note = 6;
}
message GetProcessMetricsResponse { repeated ProcessMetrics metrics = 1; }

service MetricsService {
  rpc GetAgentMetrics(GetAgentMetricsRequest) returns (GetAgentMetricsResponse);
  rpc GetProcessMetrics(GetProcessMetricsRequest) returns (GetProcessMetricsResponse);
}
```

`internal/freshness/freshness.go`：

```go
package freshness

import "time"

const (
    LIVE    = "LIVE"
    STALE   = "STALE"
    UNKNOWN = "UNKNOWN"
    MaxAge  = 10 * time.Second
)

func Classify(now time.Time, lastUpdatedUnixMs int64, nodeState string) string
```

`convert.go` 的 `ViewOf`：若 `inst.StartedAt != nil`，设 `StartedUnixMs = inst.StartedAt.UTC().UnixMilli()`。

`AuthAPI.GetMe` 本任务：Auth==nil → `unimplemented()`；有 Principal 则返回 user_id/username/csrf；permissions 可空（Task 稍后填也可以，本任务至少返回身份）。未登录（已入群）由 interceptor 返回 DENIED。

`Makefile` 增加：

```makefile
.PHONY: test proto proto-ts web
proto-ts:
	cd web && npm exec -- buf generate || true
```

**本任务不要创建完整 `web/`。** `proto-ts` 目标可以先写成注释掉的占位命令，或仅打印 `web/ not yet` 并以 exit 0；真正生成放到 Task 5。不要在本任务 `npm install`。

- [ ] **Step 1: 写失败测试**

`internal/freshness/freshness_test.go`：

```go
func TestClassify_LiveAliveRecent(t *testing.T)
func TestClassify_StaleAliveOld(t *testing.T)
func TestClassify_StaleFailedEvenIfRecent(t *testing.T)
func TestClassify_UnknownNoTimestamp(t *testing.T)
func TestClassify_UnknownRevoked(t *testing.T)
func TestClassify_StaleSuspect(t *testing.T)
func TestClassify_StaleLeft(t *testing.T)
func TestClassify_StaleJoiningWithTimestamp(t *testing.T)
```

固定 `now = time.UnixMilli(1_700_000_010_000)`。
- ALIVE + last=1_700_000_005_000 → LIVE
- ALIVE + last=1_700_000_000_000（10s 整）→ LIVE（`age <= 10s`）
- ALIVE + last=1_699_999_999_000 → STALE
- FAILED + last=1_700_000_009_000 → STALE
- last=0 → UNKNOWN
- REVOKED + last>0 → UNKNOWN
- SUSPECT + last recent → STALE
- LEFT + last recent → STALE
- JOINING + last recent → STALE

`internal/api/proto_gen_test.go` 追加 `TestProto_P5ServicesGenerated`：断言 `AuditServiceName`、`MetricsServiceName`、`GetMe` 方法存在、`ClusterOverviewResponse` 有 `GetProcessTotal`、`Instance` 有 `GetStartedUnixMs`。

`internal/api/convert` 已有测试则追加：StartedAt 非空 → proto 毫秒；nil → 0。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/freshness ./internal/api -run 'TestClassify_|TestProto_P5|TestViewOf' -count=1`

Expected: FAIL（包或符号不存在）

- [ ] **Step 3: 改 proto、`make proto`、实现 freshness、桩 handler、convert、GetMe**

`NewServer` 挂 `AuditService` 与 `MetricsService`（与现有 intercept 相同）。`GetMe` 实现身份回读。Audit/Metrics 方法一律：

```go
return nil, unimplemented()
```

`unimplemented` 已存在于 authapi。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/freshness ./internal/api ./internal/auth ./internal/control -count=1`

Expected: PASS。`internal/process` `internal/shim` `internal/store` 覆盖率不被本任务破坏。

- [ ] **Step 5: Commit**

```bash
git add proto/procmesh/v1/api.proto proto/procmesh/v1/api.pb.go proto/procmesh/v1/procmeshv1connect/api.connect.go \
  internal/freshness internal/api/proto_gen_test.go internal/api/server.go internal/api/authapi.go \
  internal/api/auditapi.go internal/api/metricsapi.go internal/api/convert.go internal/api/convert_test.go Makefile
git commit -m "$(cat <<'EOF'
feat: 扩展 Overview/GetMe/Audit/Metrics proto 与新鲜度分类

EOF
)"
```

---

### Task 2: Cluster Overview 填实 + 证书到期 + 平台说明

**Files:**
- Modify: `internal/api/clusterapi.go`
- Modify: `internal/api/clusterapi_test.go`
- Modify: `internal/api/server.go`（`Options` 可加 `RPCHealthy`/`GossipHealthy`/`CertExpires`/`CAExpires`/`Degraded` 已有）
- Modify: `internal/agent/run.go`（把 RPC/Gossip/证书时间注入 ClusterDeps 或 Options）
- Test: `internal/api/clusterapi_test.go`

**Interfaces:**
- Consumes: Task 1 的 Overview 新字段、`cluster.NodeSummary`、`control.LoadBundle` / `x509` 解析
- Produces: `ClusterAPI.Overview` 按本机 Gossip 视图填计数；`platform_note` 在 `runtime.GOOS != "linux"` 时为  
  `macOS: resource_limit ignored (no cgroup); Host reboot recovery depends on how the Agent is started.`  
  （非 darwin 的非 linux 用 `GOOS: linux process semantics unavailable`）

计数规则（只读 Gossip，不 hop）：

- `members` / `alive`：保持现有
- `suspect` / `failed`：`State == SUSPECT` / `FAILED`
- `process_total`：所有成员 `Processes` 条数之和（含 FAILED 的 last-known）
- `process_running`：`observed == "RUNNING"`
- `process_unhealthy`：`health == "UNHEALTHY"`
- `process_fatal`：`observed == "FATAL"`
- `cpu_percent` / `memory_percent` / `disk_percent`：仅对 `ALIVE` 成员求算术平均（整除截断）；无 ALIVE → 0
- `version_counts`：`agent_version` → 人数；空版本键用 `"unknown"`
- `view_unix_ms`：`Deps.now().UnixMilli()`
- `agent_degraded`：`Degraded()`
- `gossip_healthy`：`Deps.Mesh != nil`（或注入的 `GossipHealthy`）；未组网且无 Mesh → false
- `rpc_healthy`：注入；未注入默认 true（单测不测 RPC 监听）
- `cert_expires_unix` / `ca_expires_unix`：从 `Deps.Dir` 读 `agent.crt` / `ca.crt` 的 `NotAfter.Unix()`；读失败 → 0

需要 `cluster.read`。已有 `requirePerm`。

- [ ] **Step 1: 写失败测试**

在 `clusterapi_test.go` 用 `staticMesh` 放 3 个成员：

1. ALIVE v1.0.0，1 个 RUNNING/HEALTHY process
2. ALIVE v1.0.1，1 个 RUNNING/UNHEALTHY
3. FAILED v1.0.0，1 个 last-known RUNNING（不得从 running 计数里当「实时健康」——**running 计数仍包含 last-known RUNNING**，UI 靠节点徽章解释；本测试断言 `process_running==3`、`failed==1`、`process_unhealthy==1`）

断言：`members==3`、`alive==2`、`failed==1`、`process_total==3`、`version_counts["v1.0.0"]==2`、`GetViewUnixMs()>0`。

另测：无成员时 percents==0。非 linux 构建下 `platform_note` 非空（用 `runtime.GOOS` 断言，不要 `t.Skip`）。

证书：`control.Init` 过的 dir，Overview 的 `ca_expires_unix` 与 `cert_expires_unix` > now。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestCluster_Overview -count=1`

Expected: FAIL（新字段为 0 / 空）

- [ ] **Step 3: 实现 Overview 聚合与证书解析**

抽小函数 `func summarize(members []cluster.NodeSummary) (...counters)` 便于测。解析证书用 `pem.Decode` + `x509.ParseCertificate`，不要引入新依赖。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api ./internal/agent -count=1 -timeout 180s`

Expected: PASS。不要改 P4 验收语义。

- [ ] **Step 5: Commit**

```bash
git add internal/api/clusterapi.go internal/api/clusterapi_test.go internal/api/server.go internal/agent/run.go
git commit -m "$(cat <<'EOF'
feat: 填实 Cluster Overview 计数、版本分布与证书到期

EOF
)"
```

---

### Task 3: AuditService — 本机列表 + 按需聚合

**Files:**
- Modify: `internal/store/audit.go`
- Modify: `internal/store/audit_test.go`
- Modify: `internal/api/auditapi.go`
- Create: `internal/api/auditapi_test.go`
- Modify: `internal/api/process.go`（`Forwarder` 增加 `Audit`）
- Modify: `internal/api/metrics.go`（`countingForwarder` 同步加方法）
- Modify: `internal/rpc/server.go`
- Modify: `internal/agent/rpc.go`（:18683 LocalOnly 挂 Audit）
- Modify: `internal/agent/run.go`（入口 AuditAPI 注入 Store + Router + Forward）

**Interfaces:**
- Consumes: `store.AuditEvent`、`ListAudit`、Task 1 proto、`hopRoute`、`freshness.Classify`、`auth.PermAuditRead`
- Produces:

```go
func (s *Store) ListAuditAll(ctx context.Context, resource string, limit int) ([]AuditEvent, error)
// resource=="" → 不按 resource 过滤；newest first；limit<=0 当 50；limit>200 当 200
```

```go
type AuditAPI struct {
    Store     *store.Store
    Auth      *auth.Service
    LocalOnly bool
    LocalID   string
    Router    *Router
    Forward   Forwarder
    Members   func() []cluster.NodeSummary
    Now       func() time.Time
}
```

`ListAudit` 行为：

1. `requirePerm(..., auth.PermAuditRead, "", false, true)`
2. `limit` 规范化 1..200，默认 50
3. 若 `target_node` 非空且不是本机：hop 到该节点的 AuditService.ListAudit（把 target_node 清空以免二次转发）；FAILED/无 RPC → 返回 **一条** 占位 `AuditEntry{source_node, freshness:STALE 或 UNKNOWN, event.action="unavailable", event.result="UNAVAILABLE"}`，不报错整个请求
4. 若 `target_node` 空：先列本机；再对每个非本机 ALIVE 成员 hop（并发，errgroup，每节点 timeout 2s）；FAILED/SUSPECT/超时的成员各追加一条占位，`freshness` 用 `freshness.Classify`
5. 合并按 `timestamp_unix_ms` 降序，截断到 limit
6. 本机条目 `freshness=LIVE`，`source_node=LocalID`，`last_updated_unix_ms=now`

`:18683` LocalOnly 的 AuditAPI 只查本机，忽略 target_node 聚合（防止环）。

Forwarder：

```go
Audit(ctx context.Context, rt Route) (procmeshv1connect.AuditServiceClient, error)
```

- [ ] **Step 1: 写失败测试**

`store`：`TestListAuditAll_EmptyResourceReturnsAll`（两条不同 resource，空过滤都返回）；`TestListAuditAll_CapsLimit`（插入 5 条，limit=2 返回 2；limit=0 视为 50 仍返回 5；limit=500 视为 200）。

`auditapi_test.go`：

- 无 Auth 单测：本机两条，ListAudit 返回 2，freshness LIVE
- 带 Router：本机 1 条 + FAILED 成员 → 至少 1 条真实 + 1 条占位 `UNAVAILABLE`，占位 freshness 不是 LIVE
- 无 `audit.read` 的 viewer-equivalent：用 Auth 注入后 DENIED（可复用 authapi_test 的 mem store）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store -run TestListAuditAll -count=1`  
以及 `go test ./internal/api -run TestAudit -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 ListAuditAll、AuditAPI、接线**

SQL：`resource==""` 时去掉 `WHERE resource = ?`。保持 newest first。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store ./internal/api ./internal/rpc ./internal/agent -count=1 -timeout 180s`

Expected: PASS。`internal/store` 覆盖率 ≥ 80%。

- [ ] **Step 5: Commit**

```bash
git add internal/store/audit.go internal/store/audit_test.go internal/api/auditapi.go internal/api/auditapi_test.go \
  internal/api/process.go internal/api/metrics.go internal/rpc/server.go internal/agent/rpc.go internal/agent/run.go
git commit -m "$(cat <<'EOF'
feat: 实现 Audit 本机列表与跨节点聚合

EOF
)"
```

---

### Task 4: MetricsService + 进程即时快照

**Files:**
- Modify: `internal/api/metricsapi.go`
- Create: `internal/api/metricsapi_test.go`
- Modify: `internal/api/process.go`（`Forwarder.Metrics`）
- Modify: `internal/api/metrics.go`（countingForwarder）
- Modify: `internal/rpc/server.go`
- Modify: `internal/agent/rpc.go`
- Modify: `internal/agent/run.go`
- Create: `internal/api/procstat_linux.go`
- Create: `internal/api/procstat_other.go`

**Interfaces:**
- Consumes: `process.Manager`、`ClusterDeps`/`Started`、Task 1 proto、`auth.PermProcessRead` / `cluster.read`
- Produces:

```go
type MetricsAPI struct {
    Mgr       *process.Manager
    Auth      *auth.Service
    Started   time.Time
    Cluster   ClusterDeps
    LocalOnly bool
    LocalID   string
    Router    *Router
    Forward   Forwarder
    Degraded  func() bool
}

func readProcStat(pid int) (cpuPercent int32, memBytes int64, ok bool)
// linux: /proc/<pid>/stat + /proc/<pid>/status VmRSS；读失败 ok=false
// !linux: 一律 ok=false
```

`GetAgentMetrics`：`cluster.read`；uptime=`now-Started`；`process_running` 复用 `runningInstances`；members/alive 复用 Overview 同源计数；`control_quorum` 从 Control；resources 用本机 `Local()` summary。

`GetProcessMetrics`：`process.read`；hop 到 Owner；本机按 spec 的 instances 填：pid、uptime（StartedAt）、cpu/mem（`readProcStat`，失败则 -1 且 `note` 为 `process cpu/memory unavailable`；`!linux` 的 note 固定 `macos: process cpu/memory unavailable`）。

- [ ] **Step 1: 写失败测试**

- `GetAgentMetrics`：Started 1 小时前，uptime ≥ 3600；Mgr 空则 process_running=0
- `GetProcessMetrics`：apply+假 instance 困难则用带 StartedAt 的 manager 测试夹具（现有 process_test helper）。至少：未知进程 → NOT_FOUND
- `readProcStat`：`os.Getpid()` 在 linux 上 ok=true 且 memBytes>0；非 linux 上 ok=false

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run 'TestMetrics_|TestReadProcStat' -count=1`

Expected: FAIL

- [ ] **Step 3: 实现并挂到 :18680 / :18683**

:18683 LocalOnly，GetProcessMetrics 不向远端再 hop。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api ./internal/agent -count=1 -timeout 180s`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/metricsapi.go internal/api/metricsapi_test.go internal/api/procstat_linux.go \
  internal/api/procstat_other.go internal/api/process.go internal/api/metrics.go \
  internal/rpc/server.go internal/agent/rpc.go internal/agent/run.go
git commit -m "$(cat <<'EOF'
feat: 实现 Agent/Process 即时 Metrics RPC

EOF
)"
```

---

### Task 5: `internal/web` embed + `GET /` SPA 回退

**Files:**
- Create: `internal/web/embed.go`
- Create: `internal/web/embed_test.go`
- Create: `internal/web/dist/index.html`（stub）
- Create: `internal/web/dist/.keep`
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `embed.FS`
- Produces:

```go
package web

//go:embed all:dist
var dist embed.FS

func Handler() http.Handler // 静态文件；目录与未命中的 GET 回 index.html
func HasIndex() bool        // dist/index.html 存在
```

`NewServer` 在 Connect / healthz / readyz / metrics / `/v1` **之后**：

```go
engine.NoRoute(func(c *gin.Context) {
    if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
        c.Status(http.StatusNotFound)
        return
    }
    web.Handler().ServeHTTP(c.Writer, c.Request)
})
```

**禁止**把 `/procmesh.v1.`、`/healthz`、`/readyz`、`/metrics` 交给 SPA。NoRoute 只处理未挂载路径。

stub `internal/web/dist/index.html`：

```html
<!doctype html>
<title>ProcMesh</title>
<div id="app">ProcMesh</div>
```

`Makefile`：

```makefile
web:
	cd web && npm ci && npm run build
```

本任务 `web/` 仍不存在，`make web` 可以失败；不要在本任务创建 Vue 工程。

- [ ] **Step 1: 写失败测试**

`embed_test.go`：`Handler` 对 `/` 返回 200 且 body 含 `ProcMesh`；对 `/nodes/abc` 也返回 200 且同一 index（SPA）；对 `/assets/missing.js` 回 index（Vite 未构建时无 assets）。

`server_test.go`：`GET /` 200；`GET /healthz` 仍 `ok`（不被 SPA 吃掉）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/web ./internal/api -run 'TestEmbed|TestServer_Healthz|TestServer_Root' -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 embed.Handler 与 NoRoute**

用 `http.FS` + 自定义：若 `Open(path)` 失败或是目录，改 Serve `index.html`。Content-Type 由 `http.ServeContent` 决定。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/web ./internal/api -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web internal/api/server.go internal/api/server_test.go Makefile
git commit -m "$(cat <<'EOF'
feat: 用 go:embed 在 :18680 提供 SPA

EOF
)"
```

---

### Task 6: Vue 工程脚手架 + Connect-Web + freshness.ts

**Files:**
- Create: `web/package.json`
- Create: `web/vite.config.ts`
- Create: `web/tsconfig.json`、`web/tsconfig.app.json`、`web/tsconfig.node.json`
- Create: `web/index.html`
- Create: `web/src/main.ts`
- Create: `web/src/App.vue`
- Create: `web/src/style.css`
- Create: `web/src/lib/freshness.ts`
- Create: `web/src/lib/freshness.test.ts`
- Create: `web/src/lib/connect.ts`
- Create: `web/src/lib/opid.ts`
- Create: `web/src/lib/csrf.ts`
- Create: `web/src/components/FreshnessBadge.vue`
- Create: `web/src/components/FreshnessBadge.test.ts`
- Create: `web/buf.gen.yaml`（或 Makefile proto-ts 直接调 protoc）
- Modify: `Makefile`（`proto-ts` 真正生成到 `web/src/gen`）
- Modify: `.gitignore`（`web/node_modules`、`web/dist`、Playwright 产物；**不要**忽略 `internal/web/dist`）

**Interfaces:**
- Consumes: `internal/freshness` 语义（必须逐条对齐 Task 1 用例）
- Produces: 可 `cd web && npm test` 的 Vitest；`npm run build` 输出到 `../internal/web/dist`

`package.json` 锁定（不要擅自换 major）：

```json
{
  "name": "procmesh-web",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vue-tsc -b && vite build",
    "test": "vitest run",
    "test:e2e": "playwright test"
  },
  "dependencies": {
    "@bufbuild/protobuf": "^2.2.3",
    "@connectrpc/connect": "^2.0.2",
    "@connectrpc/connect-web": "^2.0.2",
    "@tanstack/vue-query": "^5.67.2",
    "vue": "^3.5.13",
    "vue-router": "^4.5.0",
    "lucide-vue-next": "^0.469.0"
  },
  "devDependencies": {
    "@tailwindcss/vite": "^4.0.9",
    "@vitejs/plugin-vue": "^5.2.1",
    "@vue/test-utils": "^2.4.6",
    "jsdom": "^26.0.0",
    "tailwindcss": "^4.0.9",
    "typescript": "^5.7.3",
    "vite": "^6.2.0",
    "vitest": "^3.0.7",
    "vue-tsc": "^2.2.4",
    "@playwright/test": "^1.50.1",
    "@bufbuild/protoc-gen-es": "^2.2.3",
    "@connectrpc/protoc-gen-connect-es": "^2.0.2"
  }
}
```

Vite `build.outDir` = `../internal/web/dist`，`emptyOutDir: true`。`base: '/'`。

`freshness.ts`：

```ts
export const LIVE = "LIVE";
export const STALE = "STALE";
export const UNKNOWN = "UNKNOWN";
export const MAX_AGE_MS = 10_000;
export function classify(nowMs: number, lastUpdatedUnixMs: number, nodeState: string): "LIVE" | "STALE" | "UNKNOWN";
export function formatAge(nowMs: number, lastUpdatedUnixMs: number): string; // "38s ago" / "unknown"
```

用例与 Go 完全相同（同一组毫秒时间戳）。

`FreshnessBadge.vue`：显示文案 `LIVE`/`STALE`/`UNKNOWN`，STALE 使用 class `freshness-stale`（背景 `#FEF3C7`，文字 `#92400E`），**不得**含 `bg-green` / `#D1FAE5` / `#10A37F`。

`connect.ts`：`createConnectTransport({ baseUrl: "", fetch: (i, init) => fetch(i, { ...init, credentials: "same-origin" }) })` + interceptor：若 `sessionStorage.procmesh_csrf` 存在则设 `X-CSRF-Token`；每个 unary 自动带 cookie。

`opid.ts`：`export function newOperationId(): string` → `crypto.randomUUID()`。

shadcn-vue：**本任务不要接入完整 shadcn CLI**。用 Tailwind v4 + 少量手写组件（badge/button/input/table）。视觉 token 写在 `style.css` 的 `@theme`。避免把半个设计系统拖进来。

- [ ] **Step 1: 写失败的 freshness.test.ts 与 FreshnessBadge.test.ts（先建最小 package 使 vitest 能跑）**

`freshness.test.ts` 复制 Go 的 8 个分类用例 + `formatAge`：`now=1700000010000, last=1700000000000` → 含 `10s ago` 或 `10s`。

Badge：渲染 STALE 时 `style`/`class` 不含 `green`；计算后的 background 为 `#FEF3C7` 或等价 rgb。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm install && npm test`

Expected: FAIL（模块不存在或断言失败）

- [ ] **Step 3: 实现 lib + badge + Vite/Tailwind + proto-ts**

`make proto-ts` 必须生成 `web/src/gen/procmesh/v1/` 下的 TS。生成文件提交到 git（与 Go pb.go 一样），这样纯 `go test` 的环境不必有 buf。

- [ ] **Step 4: `npm test` 通过；`npm run build` 写出 `internal/web/dist/index.html`**

Run: `cd web && npm test && npm run build`  
然后：`go test ./internal/web ./internal/api -run 'TestEmbed|TestServer_Root' -count=1`

Expected: PASS。embed 测试仍能找到 index（构建后应仍含可服务的 index.html）。若 Vite 的 index 不再含字面 `ProcMesh`，**更新** embed 测试为断言 `200` + `<div id="app">`，不要让 stub 断言卡死真实构建。

- [ ] **Step 5: Commit**

```bash
git add web Makefile .gitignore internal/web/dist
git commit -m "$(cat <<'EOF'
feat: 搭建 Vue3 Web 与对齐的 freshness 徽章

EOF
)"
```

---

### Task 7: 登录页 + AppShell + 路由守卫

**Files:**
- Create: `web/src/router.ts`
- Create: `web/src/pages/LoginPage.vue`
- Create: `web/src/pages/LoginPage.test.ts`
- Create: `web/src/components/AppShell.vue`
- Create: `web/src/components/AppShell.test.ts`
- Create: `web/src/lib/session.ts`
- Modify: `web/src/main.ts`
- Modify: `web/src/App.vue`
- Modify: `internal/api/authapi.go`（GetMe 填 permissions：从 Auth.Service 读该用户全部绑定权限并去重排序）
- Modify: `internal/api/authapi_test.go`

**Interfaces:**
- Consumes: `AuthService.Login` / `Logout` / `GetMe`；Cookie 由服务器 Set-Cookie
- Produces: 路由

```text
/login
/
/nodes
/nodes/:id
/processes
/processes/:idOrName
/users
/roles
/audit
```

未登录访问非 `/login` → `/login?next=`。已登录访问 `/login` → `/`。

`session.ts`：

```ts
export async function loadSession(): Promise<Me | null>
export function saveCsrf(token: string): void
export function clearSession(): void
```

`LoginPage`：username + password，Submit 调 Login（Connect JSON）。失败展示英文 `invalid credentials` / `login rate limited` / `user locked`（从 Connect error message 取）。成功 `saveCsrf` 后 `router.replace(next || '/')`。

`AppShell`：左侧导航上述条目（不含 Login），底部 username + Logout。Logout 调 `Logout`（必须带 `MutationMeta.operation_id` + CSRF）后 `clearSession` 并去 `/login`。

权限：无 `user.read` 则 Users 链隐藏；无 `role.read` 隐藏 Roles；无 `audit.read` 隐藏 Audit。Overview/Nodes/Processes 对所有已登录角色可见（Viewer 有 cluster/node/process.read）。

GetMe.permissions：并上该用户所有 Role 的 permissions。测试：super_admin 含 `process.restart`；仅 viewer 绑定不含 `process.restart`。

- [ ] **Step 1: 写失败测试**

Vitest：LoginPage 渲染 `Username`、`Password`、submit 按钮；空提交不调用 transport（可用 stub provide）。AppShell 含 `Overview`、`Processes`。  
Go：`TestAuth_GetMeReturnsPermissions`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test`  
`go test ./internal/api -run TestAuth_GetMe -count=1`

Expected: FAIL

- [ ] **Step 3: 实现页面、路由、GetMe permissions**

Login 请求 **不要** 带 CSRF。Connect 路径为 `/procmesh.v1.AuthService/Login`。

- [ ] **Step 4: 跑测试确认通过并 build**

Run: `cd web && npm test && npm run build`  
`go test ./internal/api -run TestAuth_ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src internal/api/authapi.go internal/api/authapi_test.go internal/web/dist
git commit -m "$(cat <<'EOF'
feat: 实现 Web 登录、会话恢复与侧栏壳

EOF
)"
```

---

### Task 8: Overview + Node 列表/详情（含新鲜度）

**Files:**
- Create: `web/src/pages/OverviewPage.vue`
- Create: `web/src/pages/OverviewPage.test.ts`
- Create: `web/src/pages/NodesPage.vue`
- Create: `web/src/pages/NodeDetailPage.vue`
- Create: `web/src/pages/clusterView.ts`
- Create: `web/src/pages/clusterView.test.ts`
- Modify: `web/src/router.ts`

**Interfaces:**
- Consumes: `ClusterService.Overview`、`NodeService.ListNodes` / `GetNode`、`FreshnessBadge`、`classify`
- Produces: Overview 展示两块：

**ProcMesh**（自身）：Control quorum（true/false 文字，失 quorum 用 danger 色，**不要**写成 Process 故障）、Gossip healthy、RPC healthy、cert / CA expiry（ISO 日期）、version_counts、`agent_degraded` 横幅 `Agent DEGRADED — local store impaired; business processes are not stopped.`、`platform_note`（非空才显示）

**Workload**：Agent Total / Alive / Suspect / Failed；Process Total / Running / Unhealthy / Fatal；CPU/Memory/Disk 百分比（来自 Overview）

Node 列表：hostname、node_id、state、agent_version、resources、**FreshnessBadge**、`last_updated` 相对时间。`state=FAILED` 的行不得把 process 摘要画成绿色 LIVE。

Node 详情：PRD §45 中本阶段有数据的字段——Hostname、Node ID、Address（api/rpc/gossip）、Version、Status、Boot ID、CPU/Memory/Disk、Process Count、Labels。**不要编造** Network / Load Average / Groups / Boot Time（Gossip 无这些字段）。Processes 子表：name、desired、observed、health、revisions、FreshnessBadge（用 process.freshness_unix_ms + 节点 state）。

Remove Agent 按钮：仅当 permissions 含 `node.remove`；调用 `RemoveNode` + `operation_id`；确认对话框文案 `Remove node and revoke its certificate?`。

- [ ] **Step 1: 写失败测试**

`clusterView.test.ts` 纯函数：把 proto-like 对象映射到 view-model，FAILED 节点上 RUNNING process 的 badge 为 STALE。

`OverviewPage.test.ts`：给定 mock overview，页面同时出现 `ProcMesh` 与 `Workload` 标题；`control_quorum=false` 时出现 `No quorum`；**不要**出现把 degraded 写成 process down 的文案。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test`

Expected: FAIL

- [ ] **Step 3: 实现页面；TanStack Query 轮询 Overview/ListNodes 每 5s**

- [ ] **Step 4: 跑测试确认通过并 build**

Run: `cd web && npm test && npm run build`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src internal/web/dist
git commit -m "$(cat <<'EOF'
feat: 实现 Overview 与 Node 页的 LIVE/STALE/UNKNOWN

EOF
)"
```

---

### Task 9: Process 列表/详情 + Start/Stop/Restart/Kill

**Files:**
- Create: `web/src/pages/ProcessesPage.vue`
- Create: `web/src/pages/ProcessDetailPage.vue`
- Create: `web/src/pages/processView.ts`
- Create: `web/src/pages/processView.test.ts`
- Create: `web/src/lib/headers.ts`
- Modify: `web/src/router.ts`

**Interfaces:**
- Consumes: `NodeService.ListNodes`（集群进程列表 = 各节点 Processes 摘要）、`ProcessService.GetProcess` / `StartProcess` / `StopProcess` / `RestartProcess` / `KillProcess`、`MetricsService.GetProcessMetrics`
- Produces: 列表列：name、owner hostname/node_id、desired、observed、health、latest/active revision、FreshnessBadge。来源必须是 Gossip 摘要，**不要**对每个节点 `ListProcesses`（那是权威列表，只在详情使用）。

详情：设置 header `Procmesh-Target-Node: owner_node_id` 后 `GetProcess`。展示 PRD §46 字段中 API 已有的：Name、Process ID、Owner、Instances、Desired、Observed、Health、PID、Uptime（metrics 或 started_unix_ms）、Restart Count、Exit Code、Active/Latest Revision。CPU/Memory 用 GetProcessMetrics；-1 时显示 `unknown` + note。

按钮：Start / Stop / Restart / Force Stop（Kill）。均生成 `operation_id`，带 CSRF 与 Target-Node。Viewer（无对应 perm）按钮 disabled。

`latest_revision != active_revision` 横幅原文：`Configuration changed. Restart required.`

远程超时/不可达：展示英文 `UNAVAILABLE` 或 `TIMEOUT`，**不要**改成本地成功。

- [ ] **Step 1: 写失败测试**

`processView.test.ts`：从 nodes[] 展平 processes；同名跨节点两行；FAILED owner 的 observed=RUNNING → badge STALE。  
`latest!=active` helper 返回需要横幅。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test -- processView`

Expected: FAIL

- [ ] **Step 3: 实现列表/详情/mutation**

`headers.ts`：`export function withTarget(nodeId: string): HeadersInit`

- [ ] **Step 4: 跑测试确认通过并 build**

Run: `cd web && npm test && npm run build`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src internal/web/dist
git commit -m "$(cat <<'EOF'
feat: 实现集群 Process 列表、详情与远程启停

EOF
)"
```

---

### Task 10: 远程配置（409）+ 历史 diff/rollback + 日志

**Files:**
- Create: `web/src/pages/ProcessConfigPanel.vue`
- Create: `web/src/pages/ProcessConfigPanel.test.ts`
- Create: `web/src/pages/ProcessLogsPanel.vue`
- Create: `web/src/lib/connecterr.ts`
- Create: `web/src/lib/connecterr.test.ts`
- Modify: `web/src/pages/ProcessDetailPage.vue`

**Interfaces:**
- Consumes: `ConfigService.GetConfig` / `UpdateConfig` / `History` / `Diff` / `Rollback`；`LogService.TailLogs` / `StreamLogs` / `DownloadLogs`
- Produces:

`connecterr.ts`：

```ts
export function appCode(err: unknown): string // ErrorInfo.code or ""
export function isConflict(err: unknown): boolean // code === "CONFLICT"
```

配置面板：JSON textarea（ProcessSpec 可编辑字段，只读 process_id / latest_revision）。保存调用 UpdateConfig，`expected_revision = spec.latest_revision`，`operation_id` 新 UUID。冲突：页面横幅 `409 Conflict — reload and retry`，**不**自动重试、**不**改 expected_revision 再提交。

历史：列表 revision / operator / time / comment；选两项调 Diff 显示 `diff` 文本；Rollback 调 `Rollback`（`to_revision` + `expected_revision=latest`）。

日志：默认 Tail 100 行 stdout；可切 stderr / instance。Stream：Connect streaming 追加到只读窗口；卸载时 abort。Download：调 DownloadLogs 并触发浏览器下载。无 `process.logs.download` 则隐藏 Download。无 `process.config.update` 则配置只读。

- [ ] **Step 1: 写失败测试**

`connecterr.test.ts`：构造带 `ErrorInfo{code:CONFLICT}` 的 ConnectError → `isConflict` true。  
`ProcessConfigPanel.test.ts`：mock Update 抛 CONFLICT 后出现 `409 Conflict`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test -- connecterr ProcessConfig`

Expected: FAIL

- [ ] **Step 3: 实现面板并嵌入详情页（tabs: Overview / Config / Logs）**

- [ ] **Step 4: 跑测试确认通过并 build**

Run: `cd web && npm test && npm run build`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src internal/web/dist
git commit -m "$(cat <<'EOF'
feat: 实现远程配置 CAS/409、历史与日志

EOF
)"
```

---

### Task 11: Users / Roles / Audit 页

**Files:**
- Create: `web/src/pages/UsersPage.vue`
- Create: `web/src/pages/UsersPage.test.ts`
- Create: `web/src/pages/RolesPage.vue`
- Create: `web/src/pages/AuditPage.vue`
- Create: `web/src/pages/AuditPage.test.ts`
- Modify: `web/src/router.ts`

**Interfaces:**
- Consumes: `UserService` / `RoleService` / `AuditService.ListAudit`
- Produces:

Users：表格 username、display、email、status、last_login。Create：username+password（最短 10）+ display+email。Disable 按钮。无 `user.create` / `user.update` 则隐藏对应操作。

Roles：列表内置 + 自定义；permissions 只读展示。Grant：user_id + role_id + scope CLUSTER|AGENT + 可选 scope_id。CreateRole：name + 多选 permissions（复选 PRD §16 中 V1.0 已实现的那些，与 `internal/auth/perm.go` 常量一致，**不含** `batch.execute` / `alert.*`）。

Audit：表格 time、user、action、resource、source_node、target_agent、result、**FreshnessBadge**。占位 UNAVAILABLE 行必须 STALE/UNKNOWN，不得 LIVE。资源过滤 input。说明文案：`Audit is per-node; unreachable nodes are marked STALE.`

- [ ] **Step 1: 写失败测试**

UsersPage：渲染 Create 表单密码短于 10 时按钮 disabled 或提交前校验。  
AuditPage：给定一条 `freshness=STALE` 的 entry，徽章为 STALE 且无 green 类。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test -- UsersPage AuditPage`

Expected: FAIL

- [ ] **Step 3: 实现三页**

- [ ] **Step 4: 跑测试确认通过并 build**

Run: `cd web && npm test && npm run build`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src internal/web/dist
git commit -m "$(cat <<'EOF'
feat: 实现 Users、Roles 与聚合 Audit 页

EOF
)"
```

---

### Task 12: Playwright + Case 1 + 计划索引

**Files:**
- Create: `web/playwright.config.ts`
- Create: `web/e2e/login.spec.ts`
- Create: `web/e2e/list.spec.ts`
- Create: `web/e2e/freshness.spec.ts`
- Create: `web/e2e/conflict.spec.ts`
- Create: `internal/agent/p5_accept_test.go`
- Modify: `docs/superpowers/plans/2026-08-13-v1-mvp.md`
- Modify: `Makefile`（`test-e2e`）

**Interfaces:**
- Consumes: 全部已接线的 Agent + 构建后的 embed UI
- Produces: 可演示「浏览器管集群」；Case 1 脚本化

Playwright 由 **Go 测试拉起**（避免再写一套 agent 启动器）：

`TestP5_Playwright_LoginListFreshness409`：

1. `startClusterAgent` + `cluster init` + `loginAdmin` 仅用于 apply 一个 sleep 进程（`writeSleepSpec`）
2. 再 `user create`/`role grant viewer` 可选
3. 设置 env `PROCMESH_E2E_URL=http://<addr>`、`PROCMESH_E2E_USER=admin`、`PROCMESH_E2E_PASSWORD=<pw>`
4. `exec.Command("npx", "playwright", "test")` 在 `web/` 目录跑；失败则 `t.Fatal` 输出 stdout/stderr
5. 若 `exec.LookPath("npx")` 失败：`t.Fatalf("npx required for P5 playwright")`——**不要 Skip**

`web/e2e/login.spec.ts`：打开 `/` → 重定向 login → 填 admin/password → 看见 Overview 或 `Workload`。错误密码看见 `invalid credentials`。

`list.spec.ts`：登录后 Nodes 或 Processes 能看到刚才 apply 的进程名。

`freshness.spec.ts`：用 `page.route` 劫持 `ClusterService/Overview` 或 `NodeService/ListNodes`，把一个节点改成 `state=FAILED` 且 process observed=RUNNING、`last_updated_unix_ms` 为 60s 前。断言页面出现 `STALE` 文本，且该行 **没有** CSS class/computed color 绿色（检查徽章 class `freshness-stale`）。

`conflict.spec.ts`：打开进程配置；用 `page.route` 让 `UpdateConfig` 返回 Connect 409/FailedPrecondition + ErrorInfo CONFLICT；保存后页面有 `409 Conflict`。

`playwright.config.ts`：`baseURL` 来自 `PROCMESH_E2E_URL`；`fullyParallel: false`；browser chromium；timeout 60s。

Case 1 `TestP5_Case1_WebAgentCrash`：

1. A init，B join（复用 P3/P4 双节点 helper，如有）
2. 在 A apply+start sleep 进程，观察到 RUNNING，记下 pid
3. `stopA()` 取消 A 的 Agent（shim 继续）
4. `unix.Kill(pid, 0)` 成功（进程仍在）
5. 从 B：`process list` 或 Overview/ListNodes 仍 200；B 的 `GET /` 仍 200
6. 不在 B 上自动创建 A 的进程（B `process list` 不含该本地权威副本；Gossip 可显示 last-known STALE）

macOS 跑这些测试，不要 `t.Skip`。

更新索引：

```markdown
| P5 | [2026-08-15-p5-vue-embedded-ui.md](./2026-08-15-p5-vue-embedded-ui.md) | Vue3 embedded UI, LIVE/STALE/UNKNOWN, remote config/logs |
```

- [ ] **Step 1: 写失败的 p5_accept_test 与 e2e spec**

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent -run TestP5_ -count=1 -timeout 180s`

Expected: FAIL（测试未实现或 playwright 未通）

- [ ] **Step 3: 实现 e2e、Case 1、索引；`cd web && npx playwright install chromium` 只在本机/CI 文档中说明，测试里若缺浏览器则 `playwright test` 失败信息原样抛出**

安装浏览器不要提交。可在测试开头若报 `Executable doesn't exist` 则跑一次 `npx playwright install chromium`（测试进程内，可接受）。

- [ ] **Step 4: 全量验证**

Run:

```bash
cd web && npm test && npm run build
go test ./... -count=1 -timeout 300s
go test ./internal/process ./internal/shim ./internal/store ./internal/control ./internal/auth -cover
```

Expected: 全绿；五包 ≥ 80%。Playwright 四条 spec 通过。Case 1 通过。

- [ ] **Step 5: Commit**

```bash
git add web/e2e web/playwright.config.ts internal/agent/p5_accept_test.go \
  docs/superpowers/plans/2026-08-13-v1-mvp.md Makefile internal/web/dist
git commit -m "$(cat <<'EOF'
test: 验收 Web 登录/新鲜度/409 与 Case 1

EOF
)"
```

---

## 自检

1. **规格覆盖：** §11.3 全部 V1.0 页面；§14 Vitest+Playwright+Case 1；§15 自身 vs 业务；§16 CSRF/Session；PRD §42–47、§54–56、§70、§72–74、§83、§88 Management、§92 Case 1、§94 新鲜度、§95 Vue/embed。P4 行为保持。V1.1 项明确不做。
2. **无占位符：** 任务含测试名、命令、期望、提交说明、关键签名与 proto。
3. **类型一致：** `freshness.LIVE/STALE/UNKNOWN`、`GetMe`、`ListAuditAll`、`Forwarder.Audit/Metrics`、Cookie/CSRF 名与 P4 相同。
4. **依赖方向：** `web` 包只 embed；`api` → `freshness`/`auth`/`process`/`rpc`/`control`；`process` 不 import 新包。
5. **旧测试：** `Auth==nil` 仍无认证；`GET /healthz` 不被 SPA 吞；P4 accept 保持。
6. **embed 测试：** Task 6 起断言改为 `200` + `#app`，避免 Vite index 不再含字面 `ProcMesh` 导致失败。
