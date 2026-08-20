# 集群备份执行、FS/S3 与保留策略 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让每个目标 Agent 执行本地快照并写入集群命名空间 FS/S3，支持幂等重试、结果聚合、保留清理和现有 Q5 兼容。

**Architecture:** 扩展现有 `backup.Engine`，一次 task 生成一个 snapshot payload；FS/S3 sink 只负责各自存储语义。Leader/Coordinator 通过内部 Agent task RPC 调用本地执行，字节不经过 Leader；成功、失败和 retention 结果只回报元数据。

**Tech Stack:** 现有 `internal/backup`、SQLite backup index、ConnectRPC/mTLS、fake S3、Go `os`/`crypto/sha256`。

**Spec:** `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` §4、§8、§9、§11、§16。

## Global Constraints

- 集群 FS 必须使用 `{fs_dir}/{cluster_id}/{node_id}/{snapshot_id}.json`，每个 Agent 写本地目录。
- 旧 `{fs_dir}/{snapshot_id}.json` 和旧 S3 key 继续可读；新 run 使用新 namespace。
- FS 文件 `0600`、目录 `0750`，临时写入后 `fsync + atomic rename`。
- S3 凭据只读取本地 profile/environment，不进入策略、API、Raft、audit 或日志。
- `run_id + node_id` 和 `run_id + task_id` 必须幂等；checksum 不同禁止覆盖。
- ≥95% 磁盘使用率返回 `DEGRADED`，不写 payload/index。
- 重试只针对失败 Agent；不能重新生成不同 snapshot 覆盖旧 checksum。

## File Map

- Modify: `internal/backup/types.go`、`internal/backup/engine.go`、`internal/backup/fs.go`、`internal/backup/s3.go`
- Create: `internal/backup/cluster_sink.go`、`internal/backup/retention.go`、对应测试
- Modify: `internal/store/backup.go`、`internal/store/schema.sql`、`internal/paths/paths.go`
- Create: `internal/api/cluster_backup_agent.go`、`internal/api/cluster_backup_agent_test.go`
- Modify: `internal/agent/rpc.go`、`internal/agent/run.go`
- Modify: `internal/api/backup.go` 和现有 Q5 测试

### Task 1: Namespace paths and idempotent Engine task

**Interfaces:**

```go
type ClusterCreateOpts struct {
    RunID, TaskID, PolicyID, ClusterID, NodeID string
    Sink, DestinationProfile string
    ProcessIDs []string
}
func (e *Engine) CreateCluster(ctx context.Context, opts ClusterCreateOpts) (Meta, error)
```

- [ ] **Step 1: Write failing path/idempotency tests**

测试 FS path、S3 key、重复 task 返回已有 Meta、相同 snapshot ID 不同 checksum 返回 `CONFLICT`、旧 `Create` 行为不变。

```go
meta1, err := e.CreateCluster(ctx, backup.ClusterCreateOpts{RunID: "run-1", TaskID: "task-a", Sink: "fs", ClusterID: "c1", NodeID: "n1"})
meta2, err := e.CreateCluster(ctx, backup.ClusterCreateOpts{RunID: "run-1", TaskID: "task-a", Sink: "fs", ClusterID: "c1", NodeID: "n1"})
if err != nil || meta1.SHA256 != meta2.SHA256 { t.Fatalf("not idempotent") }
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/backup -run 'TestClusterPath|TestEngine_CreateCluster' -count=1`

Expected: FAIL because the new options and namespace writers do not exist.

- [ ] **Step 3: Implement namespace-aware sinks**

FS 使用 `filepath.Join(fsDir, clusterID, nodeID, snapshotID+".json")`；S3 使用 `{prefix}/{clusterID}/{policyID}/{nodeID}/{snapshotID}.json`。将 task identity 和 location 写入本地 `backup_index` 元数据，不写 payload。Engine 在写入前查询 task key，已成功且 checksum 相同直接返回；checksum 冲突返回 `CONFLICT`。

- [ ] **Step 4: Run focused and existing tests**

Run: `go test ./internal/backup -run 'TestClusterPath|TestEngine_CreateCluster|TestFSSink_|TestS3Sink_' -count=1`，再运行 `go test ./internal/backup -count=1`。

- [ ] **Step 5: Commit**

```bash
git add internal/backup internal/store internal/paths
git commit -m "feat(backup): add cluster snapshot namespaces and idempotency"
```

### Task 2: Local task RPC and result reporting

