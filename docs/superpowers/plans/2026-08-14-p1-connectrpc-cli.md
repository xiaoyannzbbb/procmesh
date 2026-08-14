# P1 ConnectRPC + 本机 CLI 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在已完成的 P0 本机 Process Plane 之上，用 Gin 挂载 ConnectRPC（`:9000`）并交付无状态 `procmesh` CLI，使本机可用 `procmesh start/stop/logs` 管理进程。

**Architecture:** CLI 默认连 `127.0.0.1:9000`，只走 ConnectRPC（JSON 与二进制均可）。Agent 的 HTTP 入口改为 Gin：REST 只保留 `/healthz`、`/readyz`、`/metrics`；`ProcessService` / `ConfigService` / `LogService` 全部走 `procmesh.v1`。写操作必须经 `process.Manager`（`operation_id` 幂等、CAS、audit）。P0 的 `/v1/` JSON 仅作为测试兼容层挂在同一端口，不是第二套对外资源模型。本阶段不做集群、鉴权、Web、Agent 间 RPC。

**Tech Stack:** Go 1.23、ConnectRPC（`connectrpc.com/connect`）、Gin、已有 `google.golang.org/protobuf`、`gopkg.in/yaml.v3`（CLI `apply`）、`modernc.org/sqlite`。代码生成沿用仓库已有 `protoc` 方式。

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- Go 版本下限：`1.23`
- CGO-free SQLite only：`modernc.org/sqlite`（禁止 `mattn/go-sqlite3`）
- Linux 是生产保证面；macOS 必须能编译并跑单测 + 非 cgroup 集成
- `process` 不得 import `cluster` 或 `control`（本阶段也不创建这两个包）
- `api` 可以依赖 `process` / `store` / `logmgr` / `errcode`；写操作必须进 `Manager`，禁止 CLI 或 handler 直接改 SQLite
- 日志正文只在文件里，不进 SQLite
- 所有 Mutation 必须带非空 `operation_id`（UUID）；CLI 未传则自己生成
- 配置写必须带 `expected_revision`（创建时为 `0`）
- 错误码沿用 `internal/errcode`：`OK`、`CONFLICT`、`UNAVAILABLE`、`TIMEOUT`、`DENIED`、`DEGRADED`、`DUPLICATE_NODE_ID`、`INCOMPATIBLE_VERSION`、`NOT_FOUND`、`INVALID`
- `CONFLICT` → Connect `FailedPrecondition`，HTTP 映射 409
- 应用错误码放在 Connect error detail（`ErrorInfo.code`），消息为英文
- 对外主协议是 ConnectRPC；REST 仅 `/healthz`、`/readyz`、`/metrics`
- `/healthz`：进程活着即 200
- `/readyz`：本地 store 可用才 200；DEGRADED 返回 503，**不**表示业务进程有问题
- 监听默认 `127.0.0.1:9000`；非环回必须 `--insecure-listen`（P0 已有）
- P4 完成前、尚未 `cluster init`：环回无认证。本阶段不实现登录
- 不另开第四个管理 Unix socket
- CLI 默认 `--server 127.0.0.1:9000`；本阶段忽略/拒绝 `--node`（远程 Owner 是 P3）
- 不实现 Auth/User/Role/Node/Cluster/Audit 聚合/批量/告警/Web
- 测试与代码同目录：`internal/foo/foo_test.go`
- 强制 TDD：先红后绿
- P0 覆盖率门槛保持：`internal/process`、`internal/shim`、`internal/store` ≥ 80%
- 本阶段 `internal/api` 无覆盖率硬门槛，但关键路径必须有测试
- 文档与本计划使用中文；API 错误码与错误消息使用英文

## 规格解读（P1 边界）

