# ProcMesh 分布式进程管理平台 PRD v2.0

## 1. 产品概述

### 1.1 项目名称

**ProcMesh**

### 1.2 产品定位

ProcMesh 是一个面向服务器集群的 **Local-First、Agent-Owned、Peer-Managed 分布式进程管理平台**。

ProcMesh 提供类似 Supervisor 的本地进程生命周期管理能力，并扩展支持：

* 多节点统一管理
* 任意 Agent Web 管理入口
* Agent 自动发现与集群成员管理
* 远程进程操作
* 配置版本管理
* 实时日志
* 监控与健康检查
* 告警
* 批量操作
* 用户与 RBAC
* 操作审计

ProcMesh **不依赖独立部署的中心管理服务器**。

每个 Agent 均具备完整的：

* Process Manager
* Local Store
* Web UI
* API Server
* Cluster Client/Server
* Log Manager
* Metrics Collector
* Health Checker

进程配置和运行状态由进程所在 Agent 持有权威数据。

集群成员信息和进程概要信息通过 P2P 机制同步。

用户、RBAC、Agent 准入、集群身份等少量关键控制数据通过 Agent 内嵌的强一致机制维护。

---

# 2. 产品目标

ProcMesh 的核心目标是：

> 在不引入独立中心管理 Server 的情况下，实现服务器集群上的统一进程管理，同时保证管理平面故障不会影响本地业务进程运行。

核心目标包括：

### 2.1 本地自治

每个 Agent 独立管理本机 Process。

其他 Agent 全部不可用时，本机 Process Manager 仍然可以：

* 维护 Process 状态
* 自动拉起 Process
* 执行 Health Check
* 执行 Restart Policy
* 管理日志
* 保存运行状态

### 2.2 无单一管理故障点

不存在独立的：

```text
Central Management Server
```

任意健康 Agent 均可以：

* 提供 Web UI
* 提供 API
* 提供 CLI 接入
* 查看整个集群
* 管理其他 Agent

### 2.3 本地数据权威

Process 的以下信息始终由 Process 所属 Agent 作为唯一写入权威：

* Process Configuration
* Desired State
* Runtime State
* Active Configuration Revision
* Process Logs
* Detailed Metrics
* Health State

### 2.4 最小化分布式一致性范围

ProcMesh 不尝试对所有数据进行强一致复制。

根据数据类型采用：

```text
Local Strong Authority
        +
Cluster Eventual View
        +
Direct RPC Mutation
        +
Strongly Consistent Cluster Control Metadata
```

### 2.5 管理故障不影响业务

以下故障不得主动停止已经运行的业务 Process：

* 当前访问的 Web Agent 故障
* Agent 之间通信中断
* Gossip 网络异常
* 集群发生网络分区
* Cluster Control 暂时失去 Quorum
* 其他 Agent 故障

---

# 3. 非产品目标

ProcMesh V1 明确不实现：

* 跨节点调度
* 自动 Placement
* 自动故障迁移
* 跨节点 Process Replica 调度
* Container Orchestration
* Service Mesh
* 通用 Service Discovery
* 跨节点 Volume 管理
* 应用发布平台
* CI/CD
* 自动扩缩容
* Kubernetes 替代方案

节点故障时：

```text
Agent B Offline
```

ProcMesh **不会自动在 Agent A 创建 Agent B 上原有的 Process**。

---

# 4. 核心架构原则

## 4.1 Local First

进程管理能力必须完全本地化。

```text
ProcMesh Agent
│
├── Process Manager
├── Local Config Store
├── Local Runtime State
├── Local Logs
├── Health Checker
├── Restart Controller
└── Metrics Collector
```

即使：

```text
Cluster Network DOWN
```

本机 Process 仍然能够继续运行。

---

## 4.2 Agent-Owned Process

Process 必须存在明确的：

```text
owner_agent_id
```

对于：

```text
Process nginx
Owner = Agent C
```

只有 Agent C 可以修改：

```text
nginx Desired State
nginx Configuration
nginx Runtime State
```

其他 Agent 只能：

* 查询缓存
* 查询 Agent C
* 向 Agent C 发起 RPC 操作

不允许其他 Agent 直接修改 nginx 的权威数据副本。

---

## 4.3 No Dedicated Central Management Server

ProcMesh 不要求部署：

```text
procmesh-server
```

之类独立中心组件。

完整系统仅部署：

```text
procmesh-agent
```

部分 Agent 可以兼任：

```text
Cluster Control Member
```

但它仍然是普通 ProcMesh Agent。

---

## 4.4 Any Agent as Management Entry

任意 Agent 均可作为：

* Web 管理入口
* API 入口
* CLI Endpoint

例如：

```text
http://agent-a:18680
http://agent-b:18680
http://agent-c:18680
```

访问任意一个入口，都可以查看整个集群。

---

## 4.5 Read Anywhere, Write to Owner

远程读取允许：

```text
Browser
  ↓
Agent A
  ↓
Agent A Cluster Cache
```

或者：

```text
Agent A
  ↓
RPC
  ↓
Agent C
```

远程修改必须：

```text
Browser
    ↓
Agent A
    ↓
Direct RPC
    ↓
Owner Agent C
    ↓
Local Commit
```

禁止：

```text
Agent A 修改自己的副本
        ↓
Gossip
        ↓
Agent C 执行
```

---

# 5. 系统总体架构

