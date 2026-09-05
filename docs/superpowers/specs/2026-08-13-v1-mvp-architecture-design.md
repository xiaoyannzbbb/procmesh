# ProcMesh V1.0 总体架构设计

日期：2026-08-13  
状态：待用户审阅  
范围：V1.0 MVP（P0–P5）。不含 V1.1 / V1.2。

本文是实现合同，不是 PRD 摘抄。产品原则与验收场景以 `docs/v2-prd/v2-prd.md` 为准；本文锁定实现边界、组件划分、运行时布局、故障语义和测试门槛。

---

## 1. 背景与目标

ProcMesh 是 Local-First、Agent-Owned、Peer-Managed 的分布式进程管理平台。不部署独立中心管理 Server。每个 Agent 具备完整的 Process Manager、本地存储、Web/API、集群通信和（部分节点上的）Cluster Control。

V1.0 成功标准：

1. 单机可替代 Supervisor：启停、重启策略、健康检查、日志、配置 revision + CAS。
2. Agent 崩溃不带走业务进程；Host 重启后按 desired state 恢复。
3. 任意健康 Agent 可查看集群，并对 Owner 发起远程 Mutation。
4. 用户 / RBAC / 准入 / 证书吊销走内嵌 Raft，强一致。
5. 管理面或集群面故障不得主动停止已运行进程。
6. PRD §92 的 12 个故障场景可在 Linux CI 上脚本化验证（macOS 跳过 OS 专属项）。

非目标（V1.0 明确不做）：

- 跨节点调度、自动 Placement、故障迁移、跨节点依赖
- 容器编排、Service Mesh、通用服务发现
- 批量操作、Agent/Process Group、告警通道、配置备份、历史指标（V1.1）
- MFA、证书轮换管理、升级编排、大规模优化（V1.2）
- 任意命令执行默认开启（功能可编译进去，**默认 Disabled**）
- Windows

---

## 2. 已锁定约束

| 项 | 决定 |
|----|------|
| 交付形态 | 一份 V1.0 总体架构 spec，实现按 P0–P5 分阶段 |
| 平台 | Linux 为生产保证面；macOS 可编译、可跑单测和部分本地开发 |
| 语言 | 全部 Go |
| 二进制 | 三个独立二进制：`procmesh-agent`、`procmesh-shim`、`procmesh` |
| 技术栈 | 按 PRD §95：Protobuf + ConnectRPC、memberlist、hashicorp/raft + raft-boltdb/v2、bbolt、SQLite（modernc.org/sqlite）、Vue3 + Vite + TS + shadcn-vue + Tailwind v4、go:embed |
| 部署 | systemd + 裸二进制；macOS 手动或 launchd |
| Shim | Agent fork/exec 拉起；每 instance 一个 Unix socket |
| 建群 | `procmesh cluster init` 一次生成 `cluster_id`、Cluster CA、cluster secret、初始 admin，本机成为第一个 Control Member |
| 数据路径 | Hybrid：Local Authority + Gossip Summary + Direct RPC Mutation + Raft 控制面 |

macOS 降级（必须在 UI/日志里显式说明，禁止假装 Linux 语义）：

- 无 cgroup：`resource_limit` 忽略并告警
- `run_as_user` 不可用或不具备权限：拒绝启动并返回明确错误
- 无 systemd：Host reboot 恢复依赖用户如何拉起 Agent

---

## 3. 系统结构

三个逻辑 Plane，物理上打在同一个 Agent 进程里（shim 除外）。

```text
Browser / CLI
      │
      ▼
Any Agent :18680  (Web + ConnectRPC)
      │
      ├── Cluster Control (Raft :18685)     用户 / RBAC / 准入 / CRL
      ├── Gossip (memberlist :18689)        membership + summary
      └── Direct RPC mTLS (:18683)          写到 Owner
                │
                ▼
         Owner Agent
           Process Manager
                │  Unix socket
                ▼
         procmesh-shim ──► 业务进程
```

原则（不可妥协）：

1. Process 生命周期属于本机 Agent。
2. 只有 Owner 能改权威 Process 数据。
3. 任意 Agent 可提供只读集群视图。
4. 远程 Mutation 必须 Direct RPC 到 Owner，禁止「改本地副本再 Gossip」。
5. Gossip 只用于发现、故障检测、摘要。
6. 用户 / RBAC / 准入 / 吊销必须强一致。
7. Management / Cluster 故障不得主动影响 Process Plane。
8. Timeout / Unknown / Partial Success 是正常状态。
9. 所有远程 Mutation 带 `operation_id`，目标端幂等。
10. UI 必须区分 LIVE / STALE / UNKNOWN。

