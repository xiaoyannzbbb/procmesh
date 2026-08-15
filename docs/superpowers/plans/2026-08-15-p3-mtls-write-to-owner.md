# P3 mTLS Direct RPC + Write-to-Owner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在已完成的 P2 集群身份与 Gossip 之上，交付 Agent 间 `:9001` mTLS Direct RPC、入口 Write-to-Owner 转发，以及 Owner 端 `operation_id` 幂等，使 `procmesh --server A --node C restart <name>` 能从 A 重启 C 上的进程。

**Architecture:** `:9000` 仍是 CLI/外部入口（P4 完成前环回免认证保持）。远程 Mutation 由入口 Agent 经 mTLS 转发到 Owner `:9001`，入口不改权威副本、不用 Gossip 传 Mutation。`:9001` 只服务本机 Process/Config/Log（`LocalOnly`，禁止再转发）。日常认证只有 mTLS；证书 URI SAN 必须是 `procmesh://<cluster_id>/<node_id>`。Join 仍走 `:9000`（加入者尚无证书）。`process` 不得 import `cluster` / `control` / `rpc`。

**Tech Stack:** Go 1.23、已有 ConnectRPC + Gin、标准库 `crypto/tls` / `crypto/x509`、P2 的 Cluster CA 与 Agent 证书、hashicorp/memberlist（只读 RPC 地址）。

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- Go 版本下限：`1.23`
- CGO-free SQLite only：`modernc.org/sqlite`（禁止 `mattn/go-sqlite3`）
- Linux 是生产保证面；macOS 必须能编译并跑单测 + 非 cgroup 集成
- `process` 不得 import `cluster`、`control` 或 `rpc`
- `cluster` 与 `process` 只交换 summary DTO（经接口/回调），禁止 cluster 依赖 process 内部状态机
- 日志正文只在文件里，不进 SQLite
- 所有 Mutation 必须带非空 `operation_id`（UUID）；CLI 未传则自己生成
- 无 `operation_id` 的远程写必须拒绝（`INVALID`）
- 错误码沿用 `internal/errcode`：`OK`、`CONFLICT`、`UNAVAILABLE`、`TIMEOUT`、`DENIED`、`DEGRADED`、`DUPLICATE_NODE_ID`、`INCOMPATIBLE_VERSION`、`NOT_FOUND`、`INVALID`
- 应用错误码放在 Connect error detail（`ErrorInfo.code`），消息为英文
- 对外主协议是 ConnectRPC；REST 仅 `/healthz`、`/readyz`、`/metrics`
- 监听默认 `127.0.0.1:9000`、`127.0.0.1:9001`、`127.0.0.1:7946`；非环回必须 `--insecure-listen`
- **P4 完成前不关闭环回无认证。** `cluster init` 之后仍允许本机 `:9000` 无认证管理。禁止在本阶段实现登录或关掉 loopback 免认证。
- **本阶段不启动 Raft、不实现 CRL、不实现 `node remove`、不实现用户/RBAC。**
- Agent RPC 必须 mTLS。证书含 `cluster_id`、`node_id`（URI SAN `procmesh://<cluster_id>/<node_id>`）
- cluster secret **不**作为日常 RPC 凭证
- 远程 Mutation 必须 Direct RPC 到 Owner，禁止「改本地副本再 Gossip」
- 禁止因对端 FAILED 而在本机创建对方的 Process
- 远程超时：客户端标 `TIMEOUT`（结果未知）；Owner 不可达 / 无 `rpc_address` / 状态为 FAILED 标 `UNAVAILABLE`
- 重试必须复用 `operation_id`；目标端重复 `operation_id` 返回上次结果，不得重放
- V1.0 `protocol_version = 1`（`internal/version.Protocol`）；不兼容则 `INCOMPATIBLE_VERSION`
- Join token 本阶段仍只存本机 `cluster/tokens.json`；Join 仍走 `:9000`
- Cluster CA 私钥与 cluster secret、admin bootstrap、agent key 权限必须 `0600`
- 测试与代码同目录：`internal/foo/foo_test.go`
- 强制 TDD：先红后绿
- P0 覆盖率门槛保持：`internal/process`、`internal/shim`、`internal/store` ≥ 80%
- 本阶段 `internal/control` 覆盖率门槛保持 ≥ 80%
- 文档与本计划使用中文；API 错误码与错误消息使用英文
- 生成的 proto Go 文件禁止手改；本阶段 **不改** proto（用 HTTP 头传 target/source node）

## 规格解读（P3 边界）

