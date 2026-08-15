# P4 Raft + Users + RBAC + Admission + CRL Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在已完成的 P3 mTLS Write-to-Owner 之上，交付内嵌 Raft 控制面、用户/会话/API Token、RBAC、Join 准入、CRL + `node remove`，并在 `cluster init` 之后关闭环回无认证，使登录与权限生效。

**Architecture:** `cluster init` 的节点 bootstrap 为唯一 Raft voter（`:9002`）。加入者作为 Raft non-voter 接收 FSM apply，从而任意 Owner 都能独立验 session / token / RBAC。`:9000` 在集群已初始化后强制用户会话或 API Token（`healthz`/`readyz`/`metrics`、未入群的 `Init`、以及加入者尚无证书的 `Join`/`RequestJoin`/`Login` 除外）。远程 Mutation 入口先做 RBAC，再经 mTLS 转发到 Owner；入口只转发 `user_id` + `session_id`（或 `token_id`），Owner 用本地 Raft 缓存再验，不信任入口的「已授权」声明。`node remove` 删除 membership **并且** 吊销证书；被吊销节点再连拒绝。失 quorum 时 Process Plane 与本地 Process 读写仍开；`user.*` / `role.*` / `node.remove` / 准入写拒绝；远程 Mutation 受 RBAC 缓存 TTL（默认 5 分钟）约束。`process` 不得 import `cluster` / `control` / `rpc` / `auth`。

**Tech Stack:** Go 1.23、已有 ConnectRPC + Gin + argon2id + memberlist、`github.com/hashicorp/raft`、`github.com/hashicorp/raft-boltdb/v2`、`go.etcd.io/bbolt`、标准库 `crypto/x509`（CRL）。

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- Go 版本下限：`1.23`
- CGO-free SQLite only：`modernc.org/sqlite`（禁止 `mattn/go-sqlite3`）
- Linux 是生产保证面；macOS 必须能编译并跑单测 + 非 cgroup 集成
- `process` 不得 import `cluster`、`control`、`rpc` 或 `auth`
- `cluster` 与 `process` 只交换 summary DTO（经接口/回调），禁止 cluster 依赖 process 内部状态机
- 日志正文只在文件里，不进 SQLite
- **禁止把 Process runtime / logs / 全量 spec 写入 Raft**
- 所有 Mutation 必须带非空 `operation_id`（UUID）；CLI 未传则自己生成
- 无 `operation_id` 的远程写必须拒绝（`INVALID`）
- 错误码沿用 `internal/errcode`：`OK`、`CONFLICT`、`UNAVAILABLE`、`TIMEOUT`、`DENIED`、`DEGRADED`、`DUPLICATE_NODE_ID`、`INCOMPATIBLE_VERSION`、`NOT_FOUND`、`INVALID`
- 应用错误码放在 Connect error detail（`ErrorInfo.code`），消息为英文
- 对外主协议是 ConnectRPC；REST 仅 `/healthz`、`/readyz`、`/metrics`
- 监听默认 `127.0.0.1:9000`、`127.0.0.1:9001`、`127.0.0.1:9002`、`127.0.0.1:7946`；非环回必须 `--insecure-listen`
- **`cluster init` 成功后必须关闭环回无认证。** 禁止在已入群节点上保留无认证入口。尚未 `cluster init` 的 Agent 仍允许本机环回无认证（方便单机自测）
- Agent RPC 必须 mTLS。证书含 `cluster_id`、`node_id`（URI SAN `procmesh://<cluster_id>/<node_id>`）。RPC 校验：集群匹配、证书有效、**未在 CRL**
- cluster secret **不**作为日常 RPC 凭证
- 远程 Mutation 必须 Direct RPC 到 Owner，禁止「改本地副本再 Gossip」
- 禁止因对端 FAILED 而在本机创建对方的 Process
- 入口不改权威副本。Owner 不信任入口的「已授权」声明
- 密码 argon2id，最短 10。API Token / join token 只存哈希，明文只显示一次
- Session：HttpOnly + SameSite=Lax + TTL 默认 12h，记录在 Raft。登录限流每账号每分钟 5 次；连续 10 次失败锁 15 分钟
- 不实现 MFA / OIDC / LDAP / Web UI（P5）/ `command.execute` API（权限名预留，功能开关默认关且本阶段不提供命令执行入口）
- V1.0 RBAC 范围只支持 **Cluster** 与 **Agent**。不实现 Agent Group / Process Group
- 内置角色：Super Admin、Cluster Admin、Operator、Viewer。权限名按 PRD §16。`command.execute*` 默认不授予除 Super Admin 外的任何人
- FSM 只存：cluster_id、membership 授权、证书与 CRL、users、roles、permissions、bindings、cluster/security policy、join tokens、session 与 API token 元数据
- 失 quorum：Process Plane 全开；本地 Process 读写开；远程 Mutation 受 RBAC 缓存 TTL 约束（默认 5 分钟）；`user.*` / `role.*` / `node.remove` / 准入 / 安全策略写全部拒绝
- 已登录用户失 quorum 时仍可：`process.read`、status、logs.read
- V1.0 `protocol_version = 1`（`internal/version.Protocol`）；不兼容则 `INCOMPATIBLE_VERSION`
- Cluster CA 私钥与 cluster secret、admin bootstrap、agent key、session 文件权限必须 `0600`
- 测试与代码同目录：`internal/foo/foo_test.go`
- 强制 TDD：先红后绿
- P0 覆盖率门槛保持：`internal/process`、`internal/shim`、`internal/store` ≥ 80%
- 本阶段 `internal/control` 与 `internal/auth` 覆盖率门槛 ≥ 80%
- 文档与本计划使用中文；API 错误码与错误消息使用英文
- 生成的 proto Go 文件禁止手改；改完 proto 必须 `make proto`

## 规格解读（P4 边界）

