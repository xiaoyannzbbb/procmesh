# ProcMesh 部署与集群快速开始

本文面向第一次部署 ProcMesh 的运维人员，目标是完成以下工作：

1. 安装 ProcMesh；
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
| `18680` | TCP/HTTP | Web UI、CLI 和 ConnectRPC API | 管理网、集群节点 |
| `18683` | TCP/mTLS | Agent 间远程进程操作 | 仅集群节点 |
| `18685` | TCP | Raft 控制面 | 仅集群节点 |
| `18689` | TCP + UDP | Gossip 成员发现和状态传播 | 仅集群节点 |

重要安全说明：

- `--insecure-listen` 只是允许 Agent 绑定非回环地址，不会为 `18680` 自动启用 HTTPS。
- 不要把上述端口直接暴露到公网。应使用安全组或防火墙限定来源地址。
- 跨不可信网络管理时，在 `18680` 前配置 HTTPS 反向代理，或通过 VPN/堡垒机访问。
- 节点 IP 应保持稳定。多节点部署要绑定实际内网 IP，不要绑定 `0.0.0.0`，否则节点可能向集群发布不可用于远程访问的地址。

## 3. 最短路径：先在一台机器上跑起来

如果只想快速验证功能，可先执行本节。正式三节点部署请继续阅读第 4 节。

### 3.1 安装 ProcMesh

选择下面任意一种方式。两种方式安装的 Release 都包含 `procmesh`、`procmesh-agent` 和 `procmesh-shim`。

#### 自动安装（Linux，推荐）

安装器会下载最新正式 Release，并使用 Release 提供的 SHA-256 校验值验证压缩包：

```bash
curl -fsSL https://raw.githubusercontent.com/xiaoyannzbbb/procmesh/main/scripts/install.sh | bash
```

安装过程需要交互式终端。安装器会根据 `LC_ALL`、`LC_MESSAGES` 或 `LANG` 自动探测中文或英文，并在开始时询问使用哪种语言，默认选择探测结果；也可以通过 `PROCMESH_LANG=en` 或 `PROCMESH_LANG=zh` 指定默认语言。建议保留默认安装目录 `/usr/local/bin`；如果希望 Agent 作为系统服务运行，请在提示时选择安装 systemd unit，并选择立即启用和启动服务。已有配置、数据目录和 systemd unit 不会被覆盖。

默认监听地址是 `127.0.0.1:18680`。选择非回环地址时，安装器会加入 `--insecure-listen`，但不会启用 HTTPS；必须通过防火墙、HTTPS 反向代理、VPN 或堡垒机限制访问。

创建新的 Agent 配置时，安装器还会探测本机出口网卡 IPv4 和公网 IPv4，供用户选择 `network.advertise_host`。公网地址依次通过 `api.ipify.org`、`ip.sb` 和 `ifconfig.me` 探测，每个来源最多等待 3 秒，全部失败也不会中止安装；默认保持该配置为空。选择地址只改变公布地址，不会扩大 HTTP、Gossip、RPC 或 Raft 的监听范围。

HTTP 使用非回环监听地址时，安装器会询问是否让 Gossip、RPC 和 Raft Control 使用相同的监听主机，默认不启用。启用后应仅在可信集群网络开放 `18689/TCP+UDP`、`18683/TCP` 和 `18685/TCP`；使用 `0.0.0.0` 或 `::` 时必须同时选择可拨号的 `network.advertise_host`。

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

macOS 可使用 `shasum -a 256 -c -` 替代 `sha256sum -c -`。Linux 压缩包还包含默认 `agent.yaml` 和 systemd unit。

### 3.2 启动并检查单节点 Agent

如果自动安装时已经启用 systemd 服务，不需要再前台启动 Agent，直接检查服务和 HTTP 端点：

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

以前台方式启动时，保持该终端运行，另开终端验证：

```bash
curl -fsS http://127.0.0.1:18680/healthz
curl -fsS http://127.0.0.1:18680/readyz
procmesh --server 127.0.0.1:18680 status
```

