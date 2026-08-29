# 灾备副本自行捕获并按路由复制 Implementation Plan

更新：2026-08-29（补充灾备页内恢复与 Peer 回源到 Owner）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让灾备副本在应用路由后按 cron（或手动）自己捕获源节点配置快照，再按路由复制到 Peer，不再要求用户先去备份页跑一次并粘贴主备份 `run_id`；并在灾备副本页内完成 Owner 恢复，Owner 本地副本丢失时可从 Peer 安全回源。

**Architecture:** `ReplicationPolicy` 只保留可选 cron + enabled + routes。一次 replication run 的每个源节点先用现有备份引擎打本地 `sink=replica` 快照（稳定 `snapshot_id`），再走现有 mTLS `PutSnapshot`。Raft 只存 run/task 元数据。调度用 `PreviousOrEqual` 命中当前 fire，但跳过 `fire <= ScheduleEpochUnix` 的补跑；同策略已有 `RUNNING` 则把该 fire 记为 `SKIPPED`。恢复请求从任意公开入口路由到 Owner：Owner 优先读本地 replica，本地丢失时经 mTLS 从选定 Peer 取回并 hydrate，最后逐进程 CAS apply；payload 不经过 Browser 或 Leader。

**Tech Stack:** Go、Raft FSM、ConnectRPC、现有 `backup.Engine` / Peer mTLS、Vue 3 + TanStack Query、i18n。

**Contract:** 会话中拍板的捕获/调度约定（Q1-B / Q2-B+手动 / Q3-B / Q4-B / Q5-A / Q6-B / Q7-A / Q8-A / Q9-A / Q10-A / Q11-A / Q12-A / Q13-A / Q14-A），以及 2026-08-29 补充的页面内 Prepare/Restore、Peer 回源和 Owner hydrate 合同。本计划取代 `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` §10.3 的旧 trigger 与跳转备份页模型；Owner 单写者和 Restore CAS 约束不变。

**Non-goals:** 不合并备份页；不写 S3/BackupPolicy；不把 Peer adopt 为新 Owner；不改进程 Owner 语义；不在应用策略时立刻打快照；Owner 不可达时不提供异地 apply。

---

## Locked Behavior

1. 应用拓扑只写策略，不创建 run。
2. 一键生成预填 `schedule_cron=0 2 * * *`、时区=浏览器 IANA、`enabled=true`；cron 可清空为仅手动。
3. 自动：仅 Leader；只跑 **策略写入之后** 的 cron fire。`enabled=false` 跳过 cron，手动仍可跑。
4. 手动 `StartRun`：立即捕获+复制，不要 `primary_run_id` / `snapshot_refs`。同策略已有 `RUNNING` 则返回该 run。
5. 捕获：源节点本地 process spec + revision history；`sink=replica` 持久落盘；产生 `snapshot_id`+checksum。不写 `BackupRuns`。
6. 复制：有快照后按路由 mTLS 传 Peer。Peer 不 apply。
7. 失败继续，run 可为 `PARTIAL`。
8. 重试失败任务：有快照只重传；无快照或源文件丢失则对该源重捕获。成功任务不动。
9. 同策略同时一个 `RUNNING`。cron 碰到运行中：ledger `SKIPPED`，不排队。
10. 保留：一组 `keep_last` / `keep_days` / `max_bytes`；源 `replica` 与 Peer 各自执行；进行中/恢复中/最后一份不删。
11. 去掉产品面 `trigger` / `primary_policy_ids` / 主备份 run_id。旧 `AFTER_PRIMARY_BACKUP` 不再跟跑备份页（视为仅手动，直到用户写 cron）。
12. 可恢复快照在灾备副本页内 Prepare/Restore，不跳转 `/backup`；恢复弹窗默认选中点击的快照。
13. 任何公开入口都按 `source_node_id` 把 Prepare/Restore 路由到 Owner；Owner 不可达只返回 `UNAVAILABLE` 并提示，Peer 永不 apply。
14. Owner 优先读取本地 `replica`；文件丢失时从 Prepare 选定的当前可达 Peer 经 mTLS `FetchSnapshot` 回源。
15. Peer 验证调用方证书为 source Owner，并按冻结任务校验 cluster/source/target/snapshot/checksum；Owner 收到后再次验证再 hydrate。
16. hydrate 对相同 checksum 幂等；同一 snapshot ID 的 checksum 或身份不同返回 `CONFLICT`，禁止覆盖。
17. hydrate 后只在 Owner 调用现有逐进程 `ApplySpec + expected_revision`；不存在进程使用 revision 0，CAS 冲突不自动重试。
18. Prepare/Restore 都要求 `backup.manage`，并在 Owner 重验用户 scope、目标进程身份和 revision；`replication.manage` 不能替代。
19. snapshot payload 只走选定 Peer -> Owner 的内部 mTLS，不经过 Browser、公开入口 Agent、Leader、Raft、Gossip 或 audit。

---

## File Map