来源：`docs/v2-prd/v2-prd.md` 与 `docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。冲突以架构 spec 为准。

1. **P4 可演示出口**（spec §13）：登录与权限生效。`cluster init` 后无会话的 `process list` 必须 `DENIED`；admin 登录后可管理；Viewer 不能 restart。
2. **Raft**（spec §10 / §5.1）：hashicorp/raft，日志 raft-boltdb/v2，FSM 快照 bbolt。默认端口 `:9002`。init 节点 bootstrap 为唯一 voter。加入者 `AddNonvoter`。显式 `node promote` 才变 voter。默认目标 3 voter，可配 5；本阶段实现 promote，不自动把加入者升级为 voter。
3. **认证**（spec §10 / §16）：Username+Password 与 API Token。Session 在 Raft。Cookie 给未来 Web；CLI 用 `Authorization: Bearer` + 本机 session 文件。
4. **RBAC**（spec §10 / PRD §16–19）：User → Role → Permission。一个用户可绑多个 Role。Scope 仅 Cluster / Agent。
5. **远程 Mutation**（spec §9.5）：`Client → 入口 :9000 RBAC → mTLS → Owner :9001 再验 RBAC+证书+CRL → operation_id 去重 → 本地 commit`。
6. **Join**（spec §9.3）：向任一 ALIVE Agent 提交 token → 该 Agent 把签发请求交给 Raft leader → leader 校验并消费 token → 用 Cluster CA 签发 → 写入 membership → 返回证书与 CA。重复 `node_id`：`DUPLICATE_NODE_ID`。被 `remove` 的 `node_id` 再 join：`DENIED`。
7. **Remove**（spec §9.3 / Case 8）：membership 删除 **并且** 证书吊销。仅 Gossip LEFT 不算安全删除。被吊销节点再连：拒绝。
8. **失 quorum**（spec §10 / Case 9）：3 节点内存 Raft 测拒绝写；Linux 验收：voter 不可达时本地 Process 仍开，`user.create` / `node.remove` 拒绝。
9. **明确不做**：Vue / LIVE 徽章、MFA、LDAP/OIDC、证书轮换管理、command.execute 执行入口、把 Process 写入 Raft、Agent Group / Process Group。

## HTTP 头与 Cookie（本阶段锁定）

```text
Authorization: Bearer <session_id|api_token>
Cookie: procmesh_session=<session_id>
X-CSRF-Token: <csrf>                  # 仅 Cookie 会话的 Mutation；Bearer 不需要
Procmesh-Target-Node                  # 已有，CLI --node
Procmesh-Source-Node                  # 已有，入口本机 node_id
Procmesh-User-ID                      # 入口转发
Procmesh-Session-ID                   # 入口转发（session 认证时）
Procmesh-Token-ID                     # 入口转发（API token 认证时）
```

`:9001` 忽略 `Procmesh-Target-Node`。Owner 用 `Procmesh-Session-ID` / `Procmesh-Token-ID` 查 Raft 缓存，**不**信任入口声明的权限列表。

Session id 前缀 `pms_`，API token 明文前缀 `pmt_`，join token 仍是 `pmj_`。

CLI session 文件：`~/.config/procmesh/session`（0600），JSON：

```json
{"server":"http://127.0.0.1:9000","session_id":"pms_...","user_id":"...","expires_unix":0}
```

## 内置角色权限（本阶段锁定）

权限常量（`internal/auth/perm.go` 与 FSM 共用同一组字符串）：

```text
cluster.read  cluster.manage
node.read  node.manage  node.remove
process.read  process.create  process.update  process.delete
process.start  process.stop  process.restart
process.config.read  process.config.update
process.logs.read  process.logs.download
user.read  user.create  user.update  user.delete
role.read  role.manage
audit.read
command.execute  command.execute.batch
```

| Role id | 授予 |
|---------|------|
| `super_admin` | 全部 |
| `cluster_admin` | 除 `user.delete`、`role.manage`、`command.execute*` 外的全部 |
| `operator` | `cluster.read` `node.read` `process.read` `process.start` `process.stop` `process.restart` `process.config.read` `process.logs.read` |
| `viewer` | `cluster.read` `node.read` `process.read` `process.config.read` `process.logs.read` |

`cluster init` 创建的初始 admin 绑定 `super_admin`（Cluster scope）。

Scope：`CLUSTER`（`scope_id` 空）或 `AGENT`（`scope_id` = `node_id`）。检查目标节点时：Cluster binding 对所有节点生效；Agent binding 仅当目标 `node_id` 匹配。

## 鉴权例外（已入群的 :9000）

**不需要会话：**

- `GET /healthz` `GET /readyz` `GET /metrics`
- `AuthService.Login`
- `ClusterService.Join`（加入者尚无证书，持 token）
- `ClusterService.RequestJoin`（加入者本机调用，持 token）

**尚未 `cluster init`：** 全部 `:9000` 保持无认证（与 P0–P3 单机行为一致）。

**`NewServer` 未注入 `Auth`：** 单测保持无认证。Agent 在集群已初始化后必须注入 `Auth`。

## File map（本阶段创建/修改）

```text
proto/procmesh/v1/api.proto
proto/procmesh/v1/api.pb.go                          # 生成
proto/procmesh/v1/procmeshv1connect/api.connect.go   # 生成
internal/control/fsm.go
internal/control/fsm_test.go
internal/control/command.go
internal/control/raft.go
internal/control/raft_test.go
internal/control/admission.go
internal/control/admission_test.go
internal/control/crl.go
internal/control/init.go                             # LoadAdminBootstrap
internal/control/pki.go                              # 签发时返回 serial
internal/auth/perm.go
internal/auth/auth.go
internal/auth/auth_test.go
internal/auth/rbac.go
internal/auth/rbac_test.go
internal/auth/ratelimit.go
internal/rpc/header.go                               # User/Session/Token 头
internal/rpc/header_test.go
internal/rpc/tls.go                                  # CRL 钩子
internal/rpc/tls_test.go
internal/rpc/server.go                               # ServerTLS 增加 Revoked
internal/api/authn.go
internal/api/authn_test.go
internal/api/authapi.go
internal/api/authapi_test.go
internal/api/user.go
internal/api/user_test.go
internal/api/role.go
internal/api/role_test.go
internal/api/server.go                               # 挂 Auth/User/Role + interceptor
internal/api/process.go                              # Require perm
internal/api/config.go
internal/api/log.go
internal/api/node.go                                 # Remove + Promote + token→Raft
internal/api/clusterapi.go                           # Join→leader
internal/api/clusterapi_test.go                      # 翻转「init 后仍无认证」
internal/api/metrics.go                              # raft quorum
internal/api/metrics_test.go
internal/api/proto_gen_test.go
internal/cluster/check.go                            # REMOVED/REVOKED → DENIED
internal/cluster/check_test.go
internal/agentcfg/load.go                            # control.listen/advertise
internal/agentcfg/load_test.go
internal/paths/paths.go                              # RaftDir
internal/agent/run.go
internal/agent/raft.go
internal/agent/rpc.go                                # CRL + Owner 再验
internal/cli/root.go
internal/cli/client.go                               # Bearer + Auth/User/Role clients
internal/cli/auth.go
internal/cli/user.go
internal/cli/node.go                                 # remove / promote
internal/cli/root_test.go
internal/agent/p3_accept_test.go                     # login 后再调 CLI
internal/agent/cluster_accept_test.go
internal/agent/p4_accept_test.go                     # Case 8 / Case 9
docs/superpowers/plans/2026-08-13-v1-mvp.md
```

---

### Task 1: Proto — Auth / User / Role / Remove / Promote

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Generate: `proto/procmesh/v1/api.pb.go`
- Generate: `proto/procmesh/v1/procmeshv1connect/api.connect.go`
- Modify: `internal/api/proto_gen_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/node.go`
- Create: `internal/api/authapi.go`
- Create: `internal/api/user.go`
- Create: `internal/api/role.go`

**Interfaces:**
- Consumes: 现有 `NodeService` / `ClusterService` / `MutationMeta`
- Produces: 下列 RPC 与消息；生成后 `NodeAPI` / 新 handler 必须实现接口，本任务只回 `UNAVAILABLE` 桩（英文消息 `not implemented`）

在 `api.proto` 的 `JoinClusterRequest` **追加**字段（不要改已有编号）：

```protobuf
  string raft_address = 11;
```

`JoinClusterResponse` 追加：

```protobuf
  string raft_leader = 5;
```

`ClusterOverviewResponse` 追加：

```protobuf
  bool control_quorum = 4;
  string control_leader = 5;
```

`NodeService` 追加：

```protobuf
message RemoveNodeRequest {
  MutationMeta meta = 1;
  string node_id = 2;
}
message RemoveNodeResponse {}

message PromoteNodeRequest {
  MutationMeta meta = 1;
  string node_id = 2;
}
message PromoteNodeResponse {}

// 加到 service NodeService
rpc RemoveNode(RemoveNodeRequest) returns (RemoveNodeResponse);
rpc PromoteNode(PromoteNodeRequest) returns (PromoteNodeResponse);
```

文件末尾追加完整服务：

```protobuf
message LoginRequest {
  string username = 1;
  string password = 2;
}
message LoginResponse {
  string session_id = 1;
  string user_id = 2;
  string username = 3;
  int64 expires_unix = 4;
  string csrf_token = 5;
}

message LogoutRequest { MutationMeta meta = 1; }
message LogoutResponse {}

message CreateAPITokenRequest {
  MutationMeta meta = 1;
  string name = 2;
  int64 ttl_seconds = 3; // 0 = 不过期
}
message CreateAPITokenResponse {
  string token_id = 1;
  string token = 2; // 只返回一次
  int64 expires_unix = 3;
}

message RevokeAPITokenRequest {
  MutationMeta meta = 1;
  string token_id = 2;
}
message RevokeAPITokenResponse {}

service AuthService {
  rpc Login(LoginRequest) returns (LoginResponse);
  rpc Logout(LogoutRequest) returns (LogoutResponse);
  rpc CreateAPIToken(CreateAPITokenRequest) returns (CreateAPITokenResponse);
  rpc RevokeAPIToken(RevokeAPITokenRequest) returns (RevokeAPITokenResponse);
}

message User {
  string user_id = 1;
  string username = 2;
  string display_name = 3;
  string email = 4;
  string status = 5; // ACTIVE | DISABLED | LOCKED
  int64 created_unix = 6;
  int64 last_login_unix = 7;
}

message ListUsersRequest {}
message ListUsersResponse { repeated User users = 1; }

message CreateUserRequest {
  MutationMeta meta = 1;
  string username = 2;
  string password = 3;
  string display_name = 4;
  string email = 5;
}
message CreateUserResponse { User user = 1; }

message DisableUserRequest {
  MutationMeta meta = 1;
  string user_id = 2;
}
message DisableUserResponse { User user = 1; }

service UserService {
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse);
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse);
  rpc DisableUser(DisableUserRequest) returns (DisableUserResponse);
}

message Role {
  string role_id = 1;
  string name = 2;
  repeated string permissions = 3;
}

message Binding {
  string user_id = 1;
  string role_id = 2;
  string scope_type = 3; // CLUSTER | AGENT
  string scope_id = 4;
}

message ListRolesRequest {}
message ListRolesResponse {
  repeated Role roles = 1;
  repeated Binding bindings = 2;
}

