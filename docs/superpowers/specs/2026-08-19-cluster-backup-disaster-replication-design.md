# ProcMesh 集群备份与灾备副本设计

日期：2026-08-19
修订：2026-08-28（灾备自行捕获增量：副本 run 自行捕获源快照后再按路由复制，不再依赖主备份 run / trigger）
状态：待用户审阅
范围：在现有 Q5 配置备份能力之上的集群级备份、定时备份与 Peer 灾备副本

本文是实现合同，不是 PRD 摘抄。实现前必须先据本文生成独立 implementation plan，并在每个阶段完成验证。本文不改写现有 Owner、Raft、Gossip、mTLS 和 Restore CAS 约束；若与早期 V1.1 文档中“整集群灾备工具暂不做”的非目标冲突，以本文作为本功能的后续增量边界，实施时同步修正文档交叉引用。

## 1. 背景

当前 Web 创建备份时，请求被发送到当前连接的 Agent，后端直接调用该 Agent 的本地 `backup.Engine`。因此现有备份是单 Agent 备份：空的 `processIds` 代表该 Agent 的全部进程，不代表整个集群。现有定时任务同样是每个 Agent 本地轮询，且只支持本地 FS 快照。

现有 Q5 已具备 FS、S3 Compatible 和 Peer 三种 sink，但 Peer 只是一次性把当前 Agent 的快照发给指定目标，接收方只落盘，不形成集群级编排，也没有副本拓扑、定时策略、失败聚合或 Leader 故障转移语义。

本设计引入两个独立概念：

1. **集群备份（Cluster Backup）**：一次逻辑备份运行，扇出到目标 Agent；每个 Agent 备份自己的本地 process spec 与 revision history。
2. **灾备副本（Disaster Replication）**：把已生成的 Agent 快照复制到其它 Agent，Peer 是复制目标而不是普通备份 sink。

这两个概念共享调度和状态聚合基础设施，但在策略、页面、权限和恢复语义上分开。

## 2. 目标

1. Web 可以手动创建整个集群的逻辑备份，并按 Agent 查看成功、失败、超时和不可达结果。
2. Web 可以配置集群级定时备份，策略由当前 Raft Leader 调度，Leader 变化不会产生重复逻辑运行。
3. 集群 FS 备份采用“每个 Agent 写自己的本地目录”，不要求共享文件系统。
4. 集群 S3 备份使用每个 Agent 的本地 S3 配置上传自己的快照，密钥不进入 Raft 或 API 响应。
5. Web 提供独立的“灾备副本”页面，支持一键生成整个集群的合理 Peer 副本拓扑。
6. Peer 复制具备 checksum 校验、幂等、逐 Agent 重试、保留策略和健康状态。
7. 备份和副本载荷始终留在本地文件系统或对象存储，不写入 Raft、Gossip 或 Leader 内存中的大对象。
8. 恢复仍然只能在目标进程 Owner 上通过 `ApplySpec + expected_revision` 产生新 revision；Peer 文件绝不直接 apply。

## 3. 非目标

- 进程运行时、PID、日志、metrics、audit、Raft 状态、证书私钥和业务数据不进入配置备份。
- 不做跨 Agent 的事务性同时快照；同一集群运行内各 Agent 的捕获时间可以不同。
- 不做自动故障迁移、自动 Placement 或自动重建丢失的 Agent。
- 不把备份载荷或 Peer 副本写入 Raft、Gossip 或中心管理 Server。
- 不要求所有 Agent 使用同一份 S3 凭据；每个 Agent 可使用本地 profile，但必须在策略校验时检查配置完整性。
- 不把 Peer 复制配置和 FS/S3 主备份配置混在同一个 Web 下拉框中。

## 4. 核心语义

### 4.1 一次集群备份是一个逻辑运行

```text
ClusterBackupRun run-123
  ├── Agent A -> Snapshot A
  ├── Agent B -> Snapshot B
  └── Agent C -> Snapshot C
```

集群运行使用一个稳定的 `run_id`，每个 Agent 仍拥有自己的 `snapshot_id`。目标集合在运行开始时冻结，通常是 `ADMITTED` 的 Agent；运行开始时不可达的节点记录为 `UNAVAILABLE`，不会被静默排除。