---

## 4. 组件与依赖

### 4.1 三个二进制

| 二进制 | 职责 | 依赖 |
|--------|------|------|
| `procmesh-agent` | 常驻。Process reconcile、shim 管理、SQLite、Web/API、Gossip、Agent RPC、可选 Raft | 本机 shim 二进制在 PATH 或与 Agent 同目录 |
| `procmesh-shim` | 每个 Process Instance 一个。fork/exec、信号、PID、exit、stdout/stderr | 不连集群、不读 Raft、不提供 Web |
| `procmesh` | 无状态 CLI。本机 unix/http 或 `--server host:18680` | 不常驻 |

Agent 停止（含 systemd stop）**不得**级联杀死 shim 和业务进程。升级路径：停 Agent → 换二进制 → 起 Agent → 重连已有 shim。

### 4.2 Agent 内部包

| 包 | 做什么 | 不做什么 |
|----|--------|----------|
| `internal/process` | Spec/Instance 状态机、desired/observed、restart、依赖 DAG、startup_priority、CAS | 不直接 fork，不走网络 |
| `internal/shim` | 发现 socket、启停 shim、重连、stdio 代理 | 不做集群决策 |
| `internal/store` | SQLite 原子提交：spec、runtime、revision history、operation journal、local audit | 不存日志正文、不存 Raft 控制面 |
| `internal/logmgr` | 文件日志、rotation、tail/stream、磁盘保护 | 不进 SQLite |
| `internal/health` | alive / http / tcp / exec 检查 | 不改 desired state；只上报，由 process 决定是否 restart |
| `internal/metrics` | Agent/Process 即时指标；Prometheus `/metrics` | V1.0 不做历史降采样库 |
| `internal/cluster` | memberlist、节点状态、summary 编解码、重复 node_id / 版本检查 | 不传日志、全量 config、明细 metrics |
| `internal/rpc` | Agent 间 ConnectRPC + mTLS 客户端/服务端 | 不信任入口 Agent 的 RBAC 结论，必须再验 |
| `internal/control` | Raft 节点或客户端、FSM、CRL、join token | 不存 Process runtime |
| `internal/auth` | 密码哈希、session、API token、登录限流、锁定 | 不自建用户库副本；读 Control 或其缓存 |
| `internal/api` | 对外 ConnectRPC（及少量 REST：healthz/readyz/metrics、静态 Web） | 写操作转 Owner 或本机 process |
| `internal/web` | `go:embed` 构建产物 | 无业务逻辑 |
| `internal/audit` | 本机 append-only 审计 | 不实时复制到全网 |

依赖方向：`api` → `auth` / `process` / `rpc` / `control`；`process` → `shim` / `store` / `health` / `logmgr`；`cluster` 与 `process` 只交换 summary DTO。禁止 `process` import `cluster` 或 `control`。

### 4.3 仓库布局

```text
procmesh/
  go.mod
  cmd/procmesh-agent/
  cmd/procmesh-shim/
  cmd/procmesh/
  proto/
  internal/{process,shim,store,logmgr,health,metrics,cluster,rpc,control,auth,api,web,audit}
  web/                  # Vue3 源码
  deployments/systemd/procmesh-agent.service
  docs/
```

单 Go module。Protobuf 是唯一 IDL，生成 Go 与 TS client。

---

## 5. 运行时布局

### 5.1 端口

| 用途 | 默认 | 协议 |
|------|------|------|
| Web + 外部 API | `:18680` | HTTP；生产建议 TLS 终结在反向代理或 Agent 配置证书 |
| Agent RPC | `:18683` | ConnectRPC + mTLS |
| Raft | `:18685` | Raft TCP |
| Gossip | `:18689` | memberlist |

均可在 `agent.yaml` 覆盖。

### 5.2 目录

Linux 生产：

