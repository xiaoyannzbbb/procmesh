import { describe, it, expect } from 'vitest'
import { Code, ConnectError } from '@connectrpc/connect'
import { ErrorInfoSchema } from '../gen/procmesh/v1/api_pb'
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

  it('should extract a protobuf ErrorInfo detail', () => {
    const error = new ConnectError('Process not found', Code.NotFound, undefined, [
      {
        desc: ErrorInfoSchema,
        value: { code: 'PROCESS_NOT_FOUND', message: 'Process nginx not found' }
      }
    ])

    expect(extractErrorDetail(error)).toEqual({
      code: 'PROCESS_NOT_FOUND',
      message: 'Process nginx not found',
      params: {}
    })
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