该运行表示“同一策略下的一组逻辑快照”，不是跨节点原子快照。Web 必须展示此限制。

### 4.2 FS 的边界

接受每个 Agent 写自己的本地 FS 目录：

```text
{agent_fs_dir}/{cluster_id}/{node_id}/{snapshot_id}.json
```

这可以覆盖整个集群的配置逻辑，但不能防护 Agent 主机磁盘损坏或主机丢失。需要主机级灾备时，必须使用 S3 或 Peer；Web 在选择集群 FS 时明确显示该提示。

### 4.3 Peer 是复制，不是主备份 sink

推荐流程：

```text
ClusterBackupRun
  -> Agent 生成本地快照
  -> 写入 FS 或 S3 主目的地
  -> 异步复制同一快照到 Peer
  -> Peer 校验并原子落盘
```

现有 `sink=peer` 接口保留兼容，但 Web 的主备份流程不再把 Peer 作为普通目的地。Peer 页面只管理复制策略、路由、健康、验证和恢复入口。

## 5. 系统结构

```text
Browser / CLI
      |
      v
Any Agent :18680
      |
      +-- Cluster Control / Raft
      |     backup policies
      |     replication policies
      |     fire ledger / run metadata
      |
      +-- Cluster Backup Coordinator
      |     Leader-only scheduler
      |     target expansion
      |     task dispatch / aggregation
      |
      +-- Direct mTLS RPC
      |     Agent -> Agent snapshot transfer
      |     Agent -> Leader task status
      |
      +-- Local SQLite + Files
            backup_index (本机与已接收 Peer 的元数据)
            staged artifacts / FS snapshots / Peer snapshots
```

Leader 只负责控制和小型元数据，不转发快照字节。源 Agent 直接把快照传给目标 Peer；主备份上传也由源 Agent 自己完成。

### 5.1 数据权威

| 数据 | 权威 | 载体 |
|------|------|------|
| `BackupPolicy` | Raft Leader/FSM | Raft |
| `ReplicationPolicy` | Raft Leader/FSM | Raft |
| scheduled fire ledger | Raft Leader/FSM | Raft，小型、可保留窗口 |
| 集群运行/任务状态 | Raft 元数据 | Raft，仅 ID、状态、时间、计数、错误摘要 |
| Snapshot payload | 创建快照的 Agent / sink | FS、S3 或 Peer 文件 |
| `backup_index` | 所在 Agent | 本地 SQLite |
| 进程 spec 与 revision history | 进程 Owner | Owner SQLite |

Raft 禁止保存 snapshot bytes、完整 spec payload、S3 access key/secret key、backup index 路径详情或任意可恢复业务数据。Gossip 禁止携带上述数据和任务进度。

## 6. 集群备份策略

### 6.1 模型

```text
BackupPolicy
  policy_id              UUID
  name                   集群内唯一
  enabled                bool
  schedule_cron          5 字段 cron；空表示仅手动
  timezone               IANA timezone，创建时固化
  target_selector        ALL_ADMITTED | AGENT_GROUP | EXPLICIT_NODES
  target_ids             group_id 或 node_id 列表
  sink                   FS | S3
  destination_profile    逻辑 profile 名；不含凭据
  retention              keep_last / keep_days / max_bytes
  timeout_seconds        单 Agent 超时
  max_concurrency        集群运行最大并发 Agent 数
  unavailable_policy     RECORD_AND_CONTINUE | FAIL_FAST
  created_at / updated_at
  revision
```

`timezone` 必须是有效 IANA 名称。Web 默认使用浏览器时区填充，但保存后以策略中的值为准，不随 Leader 或浏览器变化。cron 初期沿用现有 5 字段语法和解析约束，使用共享校验器，避免 Web 与 Agent 语义分叉。

### 6.2 目标集合

- `ALL_ADMITTED`：运行启动时取所有已准入且未撤销的 Agent。节点是否当前 `ALIVE` 只影响运行结果，不改变已冻结目标集合。
- `AGENT_GROUP`：运行启动时读取 Raft 中的显式 Agent Group 成员。
- `EXPLICIT_NODES`：策略保存显式 node ID 列表；未知或已撤销节点拒绝保存。

