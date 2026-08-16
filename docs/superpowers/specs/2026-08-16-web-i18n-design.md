# ProcMesh Web 端 i18n 支持设计

日期：2026-08-16  
状态：待用户审阅  
范围：V1.0 P5 之后的独立 i18n 阶段

---

## 1. 背景与目标

ProcMesh 是面向全球用户的分布式进程管理平台。为了提升非英语用户的使用体验，需要在 Web 端增加完整的国际化（i18n）支持。

### 1.1 目标

1. **支持双语**：英文（默认）+ 中文简体
2. **全面覆盖**：前端 UI、错误消息、审计日志、进程状态描述
3. **用户友好**：自动检测浏览器语言，提供手动切换
4. **开发者友好**：类型安全、工具链支持、易于扩展
5. **性能优化**：按需加载，初始 bundle 影响 <20KB

### 1.2 非目标

- 日期时间本地化（统一使用 ISO 8601 格式）
- 数字格式本地化（技术指标保持通用格式）
- 第三方依赖库的翻译（如 shadcn-vue 组件）
- 后端日志翻译（保持英文，便于技术排查）

---

## 2. 技术选型

### 2.1 方案对比

| 方案 | 优点 | 缺点 | 适用场景 |
|------|------|------|----------|
| vue-i18n | Vue 生态成熟，TypeScript 支持好 | 单框架 | 标准 Vue 项目 |
| **i18next + vue-i18next** | 跨框架，功能强大，未来可前后端共享 | 略复杂 | **企业级项目（推荐）** |
| 自建轻量方案 | 零依赖，完全控制 | 需自己实现所有功能 | 极简场景 |

### 2.2 最终选择：i18next

**理由**：
1. 跨框架设计，未来后端可用 `go-i18n` 共享翻译 JSON
2. 功能完整：命名空间、插值、复数、懒加载
3. 生态丰富：TypeScript 类型生成、ESLint 插件、VSCode 扩展
4. 社区活跃，经过大量生产验证

**依赖**：
- `i18next` ~50KB gzipped
- `i18next-vue` ~10KB
- `i18next-browser-languagedetector` ~5KB
- `i18next-http-backend` ~3KB（按需加载用）

---

## 3. 架构设计

### 3.1 目录结构

```
web/
  src/
    locales/
      en/
        common.json          # 通用 UI 文本（导航、按钮、状态）
        errors.json          # 错误码到消息的映射
        process.json         # 进程相关术语
        audit.json           # 审计日志描述
      zh/
        common.json
        errors.json
        process.json
        audit.json
    lib/
      i18n.ts               # i18next 配置与初始化
      useI18n.ts            # Vue composable 封装
    types/
      i18n-resources.d.ts   # 自动生成的 TypeScript 类型
```

### 3.2 命名空间划分

| 命名空间 | 内容 | 预加载 |
|----------|------|--------|
| `common` | 导航、按钮、表单标签、通用状态 | ✅ 是 |
| `errors` | 错误码映射 | ❌ 按需 |
| `process` | 进程状态、操作、配置项 | ❌ 按需 |
| `audit` | 审计事件类型描述 | ❌ 按需 |

**原则**：
- 预加载 `common`，其他按路由懒加载
- 单个命名空间文件 <10KB（英文版）
- 按功能模块拆分，避免单文件过大

### 3.3 初始化配置（`lib/i18n.ts`）

```typescript
import i18n from 'i18next'
import I18NextVue from 'i18next-vue'
import LanguageDetector from 'i18next-browser-languagedetector'
import Backend from 'i18next-http-backend'

i18n
  .use(Backend)
  .use(LanguageDetector)
  .init({
    fallbackLng: 'en',
    supportedLngs: ['en', 'zh'],
    defaultNS: 'common',
    ns: ['common'],  // 只预加载 common
    
    detection: {
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
      lookupLocalStorage: 'procmesh_language',
    },
    
    backend: {
      loadPath: '/locales/{{lng}}/{{ns}}.json',
      requestOptions: { cache: 'default' },
    },
    
    interpolation: {
      escapeValue: false,  // Vue 已做 XSS 防护
    },
  })

export default i18n
```

