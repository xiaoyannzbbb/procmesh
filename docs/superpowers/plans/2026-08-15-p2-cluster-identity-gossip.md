# P2 Cluster Identity + Gossip Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在已完成的 P0/P1 本机 Process Plane 与 ConnectRPC CLI 之上，交付节点身份、`cluster init`、本地 join token、证书骨架和 memberlist gossip，使 `procmesh node list` 能列出本机及已加入节点。

**Architecture:** Agent 首次启动把 `node_id` 写成 `$data_dir/node_id` 并与 SQLite `local_meta` 对齐；`boot_id` 文件写入 **host** boot id（与 P0 Process 恢复语义一致，不在每次 Agent 重启时轮转 store boot_id）。`cluster init` 在 `$data_dir/cluster/` 生成 Cluster CA、本机 Agent 证书、32 字节 cluster secret、初始 Super Admin 的 argon2id 哈希，并标记本机为未来的 Control Member（**本阶段不启动 Raft**）。Join token 以哈希形式存在 `cluster/tokens.json`（TTL / 次数 / 作废）；P4 再迁入 Raft。Gossip 用 hashicorp/memberlist，`:18689` 默认同环回。Join 走已有 `:18680` ConnectRPC（P3 才做 `:18683` mTLS）。`process` 不得 import `cluster` 或 `control`；cluster 通过 `SummarySource` 回调拿摘要 DTO。

**Tech Stack:** Go 1.23、已有 ConnectRPC + Gin + modernc.org/sqlite、`github.com/hashicorp/memberlist`、`golang.org/x/crypto/argon2`、标准库 `crypto/x509` / `crypto/ecdsa`。

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- Go 版本下限：`1.23`
- CGO-free SQLite only：`modernc.org/sqlite`（禁止 `mattn/go-sqlite3`）
- Linux 是生产保证面；macOS 必须能编译并跑单测 + 非 cgroup 集成
- `process` 不得 import `cluster` 或 `control`
- `cluster` 与 `process` 只交换 summary DTO（经接口/回调），禁止 cluster 依赖 process 内部状态机
- 日志正文只在文件里，不进 SQLite
- 所有 Mutation 必须带非空 `operation_id`（UUID）；CLI 未传则自己生成
- 错误码沿用 `internal/errcode`：`OK`、`CONFLICT`、`UNAVAILABLE`、`TIMEOUT`、`DENIED`、`DEGRADED`、`DUPLICATE_NODE_ID`、`INCOMPATIBLE_VERSION`、`NOT_FOUND`、`INVALID`
- 应用错误码放在 Connect error detail（`ErrorInfo.code`），消息为英文
- 对外主协议是 ConnectRPC；REST 仅 `/healthz`、`/readyz`、`/metrics`
- 监听默认 `127.0.0.1:18680` 与 `127.0.0.1:18689`；非环回必须 `--insecure-listen`
- **P4 完成前不关闭环回无认证。** `cluster init` 之后仍允许本机无认证管理。禁止在本阶段实现登录或关掉 loopback 免认证。
- **本阶段不启动 Raft、不实现 CRL、不实现 `node remove`、不做 Agent 间 mTLS（:18683）。**
- Join token 本阶段只存本机 `cluster/tokens.json`（只存哈希）；明文 token 只在创建响应里出现一次
- Cluster CA 私钥与 cluster secret、admin bootstrap、agent key 权限必须 `0600`
- 证书 SAN 必须含 URI `procmesh://<cluster_id>/<node_id>`
- V1.0 `protocol_version = 1`（`internal/version.Protocol`）；不兼容则 `INCOMPATIBLE_VERSION`，不把对方当可互操作成员
- 重复 `node_id`：拒绝后加入者，错误码 `DUPLICATE_NODE_ID`
- `node_id` 必须是 UUID，禁止用 IP / hostname / MAC
- Process 平面的 `store` boot_id 继续使用 `paths.CurrentBootID()`（host boot）。**禁止**把 store boot_id 改成「每次 Agent 启动新 UUID」——那会破坏 shim 恢复
- Gossip 允许：node_id、地址、版本、protocol_version、labels、资源摘要（CPU/内存/磁盘百分比）、每个 process 的摘要（name、desired、observed、health、latest/active revision、freshness）。禁止：日志、全量 spec、明细 metrics、stdio、大审计
- 测试与代码同目录：`internal/foo/foo_test.go`
- 强制 TDD：先红后绿
- P0 覆盖率门槛保持：`internal/process`、`internal/shim`、`internal/store` ≥ 80%
- 文档与本计划使用中文；API 错误码与错误消息使用英文
- 生成的 proto Go 文件禁止手改；改完 proto 必须 `make proto`

## 规格解读（P2 边界）

来源：`docs/v2-prd/v2-prd.md` 与 `docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。冲突以架构 spec 为准。

1. **P2 可演示出口**（spec §13）：`procmesh node list`。
2. **身份**（spec §9.1 / PRD §7）：首次启动生成并持久化 `node_id`（UUID 文件 + SQLite）。`boot_id` 文件写入 host boot id；store 里的 boot_id 保持 P0 语义。
3. **cluster init**（spec §9.3 / PRD §9.1）：生成 `cluster_id`、Cluster CA、cluster secret、初始 Super Admin，为本机签 Agent 证书，标记本机为 Control Member。**不**在本阶段 bootstrap Raft voter 进程。
4. **cluster secret**：随机 32 字节，只存 `cluster/secret`（0600），**不**作为日常 RPC 凭证。
5. **Join token**（PRD §9.2）：TTL、使用次数、手动作废。本阶段本地文件；P4 迁 Raft。
6. **Join**：向种子 Agent 的 `:18680` 提交 token + CSR；种子校验 token、查重、用 CA 签证书，返回证书、CA、gossip 地址。PRD 写 `--server agent-a:18683` 被本阶段刻意改成 `:18680`（:18683 mTLS 是 P3）。
7. **Gossip**（spec §9.4）：memberlist。重复 node_id 与 protocol 检查在 join 与 merge 两条路径都做。
8. **CLI**（spec §11.2）：`cluster init`、`agent join --token`、`node list`、`node status`、`node token create`。额外提供 `node token revoke`（PRD 要求手动失效）。`node remove` 留给 P4。
9. **明确不做**：Raft、用户登录、RBAC 生效、CRL、node remove、Write-to-Owner、mTLS :18683、Vue、关闭免认证环回。

## File map（本阶段创建/修改）

```text
internal/identity/identity.go
internal/identity/identity_test.go
internal/paths/paths.go                          # NodeID / BootID 路径
internal/store/meta.go                           # SetNodeID
internal/version/version.go                      # Agent 版本字符串
internal/control/pki.go
internal/control/pki_test.go
internal/control/init.go
internal/control/init_test.go
internal/control/token.go
internal/control/token_test.go
internal/control/password.go                     # argon2id
internal/cluster/summary.go
internal/cluster/codec.go
internal/cluster/codec_test.go
internal/cluster/mesh.go
internal/cluster/mesh_test.go
internal/cluster/check.go
internal/cluster/check_test.go
proto/procmesh/v1/api.proto                      # 追加 Node/Cluster 服务
proto/procmesh/v1/api.pb.go                      # 生成
proto/procmesh/v1/procmeshv1connect/api.connect.go
internal/api/server.go
internal/api/node.go
internal/api/node_test.go
internal/api/clusterapi.go
internal/api/clusterapi_test.go
internal/cli/root.go
internal/cli/cluster.go
internal/cli/node.go
internal/cli/client.go
internal/cli/root_test.go
internal/agent/run.go
internal/agentcfg/load.go
internal/api/metrics.go
docs/superpowers/plans/2026-08-13-v1-mvp.md
```

---

### Task 1: `node_id` / `boot_id` 文件与 SQLite 同步

**Files:**
- Create: `internal/identity/identity.go`
- Create: `internal/identity/identity_test.go`
- Modify: `internal/paths/paths.go`（增加 `NodeID`、`BootID` 路径）
- Modify: `internal/paths/paths_test.go`
- Modify: `internal/store/meta.go`（增加 `SetNodeID`）
- Modify: `internal/store/store_test.go`（`SetNodeID` 用例）
- Modify: `internal/agent/run.go`（启动时调用 `identity.Ensure`，仍用 `paths.CurrentBootID()` 写 store boot_id）

**Interfaces:**
- Consumes: `store.GetOrCreateNodeID`、`store.SetBootID`、`paths.Layout`、`paths.CurrentBootID`
- Produces:
  - `func (l Layout) NodeIDFile() string` → `filepath.Join(l.Root, "node_id")`
  - `func (l Layout) BootIDFile() string` → `filepath.Join(l.Root, "boot_id")`
  - `func (*Store) SetNodeID(ctx context.Context, id string) error`
  - `type Meta interface { GetOrCreateNodeID(ctx context.Context) (string, error); SetNodeID(ctx context.Context, id string) error }`
  - `func Ensure(ctx context.Context, layout paths.Layout, meta Meta, hostBoot string) (nodeID string, err error)`

- [ ] **Step 1: 写失败测试（路径）**

```go
func TestLayout_NodeAndBootFiles(t *testing.T) {
    l := paths.New("/data")
    if l.NodeIDFile() != "/data/node_id" {
        t.Fatalf("NodeIDFile=%q", l.NodeIDFile())
    }
    if l.BootIDFile() != "/data/boot_id" {
        t.Fatalf("BootIDFile=%q", l.BootIDFile())
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/paths -run TestLayout_NodeAndBootFiles -count=1`
Expected: FAIL `NodeIDFile` undefined

- [ ] **Step 3: 实现路径并让测试通过**

在 `paths.Layout` 增加：

```go
func (l Layout) NodeIDFile() string { return filepath.Join(l.Root, "node_id") }
func (l Layout) BootIDFile() string { return filepath.Join(l.Root, "boot_id") }
```

- [ ] **Step 4: 写 `SetNodeID` 与 `Ensure` 失败测试**

`internal/store/store_test.go` 增加：文件已有 node_id 时 `SetNodeID` 覆盖并可再读。

`internal/identity/identity_test.go`：

```go
func TestEnsure_CreatesUUIDFileAndSyncsStore(t *testing.T) {
    ctx := context.Background()
    root := t.TempDir()
    l := paths.New(root)
    if err := l.Ensure(); err != nil { t.Fatal(err) }
    st, err := store.Open(l.Store)
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = st.Close() })

    id, err := identity.Ensure(ctx, l, st, "host-boot-1")
    if err != nil { t.Fatal(err) }
    if !isUUID(id) { t.Fatalf("not uuid: %q", id) }

    raw, err := os.ReadFile(l.NodeIDFile())
    if err != nil { t.Fatal(err) }
    if strings.TrimSpace(string(raw)) != id {
        t.Fatalf("file=%q store=%q", raw, id)
    }
    got, err := st.GetOrCreateNodeID(ctx)
    if err != nil || got != id { t.Fatalf("store=%q err=%v", got, err) }

    boot, err := os.ReadFile(l.BootIDFile())
    if err != nil { t.Fatal(err) }
    if strings.TrimSpace(string(boot)) != "host-boot-1" {
        t.Fatalf("boot file=%q", boot)
    }
}

