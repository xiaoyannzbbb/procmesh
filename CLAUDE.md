# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

ProcMesh 是一个 **Local-First、Agent-Owned、Peer-Managed** 的分布式进程管理平台。不需要独立的中心管理服务器，每个 Agent 都具备完整的进程管理、Web UI、API 和集群通信能力。

**核心特性：**
- 本地自治：每个 Agent 独立管理本机进程，集群故障不影响本地进程运行
- 去中心化：任意 Agent 都可作为管理入口，提供 Web UI 和 API
- 强一致控制：用户、RBAC、准入通过内嵌 Raft 实现强一致
- 最终一致视图：进程摘要、节点状态通过 Gossip 实现最终一致
- 远程操作：通过 mTLS RPC 直接向 Owner Agent 发起操作

**三个二进制：**
- `procmesh-agent`：常驻进程，管理本机进程、提供 Web/API、参与集群
- `procmesh-shim`：轻量级进程包装器，每个进程实例一个，Agent 崩溃后继续运行
- `procmesh`：无状态 CLI 工具

## 构建与测试

### 前置条件

- Go 1.25+
- Node.js 22.20.0+、低于 23 (前端开发)
- protoc + 插件 (修改 proto 时需要)

### 常用命令

```bash
# 构建所有二进制
make bin

# 运行所有测试
make test

# 运行 E2E 测试（Linux，需要更长超时）
make test-e2e

# 重新生成 protobuf（Go + TypeScript）
make proto
make proto-ts

# 前端构建
make web

# 仅构建前端（开发时）
cd web && npm ci && npm run build

# 前端开发服务器
cd web && npm run dev

# 前端测试
cd web && npm test
cd web && npm run test:e2e

# 前端 i18n 检查
cd web && npm run i18n:check
```

### 测试单个 Go 包

```bash
go test ./internal/process
go test ./internal/store -v
go test ./internal/agent -run TestP5_
```

## 架构设计

### 数据权威模型

ProcMesh 使用混合一致性模型：

| 数据类型 | 权威来源 | 一致性 | 存储 |
|---------|---------|--------|------|
| Process Spec/Config/Runtime/Logs | Owner Agent | 本地强一致 | SQLite + 文件 |
| Process Summary | Owner 产出 | 最终一致 | Gossip |
| Membership | 各节点观察 | 最终一致 | memberlist |
| Users/RBAC/准入/吊销 | Cluster Control | 强一致 | Raft + bbolt |

**核心原则：**
1. **Local Authority**：进程配置和状态由 Owner Agent 持有权威数据
2. **Write to Owner**：远程修改必须通过 mTLS RPC 发送到 Owner Agent
3. **Read Anywhere**：任意 Agent 可提供集群视图（可能是缓存/STALE）
4. **Gossip ≠ Transaction**：Gossip 只用于发现、故障检测、摘要，不传递配置变更

### 目录结构

```
procmesh/
├── cmd/
│   ├── procmesh-agent/     # Agent 主程序入口
│   ├── procmesh-shim/      # Shim 包装器入口
│   └── procmesh/           # CLI 工具入口
├── internal/
│   ├── process/            # 进程状态机、生命周期管理
│   ├── shim/               # Shim 协议客户端
│   ├── store/              # SQLite 本地存储（spec/instance/revision/audit）
│   ├── logmgr/             # 日志轮转、磁盘保护
│   ├── health/             # 健康检查（HTTP/TCP/Exec）
│   ├── cluster/            # Gossip 成员发现（memberlist）
│   ├── rpc/                # Agent 间 mTLS RPC
│   ├── control/            # Raft 控制面（用户/RBAC/准入）
│   ├── auth/               # 认证（密码/session/token）
│   ├── api/                # ConnectRPC API 服务
│   ├── agent/              # Agent 主控逻辑
│   └── ...
├── proto/
│   ├── procmesh/v1/        # 对外 API protobuf
│   └── shim/v1/            # Shim 协议 protobuf
├── web/
│   ├── src/
│   │   ├── components/     # Vue 组件
│   │   ├── pages/          # 页面组件
│   │   ├── lib/            # Composables 和工具函数
│   │   └── types/          # TypeScript 类型
│   ├── public/locales/     # i18n 翻译文件（en/zh）
│   └── tests/              # Vitest + Playwright 测试
└── docs/
    ├── v2-prd/             # 产品需求文档
    └── superpowers/
        ├── specs/          # 架构设计文档
        └── plans/          # 分阶段实施计划（P0-P5）
```