```text
                       Browser / CLI
                             │
                             ▼
                  ┌──────────────────┐
                  │ Any ProcMesh     │
                  │ Agent Web/API    │
                  └────────┬─────────┘
                           │
         ┌─────────────────┼──────────────────┐
         │                 │                  │
         ▼                 ▼                  ▼
   Cluster Gossip      Direct RPC       Cluster Control
 Membership/Metadata     mTLS               Raft
         │                 │                  │
         └────────────┬────┴──────────────────┘
                      │
       ┌──────────────┼──────────────┐
       ▼              ▼              ▼
   Agent A          Agent B        Agent C
      │                │              │
 Process            Process        Process
 Manager            Manager        Manager
      │                │              │
   Shim/Proc         Shim/Proc       Shim/Proc
```

---

# 6. 系统 Plane 划分

ProcMesh 分为三个逻辑 Plane。

## 6.1 Management Plane

包括：

* Web UI
* CLI
* REST/gRPC API
* 用户登录
* RBAC
* 操作入口

Management Plane 故障：

> 不得影响 Process Plane。

---

## 6.2 Cluster Plane

包括：

* Agent Membership
* Gossip
* Agent RPC
* Cluster Metadata
* Cluster Control Consensus
* Agent Authentication
* mTLS
* RBAC 数据同步

Cluster Plane 故障：

> 不得主动停止 Process。

---

## 6.3 Process Plane

包括：

* Process Manager
* procmesh-shim
* Process
* Local Config
* Restart Policy
* Health Check
* Logs
* Local Metrics

Process Plane 是 ProcMesh 最核心的数据面。

---

# 7. Agent 管理

## 7.1 Agent Identity

每个 Agent 必须包含：

```text
node_id
cluster_id
hostname
boot_id
agent_version
advertise_address
rpc_address
gossip_address
```

### node_id

第一次启动 Agent 时生成随机 UUID，并持久化。

例如：

```text
node_id = 89df31ef-....
```

不得使用：

* IP
* Hostname
* MAC Address

作为 Agent 唯一 ID。

### boot_id

Agent 每次启动生成新的：

```text
boot_id
```

用于判断：

* Agent 是否发生重启
* Runtime State 是否可能失效
* PID 是否属于上一生命周期

---

# 8. Agent 状态模型

Agent 状态至少包括：

```text
JOINING
ALIVE
SUSPECT
FAILED
LEFT
REMOVED
REVOKED
```

语义：

### JOINING

正在加入集群。

### ALIVE

正常通信。

### SUSPECT

部分节点暂时无法确认其存活。

### FAILED

超过故障检测阈值。

FAILED 表示：

> 当前无法通信。

不代表：

> 节点已永久离开集群。

### LEFT

Agent 主动正常离开。

### REMOVED

管理员将 Agent 从集群删除。

### REVOKED

Agent 身份凭证已吊销。

必须保证：

```text
FAILED != REMOVED
```

---

# 9. Agent 加入集群

## 9.1 创建第一个集群

第一台 Agent：

```bash
procmesh cluster init
```

创建：

```text
cluster_id
cluster_ca
cluster_secret
initial_admin
```

并成为第一个 Cluster Control Member。

---

## 9.2 Join Token

管理员创建：

```bash
procmesh node token create
```

生成：

```text
One-Time Join Token
```

新 Agent：

```bash
procmesh agent join \
  --server agent-a:18683 \
  --token xxx
```

加入流程：

```text
Join Token
   ↓
身份校验
   ↓
获取 Cluster ID
   ↓
生成 Agent Identity
   ↓
签发 Agent Certificate
   ↓
加入 Membership
```

Join Token 必须支持：

* TTL
* 使用次数限制
* 手动失效

---

# 10. Agent 踢出与吊销

管理员执行：

```bash
procmesh node remove agent-c
```

必须同时完成：

```text
Cluster Membership Removal
+
Agent Credential Revocation
```

被移除 Agent 再次连接时：

```text
Reject
```

不能依靠 Gossip 的：

```text
LEFT
```

实现安全删除。

---

# 11. Cluster Control

部分关键数据不能使用最终一致机制维护。

ProcMesh 内部提供轻量：

```text
Cluster Control Store
```

推荐使用 Raft。

Cluster Control 默认由：

```text
3
```

个 Agent 组成。

较大集群可以配置：

```text
5
```

个。

Cluster Control 仅存少量全局数据。

---

# 12. Cluster Control 数据

必须强一致的数据包括：

```text
cluster_id

cluster_membership_authorization
agent_certificate
certificate_revocation

users
roles
permissions
user_role_bindings

agent_groups
cluster_policy
security_policy

alert_channels
global_settings
```

不得存储：

```text
完整 Process Runtime State
实时 Metrics
Process Logs
高频监控数据
```

Process Configuration 默认仍由 Owner Agent 权威保存。

---

# 13. 用户管理

ProcMesh 自己管理用户，不依赖外部 LDAP/OIDC。

## 13.1 用户模型

```text
User
├── user_id
├── username
├── password_hash
├── display_name
├── email
├── status
├── created_at
├── updated_at
├── last_login_at
└── password_changed_at
```

状态：

```text
ACTIVE
DISABLED
LOCKED
```

---

# 14. 用户认证

支持：

* Username + Password
* API Token

后续版本可扩展：

* MFA
* Passkey
* LDAP
* OIDC

密码不得明文保存。

必须使用安全 Password Hash 算法。

必须支持：

* Password Policy
* Login Rate Limit
* Failed Login Lockout
* Session Expiration
* Token Revocation

---

# 15. RBAC

权限模型：

```text
User
  ↓
Role
  ↓
Permission
```

支持一个用户绑定多个 Role。

---

# 16. Permission Model

建议采用资源 + 动作模型。

例如：

```text
cluster.read

node.read
node.manage
node.remove

process.read
process.create
process.update
process.delete
process.start
process.stop
process.restart

process.config.read
process.config.update

process.logs.read
process.logs.download

batch.execute

command.execute
command.execute.batch

user.read
user.create
user.update
user.delete

role.read
role.manage

audit.read

alert.read
alert.manage

cluster.manage
```

