# ProcMesh Web

Vue 3 web interface for ProcMesh distributed process management.

## Features

- ✅ Real-time cluster monitoring
- ✅ Process management (start, stop, restart)
- ✅ User authentication and authorization
- ✅ Audit logging
- ✅ **Internationalization (English + 简体中文)**

## Development

```bash
# Install dependencies
npm install

# Start dev server
npm run dev

# Run tests
npm test

# Run E2E tests
npm run test:e2e

# Build for production
npm run build

# Check i18n completeness
npm run i18n:check

# Analyze bundle size
npm run analyze
```

Production builds enforce a `170 KiB` gzip budget for the complete login and
overview critical payloads. The build also emits precompressed `.gz` files for
the embedded Go server.

## i18n Support

See [docs/I18N_GUIDE.md](docs/I18N_GUIDE.md) for detailed documentation.

**Quick Start:**

```typescript
// In any component
import { useI18n } from '@/lib/useI18n'
const { t } = useI18n()

// Use in template
{{ t('common:actions.start') }}
```

## Project Structure

```
web/
├── public/
│   └── locales/          # Translation files
│       ├── en/           # English
│       └── zh/           # Chinese
├── src/
│   ├── components/       # Vue components
│   ├── pages/            # Page components
│   ├── lib/              # Utilities and composables
│   │   ├── i18n.ts       # i18n configuration
│   │   ├── useI18n.ts    # Translation composable
│   │   └── ...
│   └── types/            # TypeScript types
├── scripts/
│   ├── generate-i18n-types.ts
│   └── check-i18n-completeness.js
└── tests/
    ├── e2e/              # Playwright E2E tests
    └── ...
```
