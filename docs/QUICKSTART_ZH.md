# ProcMesh 部署与集群快速开始

本文面向第一次部署 ProcMesh 的运维人员，目标是完成以下工作：

1. 构建并安装 ProcMesh；
2. 用 systemd 启动 Agent；
3. 初始化单节点或三节点集群；
4. 创建、启动并检查第一个托管进程；
5. 掌握后续启停、更新、日志查看和基础排障方法。

生产环境仅支持 Linux。macOS 可用于本地开发和功能体验，但不具备 systemd、cgroup 等完整生产能力。

## 1. 先了解三个程序

ProcMesh 由三个二进制程序组成，部署时必须一起安装：

| 程序 | 作用 | 是否常驻 |
| --- | --- | --- |
| `procmesh-agent` | 管理本机进程，提供 Web、API、RPC、Raft 和 Gossip 服务 | 是 |
| `procmesh-shim` | 包装一个业务进程，使 Agent 重启时业务进程仍能继续运行 | 由 Agent 按需启动 |
| `procmesh` | 管理集群和进程的命令行客户端 | 否 |

ProcMesh 没有独立的中心服务器。每个节点都运行一个 Agent，每个业务进程的配置、运行状态和日志由其所在节点负责。

## 2. 推荐拓扑和端口

本文使用下面的三节点示例。请将示例 IP 替换为实际的静态内网 IP：

| 节点 | 主机名 | 内网 IP | 初始角色 |
| --- | --- | --- | --- |
| 节点 1 | `pm-node-1` | `10.0.0.11` | 初始化节点、初始 Raft voter |
| 节点 2 | `pm-node-2` | `10.0.0.12` | 加入后为 Raft non-voter |
| 节点 3 | `pm-node-3` | `10.0.0.13` | 加入后为 Raft non-voter |

每个节点使用以下端口：

| 端口 | 协议 | 用途 | 建议访问范围 |
| --- | --- | --- | --- |
| `9000` | TCP/HTTP | Web UI、CLI 和 ConnectRPC API | 管理网、集群节点 |
| `9001` | TCP/mTLS | Agent 间远程进程操作 | 仅集群节点 |
| `9002` | TCP | Raft 控制面 | 仅集群节点 |
| `7946` | TCP + UDP | Gossip 成员发现和状态传播 | 仅集群节点 |

重要安全说明：

- `--insecure-listen` 只是允许 Agent 绑定非回环地址，不会为 `9000` 自动启用 HTTPS。
- 不要把上述端口直接暴露到公网。应使用安全组或防火墙限定来源地址。
- 跨不可信网络管理时，在 `9000` 前配置 HTTPS 反向代理，或通过 VPN/堡垒机访问。
- 节点 IP 应保持稳定。多节点部署要绑定实际内网 IP，不要绑定 `0.0.0.0`，否则节点可能向集群发布不可用于远程访问的地址。

## 3. 最短路径：先在一台机器上跑起来

如果只想快速验证功能，可先执行本节。正式三节点部署请继续阅读第 4 节。

### 3.1 构建

构建机需要：

- Go 1.25 或更高版本；
- Node.js 18 或更高版本，推荐 Node.js 20 LTS；
- npm；
- GNU Make。

在仓库根目录执行：

```bash
make web
make bin
```

`make web` 会把前端构建到 `internal/web/dist/`，随后 `make bin` 会把 Web UI 嵌入 Agent。最终产物位于：

```text
bin/procmesh
bin/procmesh-agent
bin/procmesh-shim
```

### 3.2 前台启动单节点 Agent

```bash
mkdir -p /tmp/procmesh-quickstart
./bin/procmesh-agent \
  --data-dir /tmp/procmesh-quickstart \
  --listen 127.0.0.1:9000 \
  --rpc 127.0.0.1:9001 \
  --control 127.0.0.1:9002 \
  --gossip 127.0.0.1:7946 \
  --shim-bin ./bin/procmesh-shim
```

