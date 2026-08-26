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

## 环境要求

- Go 1.25 或更高版本。
- Node.js 22.20.0 或更高的 22.x 版本（低于 23），仅构建或开发 Web UI 时需要。
- GNU Make。
- Linux 用于生产部署。macOS 可用于本地开发与功能验证，但不具备 systemd、cgroup 等完整生产能力。
- 修改 Protocol Buffers 定义时，还需要 `protoc` 及相应 Go/TypeScript 插件。

## 快速开始

以下步骤以本机单节点体验为例。生产环境请先阅读[部署与集群快速开始](docs/QUICKSTART_ZH.md)。

### 1. 构建

```bash
make web
make bin
```

构建产物位于 `bin/`：

```text
bin/procmesh
bin/procmesh-agent
bin/procmesh-shim
```

### 2. 启动 Agent

在第一个终端中执行：

```bash
mkdir -p /tmp/procmesh-quickstart
./bin/procmesh-agent \
  --data-dir /tmp/procmesh-quickstart \
  --listen 127.0.0.1:18680 \
  --rpc 127.0.0.1:18683 \
  --control 127.0.0.1:18685 \
  --gossip 127.0.0.1:18689 \
  --shim-bin ./bin/procmesh-shim
```

在第二个终端中检查服务状态并初始化集群：

```bash
curl -fsS http://127.0.0.1:18680/healthz
curl -fsS http://127.0.0.1:18680/readyz
./bin/procmesh --server 127.0.0.1:18680 cluster init --admin-user admin
```

初始化命令只可成功执行一次，并会输出一次性管理员密码。请立即将其保存到密码管理器，然后登录：

```bash
./bin/procmesh --server 127.0.0.1:18680 login --user admin
```

访问 <http://127.0.0.1:18680/> 可打开 Web UI。Web UI 需要单独使用管理员账户登录。

### 3. 创建并启动第一个进程

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
./bin/procmesh --server 127.0.0.1:18680 process apply \
  --file demo-worker.yaml \
  --expected-revision 0 \
  --comment 'initial deployment'

./bin/procmesh --server 127.0.0.1:18680 process start demo-worker
./bin/procmesh --server 127.0.0.1:18680 process list
./bin/procmesh --server 127.0.0.1:18680 process logs demo-worker --lines 20 --stream stdout
```

`autostart` 用于主机或 Agent 恢复后的运行意图，不会在首次创建配置时自动启动进程，因此首次部署仍需执行 `process start`。

## 常用命令

```bash
# 测试与构建
make test             # 默认快速 Go 测试
make test-acceptance  # Agent 验收测试（真实进程/集群，耗时较长）
make test-e2e-web     # 启动测试 Agent 并运行完整 Playwright 门禁
make test-e2e         # 依次运行 Agent 验收与 Web E2E
make web
make bin

# 前端开发和测试
cd web && npm ci && npm run dev
cd web && npm test
cd web && npm run test:e2e
cd web && npm run i18n:check

# Protocol Buffers 代码生成
make proto
make proto-ts
```

常用 CLI 操作：

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

### 本机 break-glass 检查

Agent 会启动一个只存在于本机的 Unix socket。默认路径为
`$data_dir/break-glass.sock`，权限为 `0600`；生产环境以 root 运行 Agent 时只有
root 可访问。可在 `agent.yaml` 中显式配置 socket 和受限 OS 用户组：

```yaml
break_glass:
  socket: /run/procmesh-break-glass.sock
  group: procmesh-operators
```

socket 父目录需要允许该组穿越。配置组后 socket 权限为 `0660`。授权运维人员使用
独立的 `--break-glass` 入口检查本机 Owner Agent 数据：

```bash
procmesh --break-glass=/run/procmesh-break-glass.sock process list
procmesh --break-glass=/run/procmesh-break-glass.sock process get demo-worker
procmesh --break-glass=/run/procmesh-break-glass.sock process logs demo-worker --lines 100
procmesh --break-glass=/run/procmesh-break-glass.sock \
  --operation-id 9e602ad6-408f-4c59-9558-1bfbd0df59b7 \
  --reason 'recover service after control quorum loss' \
  process restart demo-worker