---

## 4. 翻译文件结构

### 4.1 通用 UI（`locales/en/common.json`）

```json
{
  "app": {
    "name": "ProcMesh",
    "tagline": "Distributed Process Management Platform"
  },
  "nav": {
    "cluster": "Cluster",
    "nodes": "Nodes",
    "processes": "Processes",
    "users": "Users",
    "audit": "Audit"
  },
  "actions": {
    "start": "Start",
    "stop": "Stop",
    "restart": "Restart",
    "delete": "Delete",
    "edit": "Edit",
    "save": "Save",
    "cancel": "Cancel"
  },
  "status": {
    "live": "Live",
    "stale": "Stale",
    "unknown": "Unknown"
  }
}
```

### 4.2 错误码映射（`locales/en/errors.json`）

```json
{
  "PROCESS_NOT_FOUND": "Process {{name}} not found",
  "INVALID_CREDENTIALS": "Invalid username or password",
  "CONFLICT": "Configuration conflict: expected revision {{expected}}, got {{actual}}",
  "UNAVAILABLE": "Target agent {{agent}} is unavailable",
  "DENIED": "Permission denied: {{action}} requires {{permission}}",
  "TIMEOUT": "Operation timed out after {{seconds}}s",
  "DUPLICATE_NODE_ID": "Node ID {{nodeId}} already exists",
  "DEGRADED": "Agent is in degraded mode: {{reason}}"
}
```

**插值语法**：`{{变量名}}`

### 4.3 进程状态（`locales/en/process.json`）

```json
{
  "desiredState": {
    "RUNNING": "Running",
    "STOPPED": "Stopped"
  },
  "observedState": {
    "STOPPED": "Stopped",
    "STARTING": "Starting",
    "RUNNING": "Running",
    "STOPPING": "Stopping",
    "EXITED": "Exited",
    "BACKOFF": "Backoff",
    "FATAL": "Fatal",
    "UNKNOWN": "Unknown"
  },
  "healthState": {
    "HEALTHY": "Healthy",
    "UNHEALTHY": "Unhealthy",
    "UNKNOWN": "Unknown"
  }
}
```

### 4.4 审计日志（`locales/en/audit.json`）

```json
{
  "action": {
    "LOGIN": "User logged in",
    "PROCESS_START": "Started process {{name}}",
    "PROCESS_STOP": "Stopped process {{name}}",
    "CONFIG_UPDATE": "Updated configuration for {{name}} (rev {{revision}})",
    "NODE_JOIN": "Node {{nodeId}} joined cluster",
    "NODE_REMOVE": "Node {{nodeId}} removed from cluster"
  },
  "result": {
    "SUCCESS": "Success",
    "FAILED": "Failed",
    "TIMEOUT": "Timeout"
  }
}
```

---

## 5. 前端实现

### 5.1 Vue Composable 封装（`lib/useI18n.ts`）

```typescript
import { useI18n as useI18nBase } from 'i18next-vue'

export function useI18n() {
  const { t, i18n } = useI18nBase()
  
  // 错误码翻译辅助（带降级）
  const tError = (code: string, fallback: string, params?: Record<string, any>) => {
    const key = `errors:${code}`
    const translated = t(key, params)
    return translated === key ? fallback : translated
  }
  
  // 切换语言
  const setLanguage = async (lang: 'en' | 'zh') => {
    await i18n.changeLanguage(lang)
    localStorage.setItem('procmesh_language', lang)
  }
  
  return {
    t,
    tError,
    currentLanguage: i18n.language,
    setLanguage,
  }
}
```

### 5.2 组件使用示例

**普通翻译**：
```vue
<template>
  <h1>{{ t('common:app.name') }}</h1>
  <button>{{ t('common:actions.start') }}</button>
</template>

<script setup>
const { t } = useI18n()
</script>
```

