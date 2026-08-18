# 进程日志目录可配置设计

日期：2026-08-18  
状态：待实现  
范围：为每个 Process Spec 增加日志目录与 stderr 合并开关。覆盖路径解析、校验文案、启动写入、读日志、轮转、磁盘自动删除、pending 提示。不改 shim 协议。

## 1. 背景

进程 stdout/stderr 目前固定写在：

```text
<data-dir>/logs/<process_id>/<instance_id>/stdout.log
<data-dir>/logs/<process_id>/<instance_id>/stderr.log
```

`instance_id` 为 `{process_id}:{ordinal}`。`ProcessSpec.log` 已有轮转字段（`max_size` / `max_files` / `max_age_seconds` / `compress`），没有路径。Start、轮转、tail / stream / download 都通过 `logmgr.InstancePaths` 拼这条固定路径。

Operator 需要把业务日志放到例如 `/var/log/myapp`，并可选把 stderr 合并进 stdout。

## 2. 目标

1. 每个进程可配置日志目录；空则保持现有默认路径（兼容已落地的文件）。
2. 可把 stderr 重定向到 stdout，此时只写 `stdout.log`。
3. 自定义目录按 instance ordinal 分子目录，避免多实例互相覆盖。
4. 改目录或重定向不自动重启；重启前读日志仍跟当前这次启动在写的文件，并提示「重启后生效」。
5. 非法路径在保存时拒绝，错误消息必须让用户看出是**日志路径配置**错了。

非目标：配置完整文件名；改默认布局；运行中 reopen 换路径；自动搬家或删除旧日志；shim 新增 RPC。

## 3. 配置

在现有 `LogPolicy` 增加两个字段：

```yaml
log:
  directory: /var/log/myapp   # 空 = 默认 <data-dir>/logs/...
  redirect_stderr: false      # true = stderr 写入 stdout.log
  max_size: 104857600
  max_files: 10
  max_age_seconds: 604800
  compress: true
```

| 字段 | 类型 | 默认 | 含义 |
|------|------|------|------|
| `directory` | string | 空 | 该进程日志根目录。必须是绝对路径，或空。 |
| `redirect_stderr` | bool | false | true 时 stdout 与 stderr 都追加到 `stdout.log`，不创建 `stderr.log`。 |

Protobuf：

```text
message LogPolicy {
  int64 max_size = 1;
  int32 max_files = 2;
  int64 max_age_seconds = 3;
  bool compress = 4;
  string directory = 5;
  bool redirect_stderr = 6;
}
```

内部 `process.LogPolicy`、CLI/localhttp DTO、Web 表单同步这两个字段。`WithDefaults` 不改 `directory` / `redirect_stderr`（空和 false 就是默认）。

## 4. 路径解析

单一函数（建议 `logmgr.Resolve` 或 `process.LogPaths`），Start / 轮转 / tail / stream / download 共用。输入：data-dir layout、`LogPolicy`、`process_id`、`instance_id`、`ordinal`。

| 条件 | stdout | stderr |
|------|--------|--------|
| `directory` 为空 | `<data-dir>/logs/<process_id>/<instance_id>/stdout.log` | 同目录 `stderr.log` |
| `directory` 非空 | `<directory>/<ordinal>/stdout.log` | 同目录 `stderr.log` |
| `redirect_stderr: true` | 同上（stdout 规则不变） | **写入时**等于 stdout 路径 |

`ordinal` 是 instance 序号（0, 1, …），不是带冒号的 `instance_id`。默认路径故意继续用 `instance_id`，避免已有文件位置变化。

`redirect_stderr` **只影响写入**。读 `stream=stderr` 仍解析 `stderr.log` 那条路径（见 §6.2）。

### 4.1 Effective spec

解析读路径和轮转目标时用 **effective `log`**：

1. instance 的 `active_revision > 0`，且能读到该 revision 的完整 Spec → 用那份 `log`。
2. 否则（从未启动、revision 丢失）→ 用最新 Spec 的 `log`。