message CreateRoleRequest {
  MutationMeta meta = 1;
  string name = 2;
  repeated string permissions = 3;
}
message CreateRoleResponse { Role role = 1; }

message GrantRoleRequest {
  MutationMeta meta = 1;
  string user_id = 2;
  string role_id = 3;
  string scope_type = 4; // CLUSTER | AGENT
  string scope_id = 5;
}
message GrantRoleResponse { Binding binding = 1; }

service RoleService {
  rpc ListRoles(ListRolesRequest) returns (ListRolesResponse);
  rpc CreateRole(CreateRoleRequest) returns (CreateRoleResponse);
  rpc GrantRole(GrantRoleRequest) returns (GrantRoleResponse);
}
```

桩实现统一：

```go
func unimplemented() error {
    return ToConnect(errcode.E(errcode.UNAVAILABLE, "not implemented"))
}
```

`NewServer` 挂上三个新 handler。`NodeAPI` 增加 `RemoveNode` / `PromoteNode` 桩。

- [ ] **Step 1: 写失败测试**

在 `internal/api/proto_gen_test.go` 追加：

```go
func TestProto_P4ServicesGenerated(t *testing.T) {
	if procmeshv1connect.AuthServiceName != "procmesh.v1.AuthService" {
		t.Fatalf("auth=%s", procmeshv1connect.AuthServiceName)
	}
	if procmeshv1connect.UserServiceName != "procmesh.v1.UserService" {
		t.Fatalf("user=%s", procmeshv1connect.UserServiceName)
	}
	if procmeshv1connect.RoleServiceName != "procmesh.v1.RoleService" {
		t.Fatalf("role=%s", procmeshv1connect.RoleServiceName)
	}
	_ = (&procmeshv1.JoinClusterRequest{}).GetRaftAddress
	_ = (&procmeshv1.LoginResponse{}).GetSessionId
	_ = (&procmeshv1.RemoveNodeRequest{}).GetNodeId
	_ = (&procmeshv1.PromoteNodeRequest{}).GetNodeId
	_ = (&procmeshv1.ClusterOverviewResponse{}).GetControlQuorum
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestProto_P4ServicesGenerated -count=1`

Expected: FAIL（类型/常量不存在）

- [ ] **Step 3: 改 proto、`make proto`、加桩、挂路由**

Run: `make proto`

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api -count=1`

Expected: PASS（现有测试不得因缺少接口方法编译失败）

- [ ] **Step 5: Commit**

```bash
git add proto/procmesh/v1/api.proto proto/procmesh/v1/api.pb.go \
  proto/procmesh/v1/procmeshv1connect/api.connect.go \
  internal/api/proto_gen_test.go internal/api/server.go \
  internal/api/node.go internal/api/authapi.go \
  internal/api/user.go internal/api/role.go
git commit -m "$(cat <<'EOF'
feat: 添加 Auth/User/Role proto 与 Remove/Promote RPC

EOF
)"
```

---

### Task 2: Control FSM（无网络）

**Files:**
- Create: `internal/control/command.go`
- Create: `internal/control/fsm.go`
- Create: `internal/control/fsm_test.go`
- Modify: `internal/control/init.go`（导出 `LoadAdminBootstrap`）
- Modify: `internal/control/init_test.go`
- Modify: `internal/control/pki.go`（`SignCSR` 仍返回 cert PEM；新增 `CertSerial(certPEM []byte) (string, error)` 返回大写十六进制 serial）

**Interfaces:**
- Consumes: `HashPassword` / `VerifyPassword` / `admin.bootstrap`
- Produces:

```go
const (
    MinPasswordLen = 10
    SessionTTL     = 12 * time.Hour
    LockAfter      = 10
    LockFor        = 15 * time.Minute
    DefaultRBACCacheTTL = 5 * time.Minute
)

type ScopeType string
const (
    ScopeCluster ScopeType = "CLUSTER"
    ScopeAgent   ScopeType = "AGENT"
)

type UserStatus string
const (
    UserActive   UserStatus = "ACTIVE"
    UserDisabled UserStatus = "DISABLED"
    UserLocked   UserStatus = "LOCKED"
)

type MemberStatus string
const (
    MemberAdmitted MemberStatus = "ADMITTED"
    MemberRemoved  MemberStatus = "REMOVED"
    MemberRevoked  MemberStatus = "REVOKED"
)

type User struct {
    ID, Username, PasswordHash, DisplayName, Email string
    Status UserStatus
    CreatedUnix, LastLoginUnix, LockedUntilUnix int64
    FailCount int
}

type Role struct {
    ID, Name string
    Perms    []string
}

type Binding struct {
    UserID, RoleID, ScopeID string
    Scope                   ScopeType
}

type Session struct {
    ID, UserID, CSRF string
    ExpiresUnix      int64
}

type APIToken struct {
    ID, UserID, Name, Hash string
    ExpiresUnix            int64
    Revoked                bool
}

type JoinToken struct {
    ID, Hash string
    ExpiresUnix int64
    Remaining   int
    Revoked     bool
}

type Member struct {
    NodeID, RaftAddr, CertSerial string
    Status                       MemberStatus
}

type Policy struct {
    RBACCacheTTL time.Duration
}

type State struct {
    ClusterID string
    Users     map[string]User      // by username
    UsersByID map[string]string    // id → username
    Roles     map[string]Role
    Bindings  []Binding
    Sessions  map[string]Session
    APITokens map[string]APIToken
    JoinTokens map[string]JoinToken // by id
    Members   map[string]Member     // by node_id
    CRL       map[string]struct{}   // cert serial hex
    Policy    Policy
}

func NewState() *State

type Command struct {
    Type string          `json:"type"`
    Body json.RawMessage `json:"body"`
}

// Type 取值（字符串必须完全一致）：
// bootstrap, user_put, user_disable, login_ok, login_fail, session_put, session_del,
// token_put, token_revoke, role_put, bind_put, join_token_put, join_token_consume,
// join_token_revoke, member_put, member_remove, crl_add

func (s *State) Apply(cmd Command, now time.Time) error
func EncodeCommand(typ string, body any) (Command, error)

func (s *State) Check(userID, perm, targetNodeID string) bool
func (s *State) SessionByID(id string) (Session, bool)
func (s *State) TokenByPlain(plain string) (APIToken, bool) // sha256 比对
func (s *State) JoinTokenByPlain(plain string) (JoinToken, bool)
func (s *State) Member(nodeID string) (Member, bool)
func (s *State) SerialRevoked(serial string) bool

type FSM struct { /* raft.FSM；mu + *State */ }
func NewFSM() *FSM
func (f *FSM) Apply(l *raft.Log) any        // 解码 Command 后 State.Apply
func (f *FSM) Snapshot() (raft.FSMSnapshot, error)
func (f *FSM) Restore(io.ReadCloser) error
func (f *FSM) View() State                  // 深拷贝
func LoadAdminBootstrap(dir string) (username, passwordHash string, err error)
func CertSerial(certPEM []byte) (string, error)
```

`bootstrap` body：

```go
type BootstrapBody struct {
    ClusterID    string
    AdminUser    string
    PasswordHash string
    AdminUserID  string
    NowUnix      int64
}
```

`Apply(bootstrap)` 必须：写入 `ClusterID`；创建四个内置角色（id 分别为 `super_admin` `cluster_admin` `operator` `viewer`，权限按本计划锁定表）；创建 admin 用户（`ACTIVE`）；绑定 `{admin, super_admin, CLUSTER, ""}`；`Policy.RBACCacheTTL = 5 * time.Minute`。重复 bootstrap → `CONFLICT`。

`user_put`：密码哈希已算好；username 空或密码哈希空 → `INVALID`。重名 → `CONFLICT`。

`login_fail`：`FailCount++`；达到 10 则 `LOCKED` 且 `LockedUntilUnix = now+15m`。

`login_ok`：清 FailCount、解 LOCKED（若已过期）、写 `LastLoginUnix`。

`session_put`：TTL 默认 12h。

`join_token_consume`：按明文哈希查找；revoked/expired/remaining<=0 → 与现有 `ConsumeToken` 相同错误码（`DENIED` / `INVALID`）。

`member_remove`：状态改 `REVOKED`，并把该成员 `CertSerial` 放入 `CRL`。

`Check`：用户不存在 / 非 ACTIVE → false；任一 binding 的角色含该 perm，且 scope 为 CLUSTER 或（AGENT 且 `scope_id==targetNodeID` 或 `targetNodeID==""`）→ true。

快照：JSON 编码整个 `State`。`Restore` 覆盖内存。

- [ ] **Step 1: 写失败测试**

`internal/control/fsm_test.go` 至少包含：

```go
func TestFSM_BootstrapCreatesAdminAndBuiltinRoles(t *testing.T)
func TestFSM_BootstrapConflict(t *testing.T)
func TestFSM_UserPutDuplicateUsername(t *testing.T)
func TestFSM_LoginFailLocksAfter10(t *testing.T)
func TestFSM_SessionPutAndGet(t *testing.T)
func TestFSM_APITokenHashOnly(t *testing.T)
func TestFSM_JoinTokenConsumeOnce(t *testing.T)
func TestFSM_MemberRemoveAddsCRL(t *testing.T)
func TestFSM_CheckSuperAdminAllowsAll(t *testing.T)
func TestFSM_CheckViewerDeniesRestart(t *testing.T)
func TestFSM_CheckAgentScope(t *testing.T)
func TestFSM_SnapshotRestore(t *testing.T)
func TestLoadAdminBootstrap(t *testing.T)
func TestCertSerial(t *testing.T)
```

`TestFSM_LoginFailLocksAfter10`：连续 10 次 `login_fail` 后用户 `LOCKED` 且 `LockedUntilUnix == now.Add(15*time.Minute).Unix()`。

`TestFSM_APITokenHashOnly`：`token_put` 的 body 只含 hash；`TokenByPlain` 能命中；`View()` 的 token 不得包含明文。

`TestFSM_CheckAgentScope`：binding `{u, operator, AGENT, "node-c"}` 对 `process.restart` + target `node-c` 为 true，对 `node-a` 为 false。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/control -run TestFSM_ -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 command / fsm / LoadAdminBootstrap / CertSerial**

内置角色权限表必须与本计划「内置角色权限」一字不差。`command.execute` / `command.execute.batch` 只给 `super_admin`。

`LoadAdminBootstrap` 读 `dir/admin.bootstrap`；缺文件 → `NOT_FOUND`。

- [ ] **Step 4: 跑测试确认通过 + 覆盖率**

Run: `go test ./internal/control -cover -count=1`

Expected: PASS，本包 ≥ 80%

- [ ] **Step 5: Commit**

```bash
git add internal/control/command.go internal/control/fsm.go \
  internal/control/fsm_test.go internal/control/init.go \
  internal/control/init_test.go internal/control/pki.go \
  internal/control/pki_test.go
git commit -m "$(cat <<'EOF'
feat: 实现 Cluster Control FSM（用户、RBAC、token、CRL）

EOF
)"
```

---

### Task 3: Raft 节点 + 三节点失 quorum

**Files:**
- Create: `internal/control/raft.go`
- Create: `internal/control/raft_test.go`
- Modify: `internal/paths/paths.go`（`RaftDir string`，`New` 设为 `filepath.Join(root, "raft")`，`Ensure` 创建它）
- Modify: `internal/paths/paths_test.go`（若有目录断言则补 `raft`）

**Interfaces:**
- Consumes: Task 2 `FSM` / `Command` / `EncodeCommand`
- Produces:

```go
type RaftConfig struct {
    Dir       string // $data_dir/raft
    Bind      string // 127.0.0.1:9002 或 127.0.0.1:0
    Advertise string // 空则用实际 bind
    NodeID    string
    ClusterID string
}

type Node struct { /* *raft.Raft + *FSM + advertise */ }

func Start(cfg RaftConfig) (*Node, error)
func (n *Node) Bootstrap() error                    // 单 voter：本机
func (n *Node) Apply(cmd Command, timeout time.Duration) error
func (n *Node) HasQuorum() bool
func (n *Node) IsLeader() bool
func (n *Node) LeaderAddr() string
func (n *Node) Advertise() string
func (n *Node) View() State
func (n *Node) LastContact() time.Time
func (n *Node) CacheFresh(ttl time.Duration) bool
func (n *Node) AddNonvoter(id, addr string) error
func (n *Node) AddVoter(id, addr string) error
func (n *Node) RemoveServer(id string) error
func (n *Node) Shutdown() error

func StartInmem(nodeID string, fsm *FSM, trans raft.Transport) (*Node, error)
```

实现约束：

- 生产路径（spec §10 锁定）：日志与 stable 用 `raft-boltdb/v2`（`$dir/raft.db`）；FSM 快照用 **bbolt**（`$dir/snapshots.bolt`，实现 `raft.SnapshotStore`：`Create`/`List`/`Open` 把快照元数据与字节存在 bucket `snaps`）。禁止 `FileSnapshotStore`。禁止把 Process 数据放进去。
- `HasQuorum`：本机是 leader 且 `VerifyLeader()` 成功 → true；否则若 `LeaderAddr()==""` → false。follower 有已知 leader 视为集群有 quorum（写仍转发 leader）。
- `CacheFresh`：`HasQuorum()` 或 `time.Since(LastContact()) < ttl`。
- 非 leader 的 `Apply` / `AddNonvoter` / `AddVoter` / `RemoveServer`：返回 `errcode.UNAVAILABLE`（消息 `not raft leader`）。测试用三节点时只在 leader 上 Apply。
- `StartInmem` 仅测试：`raft.NewInmemStore` + `InmemSnapshotStore`。

依赖：`go get github.com/hashicorp/raft@v1.7.3 github.com/hashicorp/raft-boltdb/v2@v2.3.1`

- [ ] **Step 1: 写失败测试**

```go
func TestRaft_BootstrapApplyVisible(t *testing.T) {
    // Start 临时目录 Bind 127.0.0.1:0，Bootstrap，Apply bootstrap，View 含 admin
}

func TestRaft_ThreeNodeLoseQuorumRejectsWrite(t *testing.T) {
    // 三个 StartInmem + 共享 LoopbackTransport
    // 配置 3 voter，等 leader
    // Apply user_put 成功
    // Shutdown 两个非 leader（或两个任意，留下一个）
    // 剩余节点 HasQuorum()==false
    // Apply 另一个 user_put → UNAVAILABLE
    // View 仍能读到第一个用户（Process 可读的缓存语义）
}

func TestRaft_FollowerApplyRejected(t *testing.T) {
    // 3 节点，非 leader Apply → UNAVAILABLE
}

func TestRaft_CacheFreshAfterPartition(t *testing.T) {
    // 单节点 Bootstrap 后 CacheFresh(5*time.Minute)==true
}
```

三节点 inmem 用 `raft.NewInmemTransport` 的 pair / `raft.NewLoopbackTransport`。必须真实形成 quorum 再拆，禁止 `t.Skip`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/control -run TestRaft_ -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 raft.go + paths.RaftDir**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/control ./internal/paths -count=1`

Expected: PASS

覆盖率：`go test ./internal/control -cover` ≥ 80%

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/control/raft.go internal/control/raft_test.go \
  internal/paths/paths.go internal/paths/paths_test.go
git commit -m "$(cat <<'EOF'
feat: 启动 Raft 控制面并覆盖失 quorum 拒绝写

EOF
)"
```

---

### Task 4: `internal/auth` — 会话、限流、RBAC 外观

**Files:**
- Create: `internal/auth/perm.go`
- Create: `internal/auth/auth.go`
- Create: `internal/auth/rbac.go`
- Create: `internal/auth/ratelimit.go`
- Create: `internal/auth/auth_test.go`
- Create: `internal/auth/rbac_test.go`

**Interfaces:**
- Consumes: `control.State` / `control.Node` 的只读视图；`control.VerifyPassword`；`control.MinPasswordLen` 等常量
- Produces:

```go
package auth

const (
    PermClusterRead = "cluster.read"
    // …本计划权限表的每一个，导出为 PermXxx 常量，值为表中字符串
    CookieName = "procmesh_session"
    HeaderCSRF = "X-CSRF-Token"
)

type Principal struct {
    UserID, Username, SessionID, TokenID, CSRF string
}

type Clock func() time.Time

type Store interface {
    View() control.State
    Apply(cmd control.Command, timeout time.Duration) error
    HasQuorum() bool
    CacheFresh(ttl time.Duration) bool
}

type Service struct {
    Store Store
    Now   Clock // nil → time.Now
}

func (s *Service) Login(username, password string) (sessionID, csrf, userID string, expires time.Time, err error)
func (s *Service) Logout(sessionID string) error
func (s *Service) AuthenticateBearer(token string) (Principal, error)
func (s *Service) AuthenticateSession(sessionID, csrf string, mutation bool) (Principal, error)
func (s *Service) CreateAPIToken(userID, name string, ttl time.Duration) (plain, tokenID string, expires time.Time, err error)
func (s *Service) RevokeAPIToken(tokenID string) error
func (s *Service) Allow(p Principal, perm, targetNodeID string) error // 不通过 → DENIED
func (s *Service) AllowWrite(p Principal, perm, targetNodeID string) error
func ValidPassword(pw string) error // 长度 < 10 → INVALID
```

`Login` 行为（按序）：

1. username/password 空 → `INVALID`
2. 每账号每分钟超过 5 次调用（内存计数，进程级即可）→ `DENIED` 消息 `login rate limited`
3. 用户不存在或密码错：若用户存在则 `Apply(login_fail)`；返回 `DENIED` 消息 `invalid credentials`（不区分用户是否存在）
4. 用户 `DISABLED` → `DENIED` `user disabled`
5. 用户 `LOCKED` 且 `now < LockedUntil` → `DENIED` `user locked`
6. 成功：`login_ok` + `session_put`（id = `pms_`+64hex，csrf 32 随机字节 hex，Expires=now+12h）

`AuthenticateBearer`：`pms_` 走 session；`pmt_` 走 API token 哈希。过期 / 吊销 / 用户非 ACTIVE → `DENIED`。

`AuthenticateSession`：查 session；过期 → `DENIED`。`mutation && csrf != session.CSRF` → `DENIED` `csrf mismatch`。Bearer 路径不走 CSRF。

`AllowWrite`：

- perm 属于 `user.create` `user.update` `user.delete` `role.manage` `node.remove` 或 join/token/promote 写 → 无 quorum 则 `UNAVAILABLE` 消息 `control quorum lost`
- 其它 Mutation（process.* 等）：无 quorum 且 `!CacheFresh(state.Policy.RBACCacheTTL)` → `DENIED` 消息 `rbac cache expired`
- 然后 `Allow`

`Allow`：`State.Check` 为 false → `DENIED` 消息 `permission denied`。

本包 **不** 复制用户库；所有持久状态经 `Store.Apply`。

- [ ] **Step 1: 写失败测试**

```go
func TestLogin_SuccessAndBearer(t *testing.T)
func TestLogin_BadPassword(t *testing.T)
func TestLogin_RateLimit(t *testing.T)
func TestLogin_Lockout(t *testing.T)
func TestLogin_ShortPasswordRejectedOnCreatePath(t *testing.T) // ValidPassword
func TestSession_CSRFRequiredForMutation(t *testing.T)
func TestAPIToken_ShownOnce(t *testing.T)
func TestAllow_ViewerCannotRestart(t *testing.T)
func TestAllowWrite_NoQuorumBlocksUserCreate(t *testing.T)
func TestAllowWrite_NoQuorumAllowsProcessRead(t *testing.T)
func TestAllowWrite_StaleCacheBlocksRemoteMutation(t *testing.T)
```

用内存 fake `Store`（内嵌 `*control.State`，`HasQuorum`/`CacheFresh` 可调）。先 `State.Apply(bootstrap)`。

`TestLogin_Lockout`：把 Store.Apply 做成真实 FSM Apply；10 次坏密码后第 11 次即使用对的密码也 `user locked`（在锁定期内）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/auth -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 auth / rbac / ratelimit / perm**

- [ ] **Step 4: 跑测试 + 覆盖率**

Run: `go test ./internal/auth -cover -count=1`

Expected: PASS，`internal/auth` ≥ 80%

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "$(cat <<'EOF'
feat: 实现登录、会话、API Token 与 RBAC 检查

EOF
)"
```

---

### Task 5: :9000 鉴权拦截器 + Login/Logout + 关闭环回无认证

**Files:**
- Create: `internal/api/authn.go`
- Create: `internal/api/authn_test.go`
- Modify: `internal/api/authapi.go`（实现 Login/Logout/CreateAPIToken/RevokeAPIToken）
- Create: `internal/api/authapi_test.go`
- Modify: `internal/api/server.go`（`Options.Auth *auth.Service`，Connect interceptor）
- Modify: `internal/rpc/header.go` + `header_test.go`（三个新头的 get/set）
- Modify: `internal/api/clusterapi_test.go`（`TestInit_ReturnsClusterIDAndPasswordThenConflict` 末尾：当 `Auth==nil` 仍可无认证——**本任务不要给 clusterEnv 注入 Auth**；另写新测试覆盖「注入 Auth 后 init 关闭无认证」）

**Interfaces:**
- Consumes: Task 4 `auth.Service`；Task 1 proto
- Produces:

```go
const (
    // 已有 Target/Source 之外：
    // rpc.HeaderUserID    = "Procmesh-User-ID"
    // rpc.HeaderSessionID = "Procmesh-Session-ID"
    // rpc.HeaderTokenID   = "Procmesh-Token-ID"
)