`healthz` 和 `readyz` 应返回 `ok`，`status` 应输出 `ready` 和当前进程数。

### 3.3 初始化并登录

初始化只能成功执行一次：

```bash
procmesh --server 127.0.0.1:18680 cluster init --admin-user admin
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
procmesh --server 127.0.0.1:18680 login --user admin
```

输入上一条命令返回的密码并回车。CLI 会把会话以 `0600` 权限保存到 `~/.config/procmesh/session`。不要用 `sudo procmesh` 登录后再用普通用户执行命令，否则两个用户读取的会话文件不同。

现在可在浏览器访问 `http://127.0.0.1:18680/`，或直接跳到第 7 节启动进程。浏览器不会复用 CLI 保存的会话，需要在 Web 登录页再次使用管理员账号和密码登录。

## 4. 在每个生产节点安装 Release

在三个节点上分别按照第 3.1 节的任一方式安装 ProcMesh。自动安装时保留默认安装目录 `/usr/local/bin`；在询问是否安装 systemd unit 时选择否，下一节会创建适用于多节点拓扑的配置和 unit。

验证文件和版本兼容性：

```bash
command -v procmesh
command -v procmesh-agent
command -v procmesh-shim
procmesh-agent --help
```

三个节点应安装同一版本的 Release，避免协议版本不一致导致节点拒绝加入。

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
  --listen ${PROCMESH_NODE_IP}:18680 \
  --rpc ${PROCMESH_NODE_IP}:18683 \
  --control ${PROCMESH_NODE_IP}:18685 \
  --gossip ${PROCMESH_NODE_IP}:18689 \
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
curl -fsS http://NODE_IP:18680/healthz
curl -fsS http://NODE_IP:18680/readyz
procmesh --server NODE_IP:18680 status
```

同时确认监听地址不是 `0.0.0.0` 或 `127.0.0.1`：

```bash
sudo ss -lntup | grep -E ':(18680|18683|18685|18689)\b'
```

Agent 尚未初始化集群时，`18683` 的 mTLS RPC 服务可能尚未监听；完成初始化或加入集群后会启动。

## 6. 初始化三节点集群

以下命令均以普通部署用户执行，不需要 `sudo`。

### 6.1 初始化节点 1

```bash
procmesh --server 10.0.0.11:18680 cluster init --admin-user admin
```

安全保存输出中的 `admin_password`，然后登录节点 1：

```bash
procmesh --server 10.0.0.11:18680 login --user admin
```

登录成功后验证：

```bash
procmesh --server 10.0.0.11:18680 node list
```

此时应只有节点 1。

### 6.2 生成加入令牌

为节点 2 和节点 3 创建一个可使用两次、30 分钟后过期的令牌：

```bash
procmesh --server 10.0.0.11:18680 node token create --ttl 30m --uses 2
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
procmesh --server 10.0.0.12:18680 agent join \
  --seed 10.0.0.11:18680 \
  --token '<JOIN_TOKEN>'
```

成功时会输出集群 ID 和种子节点 Gossip 地址。加入过程会在节点 2 保存 CA 证书、节点证书和 Raft/Gossip 元数据。

### 6.4 加入节点 3

在节点 3 上执行：

```bash
procmesh --server 10.0.0.13:18680 agent join \
  --seed 10.0.0.11:18680 \
  --token '<JOIN_TOKEN>'