```

该模式只支持 `process list/get/logs/start/stop/restart/kill`。四个生命周期操作必须
显式提供非空 `--operation-id` 和 `--reason`，重复 operation ID 复用本机幂等 journal，
不会重复执行生命周期副作用。该通道不使用集群 Session，不接受 `--server`、`--node`
或 `--auth-token`，失败时也不会自动切换到普通 TCP 模式。

break-glass 明确拒绝 process apply/delete/adopt、配置编辑、backup/restore、batch、
远程节点选择和全部 Control Plane 操作，也不会签发 Session、API Token 或任何可通过
TCP 使用的凭证。每次成功、失败或拒绝的请求都会写入本机 SQLite 审计；生命周期审计
包含 OS UID/用户、原因、operation ID、动作、本机节点、Process 身份、时间、结果和
脱敏错误码。

运行 `procmesh` 可查看完整命令列表和参数说明。

## 发布 GitHub Release

使用 GitHub CLI 登录，并确保 `main` 工作区没有未提交改动后执行：

```bash
gh auth login
scripts/release.sh v1.2.3
```

脚本会构建 Web UI，并为 Linux 的 amd64、arm64、armv7 及 macOS 的 amd64、arm64 目标生成包含三个二进制程序的压缩包及 `checksums.txt`；Linux 包同时包含默认 `agent.yaml` 与 systemd 单元。随后推送 `main`、创建带注释的版本标签并发布 GitHub Release。可先用 `scripts/release.sh v1.2.3 --dry-run` 只生成和检查产物。Linux 是生产目标；macOS 仅用于开发和评估。Windows 暂不发布，因为 Agent 和 Shim 依赖 Unix 进程及文件系统 API。

## 自动安装（Linux）

以下命令会交互式安装最新正式 Release。安装器只支持 `amd64`、`arm64` 和 `armv7l`，下载后强制校验 Release 的 SHA-256 值：

```bash
curl -fsSL https://raw.githubusercontent.com/xiaoyannzbbb/procmesh/main/scripts/install.sh | bash
```

默认安装到 `/usr/local/bin`，可在提示时改为其他绝对路径或 `~/...`。安装器默认不创建 systemd 服务；选择创建后可配置数据目录、配置文件、监听地址和端口，默认监听 `127.0.0.1:18680`。非回环监听会自动加入 `--insecure-listen`，因此必须使用防火墙、HTTPS 反向代理、VPN 或堡垒机限制访问。已有配置、数据目录和 systemd 单元不会被覆盖。

## 端口与安全

| 端口 | 协议 | 用途 |
| --- | --- | --- |
| `18680` | TCP/HTTP | Web UI、CLI 和 ConnectRPC API。 |
| `18683` | TCP/mTLS | Agent 间远程进程操作。 |
| `18685` | TCP | Raft 控制面。 |
| `18689` | TCP/UDP | Gossip 成员发现与状态传播。 |

生产环境中不要将这些端口直接暴露到公网。使用防火墙或安全组限制访问范围；跨不可信网络访问 `18680` 时，请使用 HTTPS 反向代理、VPN 或堡垒机。`--insecure-listen` 仅允许 Agent 监听非回环地址，不会启用 HTTPS。

## CPU 与内存诊断

Agent 默认关闭 pprof。需要采样时，可通过命令行临时启用独立的本地诊断端口：

```bash
./bin/procmesh-agent --data-dir /tmp/procmesh --pprof-listen 127.0.0.1:6060
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

## 开发说明

前端构建结果会嵌入 `procmesh-agent`。修改 Web UI 后执行 `make web`，再执行 `make bin` 以生成包含最新 UI 的 Agent 二进制。

核心数据模型遵循以下边界：进程配置、实例状态、详细指标和日志由 Owner Agent 本地管理；Gossip 只用于成员与摘要同步；所有远程修改均通过 mTLS RPC 转发至 Owner Agent。