### 进程状态机

```
Desired: RUNNING | STOPPED

Observed:
STOPPED ─start→ STARTING ─ok→ RUNNING ─exit→ EXITED
                    │                         │
                    └─fail→ BACKOFF ←─────────┘
                               │
                               └─exhausted→ FATAL
```

**重启策略：** `never` | `always` | `on-failure`  
**健康检查：** 独立于 Observed，支持 `RUNNING/UNHEALTHY` 状态

### 配置版本控制

- 每次修改生成递增 `revision`
- 维护 `latest_revision`（最新配置）和 `active_revision`（运行中配置）
- 更新时必须提供 `expected_revision`，不匹配返回 409 Conflict（CAS）
- Rollback 产生新 revision，不删除历史

### 远程操作流程

```
Browser → Agent A :18680
        → 入口 RBAC 检查
        → mTLS RPC → Owner Agent C :18683
        → Owner 再次验证 RBAC + 证书
        → operation_id 幂等去重
        → 本地 commit + 执行
        → 返回结果
```

**要点：**
- 所有 Mutation 必须携带 `operation_id`（UUID）
- Owner Agent 不信任入口 Agent 的授权声明，必须独立验证
- 目标端根据 `operation_id` 实现幂等

## 代码约定

### Go 代码风格

1. **包依赖方向：**
   - `api` → `auth` / `process` / `rpc` / `control`
   - `process` → `shim` / `store` / `health` / `logmgr`
   - `cluster` 与 `process` 只交换 summary DTO
   - **禁止** `process` import `cluster` 或 `control`

2. **错误处理：**
   - 使用 `internal/errcode` 统一错误码
   - 重要错误必须包装上下文：`fmt.Errorf("operation failed: %w", err)`

3. **测试要求：**
   - 强制 TDD：先写测试（RED），再实现（GREEN），后重构
   - 核心包（process/shim/store/control/auth）最低 80% 覆盖率
   - 集成测试必须覆盖：CAS 冲突、幂等重试、失 quorum 拒绝写

4. **状态修改：**
   - Process Spec 修改必须通过 `Manager.ApplySpec`，不得直接写 Store
   - Instance 状态更新通过 `Manager` 的 reconcile 循环
   - 所有 Mutation 先写 Operation Journal，再执行

### 前端代码风格

1. **技术栈：**
   - Vue 3 Composition API + TypeScript
   - shadcn-vue（基于 Reka UI）
   - Tailwind CSS v4
   - TanStack Vue Query（服务端状态）
   - Pinia（客户端状态：auth/theme）

2. **国际化（i18n）：**
   - 使用 `useI18n` composable：`const { t } = useI18n()`
   - 翻译键格式：`namespace:section.key`（例如 `common:actions.start`）
   - 命名空间：`common`、`node`、`process`、`user`、`audit`
   - 新增翻译后运行 `npm run i18n:check` 检查完整性

3. **数据新鲜度：**
   - 所有来自 Gossip 的数据必须显示 `last_updated_at` 和新鲜度徽章
   - 新鲜度状态：`LIVE` | `STALE` | `UNKNOWN`
   - **禁止**把 STALE 数据显示为绿色"正常"状态

4. **ConnectRPC 集成：**
   - 客户端由 protobuf 自动生成：`web/src/gen/`
   - 使用 Vue Query 包装 RPC 调用，支持缓存和重试
   - Mutation 必须携带 `operationId`（前端生成 UUID）

## 平台差异

### macOS 开发注意事项

macOS 是**开发辅助平台**，不是对等生产环境：

