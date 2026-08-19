# Peer 灾备副本 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Peer 从普通备份 sink 提升为独立灾备复制能力，支持稳定路由、mTLS 传输、checksum、幂等重试、保留和一键生成整个集群的合理副本配置。

**Architecture:** `ReplicationPolicy`/route/draft 由 Raft 管理；源 Agent 直接把主快照 bytes 发给目标 Agent；目标只接收并落盘，永不 apply。拓扑生成使用稳定环形分配，并在有故障域/容量标签时做反亲和与负载均衡。

**Tech Stack:** Go、ConnectRPC、现有 Agent mTLS、SQLite backup index、Vue API consumer（页面在 P4）。

**Spec:** `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` §10、§11、§12.2–§12.3。

## Global Constraints

- Peer 是独立复制策略，不出现在 `/backup` 的普通 FS/S3 sink 选择器。
- 目标 Agent 只接收、校验、原子落盘；禁止写 `process_specs`/`config_revisions`，禁止自动 adopt/apply。
- route/task key：`replication_policy_id + source_snapshot_id + target_node_id`。
- checksum 相同重复传输直接幂等成功；snapshot ID 相同但 checksum 不同返回 `CONFLICT`，禁止覆盖。
- 自动生成默认 `replica_factor=1`；N=1 只返回警告；N=2 生成互相复制；不默认全网状。
- 离线但已准入节点保留在 draft，显示健康警告；不因短暂离线自动修改配置。
- 新增 `replication.read` / `replication.manage`，恢复仍要求 `backup.manage`。

## File Map

- Modify: `internal/control/command.go`、`internal/control/fsm.go`、`internal/control/fsm_test.go`
- Create: `internal/backup/replication.go`、`internal/backup/topology.go`、对应测试
- Modify: `internal/backup/peer.go`、`internal/backup/peer_test.go`
- Modify: `proto/procmesh/v1/api.proto`
- Create: `internal/api/replication.go`、`internal/api/replication_test.go`
- Modify: `internal/agent/rpc.go`、`internal/rpc/client.go`、`internal/api/rbac.go`
- Generate: proto Go/TS files

### Task 1: Replication route semantics on the existing policy state

- [ ] **Step 1: Write failing FSM tests**

覆盖 P1 已保存的 replication policy route revision、source=target 拒绝、未知/撤销 Agent 拒绝、删除 policy 不删除已有副本、旧 policy revision 不能覆盖新 revision，以及候选节点不足时 apply draft 拒绝。

```go
body := control.ReplicationPolicyPutBody{
    PolicyID: "rp-1", Name: "cluster-dr", Enabled: true,
    SourceSelector: "ALL_ADMITTED", ReplicaFactor: 1,
    Routes: []control.ReplicationRoute{{SourceNodeID: "n1", TargetNodeIDs: []string{"n2"}}},
}
if err := state.Apply(mustCommand(t, control.CmdReplicationPolicyPut, body), now); err != nil { t.Fatal(err) }
if got := state.ReplicationPolicies["rp-1"].Routes[0].TargetNodeIDs[0]; got != "n2" { t.Fatal(got) }
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/control -run 'TestFSM_ReplicationPolicy' -count=1`

Expected: FAIL because policy model/commands are not present or validation is incomplete.

- [ ] **Step 3: Implement route-specific validation**

复用 P1 的 `ReplicationPolicy` state；在 route draft/apply 层校验 source/target 均为 `ADMITTED`、不得自复制、route target 不重复、factor 不超过当前候选集；保存时带 policy revision 和 draft hash，不能覆盖新 revision。

- [ ] **Step 4: Run tests and commit**

Run: `go test ./internal/control -run 'TestFSM_ReplicationPolicy' -count=1`，再运行 `go test ./internal/control -count=1`。

```bash
git add internal/control
git commit -m "feat(replication): validate peer route revisions"
```

### Task 2: Stable topology generator

**Interface:**

```go
type AgentTopology struct { NodeID, Host, Rack, Zone string; CapacityWeight float64; Admitted, Alive bool }
type RouteDraft struct { SourceNodeID string; TargetNodeIDs []string; Warnings []string }
func GenerateRoutes(nodes []AgentTopology, replicaFactor int, constraints TopologyConstraints) (RouteDraftResult, error)
```

- [ ] **Step 1: Write failing generator tests**

