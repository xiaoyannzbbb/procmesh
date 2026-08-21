import { useI18n } from './useI18n'

export function useAudit() {
  const { t } = useI18n()

  const formatAuditAction = (action: string, metadata: Record<string, unknown>): string => {
    const normalized = action.replace(/[^a-zA-Z0-9]+/g, '_').toUpperCase()
    const key = `audit:action.${normalized}`
    return t(key, { ...metadata, defaultValue: action })
  }

  const formatAuditResult = (result: string): string => {
    const key = `audit:result.${result.toUpperCase()}`
    return t(key, { defaultValue: result })
  }

  return {
    formatAuditAction,
    formatAuditResult,
  }
}
