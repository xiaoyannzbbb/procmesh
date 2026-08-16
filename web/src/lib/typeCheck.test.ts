import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { join } from 'path'

describe('i18n type safety', () => {
  it('generates i18n.d.ts with type definitions', () => {
    // Type definitions should be generated from translation files
    // This test verifies the type generation script works correctly
    const typesPath = join(process.cwd(), 'src/types/i18n.d.ts')
    const typeContent = readFileSync(typesPath, 'utf-8')

    // Should have module declaration
    expect(typeContent).toContain('declare module "i18next"')
    // Should define CustomTypeOptions interface for typed translations
    expect(typeContent).toContain('interface CustomTypeOptions')
  })

  it('all translation namespaces have matching key structures', () => {
    const namespaces = ['common', 'errors', 'process', 'audit']
    const languages = ['en', 'zh']

    for (const ns of namespaces) {
      const translations: Record<string, any> = {}

      for (const lang of languages) {
        const path = join(process.cwd(), `public/locales/${lang}/${ns}.json`)
        const content = readFileSync(path, 'utf-8')
        translations[lang] = JSON.parse(content)
      }

      // Extract all keys from en
      const getKeys = (obj: any, prefix = ''): string[] => {
        const keys: string[] = []
        for (const key in obj) {
          const fullKey = prefix ? `${prefix}.${key}` : key
          if (typeof obj[key] === 'object' && obj[key] !== null) {
            keys.push(...getKeys(obj[key], fullKey))
          } else {
            keys.push(fullKey)
          }
        }
        return keys
      }

      const enKeys = getKeys(translations.en).sort()
      const zhKeys = getKeys(translations.zh).sort()

      // Verify key parity for type safety
      expect(zhKeys).toEqual(
        enKeys,
        `${ns}: Chinese translation missing or has extra keys`,
      )
    }
  })

  it('translation resources are properly structured', () => {
    // Verify resources can be imported and are valid objects
    const commonEn = require('../../public/locales/en/common.json')
    const commonZh = require('../../public/locales/zh/common.json')

    expect(typeof commonEn).toBe('object')
    expect(typeof commonZh).toBe('object')
    expect(commonEn).not.toBeNull()
    expect(commonZh).not.toBeNull()
  })
})