目标集合冻结后，策略修改、新增节点或节点短暂离线都不改变正在运行的目标。

### 6.3 S3 profile 校验

Raft 只保存 `destination_profile`。每个 Agent 的本地配置提供 profile 的 endpoint、bucket、prefix、region、access key 和 secret key。创建或启用 S3 策略时，Leader 对目标 Agent 做能力检查：

- profile 缺失：该 Agent 标记 `CONFIG_MISSING`，策略可以保存但不能启用，或由用户选择允许运行时部分失败。
- endpoint/bucket 不可达：保存不阻塞，但健康检查显示 `UNAVAILABLE`。
- API 和 Raft 永远不回显密钥，只回显 profile 名、endpoint 主机和配置状态。

## 7. 调度、幂等和 Leader 故障转移

### 7.1 Leader-only 调度

只有当前 Raft Leader 执行策略轮询和创建 scheduled run。Follower 不创建本地定时任务；Leader 变化后，新 Leader 从 Raft policy 和 fire ledger 继续调度。

每次调度使用：

```text
fire_key = policy_id + scheduled_fire_unix
run_id   = stable UUID allocated by the fire-ledger command
```

fire ledger 只记录策略 ID、计划触发时间、run ID、状态、Leader term、租约时间和完成时间，不记录载荷。

### 7.2 运行恢复

Leader 失效时：

1. 新 Leader 读取 `RUNNING` 且 lease 已过期的运行。
2. 复用原 `run_id` 和每个 Agent 的稳定 `task_id`，不创建第二个逻辑运行。
3. 对已成功且有 checksum 的任务不重复上传；对 `PENDING`、`TIMEOUT`、`UNAVAILABLE` 和无确认结果的任务按策略重试。
4. 源 Agent 的本地 Engine 以 `run_id + node_id` 做幂等检查，重复调度返回已有结果。

如果旧 Leader 恢复并继续执行，其状态写入会因 lease/term 不匹配被拒绝或降级为过期报告，不得覆盖新 Leader 的终态。

### 7.3 运行状态

集群运行：`PENDING`、`RUNNING`、`SUCCEEDED`、`PARTIAL`、`FAILED`、`CANCELED`。单 Agent 任务：`PENDING`、`RUNNING`、`SUCCEEDED`、`FAILED`、`TIMEOUT`、`UNAVAILABLE`、`CONFIG_MISSING`、`SKIPPED`。

`PARTIAL` 是一等终态：只要至少一个 Agent 成功且至少一个 Agent 非成功，运行就是 `PARTIAL`。不可达节点必须显示为 `UNAVAILABLE`，不能伪装成空备份列表。

## 8. Agent 快照流水线

每个 Agent 执行以下步骤：

1. 校验当前集群身份、策略 revision、task lease 和本地 sink 配置。
2. 读取本机 Owner 的全部 process spec 与 `config_revisions` 历史；空进程列表仍按现有 Engine 规则处理并返回明确结果。
3. 生成包含 `cluster_id`、`node_id`、`run_id`、`snapshot_id`、格式版本、创建时间、process IDs、revision 范围和 checksum 的快照。
4. 以临时文件或流方式写入 FS/S3；FS 使用临时文件 + `fsync` + 原子 rename，文件权限 `0600`，目录权限 `0750`。
5. 将本地 backup index 写入成功或失败元数据；index 仍只在本机保存。
6. 若存在启用的 ReplicationPolicy，向路由目标发起异步 Peer 任务。
7. 上报只含元数据的任务结果：snapshot ID、bytes、checksum、时间、状态和错误摘要。

对 S3，快照可以先写本地 staging，再上传和复制；staging 在主 sink 成功且所有 Peer 任务进入终态后按生命周期清理。若 staging 已清理而 Peer 任务后续重试，源 Agent 必须从主目的地重新取回同一 checksum 的快照，不能重新读取当前进程 spec 生成可能不同的内容。staging 不进入 API、Raft 或 backup index 的载荷字段。

