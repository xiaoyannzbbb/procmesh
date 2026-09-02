import { ConnectError } from '@connectrpc/connect'
import { ErrorInfoSchema } from '../gen/procmesh/v1/errors_pb'

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
  const cause = error.cause

  if (cause && typeof cause === 'object' && 'code' in cause) {
    const detail = cause as Record<string, unknown>
    return {
      code: String(detail.code),
      message: String(detail.message || error.message),
      params: (detail.params as Record<string, string>) || {}
    }
  }

  // Try findDetails for protobuf-encoded details
  try {
    const detail = error.findDetails(ErrorInfoSchema)[0]
    if (detail) {
      return {
        code: detail.code,
        message: detail.message || error.rawMessage,
        params: {}
      }
    }
  } catch {
    // Ignore errors from findDetails and fall back to null
  }

  return null
}