| File | Responsibility |
|---|---|
| `internal/control/fsm.go` | 策略校验改为可选 cron；写入 `ScheduleEpochUnix`；去掉 trigger 强制；retry 允许空快照；新增 replication fire ledger |
| `internal/control/command.go` | `ReplicationPolicy` / put body 增加 `ScheduleEpochUnix`；`RetryFailedTasks` 语义保持但 FSM 放宽 |
| `internal/control/fsm_test.go` | 替换 trigger/primary 用例 |
| `internal/backup/engine.go` | `CaptureReplicationSnapshot`；`ReplicateSnapshot` 读 `sink=replica`；失败不丢 snapshot 身份；`HydrateReplica` 同 checksum 幂等导入，冲突不覆盖，hydrate 后复用 `Restore` |
| `internal/backup/replica_fs.go`（小文件，可内嵌 engine） | `replica` sink：`{data_dir}/backup/replica/{cluster_id}/{node_id}/{snapshot_id}.json` |
| `internal/agent/replication_scheduler.go` | 按 cron+epoch 建 **空快照** run；RUNNING 时 SKIPPED fire；不再读 `BackupRuns` |
| `internal/agent/replication_scheduler_test.go` | 重写自动调度测试 |
| `internal/backup/replication_coordinator.go` | 允许 dispatch 空 `SnapshotID` 任务（捕获阶段） |
| `internal/api/disaster_replication.go` | `StartRun` 自捕获；retry 含空快照；ListPolicies 计算下次运行文案字段（可选 `next_run_at`）；页面内 Prepare/Restore；路由 Owner；选择/拉取 Peer；Owner scope、checksum、CAS 与审计 |
| `internal/api/peer_replication.go` | mTLS-only `FetchSnapshot`；只向 source Owner 返回已授权且 checksum 匹配的 payload |
| `internal/backup/peer.go` | Peer payload 读取时重新验证 cluster/source/snapshot 身份和 checksum |
| `internal/api/rbac.go` | 新 Prepare/Restore hop 使用 `backup.manage`，Owner 端再次鉴权 |
| `internal/api/server.go`、`internal/agent/rpc.go` | 公开 Owner 路由与内部 Peer mTLS client/handler 接线；payload 不走公开入口 |
| `proto/procmesh/v1/api.proto` | 只追加 Prepare/Restore 与内部 Fetch RPC/messages，保留既有字段号 |
| `internal/api/disaster_replication_test.go` | 替换 MANUAL/`primary_run_id` 测试 |
| `web/src/pages/DisasterReplicaPage.vue` | 预览编辑 cron/tz/enabled；去掉主备份 run id；页面内恢复弹窗、快照默认选择、Owner/Peer/CAS 结果 |
| `web/src/pages/DisasterReplicaPage.test.ts` | 调度与页面内恢复 UI 测试 |
| `web/public/locales/{en,zh}/common.json` | 文案 |
| `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` | §10.1 / §10.3 / §12-15 / §18 与本计划对齐 |

Proto `trigger` / `primary_run_id` / `snapshot_refs` **保留字段号**（兼容旧客户端），服务端忽略写入、拒绝再作为捕获前置。本计划默认 **不改 `.proto` 字段号**；若要在 ListPolicies 回 `next_run_at`，再追加 optional `int64 next_run_at = 22` 并 `make proto && make proto-ts`。推荐追加，预览端也可先用 cron 字符串。

---

### Task 1: FSM — 可选 cron、ScheduleEpoch、去掉 trigger 强制

**Files:**
- Modify: `internal/control/fsm.go`（`ReplicationPolicy`、`applyReplicationPolicyPut`、`applyRetryFailedTasks`）
- Modify: `internal/control/command.go`（put body）
- Test: `internal/control/fsm_test.go`

- [ ] **Step 1: Write failing tests**

在 `fsm_test.go` 增加/改写：

```go
func TestFSM_ReplicationPolicyPut_OptionalCronAndEpoch(t *testing.T) {
	s := admittedReplicationState(t)
	now := time.Unix(1_800_000_000, 0) // 不是 02:00
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-1", PolicyID: "rp-1", Name: "cluster-replica",
		Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}},
		ScheduleCron: "0 2 * * *", Timezone: "UTC", ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), now); err != nil {
		t.Fatal(err)
	}
	got := s.ReplicationPolicies["rp-1"]
	if got.ScheduleCron != "0 2 * * *" || got.Timezone != "UTC" {
		t.Fatalf("schedule=%q tz=%q", got.ScheduleCron, got.Timezone)
	}
	if got.Trigger != "" || len(got.PrimaryPolicyIDs) != 0 {
		t.Fatalf("legacy fields must be cleared: %+v", got)
	}
	if got.ScheduleEpochUnix != now.Unix() {
		t.Fatalf("epoch=%d want %d", got.ScheduleEpochUnix, now.Unix())
	}
}

func TestFSM_ReplicationPolicyPut_EmptyCronIsManualOnly(t *testing.T) {
	s := admittedReplicationState(t)
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-2", PolicyID: "rp-manual", Name: "manual-only",
		Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}},
		ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestFSM_ReplicationPolicyPut_RejectsBadCronEvenWithoutTrigger(t *testing.T) {
	s := admittedReplicationState(t)
	body := control.ReplicationPolicyPutBody{
		OperationID: "op-3", PolicyID: "rp-bad", Name: "bad",
		Enabled: true, SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "node-a", TargetNodeIDs: []string{"node-b"}}},
		ScheduleCron: "0 * *", Timezone: "UTC", ExpectedRevision: -1,
	}
	if err := s.Apply(mustEncode(t, control.CmdReplicationPolicyPut, body), time.Unix(1, 0)); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
}

func TestFSM_RetryFailedReplicationTasks_AllowsMissingSnapshot(t *testing.T) {
	s := admittedReplicationState(t)
	run := control.ClusterBackupRun{RunID: "run-1", PolicyID: "rp-1", Status: "PARTIAL"}
	if err := s.CreateRun(control.CreateRunBody{OperationID: "op-c", LeaderTerm: 3, Replication: true, Run: run, Tasks: []control.ClusterBackupTask{
		{RunID: "run-1", TaskID: "t-copy", SourceNodeID: "node-a", NodeID: "node-b", SnapshotID: "snap", SHA256: strings.Repeat("a", 64), Status: "FAILED"},
		{RunID: "run-1", TaskID: "t-cap", SourceNodeID: "node-a", NodeID: "node-c", Status: "FAILED"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryFailedTasks(control.RetryFailedTasksBody{OperationID: "op-retry", RunID: "run-1", LeaderTerm: 3, UpdatedUnix: 100, LeaseUntilUnix: 130, Replication: true}); err != nil {
		t.Fatal(err)
	}
	if s.ReplicationTasks["run-1:t-cap"].Status != "PENDING" {
		t.Fatalf("capture-failed task not retried: %+v", s.ReplicationTasks["run-1:t-cap"])
	}
	if s.ReplicationTasks["run-1:t-copy"].SnapshotID != "snap" {
		t.Fatal("copy-failed task must keep snapshot_id")
	}
}
```

把现有 `Trigger: "MANUAL"` 成功用例改成不填 trigger。删除或改写这些应失败/应成功的旧断言：