func TestEnsure_FileWinsOverStore(t *testing.T) {
    ctx := context.Background()
    root := t.TempDir()
    l := paths.New(root)
    _ = l.Ensure()
    st, _ := store.Open(l.Store)
    t.Cleanup(func() { _ = st.Close() })
    _, _ = st.GetOrCreateNodeID(ctx)
    const fileID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
    if err := os.WriteFile(l.NodeIDFile(), []byte(fileID+"\n"), 0o640); err != nil {
        t.Fatal(err)
    }
    id, err := identity.Ensure(ctx, l, st, "b")
    if err != nil { t.Fatal(err) }
    if id != fileID { t.Fatalf("got %q", id) }
    got, _ := st.GetOrCreateNodeID(ctx)
    if got != fileID { t.Fatalf("store not synced: %q", got) }
}

func TestEnsure_StableAcrossCalls(t *testing.T) {
    ctx := context.Background()
    root := t.TempDir()
    l := paths.New(root)
    _ = l.Ensure()
    st, _ := store.Open(l.Store)
    t.Cleanup(func() { _ = st.Close() })
    a, err := identity.Ensure(ctx, l, st, "b1")
    if err != nil { t.Fatal(err) }
    b, err := identity.Ensure(ctx, l, st, "b2")
    if err != nil { t.Fatal(err) }
    if a != b { t.Fatalf("%q vs %q", a, b) }
    boot, _ := os.ReadFile(l.BootIDFile())
    if strings.TrimSpace(string(boot)) != "b2" {
        t.Fatalf("boot file must be overwritten each Ensure: %q", boot)
    }
}

func TestEnsure_RejectsNonUUIDFile(t *testing.T) {
    ctx := context.Background()
    root := t.TempDir()
    l := paths.New(root)
    _ = l.Ensure()
    st, _ := store.Open(l.Store)
    t.Cleanup(func() { _ = st.Close() })
    _ = os.WriteFile(l.NodeIDFile(), []byte("10.0.0.1\n"), 0o640)
    _, err := identity.Ensure(ctx, l, st, "b")
    if !errcode.Is(err, errcode.INVALID) {
        t.Fatalf("want INVALID, got %v", err)
    }
}
```

`isUUID`：解析 8-4-4-4-12 十六进制（与 `store` 现有 `newUUID` 格式一致）。拒绝 IPv4 / 看起来像 hostname 的值。

- [ ] **Step 5: 跑测试确认失败**

Run: `go test ./internal/identity ./internal/store -count=1`
Expected: FAIL package identity 不存在或 `SetNodeID` 未定义

- [ ] **Step 6: 最小实现**

`store.SetNodeID`：`putMeta(ctx, keyNodeID, id)`。

`identity.Ensure`：
1. `layout.Ensure()`
2. 读 `NodeIDFile`；若存在则 `strings.TrimSpace`，用正则 `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$` 校验，失败返回 `errcode.E(INVALID, "node_id must be a UUID")`；然后 `meta.SetNodeID`
3. 若不存在：`id, err := meta.GetOrCreateNodeID`，写入文件，mode `0640`
4. 写 `BootIDFile` 为 `hostBoot+"\n"`，mode `0640`（每次覆盖）
5. 返回 node id

**不要**调用 `RotateBootID` / **不要**用新 UUID 覆盖 store boot_id。

`agent.Run` 在 `store.Open` 成功后：

```go
if err := st.SetBootID(ctx, paths.CurrentBootID()); err != nil { return err }
if _, err := identity.Ensure(ctx, layout, st, paths.CurrentBootID()); err != nil { return err }
```

顺序：先 SetBootID（Process 恢复依赖），再 Ensure（写文件）。

- [ ] **Step 7: 跑测试确认通过**

Run: `go test ./internal/identity ./internal/paths ./internal/store ./internal/agent -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/identity internal/paths/paths.go internal/paths/paths_test.go internal/store/meta.go internal/store/store_test.go internal/agent/run.go
git commit -m "$(cat <<'EOF'
feat: 持久化 node_id 文件并与 store 对齐

EOF
)"
```

---

### Task 2: Cluster CA + Agent 证书骨架

**Files:**
- Create: `internal/control/pki.go`
- Create: `internal/control/pki_test.go`

**Interfaces:**
- Consumes: 无（标准库 crypto）
- Produces:
  - `const URIPrefix = "procmesh://"`
  - `func AgentURI(clusterID, nodeID string) string` → `procmesh://<clusterID>/<nodeID>`
  - `func ParseAgentURI(uri string) (clusterID, nodeID string, err error)`
  - `type Bundle struct { CACertPEM, CAKeyPEM, AgentCertPEM, AgentKeyPEM []byte }`
  - `func NewBundle(clusterID, nodeID string, now time.Time) (Bundle, error)` — 自签 CA + 为本机签 Agent 证
  - `func SignCSR(caCertPEM, caKeyPEM, csrPEM []byte, clusterID, nodeID string, now time.Time) (certPEM []byte, err error)` — CSR 必须带 URI SAN，其 **node_id** 必须等于参数 `nodeID`；CSR 的 cluster 段可与真实 cluster 不同（加入者用占位 `join`）。签发的证书 URI **重写** 为 `procmesh://<clusterID>/<nodeID>`
  - `func ParseIDs(certPEM []byte) (clusterID, nodeID string, err error)` — 从 URI SAN 读取
  - `func VerifyAgent(caCertPEM, agentCertPEM []byte, clusterID, nodeID string, now time.Time) error`
  - `func WriteBundle(dir string, b Bundle) error` — CA/agent key `0600`，证书 `0640`
  - `func LoadBundle(dir string) (Bundle, error)`
  - 文件名：`ca.crt`、`ca.key`、`agent.crt`、`agent.key`