其中：

```text
command.execute
command.execute.batch
cluster.manage
user.delete
role.manage
```

属于高风险权限。

---

# 17. 内置 Role

默认提供：

### Super Admin

拥有所有权限。

### Cluster Admin

管理：

* Agent
* Process
* Configuration
* Alert

不能默认管理：

* Super Admin

### Operator

允许：

* 查看
* Start
* Stop
* Restart
* 查看 Logs

不能：

* 修改 RBAC
* 删除 Agent
* 修改安全配置

### Viewer

仅查看权限。

---

# 18. RBAC Scope

V1 支持权限 Scope：

```text
Cluster
Agent Group
Agent
Process Group
```

例如：

```text
Role: Finance Operator

Scope:
  Agent Group = finance

Permission:
  process.read
  process.restart
  process.logs.read
```

该用户不能操作：

```text
ad-system
```

节点。

---

# 19. RBAC 一致性

用户与 RBAC 数据必须存储在：

```text
Cluster Control Store
```

所有 Agent 读取同一个逻辑版本。

不能出现：

```text
Agent A:
user-x 已删除

Agent B:
user-x 仍然有管理员权限
```

Cluster Control 无法形成 Quorum 时：

### 已登录用户

允许执行：

```text
process.read
process.status
logs.read
```

等低风险查询。

涉及关键授权变更时：

```text
user.create
user.delete
role.manage
node.remove
```

必须拒绝。

对于 Process Mutation 是否允许，可以根据缓存 RBAC 的 TTL 策略执行。

默认安全策略：

> RBAC 信息超过允许缓存有效期后，拒绝新的远程 Mutation。

---

# 20. 本地 Process 管理

每个 Agent 独立负责本机 Process。

支持：

* 创建
* 删除
* 启动
* 停止
* 重启
* 强制停止
* 查看状态
* 自动启动
* 自动重启
* 实例数量
* 运行用户
* 工作目录
* 环境变量
* Command
* Arguments
* Resource Limit
* Health Check
* Log Policy

---

# 21. Process 数据模型

定义：

```text
ProcessSpec
```

例如：

```text
process_id
process_name
owner_agent_id
group
command
args
working_directory
run_as_user
environment
instances
restart_policy
health_check
log_policy
resource_limit
startup_priority
dependencies
created_at
updated_at
```

---

# 22. Process Instance

一个 ProcessSpec 可以包含多个 Instance。

例如：

```text
worker
instances = 4
```

生成：

```text
worker:0
worker:1
worker:2
worker:3
```

每个实例拥有：

```text
instance_id
pid
desired_state
observed_state
health_state
started_at
exit_at
exit_code
restart_count
active_revision
```

必须区分：

```text
ProcessSpec ID
ProcessInstance ID
PID
```

PID 不得作为永久 Identity。

---

# 23. Desired State

每个 Process Instance 必须维护：

```text
desired_state
```

支持：

```text
RUNNING
STOPPED
```

例如：

```text
desired_state = RUNNING
observed_state = EXITED
```

表示：

> Process 应该运行，但当前异常退出。

Process Manager 根据 Restart Policy 决定是否重新启动。

---

# 24. Observed State

Process 状态包括：

```text
STOPPED
STARTING
RUNNING
STOPPING
EXITED
BACKOFF
FATAL
UNKNOWN
```

推荐状态机：

```text
STOPPED
   │
   │ start
   ▼
STARTING
   │
   ├──────────── failure ──────────┐
   │                               ▼
   ▼                            BACKOFF
RUNNING                            │
   │                               │
   │ exit                          │ retry
   ▼                               │
EXITED ────────────────────────────┘

BACKOFF
   │
   │ retries exhausted
   ▼
FATAL
```

---

# 25. Stop State Machine

```text
RUNNING
   │
   ▼
STOPPING
   │
   ├── graceful signal
   │
   ├── grace period
   │
   └── timeout
           ↓
        SIGKILL
           ↓
        STOPPED
```

支持配置：

```text
stop_signal
stop_timeout
kill_signal
```

---

# 26. Health State

Process State 和 Health State 必须分开。

Health：

```text
HEALTHY
UNHEALTHY
UNKNOWN
```

因此可能存在：

```text
RUNNING / HEALTHY

RUNNING / UNHEALTHY

RUNNING / UNKNOWN
```

---

# 27. procmesh-shim

为保证 procmesh-agent 自身重启时尽可能不影响业务 Process，建议每个 Process Instance 由轻量：

```text
procmesh-shim
```

承载。

架构：

```text
procmesh-agent
      │
      ├── procmesh-shim worker:0
      │          └── worker
      │
      └── procmesh-shim worker:1
                 └── worker
```

shim 负责：

* Process fork/exec
* Process Signal
* PID Tracking
* Exit Code
* stdout/stderr
* 基础 Process 生命周期
* Agent Reconnect

Agent 重启后：

```text
Agent Start
   ↓
Discover Existing Shim
   ↓
Recover Runtime State
   ↓
Continue Management
```

---

# 28. Agent Crash 语义

需要保证：

### Web/API Crash

Process：

```text
Unaffected
```

### Cluster Network Failure

Process：

```text
Unaffected
```

### procmesh-agent Crash

已运行 Process：

```text
继续运行
```

前提：

```text
shim 正常
```

Agent 恢复后重新接管。

### Host Crash

Process 无法继续运行。

Host 恢复后根据：

```text
autostart
```

策略恢复。

---

# 29. Restart Policy

支持：

```text
never
always
on-failure
```

配置：

