import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.resetModules()
  localStorage.clear()
})

describe('i18n resource loading', () => {
  it('fetches business namespaces only when requested', async () => {
    localStorage.setItem('procmesh_language', 'en')
    const requested: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
      const url = String(input)
      requested.push(url)
      return new Response(JSON.stringify({ loaded: url }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }))

    const { i18n, i18nReady } = await import('./i18n')
    await i18nReady

    expect(requested).toEqual(expect.arrayContaining([
      '/locales/en/common.json',
      '/locales/en/errors.json',
    ]))
    expect(requested).not.toContain('/locales/en/process.json')
    expect(requested).not.toContain('/locales/en/audit.json')
    expect(requested).not.toContain('/locales/en/features.json')

    await i18n.loadNamespaces(['features', 'process'])
    expect(requested).toContain('/locales/en/features.json')
    expect(requested).toContain('/locales/en/process.json')

    await i18n.changeLanguage('zh')
    expect(requested).toEqual(expect.arrayContaining([
      '/locales/zh/common.json',
      '/locales/zh/errors.json',
      '/locales/zh/features.json',
      '/locales/zh/process.json',
    ]))
  })

  it('finishes initialization when locale requests fail', async () => {
    localStorage.setItem('procmesh_language', 'en')
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 503 })))

    const { i18n, i18nReady } = await import('./i18n')
    await expect(i18nReady).resolves.toBeTypeOf('function')
    expect(i18n.t('app.name')).toBe('app.name')
  })
})
