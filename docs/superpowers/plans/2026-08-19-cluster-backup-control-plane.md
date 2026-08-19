# 集群备份控制面与调度 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将集群备份/复制策略、运行状态和 scheduled fire ledger 纳入现有 Raft 控制面，并由当前 Leader 提供幂等调度和 ClusterBackupService。

**Architecture:** `internal/control` 保存不含载荷的 policy/run/task 元数据；`internal/backup` 通过接口消费这些记录，不 import `control`。所有写入经过现有 Raft `Node.Apply`，API 在非 Leader 时沿用现有 forward 路径。

**Tech Stack:** Go、HashiCorp Raft、ConnectRPC、现有 `internal/control.FSM` 和 `internal/api` 测试夹具。

**Spec:** `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` §5–§8、§12。

## Global Constraints

- Raft 只保存策略、运行小型元数据和 fire ledger；不保存快照 payload、完整 spec、S3 凭据或 backup index。
- `BackupPolicy` 与 `ReplicationPolicy` 的 revision 在运行开始时冻结。
- `fire_key = policy_id + scheduled_fire_unix`；同一 fire key 只能产生一个 `run_id`。
- Leader lease/term 不匹配的状态更新不得覆盖新 Leader 的结果。
- proto 只追加字段和 service，不修改已有 BackupService 字段号；生成文件由 `make proto` / `make proto-ts` 产生。
- 测试与实现同目录；每个任务先红后绿。

## File Map

- Modify: `internal/control/command.go`、`internal/control/fsm.go`、`internal/control/fsm_test.go`
- Create: `internal/schedule/cron.go`、`internal/schedule/cron_test.go`
- Create: `internal/backup/cluster_policy.go`、`internal/backup/cluster_policy_test.go`
- Create: `internal/backup/coordinator.go`、`internal/backup/coordinator_test.go`
- Modify: `internal/agent/run.go`、`internal/agent/run_test.go`
- Modify: `internal/api/server.go`、`internal/api/rbac.go`
- Create: `internal/api/cluster_backup.go`、`internal/api/cluster_backup_test.go`
- Modify: `proto/procmesh/v1/api.proto`
- Generate: `proto/procmesh/v1/api.pb.go`、`proto/procmesh/v1/procmeshv1connect/api.connect.go`、`web/src/gen/procmesh/v1/api_pb.ts`

### Task 1: Raft policy records and validation

**Interfaces:** `control.State` produces `BackupPolicies` and `ReplicationPolicies`; `backup.Policy` consumes their JSON-equivalent fields through explicit conversion functions.

- [ ] **Step 1: Write failing FSM tests**

在 `internal/control/fsm_test.go` 添加：保存有效 FS/S3 policy；拒绝空 name、非法 cron/timezone、未知 target selector、S3 缺 profile、`replica_factor <= 0`、自复制和重复 target；删除不存在 policy 返回 `NOT_FOUND`；nil map 经过 `EnsureForTest` 可安全读取。候选节点数量相关的上限校验留给 topology preview/apply，因为 FSM 保存时可能只知道 selector 而不知道当前成员集合。

```go
cmd, _ := control.EncodeCommand(control.CmdBackupPolicyPut, control.BackupPolicyPutBody{
    PolicyID: "bp-1", Name: "nightly", Enabled: true,
    ScheduleCron: "0 2 * * *", Timezone: "Asia/Shanghai",
    TargetSelector: "ALL_ADMITTED", Sink: "fs", RetentionKeepLast: 7,
})
if err := state.Apply(cmd, now); err != nil { t.Fatal(err) }
if got := state.BackupPolicies["bp-1"].Name; got != "nightly" { t.Fatalf("name=%q", got) }
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/control -run 'TestFSM_BackupPolicy|TestFSM_ReplicationPolicy' -count=1`

Expected: FAIL because commands, state maps and validators do not exist.

- [ ] **Step 3: Implement policy commands and state**

在 `command.go` 增加 `CmdBackupPolicyPut/Delete`、`CmdReplicationPolicyPut/Delete` 及 JSON body；在 `State` 增加 `BackupPolicies map[string]BackupPolicy`、`ReplicationPolicies map[string]ReplicationPolicy`，`ensure()` 初始化；`Apply` 分发到 `applyBackupPolicyPut/Delete` 和 `applyReplicationPolicyPut/Delete`。