```yaml
restart_policy:
  mode: on-failure
  max_retries: 5
  retry_window: 60s

  backoff:
    initial: 1s
    max: 60s
    multiplier: 2
```

必须避免 Crash Loop。

---

# 30. Process Crash Loop

Process 高频失败时进入：

```text
BACKOFF
```

超过：

```text
max_retries
```

进入：

```text
FATAL
```

同时产生：

```text
PROCESS_CRASH_LOOP
```

告警。

管理员可以：

```text
reset failure state
restart
```

重新执行。

---

# 31. Process Dependency

V1 只支持：

> 同一 Agent 内 Process Dependency。

例如：

```text
api
  depends_on:
    mysql
```

支持依赖条件：

```text
STARTED
HEALTHY
```

默认：

```text
HEALTHY
```

Dependency 必须形成 DAG。

配置保存时必须检测：

```text
Circular Dependency
```

V1 不支持：

```text
跨 Agent Dependency
```

---

# 32. Process 启动顺序

支持：

```text
startup_priority
```

数值越小越早启动。

例如：

```text
mysql  = 10
redis  = 20
api    = 30
worker = 40
```

Dependency 优先级高于 startup_priority。

---

# 33. Process Configuration Storage

Process Configuration 权威存储在：

```text
Owner Agent Local Store
```

包括：

* Command
* Arguments
* Environment
* Working Directory
* Run User
* Restart Policy
* Health Check
* Log Policy
* Resource Limit
* Startup Priority
* Dependency

---

# 34. Configuration Revision

每次修改配置生成：

```text
revision
```

例如：

```text
v41
v42
v43
```

必须同时维护：

```text
latest_revision
active_revision
```

例如：

```text
Latest Configuration: v43
Running Configuration: v42
```

Web UI 显示：

```text
Configuration changed.
Restart required.
```

---

# 35. 配置修改 CAS

修改配置必须携带：

```text
expected_revision
```

例如：

```text
GET
revision = 42
```

提交：

```text
PUT
expected_revision = 42
```

如果实际：

```text
revision = 43
```

返回：

```text
409 Conflict
```

避免 Lost Update。

---

# 36. 配置历史

保留：

```text
revision
operator
timestamp
diff
comment
```

支持：

* 查看历史
* Diff
* Rollback

Rollback 本质产生新版本。

例如：

```text
v42
v43
v44
```

Rollback 到 v42 后产生：

```text
v45
```

内容等于 v42。

不得直接删除：

```text
v43 / v44
```

历史。

---

# 37. Local Store

本地数据建议拆分：

```text
Process Spec
Runtime State
Operation Journal
Configuration History
Audit Log
Local Event
Metrics
```

关键配置写入必须保证：

```text
Atomic Commit
```

---

# 38. 配置备份

Local Authority 不等于 Local Only Copy。

允许 Process Configuration 异步备份。

支持：

```text
Peer Backup
Filesystem Backup
S3 Compatible Storage
```

备份副本只能用于：

* Disaster Recovery
* Configuration Export
* Manual Restore

不得成为正常运行状态下的第二写入者。

原则：

```text
Single Writer
Multiple Backup Copies
```

---

# 39. 集群数据同步

集群之间仅同步管理所需概要信息。

## 39.1 Gossip 数据

包括：

```text
Agent Membership
Agent Address
Agent Version
Agent Labels
Agent Resource Summary
Process Summary
Configuration Revision Summary
Health Summary
```

---

# 40. 不通过 Gossip 同步的数据

不得通过 Gossip 高频同步：

```text
Process Logs
Full Process Configuration
Detailed Metrics
Metric Samples
Process stdout/stderr
Large Audit Records
```

---

# 41. 数据权威模型

| 数据                           | 权威来源            | 一致性   |
| ---------------------------- | --------------- | ----- |
| Process Config               | Owner Agent     | 本地强一致 |
| Desired State                | Owner Agent     | 本地强一致 |
| Runtime State                | Owner Agent     | 本地实时  |
| Health State                 | Owner Agent     | 本地实时  |
| Logs                         | Owner Agent     | 本地    |
| Metrics Detail               | Owner Agent     | 本地    |
| Process Summary              | Owner Agent     | 最终一致  |
| Agent Membership Observation | Gossip          | 最终一致  |
| User                         | Cluster Control | 强一致   |
| Role                         | Cluster Control | 强一致   |
| Permission                   | Cluster Control | 强一致   |
| Agent Admission              | Cluster Control | 强一致   |
| Cert Revocation              | Cluster Control | 强一致   |
| Agent Group                  | Cluster Control | 强一致   |
| Alert Channel                | Cluster Control | 强一致   |

---

# 42. Web 管理

每个 Agent 均提供完整 Web UI。

访问：

```text
http://agent-a:18680
http://agent-b:18680
http://agent-c:18680
```

任意 Agent 页面可以查看：

```text
Cluster
├── Agent A
│   ├── nginx
│   └── api
├── Agent B
│   ├── worker
│   └── redis
└── Agent C
    └── cron
```

---

# 43. Web Dashboard

## Cluster Overview

展示：

* Agent Total
* Alive
* Suspect
* Failed
* Process Total
* Running
* Unhealthy
* Fatal
* CPU Overview
* Memory Overview
* Disk Overview
* Recent Alerts
* Recent Operations

---

# 44. 数据新鲜度

最终一致的数据必须显示：

```text
last_updated_at
```

UI 不允许将 Stale 数据伪装成实时状态。

定义：

```text
LIVE
STALE
UNKNOWN
```

例如：

```text
Agent C
State: FAILED

api
Last Known State: RUNNING
Updated: 38 seconds ago
Freshness: STALE
```

而不是继续显示：