```text
/etc/procmesh/agent.yaml
/var/lib/procmesh/
  node_id
  boot_id                  # 每次启动覆盖
  cluster/                 # CA、本机证书、私钥、CRL 缓存
  store.db                 # SQLite
  raft/                    # 仅 control member
  shim/<instance_id>.sock
  runtime/<instance_id>.json   # pid、shim pid、boot_id 快照
  logs/<process_id>/<instance_id>/stdout.log
  logs/<process_id>/<instance_id>/stderr.log
```

macOS 开发：`~/Library/Application Support/procmesh/` 替代 `/var/lib/procmesh/`；配置默认 `~/.config/procmesh/agent.yaml`。

### 5.3 systemd

- Unit：`procmesh-agent.service`
- `Type=simple`，`Restart=on-failure`
- `KillMode=process`（只杀 Agent 主进程，不杀 shim 树）
- 不把业务进程放进 Agent 的 cgroup 委托树，避免停 Agent 时被 systemd 收割

---

## 6. 数据模型与权威

### 6.1 权威表

| 数据 | 权威 | 一致性 | 载体 |
|------|------|--------|------|
| Process Spec / Desired / Runtime / Health / Logs / 明细 Metrics | Owner Agent | 本地强一致或本地实时 | SQLite + 文件 |
| Process / Agent / Config revision 摘要 | Owner 产出 | 最终一致 | Gossip |
| Membership 观察 | 各节点本地观察 | 最终一致 | memberlist |
| Users / Roles / Bindings / Join Token / Cert / CRL / 全局策略 | Cluster Control | 强一致 | Raft FSM + bbolt |
| 入口侧 Request Audit | 入口 Agent | 本地 | SQLite |
| 执行侧 Execution Audit | 目标 Agent | 本地 | SQLite |

### 6.2 Process 标识

- `process_id`：UUID，创建时生成，永不复用。
- `process_name`：Owner 上唯一，可改名但 id 不变。
- `instance_id`：`{process_id}:{ordinal}`，ordinal 从 0 到 `instances-1`。
- PID 不是身份。PID 必须与当前 `boot_id` 一起解释。

V1.0 `instances` 支持 ≥1。扩缩实例数：改 spec 生成新 revision；减少时按最大 ordinal 先停再删 instance 行。

### 6.3 配置 revision

- 每次成功写入 Spec 产生单调递增 `revision`（每 process 独立计数，从 1 起）。
- 同时维护 `latest_revision` 与 `active_revision`。
- 更新必须带 `expected_revision`；不匹配返回应用错误码 `CONFLICT`，Connect 状态用 `FailedPrecondition`，HTTP 映射 409。
- Rollback 把旧 revision 内容写成新 revision，不删除中间历史。
- 历史保留字段：revision、operator、timestamp、diff、comment。

### 6.4 本地 SQLite 逻辑库

分表，不把所有东西塞进一个 JSON blob：

- `process_specs`
- `process_instances`（runtime：pid、desired、observed、health、restart_count、active_revision、exit_*）
- `config_revisions`
- `operation_journal`（主键 `operation_id`）
- `audit_events`
- `local_meta`（schema_version、node_id、cluster_id）

关键写入用单事务。SQLite 损坏检测：打开失败或 `PRAGMA integrity_check` 失败 → Agent `DEGRADED`，不杀进程，打 Critical 级本地事件（V1.0 无外部告警通道，写入 audit + 日志 + `/readyz` 降级）。

---

## 7. Process 状态机

Desired：`RUNNING` | `STOPPED`。

Observed：

```text
STOPPED ─start→ STARTING ─ok→ RUNNING ─exit→ EXITED
                    │                         │
                    └─fail→ BACKOFF ←─────────┘
                               │ retry
                               └─retries exhausted→ FATAL
RUNNING ─stop→ STOPPING ─grace/SIGKILL→ STOPPED
任意无法判定→ UNKNOWN
```

Restart policy：`never` | `always` | `on-failure`。Backoff：initial / max / multiplier。`max_retries` 在 `retry_window` 内耗尽进入 FATAL。管理员可 `reset failure state` 后再次 start。

Stop：`stop_signal`（默认 SIGTERM）→ `stop_timeout` → `kill_signal`（默认 SIGKILL）。

Health 与 Observed 分离：`HEALTHY` | `UNHEALTHY` | `UNKNOWN`。允许 `RUNNING/UNHEALTHY`。`restart_on_failure` 受 `restart_cooldown` 限制。