保持该终端运行，另开终端验证：

```bash
curl -fsS http://127.0.0.1:9000/healthz
curl -fsS http://127.0.0.1:9000/readyz
./bin/procmesh --server 127.0.0.1:9000 status
```

`healthz` 和 `readyz` 应返回 `ok`，`status` 应输出 `ready` 和当前进程数。

### 3.3 初始化并登录

初始化只能成功执行一次：

```bash
./bin/procmesh --server 127.0.0.1:9000 cluster init --admin-user admin
```

输出格式如下：

```text
cluster_id=<集群 ID>
node_id=<节点 ID>
admin_user=admin
admin_password=<一次性显示的随机密码>
```

立即把随机密码保存到密码管理器。初始化接口不会再次显示明文密码。

登录时，省略 `--password` 可从标准输入读取密码，避免密码直接出现在命令参数中：

```bash
./bin/procmesh --server 127.0.0.1:9000 login --user admin
```

输入上一条命令返回的密码并回车。CLI 会把会话以 `0600` 权限保存到 `~/.config/procmesh/session`。不要用 `sudo procmesh` 登录后再用普通用户执行命令，否则两个用户读取的会话文件不同。

现在可在浏览器访问 `http://127.0.0.1:9000/`，或直接跳到第 7 节启动进程。浏览器不会复用 CLI 保存的会话，需要在 Web 登录页再次使用管理员账号和密码登录。

## 4. 构建和分发生产二进制

### 4.1 在构建机编译

```bash
make web
make bin
go test ./...
```

如果构建机与生产节点的 CPU 架构不同，需要设置对应的交叉编译参数。以下示例构建 Linux AMD64 二进制；前端仍应先在当前平台执行 `make web`：

```bash
GOOS=linux GOARCH=amd64 go build -o bin/procmesh ./cmd/procmesh
GOOS=linux GOARCH=amd64 go build -o bin/procmesh-agent ./cmd/procmesh-agent
GOOS=linux GOARCH=amd64 go build -o bin/procmesh-shim ./cmd/procmesh-shim
```

ARM64 节点把 `GOARCH=amd64` 改为 `GOARCH=arm64`。

### 4.2 在每个节点安装

将三个文件分发到每个节点后执行：

```bash
sudo install -m 0755 bin/procmesh /usr/local/bin/procmesh
sudo install -m 0755 bin/procmesh-agent /usr/local/bin/procmesh-agent
sudo install -m 0755 bin/procmesh-shim /usr/local/bin/procmesh-shim
```

验证文件和版本兼容性：

```bash
command -v procmesh
command -v procmesh-agent
command -v procmesh-shim
procmesh-agent --help
```

三个节点应使用同一次构建产生的二进制，避免协议版本不一致导致节点拒绝加入。

## 5. 配置 systemd

以下步骤需要在每个节点执行。

### 5.1 创建目录和基础配置

```bash
sudo install -d -m 0750 /etc/procmesh
sudo install -d -m 0750 /var/lib/procmesh
```

创建 `/etc/procmesh/agent.yaml`：

```yaml
disk:
  warn_percent: 85
  cleanup_percent: 90
  emergency_percent: 95
  auto_delete: false
  emergency_stop_writes: true

batch:
  max_concurrency: 16
  target_timeout: 30s
```

这份配置可在各节点复用。默认情况下，磁盘达到 85% 时告警，90% 时进入清理等级，95% 时停止新增日志和指标写入以保护核心数据。`auto_delete: false` 表示不会自动删除旧日志；确认日志保留策略后再决定是否改为 `true`。

设置配置文件权限：

```bash
sudo chmod 0640 /etc/procmesh/agent.yaml
```

### 5.2 为每个节点指定实际 IP

节点 1 创建 `/etc/procmesh/procmesh.env`：

```bash
PROCMESH_NODE_IP=10.0.0.11
```

节点 2 和节点 3 分别使用：

```bash
PROCMESH_NODE_IP=10.0.0.12
```

