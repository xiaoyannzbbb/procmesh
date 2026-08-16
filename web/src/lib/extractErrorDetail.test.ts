import { describe, it, expect } from 'vitest'
import { Code, ConnectError } from '@connectrpc/connect'
import { extractErrorDetail } from './extractErrorDetail'

describe('extractErrorDetail', () => {
  it('should extract ErrorDetail from ConnectError', () => {
    const error = new ConnectError(
      'Process not found',
      Code.NotFound,
      undefined,
      undefined,
      {
        code: 'PROCESS_NOT_FOUND',
        message: 'Process nginx not found',
        params: { name: 'nginx' }
      }
    )

    const detail = extractErrorDetail(error)
    expect(detail).toEqual({
      code: 'PROCESS_NOT_FOUND',
      message: 'Process nginx not found',
      params: { name: 'nginx' }
    })
  })

  it('should return null for errors without ErrorDetail', () => {
    const error = new Error('Generic error')
    const detail = extractErrorDetail(error)
    expect(detail).toBeNull()
  })

  it('should handle ConnectError without details', () => {
    const error = new ConnectError('Network error', Code.Unavailable)
    const detail = extractErrorDetail(error)
    expect(detail).toBeNull()
  })

  it('should extract multiple params', () => {
    const error = new ConnectError(
      'Conflict',
      Code.FailedPrecondition,
      undefined,
      undefined,
      {
        code: 'CONFLICT',
        message: 'Configuration conflict',
        params: { expected: '5', actual: '3' }
      }
    )

    const detail = extractErrorDetail(error)
    expect(detail?.params).toEqual({ expected: '5', actual: '3' })
  })
})