```text
api RUNNING ✓
```

---

# 45. Agent 页面

展示：

* Hostname
* Node ID
* Address
* Agent Version
* Agent Status
* Boot Time
* Uptime
* CPU
* Memory
* Disk
* Network
* Load Average
* Process Count
* Labels
* Groups

支持：

* Process 管理
* Logs
* Metrics
* Audit
* Remove Agent

---

# 46. Process 页面

展示：

```text
Process Name
Process ID
Owner Agent
Instances
Desired State
Observed State
Health State
PID
CPU
Memory
Uptime
Restart Count
Exit Code
Active Revision
Latest Revision
```

支持：

* Start
* Stop
* Restart
* Force Stop
* Edit Configuration
* Rollback
* Logs
* Metrics
* Audit

---

# 47. 远程操作

例如用户访问：

```text
Agent A
```

执行：

```text
Restart Agent C / nginx
```

调用链：

```text
Browser
   ↓
Agent A
   ↓
RBAC Check
   ↓
RPC Agent C
   ↓
RBAC Context Verification
   ↓
Operation Journal
   ↓
Process Manager
   ↓
nginx
```

最终执行 Agent 必须再次验证权限。

不能只信任入口 Agent。

---

# 48. Mutation 幂等

所有修改类操作必须携带：

```text
operation_id
```

例如：

```text
f123...
```

包含：

```text
operation_id
requester
target
operation
request_time
```

目标 Agent 如果收到重复 operation_id：

```text
Return Previous Result
```

不得重复执行。

---

# 49. Operation Journal

每个 Agent 保存本地操作 Journal。

字段：

```text
operation_id
operator
source_agent
target
operation_type
request_payload
created_at
started_at
finished_at
status
result
error
```

状态：

```text
PENDING
RUNNING
SUCCESS
FAILED
TIMEOUT
UNKNOWN
```

---

# 50. 批量操作

支持：

* Batch Start
* Batch Stop
* Batch Restart
* Batch Configuration Update

按以下条件选择 Target：

* Agent
* Agent Group
* Process
* Process Group

---

# 51. 批量操作语义

批量任务不是分布式事务。

例如：

```text
Batch Restart
Target: 100
```

结果：

```text
SUCCESS   86
FAILED     5
TIMEOUT    4
UNKNOWN    5
```

不得承诺：

```text
All or Nothing
```

---

# 52. Batch Operation

Batch Operation 包含：

```text
batch_id
operator
created_at
targets
status
summary
```

每一个 Target 独立拥有：

```text
operation_id
```

支持：

* Retry Failed
* Retry Timeout
* View Result
* Export Result

---

# 53. 任意命令执行

任意 Shell Command 属于高风险功能。

V1 默认：

```text
Disabled
```

如果启用：

必须具备独立权限：

```text
command.execute
command.execute.batch
```

必须：

* 完整审计
* 参数记录
* 返回值记录
* 超时
* 输出大小限制
* 并发限制

不允许：

```text
Viewer
Operator
```

默认拥有该权限。

---

# 54. 日志管理

日志权威数据存储在 Process 所属 Agent。

支持：

* stdout
* stderr
* tail
* 实时 Stream
* 搜索
* 下载
* Rotation
* Retention

---

# 55. Log Policy

支持：

```yaml
log:
  max_size: 100MB
  max_files: 10
  max_age: 7d
  compress: true
```

---

# 56. 远程日志

访问 Agent A 查看 Agent C 日志：

```text
Browser
   ↓
Agent A
   ↓
RPC
   ↓
Agent C
   ↓
Log Stream
```

不需要把日志复制到 Agent A。

---

# 57. 日志保护

必须支持：

```text
max_tail_lines
max_download_size
max_stream_bandwidth
max_concurrent_streams
stream_timeout
```

避免日志查询拖垮 Agent。

---

# 58. Disk Protection

Agent 必须监控磁盘。

默认：

```text
Disk > 85%
Warning

Disk > 90%
Aggressive Log Cleanup

Disk > 95%
Emergency Protection
```

Emergency 状态优先保护：

```text
Config
Operation Journal
Audit
Local DB
```

可以牺牲：

```text
Old Logs
Historical Metrics
```

---

# 59. Metrics

## Agent Metrics

包括：

* CPU
* Memory
* Disk
* Network
* Load Average
* File Descriptor
* Agent Uptime

## Process Metrics

包括：

* CPU
* Memory
* PID
* Uptime
* Restart Count
* Exit Code
* IO
* File Descriptor

---

# 60. Metrics 数据策略

高频 Metrics：

```text
Local Only
```

Cluster Gossip 只传播 Summary。

例如：

```text
CPU = 42%
Memory = 71%
```

而详细 1 分钟粒度数据由 Owner Agent 提供。

---

# 61. Health Check

支持：

### Process Alive

检查 PID/Process。

### HTTP Check

```text
URL
Method
Expected Status
```

### TCP Check

检测 TCP Port。

### Custom Check

运行本地 Check Command。

---

# 62. Health Check 参数

```text
initial_delay
interval
timeout
failure_threshold
success_threshold
restart_on_failure
restart_cooldown
```

例如：

```yaml
health_check:
  type: http
  url: http://127.0.0.1:8080/health
  interval: 10s
  timeout: 2s
  failure_threshold: 3
  success_threshold: 2
  restart_on_failure: true
  restart_cooldown: 120s
```

---

# 63. 告警

支持：

* Agent Failed
* Agent Suspect Too Long
* Process Exit
* Process Fatal
* Process Crash Loop
* Health Check Failed
* CPU High
* Memory High
* Disk High
* Cluster Control No Quorum
* Local DB Error
* Certificate Expiring
* Agent Version Mismatch