- [ ] **Step 1: 写失败测试**

```go
func TestNewBundle_URIContainsClusterAndNode(t *testing.T) {
    now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
    b, err := control.NewBundle("cid-1", "nid-1", now)
    if err != nil { t.Fatal(err) }
    cid, nid, err := control.ParseIDs(b.AgentCertPEM)
    if err != nil { t.Fatal(err) }
    if cid != "cid-1" || nid != "nid-1" {
        t.Fatalf("san=%s/%s", cid, nid)
    }
    if err := control.VerifyAgent(b.CACertPEM, b.AgentCertPEM, "cid-1", "nid-1", now); err != nil {
        t.Fatal(err)
    }
}

func TestVerifyAgent_WrongCluster(t *testing.T) {
    now := time.Now()
    b, err := control.NewBundle("cid-1", "nid-1", now)
    if err != nil { t.Fatal(err) }
    err = control.VerifyAgent(b.CACertPEM, b.AgentCertPEM, "other", "nid-1", now)
    if !errcode.Is(err, errcode.DENIED) {
        t.Fatalf("got %v", err)
    }
}

func TestSignCSR_RoundTrip(t *testing.T) {
    now := time.Now()
    ca, err := control.NewBundle("cid", "seed", now)
    if err != nil { t.Fatal(err) }
    csr, key, err := control.NewCSR("cid", "joiner")
    if err != nil { t.Fatal(err) }
    cert, err := control.SignCSR(ca.CACertPEM, ca.CAKeyPEM, csr, "cid", "joiner", now)
    if err != nil { t.Fatal(err) }
    if err := control.VerifyAgent(ca.CACertPEM, cert, "cid", "joiner", now); err != nil {
        t.Fatal(err)
    }
    _ = key
}

func TestWriteBundle_KeyPerm0600(t *testing.T) {
    dir := t.TempDir()
    now := time.Now()
    b, err := control.NewBundle("c", "n", now)
    if err != nil { t.Fatal(err) }
    if err := control.WriteBundle(dir, b); err != nil { t.Fatal(err) }
    for _, name := range []string{"ca.key", "agent.key"} {
        st, err := os.Stat(filepath.Join(dir, name))
        if err != nil { t.Fatal(err) }
        if perm := st.Mode().Perm(); perm != 0o600 {
            t.Fatalf("%s perm=%o", name, perm)
        }
    }
    loaded, err := control.LoadBundle(dir)
    if err != nil { t.Fatal(err) }
    if err := control.VerifyAgent(loaded.CACertPEM, loaded.AgentCertPEM, "c", "n", now); err != nil {
        t.Fatal(err)
    }
}

func TestSignCSR_RejectsMismatchedNode(t *testing.T) {
    now := time.Now()
    ca, _ := control.NewBundle("cid", "seed", now)
    csr, _, err := control.NewCSR("join", "other")
    if err != nil { t.Fatal(err) }
    _, err = control.SignCSR(ca.CACertPEM, ca.CAKeyPEM, csr, "cid", "joiner", now)
    if !errcode.Is(err, errcode.INVALID) {
        t.Fatalf("got %v", err)
    }
}

func TestSignCSR_RewritesPlaceholderClusterURI(t *testing.T) {
    now := time.Now()
    ca, _ := control.NewBundle("cid", "seed", now)
    csr, _, err := control.NewCSR("join", "joiner")
    if err != nil { t.Fatal(err) }
    cert, err := control.SignCSR(ca.CACertPEM, ca.CAKeyPEM, csr, "cid", "joiner", now)
    if err != nil { t.Fatal(err) }
    cid, nid, err := control.ParseIDs(cert)
    if err != nil { t.Fatal(err) }
    if cid != "cid" || nid != "joiner" {
        t.Fatalf("rewritten uri=%s/%s", cid, nid)
    }
}
```

另增 `NewCSR(clusterID, nodeID string) (csrPEM, keyPEM []byte, err error)`，CSR 的 URI SAN 必须是 `procmesh://clusterID/nodeID`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/control -count=1`
Expected: FAIL package 不存在

- [ ] **Step 3: 最小实现**

- ECDSA P-256
- CA：`IsCA=true`，`KeyUsageCertSign|CRLSign`，有效期 10 年，CN=`procmesh-cluster-<clusterID>`
- Agent：`ExtKeyUsageServerAuth|ClientAuth`，有效期 2 年，CN=`node-<nodeID>`，URI SAN=`procmesh://<clusterID>/<nodeID>`
- `VerifyAgent`：用 CA 校验签名、`now` 在 NotBefore/NotAfter 内、URI 解析出的 id 与期望相等；失败 `DENIED`
- `SignCSR`：解析 CSR URI；**只强制 node_id 匹配**，cluster 段可不同；证书 URI 写成 `procmesh://<clusterID>/<nodeID>`。node_id 不符 → `INVALID`
- `WriteBundle`：`os.MkdirAll(dir, 0750)`，key `0600`，crt `0640`

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/control -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/control/pki.go internal/control/pki_test.go
git commit -m "$(cat <<'EOF'
feat: 用 Cluster CA 签发带 URI SAN 的 Agent 证书

EOF
)"
```

---

### Task 3: `cluster init` 领域逻辑（CA、secret、初始 admin）

**Files:**
- Create: `internal/control/password.go`
- Create: `internal/control/init.go`
- Create: `internal/control/init_test.go`

**Interfaces:**
- Consumes: Task 2 `NewBundle` / `WriteBundle` / `LoadBundle`
- Produces:
  - `type Meta struct { ClusterID, NodeID string; ControlMember bool; CreatedAt string }` 写入 `cluster.json`
  - `type InitResult struct { ClusterID, NodeID, AdminUser, AdminPassword string }`
  - `func Init(dir, nodeID, adminUser string, now time.Time) (InitResult, error)`
  - `func LoadMeta(dir string) (Meta, error)` — 不存在返回 `errcode.NOT_FOUND`
  - `func AlreadyInited(dir string) bool`
  - cluster secret 文件 `secret`：32 随机字节的 hex（64 字符），`0600`
  - admin bootstrap 文件 `admin.bootstrap`：`{"username":"...","password_hash":"..."}`，`0600`
  - 默认 `adminUser` 空则 `"admin"`
  - 密码：20 字符，字母数字，至少 10；argon2id 哈希
  - 已存在 `cluster.json` → `errcode.CONFLICT`
  - **不**启动 Raft，只把 `ControlMember=true` 写入 meta

- [ ] **Step 1: 写失败测试**

```go
func TestInit_WritesSecretCAAndAdmin(t *testing.T) {
    dir := t.TempDir()
    now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
    res, err := control.Init(dir, "nid-1", "", now)
    if err != nil { t.Fatal(err) }
    if res.AdminUser != "admin" { t.Fatalf("user=%q", res.AdminUser) }
    if len(res.AdminPassword) < 10 { t.Fatalf("short password") }
    if !looksUUID(res.ClusterID) { t.Fatalf("cluster_id=%q", res.ClusterID) }

    meta, err := control.LoadMeta(dir)
    if err != nil { t.Fatal(err) }
    if !meta.ControlMember || meta.NodeID != "nid-1" || meta.ClusterID != res.ClusterID {
        t.Fatalf("%+v", meta)
    }
    sec, err := os.ReadFile(filepath.Join(dir, "secret"))
    if err != nil { t.Fatal(err) }
    if len(strings.TrimSpace(string(sec))) != 64 {
        t.Fatalf("secret hex len=%d", len(strings.TrimSpace(string(sec))))
    }
    st, _ := os.Stat(filepath.Join(dir, "secret"))
    if st.Mode().Perm() != 0o600 { t.Fatalf("secret perm=%o", st.Mode().Perm()) }
    st, _ = os.Stat(filepath.Join(dir, "admin.bootstrap"))
    if st.Mode().Perm() != 0o600 { t.Fatalf("admin perm=%o", st.Mode().Perm()) }

    b, err := control.LoadBundle(dir)
    if err != nil { t.Fatal(err) }
    if err := control.VerifyAgent(b.CACertPEM, b.AgentCertPEM, res.ClusterID, "nid-1", now); err != nil {
        t.Fatal(err)
    }
    if !control.VerifyPassword(mustReadHash(t, dir), res.AdminPassword) {
        t.Fatal("password hash mismatch")
    }
}

