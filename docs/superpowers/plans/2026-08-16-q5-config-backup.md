# Q5 Configuration Backup / Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** 执行中

**Goal:** Owner 上的 Process Spec + 全量 `config_revisions` 可备份到 Filesystem / S3 Compatible / Peer；恢复只走 Owner `ApplySpec` + CAS 产生新 revision；Peer 文件永不被对端 apply；Backup 页 restore 确认强制展示 Owner + expected revision。

**Architecture:** `internal/backup` 负责 Snapshot 编解码、三 Sink、Restore 编排。`backup_index` 只存本机与已收 Peer 的元数据，载荷不进 Raft / Gossip。Create 永远快照**本机** Owner store。Restore 若本机不是 snapshot.node_id 则 hop 到原 Owner，禁止用 Peer 副本在本机重建别人的进程。S3 用自研最小 SigV4 客户端 + httptest fake，不引入 AWS SDK。

**Tech Stack:** 现有 Go + `modernc.org/sqlite` + ConnectRPC + Vue3 + Vitest。S3 兼容协议（Path-style PUT/GET/DELETE/ListObjectsV2）。不新增二进制、不新增 REST 资源、不做载荷加密层。

---

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- 强制 TDD：先红后绿；每任务先写失败测试
- `process` 不得 import `cluster`、`control`、`rpc`、`auth`、`web` 或 `backup`
- `backup` 可依赖 `store`、`process`（只通过 `ApplySpec`）、`rpc`（Peer Put）；不得让 `process` 反过来依赖它
- Raft **禁止**写入 backup 载荷或 index；Gossip **禁止**携带 backup 载荷
- 副本不是 writer：禁止把 snapshot 直写 `process_specs` / `config_revisions`
- 禁止 restore 到非 Owner；禁止在本机用 Peer 文件「重建别人的进程」
- 分区期间不得拿旧备份覆盖仍活着的 Owner（对端不可达 → `UNAVAILABLE`，不在本机 apply）
- `version.Protocol` 保持 `1`
- 错误码：`INVALID` / `CONFLICT` / `DENIED` / `NOT_FOUND` / `UNAVAILABLE` / `DEGRADED`
- 所有 Mutation 必须带非空 `operation_id`（UUID）；restore 写发给 Owner
- S3 密钥不进 Raft、不进 API 回显、不进 audit payload 明文
- 生成的 proto Go / TS 文件禁止手改；改完 proto 必须 `make proto` 与 `make proto-ts`
- 测试与代码同目录；`internal/backup` ≥ 80%；`internal/store` / `internal/auth` / `internal/api` 保持 ≥ 80%
- 文档与计划用中文；API 错误消息用英文
- 新文案进 `web/public/locales/{en,zh}`，跑 `npm run i18n:check`
- 视觉与新鲜度沿用 P5：`--bg #F7F7F8`、`--accent #10A37F` 不作状态绿；LIVE `#D1FAE5/#065F46`，STALE `#FEF3C7/#92400E`，UNKNOWN `#E5E7EB/#374151`
- **禁止把 STALE 画成绿色「正常」或「没有备份」**
- 磁盘 ≥95% 停写备份（`DEGRADED`），读尽量继续
- Peer backup RPC 必须 mTLS，与现有 Agent RPC 同一套证书
- 本阶段不做：集群一键 DR、Raft 快照外发、跨 cluster import、备份载荷额外加密、对端扫描 `backup/peer` 后自动 adopt
- 工作目录是本 worktree，提交写在 `feat/q5-config-backup`

## 规格解读（Q5 边界）

来源：`docs/superpowers/specs/2026-08-16-v1.1-architecture-design.md` §9、§10–§15；PRD §38、§89 Backup。冲突以架构 spec 为准。V1.0 合同：`docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。P5 计划明确「V1.0 不做配置备份」——本阶段按 P5 页面合同补上 Backup 页，不重做登录/Overview/启停。

1. **备份对象：** 只含本机每个（或指定）进程的 **当前 spec + `config_revisions` 全历史**。不含 runtime、PID、logs、metrics、audit、Raft、证书私钥、业务数据。
2. **快照元数据（必须逐字）：** `snapshot_id, cluster_id, node_id, created_at, process_ids[], revision_range per process, sha256, sink, location`。
3. **Sink：** `fs` 默认 `$data_dir/backup/fs/` 文件 0600；`s3` 读 `agent.yaml` 的 `backup.s3`；`peer` 调用方指定 `target_node_ids[]`，对端 `$data_dir/backup/peer/<source_node_id>/<snapshot_id>`，**只落盘禁止 apply**。
4. **触发：** 手动 `CreateBackup`；可选本机定时（默认关，`backup.schedule` 五字段 cron）。
5. **Restore：** 只许针对**当前该进程的 Owner**。唯一写路径 `Manager.ApplySpec`，产生**新 revision**，要求 `expected_revision`。可一次多进程，每进程独立 CAS，部分 `CONFLICT` 允许。
6. **权限：** `backup.read` 列表/下载；`backup.manage` 创建/删除/restore。Q1 已种子这两个 perm。
7. **List：** 本机 index ∪ 已配 S3 ∪ 指定 Peer；失败 STALE，不得显示成「没有备份」。
8. **可演示：** 改 spec 后 restore 得到新 revision；Peer 文件不被对端 apply；错误 `expected_revision` → `CONFLICT` 且 store 未被 snapshot 直写。

## File map

```text
internal/store/schema.sql
internal/store/backup.go                     # 新建
internal/store/backup_test.go                # 新建
internal/paths/paths.go
internal/paths/paths_test.go
internal/backup/types.go                     # 新建
internal/backup/types_test.go                # 新建
internal/backup/snapshot.go                  # 新建
internal/backup/snapshot_test.go             # 新建
internal/backup/sink.go                      # 新建
internal/backup/fs.go                        # 新建
internal/backup/fs_test.go                   # 新建
internal/backup/s3.go                        # 新建
internal/backup/s3_test.go                   # 新建
internal/backup/peer.go                      # 新建
internal/backup/peer_test.go                 # 新建
internal/backup/engine.go                    # 新建
internal/backup/engine_test.go               # 新建
internal/backup/schedule.go                  # 新建
internal/backup/schedule_test.go             # 新建
internal/agentcfg/load.go
internal/agentcfg/load_test.go
proto/procmesh/v1/api.proto
internal/api/proto_gen_test.go
internal/api/process.go                      # Forwarder.Backup
internal/api/metrics.go                      # countingForwarder.Backup + backup_last_success
internal/api/apitest_test.go
internal/api/auditapi_test.go
internal/api/backup.go                       # 新建
internal/api/backup_test.go                  # 新建
internal/api/server.go
internal/rpc/client.go
internal/agent/rpc.go
internal/agent/run.go
internal/cli/root.go
internal/cli/client.go
internal/cli/backup.go                       # 新建
internal/cli/root_test.go
web/src/lib/rpc.ts
web/src/router.ts
web/src/components/AppShell.vue
web/src/pages/BackupPage.vue                 # 新建
web/src/pages/BackupPage.test.ts             # 新建
web/public/locales/en/common.json
web/public/locales/zh/common.json
web/e2e/backup.spec.ts                       # 新建
internal/agent/q5_accept_test.go             # 新建
docs/superpowers/plans/2026-08-16-v1.1.md
```

生成（改 proto 后执行，不要手改）：`proto/procmesh/v1/api.pb.go`、`proto/procmesh/v1/procmeshv1connect/api.connect.go`、`web/src/gen/procmesh/v1/api_pb.ts`

---

## 本阶段锁定的模型

### Snapshot 载荷（JSON，format_version=1）

```go
type Snapshot struct {
    FormatVersion int            `json:"format_version"`
    SnapshotID    string         `json:"snapshot_id"`
    ClusterID     string         `json:"cluster_id"`
    NodeID        string         `json:"node_id"`
    CreatedAt     time.Time      `json:"created_at"`
    Processes     []ProcessDump  `json:"processes"`
}

type ProcessDump struct {
    ProcessID    string         `json:"process_id"`
    Name         string         `json:"name"`
    MinRevision  int64          `json:"min_revision"`
    MaxRevision  int64          `json:"max_revision"`
    Revisions    []RevisionDump `json:"revisions"`
}