来源：`docs/v2-prd/v2-prd.md` 与 `docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。冲突以架构 spec 为准。

1. **P3 可演示出口**（spec §13）：从 A 重启 C 上的进程。
2. **端口**（spec §5.1）：Agent RPC 默认 `:9001`，ConnectRPC + mTLS。
3. **远程 Mutation**（spec §9.5 / PRD §4.5）：`Client → 入口 :9000 → mTLS RPC → Owner :9001 → operation_id 去重 → 本地 commit`。入口不改权威副本。Owner 不信任入口的「已授权」声明。**P4 才有 RBAC/session**；本阶段 Owner 只再验 mTLS 证书（集群匹配、证书有效）。入口转发 `Procmesh-Source-Node`（本机 `node_id`）与原 `operation_id` / `operator`。
4. **幂等**（PRD §48 / spec §3.9 / Case 6）：目标 Agent 收到重复 `operation_id` 返回上次结果。Journal 已在 P0 Owner 本地；转发不得在入口再执行。
5. **故障**（spec §12 / Case 2）：网络分区两侧本地 Process 继续；跨区操作 `TIMEOUT`/`UNAVAILABLE`；**禁止**因对端 FAILED 而在本机创建对方进程。
6. **CLI**（spec §11.2）：远程加 `--server` / `--node`。P1 拒绝 `--node` 的行为在本阶段撤销。`--node` 值可以是 `node_id` 或 hostname。
7. **路由**：
   - 请求头 `Procmesh-Target-Node`（CLI `--node` 写入）优先。
   - 否则 `spec.owner_agent_id`（Apply 时）。
   - 否则 Gossip 按 process name 找 Owner。
   - 目标等于本机 `node_id`（或本机 hostname）→ 走现有本地 Manager。
   - 目标是远端 → 查 Gossip 的 `rpc_address`，mTLS 转发；找不到 / 无地址 / FAILED → `UNAVAILABLE`，**不**在本机 apply。
8. **`:9001` 生命周期**：未入群（无 `ca.crt`+`agent.crt`+`agent.key`）不监听 `:9001`。启动时已有证书则立刻听；`cluster init` / `agent join`（`RequestJoin`）成功写入证书后必须立刻启动，无需重启 Agent。
9. **加入者没有 `ca.key`**：必须新增 `control.LoadAgentCreds`，只读 `ca.crt` / `agent.crt` / `agent.key`。
10. **TLS 主机名**：Agent 证书只有 URI SAN，没有 DNS SAN。客户端必须用自定义 `VerifyPeerCertificate`（校验 CA + URI 集群匹配 + 可选期望 `node_id`），禁止依赖默认 hostname 校验。
11. **明确不做**：Raft、用户登录、RBAC 生效、CRL、node remove、Vue、关闭免认证环回、用 Gossip 传 Mutation。

## HTTP 头（本阶段锁定）

```text
Procmesh-Target-Node   CLI --node 或入口解析后的 Owner node_id
Procmesh-Source-Node   入口本机 node_id（转发时必填）
```

`:9001` 忽略 `Procmesh-Target-Node`（已在 Owner，`LocalOnly=true`）。

## File map（本阶段创建/修改）

```text
internal/agentcfg/load.go
internal/agentcfg/load_test.go
internal/control/pki.go                          # LoadAgentCreds
internal/control/pki_test.go
internal/rpc/tls.go
internal/rpc/tls_test.go
internal/rpc/header.go
internal/rpc/header_test.go
internal/rpc/client.go
internal/rpc/client_test.go
internal/rpc/server.go
internal/rpc/server_test.go
internal/rpc/errors.go
internal/api/route.go
internal/api/route_test.go
internal/api/process.go                          # 转发钩子
internal/api/process_test.go
internal/api/config.go
internal/api/config_test.go
internal/api/log.go
internal/api/log_test.go
internal/api/server.go                           # 注入 Router / Forwarder
internal/api/clusterapi.go                       # OnReady
internal/api/metrics.go
internal/api/metrics_test.go
internal/cli/root.go
internal/cli/root_test.go
internal/cli/client.go
internal/agent/run.go
internal/agent/summary.go                        # setRPC
internal/agent/cluster_accept_test.go            # 两节点验收
internal/agent/p3_accept_test.go                 # Case 2 / Case 6 / A→C restart
cmd/procmesh-agent/main.go                       # --rpc
docs/superpowers/plans/2026-08-13-v1-mvp.md
```

---

### Task 1: RPC 监听配置 + 加入者证书读取

**Files:**
- Modify: `internal/agentcfg/load.go`
- Modify: `internal/agentcfg/load_test.go`
- Modify: `internal/control/pki.go`
- Modify: `internal/control/pki_test.go`

**Interfaces:**
- Consumes: 已有 `agentcfg.LoadAll`、`control.WriteBundle` / `control.NewBundle`
- Produces:
  - `type RPC struct { Listen string; Advertise string }`
  - `Config.RPC RPC`
  - `func LoadAgentCreds(dir string) (AgentCreds, error)`
  - `type AgentCreds struct { CACertPEM, AgentCertPEM, AgentKeyPEM []byte }`

- [ ] **Step 1: 写失败测试（配置）**

在 `internal/agentcfg/load_test.go` 追加：

```go
func TestLoadAll_RPCListenAndAdvertise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	body := "rpc:\n  listen: 127.0.0.1:9001\n  advertise: 10.0.0.1:9001\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RPC.Listen != "127.0.0.1:9001" || cfg.RPC.Advertise != "10.0.0.1:9001" {
		t.Fatalf("%+v", cfg.RPC)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agentcfg -run TestLoadAll_RPCListenAndAdvertise -count=1`
Expected: FAIL `cfg.RPC` undefined

- [ ] **Step 3: 实现配置字段**

在 `agentcfg`：

```go
type Config struct {
	Disk   logmgr.Policy
	Gossip Gossip
	RPC    RPC
}

type RPC struct {
	Listen    string
	Advertise string
}

type file struct {
	Disk   *diskFile   `yaml:"disk"`
	Gossip *gossipFile `yaml:"gossip"`
	RPC    *rpcFile    `yaml:"rpc"`
}