- `invalid trigger` / `scheduled trigger missing schedule`（空 cron 改为合法）
- `after primary missing policies` 等：新语义下 `AFTER_PRIMARY_BACKUP` **可以写入**，但 FSM **清空** `Trigger` 与 `PrimaryPolicyIDs`（升级为仅手动）。不要因为旧字段拒绝应用，否则线上已有策略无法再保存。

- [ ] **Step 2: Run RED**

```bash
go test ./internal/control -run 'TestFSM_ReplicationPolicyPut_|TestFSM_RetryFailedReplicationTasks_' -count=1
```

Expected: FAIL（`ScheduleEpochUnix` 不存在，retry 仍跳过空快照）。

- [ ] **Step 3: Minimal FSM change**

`ReplicationPolicy` 增加：

```go
ScheduleEpochUnix int64 `json:"schedule_epoch_unix,omitempty"`
```

`applyReplicationPolicyPut`：

- 删除 `validReplicationTrigger` 强制。无论 body.Trigger 是什么，**存空**；`PrimaryPolicyIDs` **存空切片**。
- cron 空：合法（仅手动）。cron 非空：走现有 `validatePolicySchedule`。
- `CmdReplicationPolicyPut` 的 Apply 传入 `now`：

```go
case CmdReplicationPolicyPut:
	return applyJSON(cmd.Body, func(b ReplicationPolicyPutBody) error {
		return s.applyReplicationPolicyPut(b, now)
	})
```

- 每次成功 put：`cur.ScheduleEpochUnix = now.Unix()`（cron/enabled 变或不变都更新。这样「刚应用」的当天已过 fire 一定 <= epoch）。
- `applyRetryFailedTasks`：去掉 `(b.Replication && (task.SnapshotID == "" || task.SHA256 == ""))` 跳过。replication 失败任务一律回到 `PENDING`；**不要清空已有 SnapshotID/SHA256**（复制失败要保留，捕获失败本来就是空）。

- [ ] **Step 4: Run tests and commit**

```bash
go test ./internal/control -run 'TestFSM_Replication' -count=1
go test ./internal/control -count=1
```

```bash
git add internal/control
git commit -m "feat(replication): drop trigger; optional cron and schedule epoch"
```

---

### Task 2: 本地 `replica` 快照捕获

**Files:**
- Modify: `internal/backup/engine.go`
- Create or extend: `internal/backup/engine_replica_test.go`
- Modify: `internal/backup/replication_coordinator.go` 仅当 dispatcher 仍要求非空 snapshot 时放宽

- [ ] **Step 1: Write failing engine tests**

```go
func TestEngine_CaptureReplicationSnapshot_IdempotentPerRunAndSource(t *testing.T) {
	eng := testEngineWithProcess(t) // 现有测试夹具：一个本地 spec
	id := backup.StableReplicationSnapshotID("run-1", "node-a")
	first, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: id,
	})
	if err != nil || first.SnapshotID != id || first.SHA256 == "" || first.Sink != "replica" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID, SnapshotID: id,
	})
	if err != nil || second.SHA256 != first.SHA256 || second.SnapshotID != first.SnapshotID {
		t.Fatalf("second=%+v", second)
	}
}

func TestEngine_ReplicateSnapshot_ReadsReplicaSink(t *testing.T) {
	eng := testEngineWithProcessAndPeerPush(t)
	cap, err := eng.CaptureReplicationSnapshot(context.Background(), backup.ReplicationCaptureRequest{
		RunID: "run-1", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		SnapshotID: backup.StableReplicationSnapshotID("run-1", eng.NodeID),
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := eng.ReplicateSnapshot(context.Background(), backup.ReplicationTaskRequest{
		RunID: "run-1", TaskID: "t1", PolicyID: "rp-1", SourceNodeID: eng.NodeID,
		TargetNodeID: "peer-b", SnapshotID: cap.SnapshotID, SHA256: cap.SHA256,
	})
	if err != nil || n <= 0 {
		t.Fatalf("bytes=%d err=%v", n, err)
	}
}
```

`StableReplicationSnapshotID`：对 `run_id + "\x00" + source_node_id` 做 sha256，取 hex 前 32 字节（或 UUID v5 风格）。同一 run 的多条路由必须共享这一份快照。

- [ ] **Step 2: Run RED**

```bash
go test ./internal/backup -run 'TestEngine_CaptureReplicationSnapshot_|TestEngine_ReplicateSnapshot_ReadsReplicaSink' -count=1
```

Expected: FAIL（方法不存在）。

- [ ] **Step 3: Implement capture + replica sink**

```go
const ReplicaSinkName = "replica"

type ReplicationCaptureRequest struct {
	RunID, PolicyID, SourceNodeID, SnapshotID string
}

func StableReplicationSnapshotID(runID, sourceNodeID string) string {
	sum := sha256.Sum256([]byte("replica-snap\x00" + runID + "\x00" + sourceNodeID))
	return hex.EncodeToString(sum[:16])
}

func (e *Engine) CaptureReplicationSnapshot(ctx context.Context, req ReplicationCaptureRequest) (Meta, error) {
	if req.SourceNodeID != e.NodeID {
		return Meta{}, errcode.E(errcode.INVALID, "capture source mismatch")
	}
	if req.SnapshotID == "" {
		req.SnapshotID = StableReplicationSnapshotID(req.RunID, req.SourceNodeID)
	}
	return e.CreateCluster(ctx, ClusterCreateOpts{
		RunID:      req.RunID,
		TaskID:     "capture:" + req.SourceNodeID,
		PolicyID:   req.PolicyID,
		ClusterID:  e.resolvedClusterID(),
		NodeID:     e.NodeID,
		Sink:       ReplicaSinkName,
		SnapshotID: req.SnapshotID,
	})
}
```

`clusterSink("replica", "")` 必须返回本地 FS，路径：

```text
{data_dir}/backup/replica/{cluster_id}/{node_id}/{snapshot_id}.json
```

不要用集群备份的 FS profile，避免和 Backup 页快照混目录。