## 9. FS 与 S3

### 9.1 FS

集群 FS 的默认布局：

```text
{fs_dir}/{cluster_id}/{node_id}/{snapshot_id}.json
```

`fs_dir` 仍由每个 Agent 本地配置提供。`snapshot_id` 在集群内全局唯一，因此不同策略不会产生文件名冲突。现有旧格式 `{fs_dir}/{snapshot_id}.json` 继续可读，不强制迁移；新集群运行使用新布局。

FS 保留策略由每个 Agent 执行本地删除，并将结果汇总到集群运行。单节点磁盘压力、权限错误和删除失败都必须单独显示。

### 9.2 S3

建议对象 key：

```text
{prefix}/{cluster_id}/{policy_id}/{node_id}/{snapshot_id}.json
```

S3 上传、列举、删除、checksum 和远端生命周期由 Agent 本地 profile 完成。策略的 `retention` 是意图，具体删除由 sink 执行；如果 bucket 启用生命周期规则，Web 显示为外部保留策略，不把它误报为 Agent 已删除。

S3 失败包括凭据缺失、签名错误、权限拒绝、网络超时、bucket 不存在和远端 checksum 不一致，分别保留错误码和安全的错误摘要，不回显 secret。

## 10. 灾备副本（Peer Replication）

### 10.1 模型

```text
ReplicationPolicy
  policy_id
  name
  enabled
  source_selector       ALL_ADMITTED | AGENT_GROUP | EXPLICIT_NODES
  source_ids
  replica_factor        1 .. N-1
  routes[]              source_node_id -> target_node_ids[]
  schedule_cron         5 字段 cron；空表示仅手动
  timezone
  schedule_epoch_unix   自动调度只在 fire > 此值后触发；不补跑策略写入前已过的 cron
  retention             keep_last / keep_days / max_bytes
  max_concurrency
  verify_after_copy     bool
  bandwidth_limit       可选
  topology_constraints  anti-affinity 规则
  revision
```

产品面不再暴露 `trigger` / `primary_policy_ids`。兼容字段若仍出现在 Proto 中，服务端写入时清空，且不得再作为捕获前置。

`ReplicationRoute` 是生成后的稳定结果。运行任务使用：

```text
ReplicationTaskKey = replication_policy_id + source_snapshot_id + target_node_id
```

Peer 目录：

```text
{data_dir}/backup/peer/{source_node_id}/{cluster_id}/{snapshot_id}.json
```

目标 Agent 只做接收、临时写入、checksum 校验和原子 rename，不把副本写入 process store，也不自动 adopt 为本地 Owner spec。

### 10.2 一键生成集群副本配置

Web 的“生成整个集群副本配置”调用 preview API，生成一个未应用的 draft。生成规则：

1. 候选节点为所有 `ADMITTED` 且未撤销的 Agent；短暂离线节点保留在配置中并显示健康警告。
2. 排除 source 与自身相同的 target。
3. 按稳定 node ID 排序，默认用环形分配；每个 source 选择环上后续 `replica_factor` 个节点，保证结果确定且不会形成全网状复制。
4. 如果存在 Agent 的 host/rack/zone 故障域标签，优先选择不同故障域；缺失标签时至少保证不同 node ID，并显示“无法确认物理故障域”的警告。
5. 在满足故障域约束的候选中，按目标的当前 inbound route 数、容量权重和 node ID 进行稳定的负载均衡。
6. `replica_factor` 默认值为 1；N=2 时生成互相复制；N=1 时不生成有效路由并返回明确警告。
7. N 个节点且副本数为 `N-1` 时允许全网状结果，但 Web 必须展示预计网络、磁盘和并发开销。

Preview 返回完整 route 表、故障域信息、每个目标的预计 inbound load、缺失 Peer 能力、同机风险和预计副本数。用户确认后才把 policy 和 route 写入 Raft。策略 revision 变更不会删除旧副本；旧副本由 retention 管理。

### 10.3 调度、捕获与恢复