type rpcFile struct {
	Listen    string `yaml:"listen"`
	Advertise string `yaml:"advertise"`
}
```

在 `LoadAll` 解析 `rpc` 段，赋给 `cfg.RPC`。缺省为空字符串（默认地址由 Agent 填 `127.0.0.1:9001`）。

- [ ] **Step 4: 跑配置测试确认通过**

Run: `go test ./internal/agentcfg -count=1`
Expected: PASS

- [ ] **Step 5: 写失败测试（加入者无 ca.key 也能读证书）**

在 `internal/control/pki_test.go` 追加：

```go
func TestLoadAgentCreds_NoCAKey(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	b, err := control.NewBundle("c", "n", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.WriteBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	creds, err := control.LoadAgentCreds(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.VerifyAgent(creds.CACertPEM, creds.AgentCertPEM, "c", "n", now); err != nil {
		t.Fatal(err)
	}
	if len(creds.AgentKeyPEM) == 0 {
		t.Fatal("missing agent key")
	}
}

func TestLoadAgentCreds_MissingCert(t *testing.T) {
	_, err := control.LoadAgentCreds(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 6: 跑测试确认失败**

Run: `go test ./internal/control -run TestLoadAgentCreds -count=1`
Expected: FAIL `LoadAgentCreds` undefined

- [ ] **Step 7: 实现 `LoadAgentCreds`**

```go
type AgentCreds struct {
	CACertPEM    []byte
	AgentCertPEM []byte
	AgentKeyPEM  []byte
}

func LoadAgentCreds(dir string) (AgentCreds, error) {
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return b, nil
	}
	ca, err := read(caCertFile)
	if err != nil {
		return AgentCreds{}, err
	}
	cert, err := read(agentCertFile)
	if err != nil {
		return AgentCreds{}, err
	}
	key, err := read(agentKeyFile)
	if err != nil {
		return AgentCreds{}, err
	}
	return AgentCreds{CACertPEM: ca, AgentCertPEM: cert, AgentKeyPEM: key}, nil
}
```

- [ ] **Step 8: 跑 control 测试确认通过**

Run: `go test ./internal/control ./internal/agentcfg -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/agentcfg/load.go internal/agentcfg/load_test.go internal/control/pki.go internal/control/pki_test.go
git commit -m "$(cat <<'EOF'
feat: 增加 RPC 监听配置与无 CA 私钥的证书读取

EOF
)"
```

---

### Task 2: mTLS TLS 配置与证书校验

**Files:**
- Create: `internal/rpc/tls.go`
- Create: `internal/rpc/tls_test.go`
- Create: `internal/rpc/header.go`
- Create: `internal/rpc/header_test.go`

**Interfaces:**
- Consumes: `control.AgentCreds`、`control.ParseIDs`、`control.NewBundle`
- Produces:
  - `const HeaderTargetNode = "Procmesh-Target-Node"`
  - `const HeaderSourceNode = "Procmesh-Source-Node"`
  - `func ServerTLS(creds control.AgentCreds, clusterID string) (*tls.Config, error)`
  - `func ClientTLS(creds control.AgentCreds, clusterID, expectNodeID string) (*tls.Config, error)`
  - `func PeerIdentity(state tls.ConnectionState) (clusterID, nodeID string, err error)`
  - `func TargetOf(h http.Header) string`
  - `func SourceOf(h http.Header) string`
  - `func SetTarget(h http.Header, nodeID string)`
  - `func SetSource(h http.Header, nodeID string)`

- [ ] **Step 1: 写失败测试（同 CA 握手身份）**

`internal/rpc/tls_test.go`：

```go
package rpc_test

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
)

func TestClientTLS_AcceptsSameCluster(t *testing.T) {
	now := time.Now()
	seed, err := control.NewBundle("cid", "seed", now)
	if err != nil {
		t.Fatal(err)
	}
	peer := signPeer(t, seed, "cid", "owner", now)
	cfg, err := rpc.ClientTLS(control.AgentCreds{
		CACertPEM:    seed.CACertPEM,
		AgentCertPEM: seed.AgentCertPEM,
		AgentKeyPEM:  seed.AgentKeyPEM,
	}, "cid", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLeaf(cfg, peer.AgentCertPEM); err != nil {
		t.Fatal(err)
	}
}

func TestClientTLS_RejectsOtherCluster(t *testing.T) {
	now := time.Now()
	a, err := control.NewBundle("cid-a", "a", now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := control.NewBundle("cid-b", "b", now)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := rpc.ClientTLS(control.AgentCreds{
		CACertPEM:    a.CACertPEM,
		AgentCertPEM: a.AgentCertPEM,
		AgentKeyPEM:  a.AgentKeyPEM,
	}, "cid-a", "")
	if err != nil {
		t.Fatal(err)
	}
	err = verifyLeaf(cfg, b.AgentCertPEM)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("got %v", err)
	}
}

func TestHeader_TargetAndSource(t *testing.T) {
	h := make(http.Header)
	rpc.SetTarget(h, "node-c")
	rpc.SetSource(h, "node-a")
	if rpc.TargetOf(h) != "node-c" || rpc.SourceOf(h) != "node-a" {
		t.Fatalf("%v", h)
	}
}

func signPeer(t *testing.T, ca control.Bundle, clusterID, nodeID string, now time.Time) control.Bundle {
	t.Helper()
	csr, key, err := control.NewCSR(clusterID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := control.SignCSR(ca.CACertPEM, ca.CAKeyPEM, csr, clusterID, nodeID, now)
	if err != nil {
		t.Fatal(err)
	}
	return control.Bundle{CACertPEM: ca.CACertPEM, AgentCertPEM: cert, AgentKeyPEM: key}
}

func verifyLeaf(cfg *tls.Config, leafPEM []byte) error {
	block, _ := pem.Decode(leafPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if cfg.VerifyPeerCertificate == nil {
		return fmt.Errorf("VerifyPeerCertificate required")
	}
	return cfg.VerifyPeerCertificate([][]byte{cert.Raw}, nil)
}
```

补上测试需要的 `net/http`、`encoding/pem`、`fmt` import。`TestHeader` 需要 `net/http`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/rpc -count=1`
Expected: FAIL 包不存在或符号未定义

- [ ] **Step 3: 实现 header 与 TLS**

`internal/rpc/header.go`：

```go
package rpc

import "net/http"

const (
	HeaderTargetNode = "Procmesh-Target-Node"
	HeaderSourceNode = "Procmesh-Source-Node"
)

func TargetOf(h http.Header) string { return h.Get(HeaderTargetNode) }
func SourceOf(h http.Header) string { return h.Get(HeaderSourceNode) }

func SetTarget(h http.Header, nodeID string) {
	if nodeID == "" {
		return
	}
	h.Set(HeaderTargetNode, nodeID)
}

func SetSource(h http.Header, nodeID string) {
	if nodeID == "" {
		return
	}
	h.Set(HeaderSourceNode, nodeID)
}
```

`internal/rpc/tls.go`：解析 PEM 为 `tls.Certificate` 与 `x509.CertPool`。`ServerTLS`：`ClientAuth = tls.RequireAndVerifyClientCert`，`ClientCAs` 为集群 CA，`VerifyPeerCertificate` 校验 URI 的 `clusterID`。`ClientTLS`：`RootCAs` 为集群 CA，`InsecureSkipVerify = true`（无 DNS SAN），`VerifyPeerCertificate` 校验 CA 签名、有效期、URI `clusterID`，若 `expectNodeID != ""` 再校验 `node_id`。失败一律 `errcode.E(errcode.DENIED, "...")`。

`PeerIdentity` 从 `ConnectionState.PeerCertificates[0]` 读 URI SAN。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/rpc -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/rpc
git commit -m "$(cat <<'EOF'
feat: 实现 Agent RPC 的 mTLS 配置与身份头

EOF
)"
```

---

### Task 3: `:9001` mTLS Connect 服务端

**Files:**
- Create: `internal/rpc/server.go`
- Create: `internal/rpc/server_test.go`

**Interfaces:**
- Consumes: `rpc.ServerTLS`、`procmeshv1connect` handlers、`control.AgentCreds`
- Produces:
  - `type Server struct { ... }`
  - `func NewServer(addr string, creds control.AgentCreds, clusterID string, h http.Handler) (*Server, error)`
  - `func (*Server) Serve(l net.Listener) error`
  - `func (*Server) Shutdown(ctx context.Context) error`
  - `func (*Server) Addr() string`

服务端用 `http.Server` + `tls.NewListener`。Handler 由调用方注入（Agent 挂 LocalOnly 的 Process/Config/Log Connect handler）。本任务测试用一个返回 200 的假 handler 验证握手，不必挂真实 ProcessAPI。

- [ ] **Step 1: 写失败测试**

```go
func TestServer_RequiresClientCert(t *testing.T) {
	seed := newSeed(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := rpc.NewServer(ln.Addr().String(), credsOf(seed), "cid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// no client cert
	plain := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	_, err = plain.Get("https://" + ln.Addr().String() + "/")
	if err == nil {
		t.Fatal("expected handshake failure without client cert")
	}

	okClient := &http.Client{Transport: &http.Transport{TLSClientConfig: mustClientTLS(t, seed, "cid", "")}}
	resp, err := okClient.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestServer_RejectsForeignCA(t *testing.T) {
	seed := newSeed(t)
	other, err := control.NewBundle("other", "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := rpc.NewServer(ln.Addr().String(), credsOf(seed), "cid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	bad := &http.Client{Transport: &http.Transport{TLSClientConfig: mustClientTLS(t, other, "other", "")}}
	_, err = bad.Get("https://" + ln.Addr().String() + "/")
	if err == nil {
		t.Fatal("expected foreign CA to fail")
	}
}
```

`newSeed` 用 `control.NewBundle("cid", "seed", time.Now())`。`credsOf` 映射到 `control.AgentCreds`。`mustClientTLS` 调 `rpc.ClientTLS`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/rpc -run TestServer -count=1`
Expected: FAIL `NewServer` undefined

- [ ] **Step 3: 实现 `rpc.Server`**

`NewServer` 调 `ServerTLS`，构造：

```go
type Server struct {
	http *http.Server
	addr string
}

func NewServer(addr string, creds control.AgentCreds, clusterID string, h http.Handler) (*Server, error) {
	tlsCfg, err := ServerTLS(creds, clusterID)
	if err != nil {
		return nil, err
	}
	s := &http.Server{Addr: addr, Handler: h, TLSConfig: tlsCfg}
	return &Server{http: s, addr: addr}, nil
}

func (s *Server) Serve(l net.Listener) error {
	tlsLn := tls.NewListener(l, s.http.TLSConfig)
	s.addr = tlsLn.Addr().String()
	return s.http.Serve(tlsLn)
}
```

`Shutdown` 调 `http.Server.Shutdown`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/rpc -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/rpc
git commit -m "$(cat <<'EOF'
feat: 添加 Agent 间 mTLS Connect 服务端

EOF
)"
```

---

### Task 4: mTLS 客户端与错误映射

**Files:**
- Create: `internal/rpc/errors.go`
- Create: `internal/rpc/client.go`
- Create: `internal/rpc/client_test.go`

**Interfaces:**
- Consumes: `rpc.ClientTLS`、`procmeshv1connect.NewProcessServiceClient` 等
- Produces:
  - `func MapDialError(err error) error` — 超时/`context.DeadlineExceeded`/`os.ErrDeadlineExceeded` → `TIMEOUT`（message `rpc timed out`）；其余拨号/连接失败 → `UNAVAILABLE`（message `owner unreachable`）
  - `func MapCallError(err error) error` — 已是 `*errcode.Error` 或带 `ErrorInfo` 的 Connect 错误原样返回（保留 code）；`DeadlineExceeded` → `TIMEOUT`；`Unavailable`/`Unknown` 且无 ErrorInfo → `UNAVAILABLE`
  - `type DialConfig struct { Creds control.AgentCreds; ClusterID, ExpectNodeID, Address string; Timeout time.Duration }`
  - `func Dial(cfg DialConfig) (*http.Client, string, error)` — Address 允许 `host:port` 或已带 `https://`；返回的 base URL 必须是 `https://host:port`；Timeout 默认 5s
  - `func NewProcessClient(hc *http.Client, base string) procmeshv1connect.ProcessServiceClient`
  - `func NewConfigClient(hc *http.Client, base string) procmeshv1connect.ConfigServiceClient`
  - `func NewLogClient(hc *http.Client, base string) procmeshv1connect.LogServiceClient`

- [ ] **Step 1: 写失败测试**

```go
func TestMapDialError_Timeout(t *testing.T) {
	err := rpc.MapDialError(context.DeadlineExceeded)
	if !errcode.Is(err, errcode.TIMEOUT) {
		t.Fatalf("%v", err)
	}
}

func TestMapDialError_Refused(t *testing.T) {
	err := rpc.MapDialError(errors.New("connection refused"))
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("%v", err)
	}
}

func TestDial_CallsOwnerProcess(t *testing.T) {
	seed := newSeed(t)
	owner := signPeer(t, seed, "cid", "owner", time.Now())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	path, h := procmeshv1connect.NewProcessServiceHandler(&stubProcess{})
	mux.Handle(path, h)
	srv, err := rpc.NewServer(ln.Addr().String(), credsOf(owner), "cid", mux)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	hc, base, err := rpc.Dial(rpc.DialConfig{
		Creds:        credsOf(seed),
		ClusterID:    "cid",
		ExpectNodeID: "owner",
		Address:      ln.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cli := rpc.NewProcessClient(hc, base)
	_, err = cli.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
}

type stubProcess struct {
	procmeshv1connect.UnimplementedProcessServiceHandler
}

func (stubProcess) ListProcesses(context.Context, *connect.Request[procmeshv1.ListProcessesRequest]) (*connect.Response[procmeshv1.ListProcessesResponse], error) {
	return connect.NewResponse(&procmeshv1.ListProcessesResponse{}), nil
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/rpc -run 'TestMapDialError|TestDial' -count=1`
Expected: FAIL 符号未定义

- [ ] **Step 3: 实现 client 与错误映射**

`MapDialError` / `MapCallError` 用 `errors.Is` 识别 deadline，其余连接类错误变 `UNAVAILABLE`。`Dial` 建 `http.Client{Timeout, Transport: &http.Transport{TLSClientConfig: ClientTLS(...)}}`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/rpc -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/rpc
git commit -m "$(cat <<'EOF'
feat: 添加 mTLS RPC 客户端与超时映射

EOF
)"
```

---

### Task 5: Owner 路由解析

**Files:**
- Create: `internal/api/route.go`
- Create: `internal/api/route_test.go`

**Interfaces:**
- Consumes: `cluster.NodeSummary` / `cluster.State*`、`rpc.TargetOf`
- Produces:

```go
type Route struct {
	Local  bool
	NodeID string
	RPC    string
}

type Membership interface {
	Members() []cluster.NodeSummary
}

type Router struct {
	LocalID       string
	LocalHost     string
	Members       func() []cluster.NodeSummary
	LocalHasName  func(ctx context.Context, idOrName string) bool
}

func (r Router) Resolve(ctx context.Context, targetHint, idOrName, ownerAgentID string) (Route, error)
```

解析顺序（锁定）：

1. `targetHint`（来自 `Procmesh-Target-Node`）非空：按 `node_id` 精确匹配，否则按 `hostname` 唯一匹配。
2. 否则 `ownerAgentID` 非空：按 `node_id` 匹配。
3. 否则 `idOrName` 非空且 `LocalHasName(ctx, idOrName)==true` → 本地。
4. 否则在 Gossip `Processes[].Name` 里找 `idOrName`：命中一个 Owner → 用该节点；命中多个 → `INVALID`（`ambiguous process owner`）；零命中且无 hint → 本地（创建落在本机）。
5. 目标等于 `LocalID` 或本机 `LocalHost` → `Route{Local:true, NodeID: LocalID}`。
6. 远端节点 `State==FAILED` 或 `RPCAddress==""` → `UNAVAILABLE`（`owner unreachable`）。
7. 远端节点 `ProtocolVersion != version.Protocol` → `INCOMPATIBLE_VERSION`。
8. 给了 `targetHint` 但 membership 里找不到 → `UNAVAILABLE`（`owner not found`）。

本任务 **不** 发 RPC，只解析。

- [ ] **Step 1: 写失败测试**

```go
func TestRouter_TargetHeaderWins(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{
				{NodeID: "aaa", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9001", ProtocolVersion: version.Protocol},
				{NodeID: "ccc", Hostname: "host-c", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9003", ProtocolVersion: version.Protocol},
			}
		},
	}
	got, err := r.Resolve(context.Background(), "ccc", "nginx", "aaa")
	if err != nil {
		t.Fatal(err)
	}
	if got.Local || got.NodeID != "ccc" || got.RPC != "127.0.0.1:9003" {
		t.Fatalf("%+v", got)
	}
}

func TestRouter_FailedOwnerUnavailable(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{
				NodeID: "ccc", State: cluster.StateFailed, RPCAddress: "127.0.0.1:9003",
				ProtocolVersion: version.Protocol,
				Processes:       []cluster.ProcessSummary{{Name: "nginx"}},
			}}
		},
		LocalHasName: func(context.Context, string) bool { return false },
	}
	_, err := r.Resolve(context.Background(), "ccc", "nginx", "")
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("%v", err)
	}
}

