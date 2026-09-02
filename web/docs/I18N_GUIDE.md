# Internationalization (i18n) Guide

## Overview

ProcMesh Web uses [i18next](https://www.i18next.com/) for internationalization support.

**Supported Languages:**
- English (en) - Default
- Simplified Chinese (zh)

## Adding Translations

### 1. Add translation keys

Edit the appropriate JSON file in `public/locales/{lang}/`:

**public/locales/en/common.json:**
```json
{
  "myFeature": {
    "title": "My Feature",
    "description": "This is my feature"
  }
}
```

**public/locales/zh/common.json:**
```json
{
  "myFeature": {
    "title": "我的功能",
    "description": "这是我的功能"
  }
}
```

### 2. Use in components

```vue
<template>
  <div>
    <h1>{{ t('common:myFeature.title') }}</h1>
    <p>{{ t('common:myFeature.description') }}</p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from '@/lib/useI18n'
const { t } = useI18n()
</script>
```

### 3. Regenerate types

```bash
npm run i18n:types
```

### 4. Verify completeness

```bash
npm run i18n:check
```

## Translation Helpers

### useI18n()

Basic translation composable:

```typescript
const { t, currentLanguage, setLanguage } = useI18n()

// Translate with namespace
t('common:actions.start')

// Change language
await setLanguage('zh')
```

### useErrorHandler()

For error message translation:

```typescript
const { formatError } = useErrorHandler()

try {
  await apiCall()
} catch (err) {
  const message = formatError(err)
  toast.error(message)
}
```

### useProcessState()

For process state translation:

```typescript
const { translateObservedState, translateHealthState } = useProcessState()

const stateLabel = translateObservedState(process.observedState)
```

### useAudit()

For audit log translation:

```typescript
const { formatAuditAction, formatAuditResult } = useAudit()

const actionText = formatAuditAction(event.action, event.metadata)
```

## Namespaces

| Namespace | Content | Loading |
|-----------|---------|---------|
| `common` | UI labels, navigation, actions | Preloaded |
| `errors` | Error codes and messages | Preloaded |
| `features` | Non-overview page copy | Lazy (route fallback) |
| `process` | Process states and labels | Lazy (route) |
| `audit` | Audit action descriptions | Lazy (route) |

Production loads namespaces from `/locales/{lang}/{namespace}.json` through the
i18next HTTP backend. Keep login, navigation, and overview copy in `common`;
place copy used only by other pages in `features` so it stays out of the initial
payload. Existing common-style keys resolve through the `features` fallback.

## Adding a New Language

1. Create directory: `public/locales/{lang}/`
2. Copy all JSON files from `en/` to `{lang}/`
3. Translate all values (keep keys unchanged)
4. Add language to `i18n.ts`:
   ```typescript
   supportedLngs: ['en', 'zh', 'newlang']
   ```
5. Update LanguageSwitcher component
6. Run `npm run i18n:check` to verify

## Backend Integration

Backend returns structured errors via ConnectRPC:

```go
return api.ProcessNotFoundError("nginx")
```

Frontend extracts and translates:

```typescript
const detail = extractErrorDetail(error)
// detail: { code: "PROCESS_NOT_FOUND", params: { name: "nginx" } }

const message = tError(detail.code, detail.message, detail.params)
// English: "Process nginx not found"
// Chinese: "未找到进程 nginx"
```

## Development Workflow

1. **Add feature in English first**
   - Implement component
   - Use `t()` calls from the start
   - Add keys to `en/*.json`

2. **Add Chinese translations**
   - Translate keys in `zh/*.json`
   - Maintain 1:1 key parity

3. **Verify**
   ```bash
   npm run i18n:check  # Check completeness
   npm run lint        # Check for hardcoded strings
   npm test            # Run unit tests
   npm run test:e2e    # Run E2E tests
   ```

4. **Commit**
   - Include both `en/` and `zh/` files in same commit
   - Regenerate types before committing

## Troubleshooting

### Translation not showing

1. Check namespace is loaded
2. Verify key exists in JSON
3. Check browser console for i18next errors
4. Run `npm run i18n:types` to update types

### ESLint warnings

Hardcoded strings trigger warnings. Either:
- Add to translations and use `t()`
- Add to ignore list if not UI text

### Tests failing

1. Preload namespaces in test setup
2. Set language explicitly in tests
3. Mock i18n for unit tests if needed

## Resources

- [i18next Documentation](https://www.i18next.com/)
- [i18next-vue](https://github.com/i18next/i18next-vue)
- [Translation Completeness Check](../../scripts/check-i18n-completeness.js)