func TestInit_ConflictIfExists(t *testing.T) {
    dir := t.TempDir()
    now := time.Now()
    if _, err := control.Init(dir, "n", "admin", now); err != nil { t.Fatal(err) }
    _, err := control.Init(dir, "n", "admin", now)
    if !errcode.Is(err, errcode.CONFLICT) {
        t.Fatalf("got %v", err)
    }
}

func TestLoadMeta_NotFound(t *testing.T) {
    _, err := control.LoadMeta(t.TempDir())
    if !errcode.Is(err, errcode.NOT_FOUND) {
        t.Fatalf("got %v", err)
    }
}
```

`mustReadHash` 读 `admin.bootstrap` 的 `password_hash`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/control -run TestInit -count=1`
Expected: FAIL `Init` undefined

- [ ] **Step 3: 最小实现**

`password.go`：

```go
func HashPassword(pw string) (string, error) // argon2id, 编码 "argon2id$v=19$m=65536,t=1,p=4$<saltB64>$<hashB64>"
func VerifyPassword(encoded, pw string) bool
func RandomPassword(n int) (string, error) // crypto/rand, [A-Za-z0-9]
```

`Init`：
1. 若 `cluster.json` 存在 → `CONFLICT`（英文消息 `cluster already initialized`）
2. `clusterID := newUUID()`
3. `NewBundle` + `WriteBundle`
4. 32 字节 secret → hex → `secret` 0600
5. 生成密码、哈希、写 `admin.bootstrap` 0600
6. 写 `cluster.json` 0640
7. 返回明文密码（仅此一次）

同步把 `cluster_id` 写入调用方 store 是 Task 8/10 的事，本任务只写磁盘。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/control -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/control/init.go internal/control/init_test.go internal/control/password.go
git commit -m "$(cat <<'EOF'
feat: 实现 cluster init 的 CA、secret 与初始 admin

EOF
)"
```

---

### Task 4: Join token 本地存储

**Files:**
- Create: `internal/control/token.go`
- Create: `internal/control/token_test.go`

**Interfaces:**
- Consumes: `errcode`
- Produces:
  - `const DefaultTokenTTL = time.Hour`；`const DefaultTokenUses = 1`
  - `type TokenInfo struct { ID string; ExpiresAt time.Time; Remaining int; Revoked bool }`
  - `func CreateToken(dir string, ttl time.Duration, uses int, now time.Time) (plaintext string, info TokenInfo, err error)`
    - `ttl<=0` → DefaultTokenTTL；`uses<=0` → DefaultTokenUses
    - 明文：`pmj_` + 32 字节 hex（`pmj_` + 64 hex）
    - 只存 `sha256(plaintext)` 的 hex；文件 `tokens.json` 0640
  - `func ConsumeToken(dir, plaintext string, now time.Time) error`
    - 找不到 / 哈希不匹配 → `INVALID` (`invalid join token`)
    - 作废 → `DENIED` (`join token revoked`)
    - 过期 → `DENIED` (`join token expired`)
    - Remaining==0 → `DENIED` (`join token exhausted`)
    - 成功则 Remaining--
  - `func RevokeToken(dir, id string) error` — 找不到 `NOT_FOUND`；成功后 Consume 必须 `DENIED`
  - **禁止**把明文写进 `tokens.json`

- [ ] **Step 1: 写失败测试**

```go
func TestCreateAndConsumeToken_Once(t *testing.T) {
    dir := t.TempDir()
    now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
    plain, info, err := control.CreateToken(dir, 0, 0, now)
    if err != nil { t.Fatal(err) }
    if !strings.HasPrefix(plain, "pmj_") || info.Remaining != 1 {
        t.Fatalf("plain=%q info=%+v", plain, info)
    }
    raw, _ := os.ReadFile(filepath.Join(dir, "tokens.json"))
    if strings.Contains(string(raw), plain) {
        t.Fatal("plaintext leaked into tokens.json")
    }
    if err := control.ConsumeToken(dir, plain, now); err != nil { t.Fatal(err) }
    err = control.ConsumeToken(dir, plain, now)
    if !errcode.Is(err, errcode.DENIED) { t.Fatalf("second consume: %v", err) }
}

func TestConsumeToken_Expired(t *testing.T) {
    dir := t.TempDir()
    now := time.Unix(1000, 0)
    plain, _, err := control.CreateToken(dir, time.Second, 1, now)
    if err != nil { t.Fatal(err) }
    err = control.ConsumeToken(dir, plain, now.Add(2*time.Second))
    if !errcode.Is(err, errcode.DENIED) { t.Fatalf("got %v", err) }
}

func TestConsumeToken_Invalid(t *testing.T) {
    err := control.ConsumeToken(t.TempDir(), "pmj_nope", time.Now())
    if !errcode.Is(err, errcode.INVALID) { t.Fatalf("got %v", err) }
}

func TestRevokeToken(t *testing.T) {
    dir := t.TempDir()
    now := time.Now()
    plain, info, err := control.CreateToken(dir, time.Hour, 2, now)
    if err != nil { t.Fatal(err) }
    if err := control.RevokeToken(dir, info.ID); err != nil { t.Fatal(err) }
    err = control.ConsumeToken(dir, plain, now)
    if !errcode.Is(err, errcode.DENIED) { t.Fatalf("got %v", err) }
}