```

### 6.5 验证节点发现

回到已登录节点 1 的终端：

```bash
procmesh --server 10.0.0.11:18680 node list
```

每行依次包含：

```text
node_id  hostname  state  protocol_version  api_address  gossip_address  rpc_address
```

确认三个节点均出现、状态为 `ALIVE`，并且地址分别是实际内网 IP。保存节点 2 和节点 3 的 `node_id`，后面提升 Raft 角色和远程部署进程时会用到。

如果新节点没有立即出现，可等待几秒后重试。仍未出现时检查节点间 `18689/TCP` 和 `18689/UDP`。

### 6.6 可选但推荐：组成三 voter Raft

新加入节点默认是 Raft non-voter。三个节点全部在线并稳定后，在节点 1 执行：

```bash
procmesh --server 10.0.0.11:18680 node promote <NODE_2_ID>
procmesh --server 10.0.0.11:18680 node promote <NODE_3_ID>
```

三 voter 集群允许任意一个 voter 故障后继续保持控制面 quorum。不要只部署两个 voter 后长期运行：两个 voter 中任意一个离线都会失去多数派。提升期间确保三个节点及 `18685/TCP` 网络稳定。

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
procmesh --server 10.0.0.11:18680 process apply \
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
procmesh --server 10.0.0.11:18680 process start demo-worker
```

Agent 每秒执行一次状态协调，通常 1 至 2 秒后即可看到 `RUNNING` 和 `HEALTHY`：

```bash
procmesh --server 10.0.0.11:18680 process list
procmesh --server 10.0.0.11:18680 process get demo-worker
procmesh --server 10.0.0.11:18680 process logs demo-worker --lines 20 --stream stdout
```

### 7.3 从节点 1 入口部署到节点 2

CLI 的 `--node` 参数可指定目标 Owner 节点，值可以是 `node_id` 或唯一主机名。以下示例复用已登录节点 1 的会话，把进程部署到节点 2：

```bash
procmesh --server 10.0.0.11:18680 --node <NODE_2_ID> process apply \
  --file demo-worker.yaml \
  --expected-revision 0 \
  --comment 'deploy to node 2'

procmesh --server 10.0.0.11:18680 --node <NODE_2_ID> process start demo-worker
procmesh --server 10.0.0.11:18680 --node <NODE_2_ID> process list
```

远程写操作通过节点间 `18683/TCP` mTLS RPC 转发到 Owner Agent。若本地操作成功而远程操作失败，优先检查节点间 `18683/TCP`、节点状态和 `rpc_address`。

### 7.4 常用进程操作

```bash
# 查看详情和当前 revision
procmesh --server 10.0.0.11:18680 process get demo-worker

# 停止、启动、重启
procmesh --server 10.0.0.11:18680 process stop demo-worker
procmesh --server 10.0.0.11:18680 process start demo-worker
procmesh --server 10.0.0.11:18680 process restart demo-worker

# 强制终止并将期望状态设为 STOPPED
procmesh --server 10.0.0.11:18680 process kill demo-worker

# 读取 stdout 或 stderr
procmesh --server 10.0.0.11:18680 process logs demo-worker --lines 100 --stream stdout
procmesh --server 10.0.0.11:18680 process logs demo-worker --lines 100 --stream stderr

# 查看配置历史
procmesh --server 10.0.0.11:18680 process history demo-worker
```

进程进入 `FATAL` 后，需要先清除失败状态，再重新启动：

```bash
procmesh --server 10.0.0.11:18680 process reset-failure demo-worker
procmesh --server 10.0.0.11:18680 process start demo-worker
```

## 8. 更新和回滚进程配置

每次修改配置都使用乐观锁。先获取当前版本：

```bash
procmesh --server 10.0.0.11:18680 process get demo-worker
```

假设输出中 `revision` 为 `1`，修改 YAML 后执行：

```bash
procmesh --server 10.0.0.11:18680 process apply \
  --file demo-worker.yaml \
  --expected-revision 1 \
  --comment 'increase instances'
```

如果其他人已更新配置，命令会返回版本冲突。重新读取最新配置和 revision，确认差异后再提交，不要盲目覆盖。

回滚同样会生成一个新 revision。假设当前最新版本为 `3`，要恢复版本 `1`：

```bash
procmesh --server 10.0.0.11:18680 process rollback demo-worker \
  --to 1 \
  --expected-revision 3 \
  --comment 'rollback bad configuration'
```

部分运行参数需要重启进程才能应用。更新后用 `process get` 检查提示，并在合适的维护窗口执行：

```bash
procmesh --server 10.0.0.11:18680 process restart demo-worker
```