func PrincipalFrom(ctx context.Context) (auth.Principal, bool)
func WithPrincipal(ctx context.Context, p auth.Principal) context.Context

type Options struct {
    // 现有字段…
    Auth *auth.Service // nil = 不鉴权（单测）
}

func AuthInterceptor(svc *auth.Service, clusterInited func() bool) connect.Interceptor
```

拦截器规则：

1. `svc==nil` 或 `!clusterInited()` → 放行
2. 过程名以 `/procmesh.v1.AuthService/Login` 或 `/procmesh.v1.ClusterService/Join` 或 `/procmesh.v1.ClusterService/RequestJoin` 结尾 → 放行
3. 否则读 `Authorization: Bearer`；若无则读 Cookie `procmesh_session`
4. Bearer → `AuthenticateBearer`；Cookie → `AuthenticateSession`（Mutation 判定：方法不是 List/Get/Overview/History/Diff/Tail/Stream/Download/Status 类只读）。Cookie Mutation 必须带 `X-CSRF-Token`
5. 失败 → `ToConnect(DENIED)`
6. 成功 → `WithPrincipal`，并在 handler 里可用

`Login`：调 `svc.Login`；响应写 `Set-Cookie: procmesh_session=...; HttpOnly; SameSite=Lax; Path=/; Max-Age=43200`（不要 `Secure`，本阶段 :9000 可为明文；注释说明 P5/反代终结 TLS）。

`Logout`：需要已认证；`svc.Logout`。

`CreateAPIToken` / `RevokeAPIToken`：需要已认证；本任务先不查 RBAC（Task 7 补 `user.update`）。Create 把明文 token 放响应。

`/healthz` `/readyz` `/metrics` 不走 Connect interceptor。

- [ ] **Step 1: 写失败测试**

```go
func TestAuthn_UnauthAfterInitWhenAuthInjected(t *testing.T)
func TestAuthn_StandaloneAllowsUnauth(t *testing.T) // 未 init
func TestAuthn_LoginThenList(t *testing.T)
func TestAuthn_BadPasswordDenied(t *testing.T)
func TestAuthn_JoinAndLoginRemainPublic(t *testing.T)
func TestAuthn_CookieMutationNeedsCSRF(t *testing.T)
func TestAuthn_HealthzOpen(t *testing.T)
```

`TestAuthn_UnauthAfterInitWhenAuthInjected`：搭一个带内存 Store+Auth 的 `NewServer`，先 `cluster.Init`（或手动把 `clusterInited` 设 true 并 bootstrap FSM），不带会话 `ListProcesses` → detail `DENIED`。Login 后 List 成功。

不要改 `newClusterEnv` 默认行为（Auth 仍为 nil），避免一次打爆全部 P2 单测。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestAuthn_ -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 interceptor、Login/Logout/Token、新头**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api ./internal/rpc -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/authn.go internal/api/authn_test.go \
  internal/api/authapi.go internal/api/authapi_test.go \
  internal/api/server.go internal/rpc/header.go internal/rpc/header_test.go \
  internal/api/clusterapi_test.go
git commit -m "$(cat <<'EOF'
feat: cluster init 后强制会话并实现 Login

EOF
)"
```