**错误处理**：
```typescript
try {
  await api.startProcess(name)
} catch (err) {
  const message = tError(
    err.code,        // "PROCESS_NOT_FOUND"
    err.message,     // "Process nginx not found"
    err.params       // { name: "nginx" }
  )
  toast.error(message)  // 显示翻译后的消息
}
```

**审计日志渲染**：
```typescript
const formatAuditEvent = (event) => {
  return t(`audit:action.${event.action}`, event.metadata)
  // 例如：t('audit:action.PROCESS_START', { name: 'nginx' })
  // 英文: "Started process nginx"
  // 中文: "启动进程 nginx"
}
```

### 5.3 语言切换组件

```vue
<template>
  <DropdownMenu>
    <DropdownMenuTrigger>
      {{ currentLanguage === 'en' ? 'English' : '中文' }}
    </DropdownMenuTrigger>
    <DropdownMenuContent>
      <DropdownMenuItem @click="setLanguage('en')">
        English
      </DropdownMenuItem>
      <DropdownMenuItem @click="setLanguage('zh')">
        中文简体
      </DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</template>

<script setup>
const { currentLanguage, setLanguage } = useI18n()
</script>
```

---

## 6. 后端改动

### 6.1 Protobuf 错误结构（新增 `proto/procmesh/v1/errors.proto`）

```protobuf
syntax = "proto3";

package procmesh.v1;

message ErrorDetail {
  string code = 1;              // 错误码，如 "PROCESS_NOT_FOUND"
  string message = 2;           // 英文默认消息（降级用）
  map<string, string> params = 3;  // 插值参数
}
```

### 6.2 Go 错误构造辅助（新增 `internal/api/errors.go`）

```go
package api

import (
    "fmt"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
    pb "procmesh/proto/procmesh/v1"
)

func NewI18nError(grpcCode codes.Code, errCode, message string, params map[string]string) error {
    detail := &pb.ErrorDetail{
        Code:    errCode,
        Message: message,
        Params:  params,
    }
    
    st := status.New(grpcCode, message)
    st, _ = st.WithDetails(detail)
    return st.Err()
}

func ProcessNotFoundError(name string) error {
    return NewI18nError(
        codes.NotFound,
        "PROCESS_NOT_FOUND",
        fmt.Sprintf("Process %s not found", name),
        map[string]string{"name": name},
    )
}

func ConflictError(expected, actual int64) error {
    return NewI18nError(
        codes.FailedPrecondition,
        "CONFLICT",
        fmt.Sprintf("Configuration conflict: expected revision %d, got %d", expected, actual),
        map[string]string{
            "expected": strconv.FormatInt(expected, 10),
            "actual":   strconv.FormatInt(actual, 10),
        },
    )
}
```

### 6.3 服务端使用

```go
func (s *ProcessService) Start(ctx context.Context, req *pb.StartRequest) (*pb.StartResponse, error) {
    proc, err := s.processMgr.Get(req.ProcessId)
    if err != nil {
        return nil, ProcessNotFoundError(req.ProcessId)
    }
    // ... 执行操作
}
```

### 6.4 审计记录结构

后端审计记录需要存储结构化数据：

```go
type AuditEvent struct {
    ID        string
    Timestamp time.Time
    UserID    string
    Action    string               // "PROCESS_START"
    Metadata  map[string]string    // {"name": "nginx"}
    Result    string               // "SUCCESS"
}
```

前端读取后用 `t('audit:action.' + event.Action, event.Metadata)` 翻译。

---

## 7. 类型安全与工具链

### 7.1 TypeScript 类型生成

**自动生成翻译 key 的类型定义**，避免拼写错误。

`package.json` 添加脚本：
```json
{
  "scripts": {
    "i18n:types": "i18next-resources-to-backend-generator --input ./src/locales --output ./src/types/i18n-resources.d.ts"
  }
}
```