改 `ReplicateSnapshot`：用现有 `readPayload`（或按 `rec.Sink` 取 sink）读取，**不要**假设一定是 cluster FS/S3。checksum / cluster_id / node_id 校验保持。

磁盘 >=95% 时捕获失败（沿用 `CreateCluster`）。

- [ ] **Step 4: Run tests and commit**

```bash
go test ./internal/backup -run 'TestEngine_Capture|TestEngine_ReplicateSnapshot' -count=1
go test ./internal/backup -count=1
```

```bash
git add internal/backup
git commit -m "feat(replication): capture durable local replica snapshots"
```

---

### Task 3: 调度器 — 未来 fire、空快照 run、RUNNING 则 SKIPPED

**Files:**
- Modify: `internal/agent/replication_scheduler.go`
- Test: `internal/agent/replication_scheduler_test.go`
- Modify: `internal/control/fsm.go` 增加 `ReplicationFireLedger map[string]FireRecord`（若 Claim 走 Raft）

调度规则（不要用「只 Next、不用 PreviousOrEqual」——那会在整点永远打不中）：

```text
fire = PreviousOrEqualInTimezone(cron, tz, now)
if cron empty || !enabled || fire.IsZero(): skip
if fire.Unix() <= policy.ScheduleEpochUnix: skip          // 不补跑应用前的 slot
key = policyID + ":" + fire.Unix()
if ledger[key] exists: skip
if policy 已有 Status==RUNNING 的 ReplicationRun:
    写入 ledger[key] Status=SKIPPED
    不建 run
else:
    建 run（tasks 无 SnapshotID），ledger[key] Status=CLAIMED
```

- [ ] **Step 1: Write failing scheduler tests**（替换 `TestPlanAutomaticReplicationRunsAfterPrimary*` / `ScheduleWithoutPrimary*`）

```go
func TestPlanAutomaticReplicationRuns_SkipsFireAtOrBeforeEpoch(t *testing.T) {
	now := time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC) // 15:00
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: now.Unix(), Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("caught-up plans=%+v err=%v", plans, err)
	}
}

func TestPlanAutomaticReplicationRuns_FiresAfterEpoch(t *testing.T) {
	epoch := time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC)
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: epoch.Unix(), Revision: 1,
		Routes: []control.ReplicationRoute{{SourceNodeID: "source", TargetNodeIDs: []string{"target"}}},
	}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	task := plans[0].Create.Tasks[0]
	if task.SnapshotID != "" || task.SHA256 != "" || task.Status != "PENDING" {
		t.Fatalf("capture-pending task=%+v", task)
	}
}

func TestPlanAutomaticReplicationRuns_SkipsDisabledAndEmptyCron(t *testing.T) {
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["off"] = control.ReplicationPolicy{
		PolicyID: "off", Enabled: false, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		Routes: []control.ReplicationRoute{{SourceNodeID: "s", TargetNodeIDs: []string{"t"}}},
	}
	state.ReplicationPolicies["manual"] = control.ReplicationPolicy{
		PolicyID: "manual", Enabled: true,
		Routes: []control.ReplicationRoute{{SourceNodeID: "s", TargetNodeIDs: []string{"t"}}},
	}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("plans=%+v", plans)
	}
}

func TestPlanAutomaticReplicationRuns_SkipFireWhenPolicyRunning(t *testing.T) {
	epoch := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	now := time.Date(2027, 1, 16, 2, 0, 0, 0, time.UTC)
	state := control.NewState()
	state.ReplicationPolicies["rp"] = control.ReplicationPolicy{
		PolicyID: "rp", Enabled: true, ScheduleCron: "0 2 * * *", Timezone: "UTC",
		ScheduleEpochUnix: epoch.Unix(),
		Routes: []control.ReplicationRoute{{SourceNodeID: "s", TargetNodeIDs: []string{"t"}}},
	}
	state.ReplicationRuns["run-live"] = control.ClusterBackupRun{RunID: "run-live", PolicyID: "rp", Status: "RUNNING"}
	plans, err := planAutomaticReplicationRuns(*state, 1, now)
	if err != nil || len(plans) != 0 {
		t.Fatalf("plans=%+v, want skip", plans)
	}
	// 实现后：ClaimReplicationRuns 应写入 SKIPPED fire，随后 Tick 不再建 run
}
```

删掉所有「从 BackupRuns 取 snapshot」的自动调度测试。

- [ ] **Step 2: Run RED**

```bash
go test ./internal/agent -run 'TestPlanAutomaticReplicationRuns_' -count=1
```

- [ ] **Step 3: Implement `planAutomaticReplicationRuns`**

伪代码：

```go
func planAutomaticReplicationRuns(state control.State, term uint64, now time.Time) ([]automaticReplicationPlan, error) {
	// 只处理 Enabled && ScheduleCron != ""
	// fire := backup.PreviousOrEqualInTimezone(...)
	// if fire.Unix() <= policy.ScheduleEpochUnix { continue }
	// qualifier := strconv.FormatInt(fire.Unix(), 10)
	// if run already exists for automaticReplicationID("run", policyID, qualifier) { continue }
	// if policyHasRunning(state, policy.PolicyID) {
	//     // 由 ClaimReplicationRuns 写 SKIPPED ledger；plan 层返回 skip 标记或单独结构
	//     continue
	// }
	// buildAutomaticReplicationPlan(..., qualifier, emptySourceTask)
}

func emptySourceTask(string) control.ClusterBackupTask { return control.ClusterBackupTask{} }
```

`buildAutomaticReplicationPlan` 已有「无 snapshot 则 MissingTaskIDs」分支。**改掉**随后把 missing 标成 `SOURCE_SNAPSHOT_MISSING` 的逻辑（`applyMissingReplicationTask`）。空快照现在是合法捕获前状态，**不要**预失败。

`ClaimReplicationRuns`：

1. 对 SKIPPED fire：`CmdBackupFireClaim` 或新 command 写入 `ReplicationFireLedger[key]={Status:SKIPPED,...}`。
2. 对 CLAIMED：创建 run（空 snapshot tasks）+ ledger。
3. 继续现有 RUNNING lease takeover。