删除前必须先停止进程，等待 `process get` 显示实例已进入 `STOPPED`，再使用最新 revision 删除：

```bash
procmesh --server 10.0.0.11:18680 process stop demo-worker
procmesh --server 10.0.0.11:18680 process get demo-worker
procmesh --server 10.0.0.11:18680 process delete demo-worker --expected-revision <LATEST_REVISION>
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
curl -fsS http://10.0.0.11:18680/healthz
curl -fsS http://10.0.0.11:18680/readyz

# 2. CLI 可认证访问
procmesh --server 10.0.0.11:18680 status

# 3. 三个节点均为 ALIVE，地址均为实际内网 IP
procmesh --server 10.0.0.11:18680 node list

# 4. 本机进程可运行
procmesh --server 10.0.0.11:18680 process list

# 5. 远程节点可读取并操作
procmesh --server 10.0.0.11:18680 --node <NODE_2_ID> process list

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

### 11.2 节点列表出现 `[::]`、`0.0.0.0` 或 `127.0.0.1`

多节点部署时，这些地址通常不能供其他节点访问。如果 HTTP、Gossip、RPC 和 Raft 共用同一个可达主机，可在 `agent.yaml` 中统一配置；各端点会沿用自己的实际监听端口：

```yaml
listen: "[::]:18680"
network:
  advertise_host: "10.0.0.11"
```

`network.advertise_host` 不改变监听地址，并且不能包含端口。使用不同网络或 NAT 端口映射时，应继续分别配置顶层 `advertise`、`gossip.advertise`、`rpc.advertise` 或 `control.advertise`；端点配置优先于共享主机。

也可以把 systemd 中四个监听地址改为本机实际静态内网 IP。修改后执行：

```bash
sudo systemctl daemon-reload
sudo systemctl restart procmesh-agent
```

然后重新检查 `node list`。不要在未确认数据和集群身份的情况下删除 `/var/lib/procmesh` 或重复初始化。

### 11.3 加入失败

依次检查：

1. 节点 2/3 的 `/healthz` 和 `/readyz` 是否正常；
2. 加入节点能否访问种子节点 `18680/TCP`；
3. 令牌是否过期、已用完或已撤销；
4. 节点是否曾使用当前数据目录加入其他集群；
5. 三个节点是否使用兼容的协议版本；
6. 节点间 `18689/TCP+UDP` 和 `18685/TCP` 是否放通。

如果返回 `UNAVAILABLE` 或 `TIMEOUT`，先排除网络或 Raft quorum 问题，然后使用相同的 seed 和 token 重新执行原命令。Agent 会复用本地 `cluster/join.pending.json` 中的加入身份和服务端 Join attempt，不会再次消费 token；该文件包含临时 private key，权限必须保持 `0600`，不要复制或手工修改。确定性的 token、节点身份或协议错误会自动清理 pending attempt；修正问题后重新执行，只有 token 已失效或耗尽时才需创建新 token。

CLI 只有在节点已写入 Raft configuration 且准入状态为 `ADMITTED` 后才报告成功。如果返回 `cluster already initialized`，说明该数据目录已经完成集群身份写入，不应继续重复执行 `agent join`。

加入在远端提交 `join_prepare` 后若因成员写入超时或 Leader 切换而失败，本地尚不会写入 `cluster.json`，只保留可复用的 pending identity；不要删除 pending 文件、数据目录或重复初始化。Leader 会在启动时及此后每 5 秒自动对账 FSM 准入状态与 Raft configuration。若本地已经存在 `cluster.json`，说明远端曾确认成员关系并完成 `ADMITTED`，不应重新执行 Join；按下一节检查成员关系。

Raft advertise 必须是可拨号的 `host:port`，不能使用监听通配地址 `0.0.0.0`、`::` 或端口 `0`。Agent 会在本地发起 Join 前检查，Leader 也会在消费 token 或创建 Join attempt 前重新检查。监听地址可以继续使用 `0.0.0.0:18685`，但此时必须通过 `control.advertise` 或 `network.advertise_host` 提供其他节点实际可达的地址。

包含 protocol 1 节点的集群升级到 protocol 2 时，应先滚动升级 Raft configuration 中全部现存 voter 和 nonvoter，升级期间不要执行 Join。混合版本状态下 Join 会返回 `INCOMPATIBLE_VERSION`；成员版本尚未通过 Gossip 确认时返回 `UNAVAILABLE`。不要通过重建 token 绕过该门控。

`cluster init` 只有在本机 Raft control 完成启动后才返回管理员初始密码。若该阶段返回 `UNAVAILABLE`，本次生成的集群身份与 Raft 临时状态会被撤销，可以在排除监听地址、磁盘权限等问题后重新执行初始化。

### 11.4 Raft 成员关系检查与修复

ProcMesh 同时维护两份成员信息：Raft FSM 中的准入状态是权威期望，Raft configuration 是实际参与控制面复制的成员集合。`cluster membership reconcile` 比较二者，并用与后台周期任务相同的幂等逻辑收敛可安全修复的差异。它不是重新 Join，也不会重建集群、修改业务进程或自动晋升 voter。

通常无需定期手工执行，因为当前 Leader 会自动对账。以下场景适合手工检查：Join 返回 `UNAVAILABLE`/`TIMEOUT`、`node remove` 结果不确定、Leader 切换后成员角色异常，或相关 Prometheus 指标持续非零。

#### 标准操作流程

先执行只读检查：

```bash
procmesh --server 10.0.0.11:18680 cluster membership check
```

确认报告后再触发修复。建议显式指定 UUID；如果请求返回 `TIMEOUT`，使用同一个 `operation_id` 重试：

```bash
procmesh --server 10.0.0.11:18680 \
  --operation-id <UUID> \
  cluster membership reconcile