- **无 cgroup**：`ResourceLimit` 被忽略，启动时会告警
- **run_as_user**：可能不可用或无权限，拒绝启动并返回明确错误
- **无 systemd**：Host reboot 恢复依赖如何拉起 Agent
- 测试：单测 + 不依赖 cgroup/setuid/systemd 的集成测试可运行

**生产环境：Linux only**

### 数据路径

- Linux 生产：`/var/lib/procmesh/`、`/etc/procmesh/agent.yaml`
- macOS 开发：`~/Library/Application Support/procmesh/`、`~/.config/procmesh/agent.yaml`

## 故障语义

### Agent 崩溃

- **Web/API 崩溃**：业务进程不受影响
- **procmesh-agent 崩溃**：已运行进程继续（由 shim 保护），Agent 恢复后重连
- **Cluster 网络故障**：本地进程不受影响，远程操作 TIMEOUT/UNAVAILABLE

### 网络分区

- 两侧本地进程继续正常运行
- 跨区操作返回 TIMEOUT/UNAVAILABLE
- **禁止**因对端 FAILED 而在本机创建对方的进程
- 愈合后只收敛 membership 和 summary，不用旧缓存覆盖 Owner 权威数据

### 磁盘保护

- `>85%`：告警（本地日志 + audit）
- `>90%`：积极删除旧日志
- `>95%`：停止新日志和 metrics 写入，保护 config/journal/audit/store/raft

### Local DB 损坏

- Agent 进入 `DEGRADED` 状态
- 停止高风险写操作
- **不得**主动杀死仍在运行的业务进程

## 实施阶段

V1.0 按 P0–P5 六个阶段交付，每阶段独立验收：

| 阶段 | 内容 | 可演示 |
|------|------|--------|
| P0 | 本地进程管理：状态机、shim、restart、health、CAS、日志 | 无集群时本机管理进程 |
| P1 | ConnectRPC + CLI 管本机 | `procmesh start/stop/logs` |
| P2 | cluster init、join token、memberlist、证书 | `node list` |
| P3 | mTLS RPC、Write-to-Owner、operation_id | 从 A 重启 C 的进程 |
| P4 | Raft、用户、RBAC、准入、CRL | 登录与权限生效 |
| P5 | Vue Web、集群视图、LIVE/STALE/UNKNOWN | 浏览器管理集群 |

**当前状态：** 参考 `docs/superpowers/plans/` 下的计划文档

## 关键文档

- **产品需求：** `docs/v2-prd/v2-prd.md`
- **架构设计：** `docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`
- **实施计划：** `docs/superpowers/plans/2026-08-13-v1-mvp.md`（索引）
- **前端 i18n：** `web/docs/I18N_GUIDE.md`

## 实施时禁止事项

1. 在非 Owner Agent 上修改 Process 权威数据
2. 用 Gossip 传递 Mutation（只能传摘要）
3. Agent stop/crash 时主动杀掉业务进程
4. 因对端 FAILED 而在本机创建对方的进程
5. 把 STALE 数据显示成实时健康状态
6. 无 `operation_id` 的远程写操作
7. 无 `expected_revision` 的配置写操作
8. 把 Process runtime/logs 写入 Raft（只存控制面数据）

## 开发工作流

1. **新功能开发：**
   - 先查阅 `docs/superpowers/specs/` 确认架构约束
   - 优先编写测试（TDD）
   - 确保 internal 核心包达到 80% 覆盖率

2. **修改 protobuf：**
   - 编辑 `proto/` 下的 .proto 文件
   - 运行 `make proto` 生成 Go 代码
   - 运行 `make proto-ts` 生成 TypeScript 客户端
   - 前端需要 `cd web && npm ci` 安装 protoc 插件

3. **前端开发：**
   - 使用 `npm run dev` 启动开发服务器
   - 新增 UI 文本必须加入 `public/locales/` 翻译文件
   - 运行 `npm run i18n:check` 确保翻译完整
   - E2E 测试覆盖关键用户流程

4. **集成调试：**
   - 使用 `internal/agent/p*_test.go` 中的集成测试
   - E2E 测试模拟多节点集群场景
   - 验证故障场景（见 PRD §92）
