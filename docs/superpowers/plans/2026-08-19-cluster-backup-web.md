# 集群备份与灾备副本 Web Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将现有单 Agent Backup 页升级为集群策略/运行视图，并新增独立 `/disaster-replica` 页面，支持一键生成、预览、应用和复制健康查看。

**Architecture:** Web 只消费 P1/P3 生成的 ConnectRPC；`/backup` 管理 FS/S3 主备份，`/disaster-replica` 管理 Peer 复制。所有异步运行沿用现有 Vue Query polling、`LIVE/STALE/UNKNOWN` 和权限 gating，不把 Peer 混入主备份 sink。

**Tech Stack:** Vue 3 Composition API、Vue Query、Vue Router、ConnectRPC ES、Vitest、Vue Test Utils、i18next、Lucide icons、现有 AppShell/CSS tokens。

**Spec:** `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` §13–§14。

## Global Constraints

- 主备份页只显示 FS/S3；Peer 复制独立页面和独立权限。
- 运行状态 `PARTIAL`、Agent `UNAVAILABLE`、来源 `STALE` 必须显式显示，不得当成功或空目录。
- 每个异步 mutation 显示 pending/success/error，并可只重试失败 Agent/route。
- 新文案同时写入 `web/public/locales/en/common.json` 和 `zh/common.json`，运行 i18n 类型生成/检查。
- 不使用大 hero、卡片套卡片或营销式布局；沿用现有页面密度、表格和状态 badge。
- 按钮使用现有 Lucide 图标，图标按钮提供 tooltip/aria-label；所有表单在窄屏不溢出。

## File Map

- Modify: `web/src/lib/rpc.ts`、`web/src/router.ts`、`web/src/components/AppShell.vue`
- Modify: `web/src/pages/BackupPage.vue`、`web/src/pages/BackupPage.test.ts`
- Create: `web/src/pages/DisasterReplicaPage.vue`、`web/src/pages/DisasterReplicaPage.test.ts`
- Modify: `web/public/locales/en/common.json`、`web/public/locales/zh/common.json`
- Modify: `internal/web/dist` only through `npm run build`/existing embedding flow

### Task 1: Client types, navigation and permissions

- [ ] **Step 1: Write failing client/navigation tests**

断言 `useClusterBackupClient` 和 `useReplicationClient` 暴露生成的 service 方法；路由 `/disaster-replica` 存在；AppShell 仅在相应权限或 cluster admin 下显示入口；无权限直接访问显示现有 denied 状态。

- [ ] **Step 2: Run RED**

Run: `cd web && npm test -- --run src/components/AppShell.test.ts src/pages/DisasterReplicaPage.test.ts`

Expected: FAIL because client aliases, route and page do not exist.

- [ ] **Step 3: Implement client/router/nav**

在 `web/src/lib/rpc.ts` 增加 `ClusterBackupClient` 和 `ReplicationClient` 的 `Pick<Client<...>>`；在 router 添加 `disaster-replica`；在 AppShell 导航中以 `backup.read`/`replication.read` 显示入口，不影响现有 `/backup`。

- [ ] **Step 4: Run tests**

Run: `cd web && npm test -- --run src/components/AppShell.test.ts`。

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/rpc.ts web/src/router.ts web/src/components/AppShell.vue web/src/components/AppShell.test.ts
git commit -m "feat(web): add cluster backup and disaster replica navigation"
```

### Task 2: Cluster backup policy and run workflow

- [ ] **Step 1: Write failing BackupPage tests**

在现有 `BackupPage.test.ts` 增加：创建策略默认 target=`ALL_ADMITTED`；FS/S3 可选但没有 Peer；cron/timezone/retention 表单提交正确；run 详情展示 per-Agent status；`PARTIAL` 显示 warning 和只重试失败按钮；FS 显示主机丢失风险提示。

```ts
expect(clusterBackupClient.createPolicy).toHaveBeenCalledWith(expect.objectContaining({
  policy: expect.objectContaining({ targetSelector: "ALL_ADMITTED", sink: "fs" }),
}));
expect(wrapper.text()).toContain("PARTIAL");
expect(wrapper.find('[data-action="retry-failed"]').exists()).toBe(true);
```

- [ ] **Step 2: Run RED**

Run: `cd web && npm test -- --run src/pages/BackupPage.test.ts`

Expected: FAIL because page still only creates one local snapshot and does not expose policy/run methods.

- [ ] **Step 3: Implement policy/run UI**

保留现有本地 snapshot 列表和 restore 入口；新增策略表格、创建/编辑 dialog、run list/detail drawer。使用 `useQuery` 查询 policies/runs，`useMutation` 创建、启停、start、retry；polling 只在 `PENDING/RUNNING` 时继续。状态列展示 success/failed/unavailable counts 和 last updated。

- [ ] **Step 4: Run tests and build**

Run: `cd web && npm test -- --run src/pages/BackupPage.test.ts`，再运行 `npm run build`。

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/BackupPage.vue web/src/pages/BackupPage.test.ts
git commit -m "feat(web): manage cluster backup policies and runs"
```

### Task 3: Disaster replica generation and topology preview

- [ ] **Step 1: Write failing page tests**

测试页面加载 topology/policies；“生成整个集群副本配置”打开预览；预览显示 replica factor、route 表、warning、inbound load；N=1 warning；应用需要确认并刷新 policy revision；已有人工 route 显示替换确认；权限不足禁用写操作。

- [ ] **Step 2: Run RED**

Run: `cd web && npm test -- --run src/pages/DisasterReplicaPage.test.ts`

Expected: FAIL because page and client methods are absent.

- [ ] **Step 3: Implement page**

新建 `DisasterReplicaPage.vue`：概览指标、route table、policy form、preview dialog、run detail、retry failed route、verify replica、recoverable snapshots。Preview/apply 使用 draft revision/hash；不把 Peer 作为 `/backup` sink；route table 支持 source/targets、health、last success、lag、freshness。

- [ ] **Step 4: Run tests and accessibility checks**

Run: `cd web && npm test -- --run src/pages/DisasterReplicaPage.test.ts`；检查 dialog focus、按钮 aria-label、表格窄屏滚动和状态颜色。

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/DisasterReplicaPage.vue web/src/pages/DisasterReplicaPage.test.ts
git commit -m "feat(web): add disaster replica topology and health page"
```

### Task 4: i18n, freshness and embedded Web

- [ ] **Step 1: Write failing i18n assertions**

断言新增 key 在 en/zh 同时存在；页面不出现硬编码主文案；`STALE` banner、`PARTIAL`、`UNAVAILABLE`、FS host-loss warning 有本地化文本。

- [ ] **Step 2: Run RED**

Run: `cd web && npm run i18n:check`

Expected: FAIL because new keys are missing or type map is stale。

- [ ] **Step 3: Add locale copy and rebuild**

添加策略、运行、拓扑、预览、保留、恢复和错误文案；运行现有 i18n type generation/check，再执行 `npm run build` 更新嵌入静态资源。

- [ ] **Step 4: Run full Web suite**

Run: `cd web && npm run i18n:check && npm test -- --run && npm run build`。

- [ ] **Step 5: Commit**

```bash
git add web/public/locales web/src web/src/gen internal/web/dist
git commit -m "feat(web): localize cluster backup and disaster replica workflows"
```

## P4 验收

使用三 Agent API fixture：用户可以创建 FS/S3 集群策略、查看 partial run、只重试失败节点；灾备页面可以一键生成、预览 warning、应用 routes、查看 lag 并验证副本；Peer 不再出现在主备份 sink 下拉框。