type RevisionDump struct {
    Revision  int64           `json:"revision"`
    Operator  string          `json:"operator"`
    Timestamp time.Time       `json:"timestamp"`
    Diff      string          `json:"diff"`
    Comment   string          `json:"comment"`
    Spec      json.RawMessage `json:"spec"` // store 里的 spec_json，禁止再编码走样
}

type Meta struct {
    SnapshotID     string
    ClusterID      string
    NodeID         string
    CreatedAt      time.Time
    ProcessIDs     []string
    RevisionRanges []RevisionRange
    SHA256         string
    Sink           string // fs | s3 | peer
    Location       string
    SourceNodeID   string // peer 收件时填来源；自建为空
}

type RevisionRange struct {
    ProcessID    string
    MinRevision  int64
    MaxRevision  int64
}
```

`Encode(s Snapshot) (payload []byte, sha256hex string)`：`json.Marshal` 后对 **完整 payload 字节** 做 SHA-256，hex 小写。Decode 校验 `format_version==1`。Restore 用每个进程 `MaxRevision` 那条 `Spec` 调 `ApplySpec`。

### Sink 接口

```go
type Sink interface {
    Name() string
    Put(ctx context.Context, id string, payload []byte) (location string, err error)
    List(ctx context.Context) ([]Listed, error)
    Get(ctx context.Context, id string) ([]byte, error)
    Delete(ctx context.Context, id string) error
}

type Listed struct {
    SnapshotID string
    Location   string
}
```

| sink | 路径 / key | 模式 |
|------|------------|------|
| `fs` | `{FSDir}/{snapshot_id}.json`，FSDir 默认 `{data_dir}/backup/fs` | 文件 0600，目录 0750 |
| `s3` | `{prefix}/{cluster_id}/{node_id}/{snapshot_id}.json` | HTTPS；测试可用 `http://` httptest |
| `peer` | 对端 `{data_dir}/backup/peer/{source_node_id}/{snapshot_id}.json` | mTLS RPC，对端只 `os.WriteFile` |

### Restore 规则（必须逐字）

1. 调用方提供每个 target 的 `process_id` + `expected_revision`（禁止缺省）。
2. 本机 `node_id == snapshot.node_id` 才允许 apply。否则 hop 到 `snapshot.node_id`；hop 失败 `UNAVAILABLE`，**不得**在本机 apply。
3. 进程已存在：`ApplySpec(spec, expected_revision, opID, operator, "restore from snapshot")`。CAS 失败该 target `CONFLICT`，其它 target 继续。
4. 进程不存在且本机就是 snapshot.node_id：仅当 `expected_revision==0` 时 `ApplySpec` 创建；否则 `CONFLICT`。
5. 进程不存在且本机 **不是** snapshot.node_id：`INVALID`（禁止重建别人的进程）。即使载荷来自本地 `backup/peer/` 也一样。
6. 禁止调用 `store.PutSpec` / 直接 SQL。测试必须断言 `config_revisions` 只增加一行新 revision，旧行不被覆盖。
7. 多进程 Restore 的每进程 `operation_id` = 请求 `operation_id` + `":"` + `process_id`（保证幂等且不互相撞 journal）。

### Create 规则

- `process_ids` 空 = 本机全部 spec；非空则只备份列出的、且必须本机存在，否则 `NOT_FOUND`。
- `sink` 必须是 `fs` / `s3` / `peer`，否则 `INVALID`。
- `sink=peer` 必须非空 `target_node_ids`，且每个都是当前 `ADMITTED` 成员，否则 `INVALID`。
- 磁盘使用率 ≥95：`DEGRADED`，不写文件、不写 index。
- 成功后写 `backup_index`，更新 `backup_last_success_unix`。

### List / STALE

`BackupEntry` 与 Alert 同形：`snapshot` + `source_node` + `freshness` + `last_updated_unix_ms`。

- 本机 index：LIVE。
- 已配 S3：List 成功合并；失败加一条 **无 snapshot** 的 STALE 占位（`source_node="s3"`），不得当成空列表。
- 指定 Peer：hop `ListBackups`（或 Peer List RPC）；失败 STALE 占位（`source_node=peer_id`）。
- 未指定 peer 且未配 S3：只返回本机，不假装扫过全集群。

### agent.yaml

```yaml
backup:
  fs_dir: ""          # 空 = $data_dir/backup/fs
  schedule: ""        # 空 = 关；五字段 cron，如 "0 * * * *"
  s3:
    endpoint: ""
    bucket: ""
    prefix: ""
    region: ""
    access_key: ""
    secret_key: ""
    insecure: false   # true 仅测试允许 http
```

环境变量覆盖密钥（优先级高于 yaml）：`PROCMESH_S3_ACCESS_KEY`、`PROCMESH_S3_SECRET_KEY`。API 回显 BackupSnapshot 不得含任何 key/secret 字段。

### Proto（追加在 AlertService 之后，不要改已有字段号）

```protobuf
message BackupRevisionRange {
  string process_id = 1;
  int64 min_revision = 2;
  int64 max_revision = 3;
}

message BackupSnapshot {
  string snapshot_id = 1;
  string cluster_id = 2;
  string node_id = 3;
  int64 created_unix_ms = 4;
  repeated string process_ids = 5;
  repeated BackupRevisionRange revision_ranges = 6;
  string sha256 = 7;
  string sink = 8;
  string location = 9;
  string source_node_id = 10;
}

message BackupEntry {
  BackupSnapshot snapshot = 1;
  string source_node = 2;
  string freshness = 3; // LIVE | STALE | UNKNOWN
  int64 last_updated_unix_ms = 4;
}

message CreateBackupRequest {
  MutationMeta meta = 1;
  string sink = 2;                    // fs | s3 | peer
  repeated string process_ids = 3;    // 空 = 本机全部
  repeated string target_node_ids = 4; // peer 必填
}
message CreateBackupResponse { BackupSnapshot snapshot = 1; }

message ListBackupsRequest {
  string sink = 1;                    // 空 = 全部本机 index
  repeated string peer_node_ids = 2;  // 按需 hop
  bool include_s3 = 3;
  int32 limit = 4;                    // 0 = 50；封顶 200
}
message ListBackupsResponse { repeated BackupEntry entries = 1; }

message GetBackupRequest {
  string snapshot_id = 1;
  string sink = 2;
  string source_node_id = 3;
  bool include_payload = 4;
}
message GetBackupResponse {
  BackupSnapshot snapshot = 1;
  bytes payload = 2;
}

message DeleteBackupRequest {
  MutationMeta meta = 1;
  string snapshot_id = 2;
  string sink = 3;
  string source_node_id = 4;
}
message DeleteBackupResponse {}

message RestoreTarget {
  string process_id = 1;
  int64 expected_revision = 2;
}
message RestoreBackupRequest {
  MutationMeta meta = 1;
  string snapshot_id = 2;
  string sink = 3;
  string source_node_id = 4;
  repeated RestoreTarget targets = 5;
}
message RestoreProcessResult {
  string process_id = 1;
  string status = 2; // SUCCESS | CONFLICT | INVALID | NOT_FOUND | UNAVAILABLE | DENIED
  int64 new_revision = 3;
  string error = 4;
}
message RestoreBackupResponse { repeated RestoreProcessResult results = 1; }

message PutPeerSnapshotRequest {
  MutationMeta meta = 1;
  string source_node_id = 2;
  bytes payload = 3;
}
message PutPeerSnapshotResponse { BackupSnapshot snapshot = 1; }

service BackupService {
  rpc CreateBackup(CreateBackupRequest) returns (CreateBackupResponse);
  rpc ListBackups(ListBackupsRequest) returns (ListBackupsResponse);
  rpc GetBackup(GetBackupRequest) returns (GetBackupResponse);
  rpc DeleteBackup(DeleteBackupRequest) returns (DeleteBackupResponse);
  rpc RestoreBackup(RestoreBackupRequest) returns (RestoreBackupResponse);
  rpc PutPeerSnapshot(PutPeerSnapshotRequest) returns (PutPeerSnapshotResponse);
}
```

`PutPeerSnapshot` 是 Agent 间 RPC：对端只落盘。外部 Web/CLI 不要把它当 restore。

---

### Task 1: Snapshot 编解码 + backup_index + 路径