func TestRouter_MissingHintIsLocalCreate(t *testing.T) {
	r := Router{
		LocalID:      "aaa",
		Members:      func() []cluster.NodeSummary { return nil },
		LocalHasName: func(context.Context, string) bool { return false },
	}
	got, err := r.Resolve(context.Background(), "", "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Local || got.NodeID != "aaa" {
		t.Fatalf("%+v", got)
	}
}

func TestRouter_GossipNameFindsOwner(t *testing.T) {
	r := Router{
		LocalID: "aaa",
		Members: func() []cluster.NodeSummary {
			return []cluster.NodeSummary{{
				NodeID: "ccc", State: cluster.StateAlive, RPCAddress: "127.0.0.1:9",
				ProtocolVersion: version.Protocol,
				Processes:       []cluster.ProcessSummary{{Name: "nginx"}},
			}}
		},
		LocalHasName: func(context.Context, string) bool { return false },
	}
	got, err := r.Resolve(context.Background(), "", "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Local || got.NodeID != "ccc" {
		t.Fatalf("%+v", got)
	}
}
```

再补：hostname 匹配、`INCOMPATIBLE_VERSION`、模糊 name、本机 hostname 视为 Local。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestRouter -count=1`
Expected: FAIL `Router` undefined

- [ ] **Step 3: 实现 `Router.Resolve`**