生成后的类型文件提供自动补全：
```typescript
t('common:app.name')        // ✅ 有效
t('common:app.invalid')     // ❌ TypeScript 错误
t('errors:PROCESS_NOT_FOUND', { name: 'nginx' })  // ✅
t('errors:PROCESS_NOT_FOUND', { invalid: 'x' })   // ❌ 参数类型错误
```

### 7.2 ESLint 检查硬编码字符串

**安装**：`eslint-plugin-i18next`

**`.eslintrc.js` 配置**：
```javascript
module.exports = {
  plugins: ['i18next'],
  rules: {
    'i18next/no-literal-string': ['warn', {
      mode: 'all',
      ignore: ['^[A-Z_]+$'],  // 忽略常量
      ignoreAttribute: ['data-testid', 'type', 'role'],
    }],
  }
}
```

警告所有未通过 `t()` 翻译的硬编码字符串。

### 7.3 CI 翻译完整性检查

**添加脚本**（`scripts/check-i18n.js`）：
```javascript
// 检查 en/ 和 zh/ 的文件列表和 key 是否一致
// 详细实现见设计文档第六部分
```

**在 CI 中运行**：
```yaml
# .github/workflows/ci.yml
- name: Check i18n completeness
  run: node scripts/check-i18n.js
```

---

## 8. 性能优化

### 8.1 按需加载策略

**问题**：一次性加载所有命名空间会增加初始 bundle 大小。

**方案**：按路由懒加载。

**Vue Router 路由守卫**：
```typescript
// router/index.ts
const routes = [
  {
    path: '/process/:id',
    component: () => import('@/pages/ProcessDetail.vue'),
    meta: {
      i18nNamespaces: ['process', 'errors']
    }
  }
]

router.beforeEach(async (to, from, next) => {
  const namespaces = to.meta.i18nNamespaces as string[]
  if (namespaces) {
    await i18n.loadNamespaces(namespaces)
  }
  next()
})
```

### 8.2 构建优化

**Vite 配置**：
```typescript
export default defineConfig({
  build: {
    rollupOptions: {
      output: {
        manualChunks: {
          'i18n': ['i18next', 'i18next-vue', 'i18next-browser-languagedetector'],
        },
      },
    },
  },
  publicDir: 'public',  // locales/ 作为静态资源
})
```

### 8.3 性能指标

**完整翻译文件大小估算**：
- `common.json`: ~10KB
- `errors.json`: ~5KB
- `process.json`: ~8KB
- `audit.json`: ~6KB
- i18next 库: ~50KB (gzipped)

**优化效果**：
- 初始加载：i18next 库 + `common.json` = ~60KB
- 按需加载：进入特定页面时加载 5-10KB
- 相比一次性加载全部（~80KB），节省 ~20KB（约 25%）

---

## 9. 测试策略

### 9.1 单元测试：翻译完整性

```typescript
describe('i18n completeness', () => {
  it('should have matching keys in all namespaces', () => {
    const enKeys = flattenKeys(enCommon)
    const zhKeys = flattenKeys(zhCommon)
    expect(enKeys.sort()).toEqual(zhKeys.sort())
  })
  
  it('should have matching interpolation vars', () => {
    const enVars = extractVars(enErrors.PROCESS_NOT_FOUND)  // ["name"]
    const zhVars = extractVars(zhErrors.PROCESS_NOT_FOUND)  // ["name"]
    expect(enVars).toEqual(zhVars)
  })
})
```

### 9.2 组件测试：翻译渲染

```typescript
describe('ProcessCard i18n', () => {
  it('should render in English', () => {
    i18n.global.locale = 'en'
    const wrapper = mount(ProcessCard, {
      props: { process: { state: 'RUNNING' } }
    })
    expect(wrapper.text()).toContain('Running')
  })
  
  it('should render in Chinese', () => {
    i18n.global.locale = 'zh'
    const wrapper = mount(ProcessCard, {
      props: { process: { state: 'RUNNING' } }
    })
    expect(wrapper.text()).toContain('运行中')
  })
})
```

