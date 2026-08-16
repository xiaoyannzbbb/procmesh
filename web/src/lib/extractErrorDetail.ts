import { ConnectError } from '@connectrpc/connect'

export interface ErrorDetail {
  code: string
  message: string
  params: Record<string, string>
}

/**
 * Extract structured ErrorDetail from a ConnectRPC error.
 * Returns null if the error doesn't contain ErrorDetail.
 */
export function extractErrorDetail(error: unknown): ErrorDetail | null {
  if (!(error instanceof ConnectError)) {
    return null
  }

  // ConnectError stores ErrorDetail in the cause field
  const cause = (error as any).cause

  if (cause && typeof cause === 'object' && 'code' in cause) {
    return {
      code: String(cause.code),
      message: String(cause.message || error.message),
      params: (cause.params as Record<string, string>) || {}
    }
  }

  // Try findDetails for protobuf-encoded details
  try {
    const details = error.findDetails?.()
    if (details && details.length > 0) {
      for (const detail of details) {
        if (detail && typeof detail === 'object' && 'code' in detail) {
          return {
            code: String(detail.code),
            message: String(detail.message || error.message),
            params: (detail.params as Record<string, string>) || {}
          }
        }
      }
    }
  } catch {
    // Ignore errors from findDetails and fall back to null
  }

  return null
}