依赖：仅本机。条件 `STARTED` 或 `HEALTHY`（默认 HEALTHY）。保存时检测环，拒绝成环配置。Dependency 优先于 `startup_priority`（数值越小越先）。

Host reboot：读 desired + autostart，按 DAG + priority 恢复。`desired=STOPPED` 即使 autostart=true 也不拉起。

---

## 8. Shim 协议

路径：`$data_dir/shim/<instance_id>.sock`。

传输：4 字节大端长度 + protobuf 帧。命令：`Start`、`Stop`、`Signal`、`Status`、`Wait`、`AttachStdio`。

`Start` 携带 command/args/env/cwd/user 以及 stdout/stderr 文件路径。Shim 只往这些文件追加；rotation / retention / 磁盘保护由 Agent `logmgr` 负责（改文件或发信号让 shim reopen）。

生命周期：

1. Agent 为 instance 创建 socket 路径，exec `procmesh-shim --socket ... --instance-id ...`。
2. Shim 启动后立刻 `setsid()`，脱离 Agent 进程组，避免 Agent 收到 SIGHUP/SIGTERM 时误伤业务。
3. Shim 监听 socket，按 `Start` fork/exec 业务进程，stdio 写入给定文件。
4. Agent 崩溃：shim 与业务继续。
5. Agent 恢复：按 socket 文件重连 `Status`；匹配则接管。
6. Shim 死、业务仍在：Agent **不**再拉第二个同名进程。标 `UNKNOWN`，写 audit。V1.0 提供显式 `ProcessService.Adopt`：操作员确认 PID 后绑定到该 instance 并拉起新 shim attach；没有自动 adopt。
7. 无 shim 无进程：按 desired/autostart 正常拉起。

Shim 不实现重启策略、健康检查、RBAC。那些都在 Agent。

---

## 9. 集群

### 9.1 身份

首次启动生成并持久化 `node_id`（UUID）。禁止用 IP / hostname / MAC 当 ID。每次启动新 `boot_id`。

证书 SAN/扩展必须含 `cluster_id` 与 `node_id`。RPC 校验：集群匹配、证书有效、未在 CRL。

### 9.2 节点状态

`JOINING` | `ALIVE` | `SUSPECT` | `FAILED` | `LEFT` | `REMOVED` | `REVOKED`

`FAILED ≠ REMOVED`。FAILED 只表示当前不可通信。

### 9.3 加入与踢出

`cluster init`：

- 生成 `cluster_id`、Cluster CA（自签）、cluster secret、初始 Super Admin
- 为本机签 Agent 证书
- 本机 bootstrap 为唯一 Raft voter

cluster secret：随机 32 字节，只存 control member 的 `cluster/`（0600）。用途仅限紧急恢复文档/离线运维，**不**作为日常 RPC 凭证。日常认证只有 mTLS。

`procmesh node token create`：一次性 join token（随机，Raft 只存哈希 + TTL + 剩余次数 + 作废标记）。请求可进入任意 Agent；入口校验 `node.manage` 与 `operation_id` 后通过内部 mTLS 转发到当前 Raft Leader，Leader 重建身份并再次校验权限。Token 创建不提供幂等重放；若返回 `TIMEOUT`，结果可能已经提交，重试会使用新的 `operation_id` 并可能留下额外的有效 Token。

Join：向任一 ALIVE Agent 提交 token → 该 Agent 把签发请求交给 Raft leader → leader 校验 token 并消费次数 → 用 Cluster CA 签发证书 → 写入 membership 授权 → 返回证书与 CA。重复 `node_id`：拒绝后加入者，错误码 `DUPLICATE_NODE_ID`。

普通 joiner/nonvoter 不持有 CA 私钥。`node promote` 先完成 CA-only Admission Capability 配置，再扩充 Raft voter：Leader 写入公开的 `PREPARED` 状态（operation ID、目标节点与证书序列号、Leader term、epoch、随机 nonce、过期时间），通过 `:18683` 内部 mTLS endpoint 传输 `ca.key`；发送前必须完成 quorum-backed Leader 校验，传输 context 与该 Leader term 绑定，失去领导权立即取消。目标确认调用者是当前 Leader、自身仍为 ADMITTED 且证书未入 CRL、并且 PREPARED 已 apply 后，以 `0600` 临时文件、file fsync、rename 和 directory fsync 原子安装，并用 CA 私钥签署 challenge；context 在 rename 前取消时只清理临时文件，不得持久化密钥。Leader 用 `ca.crt` 验证 proof、写入 `READY` 后才调用 `AddVoter`。READY 与 `AddVoter` 前必须重新验证目标仍为 ADMITTED、证书序列号未变且不在 CRL。Leader 串行化 `node remove` 与 `node promote`；`node remove` 作废该节点 in-flight 的 PREPARED。已存在的相同 key 视为幂等成功，不同 key 禁止覆盖；任何安装、proof 或 READY 失败都保持 nonvoter。READY 后扩 voter 失败可用同一 operation ID 重试，但每次重试必须写入新的 term、nonce 与过期时间并重新验证 proof，不能仅依赖历史 READY。

