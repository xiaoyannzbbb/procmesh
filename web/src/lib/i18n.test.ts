import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { join } from 'path'
import { i18n } from './i18n'

describe('i18n configuration', () => {
  it('has fallbackLng set to en', () => {
    const fallbackLng = i18n.options.fallbackLng
    if (Array.isArray(fallbackLng)) {
      expect(fallbackLng).toContain('en')
    } else {
      expect(fallbackLng).toBe('en')
    }
  })

  it('supports both en and zh languages', () => {
    expect(i18n.options.supportedLngs).toContain('en')
    expect(i18n.options.supportedLngs).toContain('zh')
  })

  it('sets common as default namespace', () => {
    expect(i18n.options.defaultNS).toBe('common')
  })

  it('has common in namespaces list', () => {
    expect(i18n.options.ns).toContain('common')
  })

  it('configures localStorage detection with correct key', () => {
    const detection = i18n.options.detection
    expect(detection?.lookupLocalStorage).toBe('procmesh_language')
    expect(detection?.order).toContain('localStorage')
    expect(detection?.order).toContain('navigator')
    expect(detection?.caches).toContain('localStorage')
  })

  it('loads only core namespaces through the HTTP backend', () => {
    expect(i18n.options.resources).toBeUndefined()
    expect(i18n.options.ns).toEqual(expect.arrayContaining(['common', 'errors']))
    expect(i18n.options.ns).not.toEqual(expect.arrayContaining(['process', 'audit']))
    expect(i18n.options.fallbackNS).toEqual(expect.arrayContaining(['features']))
    expect(i18n.options.backend).toMatchObject({
      loadPath: '/locales/{{lng}}/{{ns}}.json',
    })
  })

  it('disables escapeValue for interpolation', () => {
    expect(i18n.options.interpolation?.escapeValue).toBe(false)
  })
})

describe('translation files', () => {
  it('English common.json has correct structure', () => {
    const enPath = join(process.cwd(), 'public/locales/en/common.json')
    const enContent = readFileSync(enPath, 'utf-8')
    const data = JSON.parse(enContent)

    expect(data).toHaveProperty('app')
    expect(data).toHaveProperty('nav')
    expect(data).toHaveProperty('actions')
    expect(data).toHaveProperty('status')
    expect(data).toHaveProperty('common')

    expect(data.app.name).toBe('ProcMesh')
    expect(data.nav.cluster).toBe('Cluster')
    expect(data.actions.start).toBe('Start')
    expect(data.status.live).toBe('Live')
  })

  it('Chinese common.json has correct structure', () => {
    const zhPath = join(process.cwd(), 'public/locales/zh/common.json')
    const zhContent = readFileSync(zhPath, 'utf-8')
    const data = JSON.parse(zhContent)

    expect(data).toHaveProperty('app')
    expect(data).toHaveProperty('nav')
    expect(data).toHaveProperty('actions')
    expect(data).toHaveProperty('status')
    expect(data).toHaveProperty('common')

    expect(data.app.name).toBe('ProcMesh')
    expect(data.nav.cluster).toBe('集群')
    expect(data.actions.start).toBe('启动')
    expect(data.status.live).toBe('实时')
  })

  it('en and zh have same top-level keys', () => {
    const enPath = join(process.cwd(), 'public/locales/en/common.json')
    const enContent = readFileSync(enPath, 'utf-8')
    const enData = JSON.parse(enContent)

    const zhPath = join(process.cwd(), 'public/locales/zh/common.json')
    const zhContent = readFileSync(zhPath, 'utf-8')
    const zhData = JSON.parse(zhContent)

    const enKeys = Object.keys(enData).sort()
    const zhKeys = Object.keys(zhData).sort()

    expect(enKeys).toEqual(zhKeys)
  })

  it('English and Chinese have same nested keys for each section', () => {
    const enPath = join(process.cwd(), 'public/locales/en/common.json')
    const enContent = readFileSync(enPath, 'utf-8')
    const enData = JSON.parse(enContent)

    const zhPath = join(process.cwd(), 'public/locales/zh/common.json')
    const zhContent = readFileSync(zhPath, 'utf-8')
    const zhData = JSON.parse(zhContent)

    const checkNesting = (enObj: Record<string, unknown>, zhObj: Record<string, unknown>) => {
      for (const key in enObj) {
        expect(zhObj).toHaveProperty(key)
        const enValue = enObj[key]
        const zhValue = zhObj[key]
        if (enValue && typeof enValue === 'object' && zhValue && typeof zhValue === 'object') {
          checkNesting(
            enValue as Record<string, unknown>,
            zhValue as Record<string, unknown>,
          )
        }
      }
    }

    checkNesting(enData, zhData)
  })
})
