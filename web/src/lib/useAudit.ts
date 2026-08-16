import { useI18n } from './useI18n'

export function useAudit() {
  const { t } = useI18n()

  const formatAuditAction = (action: string, metadata: Record<string, any>): string => {
    const key = `audit:action.${action}`
    const translated = t(key, metadata)

    // If translation key doesn't exist, t() returns the key without namespace (action.UNKNOWN_ACTION)
    // Check if the translated value still contains the key structure
    if (translated === key || translated === `action.${action}` || translated.includes('action.')) {
      return action
    }
    return translated
  }

  const formatAuditResult = (result: string): string => {
    const key = `audit:result.${result}`
    const translated = t(key)

    // If translation key doesn't exist, return the result code itself
    if (translated === key || translated === `result.${result}` || translated.includes('result.')) {
      return result
    }
    return translated
  }

  return {
    formatAuditAction,
    formatAuditResult,
  }
}