每个 voter 都必须持有与 FSM 指纹一致的 CA 私钥且自身 capability 状态为 READY，否则 `/readyz` 为 degraded，Token 创建和 Join 签发 fail closed；该降级不停止或阻断本地业务进程。旧集群可由持有匹配 `ca.key` 的 Leader 初始化 capability 状态；已有 keyless voter 需在该 Leader 任期内逐个重新执行 `node promote` 补配。滚动升级期间旧 Agent 因不支持 capability endpoint 而晋升失败，不得改变 voter 集合。移除 voter 不能抹除其离线 CA 副本，彻底撤销签发能力需要轮换 Cluster CA。

`procmesh node remove`：membership 删除 **并且** 证书吊销。仅 Gossip LEFT 不算安全删除。被吊销节点再连：拒绝。

### 9.4 Gossip 载荷

允许：node_id、地址、版本、protocol_version、labels、资源摘要（CPU/内存/磁盘百分比）、每个 process 的摘要（name、desired、observed、health、latest/active revision、freshness 时间戳）。

禁止：日志、全量 spec、明细 metrics、stdio、大审计记录。

协议不兼容：标 `INCOMPATIBLE_VERSION`，不互操作 Mutation。V1.0 `protocol_version = 1`。

### 9.5 远程 Mutation

```text
Client → 入口 Agent :18680
       → 入口 RBAC
       → mTLS RPC → Owner :18683
       → Owner 再验 RBAC + 证书
       → operation_id 去重
       → 本地 commit + 执行
       → 返回结果
```

入口不改权威副本。Owner 不信任入口的「已授权」声明。入口只转发 `user_id` + `session_id`（或 API `token_id`）；Owner 用 Control 数据（或 TTL 内缓存）自行查 session/token 是否有效，再查 RBAC。session 与 API token 元数据（哈希、过期、吊销）存在 Raft，这样任意 Owner 都能独立验票。

---

## 10. Cluster Control

- 实现：hashicorp/raft。日志 raft-boltdb/v2，FSM 快照在 bbolt。
- 默认 3 voter，可配 5。只有显式添加的节点是 voter。
- 非 voter Agent 用客户端读（follower 读本地 apply 后的缓存；写发给 leader）。
- FSM 只存：cluster_id、membership 授权、证书与 CRL、users、roles、permissions、bindings、cluster/security policy、join tokens、Admission Capability 的 CA SPKI SHA-256/epoch/PREPARED/READY 公开状态、session 与 API token 元数据。CA 私钥绝不进入 Raft 日志、快照、审计或普通 API。V1.0 **不**存 agent_groups、alert_channels（那些是 V1.1）。
- 失 quorum：Process Plane 全开；本地 Process 读写开；远程 Mutation 受 RBAC 缓存 TTL 约束（默认 5 分钟）；`user.*` / `role.*` / `node.remove` / 准入 / 安全策略写全部拒绝。
- 已登录用户失 quorum 时仍可：`process.read`、status、logs.read。

V1.0 RBAC 范围只支持 **Cluster** 与 **Agent**。Agent Group / Process Group 留到 V1.1（PRD §18 与 §89 冲突，以版本切分为准）。

内置角色：Super Admin、Cluster Admin、Operator、Viewer。权限名按 PRD §16。`command.execute*` 默认不授予除 Super Admin 外的任何人，且功能开关默认关。

认证：Username+Password（argon2id，密码最短 10）与 API Token（只显示一次，库内只存哈希）。Session：HttpOnly + SameSite=Lax + TTL 默认 12h，记录在 Raft。登录限流（每账号每分钟 5 次）与失败锁定（连续 10 次锁 15 分钟）。不实现 MFA/OIDC/LDAP。

