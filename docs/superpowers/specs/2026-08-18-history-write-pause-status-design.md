# 历史指标停写状态与节点提示设计

日期：2026-08-18  
状态：待实现  
范围：统一磁盘紧急保护配置、历史指标停写行为、节点状态传播与节点详情页提示。

## 1. 背景

Agent 已支持以下本机配置：

```yaml
disk:
  emergency_percent: 95
  emergency_stop_writes: true
```

进程日志保护使用该配置，但历史指标记录器仍将 `95` 写死。节点详情页只能看到取整后的磁盘使用率，无法知道历史写入是否已暂停，也无法显示该节点实际配置的紧急水位。

这会造成三个问题：

1. 自定义 `emergency_percent` 不影响历史指标停写。
2. `emergency_stop_writes: false` 时，历史指标仍会在 95% 停写。
3. 历史图表停止更新时，页面没有说明原因或恢复方式。

## 2. 目标

1. 历史指标停写与节点的磁盘紧急保护配置保持一致。
2. 任意 Agent 查看本地或远程节点时，都能获得该节点真实的停写状态和阈值。
3. 节点详情页在停写期间明确说明影响、原因与恢复条件。
4. 保持混合版本集群兼容：旧节点不提供新字段时，不显示错误提示。

非目标：运行时热加载 `agent.yaml`；在 Web 中编辑磁盘保护配置；改变日志清理或进程日志停写的现有策略。

## 3. 行为合同

历史指标写入暂停的唯一判定为：

```text
emergency_stop_writes == true
AND disk_used_percent > emergency_percent
```

边界语义与磁盘保护配置设计一致：等于 `emergency_percent` 不暂停，严格超过才暂停。

当 `emergency_stop_writes` 为 `false` 时，无论磁盘使用率多高，历史指标记录器都继续尝试写入。SQLite 或文件系统自身返回的写入错误仍按原错误路径处理。

当停写条件解除后，下一次采样周期自动恢复历史指标写入，不需要重启 Agent。历史缺口不回填。

## 4. 后端设计

### 4.1 指标记录器

`metrics.Recorder` 不再拥有固定的 95% 规则。Agent 在创建记录器时注入一个基于已加载 `logmgr.Policy` 的停写判定函数。

记录器每次准备写入分钟样本时调用该函数：

- 返回 `true`：跳过 raw/downsample 写入，仍执行现有过期数据清理，并返回 `DEGRADED`。
- 返回 `false`：按现有流程写入。

判定函数为空时不主动暂停写入，避免 `metrics` 包重新维护一套默认磁盘策略；生产 Agent 必须显式注入。

### 4.2 单一判定函数

Agent 层提供一个小型纯函数，以 `logmgr.Policy` 和精确的浮点磁盘使用率为输入。指标记录器与节点摘要共用该函数，避免实际行为与上报状态分叉。

该函数遵守第 3 节合同，并直接使用启动时加载的 `cfg.Disk`。本阶段不支持配置热加载，因此整个 Agent 生命周期内阈值稳定。

### 4.3 节点摘要与 API

在内部 `cluster.ResourceSummary` 和 protobuf `ResourceSummary` 中追加：

```text
bool history_writes_paused
int32 history_pause_percent
```

本机 `liveSource` 使用未取整的磁盘值计算 `history_writes_paused`，再将展示用磁盘百分比按现有方式取整。`history_pause_percent` 始终填入该节点配置的 `emergency_percent`。

字段通过现有 Gossip NodeSummary 传播，并由 Node API 原样转换，因此从任意 Agent 查看远程节点时仍显示远程节点自己的状态和阈值。

protobuf 只追加新字段，不复用字段号。旧节点缺少字段时解析为 `false` 和 `0`；前端只在 `history_writes_paused=true` 且阈值有效时显示提示。

## 5. 前端设计

节点详情页在“历史”区块标题下、图表上方显示非阻塞警告条。显示条件只读取后端的 `historyWritesPaused`，不使用取整后的 `diskPercent` 二次推断。

警告内容包括：

- 标题：`历史指标写入已暂停`
- 当前磁盘使用率
- 当前节点配置的紧急水位
- 影响：图表仅显示暂停前的数据，缺口不会回填
- 恢复路径：释放空间至紧急水位及以下，系统会自动恢复写入

视觉与交互要求：

- 使用现有 Lucide `TriangleAlert` 图标和 `--color-stale` / `--color-stale-fg` 语义色。
- 使用图标、标题和正文共同表达状态，不只依赖颜色。
- 容器使用 `role="status"` 和 `aria-live="polite"`，不抢占焦点。
- 当前值与阈值使用 tabular figures；长文案在窄屏自然换行，不产生横向滚动。
- 提示不提供按钮；恢复操作发生在系统外部，不伪造不可执行的页面操作。

中英文文案均放入 `common` 命名空间的 `nodeDetail` 下，不在组件中写可见字面量。

## 6. 数据流

```text
agent.yaml disk policy
        |
        +--> history pause predicate --> metrics.Recorder write decision
        |
        +--> liveSource + exact disk usage
                    |
                    v
          cluster.ResourceSummary
                    |
               Gossip state
                    |
             Node API protobuf
                    |
             NodeDetailPage banner
```

## 7. 测试

后端测试：

1. 自定义 `emergency_percent` 生效，严格超过阈值才暂停。
2. 等于阈值时继续写入。
3. `emergency_stop_writes=false` 时超过阈值仍写入。
4. 条件解除后下一周期恢复写入。
5. `liveSource` 使用精确值计算状态，并携带配置阈值。
6. Cluster JSON 编解码和 Node API 保留新增字段。

前端测试：

1. `historyWritesPaused=true` 时显示动态当前值、阈值、影响与恢复文案。
2. 暂停状态为 false 时不显示。
3. 旧节点字段缺失时不显示。
4. 中英文文案完整，i18n 检查通过。

浏览器验证：

- 桌面与窄屏页面无重叠、截断或横向滚动。
- 警告条位于历史图表之前，当前磁盘数据与提示内容一致。
- Accessibility tree 中提示为可读的状态区域。

## 8. 兼容性与风险

- 新 Agent 查看旧节点：新字段为默认值，不显示提示；这是未知能力，不误报为暂停。
- 旧 Agent 查看新节点：JSON/protobuf 的新增字段被忽略，不影响现有节点列表和详情。
- 页面显示的整数磁盘百分比可能因取整看起来等于阈值，但停写状态基于后端精确值。例如精确值 95.4%、阈值 95% 时，页面可显示当前 95% 且明确说明已超过阈值；状态以 `history_writes_paused` 为准。
- 配置只在 Agent 启动时加载，修改 YAML 后需按现有约定重启 Agent 才生效。
