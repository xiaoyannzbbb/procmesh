# Q4 Alerts + 全通道 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** 已完成

**Goal:** 进程/本机告警由 Owner 写入本地 inbox 并外发；集群级告警仅当时 Raft Leader 外发；Web + Webhook + Email + 企业微信 + 钉钉 + Slack 可测通；Alerts 页与 Overview Recent Alerts 遵守 P5 新鲜度合同（STALE 不得画成「无告警」）。

**Architecture:** `alert_channels` / `alert_policy` 进 Raft（与 AgentGroup 同形）。Alert 实例只存在发送者 SQLite，按 fingerprint 复用一行。`internal/alert` 判定 + 去重 + `Send`；`process` 不得依赖 `alert`。`ListAlerts` 本机 ∪ 对 ALIVE 节点按需 RPC，失败标 STALE。WEB 通道无网络 I/O。

**Tech Stack:** 现有 Go + `modernc.org/sqlite` + hashicorp/raft + ConnectRPC + Vue3 + Vitest。外发通道用 `net/http/httptest` 与内存 SMTP 假服务，不接真网。不新增二进制、不新增 REST 资源、不引入时序库或第三方告警总线。

---

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- 强制 TDD：先红后绿；每任务先写失败测试
- `process` 不得 import `cluster`、`control`、`rpc`、`auth`、`web` 或 `alert`
- `alert` 可依赖 `store`、`control`（只读通道/策略）、`metrics`；不得让 `process` 反过来依赖它
- Raft **禁止**写入 alert 实例；Gossip **禁止**携带 alert 实例
- 非 Leader 禁止外发 `AGENT_FAILED` / `CERT_EXPIRING` / `VERSION_MISMATCH` / `AGENT_SUSPECT_TOO_LONG`
- 不承诺 Exactly Once；Leader 切换后新 Leader 本地无 fingerprint 时允许再发一条
- V1.1 **无**手动 Ack；状态只有 `FIRING` | `RESOLVED`
- `version.Protocol` 保持 `1`
- 错误码：`INVALID` / `CONFLICT` / `DENIED` / `NOT_FOUND` / `UNAVAILABLE` / `DEGRADED`
- 所有 Mutation 必须带非空 `operation_id`（UUID）；通道/策略写发给 Raft Leader
- 通道 secret / SMTP 密码不进 audit payload 明文，API 永不回传 secret
- 生成的 proto Go / TS 文件禁止手改；改完 proto 必须 `make proto` 与 `make proto-ts`
- 测试与代码同目录；`internal/alert` ≥ 80%；`internal/control` / `internal/auth` / `internal/store` 保持 ≥ 80%
- 文档与计划用中文；API 错误消息用英文
- 新文案进 `web/public/locales/{en,zh}`，跑 `npm run i18n:check`
- 视觉与新鲜度沿用 P5：`--bg #F7F7F8`、`--accent #10A37F` 不作状态绿；LIVE `#D1FAE5/#065F46`，STALE `#FEF3C7/#92400E`，UNKNOWN `#E5E7EB/#374151`
- **禁止把 STALE 画成绿色「正常」或「当前无告警」**
- 本阶段不做：Backup、值班表、静默日历、按 Agent Group 拆通道、Exactly Once、观察者抢发、Raft fingerprint 锁
- 工作目录是本 worktree，提交写在 `feat/q4-alerts-channels`

## 规格解读（Q4 边界）