---

## 11. API、CLI、Web

### 11.1 API

HTTP 服务器用 Gin，挂 ConnectRPC handler。对外主协议：ConnectRPC（JSON 与二进制均可），监听 `:18680`。少量 REST：

- `GET /healthz` — 进程活着即 200
- `GET /readyz` — 本地 store 可用才 200；DEGRADED 返回 503 但 **不** 表示业务进程有问题
- `GET /metrics` — Prometheus
- `GET /` — 嵌入式 Web

服务划分（proto package `procmesh.v1`）：

- `AuthService`：Login / Logout / Token
- `UserService` / `RoleService`
- `NodeService`：List / Get / Token / Remove
- `ProcessService`：CRUD、Start/Stop/Restart/Kill、ResetFailure、Adopt
- `ConfigService`：Get / Update(expected_revision) / History / Diff / Rollback
- `LogService`：Tail / Stream / Download
- `MetricsService`：Agent 与 Process 即时快照
- `ClusterService`：Overview、Control 健康、证书到期
- `AuditService`：查询本机 + 按需 RPC 聚合（不要求全量复制）

所有 Mutation 请求头/字段必填 `operation_id`（UUID）。

### 11.2 CLI

本地默认连 `127.0.0.1:18680`（可用 `--server` 覆盖）。CLI 若未传 `operation_id`，自己生成 UUID。不另开第四个管理 Unix socket。

最小命令集：

```text
procmesh cluster init
procmesh agent join --server ... --token ...
procmesh status
procmesh node list | status | token create | remove
procmesh process list | get | start | stop | restart | logs
procmesh process apply --file spec.yaml --expected-revision N
procmesh user list | create | disable
procmesh role list | create | grant
```

远程加 `--server` / `--node`。

### 11.3 Web

Vue3 管理后台，视觉接近 ChatGPT Web：浅色中性底、左侧导航、主内容区。嵌入 Agent，任意节点打开都能看集群。

V1.0 页面：

- 登录
- Cluster Overview（节点/进程计数、资源摘要、Control/Gossip/RPC 自身健康、版本分布、证书到期）
- Node 详情
- Process 列表与详情（desired/observed/health、revision、启停、配置编辑、历史 diff、日志、即时指标）
- Users / Roles
- Audit（聚合视图，缺失节点标 STALE）

所有来自 Gossip 的字段显示 `last_updated_at` 与新鲜度徽章：LIVE / STALE / UNKNOWN。禁止把 STALE 画成绿色「正常」。

---

## 12. 错误处理

统一错误码：`OK`、`CONFLICT`、`UNAVAILABLE`、`TIMEOUT`、`DENIED`、`DEGRADED`、`DUPLICATE_NODE_ID`、`INCOMPATIBLE_VERSION`、`NOT_FOUND`、`INVALID`。

远程超时：客户端标 `TIMEOUT`（结果未知），不是失败也不是成功。重试必须复用 `operation_id`。

磁盘保护：

- \>85% 告警（本地日志 + audit；V1.0 无外部 channel）
- \>90% 积极删旧日志
- \>95% 停止新日志与 metrics 写入，保住 config / journal / audit / store / raft

V1.0 阈值与开关见 `agent.yaml` 的 `disk`（`docs/superpowers/specs/2026-08-14-disk-protect-config-design.md`）。默认 85/90/95、`auto_delete: false`、`emergency_stop_writes: true`。

Local DB 损坏：DEGRADED，停高风险写，不杀业务进程。

网络分区：两侧本地 Process 继续；跨区操作 TIMEOUT/UNAVAILABLE；**禁止**因对端 FAILED 而在本机创建对方的进程。愈合后只收敛 membership 与 summary，不得用旧缓存覆盖 Owner 权威数据。

---

## 13. 阶段交付（实现顺序，仍属同一 V1.0）

同一份 spec，代码按阶段合并，每阶段可独立验收。

| 阶段 | 内容 | 可演示出口 |
|------|------|------------|
| P0 | store、状态机、shim、restart、health、文件日志、本地 audit、CAS | 无集群时本机管进程 |
| P1 | ConnectRPC + CLI 管本机 | `procmesh start/stop/logs` |
| P2 | node_id、cluster init、join token、memberlist、证书骨架 | `node list` |
| P3 | mTLS RPC、Write-to-Owner、operation_id | 从 A 重启 C 上的进程 |
| P4 | Raft、用户、RBAC、准入、CRL、remove | 登录与权限生效 |
| P5 | Vue Web 嵌入、集群视图、新鲜度、远程配置/日志 UI | 浏览器管集群 |