严格按上面 8 条。不要在这里调用 Manager 或发 RPC。`LocalHasName` 为 nil 时视为 false。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api -run TestRouter -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/route.go internal/api/route_test.go
git commit -m "$(cat <<'EOF'
feat: 按 --node、owner 与 Gossip 解析 Write-to-Owner 路由

EOF
)"
```

---

### Task 6: Process / Config / Log Write-to-Owner 转发

**Files:**
- Modify: `internal/api/process.go`
- Modify: `internal/api/process_test.go`
- Modify: `internal/api/config.go`
- Modify: `internal/api/config_test.go`
- Modify: `internal/api/log.go`
- Modify: `internal/api/log_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`（若构造 Options 需要新字段）

**Interfaces:**
- Consumes: `Router.Resolve`、`rpc.TargetOf` / `rpc.SetTarget` / `rpc.SetSource`、`rpc.Dial`、`rpc.MapCallError`
- Produces:

```go
type Forwarder interface {
	Process(ctx context.Context, rt Route) (procmeshv1connect.ProcessServiceClient, error)
	Config(ctx context.Context, rt Route) (procmeshv1connect.ConfigServiceClient, error)
	Log(ctx context.Context, rt Route) (procmeshv1connect.LogServiceClient, error)
}

type ProcessAPI struct {
	Mgr       *process.Manager
	Degraded  func() bool
	LocalOnly bool
	LocalID   string
	Router    *Router
	Forward   Forwarder
}