### 9.3 E2E 测试：错误消息翻译

```typescript
test('should show translated error in Chinese', async ({ page }) => {
  await page.evaluate(() => localStorage.setItem('procmesh_language', 'zh'))
  await page.reload()
  
  await page.click('[data-testid="process-start-invalid"]')
  
  await expect(page.locator('[role="alert"]'))
    .toContainText('进程 nginx 不存在')
})
```

### 9.4 覆盖率目标

- 翻译完整性测试：100%（CI 强制）
- 组件翻译渲染：主要组件覆盖
- E2E：核心用户路径

---

## 10. 实施计划

### 10.1 与 V1.0 阶段的集成

**时机**：在 **P5（Vue Web 嵌入、集群视图）** 完成后开始。

**理由**：P5 之前 Web UI 未成型，过早引入会因 UI 重构返工。

### 10.2 阶段划分

| 阶段 | 任务 | 时间 | 交付 |
|------|------|------|------|
| **1. 基础设施** | 安装依赖、目录结构、初始化、语言切换 UI | 2-3 天 | i18n 框架可用 |
| **2. 核心 UI 翻译** | 翻译所有静态 UI 文本、TypeScript 类型、ESLint | 3-4 天 | 所有页面支持双语 |
| **3. 后端错误支持** | Protobuf、Go 辅助函数、前端 `tError()` | 2-3 天 | 错误消息翻译生效 |
| **4. 审计与状态** | 审计日志、进程状态翻译 | 2 天 | 动态内容支持双语 |
| **5. 优化与测试** | 懒加载、构建优化、测试、文档 | 2-3 天 | 性能达标、测试通过 |

**总时间**：11-15 天（约 2-3 周）

### 10.3 依赖与前置条件

- **前置**：P5 完成，Web UI 基本稳定
- **并行**：可与 V1.1 其他功能并行开发

### 10.4 风险与缓解

| 风险 | 缓解措施 |
|------|----------|
| 翻译质量不佳 | 邀请母语使用者 review |
| 后端改动影响现有功能 | 错误处理向后兼容，渐进式迁移 |
| 性能回归 | 监控 bundle 大小，Lighthouse CI |
| 遗漏硬编码字符串 | ESLint 强制检查，code review 重点关注 |

---

## 11. 未来扩展

### 11.1 V1.1+ 可能的增强

- 支持更多语言（繁体中文、日文、韩文等）
- 日期时间本地化（可选）
- 后端日志翻译（如果用户强烈需求）
- 翻译管理平台集成（如 Crowdin、Phrase）

### 11.2 后端共享翻译文件

未来如果后端也需要翻译（如发送邮件、Webhook 消息），可以：
- 使用 `go-i18n` 或 `i18next-go`
- 共享同一套 JSON 翻译文件
- 后端根据用户偏好或 API `Accept-Language` header 返回翻译内容

---

## 12. 参考文档

- i18next 官方文档：https://www.i18next.com/
- vue-i18next：https://github.com/i18next/i18next-vue
- ESLint 插件：https://github.com/edvardchen/eslint-plugin-i18next
- VSCode 扩展：i18n Ally

---

## 附录：关键决策记录

| 决策 | 选项 | 最终选择 | 理由 |
|------|------|----------|------|
| i18n 库 | vue-i18n / i18next / 自建 | i18next | 跨框架，功能强大，生态丰富 |
| 支持语言 | 仅英文 / 英+中 / 多语言 | 英+中 | V1.0 合理范围 |
| 翻译范围 | 仅 UI / UI+错误 / 全面 | 全面 | 企业级产品需求 |
| 日期格式 | 统一 / 本地化 | 统一 ISO 8601 | 技术工具特性 |
| 后端错误 | 错误码 / 后端翻译 / 混合 | 混合 | 灵活且有降级 |