**Files:**
- Create: `internal/backup/types.go`、`internal/backup/types_test.go`、`internal/backup/snapshot.go`、`internal/backup/snapshot_test.go`
- Create: `internal/store/backup.go`、`internal/store/backup_test.go`
- Modify: `internal/store/schema.sql`
- Modify: `internal/paths/paths.go`、`internal/paths/paths_test.go`

**Interfaces:**
- Consumes: `store.Revision` / `listRevisionRows`（已有 `GetRevisionSpecJSON`、`ListRevisions`）
- Produces: `backup.Snapshot` / `backup.Encode` / `backup.Decode` / `backup.MetaFromSnapshot`；`store.BackupRecord` CRUD；`paths.Layout.BackupFSDir` / `BackupPeerDir`

- [ ] **Step 1: Write the failing tests**

`internal/backup/snapshot_test.go`：

```go
package backup_test

func TestEncodeDecode_RoundTripAndSHA256(t *testing.T) {
    s := backup.Snapshot{
        FormatVersion: 1,
        SnapshotID:    "snap-1",
        ClusterID:     "c1",
        NodeID:        "n1",
        CreatedAt:     time.Unix(1_700_000_000, 0).UTC(),
        Processes: []backup.ProcessDump{{
            ProcessID: "p1", Name: "web", MinRevision: 1, MaxRevision: 2,
            Revisions: []backup.RevisionDump{
                {Revision: 1, Operator: "a", Timestamp: time.Unix(1_700_000_000, 0).UTC(), Spec: json.RawMessage(`{"Name":"web"}`)},
                {Revision: 2, Operator: "b", Timestamp: time.Unix(1_700_000_100, 0).UTC(), Spec: json.RawMessage(`{"Name":"web","Command":"/bin/web"}`)},
            },
        }},
    }
    payload, sum, err := backup.Encode(s)
    if err != nil || sum == "" || !json.Valid(payload) {
        t.Fatalf("encode %q %v", sum, err)
    }
    got, err := backup.Decode(payload)
    if err != nil || got.SnapshotID != "snap-1" || got.Processes[0].MaxRevision != 2 {
        t.Fatalf("decode %+v %v", got, err)
    }
    if _, sum2, _ := backup.Encode(got); sum2 != sum {
        t.Fatalf("sha mismatch %s %s", sum, sum2)
    }
}

func TestDecode_RejectsBadVersion(t *testing.T) {
    _, err := backup.Decode([]byte(`{"format_version":2,"snapshot_id":"x"}`))
    if !errcode.Is(err, errcode.INVALID) {
        t.Fatalf("err %v", err)
    }
}

func TestLatestSpec_ReturnsMaxRevisionRawJSON(t *testing.T) {
    raw, err := backup.LatestSpec(backup.ProcessDump{
        MaxRevision: 2,
        Revisions: []backup.RevisionDump{
            {Revision: 1, Spec: json.RawMessage(`{"A":1}`)},
            {Revision: 2, Spec: json.RawMessage(`{"A":2}`)},
        },
    })
    if err != nil || string(raw) != `{"A":2}` {
        t.Fatalf("%s %v", raw, err)
    }
}
```

`internal/store/backup_test.go`：

```go
func TestBackupIndex_PutGetListDelete(t *testing.T) {
    ctx := context.Background()
    s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = s.Close() })

    rec := store.BackupRecord{
        SnapshotID: "s1", ClusterID: "c", NodeID: "n",
        CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
        ProcessIDs: []string{"p1"}, SHA256: "abc", Sink: "fs",
        Location: "/data/backup/fs/s1.json",
        RevisionRangesJSON: `[{"process_id":"p1","min_revision":1,"max_revision":2}]`,
    }
    if err := s.PutBackup(ctx, rec); err != nil { t.Fatal(err) }
    got, err := s.GetBackup(ctx, "s1")
    if err != nil || got.Sink != "fs" || got.SHA256 != "abc" {
        t.Fatalf("%+v %v", got, err)
    }
    list, err := s.ListBackups(ctx)
    if err != nil || len(list) != 1 { t.Fatalf("%d %v", len(list), err) }
    if err := s.DeleteBackup(ctx, "s1"); err != nil { t.Fatal(err) }
    if _, err := s.GetBackup(ctx, "s1"); !errcode.Is(err, errcode.NOT_FOUND) {
        t.Fatalf("err %v", err)
    }
}

func TestBackupIndex_MissingIsNotFound(t *testing.T) {
    s, _ := store.Open(filepath.Join(t.TempDir(), "store.db"))
    t.Cleanup(func() { _ = s.Close() })
    if _, err := s.GetBackup(context.Background(), "nope"); !errcode.Is(err, errcode.NOT_FOUND) {
        t.Fatalf("err %v", err)
    }
}
```

`internal/paths/paths_test.go` 增加：

```go
func TestNew_BackupDirs(t *testing.T) {
    l := paths.New("/data")
    if l.BackupFSDir() != "/data/backup/fs" {
        t.Fatal(l.BackupFSDir())
    }
    if l.BackupPeerDir("src") != "/data/backup/peer/src" {
        t.Fatal(l.BackupPeerDir("src"))
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup ./internal/store -run 'TestEncode|TestDecode|TestLatest|TestBackupIndex|TestNew_BackupDirs' -count=1`

Expected: FAIL（包不存在 / 方法不存在）

- [ ] **Step 3: Write minimal implementation**

`schema.sql` 追加：

```sql
CREATE TABLE IF NOT EXISTS backup_index (
    snapshot_id TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    process_ids_json TEXT NOT NULL,
    revision_range_json TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    sink TEXT NOT NULL,
    location TEXT NOT NULL,
    source_node_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS backup_index_created ON backup_index(created_at DESC);
```

`store.BackupRecord` 字段与上表对应；`ProcessIDs` 用 JSON 数组存。`PutBackup` UPSERT。时间 RFC3339Nano。

`paths.Layout` 增加：

```go
func (l Layout) BackupRoot() string { return filepath.Join(l.Root, "backup") }
func (l Layout) BackupFSDir() string { return filepath.Join(l.BackupRoot(), "fs") }
func (l Layout) BackupPeerDir(sourceNodeID string) string {
    return filepath.Join(l.BackupRoot(), "peer", sourceNodeID)
}
```

`Ensure` **不要**预创建 backup 目录（由 Sink 在 Put 时 MkdirAll 0750）。

`backup.Encode` / `Decode` / `LatestSpec` / `MetaFromSnapshot` 按锁定模型实现。`Decode` 在 `format_version != 1` 或缺 `snapshot_id` 时 `INVALID`。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/backup ./internal/store ./internal/paths -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup internal/store/schema.sql internal/store/backup.go internal/store/backup_test.go internal/paths
git commit -m "feat(backup): snapshot codec and local backup_index"
```

---

### Task 2: FS Sink

**Files:**
- Create: `internal/backup/sink.go`、`internal/backup/fs.go`、`internal/backup/fs_test.go`

**Interfaces:**
- Consumes: `backup.Sink`、`paths.Layout.BackupFSDir`
- Produces: `backup.NewFSSink(dir string) *FSSink`

- [ ] **Step 1: Write the failing tests**

```go
func TestFSSink_PutGetListDelete_Mode0600(t *testing.T) {
    dir := filepath.Join(t.TempDir(), "fs")
    s := backup.NewFSSink(dir)
    payload := []byte(`{"format_version":1,"snapshot_id":"s1"}`)
    loc, err := s.Put(context.Background(), "s1", payload)
    if err != nil { t.Fatal(err) }
    if loc != filepath.Join(dir, "s1.json") { t.Fatal(loc) }
    st, err := os.Stat(loc)
    if err != nil || st.Mode().Perm() != 0o600 {
        t.Fatalf("mode %v %v", st.Mode(), err)
    }
    got, err := s.Get(context.Background(), "s1")
    if err != nil || string(got) != string(payload) { t.Fatalf("%s %v", got, err) }
    list, err := s.List(context.Background())
    if err != nil || len(list) != 1 || list[0].SnapshotID != "s1" {
        t.Fatalf("%+v %v", list, err)
    }
    if err := s.Delete(context.Background(), "s1"); err != nil { t.Fatal(err) }
    if _, err := s.Get(context.Background(), "s1"); !errcode.Is(err, errcode.NOT_FOUND) {
        t.Fatalf("err %v", err)
    }
}