测试 N=1、N=2、3 节点环、replica factor=2、重复调用结果相同、source 不作为 target、不同 zone/rack 优先、无 topology labels 生成 warning、capacity weight 影响 inbound load。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/backup -run 'TestGenerateRoutes_' -count=1`

Expected: FAIL because generator is absent.

- [ ] **Step 3: Implement deterministic generator**

按 node ID 排序；默认选择环上后续节点；若满足 anti-affinity 的候选存在，优先不同 zone/rack/host；候选不足时降级并追加 warning；以 inbound route 数和 `CapacityWeight` 做稳定 tie-break；N=1 返回空 routes 和 `single-node-no-replica` warning，不返回内部错误。

- [ ] **Step 4: Run tests and commit**

Run: `go test ./internal/backup -run 'TestGenerateRoutes_' -count=1`，再运行 `go test ./internal/backup -count=1`。

```bash
git add internal/backup
git commit -m "feat(replication): generate deterministic cluster routes"
```

### Task 3: Peer transfer, verify and retry

- [ ] **Step 1: Write failing transfer tests**

覆盖临时文件 + checksum + atomic rename；目标已有相同 checksum 幂等；checksum 冲突拒绝覆盖；接收副本不会创建进程；源目标 cluster ID 不匹配拒绝；失败 route 单独重试。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/backup ./internal/api -run 'TestPeer_|TestReplicationTask_' -count=1`

Expected: FAIL because new transfer metadata and namespace are absent.

- [ ] **Step 3: Implement transfer and internal mTLS service**

扩展 `PeerStore.Receive` 接收 `cluster_id`、`snapshot_id`、`sha256`、`run_id`、`task_id`；目标目录使用 `{data_dir}/backup/peer/{source_node_id}/{cluster_id}/{snapshot_id}.json`。在 `api.proto` 增加内部 `PeerReplicationService` 的 `PutSnapshot`、`CheckSnapshot`、`DeleteSnapshot`、`GetReplicaMetadata`，在 `internal/agent/rpc.go` local handler 注册，并在 `internal/rpc/client.go` 使用 Agent 证书调用。普通 Web session 不可命中这些方法。

- [ ] **Step 4: Run tests and commit**

Run: `go test ./internal/backup ./internal/api ./internal/rpc -run 'TestPeer_|TestReplicationTask_' -count=1`，再运行相关包全量测试。

```bash
git add internal/backup internal/api internal/rpc internal/agent proto
git commit -m "feat(replication): transfer peer snapshots with checksum"
```

### Task 4: DisasterReplicationService

- [ ] **Step 1: Write failing API tests**

覆盖 `GeneratePolicyDraft` 不写 Raft；返回 route、warnings、inbound load、topology health；`ApplyPolicyDraft` 需要 draft revision/hash；无 `replication.manage` 拒绝；`ListRecoverableSnapshots` 返回 source Owner 和 checksum；`VerifyReplica` 不执行 apply。

- [ ] **Step 2: Run RED**

Run: `go test ./internal/api -run 'TestReplicationAPI_' -count=1`

Expected: FAIL because service, proto and permission mapping are missing.

- [ ] **Step 3: Implement proto/API/permissions**

追加 `DisasterReplicationService` 与消息：`GetTopology`、`GeneratePolicyDraft`、`ApplyPolicyDraft`、`ListPolicies`、`GetPolicy`、`UpdatePolicy`、`DeletePolicy`、`StartRun`、`GetRun`、`ListRuns`、`RetryFailedRoutes`、`VerifyReplica`、`ListRecoverableSnapshots`；内部 `PeerReplicationService` 使用 spec 中的 `PutSnapshot`、`CheckSnapshot`、`DeleteSnapshot`、`GetReplicaMetadata`。在 `internal/auth/perm.go` 和 `internal/api/rbac.go` 加 `replication.read/manage`，audit 记录 policy/draft/run/task ID，不记录 payload。

```bash
make proto
make proto-ts
```

- [ ] **Step 4: Register and test**

在 `internal/api/server.go` 和 `internal/agent/rpc.go` 注册 handler；运行 `go test ./internal/api -run 'TestReplicationAPI_|TestProtoGenerated' -count=1`，再运行 `go test ./internal/api ./internal/auth -count=1`。

- [ ] **Step 5: Commit**

```bash
git add proto internal/api internal/auth internal/agent internal/rpc web/src/gen
git commit -m "feat(replication): expose disaster replication service"
```

## P3 验收

三 Agent 集群一键生成默认 factor=1 路由；预览显示故障域缺失警告；应用后可按 route 复制；目标节点只出现 Peer 文件和元数据；同 checksum 重试幂等；不同 checksum 冲突；Peer 恢复入口仍要求 Owner CAS。
