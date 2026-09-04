# ProcMesh

ProcMesh 是一个 Local-First、Agent-Owned、Peer-Managed 的分布式进程管理平台。它在不部署独立中心管理服务器的前提下，为服务器集群提供进程生命周期管理、Web 管理界面、命令行、远程操作和可观测性能力。

每个节点运行一个 ProcMesh Agent。业务进程的配置、状态和日志由所在节点持有权威数据；集群成员与进程摘要通过 Gossip 汇聚，用户、RBAC 与节点准入等控制数据通过内嵌 Raft 保持强一致。

## 特性

- 本地进程生命周期管理：启动、停止、重启、强制终止、健康检查和重启策略。
- Shim 保护：Agent 重启或异常退出时，已托管的业务进程可继续运行并在 Agent 恢复后重新接管。
- 无中心管理节点：任意健康 Agent 都可提供 Web UI、API 和 CLI 入口。
- 跨节点管理：通过 mTLS RPC 将写操作路由到进程所属的 Owner Agent。
- 配置版本控制：使用乐观锁更新配置，支持历史查看与回滚。
- 集群控制：节点发现、加入令牌、Raft 成员管理、用户、角色与 RBAC。
- 运行可观测性：进程日志、节点与进程资源指标、告警、磁盘保护和审计。
- 数据保护：支持本地文件系统、S3 和节点间备份及恢复。
- 内嵌 Vue Web UI，支持中文和英文界面。

## 快速开始

以下步骤用于在单个节点上安装并体验 ProcMesh。Linux 是生产目标，支持 `amd64`、`arm64` 和 `armv7l`；macOS Release 仅用于开发和评估。生产集群的网络、systemd 和多节点配置请继续阅读[部署与集群快速开始](docs/QUICKSTART_ZH.md)。

### 1. 安装 ProcMesh

选择下面任意一种方式。两种方式安装的 Release 都包含 `procmesh`、`procmesh-agent` 和 `procmesh-shim`。

#### 自动安装（Linux，推荐）

安装器会下载最新正式 Release，并使用 Release 提供的 SHA-256 校验值验证压缩包：

```bash
curl -fsSL https://raw.githubusercontent.com/xiaoyannzbbb/procmesh/main/scripts/install.sh | bash
```

安装过程需要交互式终端。建议保留默认安装目录 `/usr/local/bin`；如果希望 Agent 作为系统服务运行，请在提示时选择安装 systemd unit，并选择立即启用和启动服务。已有配置、数据目录和 systemd unit 不会被覆盖。

默认监听地址是 `127.0.0.1:18680`。选择非回环地址时，安装器会加入 `--insecure-listen`，但不会启用 HTTPS；必须通过防火墙、HTTPS 反向代理、VPN 或堡垒机限制访问。

#### 从 GitHub Release 下载