func TestCreateToken_MultiUse(t *testing.T) {
    dir := t.TempDir()
    now := time.Now()
    plain, info, err := control.CreateToken(dir, time.Hour, 2, now)
    if err != nil { t.Fatal(err) }
    if info.Remaining != 2 { t.Fatalf("%+v", info) }
    if err := control.ConsumeToken(dir, plain, now); err != nil { t.Fatal(err) }
    if err := control.ConsumeToken(dir, plain, now); err != nil { t.Fatal(err) }
    if err := control.ConsumeToken(dir, plain, now); !errcode.Is(err, errcode.DENIED) {
        t.Fatalf("got %v", err)
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/control -run Token -count=1`
Expected: FAIL

- [ ] **Step 3: 最小实现**

`tokens.json` 结构：`{"tokens":[{id,hash,expires_unix,remaining,revoked}]}`。读写用临时文件 + rename，避免半写。ID 用 UUID。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/control -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/control/token.go internal/control/token_test.go
git commit -m "$(cat <<'EOF'
feat: 本地 join token 支持 TTL、次数与作废

EOF
)"
```

---

### Task 5: `NodeService` / `ClusterService` Protobuf

**Files:**
- Modify: `proto/procmesh/v1/api.proto`（只追加，禁止改已有字段号）
- Modify: 生成文件（`make proto`）
- Modify: `internal/api/proto_gen_test.go`（若有生成物存在性断言则扩展）

**Interfaces:**
- Consumes: 现有 `MutationMeta`、`ErrorInfo`
- Produces: `NodeService`、`ClusterService` 及下列消息。Go 包仍是 `procmeshv1` / `procmeshv1connect`

在 `api.proto` **文件末尾追加**（不要改已有 message）：

```protobuf
message ResourceSummary {
  int32 cpu_percent = 1;
  int32 memory_percent = 2;
  int32 disk_percent = 3;
}

message ProcessSummary {
  string name = 1;
  string desired = 2;
  string observed = 3;
  string health = 4;
  int64 latest_revision = 5;
  int64 active_revision = 6;
  int64 freshness_unix_ms = 7;
}

message Node {
  string node_id = 1;
  string cluster_id = 2;
  string hostname = 3;
  string boot_id = 4;
  string state = 5; // JOINING | ALIVE | SUSPECT | FAILED | LEFT | REMOVED | REVOKED
  string agent_version = 6;
  int32 protocol_version = 7;
  string api_address = 8;
  string rpc_address = 9;
  string gossip_address = 10;
  map<string, string> labels = 11;
  ResourceSummary resources = 12;
  repeated ProcessSummary processes = 13;
  int64 last_updated_unix_ms = 14;
}

message ListNodesRequest {}
message ListNodesResponse { repeated Node nodes = 1; }

message GetNodeRequest { string id_or_hostname = 1; }
message GetNodeResponse { Node node = 1; }

message CreateJoinTokenRequest {
  MutationMeta meta = 1;
  int64 ttl_seconds = 2; // 0 = default
  int32 uses = 3;        // 0 = default
}
message CreateJoinTokenResponse {
  string token_id = 1;
  string token = 2;
  int64 expires_unix = 3;
  int32 uses = 4;
}

message RevokeJoinTokenRequest {
  MutationMeta meta = 1;
  string token_id = 2;
}
message RevokeJoinTokenResponse {}

service NodeService {
  rpc ListNodes(ListNodesRequest) returns (ListNodesResponse);
  rpc GetNode(GetNodeRequest) returns (GetNodeResponse);
  rpc CreateJoinToken(CreateJoinTokenRequest) returns (CreateJoinTokenResponse);
  rpc RevokeJoinToken(RevokeJoinTokenRequest) returns (RevokeJoinTokenResponse);
}

message InitClusterRequest {
  MutationMeta meta = 1;
  string admin_username = 2;
}
message InitClusterResponse {
  string cluster_id = 1;
  string node_id = 2;
  string admin_username = 3;
  string admin_password = 4;
}

message JoinClusterRequest {
  MutationMeta meta = 1;
  string token = 2;
  string node_id = 3;
  string hostname = 4;
  string boot_id = 5;
  int32 protocol_version = 6;
  string api_address = 7;
  string gossip_address = 8;
  string rpc_address = 9;
  bytes csr_pem = 10;
}
message JoinClusterResponse {
  string cluster_id = 1;
  bytes ca_pem = 2;
  bytes cert_pem = 3;
  string gossip_address = 4;
}

message ClusterOverviewRequest {}
message ClusterOverviewResponse {
  string cluster_id = 1;
  int32 members = 2;
  int32 alive = 3;
}

message RequestJoinRequest {
  MutationMeta meta = 1;
  string seed_server = 2; // host:port of seed :18680
  string token = 3;
}
message RequestJoinResponse {
  string cluster_id = 1;
  string gossip_address = 2;
}

service ClusterService {
  rpc Init(InitClusterRequest) returns (InitClusterResponse);
  rpc Join(JoinClusterRequest) returns (JoinClusterResponse);
  rpc RequestJoin(RequestJoinRequest) returns (RequestJoinResponse);
  rpc Overview(ClusterOverviewRequest) returns (ClusterOverviewResponse);
}
```

- [ ] **Step 1: 写失败测试（生成物/描述符）**

扩展或新增 `internal/api/proto_gen_test.go`：

```go
func TestProto_NodeAndClusterServicesGenerated(t *testing.T) {
    if procmeshv1connect.NodeServiceName == "" {
        t.Fatal("missing NodeService")
    }
    if procmeshv1connect.ClusterServiceName == "" {
        t.Fatal("missing ClusterService")
    }
    _ = (&procmeshv1.JoinClusterRequest{}).GetCsrPem
    _ = (&procmeshv1.InitClusterResponse{}).GetAdminPassword
    _ = (&procmeshv1.RequestJoinRequest{}).GetSeedServer
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestProto_NodeAndClusterServicesGenerated -count=1`
Expected: FAIL 找不到 `NodeServiceName`

- [ ] **Step 3: 追加 proto 并生成**

```bash
make proto
```

禁止手改 `*.pb.go` / `*.connect.go`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api -run TestProto -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add proto/procmesh/v1/api.proto proto/procmesh/v1/api.pb.go proto/procmesh/v1/procmeshv1connect/api.connect.go internal/api/proto_gen_test.go
git commit -m "$(cat <<'EOF'
feat: 添加 NodeService 与 ClusterService 的 Protobuf

EOF
)"
```

---

### Task 6: memberlist Mesh + summary 编解码

**Files:**
- Create: `internal/cluster/summary.go`
- Create: `internal/cluster/codec.go`
- Create: `internal/cluster/codec_test.go`
- Create: `internal/cluster/mesh.go`
- Create: `internal/cluster/mesh_test.go`
- Modify: `internal/version/version.go` — 增加 `const Agent = "0.0.0-dev"`
- Modify: `go.mod` / `go.sum` — 添加 `github.com/hashicorp/memberlist`

**Interfaces:**
- Consumes: 无 process 包
- Produces:

```go
package cluster

type State string
const (
    StateJoining State = "JOINING"
    StateAlive   State = "ALIVE"
    StateSuspect State = "SUSPECT"
    StateFailed  State = "FAILED"
    StateLeft    State = "LEFT"
    StateRemoved State = "REMOVED"
    StateRevoked State = "REVOKED"
)

type ProcessSummary struct {
    Name, Desired, Observed, Health string
    LatestRevision, ActiveRevision  int64
    FreshnessUnixMs                 int64
}

type ResourceSummary struct {
    CPUPercent, MemoryPercent, DiskPercent int
}

type NodeSummary struct {
    NodeID, ClusterID, Hostname, BootID string
    State                               State
    AgentVersion                        string
    ProtocolVersion                     int
    APIAddress, RPCAddress, GossipAddress string
    Labels                              map[string]string
    Resources                           ResourceSummary
    Processes                           []ProcessSummary
    LastUpdatedUnixMs                   int64
}

type SummarySource interface {
    Snapshot() NodeSummary
}

type Config struct {
    NodeID      string
    BindAddr    string // default 127.0.0.1
    BindPort    int    // 0 = ephemeral（测试）
    Advertise   string // host:port，可空
    Source      SummarySource
    Protocol    int    // must be version.Protocol
    Logger      *log.Logger // 可空
}

type Mesh struct { /* 未导出 memberlist + 本地 view */ }

func Start(cfg Config) (*Mesh, error)
func (m *Mesh) Join(seeds []string) (int, error)
func (m *Mesh) Leave(timeout time.Duration) error
func (m *Mesh) Shutdown() error
func (m *Mesh) Members() []NodeSummary // 含本机；按 node_id 排序
func (m *Mesh) LocalAddr() string      // host:port 实际 bind
func (m *Mesh) Update()                // 从 Source 刷新并广播

func EncodeMeta(s NodeSummary) []byte   // NodeMeta：仅身份小字段，必须 < 512 字节
func DecodeMeta(b []byte) (NodeSummary, error)
func EncodeState(s NodeSummary) []byte  // LocalState：含 processes/resources
func DecodeState(b []byte) (NodeSummary, error)
```

编码：JSON 即可（payload 小）。NodeMeta **不得**带 `Processes`（防超 512）。`LocalState` / `MergeRemoteState` 传完整摘要。

memberlist `Config.Name` 必须是 `nodeID + "#" + bootID`（避免 VM clone 被当成同一 memberlist 节点「重生」）。`NodeMeta` 里仍带真正的 `node_id`。

默认探测间隔对单测：测试里用 `BindPort=0`，`ProbeInterval=50ms` 等短间隔（可在 Start 里若 `testing` 不依赖：导出 `cfg.TestFast bool`，测试置 true）。

- [ ] **Step 1: 写编解码失败测试**

```go
func TestEncodeMeta_OmitsProcessesAndFits512(t *testing.T) {
    s := cluster.NodeSummary{
        NodeID: "n1", ClusterID: "c1", Hostname: "h", BootID: "b",
        State: cluster.StateAlive, AgentVersion: version.Agent,
        ProtocolVersion: version.Protocol, APIAddress: "127.0.0.1:18680",
        GossipAddress: "127.0.0.1:18689",
        Processes: []cluster.ProcessSummary{{Name: "web", Desired: "RUNNING"}},
    }
    raw := cluster.EncodeMeta(s)
    if len(raw) >= 512 { t.Fatalf("meta too large: %d", len(raw)) }
    got, err := cluster.DecodeMeta(raw)
    if err != nil { t.Fatal(err) }
    if len(got.Processes) != 0 { t.Fatalf("meta must omit processes: %+v", got.Processes) }
    if got.NodeID != "n1" || got.ProtocolVersion != 1 { t.Fatalf("%+v", got) }
}

func TestEncodeState_KeepsProcessSummary(t *testing.T) {
    s := cluster.NodeSummary{
        NodeID: "n1", Processes: []cluster.ProcessSummary{{
            Name: "web", Desired: "RUNNING", Observed: "RUNNING",
            Health: "HEALTHY", LatestRevision: 3, ActiveRevision: 3,
            FreshnessUnixMs: 100,
        }},
    }
    got, err := cluster.DecodeState(cluster.EncodeState(s))
    if err != nil { t.Fatal(err) }
    if len(got.Processes) != 1 || got.Processes[0].Name != "web" {
        t.Fatalf("%+v", got)
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster -count=1`
Expected: FAIL

- [ ] **Step 3: 实现 codec + version.Agent**

- [ ] **Step 4: 写两节点 mesh 测试**

```go
func TestMesh_TwoNodesSeeEachOther(t *testing.T) {
    srcA := &staticSource{s: cluster.NodeSummary{NodeID: "na", BootID: "ba", Hostname: "a", State: cluster.StateAlive, ProtocolVersion: 1}}
    srcB := &staticSource{s: cluster.NodeSummary{NodeID: "nb", BootID: "bb", Hostname: "b", State: cluster.StateAlive, ProtocolVersion: 1}}
    a, err := cluster.Start(cluster.Config{NodeID: "na", BindAddr: "127.0.0.1", BindPort: 0, Source: srcA, Protocol: 1, TestFast: true})
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = a.Shutdown() })
    b, err := cluster.Start(cluster.Config{NodeID: "nb", BindAddr: "127.0.0.1", BindPort: 0, Source: srcB, Protocol: 1, TestFast: true})
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = b.Shutdown() })
    if _, err := b.Join([]string{a.LocalAddr()}); err != nil { t.Fatal(err) }

    waitMembers(t, a, 2)
    waitMembers(t, b, 2)
    ids := map[string]bool{}
    for _, m := range a.Members() { ids[m.NodeID] = true }
    if !ids["na"] || !ids["nb"] { t.Fatalf("%v", ids) }
}
```

`waitMembers` 最多等 3s。`staticSource` 实现 `Snapshot()`。

- [ ] **Step 5: `go get github.com/hashicorp/memberlist` 并实现 Mesh**

Delegate：
- `NodeMeta`：`EncodeMeta(source.Snapshot())` 并填 LocalAddr
- `LocalState`：`EncodeState(source.Snapshot())`
- `MergeRemoteState`：解码后写入 `map[node_id]NodeSummary`
- `NotifyJoin`/`NotifyLeave`/`NotifyUpdate`：更新状态（leave → LEFT；memberlist dead → FAILED）

本机始终出现在 `Members()`。

- [ ] **Step 6: 跑测试确认通过**

Run: `go test ./internal/cluster ./internal/version -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/cluster internal/version/version.go go.mod go.sum
git commit -m "$(cat <<'EOF'
feat: 用 memberlist 同步节点摘要