验证函数必须复用 5 字段 cron 规则而不让 control import backup；将共享 cron/timezone 校验提取到无依赖 `internal/schedule`，由 backup 与 control 共用。

- [ ] **Step 4: Run focused and full control tests**

Run: `go test ./internal/control -run 'TestFSM_BackupPolicy|TestFSM_ReplicationPolicy' -count=1`，再运行 `go test ./internal/control -count=1`。

- [ ] **Step 5: Commit**

```bash
git add internal/control internal/schedule internal/backup/cluster_policy.go internal/backup/cluster_policy_test.go
git commit -m "feat(backup): persist cluster backup policies in raft"
```

### Task 2: Fire ledger and run/task metadata

**Interfaces:** `control.ClusterBackupRun`、`control.ClusterBackupTask`、`control.FireRecord` 只包含 ID、状态、时间、计数、checksum 和安全错误摘要；不包含 payload/path secrets。

```go
type FireRecord struct { FireKey, PolicyID, RunID string; ScheduledUnix, ClaimedUnix, LeaseUntilUnix int64; LeaderTerm uint64; Status string }
type ClusterBackupRun struct { RunID, PolicyID string; PolicyRevision int64; TargetNodeIDs []string; Status string; Success, Failed, Unavailable, Timeout int; CreatedUnix, StartedUnix, FinishedUnix int64 }
type ClusterBackupTask struct { RunID, TaskID, NodeID, SnapshotID, SHA256 string; Status string; Bytes int64; ErrorCode, ErrorSummary string; LeaderTerm uint64; UpdatedUnix int64 }
```

- [ ] **Step 1: Write failing idempotency tests**

覆盖：同一个 `fire_key` 两次 claim 返回同一 `run_id`；不同 policy/fire time 生成不同 run；过期 lease 可由新 term 接管；旧 term 更新被拒绝；task 成功后重复更新保持 checksum 和终态。

```go
first, created, err := state.ClaimFire(control.FireClaimBody{FireKey: "bp-1:1700000000", PolicyID: "bp-1", LeaderTerm: 3}, now)
if err != nil || !created { t.Fatal(err, created) }
second, created, err := state.ClaimFire(control.FireClaimBody{FireKey: "bp-1:1700000000", PolicyID: "bp-1", LeaderTerm: 4}, now.Add(time.Second))
if err != nil || created || second.RunID != first.RunID { t.Fatalf("%+v created=%v", second, created) }
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/control -run 'TestFSM_FireLedger|TestFSM_BackupRun' -count=1`

Expected: FAIL because ledger and run records are absent.

- [ ] **Step 3: Implement bounded Raft metadata**

增加 `BackupFireLedger map[string]FireRecord`、`BackupRuns map[string]ClusterBackupRun`、`BackupTasks map[string]ClusterBackupTask` 和 replication 对应 run/task map。实现 `ClaimFire`、`CreateRun`、`UpdateTask`、`FinishRun`；每次写入检查 lease/term，保留最近窗口由 `PruneRunMetadata(beforeUnix)` 清理，不触碰 sink 文件。

- [ ] **Step 4: Verify snapshot safety and tests**

在 `fsm_test.go` 对 `Snapshot()` JSON 做断言：不含 `payload`、`secret_key`、`access_key`、`backup_index`；运行 `go test ./internal/control -count=1`。

- [ ] **Step 5: Commit**

```bash
git add internal/control
git commit -m "feat(backup): add fire ledger and cluster run metadata"
```

### Task 3: ClusterBackupService protobuf and API

**Interfaces:** 新增 `ClusterBackupService`，包含 `CreatePolicy`、`UpdatePolicy`、`DeletePolicy`、`ListPolicies`、`ValidatePolicy`、`StartRun`、`GetRun`、`ListRuns`、`RetryFailedTasks`、`GetDestinationHealth`。

- [ ] **Step 1: Write failing API tests**