来源：`docs/v2-prd/v2-prd.md` 与 `docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。冲突以架构 spec 为准（spec §17 刻意差异）。

1. **P1 可演示出口**（spec §13）：`procmesh start/stop/logs` 管本机。
2. **三个二进制**（spec §2 / §4.1）：本阶段补齐第三个 `procmesh`。它无状态、不常驻。
3. **入口**（spec §3 / §11）：Browser/CLI → 任意 Agent `:9000`（Web + ConnectRPC）。P1 只有本机环回。
4. **服务**（spec §11.1）本阶段只做：`ProcessService`、`ConfigService`、`LogService`。不做 Auth/User/Role/Node/Cluster/MetricsService/AuditService。
5. **CLI 最小集**（spec §11.2 + PRD §66）：`status`、`process list|get|start|stop|restart|logs`、`process apply --file --expected-revision`；并提供 PRD 别名 `procmesh start|stop|restart|logs <name>`。`cluster/user/role/node` 留给后续阶段。
6. **标识**（spec §6.2）：CLI 参数可以是 `process_id` 或 `process_name`，先按 id 查，miss 再按 name 查。
7. **幂等**（PRD §48 / spec §3.9）：重复 `operation_id` 返回上次结果，不得重放。本机 journal 已在 P0；P1 handler 必须走 `Manager`。
8. **CAS**（PRD §35 / spec §6.3）：配置更新带 `expected_revision`，冲突 `CONFLICT` / 409。Rollback 写新 revision，不删历史。
9. **Kill**（PRD §20 强制停止 / spec ProcessService.Kill）：跳过优雅超时，立即发 `kill_signal`，再把 desired 置 `STOPPED`。
10. **日志**（PRD §54–§57 / spec LogService）：Tail / Stream / Download；必须有 `max_tail_lines`、`max_download_size`、`max_concurrent_streams`、`stream_timeout`。不把日志复制进 SQLite。
11. **观测**（PRD §82 / spec §15）：`/metrics` 用 Prometheus 文本；P1 只暴露本机已能得到的计数（uptime、process running），不上 gopsutil、不做历史库。
12. **PRD §68「外部 API 用 REST」**：被 spec §17.5 覆盖——Web 与外部 API 都用 ConnectRPC，不用第二套 REST 资源模型。
13. **P0 JSON `/v1/`**：仅兼容已有 P0 验收测试，挂在同一 Gin 引擎；CLI 与新测试只走 ConnectRPC。
14. **明确不做**：集群、join、mTLS、Write-to-Owner、Raft/RBAC、Vue、批量、告警、跨节点依赖。

## File map（本阶段创建/修改）

```text
proto/procmesh/v1/api.proto
proto/procmesh/v1/api.pb.go                          # 生成
proto/procmesh/v1/procmeshv1connect/api.connect.go   # 生成
cmd/procmesh/main.go
internal/api/server.go
internal/api/server_test.go
internal/api/connecterr.go
internal/api/connecterr_test.go
internal/api/process.go
internal/api/process_test.go
internal/api/config.go
internal/api/config_test.go
internal/api/log.go
internal/api/log_test.go
internal/api/metrics.go
internal/api/convert.go
internal/cli/root.go
internal/cli/root_test.go
internal/cli/client.go
internal/cli/process.go
internal/cli/status.go
internal/cli/specfile.go
internal/cli/specfile_test.go
internal/process/manager.go                          # 扩展
internal/process/manager_test.go
internal/process/types.go                            # Revision 元数据
internal/logmgr/protect.go                           # stream/download 限额
internal/logmgr/follow.go
internal/logmgr/follow_test.go
internal/agent/run.go                                # 改用 api.Server
Makefile
docs/superpowers/plans/2026-08-13-v1-mvp.md
```

生成文件禁止手改。生成命令写入 Makefile 的 `proto` 目标。

---

### Task 1: `procmesh.v1` Protobuf 与代码生成

**Files:**
- Create: `proto/procmesh/v1/api.proto`
- Create: generated `proto/procmesh/v1/api.pb.go`
- Create: generated `proto/procmesh/v1/procmeshv1connect/api.connect.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: 无
- Produces: package `procmesh.v1`（Go：`github.com/qleelulu/procmesh/proto/procmesh/v1;procmeshv1`）；Connect 包 `procmeshv1connect`；服务 `ProcessService`、`ConfigService`、`LogService`

- [ ] **Step 1: 安装生成器（若本机没有）**

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.18.1
```

- [ ] **Step 2: 写入完整 proto（一次性，后续任务不得改字段号）**

`proto/procmesh/v1/api.proto`：

```proto
syntax = "proto3";
package procmesh.v1;
option go_package = "github.com/qleelulu/procmesh/proto/procmesh/v1;procmeshv1";

message ErrorInfo {
  string code = 1;    // CONFLICT, NOT_FOUND, INVALID, DEGRADED, ...
  string message = 2;
}

message MutationMeta {
  string operation_id = 1;
  string operator = 2;
}

message Backoff {
  int64 initial_ms = 1;
  int64 max_ms = 2;
  double multiplier = 3;
}

message RestartPolicy {
  string mode = 1;            // never | always | on-failure
  int32 max_retries = 2;
  int64 retry_window_ms = 3;
  Backoff backoff = 4;
}

message HealthCheck {
  string type = 1;            // alive | http | tcp | exec
  string url = 2;
  string method = 3;
  string address = 4;
  string command = 5;
  int32 expected_status = 6;
  repeated string args = 7;
  int64 initial_delay_ms = 8;
  int64 interval_ms = 9;
  int64 timeout_ms = 10;
  int32 failure_threshold = 11;
  int32 success_threshold = 12;
  bool restart_on_failure = 13;
  int64 restart_cooldown_ms = 14;
}

message LogPolicy {
  int64 max_size = 1;
  int32 max_files = 2;
  int64 max_age_seconds = 3;
  bool compress = 4;
}

message ResourceLimit {
  int64 cpu_quota_millis = 1;
  int64 memory_bytes = 2;
  int64 open_files = 3;
}

message Dependency {
  string process_name = 1;
  string condition = 2;       // STARTED | HEALTHY
}

message ProcessSpec {
  string process_id = 1;
  string name = 2;
  string owner_agent_id = 3;
  string group = 4;
  string command = 5;
  repeated string args = 6;
  string working_directory = 7;
  string run_as_user = 8;
  map<string, string> environment = 9;
  int32 instances = 10;
  bool autostart = 11;
  string stop_signal = 12;
  string kill_signal = 13;
  int64 stop_timeout_ms = 14;
  int32 startup_priority = 15;
  RestartPolicy restart = 16;
  HealthCheck health = 17;
  LogPolicy log = 18;
  ResourceLimit resources = 19;
  repeated Dependency dependencies = 20;
  int64 latest_revision = 21;
}

message Instance {
  string instance_id = 1;
  int32 ordinal = 2;
  string desired = 3;
  string observed = 4;
  string health = 5;
  int32 pid = 6;
  int64 active_revision = 7;
  int32 restart_count = 8;
  int32 exit_code = 9;
  bool has_exit_code = 10;
}

message ProcessView {
  string process_id = 1;
  ProcessSpec spec = 2;
  repeated Instance instances = 3;
}

message ApplyProcessRequest {
  MutationMeta meta = 1;
  int64 expected_revision = 2;
  ProcessSpec spec = 3;
  string comment = 4;
}
message ApplyProcessResponse { ProcessSpec spec = 1; }

message GetProcessRequest { string id_or_name = 1; }
message GetProcessResponse { ProcessView process = 1; }

message ListProcessesRequest {}
message ListProcessesResponse { repeated ProcessView processes = 1; }