EOF
)"
```

---

### Task 7: 重复 `node_id` 与 `protocol_version` 检查

**Files:**
- Create: `internal/cluster/check.go`
- Create: `internal/cluster/check_test.go`
- Modify: `internal/cluster/mesh.go`（MergeRemoteState / Join 路径调用检查）

**Interfaces:**
- Consumes: `NodeSummary`、`errcode`
- Produces:
  - `type JoinIdentity struct { NodeID, BootID string; ProtocolVersion int }`
  - `func CheckJoin(existing []NodeSummary, req JoinIdentity) error`
    - `req.ProtocolVersion != version.Protocol` → `INCOMPATIBLE_VERSION`（消息 `incompatible protocol version`）
    - 已有成员 `node_id` 相同，且其 `State` 不是 `LEFT`/`REMOVED`/`REVOKED`，且 `BootID != req.BootID` → `DUPLICATE_NODE_ID`（消息 `duplicate node_id`）
    - 已有成员相同 `node_id` **且相同 `BootID`**：视为重入，允许
    - `req.NodeID` 空 → `INVALID`
  - Mesh 在 `MergeRemoteState`：若远程 `node_id` 等于本机但 `boot_id` 不同 → 不纳入 view，记录冲突（测试可通过 `func (m *Mesh) DuplicateConflicts() []string` 读到该 node_id）
  - 远程 `protocol_version != 1`：纳入 view 但 `State` 保持其值，另在 summary 上可识别；`CheckJoin` 仍拒绝其作为加入者。Mesh **不**把不兼容节点当可 Join 种子。本阶段最低要求：`CheckJoin` 单测 + Mesh 对本机 node_id 冲突不覆盖本机摘要。

- [ ] **Step 1: 写失败测试**

```go
func TestCheckJoin_DuplicateDifferentBoot(t *testing.T) {
    err := cluster.CheckJoin([]cluster.NodeSummary{{
        NodeID: "n", BootID: "b1", State: cluster.StateAlive, ProtocolVersion: 1,
    }}, cluster.JoinIdentity{NodeID: "n", BootID: "b2", ProtocolVersion: 1})
    if !errcode.Is(err, errcode.DUPLICATE_NODE_ID) {
        t.Fatalf("got %v", err)
    }
}

func TestCheckJoin_SameBootRejoinOK(t *testing.T) {
    err := cluster.CheckJoin([]cluster.NodeSummary{{
        NodeID: "n", BootID: "b1", State: cluster.StateAlive, ProtocolVersion: 1,
    }}, cluster.JoinIdentity{NodeID: "n", BootID: "b1", ProtocolVersion: 1})
    if err != nil { t.Fatal(err) }
}

func TestCheckJoin_LeftAllowsNewBoot(t *testing.T) {
    err := cluster.CheckJoin([]cluster.NodeSummary{{
        NodeID: "n", BootID: "b1", State: cluster.StateLeft, ProtocolVersion: 1,
    }}, cluster.JoinIdentity{NodeID: "n", BootID: "b2", ProtocolVersion: 1})
    if err != nil { t.Fatal(err) }
}

func TestCheckJoin_IncompatibleVersion(t *testing.T) {
    err := cluster.CheckJoin(nil, cluster.JoinIdentity{NodeID: "n", BootID: "b", ProtocolVersion: 2})
    if !errcode.Is(err, errcode.INCOMPATIBLE_VERSION) {
        t.Fatalf("got %v", err)
    }
}

func TestMesh_DoesNotOverwriteLocalOnDuplicateNodeID(t *testing.T) {
    src := &staticSource{s: cluster.NodeSummary{NodeID: "n", BootID: "b-local", Hostname: "me", State: cluster.StateAlive, ProtocolVersion: 1}}
    m, err := cluster.Start(cluster.Config{NodeID: "n", BindAddr: "127.0.0.1", Source: src, Protocol: 1, TestFast: true})
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = m.Shutdown() })
    m.MergeForTest(cluster.EncodeState(cluster.NodeSummary{NodeID: "n", BootID: "b-clone", Hostname: "clone", ProtocolVersion: 1}))
    locals := m.Members()
    var self cluster.NodeSummary
    for _, x := range locals {
        if x.NodeID == "n" && x.BootID == "b-local" { self = x }
    }
    if self.Hostname != "me" { t.Fatalf("local overwritten: %+v", locals) }
    if len(m.DuplicateConflicts()) == 0 { t.Fatal("expected duplicate conflict") }
}
```

`MergeForTest` 仅测试导出，调用与 `MergeRemoteState` 相同逻辑。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster -count=1`
Expected: FAIL `CheckJoin` undefined