打开 [GitHub Releases](https://github.com/xiaoyannzbbb/procmesh/releases/latest)，下载与操作系统及 CPU 架构匹配的压缩包，同时下载 `checksums.txt`：

```text
procmesh_<version>_linux_<amd64|arm64|armv7>.tar.gz
procmesh_<version>_darwin_<amd64|arm64>.tar.gz
```

在 Linux 上校验并安装三个二进制程序。将 `VERSION` 和 `ARCH` 改为实际下载版本和架构，其中版本号不含开头的 `v`：

```bash
VERSION='X.Y.Z'
ARCH='amd64'
ARCHIVE="procmesh_${VERSION}_linux_${ARCH}.tar.gz"
awk -v file="$ARCHIVE" '$2 == file { print }' checksums.txt | sha256sum -c -
tar -xzf "$ARCHIVE"
PACKAGE_DIR="${ARCHIVE%.tar.gz}"
sudo install -m 0755 \
  "$PACKAGE_DIR/procmesh" \
  "$PACKAGE_DIR/procmesh-agent" \
  "$PACKAGE_DIR/procmesh-shim" \
  /usr/local/bin/
```

macOS 可使用 `shasum -a 256 -c -` 替代 `sha256sum -c -`。Linux 压缩包还包含默认 `agent.yaml` 和 systemd unit；生产部署方式参见[部署与集群快速开始](docs/QUICKSTART_ZH.md)。

### 2. 启动并检查 Agent

如果自动安装时已经启用 systemd 服务，检查服务和 HTTP 端点：

```bash
sudo systemctl status procmesh-agent --no-pager
curl -fsS http://127.0.0.1:18680/healthz
curl -fsS http://127.0.0.1:18680/readyz
```

如果只安装了二进制程序，可在前台启动一个用于体验的单节点 Agent：

```bash
mkdir -p /tmp/procmesh-quickstart
procmesh-agent \
  --data-dir /tmp/procmesh-quickstart \
  --listen 127.0.0.1:18680 \
  --rpc 127.0.0.1:18683 \
  --control 127.0.0.1:18685 \
  --gossip 127.0.0.1:18689 \
  --shim-bin "$(command -v procmesh-shim)"
```

保持该终端运行，在另一个终端确认 Agent 已就绪：

```bash
curl -fsS http://127.0.0.1:18680/healthz
curl -fsS http://127.0.0.1:18680/readyz
procmesh --server 127.0.0.1:18680 status
```

### 3. 初始化并登录

初始化命令只能成功执行一次，并会输出一次性管理员密码：

```bash
procmesh --server 127.0.0.1:18680 cluster init --admin-user admin
```

立即将密码保存到密码管理器，然后登录：

```bash
procmesh --server 127.0.0.1:18680 login --user admin
```

访问 <http://127.0.0.1:18680/> 可打开 Web UI。浏览器不会复用 CLI 会话，需要再次使用管理员账户和密码登录。

### 4. 创建并启动第一个进程

创建 `demo-worker.yaml`：

```yaml
name: demo-worker
command: /bin/sh
args:
  - -c
  - while true; do date; sleep 5; done
working_directory: /tmp
instances: 1
autostart: true

restart:
  mode: always
  max_retries: 10
  retry_window_ms: 60000

health:
  type: alive
  interval_ms: 5000

log:
  max_size: 10485760
  max_files: 5
  compress: true
```

应用配置、启动进程并查看日志：

```bash
procmesh --server 127.0.0.1:18680 process apply \
  --file demo-worker.yaml \
  --expected-revision 0 \
  --comment 'initial deployment'

procmesh --server 127.0.0.1:18680 process start demo-worker
procmesh --server 127.0.0.1:18680 process list
procmesh --server 127.0.0.1:18680 process logs demo-worker --lines 20 --stream stdout
```

`autostart` 表示主机或 Agent 恢复后的运行意图，不会在首次创建配置时自动启动进程，因此首次部署仍需执行 `process start`。

## 架构

```text
                       Web UI / CLI / ConnectRPC
                                  |
                             任意 Agent :18680
                                  |
                     mTLS RPC 写入所属 Owner Agent
                                  |
  +-------------------------------+-------------------------------+
  |                                                               |
本地 SQLite + 日志 + 进程状态                                  Gossip 摘要同步
  |                                                               |
ProcMesh Agent -- Unix socket -- ProcMesh Shim -- 业务进程      Raft 控制数据
```

ProcMesh 包含三个二进制程序：

| 程序 | 用途 |
| --- | --- |
| `procmesh-agent` | 常驻 Agent，管理本机进程，提供 Web、API、RPC 与集群通信。 |
| `procmesh-shim` | 业务进程包装器，帮助业务进程跨 Agent 重启继续运行。 |
| `procmesh` | 管理集群和进程的命令行客户端。 |

进程配置、实例状态、详细指标和日志由 Owner Agent 本地管理；Gossip 只传播成员信息与摘要；用户、RBAC、准入和策略等控制面数据由 Raft 保存。所有远程写操作均通过 mTLS RPC 转发到 Owner Agent。

## 常用 CLI 操作

```bash
# 查看、停止和重启进程
procmesh --server 127.0.0.1:18680 process get demo-worker
procmesh --server 127.0.0.1:18680 process stop demo-worker
procmesh --server 127.0.0.1:18680 process restart demo-worker

# 查询配置历史并回滚
procmesh --server 127.0.0.1:18680 process history demo-worker
procmesh --server 127.0.0.1:18680 process rollback demo-worker \
  --to 1 --expected-revision 3 --comment 'rollback configuration'

# 管理节点与远程 Owner 上的进程
procmesh --server 127.0.0.1:18680 node list
procmesh --server 127.0.0.1:18680 --node <NODE_ID> process list
```

运行 `procmesh` 可查看完整命令列表和参数说明。

## 运维与安全

### 端口

| 端口 | 协议 | 用途 |
| --- | --- | --- |
| `18680` | TCP/HTTP | Web UI、CLI 和 ConnectRPC API。 |
| `18683` | TCP/mTLS | Agent 间远程进程操作。 |
| `18685` | TCP | Raft 控制面。 |
| `18689` | TCP/UDP | Gossip 成员发现与状态传播。 |

生产环境中不要将这些端口直接暴露到公网。使用防火墙或安全组限制访问范围；跨不可信网络访问 `18680` 时，请使用 HTTPS 反向代理、VPN 或堡垒机。`--insecure-listen` 仅允许 Agent 监听非回环地址，不会启用 HTTPS。

### 本机 break-glass 检查

Agent 会启动一个只存在于本机的 Unix socket。默认路径为 `$data_dir/break-glass.sock`，权限为 `0600`；生产环境以 root 运行 Agent 时只有 root 可访问。可在 `agent.yaml` 中显式配置 socket 和受限 OS 用户组：

```yaml
break_glass:
  socket: /run/procmesh-break-glass.sock
  group: procmesh-operators
```

socket 父目录需要允许该组穿越。配置组后 socket 权限为 `0660`。授权运维人员使用独立的 `--break-glass` 入口检查本机 Owner Agent 数据：

```bash
procmesh --break-glass=/run/procmesh-break-glass.sock process list
procmesh --break-glass=/run/procmesh-break-glass.sock process get demo-worker
procmesh --break-glass=/run/procmesh-break-glass.sock process logs demo-worker --lines 100
procmesh --break-glass=/run/procmesh-break-glass.sock \
  --operation-id 9e602ad6-408f-4c59-9558-1bfbd0df59b7 \
  --reason 'recover service after control quorum loss' \
  process restart demo-worker
procmesh --break-glass=/run/procmesh-break-glass.sock \
  --operation-id 65fd4a34-4a82-44f7-b4fa-6c8ca72aa458 \
  --reason 'recover disabled administrator' \
  user enable user-admin
```

该模式只支持 `process list/get/logs/start/stop/restart/kill` 和 `user enable`。四个进程生命周期操作及用户恢复必须显式提供非空 `--operation-id` 和 `--reason`。用户恢复只在当前 Raft Leader 的本机 socket 上提交，不向其他节点转发；恢复会删除该用户的旧 Session 并吊销旧 API Token。该通道不使用集群 Session，不接受 `--server`、`--node` 或 `--auth-token`，失败时也不会自动切换到普通 TCP 模式。

进程生命周期操作重复使用同一个 operation ID 时会复用本机幂等 journal，不会重复执行副作用；`user enable` 本身为幂等状态转换。

break-glass 明确拒绝 process apply/delete/adopt、配置编辑、backup/restore、batch、远程节点选择和除 `user enable` 外的 Control Plane 操作，也不会签发 Session、API Token 或任何可通过 TCP 使用的凭证。每次成功、失败或拒绝的请求都会写入本机 SQLite 审计；生命周期审计包含 OS UID/用户、原因、operation ID、动作、本机节点、Process 身份、时间、结果和脱敏错误码。

### CPU 与内存诊断

Agent 默认关闭 pprof。需要采样时，可通过命令行临时启用独立的本地诊断端口：

```bash
procmesh-agent --data-dir /tmp/procmesh --pprof-listen 127.0.0.1:6060
```

也可以在 `agent.yaml` 中配置 `pprof.listen`。建议始终绑定回环地址，远程机器通过 SSH 隧道访问；pprof 包含进程内部信息，不应直接暴露到公网。

```bash
go tool pprof -http=:8081 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'
go tool pprof -http=:8081 http://127.0.0.1:6060/debug/pprof/heap
curl -o goroutine.txt 'http://127.0.0.1:6060/debug/pprof/goroutine?debug=2'
```

## 文档

- [部署与集群快速开始](docs/QUICKSTART_ZH.md)：systemd 安装、三节点集群初始化、扩容、进程配置与排障。
- [UI 优化说明](docs/UI_OPTIMIZATION.md)：Web UI 设计与优化记录。
- [变更日志](CHANGELOG.md)：已发布和开发中的功能变更。
- [产品需求文档](docs/v2-prd/v2-prd.md)：产品范围、架构原则与非目标。
- [架构设计与实施计划](docs/superpowers/)：各阶段的设计决策与实施记录。

## 开发指南

### 开发环境要求

- Go 1.25 或更高版本。
- Node.js 22.20.0 或更高的 22.x 版本（低于 23）及 npm。
- GNU Make。
- 修改 Protocol Buffers 定义时，还需要 `protoc` 及相应 Go/TypeScript 插件。
- Linux 用于验证 systemd、cgroup、setuid 等完整生产语义。macOS 可用于日常开发和大部分功能验证。

### 从源码构建

```bash
make web
make bin
```

前端构建结果会写入 `internal/web/dist/` 并嵌入 `procmesh-agent`。最终产物位于：

```text
bin/procmesh
bin/procmesh-agent
bin/procmesh-shim
```

### 测试与代码生成

```bash
# Go 测试
make test             # 全量 Go 测试；非 Agent 包与 Agent 测试分片并行执行
make test-acceptance  # 无缓存 Agent 验收测试；真实进程/集群，耗时较长

# Web 开发和测试
cd web && npm ci && npm run dev
cd web && npm test
cd web && npm run test:e2e
cd web && npm run i18n:check
make test-e2e-web
make test-e2e

# Protocol Buffers 代码生成
make proto
make proto-ts
```

Agent 测试默认分为 4 个独立进程，可通过 `PROCMESH_AGENT_TEST_SHARDS=1..16` 调整并行数。每个分片使用独立的 Shim 二进制和 CLI session 路径；分片只改变执行方式，不减少 `make test` 的测试集合。

### 发布 GitHub Release

使用 GitHub CLI 登录，并确保 `main` 工作区没有未提交改动后执行：

```bash
gh auth login
scripts/release.sh v1.2.3
```

脚本会构建 Web UI，并为 Linux 的 amd64、arm64、armv7 及 macOS 的 amd64、arm64 目标生成包含三个二进制程序的压缩包及 `checksums.txt`；Linux 包同时包含默认 `agent.yaml` 与 systemd unit。随后脚本会推送 `main`、创建带注释的版本标签并发布 GitHub Release。

可先运行 `scripts/release.sh v1.2.3 --dry-run`，只生成和检查产物。Windows 暂不发布，因为 Agent 和 Shim 依赖 Unix 进程及文件系统 API。