若不想新 command，可复用 `BackupFireLedger` 且 key 前缀 `replication:`。不要和集群备份 fire key 碰撞。

Coordinator `Tick` 已调用 `ClaimReplicationRuns`；放宽 `DispatchRun`：`SnapshotID==""` 的 PENDING 任务 **也要 dispatch**（捕获）。成功拷贝仍要求 checksum。

- [ ] **Step 4: Run tests and commit**

```bash
go test ./internal/agent -run 'TestPlanAutomaticReplicationRuns_|TestRunnableReplication' -count=1
go test ./internal/agent -count=1
go test ./internal/backup -run 'TestReplicationCoordinator' -count=1
```

```bash
git add internal/agent internal/control internal/backup
git commit -m "feat(replication): schedule capture runs on future cron fires"
```

---

### Task 4: Dispatch — 先捕获再复制；拷贝失败保留 snapshot_id

**Files:**
- Modify: `internal/agent/replication_scheduler.go`（`DispatchReplicationTask`）
- Modify: `internal/backup/replication_coordinator.go`（begin 允许空 snapshot，copy 开始前若已有 snapshot 则冻结）
- Test: `internal/agent/replication_scheduler_test.go` 或 `internal/backup/engine_replica_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestDispatchReplicationTask_CapturesThenCopies(t *testing.T) {
	// source == local node
	// task.SnapshotID empty
	// after dispatch: local replica snapshot exists AND peer Put 被调用
	// UpdateReplicationTask SUCCEEDED 带 SnapshotID+SHA256
}

func TestDispatchReplicationTask_CopyFailureKeepsSnapshot(t *testing.T) {
	// capture ok, PutSnapshot 返回 UNAVAILABLE
	// UpdateReplicationTask Status=UNAVAILABLE/FAILED 且 SnapshotID 非空
}

func TestDispatchReplicationTask_ReusesStableSnapshotForSecondRoute(t *testing.T) {
	// 同一 run 两条路由同一 source，第二次 Capture 幂等
}
```

- [ ] **Step 2: Run RED**

```bash
go test ./internal/agent -run 'TestDispatchReplicationTask_' -count=1
```

- [ ] **Step 3: Implement dispatch**

```go
func (d localReplicationDispatcher) DispatchReplicationTask(ctx context.Context, task backup.ReplicationTaskRequest) error {
	if task.SourceNodeID == d.runtime.nodeID {
		if task.SnapshotID == "" || task.SHA256 == "" {
			meta, err := d.runtime.backup.CaptureReplicationSnapshot(ctx, backup.ReplicationCaptureRequest{
				RunID: task.RunID, PolicyID: task.PolicyID, SourceNodeID: task.SourceNodeID,
				SnapshotID: backup.StableReplicationSnapshotID(task.RunID, task.SourceNodeID),
			})
			if err != nil {
				_ = (raftReplicationControl{runtime: d.runtime}).UpdateReplicationTask(ctx, failUpdate(task, err))
				return err
			}
			task.SnapshotID, task.SHA256 = meta.SnapshotID, meta.SHA256
		}
		bytes, err := d.runtime.backup.ReplicateSnapshot(ctx, task)
		status := "SUCCEEDED"
		if err != nil {
			return updateWithSnapshot(d, ctx, task, "FAILED", err) // 必须带上 SnapshotID
		}
		return updateSuccess(d, ctx, task, bytes, status)
	}
	// remote: 现有 PeerReplication.ReplicateSnapshot RPC
	// 扩展请求：SnapshotId/Sha256 可空；源 Agent 的 handler 走同一 Capture-then-copy
}
```

远程路径：现有 `ReplicateSnapshot` RPC 若要求非空 checksum，改为源侧允许空并在源上捕获。**不要**在 Leader 上捕获。

`BeginReplicationTask`：空 snapshot 的 PENDING→RUNNING 必须允许，否则捕获任务进不了 dispatch。拷贝阶段若已有 snapshot，begin 仍校验 identity。

- [ ] **Step 4: Run tests and commit**

```bash
go test ./internal/agent ./internal/backup ./internal/rpc -count=1
```

```bash
git add internal/agent internal/backup internal/rpc proto
git commit -m "feat(replication): capture on source then copy along routes"
```

若改了 proto `ReplicateSnapshotRequest` 注释（字段仍可空），需要 `make proto`。字段已存在则可只改服务端校验。

---

### Task 5: StartRun — 立即捕获+复制，无主备份绑定

**Files:**
- Modify: `internal/api/disaster_replication.go`
- Test: `internal/api/disaster_replication_test.go`

- [ ] **Step 1: Rewrite API tests**

替换：

- `TestDisasterReplicationAPI_StartRun`：不再传 `SnapshotRefs`；创建后 tasks 为 PENDING 且 SnapshotID 空；`DispatchRun` 被调用。
- 删除 `TestDisasterReplicationAPI_StartRun_ManualRequiresCompleteFrozenSnapshotRefs`。
- 删除 `TestDisasterReplicationAPI_StartRunRejectsNonManualPolicy`；改为 **有 cron 的策略也可以手动 StartRun**。
- 新增：

```go
func TestDisasterReplicationAPI_StartRun_ReturnsExistingRunning(t *testing.T) {
	// 策略已有 RUNNING run
	// 新 operation_id 的 StartRun 返回该 run_id，不新建
}

func TestDisasterReplicationAPI_StartRun_DisabledPolicyStillAllowed(t *testing.T) {
	// Enabled=false，有 routes → StartRun 成功
}

func TestDisasterReplicationAPI_StartRun_IgnoresPrimaryRunId(t *testing.T) {
	// 即使填了 primary_run_id 也不去 BackupRuns 绑定
	// tasks 仍为空 snapshot（自行捕获）
}
```

- [ ] **Step 2: Run RED**

```bash
go test ./internal/api -run 'TestDisasterReplicationAPI_StartRun' -count=1
```

- [ ] **Step 3: Implement StartRun**

删除：

- `policy.Trigger != "MANUAL"` 拒绝
- `resolveReplicationSnapshotRefs` 作为前置（可留函数但 StartRun 不调用）