来源：`docs/superpowers/specs/2026-08-16-v1.1-architecture-design.md` §7、§10–§15；PRD §43 Recent Alerts、§63–65、§89 Alerts 页。冲突以架构 spec 为准。V1.0 合同：`docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。P5 计划明确「V1.0 不做告警通道 UI」——本阶段按 P5 页面合同补上，不重做登录/Overview/启停。

1. **配置权威：** `AlertChannel` + `AlertPolicy` 在 Raft。CRUD 需 `alert.manage`。失 quorum 时通道/策略写 `UNAVAILABLE`。
2. **实例权威：** 发送者本地 SQLite。无外发通道时 inbox 仍写。`type=WEB` 不调用网络 `Send`。
3. **发送者：** 进程/本机 = Owner 或本机。集群级四类 = 当时 Leader 且有 quorum。`CONTROL_NO_QUORUM` = 任一 **voter** 连续 `suspect_too_long` 看不到 Leader。
4. **去重：** 同发送者同 fingerprint 复用一行；仅当 `now - notified_at >= dedup_window` 或从未成功 Send 才再 Send。
5. **读：** `ListAlerts` = 本机 ∪ ALIVE hop；缺失/超时标 STALE。Overview Recent Alerts 同一聚合。需 `alert.read`。
6. **P5 合同：** 聚合失败 ≠ 空闲无事。Viewer 只读通道/策略。Mutation 自带 `operationId`。
7. **可演示：** 杀进程出告警；Leader 报 Agent Failed；五条外发通道可测通。

## File map

```text
internal/store/schema.sql
internal/store/alert.go                      # 新建
internal/store/alert_test.go                 # 新建
internal/control/command.go
internal/control/fsm.go
internal/control/fsm_test.go
internal/control/raft.go
internal/control/raft_test.go
internal/auth/rbac.go
internal/auth/rbac_test.go
internal/alert/types.go                      # 新建
internal/alert/types_test.go                 # 新建
internal/alert/engine.go                     # 新建
internal/alert/engine_test.go                # 新建
internal/alert/channel.go                    # 新建
internal/alert/channel_test.go               # 新建
internal/alert/scan.go                       # 新建
internal/alert/scan_test.go                  # 新建
internal/cluster/mesh.go
internal/cluster/mesh_test.go
proto/procmesh/v1/api.proto
internal/api/proto_gen_test.go
internal/api/process.go                      # Forwarder.Alert
internal/api/apitest_test.go
internal/api/auditapi_test.go
internal/api/metrics.go
internal/api/alert.go                        # 新建
internal/api/alert_test.go                   # 新建
internal/api/server.go
internal/api/metrics.go                      # alert_send_total 暴露（若走 api/metrics）
internal/rpc/client.go
internal/rpc/server.go
internal/agent/rpc.go
internal/agent/run.go
internal/cli/root.go
internal/cli/client.go
internal/cli/alert.go                        # 新建
internal/cli/root_test.go
web/src/lib/rpc.ts
web/src/router.ts
web/src/components/AppShell.vue
web/src/pages/AlertsPage.vue                 # 新建
web/src/pages/AlertsPage.test.ts             # 新建
web/src/pages/OverviewPage.vue
web/src/pages/OverviewPage.test.ts
web/public/locales/en/common.json
web/public/locales/zh/common.json
web/e2e/alert.spec.ts                        # 新建
internal/agent/q4_accept_test.go             # 新建
docs/superpowers/plans/2026-08-16-v1.1.md
```

生成（改 proto 后执行，不要手改）：`proto/procmesh/v1/api.pb.go`、`proto/procmesh/v1/procmeshv1connect/api.connect.go`、`web/src/gen/procmesh/v1/api_pb.ts`

---

## 本阶段锁定的模型

### 指纹与严重度（必须逐字）

| type | fingerprint | 发送者 | severity |
|------|-------------|--------|----------|
| `PROCESS_EXIT` | `PROCESS_EXIT:{process_id}` | Owner | WARNING |
| `PROCESS_FATAL` | `PROCESS_FATAL:{process_id}` | Owner | CRITICAL |
| `PROCESS_CRASH_LOOP` | `PROCESS_CRASH_LOOP:{process_id}` | Owner | CRITICAL |
| `HEALTH_FAILED` | `HEALTH_FAILED:{process_id}` | Owner | WARNING |
| `CPU_HIGH` | `CPU_HIGH:{process_id}` 或 `CPU_HIGH:{node_id}` | Owner / 本机 | WARNING |
| `MEMORY_HIGH` | `MEMORY_HIGH:{process_id}` 或 `MEMORY_HIGH:{node_id}` | Owner / 本机 | WARNING |
| `DISK_HIGH` | `DISK_HIGH:{node_id}` | 本机 | WARNING |
| `LOCAL_DB_ERROR` | `LOCAL_DB_ERROR:{node_id}` | 本机 | CRITICAL |
| `AGENT_FAILED` | `NODE_FAILED:{node_id}` | Leader | CRITICAL |
| `AGENT_SUSPECT_TOO_LONG` | `NODE_SUSPECT:{node_id}` | Leader | WARNING |
| `CONTROL_NO_QUORUM` | `CONTROL_NO_QUORUM:{cluster_id}` | voter | CRITICAL |
| `CERT_EXPIRING` | `CERT_EXPIRING:{node_id}` | Leader | WARNING |
| `VERSION_MISMATCH` | `VERSION_MISMATCH:{node_id}` | Leader | WARNING |

`AGENT_FAILED` 的 fingerprint **不是** `AGENT_FAILED:`。实现必须用上表。

进程判定（轮询 `Manager.ListInstances`，不读未导出的 `failures`）：

- `PROCESS_EXIT`：`Desired==RUNNING && Observed==EXITED`
- `PROCESS_CRASH_LOOP`：`Desired==RUNNING && Observed==BACKOFF`
- `PROCESS_FATAL`：`Observed==FATAL`
- `HEALTH_FAILED`：`Desired==RUNNING && Observed==RUNNING && Health==UNHEALTHY`
- 对应恢复：RUNNING+HEALTHY → RESOLVED 上述四类（各 fingerprint 独立）

阈值：读 `metric_samples` 层 `raw_min`，连续 `high_consecutive_mins` 个**存在的**点都 ≥ 阈值才 FIRING。缺口分钟不插 0、不计入连续。没有点则退回即时快照（当前值 ≥ 阈值视为 1 分钟；`high_consecutive_mins>1` 时仅快照不足以 FIRING）。进程 `MEMORY_HIGH` 用 `process.memory_bytes` 对节点 `memory` 总量换算百分比；总量未知则跳过进程内存告警。

证书：`NotAfter - now <= 30*24h`。版本：ALIVE 成员 `protocol_version ∉ {0,1}`（0 视为 1）。V1.0/V1.1 混跑不触发。

### AlertPolicy 默认（Raft `ensure()` 零值时填入）

```go
DedupWindowSec         = 600  // 10m
NotifyOnResolve        = true
CPUHighPercent         = 90
MemoryHighPercent      = 90
DiskHighPercent        = 90
HighConsecutiveMins    = 2
SuspectTooLongSec      = 120  // 2m
```

策略字段用 **int64 秒 / int 百分比**，不要 `time.Duration` JSON（避免纳秒歧义）。

### 通道 config_json

```go
// WEBHOOK
{"url":"https://...","headers":{"X-Custom":"v"},"hmac_secret":"..."}
// EMAIL
{"smtp_host":"127.0.0.1","smtp_port":2525,"username":"","password":"...","from":"a@b","to":["c@d"],"starttls":false}
// WECOM / SLACK
{"webhook_url":"https://..."}
// DINGTALK
{"webhook_url":"https://oapi.dingtalk.com/robot/send?access_token=...","secret":"SEC..."}
// WEB
{}
```

Put 时空 `hmac_secret` / `password` / `secret` = **保留已有值**。API 回显删除这三个键；`url` / `webhook_url` / `smtp_host` / `username` / `from` / `to` / `starttls` / 非 Authorization 的 headers 保留。`headers.Authorization` 也删除。

通道名：trim 后 `^[A-Za-z0-9._-]{1,64}$`。type 只能是 `WEB|WEBHOOK|EMAIL|WECOM|DINGTALK|SLACK`。

### 外发 payload（WEBHOOK POST JSON）

```json
{
  "alert_id": "...",
  "fingerprint": "PROCESS_EXIT:p1",
  "type": "PROCESS_EXIT",
  "severity": "WARNING",
  "state": "FIRING",
  "node_id": "n1",
  "process_id": "p1",
  "first_at": "2026-08-16T00:00:00Z",
  "last_at": "2026-08-16T00:00:00Z",
  "payload": {}
}
```

HMAC：请求头 `X-ProcMesh-Signature` = hex(HMAC-SHA256(hmac_secret, raw body))。  
企业微信：`{"msgtype":"text","text":{"content":"<type> <severity> <fingerprint> <state>"}}`。  
钉钉：同上 text，URL 加 `timestamp` 与 `sign`（标准加签：`base64(hmac-sha256(secret, timestamp+"\n"+secret))`，再 query-escape）。  
Slack：`{"text":"<type> <severity> <fingerprint> <state>"}`。  
Email：Subject `[<severity>] <type> node=<node_id>`，Body 为 payload JSON 文本。STARTTLS 仅当 `starttls=true`。

`Send` 同步，最多 3 次，间隔 50ms、100ms（测试可注入 backoff）。仍失败写 audit action=`alert.send` result=`error`（metadata **不含** secret）并写入行 `last_error`。成功清空 `last_error` 并刷新 `notified_at`。

WEB 通道：`Send` 直接 `nil`，不改 `notified_at`（inbox 行已在 Observe 写入）。

### 表

```sql
CREATE TABLE IF NOT EXISTS alerts (
    alert_id TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    severity TEXT NOT NULL,
    node_id TEXT NOT NULL,
    process_id TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '{}',
    state TEXT NOT NULL,
    first_at TEXT NOT NULL,
    last_at TEXT NOT NULL,
    notified_at TEXT,
    resolved_at TEXT,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS alerts_last_at ON alerts(last_at DESC);
CREATE INDEX IF NOT EXISTS alerts_state ON alerts(state);
```

---

### Task 1: SQLite alerts

**Files:**
- Modify: `internal/store/schema.sql`
- Create: `internal/store/alert.go`
- Test: `internal/store/alert_test.go`

**Interfaces:**
- Consumes: `store.Open`、`formatTime` / `parseTime`（与 batch 相同 RFC3339Nano）
- Produces:

```go
type AlertRecord struct {
    AlertID, Fingerprint, Type, Severity string
    NodeID, ProcessID, PayloadJSON, State string
    FirstAt, LastAt, NotifiedAt, ResolvedAt time.Time
    LastError string
}

func (s *Store) UpsertAlert(ctx context.Context, rec AlertRecord) error
func (s *Store) GetAlert(ctx context.Context, alertID string) (AlertRecord, error)          // 缺 → NOT_FOUND
func (s *Store) GetAlertByFingerprint(ctx context.Context, fp string) (AlertRecord, error) // 缺 → NOT_FOUND
func (s *Store) ListAlerts(ctx context.Context, limit int) ([]AlertRecord, error)           // last_at DESC；0→50，封顶 200
```

`UpsertAlert`：按 `fingerprint` `INSERT ... ON CONFLICT(fingerprint) DO UPDATE`，**不**改 `alert_id` / `first_at`；更新 type/severity/node/process/payload/state/last_at/notified_at/resolved_at/last_error。首次插入用传入的 `alert_id` 与 `first_at`。空 `alert_id` 或空 `fingerprint` → `INVALID`。

- [ ] **Step 1: Write the failing test**

```go
func TestAlert_UpsertGetListByFingerprint(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	first := time.Unix(1_700_000_000, 0).UTC()
	rec := store.AlertRecord{
		AlertID: "a1", Fingerprint: "PROCESS_EXIT:p1", Type: "PROCESS_EXIT",
		Severity: "WARNING", NodeID: "n1", ProcessID: "p1", PayloadJSON: `{}`,
		State: "FIRING", FirstAt: first, LastAt: first,
	}
	if err := s.UpsertAlert(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAlertByFingerprint(ctx, "PROCESS_EXIT:p1")
	if err != nil || got.AlertID != "a1" || got.State != "FIRING" {
		t.Fatalf("got %+v %v", got, err)
	}
	rec.LastAt = first.Add(time.Minute)
	rec.AlertID = "should-not-replace"
	rec.State = "FIRING"
	if err := s.UpsertAlert(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAlert(ctx, "a1")
	if err != nil || got.AlertID != "a1" || !got.LastAt.Equal(first.Add(time.Minute)) {
		t.Fatalf("reuse failed %+v %v", got, err)
	}
	if _, err := s.GetAlert(ctx, "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
	list, err := s.ListAlerts(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Fingerprint != "PROCESS_EXIT:p1" {
		t.Fatalf("list %+v %v", list, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store -run TestAlert_UpsertGetListByFingerprint -count=1`

Expected: FAIL（缺类型或表）

- [ ] **Step 3: Write minimal implementation**

按 Interfaces 实现。时间列用已有 `formatTime`/`scanTime`；`notified_at`/`resolved_at` 零值写 NULL。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store/schema.sql internal/store/alert.go internal/store/alert_test.go
git commit -m "feat(store): add alerts table and fingerprint upsert"
```

---

### Task 2: Raft AlertChannel / AlertPolicy + 失 quorum 门闩

**Files:**
- Modify: `internal/control/command.go`
- Modify: `internal/control/fsm.go`
- Test: `internal/control/fsm_test.go`
- Modify: `internal/auth/rbac.go`
- Test: `internal/auth/rbac_test.go`

**Interfaces:**
- Consumes: `EncodeCommand`、`State.Apply`、`ensure()`、`isControlPlaneWrite`
- Produces:

```go
const (
    CmdAlertChannelPut    = "alert_channel_put"
    CmdAlertChannelDelete = "alert_channel_delete"
    CmdAlertPolicyPut     = "alert_policy_put"
)

type AlertChannel struct {
    ChannelID   string `json:"channel_id"`
    Type        string `json:"type"`
    Name        string `json:"name"`
    Enabled     bool   `json:"enabled"`
    ConfigJSON  string `json:"config_json,omitempty"`
    CreatedUnix int64  `json:"created_unix"`
    UpdatedUnix int64  `json:"updated_unix"`
}

type AlertPolicy struct {
    DedupWindowSec      int64 `json:"dedup_window_sec"`
    NotifyOnResolve     bool  `json:"notify_on_resolve"`
    CPUHighPercent      int   `json:"cpu_high_percent"`
    MemoryHighPercent   int   `json:"memory_high_percent"`
    DiskHighPercent     int   `json:"disk_high_percent"`
    HighConsecutiveMins int   `json:"high_consecutive_mins"`
    SuspectTooLongSec   int64 `json:"suspect_too_long_sec"`
}

func DefaultAlertPolicy() AlertPolicy

type AlertChannelPutBody struct {
    ChannelID, Type, Name, ConfigJSON string
    Enabled                           bool
    NowUnix                           int64
}
type AlertChannelDeleteBody struct{ ChannelID string }
type AlertPolicyPutBody struct {
    DedupWindowSec      int64
    NotifyOnResolve     bool
    CPUHighPercent, MemoryHighPercent, DiskHighPercent int
    HighConsecutiveMins int
    SuspectTooLongSec   int64
}
```

`State` 增加 `AlertChannels map[string]AlertChannel`、`AlertPolicy AlertPolicy`。`ensure()`：nil map；若 `DedupWindowSec==0` 填 `DefaultAlertPolicy()`（`NotifyOnResolve` 默认 true，因此用「窗口为 0」判断未初始化，不允许策略把窗口设为 0：`<1` → `INVALID`）。

校验：type 枚举；name 与 AgentGroup 相同正则；`ConfigJSON` 必须是对象 JSON 或空（空当 `{}`）。删缺 id → `NOT_FOUND`。策略百分比 1–100；`HighConsecutiveMins` 1–60；`SuspectTooLongSec` 1–86400。

`isControlPlaneWrite` 增加 `PermAlertManage`。`isMutation` 只读列表增加 `PermAlertRead`。

- [ ] **Step 1: Write the failing tests**

```go
func TestFSM_AlertChannelAndPolicy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if s.AlertPolicy.DedupWindowSec != 600 || !s.AlertPolicy.NotifyOnResolve {
		t.Fatalf("defaults %+v", s.AlertPolicy)
	}
	if err := s.Apply(mustEncode(t, "alert_channel_put", control.AlertChannelPutBody{
		ChannelID: "c1", Type: "WEBHOOK", Name: "hook", Enabled: true,
		ConfigJSON: `{"url":"https://example","hmac_secret":"s"}`, NowUnix: now.Unix(),
	}), now); err != nil {
		t.Fatal(err)
	}
	ch := s.AlertChannels["c1"]
	if ch.Name != "hook" || !strings.Contains(ch.ConfigJSON, "hmac_secret") {
		t.Fatalf("channel %+v", ch)
	}
	if err := s.Apply(mustEncode(t, "alert_policy_put", control.AlertPolicyPutBody{
		DedupWindowSec: 120, NotifyOnResolve: false, CPUHighPercent: 80,
		MemoryHighPercent: 80, DiskHighPercent: 85, HighConsecutiveMins: 3, SuspectTooLongSec: 60,
	}), now); err != nil {
		t.Fatal(err)
	}
	if s.AlertPolicy.CPUHighPercent != 80 || s.AlertPolicy.NotifyOnResolve {
		t.Fatalf("policy %+v", s.AlertPolicy)
	}
	if err := s.Apply(mustEncode(t, "alert_channel_delete", control.AlertChannelDeleteBody{ChannelID: "c1"}), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AlertChannels["c1"]; ok {
		t.Fatal("channel should be gone")
	}
}

func TestFSM_AlertChannelValidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(mustEncode(t, "alert_channel_put", control.AlertChannelPutBody{
		ChannelID: "c1", Type: "SMS", Name: "x", NowUnix: now.Unix(),
	}), now)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}
```

`TestAllowWrite_NoQuorumBlocksUserCreate` 末尾追加：

```go
err = svc.AllowWrite(admin, auth.PermAlertManage, "", true)
requireCode(t, err, errcode.UNAVAILABLE, "control quorum lost")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/control ./internal/auth -run 'TestFSM_Alert|TestAllowWrite_NoQuorumBlocksUserCreate' -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`Apply` 增加三个 case。`ensure()` 初始化。auth 两处 switch 补 perm。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/control ./internal/auth -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/control/command.go internal/control/fsm.go internal/control/fsm_test.go internal/auth/rbac.go internal/auth/rbac_test.go
git commit -m "feat(control): AlertChannel and AlertPolicy Raft commands"
```

---

### Task 3: Engine — fingerprint、复用行、去重窗口

**Files:**
- Create: `internal/alert/types.go`
- Create: `internal/alert/types_test.go`
- Create: `internal/alert/engine.go`
- Test: `internal/alert/engine_test.go`

**Interfaces:**
- Consumes: `store.AlertRecord`、`control.AlertPolicy`、`control.AlertChannel`
- Produces:

```go
package alert

type Type string
type Severity string
type State string

const (
    TypeProcessExit Type = "PROCESS_EXIT"
    // ... 全部 13 个 type 常量，名字与规格表一致
    SevWarning  Severity = "WARNING"
    SevCritical Severity = "CRITICAL"
    StateFiring   State = "FIRING"
    StateResolved State = "RESOLVED"
)

func Fingerprint(typ Type, nodeID, processID, clusterID string) string
func DefaultSeverity(typ Type) Severity

type Event struct {
    Type                 Type
    NodeID, ProcessID, ClusterID string
    Payload              map[string]any
    At                   time.Time
    Firing               bool
}

type Sender interface {
    Send(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error
}

type Engine struct {
    Store      *store.Store
    NodeID     string
    NewID      func() string
    Policy     func() control.AlertPolicy
    Channels   func() []control.AlertChannel
    Sender     Sender
    Audit      func(action, result, meta string) // 可空
    Now        func() time.Time
}

func (e *Engine) Observe(ctx context.Context, ev Event) (store.AlertRecord, error)
```

`Fingerprint` 必须符合规格表（含 `NODE_FAILED` / `NODE_SUSPECT`）。  
`Observe`：

1. 算 fp；空 process 的进程类 type → `INVALID`。
2. `GetAlertByFingerprint`；没有则新 `alert_id`，`first_at=At`。
3. FIRING：state=`FIRING`，更新 `last_at`，清 `resolved_at`。若 `notified_at.IsZero()` 或 `At.Sub(notified_at) >= DedupWindow`，对每个 `enabled && type!=WEB` 的通道 `Send`；任一成功则刷新 `notified_at`。WEB 只保证行存在。全部失败写 `last_error`，不刷新 `notified_at`。
4. RESOLVED：若当前已 RESOLVED，只更新 `last_at` 不 Send。否则 state=`RESOLVED`，`resolved_at=At`；`NotifyOnResolve` 为 true 时按同样去重规则 Send。
5. 最后 `UpsertAlert`。

测试用 `recordingSender`（记录调用，可按通道返回 error）。

- [ ] **Step 1: Write the failing tests**

```go
func TestFingerprint_AgentFailedUsesNodeFailedPrefix(t *testing.T) {
	if alert.Fingerprint(alert.TypeAgentFailed, "n1", "", "") != "NODE_FAILED:n1" {
		t.Fatal(alert.Fingerprint(alert.TypeAgentFailed, "n1", "", ""))
	}
	if alert.Fingerprint(alert.TypeAgentSuspect, "n1", "", "") != "NODE_SUSPECT:n1" {
		t.Fatal("suspect")
	}
	if alert.Fingerprint(alert.TypeControlNoQuorum, "", "", "cid") != "CONTROL_NO_QUORUM:cid" {
		t.Fatal("quorum")
	}
	if alert.Fingerprint(alert.TypeCPUHigh, "n1", "p1", "") != "CPU_HIGH:p1" {
		t.Fatal("cpu proc")
	}
	if alert.Fingerprint(alert.TypeCPUHigh, "n1", "", "") != "CPU_HIGH:n1" {
		t.Fatal("cpu node")
	}
}

func TestEngine_ReuseRowAndDedupWindow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	snd := &recordingSender{}
	now := time.Unix(1_700_000_000, 0).UTC()
	eng := &alert.Engine{
		Store: st, NodeID: "n1", NewID: func() string { return "a1" },
		Policy: func() control.AlertPolicy { return control.DefaultAlertPolicy() },
		Channels: func() []control.AlertChannel {{
			ChannelID: "c1", Type: "WEBHOOK", Name: "h", Enabled: true, ConfigJSON: `{"url":"http://x"}`,
		}},
		Sender: snd,
		Now:    func() time.Time { return now },
	}
	ev := alert.Event{Type: alert.TypeProcessExit, NodeID: "n1", ProcessID: "p1", At: now, Firing: true}
	r1, err := eng.Observe(ctx, ev)
	if err != nil || r1.AlertID != "a1" || snd.n != 1 {
		t.Fatalf("first %+v n=%d err=%v", r1, snd.n, err)
	}
	ev.At = now.Add(time.Minute)
	r2, err := eng.Observe(ctx, ev)
	if err != nil || r2.AlertID != "a1" || snd.n != 1 {
		t.Fatalf("dedup %+v n=%d err=%v", r2, snd.n, err)
	}
	ev.At = now.Add(11 * time.Minute)
	if _, err := eng.Observe(ctx, ev); err != nil || snd.n != 2 {
		t.Fatalf("window n=%d err=%v", snd.n, err)
	}
}

func TestEngine_InboxWithoutOutboundStillWrites(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := &alert.Engine{
		Store: st, NewID: func() string { return "a2" },
		Policy:   func() control.AlertPolicy { return control.DefaultAlertPolicy() },
		Channels: func() []control.AlertChannel{},
		Sender:   &recordingSender{},
		Now:      func() time.Time { return time.Unix(1, 0).UTC() },
	}
	r, err := eng.Observe(ctx, alert.Event{Type: alert.TypeProcessFatal, NodeID: "n1", ProcessID: "p1", At: time.Unix(1, 0).UTC(), Firing: true})
	if err != nil || r.State != alert.StateFiring {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestEngine_ResolveRespectsNotifyOnResolve(t *testing.T) {
	// NotifyOnResolve=false：FIRING 发 1 次，RESOLVED 不再 Send
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alert -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

只实现 types + Observe。不要在本任务写 HTTP/SMTP。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/alert -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/alert
git commit -m "feat(alert): fingerprint reuse and dedup window"
```

---

### Task 4: 全通道 Sender

**Files:**
- Create: `internal/alert/channel.go`
- Test: `internal/alert/channel_test.go`

**Interfaces:**
- Consumes: `Sender`、通道 config 键（见锁定模型）
- Produces:

```go
type HTTPDoer interface {
    Do(*http.Request) (*http.Response, error)
}
type ChannelSender struct {
    HTTP    HTTPDoer          // 默认 http.DefaultClient
    DialSMTP func(host string, cfg EmailConfig) (smtpSendCloser, error) // 测试注入
    Sleep   func(time.Duration)
    Attempts int // 默认 3
}
func (s *ChannelSender) Send(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error
```

WEB → nil。未知 type → `INVALID`。`enabled` 由 Engine 过滤，Sender 仍应在 disabled 时直接 nil。

- [ ] **Step 1: Write the failing tests**

用 `httptest.NewServer`：

1. WEBHOOK：断言 POST JSON 字段、`X-ProcMesh-Signature` 等于 hex hmac。
2. WEBHOOK 无 hmac：无该头。
3. WECOM / SLACK：body 形状。
4. DINGTALK：query 含 `timestamp` 与 `sign`，签名可复算。
5. EMAIL：`DialSMTP` fake 记录 from/to/subject。
6. 前两次 500、第三次 200 → 成功且 Do 调用 3 次。
7. 三次都失败 → 返回 error。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alert -run TestChannel -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

按锁定模型实现。重试只对网络/5xx；4xx 不重试。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/alert -count=1`

Expected: PASS；`go test -cover ./internal/alert` ≥ 80%

- [ ] **Step 5: Commit**

```bash
git add internal/alert/channel.go internal/alert/channel_test.go
git commit -m "feat(alert): webhook email wecom dingtalk slack senders"
```

---

### Task 5: Scanner — 进程 / 健康 / 本机 DB / 资源阈值

**Files:**
- Create: `internal/alert/scan.go`
- Test: `internal/alert/scan_test.go`

**Interfaces:**
- Consumes: `Engine.Observe`、`store.ListMetricSamples`、`metrics.Series*` / `LayerRawMin`
- Produces:

```go
type ProcessSnap struct {
    ProcessID string
    Desired   string // RUNNING|STOPPED
    Observed  string
    Health    string
}
type NodeSample struct {
    CPUPercent, MemoryPercent, DiskPercent float64
    MemoryTotalBytes                       int64
    HaveSnapshot                           bool
}
type Scanner struct {
    Engine   *Engine
    NodeID   string
    ListProcs func() []ProcessSnap
    Samples   func(ctx context.Context, series, subject, layer string, from, to int64) ([]store.MetricSample, error)
    Snapshot  func() NodeSample
    ProcCPU   func(processID string) float64          // 即时；未知 <0
    ProcMem   func(processID string) int64            // bytes；未知 <0
    Degraded  func() bool
    Now       func() time.Time
}

func (s *Scanner) ScanLocal(ctx context.Context) error
```

`ScanLocal`：对每个进程按锁定规则 Observe FIRING/RESOLVED；`Degraded()` true → `LOCAL_DB_ERROR` FIRING，false → RESOLVE。节点 CPU/Mem/Disk 与进程 CPU/Mem 用连续分钟逻辑。

- [ ] **Step 1: Write the failing tests**

```go
func TestScanner_ProcessExitAndRecover(t *testing.T) { /* EXITED→FIRING；再 RUNNING→RESOLVED */ }
func TestScanner_CrashLoopBackoffAndFatal(t *testing.T) { /* BACKOFF 与 FATAL 两个 fingerprint */ }
func TestScanner_HealthFailed(t *testing.T) {}
func TestScanner_LocalDBError(t *testing.T) {}
func TestScanner_CPUHighNeedsConsecutiveMinutes(t *testing.T) {
    // 插入 1 个超阈值 raw_min 点，high_consecutive_mins=2 → 不 FIRING
    // 插入连续 2 个 → FIRING
    // 缺口不把连续计数跨过去
}
```

用真实 `store` + `Engine` + `recordingSender`（0 通道即可查 inbox）。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alert -run TestScanner_ -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

不要在本任务处理 Leader/集群。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/alert -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/alert/scan.go internal/alert/scan_test.go
git commit -m "feat(alert): scan process health db and resource thresholds"
```

---

### Task 6: Scanner — 集群级 + SUSPECT + IsVoter

**Files:**
- Modify: `internal/alert/scan.go`
- Test: `internal/alert/scan_test.go`
- Modify: `internal/cluster/mesh.go`（从 `memberlist.Members()` 映射 `StateSuspect`）
- Test: `internal/cluster/mesh_test.go`
- Modify: `internal/control/raft.go`
- Test: `internal/control/raft_test.go`

**Interfaces:**
- Consumes: `cluster.NodeSummary`、`control.Node.IsLeader` / `HasQuorum` / `LeaderAddr`
- Produces:

```go
func (n *Node) IsVoter() bool

type ClusterView struct {
    ClusterID string
    Leader    bool
    Voter     bool
    HasQuorum bool
    LeaderAddr string
    LeaderMissingSince time.Time // 测试可设；生产由 Scanner 记住
    Members   []cluster.NodeSummary
    CertNotAfter map[string]time.Time // node_id → NotAfter；缺省只填本机
}

func (s *Scanner) ScanCluster(ctx context.Context, view ClusterView) error
```

规则：

- 仅 `view.Leader && view.HasQuorum` 时对成员发 `AGENT_FAILED`（`StateFailed`）、`AGENT_SUSPECT_TOO_LONG`（`StateSuspect` 且 `now-last_updated >= SuspectTooLong`）、`CERT_EXPIRING`、`VERSION_MISMATCH`。非 Leader 调用 `ScanCluster` 不得对这些 type `Observe`。
- 节点回到 `ALIVE` → 对应 fingerprint RESOLVED。
- `view.Voter && !HasQuorum`（或 `LeaderAddr==""`）持续 `SuspectTooLong` → `CONTROL_NO_QUORUM` FIRING。恢复 quorum → RESOLVED。Scanner 用内部 `leaderGoneAt` 记住首次丢失时间；测试可通过连续两次 Scan + 注入 `Now` 推进。
- 非 voter 不发 `CONTROL_NO_QUORUM`。

Mesh：在已有刷新路径（`NotifyUpdate` 之后或 `Members()` 组装前）读取 `m.list.Members()`；`memberlist.StateSuspect` → `cluster.StateSuspect`（不覆盖 LEFT/REMOVED/REVOKED/FAILED）。单测若难以驱动真 memberlist，至少给 `upsert` 一条 `StateSuspect` 的单元断言：`ScanCluster` 见到 `StateSuspect` 即发告警。Mesh 映射可作为附加测试；若 memberlist 测试夹具过重，用未导出 helper 的导出测试函数 `ApplyMemberlistState(nodeID, state)` 也可，但生产路径必须接线。

- [ ] **Step 1: Write the failing tests**

```go
func TestScanner_OnlyLeaderSendsAgentFailed(t *testing.T) {
    // follower view.Leader=false，成员 FAILED → inbox 无 NODE_FAILED
    // leader=true → 有
}
func TestScanner_FollowerDoesNotSendCertOrVersion(t *testing.T) {}
func TestScanner_VoterNoQuorumAfterSuspectWindow(t *testing.T) {}
func TestScanner_NonVoterNoQuorumSilent(t *testing.T) {}
func TestNode_IsVoter(t *testing.T) {
    // 复用 startInmemVoters：voter true；若有 nonvoter 夹具则 false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alert ./internal/control -run 'TestScanner_OnlyLeader|TestScanner_Follower|TestScanner_Voter|TestScanner_NonVoter|TestNode_IsVoter' -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

- [ ] **Step 4: Run tests**

Run: `go test ./internal/alert ./internal/control ./internal/cluster -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/alert/scan.go internal/alert/scan_test.go internal/cluster/mesh.go internal/cluster/mesh_test.go internal/control/raft.go internal/control/raft_test.go
git commit -m "feat(alert): leader-only cluster alerts and voter no-quorum"
```

---

### Task 7: Proto AlertService + 代码生成

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Modify: `internal/api/proto_gen_test.go`
- Generate: `proto/procmesh/v1/api.pb.go`、`proto/procmesh/v1/procmeshv1connect/api.connect.go`、`web/src/gen/procmesh/v1/api_pb.ts`

**Interfaces:**
- Consumes: 现有 `MutationMeta`、`ErrorInfo`
- Produces: 下列消息与服务（追加在 `BatchService` 之后，不要改已有字段号）

```protobuf
message Alert {
  string alert_id = 1;
  string fingerprint = 2;
  string type = 3;
  string severity = 4;
  string node_id = 5;
  string process_id = 6;
  string payload_json = 7;
  string state = 8; // FIRING | RESOLVED
  int64 first_unix_ms = 9;
  int64 last_unix_ms = 10;
  int64 notified_unix_ms = 11;
  int64 resolved_unix_ms = 12;
  string last_error = 13;
}

message AlertEntry {
  Alert alert = 1;
  string source_node = 2;
  string freshness = 3; // LIVE | STALE | UNKNOWN
  int64 last_updated_unix_ms = 4;
}

message ListAlertsRequest {
  int32 limit = 1;          // 0 = 50；封顶 200
  string target_node = 2;   // 空 = 聚合
  string state = 3;         // 空 = 全部；FIRING | RESOLVED
}
message ListAlertsResponse { repeated AlertEntry entries = 1; }

message GetAlertRequest { string alert_id = 1; }
message GetAlertResponse { AlertEntry entry = 1; }

message AlertChannel {
  string channel_id = 1;
  string type = 2;
  string name = 3;
  bool enabled = 4;
  string config_json = 5; // 已脱敏
  int64 created_unix = 6;
  int64 updated_unix = 7;
}

message ListAlertChannelsRequest {}
message ListAlertChannelsResponse { repeated AlertChannel channels = 1; }

message PutAlertChannelRequest {
  MutationMeta meta = 1;
  string channel_id = 2; // 空 = 创建
  string type = 3;
  string name = 4;
  bool enabled = 5;
  string config_json = 6;
}
message PutAlertChannelResponse { AlertChannel channel = 1; }

message DeleteAlertChannelRequest {
  MutationMeta meta = 1;
  string channel_id = 2;
}
message DeleteAlertChannelResponse {}

message AlertPolicy {
  int64 dedup_window_sec = 1;
  bool notify_on_resolve = 2;
  int32 cpu_high_percent = 3;
  int32 memory_high_percent = 4;
  int32 disk_high_percent = 5;
  int32 high_consecutive_mins = 6;
  int64 suspect_too_long_sec = 7;
}

message GetAlertPolicyRequest {}
message GetAlertPolicyResponse { AlertPolicy policy = 1; }

message PutAlertPolicyRequest {
  MutationMeta meta = 1;
  AlertPolicy policy = 2;
}
message PutAlertPolicyResponse { AlertPolicy policy = 1; }

service AlertService {
  rpc ListAlerts(ListAlertsRequest) returns (ListAlertsResponse);
  rpc GetAlert(GetAlertRequest) returns (GetAlertResponse);
  rpc ListAlertChannels(ListAlertChannelsRequest) returns (ListAlertChannelsResponse);
  rpc PutAlertChannel(PutAlertChannelRequest) returns (PutAlertChannelResponse);
  rpc DeleteAlertChannel(DeleteAlertChannelRequest) returns (DeleteAlertChannelResponse);
  rpc GetAlertPolicy(GetAlertPolicyRequest) returns (GetAlertPolicyResponse);
  rpc PutAlertPolicy(PutAlertPolicyRequest) returns (PutAlertPolicyResponse);
}
```

- [ ] **Step 1: Write the failing test**

在 `proto_gen_test.go`：

```go
func TestProto_AlertServiceGenerated(t *testing.T) {
	if procmeshv1connect.AlertServiceName != "procmesh.v1.AlertService" {
		t.Fatalf("alert=%s", procmeshv1connect.AlertServiceName)
	}
	if procmeshv1connect.AlertServiceListAlertsProcedure == "" {
		t.Fatal("missing ListAlerts")
	}
	_ = (&procmeshv1.Alert{}).GetFingerprint
	_ = (&procmeshv1.AlertEntry{}).GetFreshness
	_ = (&procmeshv1.AlertChannel{}).GetConfigJson
	_ = (&procmeshv1.AlertPolicy{}).GetDedupWindowSec
	_ = (&procmeshv1.PutAlertChannelRequest{}).GetMeta
	var _ procmeshv1connect.AlertServiceHandler = (*AlertAPI)(nil)
}
```

本任务 `AlertAPI` 可以是空 struct 以满足接口编译；若生成代码尚无接口则测试先失败。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestProto_AlertServiceGenerated -count=1`

Expected: FAIL

- [ ] **Step 3: 改 proto 并生成**

```bash
make proto
make proto-ts
```

`AlertAPI` 本任务只回 `UNAVAILABLE` `not implemented`（与 P5 Audit 桩相同），以便 `var _` 通过。完整实现在 Task 8。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api -run 'TestProto_' -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add proto internal/api/proto_gen_test.go internal/api/alert.go web/src/gen
git commit -m "feat(proto): add AlertService"
```

---

### Task 8: AlertAPI + 聚合 STALE + Agent 接线 + 指标

**Files:**
- Modify: `internal/api/alert.go`、`internal/api/alert_test.go`（新建测试）
- Modify: `internal/api/process.go`（`Forwarder` 加 `Alert`）
- Modify: 所有 `Forwarder` 实现：`internal/agent/rpc.go` `agentForwarder`、`internal/api/apitest_test.go` `fakeForwarder`、`internal/api/auditapi_test.go` `blockingAuditForwarder`、`internal/api/metrics.go` `countingForwarder`
- Modify: `internal/rpc/client.go`（`NewAlertClient`）
- Modify: `internal/api/server.go`
- Modify: `internal/agent/rpc.go` `localHandler` 挂 LocalOnly AlertService
- Modify: `internal/agent/run.go`（Engine + Scanner ticker + 指标）
- Modify: `internal/api/metrics.go`（若 `/metrics` 在此暴露计数器）或 `internal/alert` 用 `prometheus`/`expvar` 与现有 batch 指标同一风格

**Interfaces:**
- Consumes: `applyAuth`、`metaOf`、`requirePerm`、`AuditAPI` 聚合模式
- Produces: 可用的 `AlertAPI`

```go
type AlertAPI struct {
    Store     *store.Store
    Auth      *auth.Service
    Engine    *alert.Engine // 可空；List 不需要
    LocalOnly bool
    LocalID   string
    Router    *Router
    Forward   Forwarder
    Members   func() []cluster.NodeSummary
    Now       func() time.Time
}
```

行为：

- List/Get/ListChannels/GetPolicy：`alert.read`，`write=false`，`local=true`
- Put/Delete Channel、PutPolicy：`alert.manage`，`write=true`，`local=true`，必须 `metaOf`
- `ListAlerts`：照抄 `AuditAPI.ListAudit`（2s hop，FAILED/SUSPECT 占位 STALE，ALIVE 超时 STALE）。占位 `AlertEntry.alert` 可空，但 `source_node`+`freshness=STALE` 必须在。按 `last_unix_ms` 降序。`state` 过滤只应用于**真实行**，STALE 占位始终保留（避免「过滤后看起来无告警」）。
- GetAlert：本机 `GetAlert`；NOT_FOUND 且非 LocalOnly 时不要假装成功。
- PutChannel：空 `channel_id` 则 `newAuthID()`。读现有通道，`mergeChannelConfig(old, new)` 保留空 secret。`applyAuth(CmdAlertChannelPut)`。响应 `redactChannelConfig`。
- Delete/PutPolicy 同 GroupAPI。
- 失 quorum：靠 Task 2 的 `PermAlertManage` 门闩，测一个 API 级用例。
- Viewer 无 `alert.manage` → `DENIED`。
- 指标：`alert_send_total{type,result}`（`result=ok|error`）。在 `ChannelSender.Send` 或 Engine 包装计数；`/metrics` 必须能刮到。复用 `internal/api/metrics.go` 里 batch 计数的注册方式。

Agent 接线：

- `serveHTTP` 里 `Engine` + `Scanner`，ticker **15s**（不要塞进 1s reconcile）。
- `ListProcs` ← `mgr.ListSpecs` + `ListInstances`（每进程取 ordinal 0 或任意实例：若任一实例命中规则即 FIRING）。
- `Samples` ← `st.ListMetricSamples`。
- `Snapshot` ← collector `NodeMetrics`。
- `Degraded` ← 现有 degraded 闭包。
- `ScanCluster`：`IsLeader`/`IsVoter`/`HasQuorum`/`LeaderAddr` + `mesh.Members()` + 本机 `CertNotAfter`。
- `Policy`/`Channels` ← `auth.Store().View()`。
- 磁盘 95% 不阻止 inbox 写（alert 行不是 metrics history）。
- RPC `localHandler` 必须挂 AlertService，否则聚合 hop 失败。

- [ ] **Step 1: Write the failing tests**

`alert_test.go`：

1. PutChannel 无 `operation_id` → INVALID。
2. Viewer PutChannel → DENIED。
3. Admin Put WEBHOOK 含 secret，List 回显无 `hmac_secret`，二次 Put 空 secret 后 FSM 仍有原 secret（读 `Auth.Store().View()`）。
4. ListAlerts 本机一行 LIVE。
5. 远程 ALIVE hop 失败 → 该 `source_node` freshness=STALE，且响应 `len>0`（即使本机 0 行）。
6. 失 quorum PutChannel → UNAVAILABLE。

仿 `group_test.go` 的 `newRBACEnv`。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestAlert -count=1`

Expected: FAIL

- [ ] **Step 3: Write implementation + 接线**

把所有 Forwarder 补上 `Alert(...)`。`run.go` 启动 scanner。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api ./internal/agent ./internal/rpc ./internal/alert -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/agent internal/rpc internal/alert
git commit -m "feat(api): AlertService with STALE aggregation and agent scanner"
```

---

### Task 9: CLI

**Files:**
- Create: `internal/cli/alert.go`
- Modify: `internal/cli/root.go`（usage + switch `alert`）
- Modify: `internal/cli/client.go`（`c.alert`）
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: 生成的 `AlertServiceClient`、`c.meta()`
- Produces:

```text
procmesh alert list [--state FIRING|RESOLVED]
procmesh alert get ALERT_ID
procmesh alert channel list
procmesh alert channel put --type T --name N [--id ID] [--enabled true|false] [--config JSON]
procmesh alert channel delete CHANNEL_ID
procmesh alert policy get
procmesh alert policy put --dedup-window-sec N --notify-on-resolve true|false \
  --cpu N --memory N --disk N --consecutive N --suspect-too-long-sec N
```

`alert list` 每行：`alert_id=... fingerprint=... type=... state=... node=... freshness=...`。STALE 占位无 alert_id 时打印 `source_node=... freshness=STALE`。

- [ ] **Step 1: Write the failing test**

在 `root_test.go` 断言 `Main([]string{"alert"}, ...)` 不是 unknown command（usage 含 `alert list`）。若已有 client fake 模式，补一条 list。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -count=1`

Expected: FAIL 或 usage 不含 alert

- [ ] **Step 3: Implement**

手写 switch，与 `group.go` / `batch.go` 同形。未传 `--operation-id` 时沿用 `Main` 已生成的 UUID。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cli -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): alert list channel and policy commands"
```

---

### Task 10: Web Alerts 页 + Overview Recent Alerts（P5 合同）

**Files:**
- Modify: `web/src/lib/rpc.ts`、`web/src/router.ts`、`web/src/components/AppShell.vue`
- Create: `web/src/pages/AlertsPage.vue`、`web/src/pages/AlertsPage.test.ts`
- Modify: `web/src/pages/OverviewPage.vue`、`web/src/pages/OverviewPage.test.ts`
- Modify: `web/public/locales/en/common.json`、`web/public/locales/zh/common.json`
- Create: `web/e2e/alert.spec.ts`

**Interfaces:**
- Consumes: 生成的 `AlertService`、`FreshnessBadge`、`newOperationId`
- Produces: `/alerts` inbox + Channel/Policy；Overview Recent Alerts

页面要求（P5 质量，不得缩水）：

1. 导航：`alert.read` 才显示 Alerts（lucide `Bell`），在 Batches 后、Users 前。
2. Inbox：fingerprint、type、severity、state、node、process、`FreshnessBadge`、`last_updated`。FIRING 用 `--danger` 文字色，**不要**绿底。STALE 行 `data-freshness="STALE"`，背景不得是 `#D1FAE5`。
3. 聚合提示：只要存在 STALE 条目，显示横幅 `alert.staleBanner`（中：`部分节点不可达，不能视为当前无告警。` 英：`Some nodes are unreachable. This is not an empty inbox.`）。**禁止**在有 STALE 时渲染「No alerts」成功空态。
4. Channel 表单：type 下拉、name、enabled、config textarea。`alert.manage` 才显示保存/删除。Viewer 只读。Put 带 `operationId`。回显不得出现 `hmac_secret`/`password`/`"secret"` 值。
5. Policy 表单：七个字段；Viewer 只读。
6. Overview：`listAlerts({limit:5})` 卡片，标题 `overview.recentAlerts`。有 STALE 时同样横幅，不得画成无告警。
7. `npm run i18n:check` 必须过。键至少：`nav.alerts`、`alert.title`、`alert.staleBanner`、`alert.channels`、`alert.policy`、`alert.noAlerts`、`alert.firing`、`alert.resolved`、`overview.recentAlerts`。中英都写。

Vitest：

- mock list 含一条 FIRING LIVE 与一条空 alert 的 STALE 占位 → STALE 徽章存在、横幅存在、没有「无告警」空态。
- 无 `alert.manage` 时不渲染保存按钮。
- Overview 有 STALE 时 hint/横幅可见。

Playwright `web/e2e/alert.spec.ts`：登录后 `/alerts` 不 404；横幅或空态二者按数据互斥。若现有 e2e 只打真 agent，断言导航存在且页面标题可见。

- [ ] **Step 1: Write the failing tests**

`AlertsPage.test.ts`、`OverviewPage.test.ts` 按上。

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- AlertsPage OverviewPage AppShell`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`rpc.ts` 增加 `useAlertClient()`。路由 `/alerts`。样式 token 与 Batches/Audit 一致。

- [ ] **Step 4: Run tests**

```bash
cd web && npm test && npm run i18n:check
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web
git commit -m "feat(web): Alerts page and Overview recent alerts with STALE banner"
```

---

### Task 11: Q4 验收 + embed + 索引

**Files:**
- Create: `internal/agent/q4_accept_test.go`
- Modify: `docs/superpowers/plans/2026-08-16-v1.1.md`
- Modify: `docs/superpowers/plans/2026-08-16-q4-alerts-channels.md`（本文件 Status）
- Rebuild: `internal/web/dist`（`make web`）

**Interfaces:**
- Consumes: 全部前序 + `startClusterAgent` / `joinTwo` / `mustCLI` / `readNodeID` / `startClusterAgentCtl`
- Produces: 可脚本化验收

复用 Q2/Q3 helper。测试：

```go
func TestQ4_ProcessKillWritesInbox(t *testing.T) {
    // 单节点 cluster init 后 apply 一个短命进程（/bin/false 或 sleep+kill）
    // 等到 observed EXITED/FATAL，CLI `alert list` 含 PROCESS_EXIT 或 PROCESS_FATAL
}

func TestQ4_LostQuorumRejectsPutChannelOwnerStillWritesInbox(t *testing.T) {
    // 三节点或用已有失 quorum 夹具：PutChannel 失败 UNAVAILABLE
    // Owner 仍可因本地 degraded/进程扫描写出 inbox（至少能 Upsert 后 list 本机看到）
    // 若三节点夹具过重：单测已覆盖 quorum 门闩；此处用双节点停掉 leader 后 CLI put channel 必须失败，
    // 同时对仍活 Owner `alert list` 不报错
}

func TestQ4_LeaderFailedNotSentByEveryFollower(t *testing.T) {
    // 三节点或双节点：停掉一个非入口业务节点
    // 在窗口内统计 WEBHOOK fake：只有 Leader 对应 inbox 有 NODE_FAILED
    // 断言：不是每个 follower 的 store 都有该 fingerprint
    // 允许 0 或 1 个发送者；禁止 >=2 个非 Leader 发送者
}

func TestQ4_FiveChannelsFakeServer(t *testing.T) {
    // httptest 收 WEBHOOK/WECOM/DINGTALK/SLACK；SMTP fake 收 EMAIL
    // Put 五条 enabled 通道 + 一条 WEB
    // 直接往该节点 store Upsert 后调用 Engine.Observe，或 CLI 无法注入事件时：
    // 在验收测试里打开该节点 SQLite，调 alert.Engine.Observe 一次 PROCESS_EXIT
    // 断言四个 HTTP handler 各至少 1 次、SMTP 1 次
}
```

若完整三节点 Leader 测试在 macOS 过慢，必须仍跑通「非 Leader ScanCluster 不写 NODE_FAILED」（可用 `internal/alert` 单测已有；验收测试至少双节点：入口 A list 聚合到 C 的 inbox，停 C 后 A 的 list 含 STALE，且不得打印成空 inbox only）。

**STALE 验收（必须有）：**

```go
func TestQ4_ListAlertsMarksUnreachableSTALE(t *testing.T) {
    addrA, _ := startClusterAgent(t, "")
    addrC, _, cancelC := startClusterAgentCtl(t, "")
    joinTwo(t, addrA, addrC)
    cancelC()
    out := mustCLI(t, addrA, "alert", "list")
    if !strings.Contains(strings.ToUpper(out), "STALE") {
        t.Fatalf("want STALE in aggregate list, got %s", out)
    }
}
```

更新 `docs/superpowers/plans/2026-08-16-v1.1.md`：Q4 行改为 **已完成** 并链到本文件。

- [x] **Step 1: Write the failing tests**

按上。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent -run TestQ4_ -count=1 -timeout 180s`

Expected: FAIL

- [x] **Step 3: 补齐接线缺口并 `make web`**

若扫描未挂到 run loop、或 CLI 未注册，在本任务修到验收绿。不要趁机做 Q5。

- [x] **Step 4: Run tests**

```bash
go test ./internal/alert ./internal/store ./internal/control ./internal/auth ./internal/api ./internal/cli ./internal/agent -count=1 -timeout 180s
cd web && npm test && npm run i18n:check
make web
```

Expected: PASS。`internal/alert` cover ≥ 80%。

- [ ] **Step 5: Commit**

```bash
git add internal/agent/q4_accept_test.go internal/web/dist docs/superpowers/plans
git commit -m "test(agent): Q4 alerts acceptance and mark plan complete"
```

---

## 规格覆盖自检

| 规格项 | 任务 |
|--------|------|
| AlertChannel / AlertPolicy Raft | 2 |
| secret 不回传、空 secret 保留 | 2, 8 |
| inbox 必写、无通道也可用 | 3 |
| fingerprint 表含 NODE_FAILED | 3 |
| 复用行 + dedup_window + notify_on_resolve | 3 |
| WEB/WEBHOOK/EMAIL/WECOM/DINGTALK/SLACK | 4 |
| HMAC / 钉钉加签 / 3 次退避 | 4 |
| 进程 EXIT/FATAL/CRASH_LOOP/HEALTH | 5 |
| 阈值连续分钟、缺口不插 0 | 5 |
| LOCAL_DB_ERROR | 5 |
| 仅 Leader 发集群四类 | 6 |
| voter CONTROL_NO_QUORUM | 6 |
| CERT 30d / VERSION {0,1} | 6 |
| ListAlerts 聚合 + STALE | 8, 11 |
| 失 quorum 不能 PutChannel | 2, 8, 11 |
| Owner 失 quorum 仍可写 inbox | 11 |
| 非每个 follower 外发 | 6, 11 |
| CLI | 9 |
| Alerts 页 + Overview Recent Alerts + P5 新鲜度 | 10 |
| `alert_send_total` | 8 |
| 不做 Exactly Once / Ack / Backup | 全局约束 |

## 与 P5 的关系

P5 已交付嵌入式 Vue、登录、Overview/Node/Process、LIVE/STALE/UNKNOWN、远程配置/日志。PRD §43 的 Recent Alerts 与 §89 Alerts 页在 P5 计划里被显式推迟。Q4 **补齐这两块**，并遵守 P5 视觉与新鲜度合同；不重做 P5 已交付页面。