P4 完成前：尚未 `cluster init` 的 Agent 允许本机环回无认证管理（只绑 `127.0.0.1`），方便 P0–P3 自测。`cluster init` 成功后该模式自动关闭，必须走用户会话。禁止在已入群节点上保留无认证入口。

---

## 14. 测试

强制 TDD：先红后绿。包级覆盖率：

| 包 | 最低覆盖率 |
|----|------------|
| `internal/process` | 80% |
| `internal/shim` | 80% |
| `internal/store` | 80% |
| `internal/control` | 80% |
| `internal/auth` | 80% |
| 其余 internal | 不设硬门槛，关键路径必须有测试 |
| `web/` | 关键组件 Vitest；登录/列表/新鲜度/409 Playwright |

集成：

- 本机子进程跑 shim
- 临时 SQLite 测 CAS 与 journal 幂等
- 3 节点内存 Raft 测失 quorum 拒绝写
- memberlist 测重复 node_id
- API：幂等 restart、409、DENIED、Owner down → UNAVAILABLE

故障验收（PRD §92）映射：

| Case | 阶段 | CI |
|------|------|-----|
| 1 Web Agent crash | P5 | Linux |
| 2 网络断开不迁移 | P3 | Linux |
| 3 Agent crash，shim 续命 | P0 | Linux + macOS |
| 4 Host reboot | P0 | Linux（systemd） |
| 5 并发配置 409 | P0 | 全平台 |
| 6 丢响应后同 operation_id 不重放 | P3 | 全平台 |
| 7 批量部分成功 | **不做**（V1.1） | — |
| 8 Remove 后再连拒绝 | P4 | Linux |
| 9 Control 失 quorum | P4 | Linux |
| 10 磁盘 95% | P0 | Linux |
| 11 Local DB corruption | P0 | 全平台 |
| 12 重复 node_id | P2 | 全平台 |

macOS CI：单测 + 不依赖 cgroup/setuid/systemd 的集成。Windows：不测。

---

## 15. 观测

Agent 暴露 PRD §82 所列核心指标中 V1.0 需要的部分：uptime、members 计数、rpc 计数/延迟、process running/restart/crashloop、healthcheck、log bytes、store 大小与写延迟、operation queue、raft quorum。

`/readyz` 与业务进程健康解耦：Agent DEGRADED 时 readyz 失败，Dashboard 必须同时展示「ProcMesh 自身」与「业务 Process」，避免把管理面故障看成业务故障。

---

## 16. 安全

- Agent RPC 必须 mTLS。证书含 cluster_id、node_id。
- 密码 argon2id。Token 只存哈希。
- Web：Secure Session、CSRF（cookie session 时）、SameSite、登录限流、Session TTL。
- 生产配置缺 TLS 时启动告警，开发模式允许明文 :18680。
- 无硬编码密钥。Cluster CA 私钥只在 control member 的 `cluster/` 目录，权限 0600。
- 目标 Agent 再验 RBAC。Viewer/Operator 默认无 `command.execute`。

---

## 17. 与 PRD 的刻意差异

1. **RBAC Scope**：V1.0 仅 Cluster + Agent，Group 放到 V1.1。避免未做分组模型却承诺分组授权。
2. **告警通道 / 批量 / 备份 / 历史指标**：不进 V1.0。磁盘与 DB 事件只写本地 audit + 日志。
3. **Case 7** 不作为 V1.0 验收门禁。
4. **macOS**：开发辅助面，不是对等生产面。
5. **Web 与 Agent RPC 都用 ConnectRPC**，不用第二套 REST 资源模型（healthz/readyz/metrics 除外）。

---

## 18. 实施时禁止事项

- 在非 Owner 上修改 Process 权威数据
- 用 Gossip 传递 Mutation
- Agent stop/crash 时主动杀掉仍在跑的业务进程
- 因对端 FAILED 而在本机创建对方的 Process
- 把 STALE 数据显示成实时健康
- 无 `operation_id` 的远程写
- 无 `expected_revision` 的配置写
- 把 Process runtime / logs 写入 Raft