```bash
PROCMESH_NODE_IP=10.0.0.13
```

设置权限：

```bash
sudo chmod 0640 /etc/procmesh/procmesh.env
```

### 5.3 安装 systemd 单元

创建 `/etc/systemd/system/procmesh-agent.service`：

```ini
[Unit]
Description=ProcMesh Agent
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/procmesh/procmesh.env
ExecStart=/usr/local/bin/procmesh-agent \
  --data-dir /var/lib/procmesh \
  --config /etc/procmesh/agent.yaml \
  --listen ${PROCMESH_NODE_IP}:9000 \
  --rpc ${PROCMESH_NODE_IP}:9001 \
  --control ${PROCMESH_NODE_IP}:9002 \
  --gossip ${PROCMESH_NODE_IP}:7946 \
  --shim-bin /usr/local/bin/procmesh-shim \
  --insecure-listen \
  --log-format json \
  --log-level info
Restart=on-failure
RestartSec=2s
KillMode=process
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

该单元默认以 root 运行，与仓库自带的 systemd 单元行为一致。这样才能按 `run_as_user` 切换业务进程用户并设置资源限制。若业务只需以一个固定低权限用户运行，可自行增加 `User=` 和 `Group=`，但该账户必须拥有 `/var/lib/procmesh` 以及业务工作目录、日志目录的读写权限，此时也不能再切换到其他用户。

`KillMode=process` 是有意设置的：Agent 重启或异常退出时，已由 shim 托管的业务进程不会被 systemd 一并杀死。

加载并启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now procmesh-agent
sudo systemctl status procmesh-agent --no-pager
```

查看实时日志：

```bash
sudo journalctl -u procmesh-agent -f
```

### 5.4 验证每个节点

在各节点把 `NODE_IP` 替换为本机地址：

```bash
curl -fsS http://NODE_IP:9000/healthz
curl -fsS http://NODE_IP:9000/readyz
procmesh --server NODE_IP:9000 status
```

同时确认监听地址不是 `0.0.0.0` 或 `127.0.0.1`：

```bash
sudo ss -lntup | grep -E ':(9000|9001|9002|7946)\b'
```

Agent 尚未初始化集群时，`9001` 的 mTLS RPC 服务可能尚未监听；完成初始化或加入集群后会启动。

## 6. 初始化三节点集群

以下命令均以普通部署用户执行，不需要 `sudo`。

### 6.1 初始化节点 1

```bash
procmesh --server 10.0.0.11:9000 cluster init --admin-user admin
```

安全保存输出中的 `admin_password`，然后登录节点 1：

```bash
procmesh --server 10.0.0.11:9000 login --user admin
```

登录成功后验证：

```bash
procmesh --server 10.0.0.11:9000 node list
```

此时应只有节点 1。

### 6.2 生成加入令牌

为节点 2 和节点 3 创建一个可使用两次、30 分钟后过期的令牌：

```bash
procmesh --server 10.0.0.11:9000 node token create --ttl 30m --uses 2
```

输出格式如下：

```text
token_id=<令牌 ID>
token=<加入令牌明文>
expires=<过期时间的 Unix 时间戳>
uses=2
```

加入令牌是敏感凭据。只把 `token=` 后的值临时提供给待加入节点，用完后不要写入脚本、Git 或长期日志。

### 6.3 加入节点 2

在节点 2 上执行，其中 `<JOIN_TOKEN>` 替换为上一步的令牌：

```bash
procmesh --server 10.0.0.12:9000 agent join \
  --seed 10.0.0.11:9000 \
  --token '<JOIN_TOKEN>'
```

成功时会输出集群 ID 和种子节点 Gossip 地址。加入过程会在节点 2 保存 CA 证书、节点证书和 Raft/Gossip 元数据。

### 6.4 加入节点 3

在节点 3 上执行：

```bash
procmesh --server 10.0.0.13:9000 agent join \
  --seed 10.0.0.11:9000 \
  --token '<JOIN_TOKEN>'
```

