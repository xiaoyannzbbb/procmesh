import { useI18n } from './useI18n'
import { extractErrorDetail } from './extractErrorDetail'

export function useErrorHandler() {
  const { tError } = useI18n()

  /**
   * Format an error for display to the user.
   * Attempts to extract ErrorDetail and translate, falls back to raw message.
   */
  const formatError = (error: unknown): string => {
    // Try to extract structured error detail
    const detail = extractErrorDetail(error)

    if (detail) {
      return tError(detail.code, detail.message, detail.params)
    }

    // Fallback to raw error message
    if (error instanceof Error) {
      return error.message
    }

    return String(error)
  }

  return {
    formatError
  }
}
