# 磁盘保护可配置设计

日期：2026-08-14  
状态：待实现  
范围：本机 `procmesh-agent` 磁盘水位保护。不改集群、不改进程 `log` 轮转策略。

## 1. 背景

PRD §58 与架构 spec §12 将磁盘保护定为三档固定百分比：

- \>85% 告警
- \>90% 积极删除旧进程日志
- \>95% 停止新日志写入（stdout/stderr → `/dev/null`），保住 store / journal / audit

实现把这些阈值写死在 `internal/logmgr`。macOS APFS 常报 90%+ 占用但实际仍有十余 GiB 空闲，导致本机开发日志被每秒清掉或丢到 `/dev/null`。PRD 未要求阈值可配，也未要求可关；本次作为 Agent 本机配置补齐。

## 2. 目标

1. 三档百分比可在 `agent.yaml` 配置。
2. 两个独立开关：是否自动删日志、是否在紧急水位停写。
3. 未配置时：百分比仍为 85 / 90 / 95；**默认不自动删除日志**；**默认仍开启紧急停写**。

非目标：把 `data-dir` / `listen` 迁入 YAML；Web / Connect 热更新这些项；按剩余字节判定。

## 3. 配置文件

### 3.1 路径

- `--config PATH`：显式指定。文件必须存在且能解析，否则 Agent 启动失败（退出码 2）。
- 未传 `--config`：
  - Linux：`/etc/procmesh/agent.yaml`
  - macOS：`$HOME/.config/procmesh/agent.yaml`
  - 默认路径文件不存在：使用内置默认，不报错。
  - 默认路径文件存在但非法：启动失败。

本阶段 YAML **只解析磁盘保护段**。未知顶层键忽略。其他 Agent 旗标（`--data-dir`、`--listen` 等）保持不变。

### 3.2 字段

```yaml
disk:
  warn_percent: 85
  cleanup_percent: 90
  emergency_percent: 95
  auto_delete: false
  emergency_stop_writes: true
```

| 字段 | 类型 | 默认 | 含义 |
|------|------|------|------|
| `warn_percent` | 整数 | 85 | 超过则告警（本地日志 + audit）。不删文件、不停写。 |
| `cleanup_percent` | 整数 | 90 | 仅当 `auto_delete: true` 时，超过则删除最旧进程日志，直到占用 ≤ `warn_percent` 或没有可删日志。 |
| `emergency_percent` | 整数 | 95 | 仅当 `emergency_stop_writes: true` 时，超过则新进程 stdout/stderr 使用 `/dev/null`。 |
| `auto_delete` | 布尔 | **false** | `false`：永不因磁盘水位删除进程日志。进程 `log` 轮转（max_size/files/age）仍生效。 |
| `emergency_stop_writes` | 布尔 | **true** | `false`：即使超过 `emergency_percent` 也继续写日志文件。 |

省略整个 `disk:` 或其中某字段：该字段用默认值。

### 3.3 校验

启动时校验，失败则拒绝启动：

- 三个百分比均为整数，范围 `1..100`。
- 必须 `warn_percent < cleanup_percent < emergency_percent`。
- 两个开关必须是布尔。

占用判定仍只用 Statfs 百分比，与现实现相同：`used > N` 触发该档。不引入剩余字节下限。

## 4. 运行时行为

`logmgr.Manager` 持有上述配置。Agent 每秒 `Protect` 一次：

| 条件 | `auto_delete` | `emergency_stop_writes` | 行为 |
|------|---------------|-------------------------|------|
| used ≤ warn | 任意 | 任意 | OK |
| warn < used ≤ cleanup | 任意 | 任意 | Warn：audit，不删、不停写 |
| cleanup < used ≤ emergency | false | 任意 | Warn：不删 |
| cleanup < used ≤ emergency | true | 任意 | Cleanup：删最旧日志直到 used ≤ warn 或无文件 |
| used > emergency | 任意 | false | 不低于 Cleanup 档（是否删只看 `auto_delete`）；**不停写** |
| used > emergency | 任意 | true | Emergency：`WritesAllowed()==false`，新 `start` 走 `/dev/null`；若 `auto_delete` 仍会删旧日志 |

解除停写滞后保持现状：进入 Emergency 后，占用回落到 `cleanup_percent`（含）及以下才恢复写入。

`RotateLogs`（按进程 `log` 策略轮转）不受这两个开关影响。

## 5. 包与接口

- `internal/agentcfg`（或等价小包）：读 YAML、填默认、校验。不要让 `logmgr` 解析 YAML。
- `logmgr.Policy`：五个字段；`Protect` / `WritesAllowed` / `classify` 使用 Policy，不再读包级常量作唯一来源。包级 85/90/95 仅作为默认值。
- `agent.Options` 增加 `ConfigPath string`；`Run` 加载后把 Policy 交给 `logmgr.Manager`。
- `cmd/procmesh-agent` 增加 `--config`。

测试：

- 缺文件 / 默认路径缺失 → 默认 Policy。
- 显式 `--config` 缺失 → 错误。
- 非法顺序或越界 → 错误。
- `auto_delete: false` 且 used=91 → 不删测试目录中的日志。
- `emergency_stop_writes: false` 且 used=96 → `WritesAllowed()==true`。
- 默认 Policy：91% 不删；96% 且停写开启 → 不允许写。

## 6. 文档

更新架构 spec §12 与 PRD 不对账的说明：V1.0 阈值可配；默认不自动删除；默认仍紧急停写。本设计文档是实现合同。

## 7. 刻意不做

- CLI `procmesh` 子命令改磁盘配置。
- 运行中热加载 YAML。
- 按剩余字节或 inode 判定。
- 关闭进程级 log 轮转。