### 6.5 验证节点发现

回到已登录节点 1 的终端：

```bash
procmesh --server 10.0.0.11:9000 node list
```

每行依次包含：

```text
node_id  hostname  state  protocol_version  api_address  gossip_address  rpc_address
```

确认三个节点均出现、状态为 `ALIVE`，并且地址分别是实际内网 IP。保存节点 2 和节点 3 的 `node_id`，后面提升 Raft 角色和远程部署进程时会用到。

如果新节点没有立即出现，可等待几秒后重试。仍未出现时检查节点间 `7946/TCP` 和 `7946/UDP`。

### 6.6 可选但推荐：组成三 voter Raft

新加入节点默认是 Raft non-voter。三个节点全部在线并稳定后，在节点 1 执行：

```bash
procmesh --server 10.0.0.11:9000 node promote <NODE_2_ID>
procmesh --server 10.0.0.11:9000 node promote <NODE_3_ID>
```

三 voter 集群允许任意一个 voter 故障后继续保持控制面 quorum。不要只部署两个 voter 后长期运行：两个 voter 中任意一个离线都会失去多数派。提升期间确保三个节点及 `9002/TCP` 网络稳定。

## 7. 创建并启动第一个进程

### 7.1 准备一个最小进程配置

在管理终端创建 `demo-worker.yaml`：

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
  backoff:
    initial_ms: 1000
    max_ms: 30000
    multiplier: 2

health:
  type: alive
  initial_delay_ms: 1000
  interval_ms: 5000
  timeout_ms: 1000
  failure_threshold: 3
  success_threshold: 1

log:
  max_size: 10485760
  max_files: 5
  max_age_seconds: 604800
  compress: true
```

最小必填字段是 `name`、`command` 和 `instances`。示例显式写出了常用重启、健康检查和日志轮转参数，便于直接用于验证。

`autostart: true` 表示主机重启、Agent 恢复时应恢复该进程，不表示创建配置后立即启动。首次创建后仍要显式执行 `process start`。

### 7.2 部署到节点 1

创建配置时使用期望版本 `0`：

```bash
procmesh --server 10.0.0.11:9000 process apply \
  --file demo-worker.yaml \
  --expected-revision 0 \
  --comment 'initial deployment'
```

输出类似：

```text
<process_id> revision=1
```

然后启动：

```bash
procmesh --server 10.0.0.11:9000 process start demo-worker
```

Agent 每秒执行一次状态协调，通常 1 至 2 秒后即可看到 `RUNNING` 和 `HEALTHY`：

```bash
procmesh --server 10.0.0.11:9000 process list
procmesh --server 10.0.0.11:9000 process get demo-worker
procmesh --server 10.0.0.11:9000 process logs demo-worker --lines 20 --stream stdout
```

### 7.3 从节点 1 入口部署到节点 2

CLI 的 `--node` 参数可指定目标 Owner 节点，值可以是 `node_id` 或唯一主机名。以下示例复用已登录节点 1 的会话，把进程部署到节点 2：

```bash
procmesh --server 10.0.0.11:9000 --node <NODE_2_ID> process apply \
  --file demo-worker.yaml \
  --expected-revision 0 \
  --comment 'deploy to node 2'

procmesh --server 10.0.0.11:9000 --node <NODE_2_ID> process start demo-worker
procmesh --server 10.0.0.11:9000 --node <NODE_2_ID> process list
```

远程写操作通过节点间 `9001/TCP` mTLS RPC 转发到 Owner Agent。若本地操作成功而远程操作失败，优先检查节点间 `9001/TCP`、节点状态和 `rpc_address`。

### 7.4 常用进程操作

```bash
# 查看详情和当前 revision
procmesh --server 10.0.0.11:9000 process get demo-worker

# 停止、启动、重启
procmesh --server 10.0.0.11:9000 process stop demo-worker
procmesh --server 10.0.0.11:9000 process start demo-worker
procmesh --server 10.0.0.11:9000 process restart demo-worker