自动：`enabled` 且 `schedule_cron` 非空时，仅当前 Raft Leader 在 `fire > ScheduleEpochUnix` 时创建 replication run；不补跑策略写入前已过的 cron。`enabled=false` 跳过 cron，手动仍允许。

每个源节点先捕获本地 process spec 与 revision history，以 `sink=replica` 落盘并产生稳定 `snapshot_id`+checksum，再按路由经 mTLS 复制到 Peer。不读取 `ClusterBackupRun`，也不写主备份 `BackupRuns`。Peer 只校验并落盘，不 apply。

手动：`StartRun` 走同一捕获+复制流水线，不要求 `primary_run_id` / 既有主备份快照引用。应用拓扑只写策略，不立刻创建 run。

同策略同时最多一个 `RUNNING`；若 cron fire 撞上运行中，该 fire 记为 `SKIPPED`，不排队。失败 route 继续推进，run 可为 `PARTIAL`。

重试只处理失败任务：已有冻结快照则只重传；快照为空或源文件丢失则对该源重捕获。成功任务不动。目标节点已存在相同 snapshot ID 和 checksum 时直接返回幂等成功；ID 相同但 checksum 不同视为冲突并阻止覆盖。

恢复页面可以列出 Peer 副本，并跳转到 Owner 选择和 CAS 恢复流程。Peer 目标本身不是新的 Owner，不能在目标节点直接启动源节点的进程。

### 10.4 Draft 一致性与 source selector

`GeneratePolicyDraft` 必须在当前 Raft Leader 上执行。Follower 收到请求时使用现有内部 mTLS forwarding 转发到 Leader；preview 本身不写 Raft。`ApplyPolicyDraft` 在同一个 Leader 权威边界重新解析拓扑并验证 draft。

Draft 中的节点集合分为两个不同概念：

- source 集合由 `source_selector/source_ids` 从当前 Raft state 解析。`ALL_ADMITTED` 取全部已准入且未撤销成员，`EXPLICIT_NODES` 取显式节点，`AGENT_GROUP` 取所列 group 的已准入成员。
- target 候选集合始终是全部已准入且未撤销成员；每条 route 再排除 source 自身。

生成器只为解析后的 source 集合生成 route，但可以从完整 target 候选集合选取目标。FSM 保存 policy 时必须重新解析 selector，并要求 route source 集合与解析结果完全相等；不得缺少 source、增加 selector 外 source 或重复 source。

Topology revision 绑定排序后的 Raft 成员身份及实际参与路由选择的 host/rack/zone/capacity 属性。瞬时 Gossip `Alive` 不进入 revision；它只影响 preview warning 和 topology health。Apply 时成员或路由属性变化返回稳定 conflict，单纯 liveness 波动不使 draft 失效。

Draft hash 绑定规范化后的策略输入和 topology revision。`source_ids`、`primary_policy_ids` 按集合规范化，空数组和空 map 不因 Proto JSON 表示差异产生不同摘要。Route 不进入新版 hash：preview route 允许用户在确认界面显式编辑，且目标选择算法可以独立演进；Apply 仍须在 Leader 上校验 topology revision、策略摘要，并由 FSM 校验最终 route 的 source/target 完整性。升级兼容期内，Apply 可以接受由服务端按同一 topology 重算成功的旧 route-bound hash，但不能接受客户端针对任意 route 自行重算的旧摘要。

### 10.5 Peer operation authorization state

Peer 写入授权以 Raft 中冻结的 replication run/task 为唯一权威。`PutSnapshot` 除 cluster、run、task、snapshot 和 checksum 外，还必须携带 `policy_id` 与 `policy_revision`。目标 Agent 将这些字段与当前 Raft run、task、run term 和 lease 逐项比较，并验证 mTLS source、local target、snapshot/checksum 及 route 状态；任何不一致都在访问 `PeerStore` 前拒绝。

失败 route 不可直接从终态重新发起网络写入。Leader 必须先通过 Raft transition 创建当前 term/lease 下的 retry intent，将选中的失败 task 原子恢复为 `PENDING`，再派发任务。目标只接受 `PENDING` 或 `RUNNING` task。已经 `SUCCEEDED` 的 task 不重新派发；网络响应丢失时，只要 Raft task 仍处于授权非终态，相同 immutable identity 与 checksum 的 Put 由 `PeerStore` 幂等成功。