---

# 64. Alert Channel

支持：

* Web
* Webhook
* Email
* 企业微信
* 钉钉
* Slack

Alert Channel Configuration 存储于：

```text
Cluster Control Store
```

---

# 65. 告警去重

Agent Offline 等分布式故障可能被多个节点同时检测。

必须生成：

```text
alert_fingerprint
```

例如：

```text
NODE_FAILED:{node_id}
```

结合：

```text
dedup_window
```

进行告警去重。

V1 不承诺 Exactly Once Alert。

---

# 66. CLI

本地：

```bash
procmesh status

procmesh process list

procmesh start nginx
procmesh stop nginx
procmesh restart nginx

procmesh logs nginx
```

远程：

```bash
procmesh node list

procmesh node status agent-a

procmesh process list --node agent-a

procmesh restart --node agent-a nginx
```

---

# 67. 用户管理 CLI

```bash
procmesh user list

procmesh user create admin2

procmesh user disable user-a

procmesh user enable user-a

procmesh role list

procmesh role create operator-finance

procmesh role grant operator-finance process.restart
```

---

# 68. API

每个 Agent 提供：

```text
Authentication API
User API
RBAC API
Agent API
Process API
Configuration API
Log API
Metrics API
Cluster API
Batch API
Alert API
Audit API
```

建议外部 API 使用 REST。

Agent-to-Agent 内部通信优先使用：

```text
gRPC + mTLS
```

---

# 69. Agent 间通信安全

所有 Agent RPC 必须：

```text
mTLS
```

每个 Agent 使用 Cluster CA 签发的 Certificate。

Certificate 必须包含：

```text
cluster_id
node_id
```

Agent 必须验证：

```text
Cluster ID Match
Certificate Valid
Certificate Not Revoked
```

---

# 70. Web 安全

支持：

* Secure Session
* CSRF Protection
* XSS Protection
* SameSite Cookie
* Session TTL
* Login Rate Limit

生产环境推荐强制：

```text
HTTPS
```

---

# 71. API Token

用户可以创建 API Token。

Token 属性：

```text
token_id
user_id
name
created_at
expires_at
last_used_at
status
```

Token 只显示：

```text
Once
```

数据库只保存：

```text
Token Hash
```

支持：

```text
Revoke
```

---

# 72. 审计

所有 Mutation 必须进入 Audit。

包括：

```text
Login
Logout
User Change
Role Change
Agent Join
Agent Remove
Process Create
Process Delete
Process Start
Process Stop
Process Restart
Config Change
Config Rollback
Batch Operation
Command Execute
Alert Config Change
```

---

# 73. Audit Record

包含：

```text
audit_id
timestamp
user_id
username
source_ip
source_agent
target_agent
resource
action
operation_id
result
metadata
```

Audit 默认 Append Only。

---

# 74. Cluster Audit View

每个操作最终由 Target Agent 保存权威 Audit。

入口 Agent 可以保存：

```text
Request Audit
```

目标 Agent 保存：

```text
Execution Audit
```

Web 聚合展示。

不得要求所有 Audit 实时复制到所有 Agent。

---

# 75. Network Partition

网络分区必须作为正常故障模型支持。

例如：

```text
Cluster:

A ─── B    X    C ─── D
```

Partition 期间：

### A/B

继续管理本地 Process。

### C/D

继续管理本地 Process。

### 远程不可达 Agent

操作：

```text
TIMEOUT / UNAVAILABLE
```

### 禁止

由于某 Agent 被判断 FAILED 而自动在其他节点创建 Process。

---

# 76. Partition Heal

网络恢复后：

* Membership 收敛
* Agent Summary 收敛
* Process Summary 更新
* Config Revision Summary 更新

不得：

* 自动覆盖 Owner Agent Config
* 根据旧缓存修改 Desired State

Owner Agent 永远拥有自己的 Process 权威状态。

---

# 77. Cluster Control No Quorum

如果 Cluster Control 暂时失去 Quorum：

必须继续：

* Process 生命周期管理
* Auto Restart
* Health Check
* Local Logging
* Local Monitoring
* Local CLI

可继续：

* 读取缓存 Cluster Metadata
* 查看 Local Process

默认限制：

* 创建用户
* 删除用户
* 修改 Role
* Agent Admission
* Agent Remove
* 修改全局安全配置

Process 远程 Mutation 是否允许由：

```text
RBAC Cache TTL
```

控制。

---

# 78. Agent 重启恢复

Agent 启动流程：

```text
Load Local DB
   ↓
Load Process Spec
   ↓
Discover Existing Shim
   ↓
Recover Runtime State
   ↓
Reconcile Desired State
   ↓
Join Cluster
```

必须防止：

```text
Agent Restart
   ↓
重复启动已经运行的 Process
```

---

# 79. Host Reboot

Host 重启：

```text
procmesh-agent autostart
```

读取：

```text
Process Desired State
```

以及：

```text
autostart
```

按照：

```text
Dependencies
+
startup_priority
```

恢复 Process。

---

# 80. Local DB Corruption

系统必须检测：

```text
Local Database Corruption
```

原则：

> 数据库异常不得主动杀死仍正常运行的业务 Process。

进入：

```text
DEGRADED
```

状态。

停止新的高风险 Mutation，并产生 Critical Alert。

---

# 81. Duplicate Node ID

如果两个 Agent 使用相同：

```text
node_id
```

同时加入集群，必须拒绝后加入节点。

输出：

```text
DUPLICATE_NODE_ID
```

避免 VM Clone 产生身份冲突。

---

# 82. Agent 自身可观测性

ProcMesh 必须暴露：

```text
/healthz
/readyz
/metrics
```

