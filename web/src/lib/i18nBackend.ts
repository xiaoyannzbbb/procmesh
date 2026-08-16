import commonEn from '../../public/locales/en/common.json'
import commonZh from '../../public/locales/zh/common.json'
import errorsEn from '../../public/locales/en/errors.json'
import errorsZh from '../../public/locales/zh/errors.json'
import processEn from '../../public/locales/en/process.json'
import processZh from '../../public/locales/zh/process.json'
import auditEn from '../../public/locales/en/audit.json'
import auditZh from '../../public/locales/zh/audit.json'

const translations = {
  en: {
    common: commonEn,
    errors: errorsEn,
    process: processEn,
    audit: auditEn,
  },
  zh: {
    common: commonZh,
    errors: errorsZh,
    process: processZh,
    audit: auditZh,
  },
}

export function loadNamespace(language: string, namespace: string): Record<string, unknown> {
  return translations[language as keyof typeof translations]?.[namespace as keyof (typeof translations.en)] || {}
}

export function getAvailableNamespaces(): string[] {
  return ['common', 'errors', 'process', 'audit']
}