func TestFSSink_RejectsPathEscape(t *testing.T) {
    s := backup.NewFSSink(t.TempDir())
    if _, err := s.Put(context.Background(), "../x", []byte("{}")); !errcode.Is(err, errcode.INVALID) {
        t.Fatalf("err %v", err)
    }
}

func TestFSSink_Name(t *testing.T) {
    if backup.NewFSSink("/tmp").Name() != "fs" { t.Fatal("name") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup -run TestFSSink_ -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`snapshotID` 必须匹配 `^[A-Za-z0-9._-]+$`，否则 `INVALID`。`Put`：`MkdirAll(dir, 0750)`，写入 `{id}.json.tmp` 再 `Rename`，`Chmod 0600`。`Get`/`Delete` 不存在 → `NOT_FOUND`。`List` 只枚举 `*.json`。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/backup -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup
git commit -m "feat(backup): filesystem sink with 0600 files"
```

---

### Task 3: S3 Sink + fake server

**Files:**
- Create: `internal/backup/s3.go`、`internal/backup/s3_test.go`

**Interfaces:**
- Consumes: `backup.Sink`、`agentcfg` 稍后接线的 `S3Config`
- Produces: `backup.S3Config`、`backup.NewS3Sink(cfg S3Config) (*S3Sink, error)`

```go
type S3Config struct {
    Endpoint  string
    Bucket    string
    Prefix    string
    Region    string
    AccessKey string
    SecretKey string
    Insecure  bool // 仅测试
    ClusterID string
    NodeID    string
    HTTP      *http.Client // 测试注入
}
```

对象 key：`path.Join(prefix, clusterID, nodeID, snapshotID+".json")`，空 prefix 则 `{cluster}/{node}/{id}.json`。

实现最小 Path-style S3：`PUT`/`GET`/`DELETE` `/{bucket}/{key}`，`GET /{bucket}?list-type=2&prefix=`。Authorization 用 AWS SigV4（`AWS4-HMAC-SHA256`）。测试用 `httptest.NewServer` 自己实现这四个操作并校验签名头存在；**不要**连真 S3。

- [ ] **Step 1: Write the failing tests**

```go
func TestS3Sink_PutGetListDeleteAgainstFake(t *testing.T) {
    fake := newFakeS3(t) // httptest，内存 map
    cfg := backup.S3Config{
        Endpoint: fake.URL, Bucket: "b", Prefix: "p", Region: "us-east-1",
        AccessKey: "AK", SecretKey: "SK", Insecure: true,
        ClusterID: "c", NodeID: "n", HTTP: fake.Client(),
    }
    s, err := backup.NewS3Sink(cfg)
    if err != nil { t.Fatal(err) }
    payload := []byte(`{"format_version":1,"snapshot_id":"s1"}`)
    loc, err := s.Put(context.Background(), "s1", payload)
    if err != nil { t.Fatal(err) }
    if !strings.Contains(loc, "s1.json") { t.Fatal(loc) }
    got, err := s.Get(context.Background(), "s1")
    if err != nil || string(got) != string(payload) { t.Fatalf("%s %v", got, err) }
    list, err := s.List(context.Background())
    if err != nil || len(list) != 1 { t.Fatalf("%+v %v", list, err) }
    if err := s.Delete(context.Background(), "s1"); err != nil { t.Fatal(err) }
    if _, err := s.Get(context.Background(), "s1"); !errcode.Is(err, errcode.NOT_FOUND) {
        t.Fatalf("err %v", err)
    }
}

func TestS3Sink_MissingBucketInvalid(t *testing.T) {
    _, err := backup.NewS3Sink(backup.S3Config{Endpoint: "http://x", AccessKey: "a", SecretKey: "b"})
    if !errcode.Is(err, errcode.INVALID) { t.Fatalf("err %v", err) }
}

func TestS3Sink_Name(t *testing.T) {
    s, _ := backup.NewS3Sink(backup.S3Config{Endpoint: "http://x", Bucket: "b", AccessKey: "a", SecretKey: "s", Insecure: true})
    if s.Name() != "s3" { t.Fatal("name") }
}
```

`newFakeS3` 实现：校验 `Authorization` 含 `AWS4-HMAC-SHA256`；按 path-style 存取 body。ListObjectsV2 返回最少可用 XML：

```xml
<ListBucketResult><Contents><Key>p/c/n/s1.json</Key></Contents></ListBucketResult>
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup -run TestS3Sink_ -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`s3.go`：SigV4 签 `PUT/GET/DELETE/GET?list-type=2`。`Insecure=false` 且 endpoint 以 `http://` 开头 → `INVALID`（生产必须 HTTPS）。空 bucket/endpoint → `INVALID`。HTTP 404 → `NOT_FOUND`；其它 5xx → `UNAVAILABLE`。

不要把 SecretKey 写进 `Listed.Location` 或 error 字符串。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/backup -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup
git commit -m "feat(backup): S3-compatible sink with SigV4 and fake server tests"
```

---

### Task 4: Engine Create / Get / Delete + 磁盘 95%

**Files:**
- Create: `internal/backup/engine.go`、`internal/backup/engine_test.go`

**Interfaces:**
- Consumes: `store.Store`（`ListSpecs`、`listRevisionRows`/`GetRevisionSpecJSON`、`PutBackup`）、`Sink`、`ApplySpec` 本任务还不用
- Produces:

```go
type Engine struct {
    Store     *store.Store
    NodeID    string
    ClusterID string
    Sinks     map[string]Sink // "fs"|"s3"
    DiskPercent func() float64 // nil = 0
    Now       func() time.Time
    NewID     func() (string, error) // 测试注入；默认 UUID
    LastSuccessUnix atomic.Int64
}

func (e *Engine) Create(ctx context.Context, processIDs []string, sinkName string) (backup.Meta, error)
func (e *Engine) Get(ctx context.Context, snapshotID, sinkName string) (backup.Meta, []byte, error)
func (e *Engine) Delete(ctx context.Context, snapshotID, sinkName string) error
func (e *Engine) ListLocal(ctx context.Context) ([]backup.Meta, error)
```

需要给 store 增加 `ListRevisionDumps(ctx, processID) ([]store.Revision, error)`——已有 `listRevisionRows` 未导出。本任务在 `store` 导出：

```go
func (s *Store) ListRevisionDumps(ctx context.Context, processID string) ([]Revision, error) {
    return s.listRevisionRows(ctx, processID)
}
```

- [ ] **Step 1: Write the failing tests**

用真实 `store.Open` + `PutSpec` 建两个 revision，再 `NewFSSink`。

```go
func TestEngine_CreateListsAndGetsFS(t *testing.T) {
    ctx := context.Background()
    st, spec := seedProcess(t) // name=web, two revisions
    dir := filepath.Join(t.TempDir(), "fs")
    e := &backup.Engine{
        Store: st, NodeID: "n1", ClusterID: "c1",
        Sinks: map[string]backup.Sink{"fs": backup.NewFSSink(dir)},
        Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
        NewID: func() (string, error) { return "snap-1", nil },
    }
    meta, err := e.Create(ctx, nil, "fs")
    if err != nil { t.Fatal(err) }
    if meta.SnapshotID != "snap-1" || meta.Sink != "fs" || meta.SHA256 == "" {
        t.Fatalf("%+v", meta)
    }
    if len(meta.ProcessIDs) != 1 || meta.RevisionRanges[0].MaxRevision < 1 {
        t.Fatalf("%+v", meta)
    }
    if e.LastSuccessUnix.Load() != 1_700_000_000 {
        t.Fatalf("metric %d", e.LastSuccessUnix.Load())
    }
    m2, payload, err := e.Get(ctx, "snap-1", "fs")
    if err != nil || m2.SHA256 != meta.SHA256 || len(payload) == 0 {
        t.Fatalf("%+v %v", m2, err)
    }
    snap, err := backup.Decode(payload)
    if err != nil || snap.Processes[0].ProcessID != spec.ProcessID {
        t.Fatalf("%+v %v", snap, err)
    }
}

func TestEngine_UnknownSinkInvalid(t *testing.T) {
    e := &backup.Engine{Sinks: map[string]backup.Sink{}}
    _, err := e.Create(context.Background(), nil, "tape")
    if !errcode.Is(err, errcode.INVALID) { t.Fatalf("err %v", err) }
}

func TestEngine_MissingProcessNotFound(t *testing.T) {
    e := seededEngine(t)
    _, err := e.Create(context.Background(), []string{"nope"}, "fs")
    if !errcode.Is(err, errcode.NOT_FOUND) { t.Fatalf("err %v", err) }
}

func TestEngine_Disk95RejectsCreate(t *testing.T) {
    e := seededEngine(t)
    e.DiskPercent = func() float64 { return 95 }
    _, err := e.Create(context.Background(), nil, "fs")
    if !errcode.Is(err, errcode.DEGRADED) { t.Fatalf("err %v", err) }
    list, _ := e.ListLocal(context.Background())
    if len(list) != 0 { t.Fatalf("must not write index: %d", len(list)) }
}

func TestEngine_DeleteRemovesFileAndIndex(t *testing.T) {
    e := seededEngine(t)
    meta, err := e.Create(context.Background(), nil, "fs")
    if err != nil { t.Fatal(err) }
    if err := e.Delete(context.Background(), meta.SnapshotID, "fs"); err != nil { t.Fatal(err) }
    if _, _, err := e.Get(context.Background(), meta.SnapshotID, "fs"); !errcode.Is(err, errcode.NOT_FOUND) {
        t.Fatalf("err %v", err)
    }
}
```

`seedProcess`：`store.PutSpec` 一次 create（rev 1），再 update（rev 2）。`seededEngine` 复用。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup -run TestEngine_ -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`Create`：校验 sink；`DiskPercent() >= 95` → `DEGRADED`；收集 spec+全历史；`Encode`；`Sink.Put`；`Store.PutBackup`；`LastSuccessUnix = Now().Unix()`。

`Get`：先 index，再 sink.Get；payload 的 sha256 必须等于 index，否则 `INVALID`（损坏）。

空 process 列表（本机零进程）→ `INVALID`（"no processes to backup"）。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/backup ./internal/store -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup internal/store
git commit -m "feat(backup): create get delete engine with disk 95 guard"
```

---

### Task 5: Restore 只走 ApplySpec + CAS

**Files:**
- Modify: `internal/backup/engine.go`、`internal/backup/engine_test.go`

**Interfaces:**
- Consumes: `process.Manager.ApplySpec`
- Produces:

```go
type Applier interface {
    ApplySpec(ctx context.Context, spec process.ProcessSpec, expectedRevision int64, opID, operator, comment string) (process.ProcessSpec, error)
    GetSpec(ctx context.Context, processID string) (process.ProcessSpec, error)
}

type RestoreTarget struct {
    ProcessID         string
    ExpectedRevision  int64
}

type RestoreResult struct {
    ProcessID    string
    Status       string
    NewRevision  int64
    Error        string
}

func (e *Engine) Restore(ctx context.Context, snapshotID, sinkName, opID, operator string, targets []RestoreTarget) ([]RestoreResult, error)
```

`Engine.Apply` 字段类型 `Applier`。测试用真实 `process.Manager`（与现有 `process` 单测同样的 deps 夹具），**禁止** mock ApplySpec 来「证明」走了 ApplySpec——要用真实 Manager，然后断言 store 的 `latest_revision` 增加，且旧 revision 行仍在。

- [ ] **Step 1: Write the failing tests**

```go
func TestEngine_RestoreAppliesNewRevisionViaCAS(t *testing.T) {
    ctx := context.Background()
    mgr, st, spec := seedManagedProcess(t) // latest=1
    e := engineWithMgr(t, st, mgr)
    meta, err := e.Create(ctx, nil, "fs")
    if err != nil { t.Fatal(err) }

    spec.Command = "/bin/changed"
    if _, err := mgr.ApplySpec(ctx, spec, spec.LatestRevision, "op-change", "t", ""); err != nil {
        t.Fatal(err)
    }
    latest, _ := mgr.GetSpec(ctx, spec.ProcessID)
    if latest.LatestRevision < 2 { t.Fatalf("rev %d", latest.LatestRevision) }

    results, err := e.Restore(ctx, meta.SnapshotID, "fs", "op-restore", "t", []backup.RestoreTarget{{
        ProcessID: spec.ProcessID, ExpectedRevision: latest.LatestRevision,
    }})
    if err != nil { t.Fatal(err) }
    if len(results) != 1 || results[0].Status != "SUCCESS" || results[0].NewRevision != latest.LatestRevision+1 {
        t.Fatalf("%+v", results)
    }
    got, _ := mgr.GetSpec(ctx, spec.ProcessID)
    if got.Command != spec.Command && got.Command != "/bin/true" {
        // restore 应回到 snapshot 里的 command（seed 的原始值），不是 /bin/changed
    }
    if got.Command == "/bin/changed" {
        t.Fatal("restore did not apply snapshot spec")
    }
    revs, _ := st.ListRevisions(ctx, spec.ProcessID)
    if len(revs) < 3 { t.Fatalf("history rewritten? %d", len(revs)) }
}

func TestEngine_RestoreWrongExpectedConflictDoesNotRewriteStore(t *testing.T) {
    ctx := context.Background()
    mgr, st, spec := seedManagedProcess(t)
    e := engineWithMgr(t, st, mgr)
    meta, _ := e.Create(ctx, nil, "fs")
    before, _ := st.ListRevisions(ctx, spec.ProcessID)
    results, err := e.Restore(ctx, meta.SnapshotID, "fs", "op-bad", "t", []backup.RestoreTarget{{
        ProcessID: spec.ProcessID, ExpectedRevision: spec.LatestRevision + 9,
    }})
    if err != nil { t.Fatal(err) } // 部分失败不返回顶层 error
    if len(results) != 1 || results[0].Status != "CONFLICT" {
        t.Fatalf("%+v", results)
    }
    after, _ := st.ListRevisions(ctx, spec.ProcessID)
    if len(after) != len(before) {
        t.Fatal("store was written despite CAS conflict")
    }
}

func TestEngine_RestoreForeignSnapshotWithoutLocalProcessInvalid(t *testing.T) {
    // Engine.NodeID="n-local"；payload 里 node_id="n-other" 且本机无该 process
    // Restore → 该 target Status=INVALID，ApplySpec 调用次数 0
}

func TestEngine_RestoreMissingExpectedTargetsInvalid(t *testing.T) {
    e := seededEngine(t)
    _, err := e.Restore(context.Background(), "x", "fs", "op", "t", nil)
    if !errcode.Is(err, errcode.INVALID) { t.Fatalf("err %v", err) }
}
```

`seedManagedProcess` 必须走真实 `process.NewManager` + temp store（抄 `internal/process/manager_test.go` 的最小夹具：Store、Layout、空 shim 即可，ApplySpec 不需要真启进程）。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup -run TestEngine_Restore -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`Restore`：

1. targets 空 → `INVALID`
2. `Get` payload + `Decode`
3. 若 `snap.NodeID != e.NodeID`：每个 target 标 `INVALID`（「cannot restore another node's process on this agent」）。hop 是 Task 8 的 API 层职责，Engine 只处理本机。
4. 对每个 target：`LatestSpec` → `json.Unmarshal` 到 `process.ProcessSpec`，保留 snapshot 里的 `ProcessID`/`Name`/`Command`/…；`ApplySpec(..., expected, opID+":"+pid, operator, "restore from snapshot "+id)`。
5. `errcode.Is(err, CONFLICT)` → 该行 `CONFLICT`；`NOT_FOUND` 且 expected==0 不应发生（ApplySpec 0 是 create）。`NOT_FOUND` 且 expected!=0 → `CONFLICT`。其它错误映射 `INVALID`/`UNAVAILABLE`。
6. **禁止** `st.PutSpec`。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/backup ./internal/process ./internal/store -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup
git commit -m "feat(backup): restore via ApplySpec CAS without rewriting history"
```

---

### Task 6: Peer 落盘且禁止 apply

**Files:**
- Create: `internal/backup/peer.go`、`internal/backup/peer_test.go`
- Modify: `internal/backup/engine.go`、`internal/backup/engine_test.go`

**Interfaces:**
- Consumes: `paths.Layout.BackupPeerDir`、`Encode/Decode`
- Produces:

```go
type PeerStore struct {
    Root string // data_dir
}

func (p *PeerStore) Receive(ctx context.Context, sourceNodeID string, payload []byte) (backup.Meta, error)
func (p *PeerStore) Get(ctx context.Context, sourceNodeID, snapshotID string) ([]byte, error)
func (p *PeerStore) List(ctx context.Context, sourceNodeID string) ([]backup.Listed, error)
func (p *PeerStore) Delete(ctx context.Context, sourceNodeID, snapshotID string) error
```

`Engine.ReceivePeer(ctx, sourceNodeID, payload)`：`PeerStore.Receive` + `Store.PutBackup`（`Sink=peer`, `SourceNodeID=source`）。**不得**调用 `Apply`。

`Engine.Push` 本任务只定义接口，真 RPC 在 Task 8：

```go
type PeerPusher interface {
    PutPeerSnapshot(ctx context.Context, nodeID string, sourceNodeID string, payload []byte) error
}
```

本任务 Engine.Create `sink=peer` 在 `PeerPush != nil` 时对每个 target 调 `PutPeerSnapshot`；单测用 fake pusher。Create 仍在本机 index 记一条 `sink=peer`（location 写成 `peer://{node}/{id}` 逗号拼接）。

- [ ] **Step 1: Write the failing tests**

```go
func TestPeerStore_ReceiveWritesFile0600AndNeverNeedsApplier(t *testing.T) {
    root := t.TempDir()
    p := &backup.PeerStore{Root: root}
    snap := backup.Snapshot{FormatVersion: 1, SnapshotID: "s1", ClusterID: "c", NodeID: "src",
        CreatedAt: time.Unix(1, 0).UTC(),
        Processes: []backup.ProcessDump{{ProcessID: "p-remote", Name: "other", MaxRevision: 1,
            Revisions: []backup.RevisionDump{{Revision: 1, Spec: json.RawMessage(`{"Name":"other","ProcessID":"p-remote"}`)}}}}}
    payload, _, err := backup.Encode(snap)
    if err != nil { t.Fatal(err) }
    meta, err := p.Receive(context.Background(), "src", payload)
    if err != nil { t.Fatal(err) }
    want := filepath.Join(root, "backup", "peer", "src", "s1.json")
    if meta.Location != want { t.Fatal(meta.Location) }
    st, err := os.Stat(want)
    if err != nil || st.Mode().Perm() != 0o600 { t.Fatalf("mode %v", st.Mode()) }
}

func TestEngine_ReceivePeerDoesNotApply(t *testing.T) {
    mgr, st, _ := seedManagedProcess(t)
    e := engineWithMgr(t, st, mgr)
    before, _ := st.ListSpecs(context.Background())
    payload := foreignSnapshotPayload(t) // node_id=other, process_id=foreign
    if _, err := e.ReceivePeer(context.Background(), "other", payload); err != nil {
        t.Fatal(err)
    }
    after, _ := st.ListSpecs(context.Background())
    if len(after) != len(before) {
        t.Fatal("peer receive must not create processes")
    }
    // 再 Restore 这条 peer 快照：本机 NodeID != other → INVALID，specs 仍不变
    recs, _ := st.ListBackups(context.Background())
    var peerID string
    for _, r := range recs {
        if r.Sink == "peer" { peerID = r.SnapshotID }
    }
    results, err := e.Restore(context.Background(), peerID, "peer", "op", "t", []backup.RestoreTarget{{
        ProcessID: "foreign", ExpectedRevision: 0,
    }})
    if err != nil { t.Fatal(err) }
    if results[0].Status != "INVALID" {
        t.Fatalf("%+v", results)
    }
    after2, _ := st.ListSpecs(context.Background())
    if len(after2) != len(before) {
        t.Fatal("restore of peer copy created a process")
    }
}

func TestEngine_CreatePeerCallsPusher(t *testing.T) {
    e := seededEngine(t)
    var got []string
    e.PeerPush = backup.PeerPushFunc(func(ctx context.Context, nodeID, source string, payload []byte) error {
        got = append(got, nodeID)
        if source != e.NodeID || len(payload) == 0 { t.Fatalf("bad push %s %s", source, nodeID) }
        return nil
    })
    e.Admitted = func(id string) bool { return id == "peer-1" }
    meta, err := e.CreatePeer(context.Background(), nil, []string{"peer-1"})
    if err != nil { t.Fatal(err) }
    if meta.Sink != "peer" || got[0] != "peer-1" { t.Fatalf("%+v %v", meta, got) }
}

func TestEngine_CreatePeerRejectsNonAdmitted(t *testing.T) {
    e := seededEngine(t)
    e.Admitted = func(string) bool { return false }
    _, err := e.CreatePeer(context.Background(), nil, []string{"x"})
    if !errcode.Is(err, errcode.INVALID) { t.Fatalf("err %v", err) }
}
```

若用 `Create(ctx, ids, "peer")` 更干净，则 `Create` 增加 `targetNodeIDs []string` 参数。为避免改签名，提供 `CreatePeer` **或** 让 `Create` 接受 options：

```go
type CreateOpts struct {
    ProcessIDs     []string
    Sink           string
    TargetNodeIDs  []string
}
func (e *Engine) Create(ctx context.Context, opt CreateOpts) (Meta, error)
```

本任务把 Task 4 的 `Create(ctx, ids, sink)` **改成** `Create(ctx, CreateOpts)`，并改已有测试调用。这是计划内重构，不是范围蔓延。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup -run 'TestPeer|TestEngine_Receive|TestEngine_CreatePeer' -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`Receive`：Decode 校验 → 写 `backup/peer/<source>/<id>.json` 0600。`sourceNodeID` 与 snapshotID 同样防路径穿越。

`Create` sink=peer：校验 Admitted；Encode；对每个 target `PeerPush`；任一对端失败 → 该 Create 返回 `UNAVAILABLE`（已成功的对端文件可留，index 仍写成功的 location 列表）。全部失败则不写成功 metric。

`Restore` 当 `sink=="peer"` 时从 `PeerStore.Get` 读文件，**仍然**执行「snap.NodeID != e.NodeID → INVALID」。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/backup -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup
git commit -m "feat(backup): peer receive writes files and never applies"
```

---

### Task 7: Proto BackupService + 代码生成

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Modify: `internal/api/proto_gen_test.go`
- Generate: Go + TS 客户端

**Interfaces:**
- Consumes: 现有 `MutationMeta`
- Produces: 上文锁定的 messages + `BackupService`

- [ ] **Step 1: Write the failing test**

`internal/api/proto_gen_test.go` 追加：

```go
func TestProto_BackupServiceGenerated(t *testing.T) {
    if procmeshv1connect.BackupServiceName != "procmesh.v1.BackupService" {
        t.Fatalf("backup=%s", procmeshv1connect.BackupServiceName)
    }
    if procmeshv1connect.BackupServiceCreateBackupProcedure == "" {
        t.Fatal("missing CreateBackup")
    }
    if procmeshv1connect.BackupServiceRestoreBackupProcedure == "" {
        t.Fatal("missing RestoreBackup")
    }
    if procmeshv1connect.BackupServicePutPeerSnapshotProcedure == "" {
        t.Fatal("missing PutPeerSnapshot")
    }
    _ = (&procmeshv1.BackupSnapshot{}).GetSha256
    _ = (&procmeshv1.BackupEntry{}).GetFreshness
    _ = (&procmeshv1.RestoreBackupRequest{}).GetTargets
    _ = (&procmeshv1.CreateBackupRequest{}).GetTargetNodeIds
    var _ procmeshv1connect.BackupServiceHandler = (*BackupAPI)(nil)
}
```

可先放一个空 `type BackupAPI struct{}` 在 `backup.go` 只为编译接口；RPC 方法在 Task 8 实现。若 Task 7 不想建空 handler，测试里不要写 `var _ Handler = (*BackupAPI)(nil)`，改到 Task 8。**本任务不要写 `var _` 到未实现类型**——只断言生成的 Name/Procedure/字段。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestProto_BackupServiceGenerated -count=1`

Expected: FAIL（没有 BackupServiceName）

- [ ] **Step 3: 追加 proto 并生成**

把锁定的 protobuf 追加到 `api.proto` 末尾（AlertService 之后）。然后：

```bash
make proto
make proto-ts
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api -run TestProto_ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add proto internal/api/proto_gen_test.go proto/procmesh/v1 web/src/gen
git commit -m "feat(proto): BackupService create list get delete restore and peer put"
```

---

### Task 8: API + RPC hop + STALE List + 指标 + 配置 + 定时

**Files:**
- Create: `internal/api/backup.go`、`internal/api/backup_test.go`
- Create: `internal/backup/schedule.go`、`internal/backup/schedule_test.go`
- Modify: `internal/api/server.go`、`internal/api/process.go`（Forwarder）、`internal/api/metrics.go`、`internal/api/apitest_test.go`、`internal/api/auditapi_test.go` 及所有实现 Forwarder 的 fake
- Modify: `internal/rpc/client.go`、`internal/agent/rpc.go`、`internal/agent/run.go`
- Modify: `internal/agentcfg/load.go`、`internal/agentcfg/load_test.go`

**Interfaces:**
- Consumes: `backup.Engine`、已生成 `BackupService`
- Produces: 对外 BackupService；LocalOnly 处理 `PutPeerSnapshot`；`backup_last_success_unix`；`backup.schedule` 默认关

`Forwarder` 增加：

```go
Backup(ctx context.Context, rt Route) (procmeshv1connect.BackupServiceClient, error)
```

所有 fake / countingForwarder / agentForwarder 必须补上，否则编译失败。`NewBackupClient` 放 `internal/rpc/client.go`。hop timeout = `rpc.UnaryTimeout`（Create/Restore 用 `MutationTimeout`）。

`BackupAPI`：

- `CreateBackup`：`backup.manage`；空 `operation_id` → `INVALID`；调 `Engine.Create`。
- `ListBackups`：`backup.read`；本机 `ListLocal` LIVE；`include_s3` 且已配 S3 则 List，失败 STALE 占位；每个 `peer_node_ids` hop `ListBackups`（LocalOnly 对端只回本机），失败 STALE。limit 0=50 封顶 200。
- `GetBackup`：`backup.read`；`include_payload` 时返回 bytes。
- `DeleteBackup`：`backup.manage` + operation_id。
- `RestoreBackup`：`backup.manage` + operation_id；targets 空 `INVALID`。若 snapshot.node_id != LocalID 且非 LocalOnly → hop 到该 node；对端仍是 Owner 再 `Engine.Restore`。本机非 Owner 且 LocalOnly → `INVALID`。
- `PutPeerSnapshot`：仅 LocalOnly 或来自 mTLS hop（与其它 Owner RPC 相同，走 `OwnerAuthInterceptor`）；`backup.manage`；`Engine.ReceivePeer`。**禁止**在此调用 Restore/ApplySpec。

`agent.yaml` 增加 `backup` 段，默认 `Schedule=""`。`LoadAll` 解析；密钥可用 env 覆盖。`S3Config` 回显测试：加一个 `Redacted() S3Config` 把 SecretKey/AccessKey 清空——API 不得返回这两字段（proto 里本来就没有）。

`schedule.go`：

```go
func Next(cronExpr string, from time.Time) (time.Time, error)
```

只支持五字段（分 时 日 月 周），token 为 `*` 或单个整数。非法 → `INVALID`。空表达式 → 零 Time, nil（表示关闭）。`Engine.TickSchedule(ctx)`：若到点则 `Create(CreateOpts{Sink:"fs"})`。Agent run loop 每分钟调一次即可（挂在现有 1s ticker 里，用 lastMinute 去重）。

`/metrics` 增加 `procmesh_backup_last_success_unix`（gauge，值来自 `Engine.LastSuccessUnix`）。抄 `alert_send_total` 的暴露方式。

- [ ] **Step 1: Write the failing tests**

`backup_test.go` 关键用例：

```go
func TestBackupAPI_CreateRequiresOperationID(t *testing.T)
func TestBackupAPI_CreateFSAndListLIVE(t *testing.T)
func TestBackupAPI_ListMarksS3FailureSTALE(t *testing.T)
func TestBackupAPI_ListMarksPeerFailureSTALE(t *testing.T)
func TestBackupAPI_RestoreConflict(t *testing.T)
func TestBackupAPI_RestoreHopsToOwner(t *testing.T)
func TestBackupAPI_PutPeerSnapshotDoesNotCreateProcess(t *testing.T)
func TestBackupAPI_DeniedWithoutPerm(t *testing.T) // 若夹具能注入 principal
```

`schedule_test.go`：

```go
func TestNext_Hourly(t *testing.T) {
    from := time.Date(2026, 8, 16, 10, 15, 0, 0, time.UTC)
    got, err := backup.Next("0 * * * *", from)
    if err != nil || !got.Equal(time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)) {
        t.Fatalf("%v %v", got, err)
    }
}
func TestNext_EmptyDisabled(t *testing.T) {
    tm, err := backup.Next("", time.Now())
    if err != nil || !tm.IsZero() { t.Fatalf("%v %v", tm, err) }
}
```

`agentcfg`：解析 `backup.s3.bucket`；缺省 schedule 为空。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api ./internal/backup ./internal/agentcfg -run 'TestBackupAPI_|TestNext_|TestLoad' -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

接线：`api.Options` 增加 `Backup *backup.Engine`。`NewServer` mount BackupService。`rpcRuntime.localHandler` 同样 mount LocalOnly BackupAPI。`serveHTTP` 创建 Engine（FSDir 来自 config 或 layout；S3 若 bucket 非空）。`Admitted` 读 `rt.control().View().Member(id).Status == ADMITTED`。`PeerPush` 用 Forwarder.Backup().PutPeerSnapshot。

STALE 占位：`BackupEntry{Snapshot: nil, SourceNode: id, Freshness: "STALE", LastUpdatedUnixMs: now.UnixMilli()}`。

List 只要存在 STALE，不得把响应变成空成功。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api ./internal/backup ./internal/agentcfg ./internal/rpc ./internal/agent -count=1 -timeout 180s`

Expected: PASS。所有 Forwarder 实现编译通过。

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/backup internal/agentcfg internal/rpc internal/agent
git commit -m "feat(api): BackupService with STALE list, peer hop, and schedule"
```

---

### Task 9: CLI backup 命令

**Files:**
- Create: `internal/cli/backup.go`
- Modify: `internal/cli/root.go`、`internal/cli/client.go`、`internal/cli/root_test.go`

**Interfaces:**
- Consumes: 生成的 BackupService client
- Produces:

```
procmesh backup create --sink=fs|s3|peer [--process-id ID]... [--peer-node ID]...
procmesh backup list [--sink S] [--peer-node ID]... [--include-s3]
procmesh backup get SNAPSHOT_ID [--sink S] [--source-node ID] [--payload]
procmesh backup delete SNAPSHOT_ID --sink S
procmesh backup restore SNAPSHOT_ID --sink S --process-id ID --expected-revision N [--source-node ID]
```

可重复 `--process-id` / `--peer-node`。`restore` 可重复一对 `--process-id` + `--expected-revision`：若只支持单进程，文档与测试按**单进程**做（多进程走 API/Web）。计划锁定 CLI restore **一次一个进程**（可重复调用）；Web 可一次多选。

`usageText` 增加上述五行。`client` 增加 `backup` 字段。flags：`sink`、`peer-node`（append）、`include-s3`、`source-node`、`payload`（bool）。`--process-id` 与 `--expected-revision` 已存在。

输出：

- list：每行 `SNAPSHOT SINK NODE FRESHNESS SHA256`；STALE 占位必须打印 `STALE` 大写，即使没有 snapshot_id。
- create/get：打印 snapshot_id、sink、sha256、revision ranges。
- restore：打印 `PROCESS STATUS NEW_REV ERROR`。

- [ ] **Step 1: Write the failing tests**

`root_test.go` 增加 usage 含 `backup create`；`backup` 未知子命令 usageError。若已有 CLI 集成风格（httptest + 真 API），补：

```go
func TestCLI_BackupCreateListRestoreConflict(t *testing.T)
```

否则本任务只测 parse + usage，集成放到 Task 11。**至少**要有：

```go
func TestParseArgs_BackupFlags(t *testing.T)
func TestMain_BackupUsage(t *testing.T)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -run 'Backup|ParseArgs' -count=1`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

未传 `operation_id` 时沿用 CLI 已有自生成。

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cli -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): backup create list get delete restore"
```

---

### Task 10: Web Backup 页（P5 合同）

**Files:**
- Modify: `web/src/lib/rpc.ts`、`web/src/router.ts`、`web/src/components/AppShell.vue`
- Create: `web/src/pages/BackupPage.vue`、`web/src/pages/BackupPage.test.ts`
- Modify: `web/public/locales/en/common.json`、`web/public/locales/zh/common.json`
- Create: `web/e2e/backup.spec.ts`

**Interfaces:**
- Consumes: 生成的 `BackupService`、`FreshnessBadge`、`newOperationId`
- Produces: `/backup` 快照列表 + 创建 + restore 确认

页面要求（P5 质量，不得缩水）：

1. 导航：`backup.read` 才显示 Backup（lucide `Archive`），在 Alerts 后、Users 前。
2. 列表：snapshot_id、sink、node/owner、process 数、sha256 短写、`FreshnessBadge`、`last_updated`。STALE 行 `data-freshness="STALE"`，背景不得是 `#D1FAE5`。
3. 聚合提示：只要存在 STALE，显示横幅 `backup.staleBanner`（中：`部分源不可达，不能视为没有备份。` 英：`Some backup sources are unreachable. This is not an empty catalog.`）。**禁止**在有 STALE 时渲染「No backups」成功空态。
4. 创建：`backup.manage` 才显示。sink 下拉 fs/s3/peer；peer 时多行 node id。Create 带 `operationId`。
5. Restore 确认：**强制**展示 Owner（`snapshot.nodeId`）与每个目标进程的 `expectedRevision` 输入框（预填需用户显式看到，不得暗填提交）。确认按钮在用户看见这两项之前不可用。Viewer 只读，无 Restore/Delete/Create。
6. Restore 返回某 target `CONFLICT` 时页面显示错误，不得当成成功。
7. `npm run i18n:check` 必须过。键至少：`nav.backup`、`backup.title`、`backup.staleBanner`、`backup.noBackups`、`backup.create`、`backup.restore`、`backup.restoreConfirm`、`backup.owner`、`backup.expectedRevision`、`backup.sink`、`backup.delete`。中英都写。

Vitest：

- mock list 含一条 LIVE snapshot 与一条空 snapshot 的 STALE 占位 → STALE 徽章存在、横幅存在、没有「无备份」空态。
- 无 `backup.manage` 时不渲染创建/restore 按钮。
- 打开 restore 对话框：能看到 Owner 文本与 expected revision 输入。

Playwright `web/e2e/backup.spec.ts`：登录后 `/backup` 不 404；导航在有权限时可见。

- [ ] **Step 1: Write the failing tests**

`BackupPage.test.ts` 按上。`AppShell` 若有导航测试，补 `backup.read` 显示 Backup。

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- BackupPage AppShell`

Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

`rpc.ts` 增加 `useBackupClient()`。路由 `/backup`。样式 token 与 Alerts/Batches 一致。List 调 `includeS3: true`，并把当前 ALIVE peers 填进 `peerNodeIds`（从已有 node list / overview 取；若拿不到节点列表，至少 includeS3 + 本机，并在文案说明「指定 Peer 可在 CLI `--peer-node`」——**优先**用 `listNodes`）。

- [ ] **Step 4: Run tests**

```bash
cd web && npm test && npm run i18n:check
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web
git commit -m "feat(web): Backup page with STALE banner and restore confirmation"
```

---

### Task 11: Q5 验收 + embed + 索引

**Files:**
- Create: `internal/agent/q5_accept_test.go`
- Modify: `docs/superpowers/plans/2026-08-16-v1.1.md`
- Modify: `docs/superpowers/plans/2026-08-16-q5-config-backup.md`（本文件 Status → 已完成）
- Rebuild: `internal/web/dist`（`make web`）

**Interfaces:**
- Consumes: 全部前序 + `startClusterAgent` / `joinTwo` / `mustCLI` / `readNodeID`
- Produces: 可脚本化验收

复用 Q2/Q4 helper。测试：

```go
func TestQ5_RestoreWrongRevisionConflict(t *testing.T) {
    // cluster init + apply 进程
    // backup create --sink=fs
    // 再 apply 改 command（新 revision）
    // restore --expected-revision 旧值 → CLI 非 0，输出含 CONFLICT
    // process get 的 command 仍是「改过的」，不是 snapshot 直写回去且 revision 不回退
}

func TestQ5_RestoreAppliesNewRevision(t *testing.T) {
    // create backup（rev1）
    // apply 改成 rev2
    // restore --expected-revision 2
    // latest 变成 3，command 回到 snapshot
}

func TestQ5_PeerPutDoesNotApplyOnReceiver(t *testing.T) {
    addrA, _ := startClusterAgent(t, "")
    addrC, rootC := startClusterAgent(t, "")
    joinTwo(t, addrA, addrC)
    // 在 A apply 一个进程
    // backup create --sink=peer --peer-node {C的 node_id}
    // 打开 C 的 data_dir：backup/peer/<A-node>/*.json 存在
    // 在 C：process list 不得出现 A 的进程名
}

func TestQ5_ListMarksUnreachablePeerSTALE(t *testing.T) {
    addrA, _ := startClusterAgent(t, "")
    addrC, _, cancelC := startClusterAgentCtl(t, "")
    joinTwo(t, addrA, addrC)
    nidC := /* C node id */
    cancelC()
    out := mustCLI(t, addrA, "backup", "list", "--peer-node", nidC)
    if !strings.Contains(strings.ToUpper(out), "STALE") {
        t.Fatalf("want STALE, got %s", out)
    }
}

func TestQ5_Disk95StopsBackupWrites(t *testing.T) {
    // 若难以把真 agent 磁盘打到 95%：在 internal/backup 已有单测。
    // 本验收至少：正常 create 成功；并 go test ./internal/backup -run TestEngine_Disk95 已存在。
    // 若 agent 可注入 DiskPercent，则在此断言 CLI create 返回 DEGRADED。
}
```

更新 `docs/superpowers/plans/2026-08-16-v1.1.md`：Q5 行改为 **已完成** 并链到本文件。

- [ ] **Step 1: Write the failing tests**

按上。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent -run TestQ5_ -count=1 -timeout 180s`

Expected: FAIL

- [ ] **Step 3: 补齐接线缺口并 `make web`**

若 Engine 未挂到 run loop、或 CLI 未注册、或 LocalOnly 未 mount PutPeerSnapshot，在本任务修到验收绿。不要趁机做 V1.2 DR。

覆盖率：`go test ./internal/backup -cover` ≥ 80%。

- [ ] **Step 4: Run tests**

```bash
go test ./internal/backup ./internal/store ./internal/api ./internal/cli ./internal/agent -count=1 -timeout 180s
cd web && npm test && npm run i18n:check
make web
```

Expected: PASS。`internal/backup` cover ≥ 80%。

- [ ] **Step 5: Commit**

```bash
git add internal/agent/q5_accept_test.go internal/web/dist docs/superpowers/plans
git commit -m "test(agent): Q5 backup acceptance and mark plan complete"
```

---

## 规格覆盖自检

| 规格项 | 任务 |
|--------|------|
| 只备份 spec + revision 历史 | 1, 4 |
| 元数据字段齐全 + sha256 | 1, 4 |
| FS 0600 | 2 |
| S3 compatible + 密钥不回显 + fake | 3, 8 |
| Peer 只落盘禁止 apply | 6, 8, 11 |
| Peer 目标必须 ADMITTED | 6, 8 |
| Create 手动 + 可选 cron 默认关 | 4, 8 |
| Restore = ApplySpec 新 revision + expected_revision | 5, 8, 11 |
| 错误 expected_revision → CONFLICT，store 未被直写 | 5, 11 |
| 禁止非 Owner / 禁止用 Peer 重建别人的进程 | 5, 6, 11 |
| 部分 CONFLICT 允许 | 5 |
| List 本机 ∪ S3 ∪ Peer，失败 STALE | 8, 9, 10, 11 |
| backup.read / backup.manage | 8, 10 |
| operation_id | 8, 9 |
| 磁盘 95% DEGRADED | 4, 11 |
| `backup_last_success_unix` | 4, 8 |
| CLI create/list/restore --sink | 9 |
| Backup 页 + restore 确认 Owner + expected revision | 10 |
| Playwright / Vitest restore 409 | 10, 11 |
| 不做加密层 / 整集群 DR / 自动 adopt | 全局约束 |

## 与 P5 / Q1–Q4 的关系

P5 已交付嵌入式 Vue 与 LIVE/STALE/UNKNOWN。Q1 已种子 `backup.read` / `backup.manage`。Q4 交付了 Alerts 聚合 + STALE 横幅模式，Backup 页必须复用同一新鲜度合同。不重做 P5/Q4 已交付页面。