---

### Task 6: User / Role API + CLI login/user/role

**Files:**
- Modify: `internal/api/user.go` / 创建 `internal/api/user_test.go`
- Modify: `internal/api/role.go` / 创建 `internal/api/role_test.go`
- Modify: `internal/cli/root.go`（usage + 命令分发）
- Modify: `internal/cli/client.go`（Auth/User/Role 客户端 + Bearer interceptor + 读 session 文件）
- Create: `internal/cli/auth.go`
- Create: `internal/cli/user.go`
- Modify: `internal/cli/root_test.go`
- Create: `internal/cli/auth_test.go`

**Interfaces:**
- Consumes: Task 5 interceptor + `auth.Service` + FSM 命令
- Produces: 真实 User/Role handler；CLI：

```text
procmesh login [--user NAME] [--password PASS]
procmesh logout
procmesh user list | create --user NAME --password PASS [--display NAME] [--email E]
procmesh user disable USER_ID
procmesh role list | create --name NAME --perm P [--perm P...]
procmesh role grant --user-id ID --role-id ID [--scope CLUSTER|AGENT] [--scope-id NODE]
```

`login`：密码来自 `--password`，否则 `PROCMESH_PASSWORD`，否则 stdin 一行。成功把 session 写到 `sessionPath()`：