新建 run：

```go
for _, route := range policy.Routes {
	for _, targetID := range route.TargetNodeIDs {
		tasks = append(tasks, control.ClusterBackupTask{
			RunID: runID, TaskID: replicationTaskID(runID, route.SourceNodeID, targetID),
			NodeID: targetID, SourceNodeID: route.SourceNodeID,
			Status: "PENDING", UpdatedUnix: now.Unix(),
		})
	}
}
```

在 Create 之前：

```go
for _, run := range st.ReplicationRuns {
	if run.PolicyID == policy.PolicyID && run.Status == "RUNNING" {
		return startRunResponse(run) // 审计 SUCCESS
	}
}
```

`operation_id` 幂等（同一 operation 已有 run）保持。禁用策略不拦截。无路由仍 `INVALID`。

Create 成功后 `DispatchRun` 带空 SnapshotID 任务。

- [ ] **Step 4: Run tests and commit**

```bash
go test ./internal/api -run 'TestDisasterReplicationAPI_' -count=1
go test ./internal/api -count=1
```

```bash
git add internal/api
git commit -m "feat(replication): StartRun captures instead of binding backup runs"
```

---

### Task 6: RetryFailedRoutes — 缺什么补什么

**Files:**
- Modify: `internal/api/disaster_replication.go`（`RetryFailedRoutes`）
- Test: `internal/api/disaster_replication_test.go`
- FSM 已在 Task 1 放宽

- [ ] **Step 1: Failing test**

```go
func TestDisasterReplicationAPI_RetryFailedRoutes_RecaptureAndCopy(t *testing.T) {
	// run PARTIAL
	// task A: FAILED + snapshot  → retry 后仍有 snapshot，PENDING
	// task B: FAILED + 无 snapshot → PENDING 空 snapshot
	// DispatchRun 包含两者
	// SUCCEEDED 任务不在 frozen 列表
}
```

- [ ] **Step 2: Run RED**

```bash
go test ./internal/api -run 'TestDisasterReplicationAPI_RetryFailedRoutes_RecaptureAndCopy' -count=1
```

- [ ] **Step 3: 修改 RetryFailedRoutes 的 retriedCount / Dispatch 过滤**

现在只 dispatch `PENDING && SnapshotID != ""`。改为：

```go
retryable := task.Status == "PENDING" && (/* 刚被 retry 命令复位 */)
// dispatch: PENDING && (有完整 snapshot 或 空 snapshot)
```

`retriedCount` 统计复位的失败任务，包括空快照。

源文件丢失：dispatch 捕获时 `GetBackup` NOT_FOUND → 再 `CaptureReplicationSnapshot`（同一稳定 ID 会新建；若旧 ID 冲突 checksum，用新 ID 并写回任务——优先：稳定 ID + CreateCluster 在记录缺失时重建）。

- [ ] **Step 4: Commit**

```bash
go test ./internal/api ./internal/control -count=1
git add internal/api internal/control
git commit -m "feat(replication): retry recaptures missing snapshots only"
```

---

### Task 7: 保留策略作用于 replica 本地 + Peer

**Files:**
- Modify: `internal/backup/retention.go` 或 replication retention planner
- Test: 现有 peer retention 测试旁增加 replica sink 组

- [ ] **Step 1: Failing test**

同一 `keep_last=1`：源节点两份 `sink=replica` 快照，成功捕获第二份后只留最新；Peer 上对应旧副本按现有 `ReplicationDeleteIntent` 删。源删除 **不** 要求 Peer 已删，Peer 删除 **不** 要求源还在。最后一份可用副本（源或 Peer 任一侧自己的最后一份）不删。

- [ ] **Step 2: Implement**

捕获成功路径（Task 4 末尾）对 `sink=replica` 调 `ApplyRetention`，policy 来自 replication policy 数字。Peer 侧保持现有 planner。`max_bytes` 同样分侧统计。

过滤：retention 只扫 `PolicyID == replication policy` 且 `Sink == replica` 的 index，不要删集群备份 FS/S3 对象。

- [ ] **Step 3: Commit**

```bash
go test ./internal/backup -run 'Retention|Replica' -count=1
git add internal/backup
git commit -m "feat(replication): retain local replica snapshots independently of peers"
```

---

### Task 8: Web 灾备页 — 调度可见、手动不再填 run_id

**Files:**
- Modify: `web/src/pages/DisasterReplicaPage.vue`
- Modify: `web/src/pages/DisasterReplicaPage.test.ts`
- Modify: `web/public/locales/en/common.json`、`web/public/locales/zh/common.json`
- Modify: `web/src/types/i18n.d.ts`（若由手写维护）

- [ ] **Step 1: Write failing page tests**

```ts
it("prefills cron and timezone when generating a draft", async () => {
  await wrapper.get('[data-action="generate"]').trigger("click");
  expect(replicationClient.generatePolicyDraft).toHaveBeenCalledWith(
    expect.objectContaining({
      scheduleCron: "0 2 * * *",
      timezone: expect.any(String),
      enabled: true,
    }),
  );
  // 预览可改 cron / timezone / enabled
  expect(wrapper.get('[data-field="schedule-cron"]').exists()).toBe(true);
});

it("shows manual-only when cron is cleared in preview", async () => {
  await wrapper.get('[data-action="generate"]').trigger("click");
  await wrapper.get('[data-field="schedule-cron"]').setValue("");
  expect(wrapper.get('[data-next-run]').text()).toContain("manual"); // i18n replica.manualOnly
});

it("starts a run without a primary backup run id", async () => {
  expect(wrapper.find('input[name="primaryRunId"]').exists()).toBe(false);
  await wrapper.get('[data-action="start-run"]').trigger("click");
  expect(replicationClient.startRun).toHaveBeenCalledWith(
    expect.objectContaining({ policyId: "rep-1" }),
  );
  const arg = replicationClient.startRun.mock.calls[0][0];
  expect(arg.primaryRunId ?? "").toBe("");
});

it("shows schedule disabled instead of next run when policy is disabled", async () => {
  // listPolicies 返回 enabled:false, scheduleCron:"0 2 * * *"
  expect(wrapper.get('[data-next-run]').text()).toMatch(/disabled/i);
});
```