启动（下一次 Start）用 **最新 Spec** 的 `log`。Start 成功后 `active_revision` 更新为 `latest_revision`，effective 与最新对齐。

## 5. 校验

`ValidateSpec`（Apply / Update / 回滚写入新 revision 前）校验 `log.directory`。`directory == ""` 合法。

先 `filepath.Clean`，再判断。相对路径、`./foo`、`foo/bar`、`C:\...` 一律非法。

拒绝（均返回 `errcode.INVALID`）：

1. 非绝对路径（Clean 后不以 `/` 开头，或为 `"."`）。
2. 等于或落在 data-dir 下这些内部位置：`store.db`、`raft/`、`cluster/`、`runtime/`、`shim/`。
3. 等于或落在这些系统前缀下（路径分量对齐：`/etc` 和 `/etc/foo` 拒绝，`/etcfoo` 允许）：`/etc`、`/usr`、`/bin`、`/sbin`、`/lib`、`/lib64`、`/boot`、`/dev`、`/proc`、`/sys`、`/root`。
4. Clean 后为 `/`。

允许：`/var/log/...`、`/data/...`、`data-dir/logs/...` 及其它未列入的绝对路径。

### 5.1 错误消息

`errcode.E` 的 `Msg` **必须以 `log path: ` 开头**，后面用完整英文句子说明原因。`Error()` 因此为 `INVALID: log path: ...`。Connect `ErrorInfo.message` 用 `err.Error()`，CLI 和 Web 原样展示，禁止吞成泛化的 “invalid spec” / “name”。

固定文案：

| 条件 | Msg |
|------|-----|
| 非绝对路径 | `log path: directory must be an absolute path` |
| 指向 Agent 内部 | `log path: directory must not point at Agent internal data (store.db, raft, cluster, runtime, shim)` |
| 系统前缀 | `log path: directory is not allowed under /etc`（前缀按实际命中项替换） |
| Clean 后为 `/` | `log path: directory must not be /` |

Web 配置表单保存失败时展示这条 message，并让「日志目录」字段进入错误态。

## 6. 运行时

### 6.1 启动

Reconcile Start：

1. 用最新 Spec 解析路径。
2. 磁盘紧急停写已触发（`WritesAllowed()==false`）→ stdout/stderr 都改 `/dev/null`（与现行为相同，自定义目录也如此）。
3. 否则 `logmgr.Prepare`：`redirect_stderr` 时只 Prepare stdout 路径。
4. 给 shim 的 `StartRequest`：`redirect_stderr` 时 `stdout_path` 与 `stderr_path` 相同（shim 已支持同一文件描述符）。
5. 目录不存在则 `MkdirAll`，权限与现有日志目录一致（`0750` 目录、`0640` 文件）。
6. 创建或打开失败：本次 Start 失败，走 BACKOFF，不改 Spec。
7. Start 成功：`active_revision = latest_revision`。

不新增 shim 字段，不实现运行中 reopen。

### 6.2 读日志

`TailLogs` / `StreamLogs` / `DownloadLogs` 请求字段不变。

- `stream` 缺省或 `stdout` → 读 effective stdout 路径。
- `stream=stderr` → 读 effective **stderr 路径**（`{dir}/stderr.log`），即使 `redirect_stderr==true`。文件不存在当作空，不当错误。
- 因此重定向生效后 stderr 页签为空；内容在 stdout。

### 6.3 轮转

对每个 instance，对 effective 路径上**实际存在**的文件做现有 size / files / age / compress。`redirect_stderr` 且没有 `stderr.log` 时只轮转 stdout。刚改目录尚未重启时，轮转的是旧文件。

自定义目录下的文件同样使用该进程 `LogPolicy` 的轮转字段。

### 6.4 改目录不搬家

旧文件留在原处，不 copy、不删除。

### 6.5 pending

`Instance` proto 增加只读字段（当前 `Instance` 用到 11，本字段用 12）：