```go
func sessionPath() string // $XDG_CONFIG_HOME/procmesh/session 或 ~/.config/procmesh/session
func writeSession(path string, s fileSession) error // 0600
func readSession(path string) (fileSession, error)
```

测试必须注入 `sessionPath`（包级 `var sessionFileFn = defaultSessionPath`）。

`newClient` 增加 `authToken string`。`Main` 在 parse 后：若命令不是 `login`，读 session 文件；server 匹配则带 Bearer。`--auth-token` flag 覆盖文件（测试与脚本用）。

User/Role handler：

- 从 `PrincipalFrom` 取用户；无 principal 且 Auth 注入 → interceptor 已拦；Auth nil 时单测可直接调（返回 UNAVAILABLE 或放行——**统一：Auth nil 则 User/Role 返回 UNAVAILABLE `auth not configured`**，避免绕过）
- `ListUsers` / `CreateUser` / `DisableUser` / `ListRoles` / `CreateRole` / `GrantRole` 本任务先做功能正确性；RBAC 权限门闸在 Task 7 统一加。CreateUser 调 `auth.ValidPassword`，哈希用 `control.HashPassword`，`Apply(user_put)`。
- `CreateRole` 拒绝未知 perm 字符串（不在 perm 表内）→ `INVALID`
- `GrantRole`：`scope_type` 非法 → `INVALID`；`AGENT` 时 `scope_id` 必填

- [ ] **Step 1: 写失败测试**

API：`TestUser_CreateListDisable`、`TestRole_CreateAndGrant`、`TestUser_ShortPassword`。

CLI：`TestCLI_LoginWritesSessionAndSendsBearer`、`TestCLI_UserCreateUsage`。

CLI 测试起 `httptest` 假 AuthService（记录 Authorization 头）或复用 api 测试服务器。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run 'TestUser_|TestRole_' -count=1 ; go test ./internal/cli -run TestCLI_Login -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 handler 与 CLI**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api ./internal/cli -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/user.go internal/api/user_test.go \
  internal/api/role.go internal/api/role_test.go \
  internal/cli
git commit -m "$(cat <<'EOF'
feat: 实现 User/Role API 与 login/user/role CLI

EOF
)"
```

---

### Task 7: 在现有 API 上强制 RBAC

**Files:**
- Modify: `internal/api/process.go` `config.go` `log.go` `node.go` `clusterapi.go` `authapi.go` `user.go` `role.go`
- Modify: 对应 `*_test.go`（带 Auth 的新用例；无 Auth 的旧用例保持原行为）
- Create: `internal/api/rbac_test.go`

**Interfaces:**
- Consumes: `auth.Service.Allow` / `AllowWrite` + `PrincipalFrom`
- Produces: `func requirePerm(ctx, svc *auth.Service, perm, targetNode string, write bool) error`

规则（`svc==nil` 则直接 nil）：

| RPC | perm | write | targetNode |
|-----|------|-------|------------|
| ListProcesses, GetProcess | `process.read` | false | hop 解析出的 node 或 `""` |
| ApplyProcess | `process.create`（新）或 `process.update`（已存在） | true | owner |
| DeleteProcess | `process.delete` | true | owner |
| StartProcess | `process.start` | true | owner |
| Stop/Kill | `process.stop` | true | owner |
| RestartProcess | `process.restart` | true | owner |
| ResetFailure, AdoptInstance | `process.update` | true | owner |
| GetConfig, History, Diff | `process.config.read` | false | owner |
| UpdateConfig, Rollback | `process.config.update` | true | owner |
| TailLogs, StreamLogs | `process.logs.read` | false | owner |
| DownloadLogs | `process.logs.download` | false | owner |
| ListNodes, GetNode, Overview | `node.read` / `cluster.read` | false | `""` |
| CreateJoinToken, RevokeJoinToken | `node.manage` | true | `""` |
| RemoveNode | `node.remove` | true | 目标 node |
| PromoteNode | `cluster.manage` | true | 目标 node |
| Init | 无（未入群） | — | — |
| Join, RequestJoin | 无（token） | — | — |
| Login | 无 | — | — |
| Logout | 已登录即可 | — | — |
| CreateAPIToken, RevokeAPIToken | `user.update` | true | `""` |
| ListUsers | `user.read` | false | `""` |
| CreateUser | `user.create` | true | `""` |
| DisableUser | `user.update` | true | `""` |
| ListRoles | `role.read` | false | `""` |
| CreateRole, GrantRole | `role.manage` | true | `""` |

只读在失 quorum 时：`Allow` 即可（不过 `AllowWrite`）。写走 `AllowWrite`。

目标节点：若 `hop` 已解析出远端 `Route.NodeID` 用它；本地用 `LocalID`。

- [ ] **Step 1: 写失败测试**

`internal/api/rbac_test.go`：bootstrap admin + 一个 viewer 用户。viewer 会话：

- `ListProcesses` 成功
- `RestartProcess` → `DENIED`
- `CreateUser` → `DENIED`
- `CreateJoinToken` → `DENIED`

admin 会话：上述写成功（CreateUser / CreateJoinToken；Restart 可用空 manager 走到权限之后的 NOT_FOUND，但不得是 DENIED）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestRBAC_ -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 requirePerm 并接到各 RPC 入口（本地执行与转发之前都要查）**

转发前查的是**入口** RBAC。Owner 再验在 Task 8。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "$(cat <<'EOF'
feat: 按内置角色强制 Process/Node/User RBAC

EOF
)"
```

---

### Task 8: 转发会话到 Owner 并再验

**Files:**
- Modify: `internal/api/process.go` `config.go` `log.go`（`remote*` 设置三个头）
- Modify: `internal/rpc/client.go`（可选：Dial 后自动拷贝 incoming header——若更干净就做一个 `CopyIdentity(dst, src http.Header)`）
- Modify: `internal/agent/rpc.go`（LocalOnly handler 加 Owner 再验 interceptor）
- Modify: `internal/api/process_test.go` / `apitest_test.go`（已有 fakeForwarder 断言头）
- Create: `internal/api/forward_auth_test.go`

**Interfaces:**
- Consumes: `rpc.HeaderUserID` / `HeaderSessionID` / `HeaderTokenID`
- Produces:

```go
func CopyIdentity(dst, src http.Header)
```

入口 `remoteProcess/Config/Log`：从 `PrincipalFrom(ctx)` 设置 User + Session 或 Token 头。

Owner `:9001` interceptor（Agent 注入同一 `auth.Service`）：

1. mTLS 已保证集群成员
2. 读 `Procmesh-Session-ID` 或 `Procmesh-Token-ID`（无则 `DENIED` `missing session`——**已入群 Owner 拒绝匿名 hop**）
3. `AuthenticateBearer` 等价校验（session id / token id）
4. 按 Task 7 表再 `Allow`/`AllowWrite`，target 为本机 `LocalID`
5. 不信任任何「已授权」自定义头

未入群、LocalOnly 测试服务器无 Auth 时不加该 interceptor。

- [ ] **Step 1: 写失败测试**

```go
func TestForward_SetsSessionHeaders(t *testing.T)
func TestOwner_RejectsHopWithoutSession(t *testing.T)
func TestOwner_RechecksViewerRestart(t *testing.T)
```

`TestForward_SetsSessionHeaders`：复用现有 hop 测试，Login 后 Restart 转发，fake client 看到 `Procmesh-User-ID` 与 `Procmesh-Session-ID`。

`TestOwner_RechecksViewerRestart`：Owner 侧 Auth 为 viewer，即使入口误放行，Owner 仍 DENIED。可用 LocalOnly server + 直接调 Restart。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run 'TestForward_|TestOwner_' -count=1`

Expected: FAIL

- [ ] **Step 3: 实现拷贝头 + Owner interceptor**

Agent 的 `localHandler` 必须挂上该 interceptor（若 `rt.auth != nil`）。本任务可先改 `internal/agent/rpc.go` 接缝：`rpcRuntime` 增加 `auth *auth.Service`，若 run.go 尚未接线则保持 nil，测试里单独构造。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api ./internal/agent -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/rpc internal/agent/rpc.go
git commit -m "$(cat <<'EOF'
feat: 入口转发会话并由 Owner 再验 RBAC

EOF
)"
```

---

### Task 9: Join / Token 迁入 Raft + AddNonvoter