核心 Metrics：

```text
procmesh_agent_uptime

procmesh_cluster_members
procmesh_cluster_alive_members
procmesh_cluster_suspect_members

procmesh_rpc_requests_total
procmesh_rpc_errors_total
procmesh_rpc_latency

procmesh_process_running
procmesh_process_restart_total
procmesh_process_crashloop_total

procmesh_healthcheck_total
procmesh_healthcheck_failures_total

procmesh_log_bytes
procmesh_log_dropped_bytes

procmesh_store_size
procmesh_store_write_latency

procmesh_operation_queue_depth

procmesh_cluster_control_quorum
```

---

# 83. Web 自身状态

Dashboard 必须展示 ProcMesh 自身：

```text
Cluster Health
Cluster Control Health
Gossip Health
RPC Health
Version Distribution
Certificate Expiration
```

避免：

> 监控系统本身出故障但用户误以为业务 Process 出故障。

---

# 84. Agent Version

Agent Gossip Summary 包含：

```text
agent_version
protocol_version
```

必须支持 Protocol Compatibility Check。

例如：

```text
Agent A: v1.3
Agent B: v1.2
```

只要协议兼容，可以共同运行。

不兼容时：

```text
INCOMPATIBLE_VERSION
```

---

# 85. Rolling Upgrade

升级 Agent 时：

```text
Stop Agent
   ↓
Process/Shim Continue
   ↓
Upgrade Binary
   ↓
Start Agent
   ↓
Reconnect Shim
```

升级不应默认 Restart Business Process。

---

# 86. 一致性设计总结

ProcMesh 使用四种数据机制。

## A. Local Strong State

```text
Process Config
Desired State
Runtime State
Operation Journal
```

权威：

```text
Owner Agent
```

---

## B. Eventual Cluster Metadata

通过：

```text
Gossip
```

传播：

```text
Membership
Process Summary
Resource Summary
Version
Config Revision Summary
```

---

## C. Direct RPC

所有远程 Mutation：

```text
Start
Stop
Restart
Configuration Update
Delete
```

直接发送给：

```text
Owner Agent
```

---

## D. Strong Cluster Control

通过：

```text
Raft
```

维护：

```text
Users
RBAC
Agent Admission
Certificate Revocation
Cluster Policy
Agent Groups
Alert Channels
```

---

# 87. SLO 建议

ProcMesh V1 建议目标：

### Local Process Management

Agent 正常时：

```text
99.99%
```

本地 Process Control 可用。

### Process Management Independence

Cluster Plane 故障不得主动影响：

```text
100%
```

已运行 Process。

### Remote Operation

健康网络条件下：

```text
P95 < 500ms
```

完成 RPC 接受响应。

不包含 Process 自身 Stop/Start 时间。

### Dashboard Freshness

Alive Agent Summary：

```text
95% < 10s
```

---

# 88. MVP V1.0

V1.0 必须包含：

### Local

* Process Manager
* Process State Machine
* procmesh-shim
* Start/Stop/Restart
* Auto Start
* Restart Policy
* Configuration Revision
* CAS
* Logs
* Health Check
* Metrics
* Local Audit

### Cluster

* Agent Identity
* Agent Join
* Gossip Membership
* Direct RPC
* mTLS
* Cluster Process View

### Cluster Control

* Embedded Raft
* User Management
* RBAC
* Agent Admission
* Certificate Revocation

### Management

* Web UI
* CLI
* API
* Remote Process Control
* Remote Config
* Remote Logs

---

# 89. V1.1

增加：

* Batch Operation
* Agent Group
* Process Group
* Advanced Dashboard
* Alert
* Webhook
* Email
* 企业微信
* 钉钉
* Slack
* Configuration Backup
* Configuration Restore
* Historical Metrics

---

# 90. V1.2

增加：

* Advanced RBAC Scope
* MFA
* Advanced Audit Search
* Upgrade Management
* Agent Certificate Rotation
* Disaster Recovery Tool（整集群配置备份与 Peer 灾备副本已作为 Q5 之后的增量交付，见 `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md`；不包含自动故障迁移或跨节点 Placement）
* Alert Dedup Optimization
* Large Cluster Optimization

---

# 91. 明确暂不实现

以下能力不进入 V1：

```text
Cross-Node Scheduler

Cross-Node Process Failover

Cross-Node Process Dependency

Automatic Placement

Container Scheduling

Distributed Volume

Generic Service Discovery

Exactly Once Batch Transaction

Exactly Once Alert

Arbitrary Command Batch Execution by Default
```

---

# 92. 核心故障验收场景

正式发布前至少必须验证以下场景。

### Case 1

当前 Web Agent Crash。

预期：

```text
业务 Process 不受影响
用户可访问其他 Agent
```

### Case 2

Agent A 与 Agent B 网络断开。

预期：

```text
两边本地 Process 正常
远程管理失败
不发生 Process 迁移
```

### Case 3

procmesh-agent Crash。

预期：

```text
Shim / Process 继续运行
Agent 恢复后重新接管
```

### Case 4

Agent Host Reboot。

预期：

```text
根据 Desired State 自动恢复
```

### Case 5

两个管理员并发修改 Process Config。

预期：

```text
一个成功
另一个 409 Conflict
```

### Case 6

Restart RPC 已执行但 Response 丢失。

预期：

```text
重试相同 operation_id
不重复 Restart
```

### Case 7

100 节点批量 Restart，中途部分 Agent Offline。

预期：

```text
显示 Partial Success
不隐藏 Timeout/Unknown
```

### Case 8

Agent 被管理员 Remove 后重新连接。

预期：

```text
拒绝连接
```

### Case 9

Cluster Control No Quorum。

预期：

