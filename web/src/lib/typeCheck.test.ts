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
      const translations: Record<string, Record<string, unknown>> = {}

      for (const lang of languages) {
        const path = join(process.cwd(), `public/locales/${lang}/${ns}.json`)
        const content = readFileSync(path, 'utf-8')
        translations[lang] = JSON.parse(content) as Record<string, unknown>
      }

      // Extract all keys from en
      const getKeys = (obj: Record<string, unknown>, prefix = ''): string[] => {
        const keys: string[] = []
        for (const [key, value] of Object.entries(obj)) {
          const fullKey = prefix ? `${prefix}.${key}` : key
          if (typeof value === 'object' && value !== null) {
            keys.push(...getKeys(value as Record<string, unknown>, fullKey))
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
    const commonEn = JSON.parse(
      readFileSync(join(process.cwd(), 'public/locales/en/common.json'), 'utf-8'),
    ) as Record<string, unknown>
    const commonZh = JSON.parse(
      readFileSync(join(process.cwd(), 'public/locales/zh/common.json'), 'utf-8'),
    ) as Record<string, unknown>

    expect(typeof commonEn).toBe('object')
    expect(typeof commonZh).toBe('object')
    expect(commonEn).not.toBeNull()
    expect(commonZh).not.toBeNull()
  })
})