- [ ] **Step 2: Run RED**

```bash
cd web && npx vitest run src/pages/DisasterReplicaPage.test.ts
```

- [ ] **Step 3: Implement UI**

`draftRequest()`：

```ts
trigger: "", // 不再发送 MANUAL
primaryPolicyIds: [],
scheduleCron: policy?.scheduleCron ?? "0 2 * * *",
timezone: policy?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
enabled: policy?.enabled ?? true,
```

预览 `dl` 增加可编辑：

- `input[data-field="schedule-cron"]`
- `input[data-field="timezone"]`
- `input[type=checkbox][data-field="enabled"]`
- `[data-next-run]`：`!draft.enabled` → `replica.scheduleDisabled`；空 cron → `replica.manualOnly`；否则 `` `${cron} (${tz})` ``（与备份页 `nextRunLabel` 同形，并加一句 `replica.nextRunHint`：「应用后不会立刻备份，到点才跑」）。

配置区同样展示当前策略的下次运行，禁用时不显示会到来的时间。

运行区：删除 `primaryRunId` input。`onStartRun` 只要求 `canManage && policyId`。按钮 `data-action="start-run"`。

i18n（en/zh 都必须有）：

| key | zh | en |
|---|---|---|
| `replica.schedule` | 调度 | Schedule |
| `replica.scheduleCron` | Cron | Cron |
| `replica.timezone` | 时区 | Timezone |
| `replica.nextRun` | 下次运行 | Next run |
| `replica.manualOnly` | 仅手动 | Manual only |
| `replica.scheduleDisabled` | 已禁用定时 | Schedule disabled |
| `replica.nextRunHint` | 应用配置后不会立刻备份，将在下次 cron 触发；现在备份请点启动运行。 | Applying the policy does not back up immediately. The next cron fire will. Use Start run to back up now. |
| `replica.startRun` | 立即备份并复制 | Capture and replicate now |

`replica.trigger` / `replica.primaryRunId` 可留着以免旧测试碎，页面不再绑定。

- [ ] **Step 4: i18n + tests + commit**

```bash
cd web && npx vitest run src/pages/DisasterReplicaPage.test.ts
cd web && npm run i18n:check
```

```bash
git add web/src/pages/DisasterReplicaPage.vue web/src/pages/DisasterReplicaPage.test.ts web/public/locales web/src/types/i18n.d.ts
git commit -m "feat(web): schedule replica capture from disaster-replica page"
```

---

### Task 9: 文档与交叉引用

**Files:**
- Modify: `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` §10.1、§10.3、§12.2、§12.3、§13.2、§14、§15、§18
- Modify: 本计划的 2026-08-29 灾备恢复增量合同与验收项

- [ ] **Step 1: 改 spec**

§10.1 `ReplicationPolicy`：删除 `trigger` / `primary_policy_ids`；增加 `schedule_epoch_unix`；`schedule_cron` 空=仅手动。

§10.3 整节替换为：

```text
自动：enabled 且 cron 非空时，Leader 在 fire > ScheduleEpochUnix 时创建 replication run。
每个源节点捕获本地 replica 快照，再按路由复制。不读取 ClusterBackupRun。
手动：StartRun 走同一流水线。enabled=false 仍允许手动。
同策略一个 RUNNING；冲突的 cron fire 记 SKIPPED。
不补跑应用前已过的 cron。
```

§13.2：预览必须可编辑 cron/时区/enabled；去掉「选择已有 ClusterBackupRun」。