Peer 删除使用独立的 durable `ReplicationDeleteIntent`：

```text
ReplicationDeleteIntent
  intent_id
  policy_id / policy_revision
  source_node_id / target_node_id / snapshot_id
  leader_term / expires_unix
  status             PENDING | SUCCEEDED | FAILED | EXPIRED
```

Retention planner 只能为策略 namespace 中已确认可删除的精确副本创建 intent，并继续遵守“正在复制、正在恢复、唯一最后副本不得删除”的保留约束。`DeleteSnapshot` 必须携带 `intent_id`、policy identity 和 source/target/snapshot identity；目标 Agent 在本地删除前验证 intent 为当前 term 下未过期的 `PENDING` intent。删除不存在的精确对象仍是幂等成功；成功或确定不存在后 intent 进入 `SUCCEEDED`。仅有 admitted source 身份、用户输入路径或过期 intent 均不得授权删除。

Check 和 metadata 操作只允许 mTLS source 查询 Raft 中曾冻结到当前 target 的同一 source/snapshot route。授权失败统一返回拒绝，不根据本地文件是否存在改变响应，并且不得调用 `PeerStore`。

## 11. 保留策略

主备份和 Peer 副本分别保留，互不误删：

- `keep_last`：按 policy、source node 和 snapshot 成功时间保留。
- `keep_days`：使用创建时间和策略 timezone 计算。
- `max_bytes`：达到上限时先删除最旧的已完成副本；正在复制、正在恢复或唯一可用的最后副本不得删除。
- 删除操作限制在由 ProcMesh 生成的 namespace/prefix 内；禁止根据用户提供的任意路径递归删除。
- 删除失败保留 `RETENTION_FAILED` 状态和重试入口，不把文件实际存在误报为已删除。

FS、S3、Peer 只共享保留意图，不共享删除实现。S3 可以交给 bucket lifecycle，但 Agent 仍负责识别远端 object 是否存在和报告状态。

## 12. API 边界

继续使用现有 Gin + ConnectRPC 入口，不新增第二套 REST 资源模型。建议新增两个公开服务和一个内部 mTLS 服务：

### 12.1 `ClusterBackupService`

- `CreatePolicy`
- `UpdatePolicy`
- `DeletePolicy`
- `ListPolicies`
- `ValidatePolicy`
- `StartRun`
- `GetRun`
- `ListRuns`
- `RetryFailedTasks`
- `GetRunEvents`
- `GetDestinationHealth`

`StartRun` 支持 `policy_id` 或一次性参数；一次性运行仍创建 `run_id`，但不保存为长期策略。

### 12.2 `DisasterReplicationService`

- `GetTopology`
- `GeneratePolicyDraft`
- `ApplyPolicyDraft`
- `ListPolicies`
- `GetPolicy`
- `UpdatePolicy`
- `DeletePolicy`
- `StartRun`
- `GetRun`
- `ListRuns`
- `RetryFailedRoutes`
- `VerifyReplica`
- `ListRecoverableSnapshots`

Draft API 不直接写 Raft；`ApplyPolicyDraft` 必须带 draft revision 和规范化策略摘要 hash，防止用户基于旧拓扑或已修改的策略输入覆盖当前状态；可编辑 route 由 FSM 独立校验。

### 12.3 内部 `PeerReplicationService`

- `PutSnapshot`
- `CheckSnapshot`
- `DeleteSnapshot`
- `GetReplicaMetadata`

该服务只监听现有 mTLS Agent RPC 面，禁止使用普通 Web 用户凭据作为节点认证。Leader 通过控制任务授权 source/target，目标 Agent 仍校验 cluster ID、source admission、snapshot checksum 和 policy revision。

## 13. Web 设计

### 13.1 主备份页面 `/backup`

页面包含：