# 强制终止并将期望状态设为 STOPPED
procmesh --server 10.0.0.11:9000 process kill demo-worker

# 读取 stdout 或 stderr
procmesh --server 10.0.0.11:9000 process logs demo-worker --lines 100 --stream stdout
procmesh --server 10.0.0.11:9000 process logs demo-worker --lines 100 --stream stderr

# 查看配置历史
procmesh --server 10.0.0.11:9000 process history demo-worker
```

进程进入 `FATAL` 后，需要先清除失败状态，再重新启动：

```bash
procmesh --server 10.0.0.11:9000 process reset-failure demo-worker
procmesh --server 10.0.0.11:9000 process start demo-worker
```

## 8. 更新和回滚进程配置

每次修改配置都使用乐观锁。先获取当前版本：

```bash
procmesh --server 10.0.0.11:9000 process get demo-worker
```

假设输出中 `revision` 为 `1`，修改 YAML 后执行：

```bash
procmesh --server 10.0.0.11:9000 process apply \
  --file demo-worker.yaml \
  --expected-revision 1 \
  --comment 'increase instances'
```

如果其他人已更新配置，命令会返回版本冲突。重新读取最新配置和 revision，确认差异后再提交，不要盲目覆盖。

回滚同样会生成一个新 revision。假设当前最新版本为 `3`，要恢复版本 `1`：

```bash
procmesh --server 10.0.0.11:9000 process rollback demo-worker \
  --to 1 \
  --expected-revision 3 \
  --comment 'rollback bad configuration'
```

部分运行参数需要重启进程才能应用。更新后用 `process get` 检查提示，并在合适的维护窗口执行：

```bash
procmesh --server 10.0.0.11:9000 process restart demo-worker
```

删除前必须先停止进程，等待 `process get` 显示实例已进入 `STOPPED`，再使用最新 revision 删除：

```bash
procmesh --server 10.0.0.11:9000 process stop demo-worker
procmesh --server 10.0.0.11:9000 process get demo-worker
procmesh --server 10.0.0.11:9000 process delete demo-worker --expected-revision <LATEST_REVISION>
```

## 9. 进程 YAML 常用字段

| 字段 | 说明 |
| --- | --- |
| `name` | 进程名；必须以字母开头，只能包含字母、数字、`_`、`-`，最长 63 字符 |
| `process_id` | 可选；创建时省略可自动生成 UUID，更新时建议保留原 ID |
| `owner_agent_id` | 可选；通常通过 CLI `--node` 指定 Owner，不必手写 |
| `group` | 可选进程组，用于筛选和 RBAC scope |
| `command` | 可执行文件路径或可由运行环境找到的命令 |
| `args` | 参数数组，不经过 shell 解析；需要管道、重定向时显式使用 `/bin/sh -c` |
| `working_directory` | 工作目录 |
| `run_as_user` | Linux 运行用户；Agent 必须有切换到该用户的权限 |
| `environment` | 环境变量键值表 |
| `instances` | 实例数，最小为 1 |
| `autostart` | 主机重启后的恢复策略，不代替首次 `process start` |
| `restart.mode` | `never`、`always` 或 `on-failure` |
| `health.type` | 空值/`alive`、`http`、`tcp` 或 `exec` |
| `log.directory` | 可选自定义日志目录；需确保权限和磁盘策略正确 |
| `log.redirect_stderr` | 为 `true` 时把 stderr 一并写入 stdout 日志 |
| `resources.cpu_quota_millis` | Linux cgroup v2 CPU 配额 |
| `resources.memory_bytes` | Linux cgroup v2 内存上限 |
| `resources.open_files` | Linux 文件描述符上限 |
| `dependencies` | 按进程名声明依赖，条件为 `STARTED` 或 `HEALTHY` |

带环境变量、资源限制和 TCP 健康检查的示例：

```yaml
name: api-server
group: production
command: /opt/myapp/bin/api-server
args:
  - --config
  - /etc/myapp/config.yaml