```text
bool log_path_pending = 12;
```

计算：用最新 Spec 的 `(directory, redirect_stderr)` 与 effective（active revision）的这两项比较。不一致则为 true。instance 无 `active_revision` 时为 false（下次 Start 直接用最新 Spec）。

不写入 Spec，不进 revision 历史，不进 SQLite instance 行。

页面级横幅：任一 instance 为 true 即显示。

## 7. 磁盘保护

水位与开关仍是 Agent `disk:` 配置（`warn_percent` / `cleanup_percent` / `emergency_percent` / `auto_delete` / `emergency_stop_writes`），不是写死的 85/90/95。

| 机制 | 自定义目录 |
|------|------------|
| 超过 **emergency 阈值** 且 `emergency_stop_writes` | 下次 Start 写 `/dev/null`（与默认路径相同） |
| 进程自己的轮转 | 始终作用于 effective 路径 |
| 超过 **cleanup 阈值** 且 `auto_delete` | 见下 |

自动删除扫描：

1. 现有 `<data-dir>/logs/` 整树（行为不变）。
2. 每个进程最新 Spec 的非空 `log.directory`：仅当该目录与 data-dir **同一分区** 时，才扫描并删除其下本 Agent 管理的日志文件。

同一分区：比较 data-dir 与目标目录的设备号（`stat` 的 `Dev`）。目录尚不存在则沿父目录向上找到第一个存在的路径再比。无法判定设备时 **不删除**（保守）。

只删除本进程布局下的文件，不删用户放在同一目录里的其它文件：

```text
{directory}/{ordinal}/stdout.log
{directory}/{ordinal}/stderr.log
以及现有轮转归档命名（.1 / .1.gz / .2 …）
```

不同分区的自定义目录：不参与自动删除，只靠该进程轮转。

删除去重：同一文件不要删两次。

## 8. API / CLI / Web

- Apply / Update / YAML 可写 `log.directory`、`log.redirect_stderr`。
- Get / List 的 `ProcessSpec.log` 回显这两项；`Instance.log_path_pending` 只在运行时视图出现。
- 读日志 RPC 签名不变。
- CLI `process get`：任一 instance pending 时打印  
  `log path pending: restart to apply`。
- CLI `procmesh logs` 与 API 一致，不新子命令。

Web：

- 进程配置「日志」节：目录输入框、`redirect_stderr` 开关。
- 配置页保存后、进程详情页：pending 时告警条「日志路径将在重启后生效」，不自动重启。
- 日志面板 stderr 页签保持可点，仍请求 `stream=stderr`（空就空）。按状态提示：
  - effective 已重定向：`stderr 已合并到 stdout`
  - 最新 Spec 已打开重定向但 pending：`重启后 stderr 将合并到 stdout`

文案走 i18n（`en` / `zh`）。

## 9. 测试

必须覆盖：

1. 未配 `directory`：路径与现网一致。
2. 自定义目录 + 两实例：`{dir}/0/stdout.log`、`{dir}/1/stdout.log`。
3. `redirect_stderr`：只创建/写入 stdout；stderr tail 为空。
4. 改 `directory` 后、重启前：tail 仍是旧文件，`log_path_pending=true`；重启后切到新路径，pending=false。
5. 相对路径、`/etc/...`、`data-dir/raft/...`、`/var/log/../etc` 保存失败；`err.Error()` 含 `log path:`。
6. 自动删除：同分区自定义目录下的 `{ordinal}/*.log` 可被删；不同分区的自定义目录不被删；自定义目录里与布局无关的文件不被删。
7. 紧急停写：自定义目录下次 Start 也走 `/dev/null`。

## 10. 非目标（再列一次）

- 不改默认路径布局。
- 不自动迁移旧日志。
- 不因改 `log.directory` / `redirect_stderr` 自动重启进程。
- 不把自定义目录的任意文件纳入自动删除。
- 不在 Web 编辑 Agent 磁盘水位。