# 修复后重新读取当前状态
procmesh --server 10.0.0.11:18680 cluster membership check
```

CLI 未指定 `--operation-id` 时会自动生成。`check` 需要集群范围的 `cluster.read` 权限；`reconcile` 需要 `cluster.manage`、有效 quorum，并由当前 Raft Leader 执行。请求可以发送到任一 Agent，入口节点会通过内部 mTLS 转发到 Leader，Leader 会重新鉴权并写入 `cluster.membership.reconcile` 审计事件。

#### 状态与退出码

| `status` | 含义 | 后续操作 |
| --- | --- | --- |
| `CLEAN` | FSM 与 Raft configuration 一致。 | 无需处理，CLI 退出码为 `0`。 |
| `DRIFTED` | 只有可自动修复的差异。 | 执行 `reconcile`；非 `CLEAN` 报告的退出码为 `1`。 |
| `BLOCKED` | 至少存在一个不能安全自动处理的问题。 | 查看逐节点 issue 并人工核对；CLI 退出码为 `1`。 |

RPC、认证或 quorum 错误同样返回退出码 `1`，但错误写到 stderr；参数错误返回 `2`。当 quorum 丢失时，`check` 在本地 configuration 仍可读的情况下会返回 `freshness=STALE`、`has_quorum=false` 和 `leader=false`，而 `reconcile` 返回 `UNAVAILABLE` 且不写入。

报告首先输出以下 `key=value` 字段：

- `status`：整体结果；
- `freshness`：`LIVE` 或 `STALE`；
- `has_quorum`、`leader`：当前控制面可写性；
- `observed_unix_ms`：本次观测时间；
- `repaired`：仅 `reconcile` 返回，本次已解决的可修复 issue 数；
- `issues`：剩余 issue 数。

随后每行 issue 依次为 `node_id`、`kind`、`member_state`、`actual_role`、`repairable`，字段间使用 Tab，缺失值显示为 `-`。报告和审计均不包含 Raft 地址。

#### 修复边界

| issue `kind` | `reconcile` 的行为 |
| --- | --- |
| `MISSING_MEMBER` | FSM 中为 `JOINING`/`ADMITTED`，但 Raft 中缺失：作为 nonvoter 补入。 |
| `ADDRESS_MISMATCH` | node ID 存在但地址不一致：纠正地址，并保留当前 voter/nonvoter 身份。 |
| `JOIN_INCOMPLETE` | Raft 已精确包含该成员，但 FSM 仍为 `JOINING`：推进为 `ADMITTED`。 |
| `REMOVAL_PENDING` | FSM 已为 `REMOVED`/`REVOKED`，但 Raft 中仍存在：重试移除。 |
| `UNEXPECTED_MEMBER` | Raft 中存在、FSM 中完全未知：标记 `BLOCKED`，绝不自动删除。 |
| `INVALID_DESIRED_MEMBER` | FSM 中的活动成员缺少 Raft 地址，或保存了 `0.0.0.0`/`::` 等不可拨号地址：标记 `BLOCKED`，不猜测、覆盖或推进准入状态。 |

同一轮中一个成员失败不会阻塞其它独立成员。即使最终状态为 `BLOCKED`，命令仍可能已经修复其它安全项，因此应同时检查 `repaired` 和剩余 issue。不要为消除 `UNEXPECTED_MEMBER` 或 `INVALID_DESIRED_MEMBER` 而直接编辑 Raft/FSM 文件或删除数据目录；先核对节点身份、集群历史、quorum 和备份，再按事故恢复流程处理。

#### 可观测性

可通过 `/metrics` 观察 `procmesh_raft_membership_reconcile_pending_issues`、`procmesh_raft_membership_reconcile_last_success_unix` 和 `procmesh_raft_membership_reconcile_consecutive_failures`。连续失败只在首次失败和恢复时写日志，避免周期任务反复刷屏。

### 11.5 CLI 返回 authentication required

重新针对当前 `--server` 登录：

```bash
procmesh --server 10.0.0.11:18680 login --user admin
```

默认会话只匹配登录时的 server 地址。使用 `10.0.0.11:18680` 登录后，改用主机名或另一个节点地址时不会自动复用该会话。

### 11.6 创建后进程一直是 STOPPED

`process apply` 只保存配置并创建实例记录。执行：

```bash
procmesh --server 10.0.0.11:18680 process start <NAME>
```

### 11.7 启动进程时报 shim not found

确认 `procmesh-shim` 位于 `PATH` 中或与 `procmesh-agent` 在同一目录。本文 systemd 单元还通过 `--shim-bin /usr/local/bin/procmesh-shim` 显式指定了路径：

```bash
ls -l /usr/local/bin/procmesh-shim
sudo journalctl -u procmesh-agent -n 100 --no-pager
```

### 11.8 进程启动后立即退出或进入 FATAL

检查以下内容：

```bash
procmesh --server 10.0.0.11:18680 process get <NAME>
procmesh --server 10.0.0.11:18680 process logs <NAME> --lines 200 --stream stdout
procmesh --server 10.0.0.11:18680 process logs <NAME> --lines 200 --stream stderr
sudo journalctl -u procmesh-agent -n 200 --no-pager
```

重点确认命令路径、工作目录、运行用户权限、环境变量、端口冲突、cgroup v2 权限和健康检查地址。

### 11.9 远程操作失败，但本地操作正常

检查目标节点是否为 `ALIVE`，其 `rpc_address` 是否为实际内网 IP，以及节点间 `18683/TCP` 是否可达：

```bash
procmesh --server 10.0.0.11:18680 node list
nc -vz 10.0.0.12 18683
```

Agent 间 RPC 在集群初始化后使用 mTLS。如果证书或集群身份不一致，不要手工复制单个证书文件，应通过正常的加入流程恢复节点。

### 11.10 Raft 操作返回 quorum 或 unavailable

确认多数 voter 在线且 `18685/TCP` 双向可达。三 voter 集群至少需要两个 voter 在线。Gossip 显示 `ALIVE` 不等于 Raft 一定具有 quorum，两者使用不同端口和一致性机制。

如果 `cluster membership check` 同时报告一个 `JOINING`/`NON_VOTER` 成员，且该成员历史上使用了 `0.0.0.0:18685` 或 `[::]:18685`，它可能让 Leader 把该地址解释为自己的本地 Raft listener，造成持续切主。包含该修复的版本会拒绝向这类已持久化地址拨号，使 Leader 能恢复稳定，但不会擅自删除成员。恢复步骤如下：

1. 通过 SSH 或其他不依赖 ProcMesh quorum 的通道，先升级一个现有 voter 到包含修复的版本并重启 Agent；业务进程由 shim 继续托管。
2. 等待该节点当选并确认所有存活节点连续看到 `procmesh_cluster_control_quorum 1`。若仍未稳定，再逐个升级其他 voter；每次只重启一个。
3. 使用固定 `operation_id` 显式执行 `node remove <BAD_NODE_ID>`。不要运行 `cluster membership reconcile`，因为不可拨号的活动成员会保持 `BLOCKED`，等待人工核对和删除。
4. 确认 `cluster membership check` 返回 `CLEAN`，并观察 Leader、quorum 和本地业务进程。
5. 再通过更新页滚动升级剩余节点。更新器逐个替换三个二进制文件，等待节点以目标版本恢复 `ALIVE/LIVE` 后才继续，并保留一份旧二进制在 `$data_dir/update/previous/`。

升级本身不会改写已经持久化的 Raft configuration，因此不能跳过第 3 步。不要直接编辑 `raft.db`、删除 Raft 数据目录或重新执行 `cluster init`。

### 11.11 升级后创建加入令牌返回 `admission capability unavailable`

`v0.1.39` 开始，每个 Raft voter 必须持有与集群 CA 匹配的 Admission Capability。旧版本只在初始化节点保留 `cluster/ca.key`；旧集群已经晋升的其他 voter 升级后若当选 Leader，会拒绝创建令牌和签发 Join，但不会停止本地业务进程。

先确认 Raft 仍有 quorum，并在每个 voter 本机检查哪个节点持有 CA 私钥；不要打印、粘贴或写入日志：

```bash
sudo test -s /var/lib/procmesh/cluster/ca.key && echo present || echo missing
```

如果当前 Leader 没有该文件，需要通过受控的加密运维通道，把初始化节点上的 `ca.key` 安装到当前 Leader 的同一目录，权限设为 `0600`。安装前必须在源节点和 Leader 上校验它与各自 `ca.crt` 的公钥一致；不一致时立即停止，不能覆盖。完成后，从当前 Leader 对其余旧 voter 逐个重新执行：

```bash
procmesh --server <LEADER_API> node promote <VOTER_NODE_ID>
```

该操作会通过内部 mTLS capability 流程分发 CA 私钥并把节点标记为 `READY`；已有 voter 的 Raft 身份保持不变。全部 voter 完成后再创建加入令牌。迁移期间不要删除数据目录、重新 `cluster init`，也不要通过日志、审计或聊天传递 CA 私钥。

### 11.12 停止 Agent 与停止业务进程的区别

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
curl -fsS http://10.0.0.11:18680/healthz
curl -fsS http://10.0.0.11:18680/readyz

# 登录和集群
procmesh --server 10.0.0.11:18680 login --user admin
procmesh --server 10.0.0.11:18680 node list
procmesh --server 10.0.0.11:18680 node token create --ttl 30m --uses 1
procmesh --server 10.0.0.11:18680 cluster membership check
procmesh --server 10.0.0.11:18680 --operation-id <UUID> cluster membership reconcile

# 本机进程
procmesh --server 10.0.0.11:18680 process list
procmesh --server 10.0.0.11:18680 process start <NAME>
procmesh --server 10.0.0.11:18680 process stop <NAME>
procmesh --server 10.0.0.11:18680 process restart <NAME>
procmesh --server 10.0.0.11:18680 process logs <NAME> --lines 100 --stream stdout

# 远程节点进程
procmesh --server 10.0.0.11:18680 --node <NODE_ID> process list
procmesh --server 10.0.0.11:18680 --node <NODE_ID> process restart <NAME>
```