- 备份策略列表：启用状态、目的地 FS/S3、下次运行时间、最近运行状态、保留策略。
- 新建/编辑策略：目标集合、cron、timezone、sink、profile、超时、并发和保留。
- 运行列表：`run_id`、目标数量、成功/失败/不可达计数、开始/完成时间。
- 运行详情：每个 Agent 的状态、snapshot ID、bytes、checksum、错误摘要和重试按钮。
- 明确提示“集群 FS 不防护主机丢失”。

异步操作使用现有 Vue Query polling 模式。运行状态为 `PARTIAL` 时不显示成成功；重试按钮只重试失败 Agent。

### 13.2 灾备副本页面 `/disaster-replica`

页面包含三个主要区域：

1. **概览**：路由总数、健康/延迟/失败数量、最近一次成功复制、可恢复快照数量。
2. **副本配置**：当前路由表、复制因子、cron/时区/enabled、保留、并发和拓扑约束。
3. **运行与恢复**：复制运行详情、按 route 重试、checksum 验证、可恢复快照和 Owner 恢复入口。不提供选择已有 `ClusterBackupRun` 或粘贴主备份 `run_id`。

“一键生成整个集群副本配置”打开轻量预览确认：默认预填 `schedule_cron=0 2 * * *`、浏览器 IANA 时区、`enabled=true`；预览中必须可编辑 cron、时区与 enabled（cron 可清空为仅手动）；同时展示生成规则、route 表、故障域警告和开销估算。确认应用后只写入策略与路由，不立刻创建 run；页面显示 policy revision。已有人工修改的 route 不被静默覆盖，必须由用户明确选择“替换当前配置”。

Peer 不出现在 `/backup` 的普通 sink 选择器中，避免用户误以为“创建一次 Peer 备份”就等价于建立灾备策略。旧 `AFTER_PRIMARY_BACKUP` 不再跟跑备份页。

所有状态同时提供 `LIVE`、`STALE`、`UNKNOWN` 语义。Leader/目标 Agent 不可达时保留最后已知结果并标明时间，不显示空白列表。

## 14. 权限与审计

新增独立权限：

| 权限 | 作用 |
|------|------|
| `backup.read` | 查看备份策略、运行、快照元数据和目的地健康 |
| `backup.manage` | 创建/修改/删除备份策略、启动/重试运行、删除主备份、发起恢复 |
| `replication.read` | 查看灾备拓扑、复制运行、健康和可恢复快照 |
| `replication.manage` | 生成/应用副本配置、修改路由、启动/重试/验证复制、删除副本 |

Peer 恢复最终仍需要 `backup.manage`，并且在 Owner 上重新校验 scope、expected revision 和当前进程身份。

所有控制面写入产生 audit：策略创建/修改/删除、手动运行、重试、生成草稿、应用草稿、路由替换、验证、保留删除和恢复请求。审计只记录用户、时间、policy/run/task ID、结果和错误摘要，不记录快照 payload、S3 secret 或完整路径中的凭据。

## 15. 失败语义

| 场景 | 结果 |
|------|------|
| Agent 在运行开始前不可达 | task=`UNAVAILABLE`，run 按配置为 `PARTIAL` 或 `FAILED` |
| Agent 在上传中超时 | task=`TIMEOUT`，支持单 Agent 重试 |
| FS 权限/磁盘不足 | task=`FAILED`，保留本地错误码和磁盘指标 |
| S3 凭据缺失 | task=`CONFIG_MISSING`，不重试直到配置变更或手动重试 |
| S3 网络/权限错误 | task=`FAILED`，指数退避重试 |
| Peer 目标离线 | route=`UNAVAILABLE`，源快照保留等待重试 |
| Peer checksum 不一致 | route=`FAILED`，禁止覆盖，需重新传输或人工处理 |
| Leader 变化 | 复用 run/task ID，按 lease 和幂等规则恢复 |
| policy 在运行中修改 | 当前运行继续使用冻结的 policy revision，下一次运行使用新 revision |
| target 被撤销 | 新运行拒绝；旧运行保留结果，不自动迁移副本 |
| retention 删除失败 | `RETENTION_FAILED`，不影响已有快照可恢复性 |

## 16. 兼容与迁移