// ConfigAPI / LogAPI 同样增加 LocalOnly / LocalID / Router / Forward
```

行为（锁定）：

- `LocalOnly==true`（`:9001`）：忽略 target 头，只走本机；缺 `operation_id` 仍 `INVALID`。
- `:9000` 上每个 Process/Config Mutation 以及 Get/List/History/Diff/Tail/Stream/Download：先 `hint := rpc.TargetOf(req.Header())`，再 `Resolve`。
- `Route.Local==true`：现有本机逻辑。Apply 时若 `spec.OwnerAgentID==""`，写成 `LocalID`（LocalID 空则保持空，兼容单测）。
- `Route.Local==false`：**禁止**调用 `s.Mgr.*`。用 Forwarder 拿到客户端，把同一请求（含原 `operation_id`）转发，并设置 `Procmesh-Source-Node=LocalID`、`Procmesh-Target-Node=rt.NodeID`。错误走 `MapCallError` 再 `ToConnect`。
- 入口 **不得** `BeginOperation` / `PeekOp` 转发请求——幂等只发生在 Owner。
- `Forward==nil` 且需要转发 → `UNAVAILABLE`（`owner unreachable`）。

测试用假 Forwarder（记录调用、返回固定 view / 错误），不要真 mTLS。保留现有本机单测全部通过。

- [ ] **Step 1: 写失败测试（不在本机执行远程 restart）**

在 `internal/api/process_test.go` 追加（按现有测试的 server 构造方式接 `Router`+假 Forwarder）：

```go
func TestProcess_RestartForwardsToOwner(t *testing.T) {
	// 本机 Manager 为空或没有 nginx。
	// Router 解析到远端 ccc。
	// Forwarder.Process 被调用一次，RestartProcess 被调用且 operation_id 原样传递。
	// 本机 ListProcesses 仍为空。
}

func TestProcess_ApplyDoesNotCreateLocalWhenOwnerRemote(t *testing.T) {
	// target=ccc 或 spec.owner_agent_id=ccc
	// Forwarder.Process.ApplyProcess 被调用
	// 本机 Manager 无该 process
}

func TestProcess_LocalOnlyIgnoresTargetHeader(t *testing.T) {
	// LocalOnly=true，本机有进程
	// 即使 Header 指向远端，也走本机 Restart
}
```

Config 的 Update/Rollback、Log 的 Tail 各写一个「远端则转发、本机无副作用」测试。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run 'TestProcess_RestartForwards|TestProcess_ApplyDoesNotCreate|TestProcess_LocalOnly' -count=1`
Expected: FAIL（字段或行为不存在）

- [ ] **Step 3: 实现转发钩子**

在 `ProcessAPI`/`ConfigAPI`/`LogAPI` 每个导出 RPC 开头调用统一的 `hop`：

```go
func (s *ProcessAPI) hop(ctx context.Context, header http.Header, idOrName, ownerAgentID string) (local bool, rt Route, err error) {
	if s.LocalOnly || s.Router == nil {
		return true, Route{Local: true, NodeID: s.LocalID}, nil
	}
	rt, err = s.Router.Resolve(ctx, rpc.TargetOf(header), idOrName, ownerAgentID)
	if err != nil {
		return false, Route{}, err
	}
	return rt.Local, rt, nil
}
```

转发时复制 header，设置 source/target，再调对应客户端方法，返回其 response。

- [ ] **Step 4: 跑 api 包测试确认通过**

Run: `go test ./internal/api -count=1`
Expected: PASS（含全部旧测试）

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "$(cat <<'EOF'
feat: 入口 Agent 将 Process/Config/Log 转发到 Owner

EOF
)"
```

---

### Task 7: CLI `--node`

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`
- Modify: `internal/cli/client.go`
- Modify: `internal/cli/process.go`（若 client 构造签名变化）

**Interfaces:**
- Consumes: `rpc.HeaderTargetNode`
- Produces: `--node` 不再拒绝；所有 CLI Connect 请求自动带 `Procmesh-Target-Node`（非空时）

实现：在 `newClient` 增加 `node string`，用 `connect.WithInterceptors` 的 unary+streaming interceptor 给每个请求 `req.Header().Set(rpc.HeaderTargetNode, node)`。`usageText` 改为 `--node NODE              target owner node_id or hostname`。删除 `Main` 里对 `--node` 的提前拒绝。

- [ ] **Step 1: 改失败测试**

把 `TestCLI_NodeRejected` 换成：

```go
func TestCLI_NodeHeaderSent(t *testing.T) {
	var got string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Procmesh-Target-Node")
		http.Error(w, "no", 404)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	code, _, _ := runCLI("--server", srv.URL, "--node", "node-c", "process", "list")
	if code == 2 {
		t.Fatalf("P3 must accept --node, exit=%d", code)
	}
	if got != "node-c" {
		t.Fatalf("header=%q", got)
	}
}
```

（若 Connect 客户端 404 导致 exit=1，只要 header 被写上即可。）

保留一个「空 `--node` 不设头」断言可附在同测试或另测。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli -run TestCLI_Node -count=1`
Expected: FAIL（仍打印 P3 拒绝或头为空）

- [ ] **Step 3: 实现 interceptor，删拒绝逻辑**

- [ ] **Step 4: 跑 CLI 测试确认通过**

Run: `go test ./internal/cli -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "$(cat <<'EOF'
feat: CLI --node 通过请求头指定 Owner