- [ ] **Step 3: 实现并接入 Mesh**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cluster -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/check.go internal/cluster/check_test.go internal/cluster/mesh.go
git commit -m "$(cat <<'EOF'
feat: 拒绝重复 node_id 与不兼容协议版本

EOF
)"
```

---

### Task 8: Connect 处理器（Init / Join / List / Token）

**Files:**
- Create: `internal/api/clusterapi.go`
- Create: `internal/api/clusterapi_test.go`
- Create: `internal/api/node.go`
- Create: `internal/api/node_test.go`
- Modify: `internal/api/server.go` — 挂载 NodeService、ClusterService；Options 增加集群依赖
- Modify: `internal/store/meta.go` 调用方在 Init 成功后 `SetClusterID`

**Interfaces:**
- Consumes: `control.Init` / `CreateToken` / `ConsumeToken` / `RevokeToken` / `LoadMeta` / `LoadBundle` / `SignCSR` / `NewCSR`；`cluster.Mesh` / `CheckJoin`
- Produces: Connect handlers

```go
type ClusterDeps struct {
    Dir        string          // layout.ClusterDir
    Store      ClusterMetaStore // GetOrCreateNodeID, SetClusterID, GetClusterID
    Mesh       NodeLister      // Members() []cluster.NodeSummary；可空则 List 只返回 Local
    Local      func() cluster.NodeSummary
    GossipAddr func() string   // 本机 advertise，Join 响应用
    Now        func() time.Time
}

type ClusterMetaStore interface {
    GetOrCreateNodeID(ctx context.Context) (string, error)
    SetClusterID(ctx context.Context, id string) error
    GetClusterID(ctx context.Context) (string, error)
}

type NodeLister interface {
    Members() []cluster.NodeSummary
}
```

行为：
- `Init`：`operation_id` 空 → `INVALID`。调用 `control.Init`；成功后 `SetClusterID`。已 init → `CONFLICT`。
- `CreateJoinToken`：未 init → `FAILED`/`INVALID`（`cluster not initialized`）。空 operation_id → `INVALID`。
- `Join`（种子侧）：未 init → `INVALID`。`CheckJoin(members, req)`；`ConsumeToken`；`SignCSR`（重写 URI 为真实 cluster）；返回 ca/cert/本机 gossip。
- `RequestJoin`（加入者本机）：已有 `cluster.json` → `CONFLICT`。对本机 `NewCSR("join", localNodeID)`，向 `seed_server` 调种子 `Join`（填本机 node_id/boot_id/hostname/protocol/api/gossip + CSR），把返回的 CA+证书与本机 key 写成 Bundle（**不**写 ca.key / secret），写 `cluster.json{control_member:false}`，`SetClusterID`，`Mesh.Join([]string{gossip})`。种子不可达 → `UNAVAILABLE`。
- `ListNodes`：`Mesh.Members()`；无 Mesh 时返回 `Local()` 一个节点（standalone）。
- `GetNode`：按 `node_id` 精确匹配，否则 hostname；都 miss → `NOT_FOUND`。
- `Overview`：members 计数、state==ALIVE 计数、cluster_id。
- **不要**在 Init 后关闭无认证。
- DEGRADED 时写操作（Init/Join/Create/Revoke）返回 `DEGRADED`，List/Get/Overview 仍可用。

- [ ] **Step 1: 写失败测试（httptest + Connect client）**

`clusterapi_test.go` 用 `api.NewServer` + `httptest.NewServer`：

1. `Init` 返回 cluster_id 与一次性 admin_password；第二次 Init → `CONFLICT`
2. `CreateJoinToken` 在 init 前 → 非 OK；init 后拿到 `pmj_` 前缀
3. `Join` 用该 token + `control.NewCSR("join", nodeID)` 得到可 `VerifyAgent` 的证书（URI cluster 为种子 cluster_id）
4. 第二次用同一 token Join → `DENIED`
5. 两个不同 boot_id、相同 node_id 的 Join：先把 Local/Mesh 做成已有该 node_id → `DUPLICATE_NODE_ID`
6. `ListNodes` 至少包含本机 node_id
7. 缺 `operation_id` 的 Init → `INVALID`
8. 两个 httptest：种子 Init+CreateToken；加入者 `RequestJoin(seed, token)` 后加入者 `LoadMeta` 有相同 cluster_id 且 `ControlMember==false`；种子 `ListNodes` 不必已 gossip（本任务可不启 mesh join），但加入者磁盘上证书能被种子 CA `VerifyAgent`

可先注入假 Mesh（切片）。不要在本任务启动 `agent.Run`。

`ClusterDeps` 还需：`NodeID`、`Hostname`、`BootID`、`APIAddr`、`HTTPClient`（加入者用来打种子）。`RequestJoin` 用这些填 `JoinClusterRequest`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run 'Init|Join|ListNodes|CreateJoin' -count=1`
Expected: FAIL

- [ ] **Step 3: 实现 handlers 并在 `NewServer` 挂载**

```go
np, nh := procmeshv1connect.NewNodeServiceHandler(&NodeAPI{...})
cp, ch := procmeshv1connect.NewClusterServiceHandler(&ClusterAPI{...})
```

`api.Options` 增加 `Cluster ClusterDeps`（零值表示未接线；Init/Join 返回 `UNAVAILABLE` / `cluster not configured`，List 走 Local 若提供）。测试里填完整 Deps。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api -count=1`
Expected: PASS（含原 P1 测试）

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "$(cat <<'EOF'
feat: 实现 Cluster/Node Connect 接口

EOF
)"
```

---

### Task 9: CLI `cluster` / `node` / `agent join`

**Files:**
- Modify: `internal/cli/root.go`（usage、dispatch、新 flag）
- Modify: `internal/cli/client.go`（Node/Cluster client）
- Create: `internal/cli/cluster.go`
- Create: `internal/cli/node.go`
- Modify: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: Task 5 生成的 client、Task 8 服务
- Produces:

```text
procmesh cluster init [--admin-user NAME]
procmesh agent join --seed HOST:PORT --token TOKEN
procmesh node list
procmesh node status [id-or-hostname]
procmesh node token create [--ttl DURATION] [--uses N]
procmesh node token revoke TOKEN_ID
```

全局已有 `--server`、`--operation-id`。新增：
- `--admin-user`（仅 init）
- `--seed`（join，种子 `:18680`）
- `--token`（join）
- `--ttl`（默认 `1h`，`time.ParseDuration`）
- `--uses`（默认 1）

`agent join` 调 **本机** Agent 的 `ClusterService.RequestJoin`（默认 `--server 127.0.0.1:18680` 是加入者本机；种子地址是 `--seed`）。

新增 flag：
- `--seed HOST:PORT`（`agent join` 必填，种子的 `:18680`）
- `--token`（join 必填）

注意：全局 `--server` 仍指向 CLI 要说话的 Agent。加入者本机执行：

```text
procmesh --server 127.0.0.1:18680 agent join --seed 10.0.0.1:18680 --token pmj_...
```

单测里两个 httptest 时：对加入者 server 调 `agent join --seed <seedURL>`。

`RequestJoin` / `SignCSR` 契约已在 Task 2/5/8 锁定，本任务不要再改 proto 字段号。

CLI 输出（稳定、可测）：

```text
# cluster init
cluster_id=<id>
node_id=<id>
admin_user=admin
admin_password=<pw>

# node list （空格对齐即可，至少这些列）
<node_id>\t<hostname>\t<state>\t<protocol>\t<api>\t<gossip>

# node status
同 List 过滤单行；找不到 exit 1 + NOT_FOUND

# node token create
token_id=<id>
token=<plain>
expires=<unix>
uses=<n>

# agent join
cluster_id=<id>
gossip=<addr>
```

