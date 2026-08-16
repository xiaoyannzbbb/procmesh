import { describe, it, expect } from 'vitest'
import { useI18n } from './useI18n'

describe('i18n type checking', () => {
  it('should have type-safe translation keys', () => {
    const { t } = useI18n()

    // These should compile without errors
    t('common:app.name')
    t('common:actions.start')
    t('errors:PROCESS_NOT_FOUND', { name: 'nginx' })
    t('process:observedState.RUNNING')
    t('audit:action.LOGIN')

    // @ts-expect-error - invalid key should cause type error
    t('common:invalid.key')

    // @ts-expect-error - invalid namespace should cause type error
    t('invalid:key')

    expect(true).toBe(true)
  })
})
