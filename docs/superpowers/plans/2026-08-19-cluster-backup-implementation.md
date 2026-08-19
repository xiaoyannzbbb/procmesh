# ProcMesh 集群备份与灾备副本实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在保留现有单 Agent Q5 备份兼容性的前提下，交付集群级 FS/S3 定时备份、独立 Peer 灾备副本页面和一键副本拓扑生成。

**Architecture:** 集群备份策略、运行元数据、scheduled fire ledger 和灾备复制策略进入 Raft；快照 payload、S3 凭据和本地 `backup_index` 仍留在 Agent/sink。当前 Leader 执行唯一调度，Agent 直接写 FS/S3 或通过 mTLS 把快照传给 Peer，Web 通过两个独立 service 展示策略、运行和恢复状态。

**Tech Stack:** Go、HashiCorp Raft、SQLite、ConnectRPC、Vue 3、Vue Query、Vitest、现有 mTLS Agent RPC、fake S3 测试服务器。

**Spec:** `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md`

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`。
- 强制 TDD：每个任务先写失败测试，再写最小实现。
- `process` 不得 import `cluster`、`control`、`rpc`、`auth`、`web` 或 `backup`。
- Raft 只保存策略、运行小型元数据和 fire ledger；禁止保存 snapshot payload、完整 spec、S3 凭据和本地 backup index。
- Gossip 不携带备份载荷、复制载荷或任务进度。
- 快照副本不是 writer；Restore 只能到 Owner，并通过 `ApplySpec + expected_revision` 生成新 revision。
- 所有控制面 mutation 带非空 `operation_id`；所有跨 Agent 任务带稳定 `run_id`、`task_id` 和 checksum。
- S3 access key/secret key 不进入 Raft、API response、audit payload 或日志。
- proto 生成文件禁止手改；修改 `api.proto` 后运行 `make proto` 和 `make proto-ts`。
- 文档与计划使用中文；API 错误消息使用英文；Web 文案同步 `web/public/locales/{en,zh}`。
- 保留现有 `backup.read` / `backup.manage`；新增 `replication.read` / `replication.manage`。
- 保留现有 `BackupService`、`agent.yaml backup.schedule` 和 `sink=peer` 的兼容语义。
- 状态必须保留 `LIVE` / `STALE` / `UNKNOWN`，不可达不能显示成空列表。
- 每个阶段完成后运行该阶段列出的测试，再进入下一阶段。

## 执行顺序

| 阶段 | 计划 | 完成后的可用能力 |
|------|------|------------------|
| P1 | [2026-08-19-cluster-backup-control-plane.md](./2026-08-19-cluster-backup-control-plane.md) | Raft 策略、运行元数据、fire ledger、Leader 调度和 ClusterBackupService |
| P2 | [2026-08-19-cluster-backup-execution.md](./2026-08-19-cluster-backup-execution.md) | Agent 扇出、集群 FS/S3 路径、幂等、聚合、保留和兼容迁移 |
| P3 | [2026-08-19-disaster-replication.md](./2026-08-19-disaster-replication.md) | Peer 复制、checksum、失败重试、自动拓扑生成和复制 service |
| P4 | [2026-08-19-cluster-backup-web.md](./2026-08-19-cluster-backup-web.md) | `/backup` 集群策略与运行页、`/disaster-replica` 灾备副本页 |
| P5 | [2026-08-19-cluster-backup-integration.md](./2026-08-19-cluster-backup-integration.md) | Leader 故障转移、权限审计、指标、迁移和全量验收 |

P1 → P2 → P3 → P4 → P5 严格串行。P2 不在 P1 的 fire ledger 和任务状态模型稳定前开始；P3 不重复实现 P2 的 snapshot 生成；P4 只消费已生成的 ConnectRPC/TS API；P5 负责跨阶段故障和兼容验收，不引入新的业务模型。

## 验收门槛

```bash
go test ./internal/control ./internal/backup ./internal/api ./internal/rpc -count=1
cd web && npm test -- --run
cd .. && git diff --check
```

集成阶段还必须运行：

```bash
go test ./internal/agent -run 'TestP5_|TestClusterBackup_|TestDisasterReplication_' -count=1 -timeout 240s
```