message DeleteProcessRequest {
  MutationMeta meta = 1;
  string id_or_name = 2;
  int64 expected_revision = 3;
}
message DeleteProcessResponse {}

message ProcessRefRequest {
  MutationMeta meta = 1;
  string id_or_name = 2;
}
message ProcessRefResponse { ProcessView process = 1; }

message AdoptRequest {
  MutationMeta meta = 1;
  string instance_id = 2;
  int32 pid = 3;
}
message AdoptResponse { ProcessView process = 1; }

service ProcessService {
  rpc ListProcesses(ListProcessesRequest) returns (ListProcessesResponse);
  rpc GetProcess(GetProcessRequest) returns (GetProcessResponse);
  rpc ApplyProcess(ApplyProcessRequest) returns (ApplyProcessResponse);
  rpc DeleteProcess(DeleteProcessRequest) returns (DeleteProcessResponse);
  rpc StartProcess(ProcessRefRequest) returns (ProcessRefResponse);
  rpc StopProcess(ProcessRefRequest) returns (ProcessRefResponse);
  rpc RestartProcess(ProcessRefRequest) returns (ProcessRefResponse);
  rpc KillProcess(ProcessRefRequest) returns (ProcessRefResponse);
  rpc ResetFailure(ProcessRefRequest) returns (ProcessRefResponse);
  rpc AdoptInstance(AdoptRequest) returns (AdoptResponse);
}

message GetConfigRequest { string id_or_name = 1; }
message GetConfigResponse { ProcessSpec spec = 1; }

message UpdateConfigRequest {
  MutationMeta meta = 1;
  string id_or_name = 2;
  int64 expected_revision = 3;
  ProcessSpec spec = 4;
  string comment = 5;
}
message UpdateConfigResponse { ProcessSpec spec = 1; }

message HistoryRequest { string id_or_name = 1; }
message Revision {
  int64 revision = 1;
  string operator = 2;
  int64 timestamp_unix_ms = 3;
  string diff = 4;
  string comment = 5;
}
message HistoryResponse { repeated Revision revisions = 1; }

message DiffRequest {
  string id_or_name = 1;
  int64 from_revision = 2;
  int64 to_revision = 3;
}
message DiffResponse { string diff = 1; }

message RollbackRequest {
  MutationMeta meta = 1;
  string id_or_name = 2;
  int64 to_revision = 3;
  int64 expected_revision = 4;
  string comment = 5;
}
message RollbackResponse { ProcessSpec spec = 1; }

service ConfigService {
  rpc GetConfig(GetConfigRequest) returns (GetConfigResponse);
  rpc UpdateConfig(UpdateConfigRequest) returns (UpdateConfigResponse);
  rpc History(HistoryRequest) returns (HistoryResponse);
  rpc Diff(DiffRequest) returns (DiffResponse);
  rpc Rollback(RollbackRequest) returns (RollbackResponse);
}

message TailLogsRequest {
  string id_or_name = 1;
  string instance_id = 2;   // 空 = 全部 instance 的 stdout
  string stream = 3;        // stdout | stderr；默认 stdout
  int32 lines = 4;          // 0 = 默认 100
}
message TailLogsResponse { repeated string lines = 1; }

message StreamLogsRequest {
  string id_or_name = 1;
  string instance_id = 2;
  string stream = 3;
}
message DownloadLogsRequest {
  string id_or_name = 1;
  string instance_id = 2;
  string stream = 3;
}
message LogChunk { bytes data = 1; bool eof = 2; }

service LogService {
  rpc TailLogs(TailLogsRequest) returns (TailLogsResponse);
  rpc StreamLogs(StreamLogsRequest) returns (stream LogChunk);
  rpc DownloadLogs(DownloadLogsRequest) returns (stream LogChunk);
}
```

- [ ] **Step 3: 更新 Makefile**

```makefile
.PHONY: test proto
test:
	go test ./...
proto:
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc \
		--go_out=. --go_opt=module=github.com/qleelulu/procmesh \
		--connect-go_out=. --connect-go_opt=module=github.com/qleelulu/procmesh \
		proto/shim/v1/shim.proto \
		proto/procmesh/v1/api.proto
```

- [ ] **Step 4: 生成并加入依赖**

```bash
make proto
go get connectrpc.com/connect@v1.18.1
go get github.com/gin-gonic/gin@v1.10.1
go get gopkg.in/yaml.v3@v3.0.1
go mod tidy
```

Expected: 生成文件存在且 `go build ./proto/...` 成功。

- [ ] **Step 5: 写一个编译级测试证明服务名已生成**

`internal/api/proto_gen_test.go`：

```go
package api

