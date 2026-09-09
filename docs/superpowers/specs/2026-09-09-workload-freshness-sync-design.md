# ProcMesh Workload 新鲜度同步设计

日期：2026-09-09

## 1. 问题与根因

成员元数据通过 memberlist `NodeMeta` 频繁更新，但进程摘要因 512 字节限制不在
`NodeMeta` 中，只依赖低频、随机的 push/pull `LocalState`。因此节点本身持续为实时，
其进程摘要仍可能超过新鲜度窗口并周期性显示为 `STALE`。

## 2. 目标与不变量

- Process Spec、Runtime、Logs 和进程摘要始终由 Owner Agent 产出并负责权威性。
- Web/API 请求只读观察节点本地缓存，不在请求链路同步访问远端。
- 远程读取失败时保留最后一份完整快照并标记非实时，不能发布空数据。
- Gossip 不承载配置写入或事务；Raft 不保存进程 Runtime 或摘要。
- Observer 使用自己的接收时间计算新鲜度，不信任 Owner 墙钟。

## 3. 同步协议

Owner 为每次 Agent 启动生成独立 `workload_epoch`，并维护：

- `workload_version`：完整、规范化进程摘要内容变化时递增。
- `workload_observation_seq`：每次完整读取成功时递增；读取任一 spec/instance 失败均不递增。
- 最后一份完整成功快照：读取失败期间继续保留，时间戳不刷新。

`NodeMeta` 只携带同步协议版本和上述三个轻量字段。Observer 的处理规则：

1. epoch/version 与缓存一致且 observation_seq 前进：用 Observer 当前时间刷新整份缓存的验证时间。
2. epoch 或 version 变化：异步通过内部 mTLS `WorkloadSummaryService` 从 Owner 拉取完整快照。
3. RPC 快照只有在 node/epoch/version 仍与最新成员视图一致，且 observation_seq 更新时才原子替换。
4. `LocalState` 继续携带完整摘要，用于首次加入和反熵；旧 observation_seq 不能覆盖更新缓存。

内部 RPC 仅挂载在 Agent mTLS 监听器上，校验对端证书集群身份，不暴露到 Web 监听器。
响应按最多 256 行分块流式传输，客户端在完整接收且所有分块版本一致后才提交缓存。

## 4. 故障与并发语义

- 每节点最多一个待处理抓取；全局使用固定 worker 池和有界队列。
- 同一目标失败后按 1、2、4、8、16、30 秒进行有上限指数退避。
- epoch/version 变化会隔离旧失败并立即处理新目标；在途旧响应必须丢弃。
- 失败保留旧快照：有旧数据时为 `STALE/FETCH_FAILED`，无快照时为 `UNKNOWN/FETCH_FAILED`。
- Owner 非 `ALIVE` 时最后已知数据为 `STALE/OWNER_NOT_ALIVE`。
- 目标变化但尚未完成抓取时为 `STALE/SYNC_PENDING`。
- 验证时间超过进程新鲜度窗口时为 `STALE/EXPIRED`。
- 旧版本 Agent 没有 hint 时继续使用原有进程行时间戳，原因为 `LEGACY`。

空快照是合法的完整结果，必须原子清除旧进程列表；RPC/Store 错误不是空快照。

## 5. API 与可观测性

`Node` API 显式返回：

- `workload_freshness`
- `workload_last_verified_unix_ms`
- `workload_freshness_reason`

Web 优先采用服务端 workload 新鲜度覆盖进程行，字段缺失时保留旧客户端的时间戳算法，
以兼容滚动升级。

`/metrics` 暴露抓取 success/error/discarded 计数、最后抓取耗时、队列深度、当前失败节点数
和最旧缓存年龄。日志只记录首次进入抓取失败和完成新版本同步的状态转换，不记录原始错误、
凭据或敏感路径。

## 6. 验证范围

回归测试覆盖相同 hint 刷新、重复/旧 hint 不刷新、版本变化抓取、空快照、乱序响应、epoch
隔离、失败保留、指数退避、源时钟偏差、NodeMeta 字节上限、mTLS 鉴权与分块，以及 Web
服务端新鲜度覆盖。跨组件测试使用真实 mTLS RPC 验证 Owner 变更最终进入 Observer 本地缓存。