**Files:**
- Create: `internal/control/admission.go`
- Create: `internal/control/admission_test.go`
- Modify: `internal/api/node.go`（Create/Revoke token → Control）
- Modify: `internal/api/clusterapi.go`（Join 走 leader；消费 Raft token；Admit member；AddNonvoter）
- Modify: `internal/api/clusterapi_test.go` / `node_test.go`（注入内存 Node 或 Admission）
- Modify: `internal/agentcfg/load.go` + `load_test.go`（`Control.Listen` / `Control.Advertise`）

**Interfaces:**
- Consumes: Task 3 `*control.Node`，现有 `SignCSR` / `CheckJoin`
- Produces:

```go
// agentcfg
type Control struct { Listen, Advertise string }
// Config.Control Control
// yaml: control.listen / control.advertise

type Admission struct {
    Node *control.Node
    // Sign 用的 CA 仅 leader 有
}

func (a *Admission) CreateToken(ttl time.Duration, uses int, now time.Time) (plain string, info TokenInfo, err error)
func (a *Admission) ConsumeToken(plain string, now time.Time) error
func (a *Admission) RevokeToken(id string) error
func (a *Admission) Admit(nodeID, raftAddr, certSerial string) error
func (a *Admission) IsRevoked(nodeID string) bool
```

`ClusterDeps` 增加：

```go
Control *control.Node // nil = 单测走旧 tokens.json
OnAdmit func(nodeID, raftAddr string) error // leader AddNonvoter；nil 忽略
```

`CreateJoinToken`：`Control!=nil` 则 `Admission.CreateToken`（`join_token_put`，哈希 sha256，prefix `pmj_` 保持）；否则保留文件实现，供无 Raft 单测。

`Join`：

1. 现有 CheckJoin
2. 若 `Control!=nil` 且 `Admission.IsRevoked(node_id)` → `DENIED` `node removed`
3. 若 `Control!=nil` 且本机不是 leader：把请求转到 `http://` + 从 gossip/overview 得到的 leader API（测试可设 `ClusterDeps.LeaderAPI func() string`）。转失败 → `UNAVAILABLE`
4. Leader：`ConsumeToken`（Raft）→ `SignCSR` → `CertSerial` → `Admit` → 若 `raft_address!=""` 则 `AddNonvoter(node_id, raft_address)`（失败只记日志，不回滚证书——加入者仍能拿到证；后续 Task 11 重试）
5. 响应填 `raft_leader = Control.Advertise()`

`RequestJoin`：在发 Join 前由调用方填 `RaftAddress`（本任务 API 层若 `GossipAddr` 类推，加 `ClusterDeps.RaftAddr func() string`）。

`agentcfg` 测试：

```go
func TestLoadAll_ControlListenAndAdvertise(t *testing.T) {
    body := "control:\n  listen: 127.0.0.1:9002\n  advertise: 10.0.0.1:9002\n"
    // 断言 cfg.Control.Listen / Advertise
}
```

- [ ] **Step 1: 写失败测试**

```go
func TestAdmission_CreateConsumeRevoke(t *testing.T) // 真 Raft 单节点
func TestJoin_UsesRaftTokenNotFile(t *testing.T)
func TestJoin_RevokedNodeDenied(t *testing.T)
func TestLoadAll_ControlListenAndAdvertise(t *testing.T)
```

`TestJoin_UsesRaftTokenNotFile`：只 bootstrap Raft、用 Admission 发卡，不写 `tokens.json`；Join 成功；`tokens.json` 仍不存在。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/control -run TestAdmission_ -count=1 ; go test ./internal/api -run TestJoin_UsesRaftTokenNotFile -count=1 ; go test ./internal/agentcfg -run TestLoadAll_Control -count=1`

Expected: FAIL

- [ ] **Step 3: 实现 Admission 与 Join/Token 改道**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/control ./internal/api ./internal/agentcfg -count=1`

Expected: PASS。旧的 `tokens.json` 单测（`Control==nil`）必须仍然通过。

- [ ] **Step 5: Commit**

```bash
git add internal/control/admission.go internal/control/admission_test.go \
  internal/api/node.go internal/api/clusterapi.go \
  internal/api/clusterapi_test.go internal/api/node_test.go \
  internal/agentcfg/load.go internal/agentcfg/load_test.go
git commit -m "$(cat <<'EOF'
feat: Join token 与准入迁入 Raft

EOF
)"
```

---

### Task 10: `node remove` + CRL + CheckJoin 拒绝吊销节点

**Files:**
- Modify: `internal/cluster/check.go`
- Modify: `internal/cluster/check_test.go`（`TestCheckJoin_RemovedAndRevokedAllowNewBoot` **改为** REMOVED/REVOKED → `DENIED` 消息 `node removed`。LEFT 仍允许再加入）
- Modify: `internal/api/node.go`（实现 `RemoveNode` / `PromoteNode`）
- Create: `internal/api/node_remove_test.go`
- Modify: `internal/rpc/tls.go` / `tls_test.go` / `server.go`
- Create: `internal/control/crl.go`（若 serial 检查未放在 fsm，则薄封装 `func (s State) SerialRevoked`）
- Modify: `internal/cli/node.go` + `root.go` usage：`node remove NODE_ID`、`node promote NODE_ID`

**Interfaces:**
- Consumes: FSM `member_remove` / `crl_add`；`control.Node.RemoveServer`
- Produces:

```go
// rpc
func ServerTLS(creds control.AgentCreds, clusterID string, revoked func(serial string) bool) (*tls.Config, error)
```

所有现有 `ServerTLS` 调用处：测试传 `nil`（视为永不吊销）；Agent 传 `func(s string) bool { return node.View().SerialRevoked(s) }`。

`verifyPeer` 在链校验成功后：若 `revoked != nil && revoked(大写 hex serial)` → `DENIED` `certificate revoked`。

`RemoveNode`：

1. `requirePerm(node.remove)`
2. 不能 remove 自己 → `INVALID` `cannot remove self`
3. `Apply(member_remove)`（把 status=REVOKED 且 serial 进 CRL）
4. `RemoveServer(node_id)`（Raft，忽略 not found）
5. 不要求对方在线

`PromoteNode`：

1. `requirePerm(cluster.manage)`
2. 目标必须已 ADMITTED 且有 `RaftAddr`
3. `AddVoter(node_id, raftAddr)`
4. 本阶段 **不** 分发 `ca.key`（只有已有 CA 的 voter 能签发；init 节点是默认签发者）。注释写明：V1.0 默认单 voter 签发；promote 只扩 quorum。Case 9 用「唯一 voter 宕机」验证失 quorum，不依赖 3 voter。

CheckJoin：

```go
case StateRemoved, StateRevoked:
    return errcode.E(errcode.DENIED, "node removed")
```

- [ ] **Step 1: 写失败测试**

```go
func TestCheckJoin_RemovedAndRevokedDenied(t *testing.T)
func TestRemoveNode_AddsCRLAndDeniesRejoin(t *testing.T)
func TestServerTLS_RevokedSerialDenied(t *testing.T)
func TestCLI_NodeRemoveUsage(t *testing.T)
```