**Interfaces:** 内部 mTLS handler 提供 `RunClusterBackupTask` 和 `GetClusterBackupTask`；请求含 `run_id`、`task_id`、policy revision、sink/profile、目标 node ID，响应只含 Meta/状态/错误摘要。

- [ ] **Step 1: Write failing internal RPC tests**

测试：合法 Agent mTLS 可执行本地 task；cluster ID/node ID 不匹配拒绝；用户 Web token 不能调用 internal endpoint；目标 task 失败只返回该 task 错误；重复 task 返回原结果。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/api ./internal/rpc -run 'TestClusterBackupAgent_' -count=1`

Expected: FAIL because internal RPC and mTLS authorization are absent.

- [ ] **Step 3: Implement handler and dispatch adapter**

在 `internal/api/cluster_backup_agent.go` 添加本地执行 handler；在 `internal/agent/rpc.go` 的 local handler 注册；在 `internal/rpc/client.go` 增加 node-cert client 方法。Coordinator 的 `AgentDispatcher` 使用该 client，成功后调用 Leader control-plane `UpdateTask`。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api ./internal/rpc -run 'TestClusterBackupAgent_' -count=1`，再运行 `go test ./internal/api ./internal/rpc -count=1`。

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/rpc internal/agent proto
git commit -m "feat(backup): execute cluster tasks over internal mTLS rpc"
```

### Task 3: Aggregation, partial failure and retention

**Interfaces:** Coordinator 将本地结果映射为 `ClusterBackupTask`；`retention.Run(ctx, policy, sink)` 返回每个删除项的成功/失败状态。

- [ ] **Step 1: Write failing coordinator tests**

覆盖 3 个 Agent 中 2 个成功、1 个不可达时 run=`PARTIAL`；`FAIL_FAST` 在首个不可达后停止未开始任务但保留已完成结果；重试只 dispatch failed/timeout/unavailable；成功 task 不重复写 sink。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/backup -run 'TestCoordinator_Aggregates|TestCoordinator_RetryFailed|TestRetention_' -count=1`

Expected: FAIL because aggregation and retention are not connected.

- [ ] **Step 3: Implement result aggregation and deletion guards**

完成后按成功/非成功计数计算 run 终态；保留执行限制在 policy namespace/prefix；跳过 running/restoring/唯一可用副本；S3 删除失败返回 `RETENTION_FAILED`。staging 清理前检查 Peer 任务终态，后续重试从 primary sink 取回相同 checksum。

- [ ] **Step 4: Run tests and coverage**

Run: `go test ./internal/backup -run 'TestCoordinator_Aggregates|TestCoordinator_RetryFailed|TestRetention_' -count=1`，再运行 `go test ./internal/backup -coverprofile=/tmp/backup.out -count=1`。

- [ ] **Step 5: Commit**

```bash
git add internal/backup internal/api internal/control
git commit -m "feat(backup): aggregate cluster results and retain snapshots"
```

### Task 4: Compatibility and local schedule migration

- [ ] **Step 1: Write regression tests**

在 `internal/api/backup_test.go` 和 `internal/backup/schedule_test.go` 固定：旧 `CreateBackup` 仍是本机语义；旧 FS/S3 key 可读；旧 `sink=peer` 仍可调用；没有 ClusterBackupPolicy 时 `agent.yaml backup.schedule` 仍每分钟只触发一次本地 FS snapshot。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/api ./internal/backup -run 'TestBackupAPI_|TestTickSchedule_' -count=1`

Expected: 新 namespace/Coordinator 改动造成的兼容断言先失败。

- [ ] **Step 3: Implement compatibility adapters**

旧 API 继续调用 `Engine.Create`；旧 schedule 只在新控制面没有 enabled policy 时运行；旧 Peer 目录保持可读，新的 Peer namespace 在 P3 增加；旧 API 的权限和错误码不变。

- [ ] **Step 4: Run regression suite**

Run: `go test ./internal/api ./internal/backup -run 'TestBackupAPI_|TestTickSchedule_' -count=1`，再运行 `go test ./internal/api ./internal/backup -count=1`。

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/backup internal/agentcfg
git commit -m "fix(backup): preserve local backup compatibility"
```

## P2 验收

三 Agent 测试集群中，每个 Agent 只能写自己的 `{fs_dir}/{cluster_id}/{node_id}/`；S3 key 含 cluster/policy/node；一个不可达节点得到 `PARTIAL + UNAVAILABLE`；重复 Leader dispatch 不产生重复文件；保留策略不触碰其它 namespace。