- [ ] **Step 1: 写 CLI 失败测试**

复用 `newTestServer`，但要注入 ClusterDeps（扩展 helper）。

```go
func TestCLI_ClusterInitAndNodeList(t *testing.T) {
    url := newClusterTestServer(t)
    code, out, errb := runCLI("--server", url, "cluster", "init")
    if code != 0 { t.Fatalf("init exit=%d stderr=%q stdout=%q", code, errb, out) }
    if !strings.Contains(out, "cluster_id=") || !strings.Contains(out, "admin_password=") {
        t.Fatalf("stdout=%q", out)
    }
    code, out, errb = runCLI("--server", url, "node", "list")
    if code != 0 { t.Fatalf("list exit=%d %q %q", code, errb, out) }
    if !strings.Contains(out, "ALIVE") && !strings.Contains(out, "JOINING") {
        t.Fatalf("list=%q", out)
    }
}

func TestCLI_TokenCreate(t *testing.T) {
    url := newClusterTestServer(t)
    if code, _, errb := runCLI("--server", url, "cluster", "init"); code != 0 {
        t.Fatalf("init %s", errb)
    }
    code, out, errb := runCLI("--server", url, "node", "token", "create")
    if code != 0 { t.Fatalf("%d %q %q", code, errb, out) }
    if !strings.Contains(out, "token=pmj_") { t.Fatalf("%q", out) }
}

func TestCLI_UnknownStillUsage(t *testing.T) {
    code, _, errb := runCLI("foobar")
    if code != 2 || errb == "" { t.Fatalf("%d %q", code, errb) }
}
```

`RequestJoin` 的端到端放到 Task 10（需要两个 server）。本任务至少：init / list / token create / token revoke / usage。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli -run 'ClusterInit|TokenCreate' -count=1`
Expected: FAIL unknown command

- [ ] **Step 3: 实现 CLI**

更新 usageText。`newClient` 增加 node/cluster clients。`agent join` 调本机 `RequestJoin`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cli ./internal/api ./internal/control -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli proto/procmesh/v1 internal/api internal/control
git commit -m "$(cat <<'EOF'
feat: 添加 cluster init、node list 与 join CLI

EOF
)"
```

---

### Task 10: Agent 接线、gossip 配置、两节点验收

**Files:**
- Modify: `internal/agent/run.go`
- Modify: `cmd/procmesh-agent/main.go`（可选 `--gossip` flag）
- Modify: `internal/agentcfg/load.go` + `load_test.go`（`gossip.listen` / `gossip.advertise`）
- Modify: `internal/api/server.go` / `metrics.go`（`procmesh_cluster_members` / `procmesh_cluster_alive_members`）
- Create: `internal/agent/cluster_accept_test.go` — 两 Agent + join + duplicate
- Modify: `docs/superpowers/plans/2026-08-13-v1-mvp.md` — P2 行改为本计划路径

**Interfaces:**
- Consumes: 以上全部
- Produces: 可演示路径

`agent.yaml`：

```yaml
gossip:
  listen: 127.0.0.1:18689
  advertise: ""
```

`agentcfg.Load` 继续返回 disk Policy；**另增** `func LoadFile(path string, required bool) (Config, error)` 或扩展现有 Load 返回 `agentcfg.Config{Disk logmgr.Policy; Gossip Gossip}`。为减少破坏：增加 `LoadAll`，`Run` 改用 `LoadAll`；旧 `Load` 保留给现有测试（内部调 LoadAll 只取 Disk）。

`agent.Options`：

```go
GossipListen    string // 默认 127.0.0.1:18689
GossipAdvertise string
```

`Run`：
1. identity.Ensure + SetBootID（Task 1 已做）
2. 若 `control.AlreadyInited(layout.ClusterDir)` 或存在 `agent.crt`：加载 bundle，启动 Mesh
3. 未 init：仍启动单机 Mesh（本机 Members=1），方便 `node list`
4. `SummarySource` 从 `process.Manager` 填 ProcessSummary（在 **agent 包** 实现，禁止 process import cluster）。资源摘要本阶段可全 0；若容易拿到磁盘占用百分比可填 DiskPercent，否则 0
5. `api.Options.Cluster` 接上 Dir/Store/Mesh/Local/GossipAddr
6. `hostname, _ := os.Hostname()`
7. `rpc_address` 本阶段填空或 `127.0.0.1:18683` 占位字符串（不监听）
8. 非环回 gossip bind 走与 HTTP 相同的 `--insecure-listen` 检查
9. 关闭时 `Mesh.Leave` + `Shutdown`（不要杀 shim）

`RequestJoin` 成功后本机 `SetClusterID` 并 `Mesh.Join`。

Metrics 在现有文本上追加：

```
procmesh_cluster_members <n>
procmesh_cluster_alive_members <n>
```

- [ ] **Step 1: 写验收测试（先红）**

`internal/agent/cluster_accept_test.go`：

```go
func TestAccept_NodeListAfterInit(t *testing.T) {
    // 启动 1 个 agent.Run（OnListen），CLI cluster init，CLI node list 含本机 node_id
}

func TestAccept_JoinTwoAgents(t *testing.T) {
    // Agent A、B 两个 data-dir
    // A: cluster init → token create
    // B: agent join --server A --token
    // A 与 B 的 node list 都看到 2 行，node_id 不同
}

func TestAccept_DuplicateNodeIDRejected(t *testing.T) {
    // Agent A init
    // 把 A 的 node_id 文件复制到 C 的 data-dir（先让 C Ensure 之前写入相同 UUID）
    // C RequestJoin / CLI join → 错误含 DUPLICATE_NODE_ID
    // A 的进程平面不受影响（可同时 apply 一个 never 进程仍在）
}
```

启动 Agent 用 `agent.Run` + `OnListen`，HTTP 与 gossip 都绑 `127.0.0.1:0`（Options 支持 GossipListen=`127.0.0.1:0`）。

CLI 走 `cli.Main`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent -run TestAccept_ -count=1`
Expected: FAIL（未接线或 join 未启动 gossip）

- [ ] **Step 3: 接线实现**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/agent ./internal/agentcfg ./internal/api ./internal/cli ./internal/cluster ./internal/control ./internal/identity -count=1`
Expected: PASS

再跑全量：

Run: `go test ./...`
Expected: PASS

覆盖率抽查（不得低于 P0 门槛）：

```bash
go test ./internal/process ./internal/shim ./internal/store -cover
```

- [ ] **Step 5: 更新计划索引**

`docs/superpowers/plans/2026-08-13-v1-mvp.md` 的 P2 行改为：

`[2026-08-15-p2-cluster-identity-gossip.md](./2026-08-15-p2-cluster-identity-gossip.md)`

- [ ] **Step 6: Commit**

```bash
git add internal/agent internal/agentcfg internal/api/metrics.go internal/api/server.go cmd/procmesh-agent docs/superpowers/plans/2026-08-13-v1-mvp.md
git commit -m "$(cat <<'EOF'
feat: Agent 启动 gossip 并验收两节点加入

EOF
)"
```

---

## 自检（写作后）

| Spec 项 | 任务 |
|---------|------|
| node_id 文件 + UUID | Task 1 |
| boot_id 文件（host）且不破坏 recover | Task 1 |
| cluster init：cluster_id / CA / secret / admin | Task 3 |
| 证书 URI 含 cluster_id、node_id | Task 2 |
| Control Member 标记、无 Raft | Task 3 |
| join token TTL/次数/作废 | Task 4 |
| memberlist + summary | Task 6 |
| DUPLICATE_NODE_ID | Task 7 + Task 10 |
| INCOMPATIBLE_VERSION | Task 7 |
| NodeService / ClusterService | Task 5、8 |
| CLI node list / cluster init / join | Task 9–10 |
| 不关闭免认证、无 Raft、无 remove、无 :18683 mTLS | Global Constraints |
| process 不 import cluster/control | Global Constraints + Task 10 source 在 agent |

无 TBD/TODO 占位。类型名在后续任务与前面 Produces 一致。