在 `internal/api/cluster_backup_test.go` 测试：无 `backup.manage` 拒绝写策略；非 Leader 请求被 forward；`StartRun` 冻结 target IDs 和 policy revision；`GetRun` 返回 per-Agent `UNAVAILABLE`；S3 secret 不出现在 proto response。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/api -run 'TestClusterBackupAPI_' -count=1`

Expected: FAIL because proto messages/service and handler do not exist.

- [ ] **Step 3: Append proto definitions and regenerate**

在 `api.proto` 追加 `ClusterBackupPolicy`、`ClusterBackupRun`、`ClusterBackupTask`、`CreateClusterBackupPolicyRequest`、`StartClusterBackupRunRequest` 等消息；追加 `service ClusterBackupService`，不改已有字段号。

```bash
make proto
make proto-ts
```

- [ ] **Step 4: Implement handler and registration**

新建 `internal/api/cluster_backup.go`，使用现有 auth interceptor、`backup.manage` / `backup.read`、`Router` 和 Raft `Node.Apply`。在 `internal/api/server.go` 和 `internal/agent/rpc.go` 注册 handler。所有非 Leader mutation 使用现有 forwarder；`ValidatePolicy` 只读目标成员和本地 profile 状态。

- [ ] **Step 5: Run API/proto tests**

Run: `go test ./internal/api -run 'TestClusterBackupAPI_|TestProtoGenerated' -count=1`，再运行 `go test ./internal/api -count=1`。

- [ ] **Step 6: Commit**

```bash
git add proto internal/api internal/agent web/src/gen
git commit -m "feat(backup): add cluster backup control api"
```

### Task 4: Leader scheduler and coordinator interface

**Interfaces:** `backup.Coordinator` 只依赖接口：

```go
type PolicyView struct { Policy Policy; Revision int64; TargetNodeIDs []string }
type BackupTaskRequest struct { RunID, TaskID, PolicyID, NodeID string; PolicyRevision int64; Sink, DestinationProfile string }
type TaskUpdate struct { RunID, TaskID, NodeID, Status, SnapshotID, SHA256 string; Bytes int64; ErrorCode, ErrorSummary string }
type ControlPlane interface {
    ListEnabledBackupPolicies(ctx context.Context) ([]PolicyView, error)
    ClaimFire(ctx context.Context, key string, policyID string, term uint64, now time.Time) (runID string, claimed bool, err error)
    UpdateTask(ctx context.Context, update TaskUpdate) error
}
type AgentDispatcher interface {
    DispatchBackupTask(ctx context.Context, task BackupTaskRequest) error
}
```

- [ ] **Step 1: Write failing scheduler tests**

覆盖 timezone、`Next` 只返回严格晚于 `from` 的时间；同一分钟重复 tick 只 dispatch 一次；Follower 不 claim；Leader 变化后复用 run/task ID；策略 disabled 不产生 fire。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/backup -run 'TestCoordinator_|TestClusterSchedule_' -count=1`

Expected: FAIL because coordinator and timezone-aware schedule are absent.

- [ ] **Step 3: Implement coordinator**

新建 `internal/backup/coordinator.go` 和 `cluster_policy.go`：每次 tick 读取 Leader term、计算下一 fire、调用 `ClaimFire`、冻结 target set、按 `max_concurrency` dispatch；dispatch 错误转为 task 状态，不让一个 Agent 失败中断其它 target。

- [ ] **Step 4: Wire Agent runtime**

在 `internal/agent/run.go` 启动/停止 coordinator；保留现有本地 `TickSchedule` 作为兼容路径，仅当没有启用 Raft ClusterBackupPolicy 时执行。Leader 变化由 coordinator 每分钟重新读取 term，不依赖旧 Leader 的 goroutine 状态。

- [ ] **Step 5: Run tests**

Run: `go test ./internal/backup ./internal/agent -run 'TestCoordinator_|TestClusterSchedule_|TestAgentBackupScheduler' -count=1`，再运行 `go test ./internal/backup ./internal/agent -count=1`。

- [ ] **Step 6: Commit**

```bash
git add internal/backup internal/agent internal/control internal/api
git commit -m "feat(backup): schedule cluster runs from raft leader"
```

## P1 验收

```bash
go test ./internal/control ./internal/backup ./internal/api ./internal/agent -count=1
```

验收点：策略和 fire ledger 经过 Raft snapshot/restore 保留；同一 scheduled fire 只有一个 run；API 不泄露凭据；Follower 不调度；目标集合在 run 创建时冻结。