`TestServerTLS_RevokedSerialDenied`：两套同 CA 证书，revoked 回调对其中一张 serial 返回 true；用该证书 Dial 必须失败且错误含 `revoked` 或 detail `DENIED`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster -run TestCheckJoin_Removed -count=1 ; go test ./internal/rpc -run TestServerTLS_Revoked -count=1 ; go test ./internal/api -run TestRemoveNode_ -count=1`

Expected: FAIL（旧 test 名若仍是 Allow 则先改名再红）

- [ ] **Step 3: 实现 CheckJoin / CRL / Remove / Promote / CLI**

同步改所有 `rpc.ServerTLS(` 调用点（`internal/rpc/server.go`、测试、`internal/agent/rpc.go`）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cluster ./internal/rpc ./internal/api ./internal/cli ./internal/control -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/check.go internal/cluster/check_test.go \
  internal/api/node.go internal/api/node_remove_test.go \
  internal/rpc/tls.go internal/rpc/tls_test.go internal/rpc/server.go \
  internal/control/crl.go internal/cli/node.go internal/cli/root.go \
  internal/agent/rpc.go
git commit -m "$(cat <<'EOF'
feat: 实现 node remove、证书吊销与再加入拒绝

EOF
)"
```

---

### Task 11: Agent 接线 — 启动 Raft、注入 Auth、指标

**Files:**
- Create: `internal/agent/raft.go`
- Modify: `internal/agent/run.go`
- Modify: `internal/agent/rpc.go`
- Modify: `internal/agentcfg` 已在 Task 9
- Modify: `internal/api/metrics.go` / `metrics_test.go`
- Modify: `cmd/procmesh-agent/main.go`（如需 `--control` flag；默认读 yaml，flag 可覆盖，与 `--rpc` 对称）
- Modify: `internal/api/clusterapi.go` `Init`：成功后 `OnReady` 已有；本任务 OnReady 同时 startRPC + startRaft + bootstrap FSM admin
- Modify: `internal/agent/run_test.go` / `cluster_accept_test.go` 辅助函数（见步骤）

**Interfaces:**
- Consumes: 以上全部
- Produces:

```go
// agent.Options 增加
ControlListen    string // default 127.0.0.1:9002；测试 127.0.0.1:0
ControlAdvertise string
OnControlListen  func(addr string)

// metrics 追加
# HELP procmesh_cluster_control_quorum Whether this node sees a Raft leader (1) or not (0).
# TYPE procmesh_cluster_control_quorum gauge
procmesh_cluster_control_quorum 0|1
```

启动顺序（`Run`）：

1. 现有 store / process / gossip / :9000
2. 若已有 `cluster.json`：`Start` Raft（**不要**再次 Bootstrap）。init 节点目录已有 raft 则 Open；全新 init 在 `ClusterAPI.Init` 之后的 `OnReady` 里 `Start`+`Bootstrap`+`Apply(bootstrap from LoadAdminBootstrap)`
3. 构造 `auth.Service{Store: raftNode}`
4. `api.NewServer` 设 `Auth`（仅当 `control.AlreadyInited`）
5. `startRPC` 把 CRL 回调和 Owner interceptor 接上
6. `RequestJoin` 成功后：`Start` Raft（不 Bootstrap），用响应 `raft_leader` 作为配置里的已知 leader（hashicorp：AddNonvoter 由 seed 在 Join 时完成）

`Init` 后关闭无认证：因为 `AlreadyInited==true` 且 `Auth!=nil`。

`joinTwo` / `startClusterAgent` / P3 accept：**必须**在 `cluster init` 之后用返回的 `admin_password` 调 `procmesh login --user admin --password ...`，后续 CLI 自动带 session。抽 helper：

```go
func loginAdmin(t *testing.T, server, password string) {
    t.Helper()
    // 设置 session 文件到 t.TempDir，并设置 sessionFileFn 或 PROCMESH_SESSION 环境
}
```

为避免污染开发者 `~/.config`，CLI 增加环境变量 `PROCMESH_SESSION` 覆盖 session 路径（Task 6 若未加，本任务必须加）。accept 测试设到 `t.TempDir()/session`。

`Overview` 填 `control_quorum` / `control_leader`。

- [ ] **Step 1: 写失败测试**

```go
func TestMetrics_ControlQuorum(t *testing.T) // NewServer 带 HasQuorum=false 的 stub → 指标 0
func TestAgent_InitClosesUnauth(t *testing.T) // 真 agent：init 后无 login 的 process list 退出码 !=0
```

`TestAgent_InitClosesUnauth` 放 `internal/agent/p4_accept_test.go` 也可以，但本任务至少要让现有 `cluster_accept` 在接线后先改 helper 再绿。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestMetrics_ControlQuorum -count=1 ; go test ./internal/agent -run TestAgent_InitClosesUnauth -count=1`

Expected: FAIL

- [ ] **Step 3: 接线 Run / OnReady / metrics / accept helper**

更新 `cluster_accept_test.go` 与 `p3_accept_test.go`：init 后 login。`joinTwo` 在 token create 前 login。

- [ ] **Step 4: 全量测试**

Run: `go test ./... -count=1`

Expected: PASS

覆盖率抽查不得回退：

```bash
go test ./internal/process ./internal/shim ./internal/store ./internal/control ./internal/auth -cover
```

Expected: 五包 ≥ 80%

- [ ] **Step 5: Commit**

```bash
git add internal/agent internal/api/metrics.go internal/api/metrics_test.go \
  cmd/procmesh-agent/main.go internal/cli
git commit -m "$(cat <<'EOF'
feat: Agent 启动 Raft 并在入群后关闭无认证入口

EOF
)"
```

---

### Task 12: 验收 Case 8 / Case 9 + 计划索引

**Files:**
- Create: `internal/agent/p4_accept_test.go`
- Modify: `docs/superpowers/plans/2026-08-13-v1-mvp.md`（P4 行改为指向本计划）
- Modify: 若 Task 11 未完全修完 P3 accept，本任务收尾

**Interfaces:**
- Consumes: 已接线的 agent + CLI
- Produces: 可演示「登录生效、remove 后再连拒绝、失 quorum 限制写」

验收测试（`internal/agent/p4_accept_test.go`）：

```go
func TestP4_LoginRequiredAfterInit(t *testing.T)
func TestP4_ViewerCannotRestart(t *testing.T)
func TestP4_Case8_RemoveThenRejoinDenied(t *testing.T)
func TestP4_Case9_ControlDownLocalProcessContinues(t *testing.T)
```

`TestP4_LoginRequiredAfterInit`：`cluster init` 后不 login，`process list` 失败（stderr 含 `DENIED`）。login 后成功。

`TestP4_ViewerCannotRestart`：admin 建用户 `view1` 并 `role grant viewer`；用该用户 login；`process restart` → `DENIED`。

`TestP4_Case8_RemoveThenRejoinDenied`：

1. A init，B join
2. admin 从 A `node remove <B node_id>`
3. B 再 `agent join`（新 token）必须失败，detail `DENIED` 或消息 `node removed` / `certificate revoked`
4. 用 B 的旧证书 Dial A 的 `:9001` 必须失败

`TestP4_Case9_ControlDownLocalProcessContinues`：

1. A init（唯一 voter），C join（non-voter）
2. 在 C 上 apply+start 一个 sleep 进程，观察到 RUNNING
3. 停 A（cancel A 的 ctx / 关进程）
4. C 上 `process list` 仍成功且 RUNNING（本地读）
5. C 上 `user create` 必须失败（`UNAVAILABLE` 或 `control quorum lost`）
6. 不停 C 上的业务进程

macOS 跑这些测试（不依赖 cgroup）。不要 `t.Skip`。

更新索引：

```markdown
| P4 | [2026-08-15-p4-raft-rbac-admission.md](./2026-08-15-p4-raft-rbac-admission.md) | 登录与权限生效 |
```

- [ ] **Step 1: 写失败测试（四个验收）**

- [ ] **Step 2: 跑测试确认失败（若已意外通过则检查是否断言过弱）**

Run: `go test ./internal/agent -run TestP4_ -count=1 -timeout 120s`

- [ ] **Step 3: 补齐缺口（helper、remove 后 CheckJoin、overview）**

- [ ] **Step 4: 全量测试 + 覆盖率**

Run: `go test ./... -count=1`

```bash
go test ./internal/process ./internal/shim ./internal/store ./internal/control ./internal/auth -cover
```

Expected: 全绿；五包 ≥ 80%

- [ ] **Step 5: Commit**

```bash
git add internal/agent/p4_accept_test.go docs/superpowers/plans/2026-08-13-v1-mvp.md
git commit -m "$(cat <<'EOF'
test: 验收登录、Case 8 吊销再连与 Case 9 失 quorum

EOF
)"
```

---

## 自检

1. **规格覆盖：** §3.6 用户/RBAC 强一致；§4.2 `control`/`auth`；§5.1 `:9002`；§5.2 `raft/`；§9.1 CRL；§9.3 join/remove；§9.5 入口+Owner 再验；§10 Raft FSM 内容与失 quorum；§11.1 Auth/User/Role/Remove；§11.2 login/user/role/remove CLI；§13 P4 演示；§14 三节点内存 Raft、Case 8/9、`auth`/`control` 80%；§16 密码/session/限流；§17 无 Group scope；§18 不把 Process 写入 Raft。P5 项（Vue/LIVE）明确不做。
2. **无占位符：** 任务均含测试名、命令、期望、提交说明与关键类型签名。
3. **类型一致：** `control.Node` / `auth.Service` / `Store` / `Admission` / 头常量 / 角色 id / token 前缀在后续任务中名称一致。
4. **旧测试策略：** `Options.Auth == nil` 保持 P0–P3 单测无认证；真 Agent 入群后注入 Auth。`tokens.json` 路径仅当 `ClusterDeps.Control==nil`。CheckJoin 的 REMOVED/REVOKED 语义在 Task 10 起改为拒绝。
5. **依赖方向：** `api` → `auth` / `control` / `rpc` / `process`；`auth` → `control`（只读 State + Apply 接口）；`process` 不 import `auth`/`control`/`rpc`/`cluster`。