EOF
)"
```

---

### Task 8: Agent 接线——证书就绪后启动 `:9001`

**Files:**
- Modify: `internal/agent/run.go`
- Modify: `internal/agent/summary.go`
- Modify: `internal/api/clusterapi.go`
- Modify: `internal/api/clusterapi_test.go`（Init/Join 后调用 OnReady）
- Modify: `cmd/procmesh-agent/main.go`
- Modify: `internal/agent/cluster_accept_test.go`（`RPCListen: "127.0.0.1:0"`）

**Interfaces:**
- Consumes: `control.LoadAgentCreds`、`rpc.NewServer`、`rpc.Dial`、`api.Router`、`agentcfg.RPC`
- Produces:
  - `agent.Options.RPCListen string`（默认 `127.0.0.1:9001`；测试 `127.0.0.1:0`）
  - `agent.Options.RPCAdvertise string`
  - `agent.Options.OnRPCListen func(addr string)`（可空）
  - `api.ClusterDeps.OnReady func() error`（可空；`Init` 与 `RequestJoin` 成功写完证书后调用，失败只打 stderr，不回滚已成功的 init/join）
  - `liveSource.setRPC(addr string)`
  - 具体 Forwarder：用本机 `AgentCreds` Dial 到 `rt.RPC`
  - `:9000` 的 Process/Config/Log API：`LocalOnly=false`，注入 Router+Forwarder
  - `:9001` 的同一套 handler：**新实例** `LocalOnly=true`（不要共享会变的字段）

行为（锁定）：

- `RPCListen` 空：CLI flag 空则读 `cfg.RPC.Listen`，再空则 `127.0.0.1:9001`。
- 非环回同样走 `CheckListen`。
- 无证书：不听 `:9001`，`RPCAddress` 保持空。
- 有证书：`net.Listen` → `rpc.NewServer` → `src.setRPC(addr)` → `mesh.Update()` → `OnRPCListen`。
- `Init`/`RequestJoin` 之后 `OnReady` 再走同一条启动路径（已在听则 no-op）。
- 关闭 Agent 时 `Shutdown` RPC server。
- Gossip `rpc_address` 必须是实际绑定地址（含 `:0` 解析后的端口）。
- 本机 Apply 写入 `owner_agent_id=nodeID`（经由 ProcessAPI.LocalID）。

- [ ] **Step 1: 写失败测试（Init 后 node list 出现 rpc_address）**

在 `internal/agent/cluster_accept_test.go` 或新文件 `internal/agent/p3_rpc_test.go`：

```go
func TestAccept_RPCAddressAfterInit(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		code, out, errb = runP1CLI("--server", addr, "node", "list")
		if code == 0 && strings.Contains(out, "127.0.0.1:") && strings.Count(out, "127.0.0.1:") >= 1 {
			// node list 已打印 rpc 或至少成员仍在；改为解析 rpc 列/字段
			if rpcAddrFromNodeList(out) != "" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("missing rpc_address after init: %q stderr=%q", out, errb)
}
```

先看现有 `node list` 输出格式；若没有 rpc 列，在本任务把 `internal/cli/node.go` 的 list 行加上 `rpc_address` 字段（tab 分隔，加在已有列后），并更新对应 CLI 单测的列断言（若有写死列数）。

`startClusterAgentAt` 增加 `RPCListen: "127.0.0.1:0"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent -run TestAccept_RPCAddressAfterInit -count=1`
Expected: FAIL（无 rpc_address 或 RPC 未启动）

- [ ] **Step 3: 接线实现**

`serveHTTP` 同时持有 RPC server。抽取 `startRPCLocked()`：LoadAgentCreds → listen → NewServer（LocalOnly handlers）→ setRPC。

实现 `agentForwarder`：

```go
func (f *agentForwarder) Process(ctx context.Context, rt api.Route) (procmeshv1connect.ProcessServiceClient, error) {
	hc, base, err := rpc.Dial(rpc.DialConfig{
		Creds: f.creds, ClusterID: f.clusterID, ExpectNodeID: rt.NodeID, Address: rt.RPC,
	})
	if err != nil {
		return nil, rpc.MapDialError(err)
	}
	return rpc.NewProcessClient(hc, base), nil
}
```

`clusterID` 从 store / creds URI 读取。

`cmd/procmesh-agent/main.go` 增加 `--rpc` flag，传入 `Options.RPCListen`。

- [ ] **Step 4: 跑 agent/api/cli 测试确认通过**

Run: `go test ./internal/agent ./internal/api ./internal/cli ./internal/control ./internal/rpc -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent internal/api internal/cli cmd/procmesh-agent
git commit -m "$(cat <<'EOF'
feat: 证书就绪后启动 :9001 并广播 rpc_address

EOF
)"
```

---

### Task 9: 验收 — A 重启 C、Case 6 幂等、Case 2 不迁移

**Files:**
- Create: `internal/agent/p3_accept_test.go`

**Interfaces:**
- Consumes: 已接线的两节点 Agent、CLI `--node`、本机 journal
- Produces: 三个验收测试，全部必须在 macOS 上可跑（不用 cgroup）

辅助：复用 `startClusterAgent` / `runP1CLI` / join 流程（与 `TestAccept_JoinTwoAgents` 相同）。进程 spec 用 `/bin/sleep 60`。

- [ ] **Step 1: 写失败测试（若接线未完成会红；接线完成后这些是新验收，先红再绿）**

```go
func TestP3_RestartOnOwnerFromEntry(t *testing.T) {
	addrA, rootA := startClusterAgent(t, "")
	addrC, rootC := startClusterAgent(t, "")
	idC := readNodeID(t, rootC)
	joinTwo(t, addrA, addrC)

	spec := writeSleepSpec(t)
	code, out, errb := runP1CLI("--server", addrC, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply on C: %d %q %q", code, errb, out)
	}
	code, _, errb = runP1CLI("--server", addrC, "process", "start", "sleep")
	if code != 0 {
		t.Fatalf("start on C: %q", errb)
	}
	waitObserved(t, addrC, "sleep", "RUNNING")

	// Gossip 把 C 的 process 摘要传到 A
	waitGossipName(t, addrA, "sleep")

	code, _, errb = runP1CLI("--server", addrA, "--node", idC, "process", "restart", "sleep")
	if code != 0 {
		t.Fatalf("restart via A: %q", errb)
	}

	// A 本机不得出现 sleep
	code, listA, errb := runP1CLI("--server", addrA, "process", "list")
	if code != 0 {
		t.Fatal(errb)
	}
	if strings.Contains(listA, "sleep") {
		t.Fatalf("entry must not own sleep: %q", listA)
	}

	code, listC, errb := runP1CLI("--server", addrC, "process", "list")
	if code != 0 {
		t.Fatal(errb)
	}
	if !strings.Contains(listC, "sleep") {
		t.Fatalf("owner lost sleep: %q", listC)
	}
}