```text
本地 Process 正常
用户/RBAC Mutation 被限制
```

### Case 10

磁盘达到 95%。

预期：

```text
限制日志
优先保留核心配置和状态
产生 Critical Alert
```

### Case 11

Agent Local DB Corruption。

预期：

```text
已有业务 Process 不被主动终止
Agent 进入 DEGRADED
```

### Case 12

VM Clone 导致 node_id 重复。

预期：

```text
拒绝重复 Agent 加入
```

---

# 93. 最终产品架构定义

ProcMesh 的最终架构可以概括为：

```text
               ┌─────────────────────────┐
               │ Cluster Control         │
               │ Raft / Strong Consistency│
               │                         │
               │ Users / RBAC            │
               │ Agent Admission         │
               │ Security Policy         │
               └────────────┬────────────┘
                            │

      Agent A               │               Agent B
┌────────────────┐          │        ┌────────────────┐
│ Local Authority│          │        │ Local Authority│
│                │          │        │                │
│ Process Config │          │        │ Process Config │
│ Desired State  │          │        │ Desired State  │
│ Runtime State  │          │        │ Runtime State  │
└───────┬────────┘          │        └───────┬────────┘
        │                   │                │
        ├──── Gossip Metadata ───────────────┤
        │                                    │
        └──────── Direct RPC / mTLS ─────────┘
```

---

# 94. 产品核心原则

ProcMesh 必须长期坚持以下原则。

### Principle 1 — Local First

Process 生命周期属于本机 Agent。

### Principle 2 — Agent Owned

Process 只允许 Owner Agent 修改权威状态。

### Principle 3 — Read Anywhere

任何 Agent 可以提供集群视图。

### Principle 4 — Write to Owner

所有远程 Mutation 必须发送到 Owner Agent。

### Principle 5 — Gossip Is Not Transaction

Gossip 只用于：

```text
Discovery
Failure Detection
Summary Metadata
```

不用于 Process Configuration Transaction。

### Principle 6 — Control Data Is Strongly Consistent

用户、RBAC、Agent Admission、安全状态必须强一致。

### Principle 7 — Process Plane Independent

Management Plane 和 Cluster Plane 故障不得主动影响 Process Plane。

### Principle 8 — Partial Failure Is Normal

远程操作和批量操作必须将：

```text
Timeout
Unknown
Partial Success
```

作为正常状态处理。

### Principle 9 — Every Mutation Is Idempotent

所有远程修改操作必须携带：

```text
operation_id
```

### Principle 10 — Never Hide Stale Data

Web UI 必须明确展示：

```text
LIVE
STALE
UNKNOWN
```

---

# 95. 技术选型

## 一、我推荐的整体技术栈

| 模块                | 推荐技术                             | 建议                     |
| ----------------- | -------------------------------- | ---------------------- |
| 后端语言              | **Go**                           | 非常适合                   |
| Web/API Server    | Gin + ConnectRPC         | Gin/Fiber      |
| API IDL           | **Protobuf**                     | Agent、CLI、Web 共用模型     |
| Agent RPC         | **ConnectRPC / gRPC-compatible** | mTLS                   |
| Web API           | **ConnectRPC + 少量 REST**         | Vue 可直接生成 TS Client    |
| Gossip            | **hashicorp/memberlist**         | 成员发现、Failure Detection |
| Consensus         | **hashicorp/raft**               | 用户/RBAC/准入等            |
| Raft Log          | **raft-boltdb/v2**               | V1 更稳妥                 |
| 本地业务存储            | **SQLite**                       | 强烈推荐                   |
| SQLite Driver     | **modernc.org/sqlite**           | CGO-Free               |
| Control FSM Store | **bbolt**                        | 用户/RBAC/Policy         |
| Process 管理        | `os/exec` + `x/sys/unix`         | Linux 原生能力             |
| 系统指标              | **gopsutil/v4**                  | CPU/内存/磁盘/进程           |
| 内部指标              | **Prometheus client_golang**     | `/metrics`             |
| 前端                | **Vue 3 + TypeScript**           | 很合适                    |
| Build             | **Vite**                         | 不建议 Nuxt               |
| UI                | **shadcn-vue**                   | 很适合你的风格                |
| Primitive         | **Reka UI**                      | shadcn-vue 底层生态        |
| CSS               | **Tailwind CSS v4**              | 推荐                     |
| Server State      | **TanStack Vue Query**           | 比全塞 Pinia 更合适          |
| Client State      | **Pinia**                        | Auth/UI/Theme          |
| 图表                | shadcn-vue Chart / Unovis        | Dashboard 足够           |
| 日志存储              | **普通文件**                         | 不进数据库                  |
| 历史 Metrics        | SQLite Downsample                | V1 足够                  |
| 前端部署              | `go:embed`                       | 打进 Agent 单二进制          |

其中 `memberlist` 本身就是 Go 的 Gossip membership / failure detection 库，非常贴合 ProcMesh 的 Cluster Plane；HashiCorp Raft 则适合你前面定义的 Cluster Control replicated state machine。([GitHub][1])

## 界面UI

整体Web UI样式是常规的管理后台布局，界面配色与UI类似于ChatGPT Web的风格

# 96. 一句话产品定义

> **ProcMesh 是一个 Local-First、Agent-Owned、Peer-Managed 的分布式进程管理平台：每个 Agent 独立负责本机进程生命周期，通过 P2P 成员发现、弱一致集群视图和直接 RPC 实现多节点统一管理，并通过内嵌强一致 Cluster Control 管理用户、RBAC、Agent 身份及关键安全状态，从而在不依赖独立中心管理服务器的情况下，实现高可用、可审计、可扩展的服务器进程管理能力。**