working_directory: /opt/myapp
run_as_user: myapp
environment:
  APP_ENV: production
  PORT: "8080"
instances: 2
autostart: true
stop_signal: SIGTERM
kill_signal: SIGKILL
stop_timeout_ms: 15000

restart:
  mode: on-failure
  max_retries: 5
  retry_window_ms: 60000
  backoff:
    initial_ms: 1000
    max_ms: 30000
    multiplier: 2

health:
  type: tcp
  address: 127.0.0.1:8080
  initial_delay_ms: 5000
  interval_ms: 10000
  timeout_ms: 2000
  failure_threshold: 3
  success_threshold: 1
  restart_on_failure: true
  restart_cooldown_ms: 30000

log:
  max_size: 104857600
  max_files: 10
  max_age_seconds: 604800
  compress: true
  redirect_stderr: false

resources:
  cpu_quota_millis: 500
  memory_bytes: 536870912
  open_files: 4096
```

如果同一节点上的多个实例都绑定同一个固定端口，应用本身必须支持端口复用或按实例分配端口，否则只有第一个实例能正常监听。

## 10. 部署验收清单

完成部署后逐项检查：

```bash
# 1. Agent 存活和数据存储就绪
curl -fsS http://10.0.0.11:9000/healthz
curl -fsS http://10.0.0.11:9000/readyz

# 2. CLI 可认证访问
procmesh --server 10.0.0.11:9000 status

# 3. 三个节点均为 ALIVE，地址均为实际内网 IP
procmesh --server 10.0.0.11:9000 node list

# 4. 本机进程可运行
procmesh --server 10.0.0.11:9000 process list

# 5. 远程节点可读取并操作
procmesh --server 10.0.0.11:9000 --node <NODE_2_ID> process list

