# ProcMesh Web

## i18n Bundle Size

After implementing i18n support:

- i18n libraries: ~15KB gzipped (i18next + plugins)
- Initial translation files: ~3KB gzipped (common.json only)
- Total i18n impact: ~18KB gzipped
- Additional namespaces loaded on-demand: ~2-3KB each

Target: <20KB initial bundle impact ✓
