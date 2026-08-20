# 集群备份与灾备副本集成验收 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 P1–P4 交付后验证 Leader 故障转移、权限、审计、指标、兼容路径和恢复安全边界，形成可重复的集群级验收套件。

**Architecture:** 集成测试使用现有 in-memory Raft、Agent API test server、mTLS RPC fixture、fake S3 和临时 data directories；不把 payload 放入测试控制状态，也不通过 sleep 依赖不稳定的时序。

**Tech Stack:** Go test、in-memory Raft、ConnectRPC test client、fake S3、Vitest。

**Spec:** `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` §15–§18。

## Global Constraints

- 失败结果必须可见：`PARTIAL`、`UNAVAILABLE`、`CONFIG_MISSING`、`RETENTION_FAILED`。
- restore 只能到 Owner、必须 CAS、Peer 文件不能自动 apply。
- 审计不含 payload/secret；指标标签不能包含完整 URL 或凭据。
- 老 BackupService、本地 schedule、旧 FS/S3/Peer 路径必须继续通过回归测试。
- 不以“测试通过”替代检查：Raft snapshot JSON 必须显式断言无 payload/secret/index。

## File Map

- Create: `internal/agent/cluster_backup_accept_test.go`
- Modify: `internal/api/auditapi_test.go`、`internal/api/metrics_test.go`、`internal/agent/q5_accept_test.go`
- Modify: `internal/control/fsm_test.go`、`internal/backup/*_test.go`
- Create/modify: `web/e2e/cluster-backup.spec.ts`、`web/e2e/disaster-replica.spec.ts`
- Modify: `docs/superpowers/plans/2026-08-16-v1.1.md` 和相关 README/用户文档交叉引用

### Task 1: Three-Agent cluster backup acceptance

- [ ] **Step 1: Write failing end-to-end tests**

启动三个 in-memory Agent/Control nodes，给每个 Owner 写一个不同 spec；创建 ALL_ADMITTED FS policy，执行 run；断言每个本地目录只出现对应 node ID，run summary 为 3 success，snapshot metadata cluster/node/run 正确。

```go
run := startClusterBackupRun(t, "fs", "ALL_ADMITTED")
if run.Status != "SUCCEEDED" || run.Summary.Success != 3 { t.Fatalf("%+v", run) }
for _, node := range nodes {
    assertOnlyNodeNamespace(t, node.DataDir, clusterID, node.NodeID)
}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/agent -run 'TestClusterBackup_FS_' -count=1 -timeout 180s`

Expected: FAIL until P1/P2 are wired into the Agent runtime.

- [ ] **Step 3: Implement fixture helpers and fix integration wiring**

使用现有 `internal/agent` test bootstrap、真实 temporary dirs 和 fake clock；不要读取 Leader 内存中的 payload。修复只涉及依赖接线、状态上报和测试 fixture，不增加新业务模型。

- [ ] **Step 4: Run test**

Run: `go test ./internal/agent -run 'TestClusterBackup_FS_' -count=1 -timeout 180s`。

- [ ] **Step 5: Commit**

```bash
git add internal/agent internal/api internal/backup internal/control
git commit -m "test(backup): accept three-agent cluster fs backup"
```

### Task 2: Leader failover and duplicate prevention

- [ ] **Step 1: Write failing failover tests**

分别在 fire claim 后、一个 task 上传中、结果汇报前停止 Leader；新 Leader 必须复用原 run/task IDs，成功 task 不重复写文件，旧 term 更新不能覆盖终态。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/agent -run 'TestClusterBackup_LeaderFailover' -count=1 -timeout 240s`

Expected: FAIL if duplicate run or task is observed.

- [ ] **Step 3: Implement deterministic clock/lease test hooks**

让 coordinator 注入 `Now func() time.Time`、`LeaderTerm func() uint64` 和 dispatch hook；用 fake clock 推进 lease，不使用固定 sleep。修复 fire ledger、task CAS 或 coordinator resume 条件。

- [ ] **Step 4: Run test and commit**

Run: `go test ./internal/agent -run 'TestClusterBackup_LeaderFailover' -count=1 -timeout 240s`。

```bash
git add internal/backup internal/control internal/agent
git commit -m "test(backup): verify leader failover idempotency"
```

### Task 3: S3, Peer and restore safety acceptance

- [ ] **Step 1: Write failing tests**

覆盖 fake S3 profile、远端 key namespace、secret 不回显；Peer route 成功和 checksum 冲突；目标 Agent 的 Peer 文件不能创建/启动源进程；Owner restore 通过 CAS 产生新 revision；错误 expected revision 返回 `CONFLICT`。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/agent ./internal/api ./internal/backup -run 'TestClusterBackup_S3_|TestDisasterReplication_|TestRestore_' -count=1 -timeout 240s`

Expected: FAIL until all cross-package wiring is complete.

- [ ] **Step 3: Implement only integration fixes**

检查 mTLS route、source_node_id、Owner hop、checksum metadata、audit redaction 和 retention namespace；禁止通过修改 Restore 规则绕过现有 CAS。

- [ ] **Step 4: Run focused tests**

Run: `go test ./internal/agent ./internal/api ./internal/backup -run 'TestClusterBackup_S3_|TestDisasterReplication_|TestRestore_' -count=1 -timeout 240s`。

- [ ] **Step 5: Commit**

```bash
git add internal/agent internal/api internal/backup internal/rpc internal/control
git commit -m "test(backup): verify s3 peer and restore safety"
```

### Task 4: Permissions, audit, metrics and documentation

- [ ] **Step 1: Write failing assertions**

测试 Viewer 只能读；没有 `replication.manage` 不能生成/apply route；audit 事件包含 policy/run/task ID 但不含 secret/payload；metrics 包含 status/result labels；文档不再把整集群灾备列为当前非目标。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/auth ./internal/api -run 'Test.*Backup.*Permission|Test.*Replication.*Permission|Test.*Audit.*Backup' -count=1`

Expected: FAIL until permission mapping/audit/metrics hooks are connected.

- [ ] **Step 3: Implement hooks and documentation links**

更新 `internal/auth/perm.go`、`internal/api/rbac.go`、audit event builders、backup/replication counters；修正 `docs/superpowers/specs/2026-08-16-v1.1-architecture-design.md` 中旧的“整集群灾备暂不做”交叉引用，指向新 spec 和分阶段 plans。

- [ ] **Step 4: Run full verification**

```bash
go test ./internal/auth ./internal/control ./internal/backup ./internal/api ./internal/rpc ./internal/agent -count=1
cd web && npm run i18n:check && npm test -- --run && npm run build
cd .. && git diff --check
```

- [ ] **Step 5: Commit**

```bash
git add internal docs web
git commit -m "feat(backup): complete cluster backup disaster replication acceptance"
```

## P5 验收

```bash
go test ./internal/agent -run 'TestClusterBackup_|TestDisasterReplication_|TestRestore_' -count=1 -timeout 240s
go test ./...
cd web && npm test -- --run
```

验收结果必须同时证明：集群 FS 只提供逻辑覆盖而不宣称主机灾备；S3/Peer 可跨主机保存；Leader 变化不重复逻辑运行；Peer 永不直接 apply；Web 能显示 partial/stale/unavailable 并提供逐失败项重试。

