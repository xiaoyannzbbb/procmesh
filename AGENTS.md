# AGENTS.md

本文件适用于整个仓库。开始修改前先阅读与任务直接相关的源码、测试和设计文档；实现与文档不一致时，以当前可执行行为和测试为事实，并在变更中同步修正文档或说明差异。

## 项目边界

ProcMesh 是 Local-First、Agent-Owned、Peer-Managed 的分布式进程管理平台。后端使用 Go，Web 管理端位于 `web/`，采用 Vue 3、TypeScript 和 Vite。主要入口在 `cmd/`，核心实现位于 `internal/`，对外 API 与 Shim 协议位于 `proto/`。

涉及架构或行为变更时，按任务范围阅读：

- 产品与总体架构：`docs/v2-prd/v2-prd.md`、`docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`。
- 后续功能设计：`docs/superpowers/specs/` 中与目标模块同名或主题相关的最新文档。
- 部署与运维行为：`README.md`、`docs/QUICKSTART_ZH.md`。
- Web 国际化：`web/docs/I18N_GUIDE.md`。

## 不变量

- Process Spec、Runtime、Logs 的权威数据属于 Owner Agent；远程写操作必须路由到 Owner，不能在入口节点直接修改副本。
- Gossip 只传播成员信息和摘要，不承载配置变更或事务；Raft 只保存用户、RBAC、准入、策略等控制面强一致数据，不保存进程运行时或日志。
- 配置写入保留 CAS 语义并校验 `expected_revision`；远程副作用保留 `operation_id` 幂等语义。不得用静默覆盖或自动重试破坏冲突反馈。
- Agent 退出、网络分区或对端 `FAILED` 不应杀死本地业务进程，也不能触发在其他节点重建该进程。
- 集群摘要必须保留 `LIVE`、`STALE`、`UNKNOWN` 的新鲜度含义；界面不能把过期或未知数据展示为实时健康。
- 安全边界需要在实际执行写操作的节点重新验证。认证凭据、密钥、完整敏感路径不得进入日志、审计或 API 响应。

## 修改约定

- 行为修复和新功能优先先写可复现的失败测试，再实现最小改动；测试应覆盖成功路径及相关的 CAS、幂等、权限、quorum 或故障语义。
- 保持现有包职责。跨包调用优先依赖窄接口；错误使用 `internal/errcode` 的稳定错误码，并用 `%w` 包装底层原因。
- Process 配置修改通过管理层既有入口执行，实例状态由 reconcile 流程推进；不要绕过业务层直接写 Store。
- 修改 `.proto` 后运行 `make proto`；涉及 Web API 时再运行 `make proto-ts`。不要手工编辑 `*.pb.go`、`*.connect.go` 或 `web/src/gen/`。
- Go 文件提交前运行 `gofmt`。平台特定代码沿用 `_linux.go`、`_darwin.go`、`_other.go` 的现有拆分，并保留非 Linux 的明确降级行为。
- Web 代码沿用 Vue 3 Composition API 和 TypeScript；服务端状态沿用现有 Vue Query 封装。新增可见文案必须使用 `useI18n`，同时补齐 `web/public/locales/en/` 与 `zh/`。
- 不修改依赖目录和运行产物，例如 `web/node_modules/`、`bin/`、`dist/`、`web/test-results/`；只提交可再生成的源文件或仓库已跟踪的生成文件。

## 验证

先运行受影响范围内最小、最直接的测试，再按改动范围扩大验证：

```bash
# Go
go test ./internal/<package>
make test

# Web（在 web/ 下）
npm test
npm run lint
npm run build:check
npm run i18n:check

# 跨进程或集群验收
make test-acceptance
make test-e2e-web
```

修改 Proto 时额外验证生成结果和相关 Go/Web 测试。验收测试会启动真实进程和集群，耗时更长；Web E2E 还需要 Playwright 环境。macOS 可运行大部分单元和集成测试，但 systemd、cgroup、setuid 等生产语义必须在 Linux 验证。

完成任务时说明修改内容、已运行的验证及未运行验证的原因。不要为了让测试通过而削弱断言、吞掉错误或改变不相关行为。