func TestP3_SameOperationIDDoesNotReplay(t *testing.T) {
	addrA, _ := startClusterAgent(t, "")
	addrC, rootC := startClusterAgent(t, "")
	idC := readNodeID(t, rootC)
	joinTwo(t, addrA, addrC)
	spec := writeSleepSpec(t)
	if code, _, errb := runP1CLI("--server", addrC, "process", "apply", "--file", spec, "--expected-revision", "0"); code != 0 {
		t.Fatal(errb)
	}
	if code, _, errb := runP1CLI("--server", addrC, "process", "start", "sleep"); code != 0 {
		t.Fatal(errb)
	}
	waitObserved(t, addrC, "sleep", "RUNNING")
	waitGossipName(t, addrA, "sleep")

	op := "op-p3-restart-once"
	if code, _, errb := runP1CLI("--server", addrA, "--node", idC, "--operation-id", op, "process", "restart", "sleep"); code != 0 {
		t.Fatal(errb)
	}
	// 读 C 上 restart_count（process get 输出或 list）；再重放同一 operation_id
	first := restartCount(t, addrC, "sleep")
	if code, _, errb := runP1CLI("--server", addrA, "--node", idC, "--operation-id", op, "restart", "sleep"); code != 0 {
		t.Fatal(errb)
	}
	second := restartCount(t, addrC, "sleep")
	if second != first {
		t.Fatalf("replayed restart: first=%d second=%d", first, second)
	}
}

func TestP3_FailedOwnerDoesNotMigrate(t *testing.T) {
	addrA, _ := startClusterAgent(t, "")
	addrC, rootC := startClusterAgent(t, "")
	idC := readNodeID(t, rootC)
	cancelC := cancelFnFor(t, rootC) // 见下：startClusterAgent 需能返回 cancel，或本测试自建 C
	joinTwo(t, addrA, addrC)
	spec := writeSleepSpec(t)
	if code, _, errb := runP1CLI("--server", addrC, "process", "apply", "--file", spec, "--expected-revision", "0"); code != 0 {
		t.Fatal(errb)
	}
	waitGossipName(t, addrA, "sleep")
	cancelC() // C 下线，A 侧应变 FAILED 或至少 RPC 不可达

	code, _, errb := runP1CLI("--server", addrA, "--node", idC, "process", "restart", "sleep")
	if code == 0 {
		t.Fatal("restart must fail when owner is down")
	}
	if !strings.Contains(errb, "UNAVAILABLE") && !strings.Contains(errb, "TIMEOUT") {
		t.Fatalf("want UNAVAILABLE or TIMEOUT, got %q", errb)
	}
	code, listA, errb := runP1CLI("--server", addrA, "process", "list")
	if code != 0 {
		t.Fatal(errb)
	}
	if strings.Contains(listA, "sleep") {
		t.Fatalf("must not migrate process to entry: %q", listA)
	}
}
```

`joinTwo` 抽取 `TestAccept_JoinTwoAgents` 的 init+token+join+等待双方 node list 含两个 id。

`restartCount`：若 `process get` / `list` 没有 restart_count 列，用 Connect 在测试里直连 C 的 `GetProcess` 读 `instances[0].restart_count`，或给 `process get` 加一行 `restart_count\tN`（只加字段，不改其它输出）。**优先直连 GetProcess**，避免无必要改 CLI 格式。

`TestP3_FailedOwnerDoesNotMigrate` 需要能停掉 C：给 `startClusterAgentAt` 同文件增加 `startClusterAgentCtl` 返回 `addr, root, cancel`，或在本测试内复制启动逻辑并持有 cancel。不要停 A。

- [ ] **Step 2: 跑测试确认失败（若 Task 8 已完成，应是缺测试辅助/行为缺口）**

Run: `go test ./internal/agent -run 'TestP3_' -count=1`
Expected: 先红（测试文件存在但辅助或行为未齐）

- [ ] **Step 3: 补齐辅助函数与任何使三案通过的最小缺口（禁止削弱断言）**

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/agent -run 'TestP3_|TestAccept_' -count=1`
Expected: PASS

再跑：`go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent
git commit -m "$(cat <<'EOF'
test: 验收从入口重启 Owner 进程、幂等与禁止迁移

EOF
)"
```

---

### Task 10: RPC 指标与 V1 索引

**Files:**
- Modify: `internal/api/metrics.go`
- Modify: `internal/api/metrics_test.go`（若无则创建）
- Modify: `docs/superpowers/plans/2026-08-13-v1-mvp.md`

**Interfaces:**
- Consumes: 已有 `/metrics` 文本格式
- Produces: 在现有 Prometheus 文本中追加：

```text
# HELP procmesh_rpc_forward_total Remote owner RPC forward attempts.
# TYPE procmesh_rpc_forward_total counter
procmesh_rpc_forward_total <n>
```

用 `atomic.Uint64` 挂在 `api.Options` 或包级 `ForwardCount` 由 Forwarder 包装递增。单测：调一次假转发后 `/metrics` 含 `procmesh_rpc_forward_total 1`。

更新 `docs/superpowers/plans/2026-08-13-v1-mvp.md` 中 P3 行：

```markdown
| P3 | [2026-08-15-p3-mtls-write-to-owner.md](./2026-08-15-p3-mtls-write-to-owner.md) | 从 A 重启 C 上的进程 |
```

- [ ] **Step 1: 写失败测试（metrics 含 forward counter）**

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api -run TestMetrics_ForwardTotal -count=1`
Expected: FAIL

- [ ] **Step 3: 实现计数器与索引更新**

- [ ] **Step 4: 全量测试**

Run: `go test ./... -count=1`
Expected: PASS

覆盖率抽查（不得回退）：

```bash
go test ./internal/process ./internal/shim ./internal/store ./internal/control -cover
```

Expected: 四包 ≥ 80%

- [ ] **Step 5: Commit**

```bash
git add internal/api/metrics.go internal/api/metrics_test.go docs/superpowers/plans/2026-08-13-v1-mvp.md
git commit -m "$(cat <<'EOF'
feat: 暴露 RPC 转发指标并登记 P3 计划

EOF
)"
```

---

## 自检

1. **规格覆盖：** §3.4/3.8/3.9 Direct RPC + timeout + operation_id；§4.2 `internal/rpc`；§5.1 `:9001`；§9.5 Write-to-Owner；§11.2 `--node`；§12 分区不迁移；§13 P3 演示；§14 Case 2 / Case 6；§16 mTLS；§18 禁止非 Owner 写 / 无 operation_id 远程写。P4 项（Raft/RBAC/CRL/关环回）明确不做。
2. **无占位符：** 任务均含测试代码、命令、期望、提交说明。
3. **类型一致：** `LoadAgentCreds` / `AgentCreds` / `Router.Resolve` / `Forwarder` / `DialConfig` / 头常量在后续任务中名称一致。
