import { useI18n } from './useI18n'

export function useAudit() {
  const { t } = useI18n()

  const formatAuditAction = (action: string, metadata: Record<string, unknown>): string => {
    const key = `audit:action.${action}`
    return t(key, { ...metadata, defaultValue: action })
  }

  const formatAuditResult = (result: string): string => {
    const key = `audit:result.${result}`
    return t(key, { defaultValue: result })
  }

  return {
    formatAuditAction,
    formatAuditResult,
  }
}