# 6. Agent 开机自启
systemctl is-enabled procmesh-agent
systemctl is-active procmesh-agent
```

建议在测试进程上再验证一次 Agent 恢复语义：重启 `procmesh-agent`，确认业务进程没有被杀死，Agent 恢复后仍能重新识别并管理该进程。

## 11. 运维和故障排查

### 11.1 Agent 启动失败：non-loopback listen requires

症状：

```text
non-loopback listen requires --insecure-listen
```

原因是 Agent 绑定了内网 IP，但未显式允许非回环监听。确认端口只在可信网络开放后，在 systemd 的 `ExecStart` 中加入 `--insecure-listen`。

### 11.2 节点列表出现 `0.0.0.0` 或 `127.0.0.1`

多节点部署时，这些地址通常不能供其他节点访问。把 systemd 中四个监听地址改为本机实际静态内网 IP，执行：

```bash
sudo systemctl daemon-reload
sudo systemctl restart procmesh-agent
```

然后重新检查 `node list`。不要在未确认数据和集群身份的情况下删除 `/var/lib/procmesh` 或重复初始化。

### 11.3 加入失败

依次检查：

1. 节点 2/3 的 `/healthz` 和 `/readyz` 是否正常；
2. 加入节点能否访问种子节点 `9000/TCP`；
3. 令牌是否过期、已用完或已撤销；
4. 节点是否曾使用当前数据目录加入其他集群；
5. 三个节点是否使用兼容的协议版本；
6. 节点间 `7946/TCP+UDP` 和 `9002/TCP` 是否放通。

创建新令牌后可以重试，但如果返回 `cluster already initialized`，说明该数据目录已有集群身份，不应继续重复执行 `agent join`。

### 11.4 CLI 返回 authentication required

重新针对当前 `--server` 登录：

```bash
procmesh --server 10.0.0.11:9000 login --user admin
```

默认会话只匹配登录时的 server 地址。使用 `10.0.0.11:9000` 登录后，改用主机名或另一个节点地址时不会自动复用该会话。

### 11.5 创建后进程一直是 STOPPED

`process apply` 只保存配置并创建实例记录。执行：

```bash
procmesh --server 10.0.0.11:9000 process start <NAME>
```

### 11.6 启动进程时报 shim not found

确认 `procmesh-shim` 位于 `PATH` 中或与 `procmesh-agent` 在同一目录。本文 systemd 单元还通过 `--shim-bin /usr/local/bin/procmesh-shim` 显式指定了路径：

```bash
ls -l /usr/local/bin/procmesh-shim
sudo journalctl -u procmesh-agent -n 100 --no-pager
```

### 11.7 进程启动后立即退出或进入 FATAL

检查以下内容：

```bash
procmesh --server 10.0.0.11:9000 process get <NAME>
procmesh --server 10.0.0.11:9000 process logs <NAME> --lines 200 --stream stdout
procmesh --server 10.0.0.11:9000 process logs <NAME> --lines 200 --stream stderr
sudo journalctl -u procmesh-agent -n 200 --no-pager
```

重点确认命令路径、工作目录、运行用户权限、环境变量、端口冲突、cgroup v2 权限和健康检查地址。

### 11.8 远程操作失败，但本地操作正常

检查目标节点是否为 `ALIVE`，其 `rpc_address` 是否为实际内网 IP，以及节点间 `9001/TCP` 是否可达：

```bash
procmesh --server 10.0.0.11:9000 node list
nc -vz 10.0.0.12 9001
```

Agent 间 RPC 在集群初始化后使用 mTLS。如果证书或集群身份不一致，不要手工复制单个证书文件，应通过正常的加入流程恢复节点。

### 11.9 Raft 操作返回 quorum 或 unavailable

确认多数 voter 在线且 `9002/TCP` 双向可达。三 voter 集群至少需要两个 voter 在线。Gossip 显示 `ALIVE` 不等于 Raft 一定具有 quorum，两者使用不同端口和一致性机制。

### 11.10 停止 Agent 与停止业务进程的区别

```bash
sudo systemctl stop procmesh-agent
```

该操作只停止 Agent，shim 保护的业务进程应继续运行。计划下线节点时，应先通过 `procmesh process stop` 停止该节点的业务进程，再处理 Agent 和节点成员关系。

## 12. 重要数据和备份

Linux 默认数据根目录为 `/var/lib/procmesh`，包含：

```text
/var/lib/procmesh/store.db     # 本机进程配置、状态、审计等
/var/lib/procmesh/logs/        # 默认业务日志
/var/lib/procmesh/runtime/     # 运行时信息
/var/lib/procmesh/shim/        # shim socket 和 shim 日志
/var/lib/procmesh/cluster/     # 集群身份、CA/节点证书和元数据
/var/lib/procmesh/raft/        # Raft 数据
/var/lib/procmesh/backup/      # 本地备份数据
```

不要在 Agent 或业务进程仍运行时直接复制、修改或删除这些文件。生产环境应制定定期备份和恢复演练，并严格保护 `cluster/` 中的 CA 私钥及其他敏感材料。

## 13. 快速命令索引

```bash
# Agent
systemctl status procmesh-agent
journalctl -u procmesh-agent -f

# 健康检查
curl -fsS http://10.0.0.11:9000/healthz
curl -fsS http://10.0.0.11:9000/readyz

# 登录和集群
procmesh --server 10.0.0.11:9000 login --user admin
procmesh --server 10.0.0.11:9000 node list
procmesh --server 10.0.0.11:9000 node token create --ttl 30m --uses 1

# 本机进程
procmesh --server 10.0.0.11:9000 process list
procmesh --server 10.0.0.11:9000 process start <NAME>
procmesh --server 10.0.0.11:9000 process stop <NAME>
procmesh --server 10.0.0.11:9000 process restart <NAME>
procmesh --server 10.0.0.11:9000 process logs <NAME> --lines 100 --stream stdout

# 远程节点进程
procmesh --server 10.0.0.11:9000 --node <NODE_ID> process list
procmesh --server 10.0.0.11:9000 --node <NODE_ID> process restart <NAME>
```