文首修订日期最终更新为 2026-08-29，同时注明「灾备自行捕获」与「页面内恢复、Peer 回源」增量。

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md
git commit -m "docs(replication): replica runs capture snapshots themselves"
```

---

### Task 10: 回归与验收

- [ ] **Step 1: 包级测试**

```bash
go test ./internal/control ./internal/backup ./internal/api ./internal/agent -count=1
cd web && npx vitest run src/pages/DisasterReplicaPage.test.ts src/pages/BackupPage.test.ts
cd web && npm run i18n:check
```

备份页测试必须仍然通过（Peer 不出现在 sink 选择器；集群备份仍要 `run_id`）。

- [ ] **Step 2: 手工验收清单**（实现者在浏览器或 API 上勾）

1. 一键生成 → 预览看到 `0 2 * * *` 和时区 → 应用 → **没有**新 replication run。
2. 清空 cron 应用 → 显示仅手动；到点不跑。
3. 点「立即备份并复制」→ 源节点出现 `backup/replica/...json` → 目标 `backup/peer/...` → run SUCCEEDED/PARTIAL。
4. 不填任何主备份 ID。
5. 运行中再点手动 → 返回同一 run_id。
6. 禁用策略 → 无下次运行时间；手动仍能跑。
7. 下午应用每天 02:00 → 当天不跑；第二天 02:00 后出现 run。
8. 备份页跑一次集群备份 **不会** 自动引出灾备 run。
9. PARTIAL 不显示成成功；重试只动失败路由。
10. 可恢复快照在灾备页打开恢复弹窗，默认选中点击项，不跳转备份页。
11. Owner 本地 replica 存在时直接 Prepare/Restore，并逐进程 CAS apply。
12. 删除 Owner 本地 replica 后，选择仍持有副本的 Peer；payload 经 mTLS 回源并 hydrate 到 Owner 后恢复成功。
13. 停止 Owner 后从任意入口发起恢复，只看到 Owner 不可达提示，Peer 上没有 `ApplySpec` 副作用。
14. 构造同 snapshot ID、不同 checksum，返回 `CONFLICT`，Owner 本地对象不被覆盖。
15. 无 `backup.manage` 看不到恢复按钮；有权限时 Owner 仍重验 scope。Browser/Leader 响应、日志和审计中不出现 payload。

Linux 上再跑 `make test-acceptance` 中与 backup/replication 相关的部分（macOS 无 cgroup 也可跑多数 API/FSM 测试）。

- [ ] **Step 3: Final commit only if docs/tests leftover**

---

### Task 11: 灾备页内恢复与 Peer 回源

**Files:**

- Modify: `proto/procmesh/v1/api.proto`
- Modify: `internal/api/disaster_replication.go`、`internal/api/peer_replication.go`、`internal/api/rbac.go`
- Modify: `internal/api/server.go`、`internal/agent/rpc.go`
- Modify: `internal/backup/engine.go`、`internal/backup/peer.go`
- Modify: `web/src/pages/DisasterReplicaPage.vue`、对应测试和 i18n

**Public contract:**

- `PrepareRecoverableSnapshotRestore(source_node_id, snapshot_id, sha256, storage_node_id?)` 返回 Owner 当前 revision、快照 revision 和选定存储节点，不返回 payload。
- `RestoreRecoverableSnapshot(meta, source_node_id, snapshot_id, sha256, storage_node_id, targets)` 只在 Owner 执行；targets 必须逐项携带 `expected_revision`。
- 两个请求都可从任意公开 Agent 进入，但必须转发到 Owner。Owner 不可达返回 `UNAVAILABLE`，不得转给 Peer apply。

**Internal contract:**

- `PeerReplicationService.FetchSnapshot` 只监听 Agent mTLS 面。调用方证书必须等于 `source_node_id`，Raft 冻结任务必须证明该 Peer 持有相同 snapshot/checksum。
- Owner 本地副本优先；本地缺失时从选定 Peer 拉取，校验 cluster、Owner、snapshot ID 和 checksum 后调用 `HydrateReplica`。
- `HydrateReplica` 同 checksum 幂等；同 ID 不同 checksum/身份返回 `CONFLICT`。成功后复用现有 `Engine.Restore` 逐进程 CAS apply。
- payload 不进入 Browser、公开入口、Leader、Raft、Gossip、audit 或错误详情。

- [ ] **Step 1: Append Proto interfaces and regenerate**

只追加公开 Prepare/Restore 和内部 `FetchSnapshot`，保留所有既有字段号；运行 `make proto && make proto-ts`，不得手改生成文件。

- [ ] **Step 2: Implement Owner routing, Peer fetch, hydrate and CAS**

先写 Owner 本地优先、文件丢失回源、Owner 不可达、mTLS 非 Owner 拒绝、checksum 冲突不覆盖和 Peer 零 apply 的失败测试，再实现最小路径。所有鉴权与身份/checksum 校验必须发生在 apply 前。

- [ ] **Step 3: Replace Backup deep link with in-page restore dialog**

按钮只受 `backup.manage` 控制；弹窗默认选中点击快照，只切换同 Owner 快照，Prepare 成功后显示当前 revision，确认后直接 Restore 并保留逐进程结果。

- [ ] **Step 4: Run focused verification**

**Required verification:**

```bash
go test ./internal/backup -run 'HydrateReplica|Peer' -count=1
go test ./internal/api -run 'RecoverableSnapshotRestore|FetchSnapshot|Restore' -count=1
go test ./internal/agent -run 'PeerOperation|PeerReplication' -count=1
cd web && npx vitest run src/pages/DisasterReplicaPage.test.ts
cd web && npm run i18n:check
```

验收必须覆盖 Owner 本地优先、Owner 文件丢失后 Peer 回源、Owner 不可达、Peer 非 Owner 证书拒绝、checksum 冲突不覆盖、`backup.manage`/scope 拒绝、CAS 冲突以及 Peer 零 apply。

---

## Spec coverage

| 约定 | Task |
|---|---|
| 灾备自己捕获+按路由复制 | 2, 4, 5 |
| 自动=未来 cron；空 cron 仅手动 | 1, 3 |
| 生成预填 02:00 + 浏览器时区 | 8 |
| 手动立即捕获，无 run_id | 5, 8 |
| 源本地 replica FS + Peer | 2, 4 |
| PARTIAL | 已有聚合 + 4 失败更新 |
| 断开 ClusterBackupRun | 3, 5 |
| 不补跑 | 1 epoch + 3 |
| RUNNING 跳过 cron | 3 |
| 重试缺什么补什么 | 1, 6 |
| 保留分侧执行 | 7 |
| 预览可改 cron/tz/enabled | 8 |
| 无 trigger | 1, 8, 9 |
| enabled 只关定时 | 3, 5, 8 |
| 备份页独立 | 10 回归 |
| 灾备页内 Prepare/Restore | 11 |
| Owner 本地 replica 优先 | 11 |
| Owner 丢失后 Peer mTLS 回源 + hydrate | 11 |
| Owner 不可达、Peer 永不 apply | 11 |
| `backup.manage` + Owner scope 重验 | 11 |
| payload 不经 Browser/Leader | 11 |
| 同 ID 不同 checksum=`CONFLICT` | 11 |
| Owner 逐进程 CAS 恢复 | 11，复用现有 restore |

## 实现时不要做的事

- 不要用 `NextInTimezone` 作为唯一 fire 计算（整点打不中）。
- 不要在 missing snapshot 时写 `SOURCE_SNAPSHOT_MISSING` 预失败。
- 不要把 replica 快照编进 `BackupRuns` 或备份页列表。
- 不要在 ApplyPolicyDraft 成功后自动 `StartRun`。
- 不要为了让旧 `AFTER_PRIMARY_BACKUP` 测试通过而继续读 `BackupRuns`。
- 不要让 Browser、公开入口或 Leader 中转 `FetchSnapshot` payload。
- 不要在 Owner 不可达或本地文件丢失时改到 Peer 上 `ApplySpec`。
- 不要把 `STALE` / `UNKNOWN` inventory 当作当前可读证明；Prepare/Restore 必须实时重验。
- 不要用相同 snapshot ID 覆盖不同 checksum 的 Owner 本地对象。

---

## Execution

Plan saved to `docs/plans/2026-08-28-disaster-replica-capture-and-replicate.md`.

Two execution options:

1. **Subagent-Driven (recommended)** — 每个 Task 一个新子代理，Task 之间审查
2. **Inline Execution** — 本会话按 executing-plans 逐 Task 推进，检查点停下来

Which approach?