import (
	"testing"

	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestGeneratedServiceNames(t *testing.T) {
	if procmeshv1connect.ProcessServiceName != "procmesh.v1.ProcessService" {
		t.Fatalf("process=%s", procmeshv1connect.ProcessServiceName)
	}
	if procmeshv1connect.ConfigServiceName != "procmesh.v1.ConfigService" {
		t.Fatalf("config=%s", procmeshv1connect.ConfigServiceName)
	}
	if procmeshv1connect.LogServiceName != "procmesh.v1.LogService" {
		t.Fatalf("log=%s", procmeshv1connect.LogServiceName)
	}
}
```

Run: `go test ./internal/api/ -count=1 -run TestGeneratedServiceNames`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add proto/procmesh Makefile go.mod go.sum internal/api/proto_gen_test.go
git commit -m "feat: 添加 procmesh.v1 Protobuf 与 Connect 代码生成"
```

---

### Task 2: errcode → Connect 错误映射

**Files:**
- Create: `internal/api/connecterr.go`
- Create: `internal/api/connecterr_test.go`

**Interfaces:**
- Consumes: `internal/errcode.Error`
- Produces:
  - `func ToConnect(err error) error`
  - `func CodeOf(err error) errcode.Code`
  - 映射表（必须一字不差）：

| errcode | connect.Code |
|---------|--------------|
| CONFLICT | FailedPrecondition |
| NOT_FOUND | NotFound |
| INVALID | InvalidArgument |
| DEGRADED | Unavailable |
| UNAVAILABLE | Unavailable |
| TIMEOUT | DeadlineExceeded |
| DENIED | PermissionDenied |
| DUPLICATE_NODE_ID | AlreadyExists |
| INCOMPATIBLE_VERSION | FailedPrecondition |
| 其他 / 非 errcode | Unknown |

Connect error 必须附加 `procmeshv1.ErrorInfo{Code, Message}` detail。`err == nil` 时 `ToConnect` 返回 nil。

- [ ] **Step 1: 写失败测试**

`internal/api/connecterr_test.go`：

```go
package api

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"google.golang.org/protobuf/proto"
)

func TestToConnect_Nil(t *testing.T) {
	if ToConnect(nil) != nil {
		t.Fatal("nil")
	}
}

func TestToConnect_ConflictDetail(t *testing.T) {
	err := ToConnect(errcode.E(errcode.CONFLICT, "revision mismatch"))
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("%T", err)
	}
	if ce.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("code=%v", ce.Code())
	}
	if len(ce.Details()) == 0 {
		t.Fatal("missing detail")
	}
	msg, err := ce.Details()[0].Value()
	if err != nil {
		t.Fatal(err)
	}
	info, ok := msg.(*procmeshv1.ErrorInfo)
	if !ok || info.GetCode() != "CONFLICT" {
		t.Fatalf("%v", msg)
	}
	if !proto.Equal(&procmeshv1.ErrorInfo{Code: "CONFLICT", Message: "CONFLICT: revision mismatch"}, info) &&
		info.GetMessage() == "" {
		t.Fatalf("empty message: %+v", info)
	}
}

func TestToConnect_Table(t *testing.T) {
	cases := []struct {
		in   errcode.Code
		want connect.Code
	}{
		{errcode.NOT_FOUND, connect.CodeNotFound},
		{errcode.INVALID, connect.CodeInvalidArgument},
		{errcode.DEGRADED, connect.CodeUnavailable},
		{errcode.UNAVAILABLE, connect.CodeUnavailable},
		{errcode.TIMEOUT, connect.CodeDeadlineExceeded},
		{errcode.DENIED, connect.CodePermissionDenied},
		{errcode.DUPLICATE_NODE_ID, connect.CodeAlreadyExists},
		{errcode.INCOMPATIBLE_VERSION, connect.CodeFailedPrecondition},
	}
	for _, tc := range cases {
		err := ToConnect(errcode.E(tc.in, "x"))
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != tc.want {
			t.Fatalf("%s -> %v", tc.in, err)
		}
	}
}

func TestToConnect_PlainErrorUnknown(t *testing.T) {
	err := ToConnect(errors.New("boom"))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnknown {
		t.Fatalf("%v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/ -count=1 -run TestToConnect`
Expected: FAIL（`ToConnect` 未定义）。

- [ ] **Step 3: 最小实现**

`internal/api/connecterr.go`：按上表映射；构造 `connect.NewError(code, err)`，再 `ce.AddDetail(&procmeshv1.ErrorInfo{Code: string(c), Message: err.Error()})`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api/ -count=1 -run 'TestToConnect|TestGenerated'`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/api/connecterr.go internal/api/connecterr_test.go
git commit -m "feat: 将 errcode 映射为 Connect 状态与 ErrorInfo"
```

---

### Task 3: Manager 补齐 Resolve / Kill / History / Rollback

**Files:**
- Modify: `internal/process/types.go`（新增 `Revision`）
- Modify: `internal/process/manager.go`（扩展 `StateStore` 与方法）
- Modify: `internal/process/manager_test.go`
- Modify: `internal/store/spec.go`（若需要让 `store.Revision` 转成 `process.Revision`；优先在 store 增加适配，避免 `process` import `store`）

**Interfaces:**
- Consumes: 现有 `Manager`、`store.GetSpecByName`、`store.ListRevisions`、`store.RollbackSpec`、`shim.Client.Stop` / `Signal`
- Produces（签名必须一致）:

```go
type Revision struct {
    Revision  int64
    Operator  string
    Timestamp time.Time
    Diff      string
    Comment   string
}

// 追加到 StateStore（store.Store 必须实现）：
GetSpecByName(ctx context.Context, name string) (ProcessSpec, error)
ListRevisions(ctx context.Context, processID string) ([]Revision, error)
RollbackSpec(ctx context.Context, processID string, toRevision, expectedLatest int64, operator, comment string) (ProcessSpec, error)

func (m *Manager) Resolve(ctx context.Context, idOrName string) (ProcessSpec, error)
func (m *Manager) Kill(ctx context.Context, processID, opID, operator string) error
func (m *Manager) ListRevisions(ctx context.Context, processID string) ([]Revision, error)
func (m *Manager) Rollback(ctx context.Context, processID string, toRevision, expectedLatest int64, opID, operator, comment string) (ProcessSpec, error)
```

`Resolve`：空字符串 → `INVALID`；先 `GetSpec`，若 `NOT_FOUND` 再 `GetSpecByName`。
`Kill`：`operation_id` 经 `beginOp`/`finishOp`，类型 `"kill"`；live orphan（UNKNOWN + 活 PID + 同 boot）必须拒绝，错误与 stop 相同（`adopt required`）；对每个 instance 用 `KillSignal`（空则 `SIGKILL`）经 shim 立即终止（`StopRequest.TimeoutMs=0` 或 `Signal`）；然后 desired=`STOPPED` 并 `reconcileLocked`。幂等重放与其他 mutation 相同。
`Rollback`：走 journal；内部调 `Store.RollbackSpec`；成功后 `ensureInstances`；audit `process.rollback`。
`ListRevisions`：透传 store，不写 journal。

`store.ListRevisions` 当前返回 `store.Revision`（含 `SpecJSON`）。本任务让 `store.Store.ListRevisions` 仍可保留内部类型，但 `StateStore.ListRevisions` 必须返回 `[]process.Revision`。实现上在 `store` 包增加：

```go
func (s *Store) ListRevisions(ctx context.Context, processID string) ([]process.Revision, error)
```

若与现有同名方法冲突：把现有方法改名为 `listRevisionRows`（测试改为走新方法，或同时提供两者，新方法给接口）。**禁止**让 `process` import `store`。

- [ ] **Step 1: 写失败测试（追加到 `manager_test.go`）**

```go
func TestResolve_ByNameAndID(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	got, err := m.ApplySpec(ctx, process.ProcessSpec{Name: "nginx", Command: "/bin/true"}, 0, "op-a", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	byID, err := m.Resolve(ctx, got.ProcessID)
	if err != nil || byID.Name != "nginx" {
		t.Fatalf("%+v %v", byID, err)
	}
	byName, err := m.Resolve(ctx, "nginx")
	if err != nil || byName.ProcessID != got.ProcessID {
		t.Fatalf("%+v %v", byName, err)
	}
	if _, err := m.Resolve(ctx, "missing"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("%v", err)
	}
	if _, err := m.Resolve(ctx, ""); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("%v", err)
	}
}

func TestKill_StopsRunningProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("shim")
	}
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	spec := process.ProcessSpec{ProcessID: "k1", Name: "k1", Command: "/bin/sleep", Args: []string{"60"}}
	inst := startSleep(t, m, st, spec)
	if err := m.Kill(ctx, "k1", "op-kill", "t"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != process.DesiredStopped {
		t.Fatalf("desired=%s", got.Desired)
	}
	if pidAlive(got.PID) && got.Observed != process.ObservedStopped {
		// after kill+reconcile, pid should be dead or observed STOPPED
		if err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		got, _ = st.GetInstance(ctx, inst.InstanceID)
	}
	if got.Observed != process.ObservedStopped && got.Observed != process.ObservedStopping {
		t.Fatalf("observed=%s pid=%d", got.Observed, got.PID)
	}
}

func TestKill_Idempotent(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	if _, err := m.ApplySpec(ctx, process.ProcessSpec{ProcessID: "k2", Name: "k2", Command: "/bin/true"}, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(ctx, "k2", "op-k", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(ctx, "k2", "op-k", "t"); err != nil {
		t.Fatal(err)
	}
}

func TestRollback_CreatesNewRevision(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	s1 := process.ProcessSpec{ProcessID: "r1", Name: "r1", Command: "/bin/true", Args: []string{"a"}}
	if _, err := m.ApplySpec(ctx, s1, 0, "op-1", "t", "c1"); err != nil {
		t.Fatal(err)
	}
	s1.Args = []string{"b"}
	if _, err := m.ApplySpec(ctx, s1, 1, "op-2", "t", "c2"); err != nil {
		t.Fatal(err)
	}
	out, err := m.Rollback(ctx, "r1", 1, 2, "op-rb", "t", "back")
	if err != nil {
		t.Fatal(err)
	}
	if out.LatestRevision != 3 || len(out.Args) != 1 || out.Args[0] != "a" {
		t.Fatalf("%+v", out)
	}
	revs, err := m.ListRevisions(ctx, "r1")
	if err != nil || len(revs) != 3 {
		t.Fatalf("n=%d err=%v", len(revs), err)
	}
}

func TestRollback_Conflict(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	s1 := process.ProcessSpec{ProcessID: "r2", Name: "r2", Command: "/bin/true"}
	if _, err := m.ApplySpec(ctx, s1, 0, "op-1", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rollback(ctx, "r2", 1, 99, "op-rb", "t", ""); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("%v", err)
	}
}
```

`pidAlive` 若测试包不可见，用 `unix.Kill(pid, 0)` 或已有 helper。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/process/ -count=1 -run 'TestResolve_|TestKill_|TestRollback_'`
Expected: FAIL（方法不存在）。

- [ ] **Step 3: 实现**

实现时复用 `beginOp`/`finishOp`/`audit`/`ensureInstances`/`rejectLiveOrphanStop`。Kill 的停止路径参考 `stopInstance`，但 `TimeoutMs=0` 且 signal 为 `KillSignal`。

`store.Store` 为 `ListRevisions` 做适配：扫描现有行，填 `process.Revision`（不要把 `SpecJSON` 暴露给 process 包）。现有依赖 `store.Revision.SpecJSON` 的测试（Diff 会在 Task 5 用）可继续用未导出的内部结构或新增 `GetRevisionSpecJSON`。

- [ ] **Step 4: 跑测试确认通过**

Run:

```bash
go test ./internal/process/ ./internal/store/ -count=1
go test ./internal/process/ ./internal/store/ -cover
```

Expected: PASS；覆盖率仍 ≥ 80%。

- [ ] **Step 5: Commit**

```bash
git add internal/process internal/store
git commit -m "feat: 为 API 补齐 Resolve、Kill、Rollback 与 revision 列表"
```

---

### Task 4: ProcessService

**Files:**
- Create: `internal/api/convert.go`
- Create: `internal/api/process.go`
- Create: `internal/api/process_test.go`

**Interfaces:**
- Consumes: Task 1 proto、Task 2 `ToConnect`、Task 3 `Manager.Resolve/Kill`、现有 `ApplySpec/DeleteSpec/SetDesired/Restart/ResetFailure/Adopt/ListSpecs/ListInstances/Reconcile/PeekOp`
- Produces:

```go
type ProcessAPI struct {
    Mgr      *process.Manager
    Degraded func() bool
}

func (s *ProcessAPI) ListProcesses(...)
func (s *ProcessAPI) GetProcess(...)
func (s *ProcessAPI) ApplyProcess(...)
func (s *ProcessAPI) DeleteProcess(...)
func (s *ProcessAPI) StartProcess(...)
func (s *ProcessAPI) StopProcess(...)
func (s *ProcessAPI) RestartProcess(...)
func (s *ProcessAPI) KillProcess(...)
func (s *ProcessAPI) ResetFailure(...)
func (s *ProcessAPI) AdoptInstance(...)
```

规则：
- `Degraded()==true` 时所有 Mutation 返回 `DEGRADED`（含 Create/Apply）。读（List/Get）仍可用。
- Mutation 缺 `operation_id` → `INVALID`（`operation_id required`）。
- `PeekOp` 已是 SUCCESS/FAILED 则直接返回上次结果，不重放（Apply 用 journal 里的 spec JSON 填 response）。
- Start → `SetDesired(RUNNING)` + `Reconcile`；Stop → `SetDesired(STOPPED)` + `Reconcile`。
- Apply：`expected_revision==0` 为创建；更新必须带已有 id/name。
- `convert.go`：`SpecToProto` / `ProtoToSpec` / `ViewOf`。时长字段：proto 用毫秒，Go 用 `time.Duration`。
- 测试用 `httptest.NewServer` + `procmeshv1connect.NewProcessServiceClient`，不要 mock Manager（用 `newTestManager` 同类真实 store；可把 helper 放到 `internal/api/apitest_test.go` 复制 `newTestManager` 所需最小脚手架，或导出测试构造）。

- [ ] **Step 1: 写失败测试**

覆盖至少：
1. `Apply` 创建 + `Get` by name + `List` 非空
2. `Start` 后 instance desired=RUNNING（可用 `/bin/true` 避免长驻；或 `/bin/sleep 30` 再 Stop）
3. 缺 `operation_id` → `InvalidArgument` 且 detail.code=`INVALID`
4. 第二次 `Apply` 错误 `expected_revision` → `FailedPrecondition` / `CONFLICT`
5. 相同 `operation_id` 的第二次 `Start` 成功且不报错
6. `Degraded` 时 `Start` → `Unavailable` / `DEGRADED`，`List` 仍成功

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/ -count=1 -run TestProcess`
Expected: FAIL。

- [ ] **Step 3: 实现 convert + ProcessAPI**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api/ -count=1`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "feat: 实现本机 ProcessService Connect 接口"
```

---

### Task 5: ConfigService

**Files:**
- Create: `internal/api/config.go`
- Create: `internal/api/config_test.go`

**Interfaces:**
- Consumes: `Manager.Resolve/ApplySpec/Rollback/ListRevisions`；为 Diff 需要两个 revision 的 spec。若 `ListRevisions` 不含 spec JSON，在 `store` 增加：

```go
func (s *Store) GetRevisionSpec(ctx context.Context, processID string, rev int64) (process.ProcessSpec, error)
```

并加到某个 **api 可调用** 的接口上。不要让 `process.Manager` 为了 Diff 变得臃肿：`ConfigAPI` 可以持有：

```go
type RevisionStore interface {
    GetRevisionSpec(ctx context.Context, processID string, rev int64) (process.ProcessSpec, error)
}
```

`Diff`：`from`/`to` 都要存在，否则 `NOT_FOUND`；输出纯文本，格式与 `store.specDiff` 一致（可导出 `store.SpecDiff(old, new process.ProcessSpec) string`，避免复制）。
- `UpdateConfig` 必须 `expected_revision > 0`，否则 `INVALID`。创建走 `ProcessService.ApplyProcess`。
- `Rollback` 走 `Manager.Rollback`。

- [ ] **Step 1: 写失败测试**

1. Apply v1、v2 后 `History` 长度为 2，含 operator/diff/comment
2. `Diff(1,2)` 非空且包含变化字段
3. `Rollback` 到 1 后 latest=3 且 command/args 回到 v1
4. 错误 `expected_revision` 的 Update → CONFLICT
5. 缺 `operation_id` 的 Update/Rollback → INVALID

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/ -count=1 -run TestConfig`
Expected: FAIL。

- [ ] **Step 3: 实现**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api/ ./internal/store/ -count=1`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/store
git commit -m "feat: 实现 ConfigService 的历史、diff 与 rollback"
```

---

### Task 6: LogService 与日志保护

**Files:**
- Create: `internal/logmgr/follow.go`
- Create: `internal/logmgr/follow_test.go`
- Create: `internal/api/log.go`
- Create: `internal/api/log_test.go`
- Modify: `internal/logmgr/logmgr.go`（如需导出常量）

**Interfaces:**
- Consumes: `logmgr.Tail`、`logmgr.InstancePaths`、`Manager.Resolve/ListInstances/Layout`
- Produces:

```go
const (
    MaxTailLines         = 10000
    DefaultTailLines     = 100
    MaxDownloadSize      = 50 << 20 // 50MiB
    MaxConcurrentStreams = 8
    StreamTimeout        = 5 * time.Minute
)

func Follow(ctx context.Context, path string, fromEnd bool) (<-chan []byte, <-chan error)
```

`Follow`：从文件当前末尾（`fromEnd=true`）或从头读；每读到新字节发 chunk；`ctx` 取消时关闭 channel。测试用临时文件写两行再 append。

`LogAPI`：
- Tail：`lines<=0` 则 100；封顶 `MaxTailLines`。`stream` 默认 `stdout`。无日志文件 → 空 `lines`，不是错误。未知 process → `NOT_FOUND`。
- Stream：计入并发；超过 `MaxConcurrentStreams` → `UNAVAILABLE`。`context` 或 `StreamTimeout` 结束。
- Download：从头读，累计超过 `MaxDownloadSize` 后停止并最后一帧 `eof=true`（截断，不 500）。
- 选择 instance：指定 `instance_id` 则只该文件；否则 Tail 合并全部 instance（与 P0 JSON logs 相同）；Stream/Download 未指定 instance 且有多个 → `INVALID`（`instance_id required`）。

- [ ] **Step 1: 写 logmgr Follow 失败测试**

```go
func TestFollow_ReadsAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stdout.log")
	if err := os.WriteFile(p, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, errCh := Follow(ctx, p, false)
	got := readOne(t, ch, errCh)
	if !bytes.Contains(got, []byte("a\n")) {
		t.Fatalf("%q", got)
	}
	if err := os.WriteFile(p, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = readOne(t, ch, errCh)
	if !bytes.Contains(got, []byte("b\n")) {
		t.Fatalf("%q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败，然后实现 Follow，再绿**

Run: `go test ./internal/logmgr/ -count=1 -run TestFollow`
Expected: 先 FAIL 后 PASS。

- [ ] **Step 3: 写 LogService 测试（真实文件 + 真实 Manager layout）**

至少：Tail 默认行数、封顶、缺 process、Download 截断、Stream 取消。

- [ ] **Step 4: 实现 LogAPI 并跑通**

Run: `go test ./internal/api/ ./internal/logmgr/ -count=1`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/logmgr internal/api
git commit -m "feat: 实现 LogService 的 tail/stream/download 与限额"
```

---

### Task 7: Gin 入口、REST 探针、接入 agent.Run

**Files:**
- Create: `internal/api/server.go`
- Create: `internal/api/server_test.go`
- Create: `internal/api/metrics.go`
- Modify: `internal/agent/run.go`（`serveHTTP` 改用 `api.NewServer`）
- Modify: P0 若有测试依赖 `localhttp.NewServerOpts` 的 agent 启动路径，保持 `/healthz`、`/readyz`、`/v1/` 行为不变

**Interfaces:**
- Consumes: ProcessAPI、ConfigAPI、LogAPI、`localhttp.NewServerOpts`（兼容层）
- Produces:

```go
type Server struct {
    Engine *gin.Engine
    HTTP   *http.Server
}

func NewServer(opts Options) (*Server, error)

type Options struct {
    Addr     string
    Mgr      *process.Manager
    Logs     *logmgr.Manager
    Store    RevisionStore // 可为 *store.Store；nil 时 Config.Diff 不可用
    Degraded bool
    Ready    func() error
    Started  time.Time
}
```

挂载：
- `GET /healthz` → 200 `ok`
- `GET /readyz` → Ready/Degraded 逻辑与 P0 `localhttp` **完全相同**（DEGRADED 时 503 且 body `DEGRADED`）
- `GET /metrics` → Prometheus 文本，至少：
  - `procmesh_agent_uptime`（秒，gauge）
  - `procmesh_process_running`（observed=RUNNING 的 instance 数，gauge）
  - Agent 无 Manager 时 running=0，uptime 仍报
- Connect：三个 service handler
- 兼容：把 `localhttp` 的 mux 挂到 `/v1/`，P0 `TestCase*` 继续过
- Gin 使用 `gin.New()`（不要 `gin.Default()` 的 logger 污染测试输出）
- `agent.serveHTTP` 改为 `api.NewServer`，`Serve`/`Shutdown` 语义与现在一致（`http.ErrServerClosed` 不是错误）

- [ ] **Step 1: 写 server 测试**

```go
func TestServer_HealthReadyMetrics(t *testing.T) { /* httptest + GET */ }
func TestServer_ReadyDegraded(t *testing.T) { /* 503 DEGRADED */ }
func TestServer_ConnectAndLegacyJSON(t *testing.T) {
    // Connect ListProcesses 200
    // POST /v1/processes 仍可用（P0 兼容）
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/ -count=1 -run TestServer`
Expected: FAIL。

- [ ] **Step 3: 实现并改 `agent.Run`**

- [ ] **Step 4: 回归**

```bash
go test ./internal/api/ ./internal/agent/ ./internal/localhttp/ -count=1
go test ./... -count=1
```

Expected: 全 PASS。测试输出无 gin debug 噪声。

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/agent
git commit -m "feat: 用 Gin 挂载 ConnectRPC、探针与 P0 JSON 兼容层"
```

---

### Task 8: `procmesh` CLI

**Files:**
- Create: `cmd/procmesh/main.go`
- Create: `internal/cli/root.go`
- Create: `internal/cli/client.go`
- Create: `internal/cli/process.go`
- Create: `internal/cli/status.go`
- Create: `internal/cli/specfile.go`
- Create: `internal/cli/root_test.go`
- Create: `internal/cli/specfile_test.go`

**Interfaces:**
- Consumes: Connect 客户端、`specfile` YAML
- Produces:

```go
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int
```

`cmd/procmesh/main.go` 只有 `os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))`。

全局旗标（在子命令之前或之后都要能解析，采用手动 flag + 第一段子命令，不要引入 cobra）：

| 旗标 | 默认 | 含义 |
|------|------|------|
| `--server` | `127.0.0.1:9000` | Connect 基址，自动补 `http://` |
| `--operation-id` | 空则生成 UUID | 本次 mutation |
| `--operator` | `$USER` 或 `cli` | 操作者 |
| `--node` | 空 | **若非空：stderr 打印英文 `remote --node is not supported until P3`，退出码 2** |

命令：

```text
procmesh status
procmesh process list
procmesh process get <id-or-name>
procmesh process start <id-or-name>
procmesh process stop <id-or-name>
procmesh process restart <id-or-name>
procmesh process kill <id-or-name>
procmesh process logs <id-or-name> [--lines N] [--instance ID] [--stream stdout|stderr]
procmesh process apply --file spec.yaml --expected-revision N [--comment T]
procmesh process delete <id-or-name> --expected-revision N
procmesh process history <id-or-name>
procmesh process rollback <id-or-name> --to N --expected-revision M
procmesh process reset-failure <id-or-name>
procmesh process adopt <instance-id> --pid N
procmesh start <id-or-name>      # 别名
procmesh stop <id-or-name>
procmesh restart <id-or-name>
procmesh logs <id-or-name>
```

未知命令 / 缺参数：退出码 2，stderr 用法。
Connect 业务错误：退出码 1，stderr 打印 `CODE: message`（优先 detail.ErrorInfo）。
成功：
- `status`：打印 `ready` 或 `degraded`（先 GET `/readyz`，再 ListProcesses 打进程数）
- `list`：每行 `name\tid\tdesired\tobserved\thealth\tpid`（多 instance 则多行）
- `get` / `history` / `logs`：人类可读纯文本，不要 JSON（`apply` 成功打印 `process_id revision=N`）

`specfile.Load(path)`：YAML 或 JSON，字段与 P0 DTO 相同的 snake_case（`process_id`、`working_directory`、`stop_timeout_ms`…）。用 `yaml.v3` 解到与 `localhttp.ProcessSpec` 同形的结构体（**复制字段，不要让 cli import localhttp**），再转 `procmeshv1.ProcessSpec`。

- [ ] **Step 1: specfile 单测（不启服务器）**

YAML 含 `name`/`command`/`args`/`restart.mode`，断言 proto 字段。

- [ ] **Step 2: CLI 对 httptest Connect 服务器的测试**

用 Task 4–7 的 `api.NewServer` + 真实 Manager：
1. `process apply` + `process list` 看得到 name
2. `start` 别名退出 0
3. 错误 revision → 退出 1 且 stderr 含 `CONFLICT`
4. `--node x` → 退出 2
5. 未知命令 → 退出 2

- [ ] **Step 3: 跑测试确认失败，再实现，再绿**

Run: `go test ./internal/cli/ -count=1`
Expected: 先红后绿。

- [ ] **Step 4: `go build -o /tmp/procmesh ./cmd/procmesh` 必须成功**

- [ ] **Step 5: Commit**

```bash
git add cmd/procmesh internal/cli
git commit -m "feat: 添加本机 procmesh CLI"
```

---

### Task 9: P1 验收（CLI × 活 Agent）

**Files:**
- Create: `internal/agent/p1_cli_test.go`

**Interfaces:**
- Consumes: `agent.Run`、`cli.Main`
- Produces: P1 可演示出口的回归测试

用 `agent.Run` 听 `127.0.0.1:0`（已有 `OnListen`），对实际地址调 `cli.Main`：

1. `process apply --file` 一个 `/bin/sleep 60` spec（`expected-revision 0`）
2. `process start <name>`
3. `process list` 含 RUNNING 或 STARTING
4. `logs <name>` 退出 0
5. 第二次 apply 不改 revision 旗标 → CONFLICT / 退出 1
6. 相同 `--operation-id` 的第二次 `restart` 退出 0
7. `process stop <name>` 后 desired=STOPPED
8. `/readyz` 200；破坏 store 后（可选，不重复 P0 Case 11）不作为本任务必做

Linux/macOS 都跑，不要 `t.Skip`。需要 shim：复用 `internal/agent` 里已有的 `startSleepAgent` 脚手架或等价物。

- [ ] **Step 1: 写失败测试**

- [ ] **Step 2: 跑测试确认失败（若 CLI/server 已通则会直接绿；若缺胶水则红）**

Run: `go test ./internal/agent/ -count=1 -run TestP1_`

- [ ] **Step 3: 补齐遗漏胶水（若有）**

- [ ] **Step 4: 全量回归**

```bash
go test ./... -count=1
go test ./internal/process/ ./internal/shim/ ./internal/store/ -cover
```

Expected: 全 PASS；三包覆盖率 ≥ 80%。

- [ ] **Step 5: Commit**

```bash
git add internal/agent
git commit -m "test: 增加 P1 CLI 对本机 Agent 的验收"
```

---

## Self-review（计划 vs 规格）

| 规格项 | 任务 |
|--------|------|
| Gin + ConnectRPC `:9000` | 7 |
| ProcessService CRUD/Start/Stop/Restart/Kill/ResetFailure/Adopt | 3–4 |
| ConfigService Get/Update/History/Diff/Rollback | 3、5 |
| LogService Tail/Stream/Download | 6 |
| `/healthz` `/readyz` `/metrics` | 7 |
| Mutation `operation_id` + journal 幂等 | 4–5、8–9 |
| `expected_revision` CAS → CONFLICT/409 | 4–5、8 |
| CLI 默认环回、自生成 UUID、无第四 socket | 8 |
| `procmesh start/stop/logs` 演示出口 | 8–9 |
| PRD 日志保护上限 | 6 |
| 环回无认证（P4 前） | 7（不挂 auth） |
| 保留 P0 JSON 使 Cases 3/5/10/11 仍过 | 7 |
| 不做集群/RBAC/Web/批量 | 全计划省略 |

刻意延后：`AuthService` 等、`--node` 转发、mTLS `:9001`、Vue、`MetricsService` RPC（只有 REST `/metrics` 子集）。