1. 现有 `BackupAPI.CreateBackup` 继续代表“当前 Agent 本地备份”，不改变空 `processIds` 的含义。
2. 现有 `agent.yaml backup.schedule` 保留兼容，标记为本地定时备份；新 Web 集群策略不写入该字段。
3. 现有 `sink=peer` API 保留兼容并转换成一次性 replication task；新 Web 不把它作为主备份 sink。
4. 旧 FS 文件 `{fs_dir}/{snapshot_id}.json` 和旧 S3 key 继续可列举、读取和恢复；新集群运行使用带 cluster/node/policy namespace 的路径。
5. 老版本 Agent 收到新 ClusterBackup/Replication RPC 时返回 `UNAVAILABLE` 或 `UNIMPLEMENTED`，不把整个集群标为版本不兼容。策略目标检查会显示不支持该能力的节点。
6. 所有新 policy、run、route 和 replica 元数据使用版本字段，未来格式升级必须保留只读兼容窗口。

## 17. 可观测性

新增指标：

- `backup_runs_total{policy,sink,status}`
- `backup_tasks_total{sink,status}`
- `backup_task_duration_seconds{sink}`
- `backup_bytes_total{sink,result}`
- `backup_retention_delete_total{sink,result}`
- `replication_runs_total{policy,status}`
- `replication_tasks_total{status}`
- `replication_lag_seconds{source,target}`
- `replication_bytes_total{result}`
- `backup_last_success_unix{policy,node}`

日志中必须包含 `run_id`、`task_id`、`policy_id`、`source_node_id`、`target_node_id` 和 `snapshot_id`。禁止打印 S3 access key、secret key、签名 header 或完整敏感 URL。

## 18. 测试门槛

### 18.1 单元测试

- cron + timezone 计算和 DST 边界。
- target selector 冻结、Agent Group 变化和撤销节点。
- fire ledger 幂等、Leader term/lease 过期和重复调度。
- FS/S3 namespace、原子写入、checksum、保留策略和路径安全。
- Peer route 生成：N=1、N=2、环形、副本因子、故障域、负载均衡和稳定排序。
- route/task 幂等、checksum 冲突和按失败项重试。

### 18.2 集成测试

- 三 Agent 集群运行 FS 备份，验证每个 Agent 只写自己的目录。
- 三 Agent 集群运行 S3 备份，使用 fake S3 验证每个 Agent 的本地 profile 和 key namespace。
- Leader 在调度、上传、状态汇报三个阶段分别失效，验证不产生重复逻辑 run。
- 目标 Agent 离线后恢复，验证 `UNAVAILABLE`、重试和 `PARTIAL` 转终态。
- Peer mTLS 传输、临时文件、checksum、目标落盘和禁止 apply。
- 保留策略只删除 ProcMesh namespace，不删除其它对象或其它 policy 的快照。
- Raft/FSM 检查不包含 payload、secret 和本地 backup index。
- Restore 只能到 Owner，使用 CAS 产生新 revision，Peer 副本不会直接启动进程。

### 18.3 Web 测试

- 新建/编辑 FS 和 S3 集群策略。
- 定时策略的 timezone、下次运行时间和禁用/启用。
- 运行详情的 per-Agent 状态、部分失败和只重试失败项。
- 灾备页面一键生成、预览警告、应用、手工修改冲突和 route 重试。
- `LIVE` / `STALE` / `UNKNOWN` 状态，以及不可达节点不被显示为空。
- 权限拒绝、错误文案、异步 polling 停止条件和移动端布局。

## 19. 实施切分建议

实现计划应按以下边界拆分，每阶段都有可独立验证的结果：

1. ClusterBackup policy、fire ledger、run/task 元数据和 Leader scheduler。
2. Agent fan-out、FS/S3 cluster namespace、结果聚合和 retention。
3. PeerReplication policy、内部 mTLS transfer、幂等和校验。
4. 自动拓扑生成、故障域/负载均衡、preview/apply API。
5. Web `/backup` 集群策略与运行详情。
6. Web `/disaster-replica` 配置生成、拓扑、健康和恢复入口。
7. 故障转移、迁移兼容、审计、指标和全量测试。

在本 spec 获得审阅通过前，不开始上述实现阶段，也不创建 implementation plan。
