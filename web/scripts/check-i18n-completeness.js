import fs from 'fs'
import path from 'path'
import { fileURLToPath } from 'url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

const LOCALES_DIR = path.join(__dirname, '../public/locales')
const REFERENCE_LANG = 'en'

function flattenKeys(obj, prefix = '') {
  const keys = []

  for (const [key, value] of Object.entries(obj)) {
    const fullKey = prefix ? `${prefix}.${key}` : key

    if (typeof value === 'string') {
      keys.push(fullKey)
    } else if (typeof value === 'object' && value !== null) {
      keys.push(...flattenKeys(value, fullKey))
    }
  }

  return keys.sort()
}

function extractVars(str) {
  const matches = str.match(/\{\{(\w+)\}\}/g)
  return matches ? matches.map(m => m.slice(2, -2)).sort() : []
}

function checkCompleteness() {
  let hasErrors = false

  // Get all languages
  const languages = fs.readdirSync(LOCALES_DIR)
    .filter(name => fs.statSync(path.join(LOCALES_DIR, name)).isDirectory())

  if (!languages.includes(REFERENCE_LANG)) {
    console.error(`❌ Reference language '${REFERENCE_LANG}' not found`)
    return false
  }

  // Get all namespaces from reference language
  const refDir = path.join(LOCALES_DIR, REFERENCE_LANG)
  const namespaces = fs.readdirSync(refDir)
    .filter(file => file.endsWith('.json'))
    .map(file => file.replace('.json', ''))

  console.log(`Checking ${languages.length} languages, ${namespaces.length} namespaces...`)
  console.log('')

  // Check each namespace
  for (const namespace of namespaces) {
    const refFile = path.join(refDir, `${namespace}.json`)
    const refContent = JSON.parse(fs.readFileSync(refFile, 'utf-8'))
    const refKeys = flattenKeys(refContent)

    // Check each language
    for (const lang of languages) {
      if (lang === REFERENCE_LANG) continue

      const langFile = path.join(LOCALES_DIR, lang, `${namespace}.json`)

      // Check file exists
      if (!fs.existsSync(langFile)) {
        console.error(`❌ ${lang}/${namespace}.json is missing`)
        hasErrors = true
        continue
      }

      const langContent = JSON.parse(fs.readFileSync(langFile, 'utf-8'))
      const langKeys = flattenKeys(langContent)

      // Check for missing keys
      const missingKeys = refKeys.filter(k => !langKeys.includes(k))
      if (missingKeys.length > 0) {
        console.error(`❌ ${lang}/${namespace}.json missing keys:`)
        missingKeys.forEach(k => console.error(`   - ${k}`))
        hasErrors = true
      }

      // Check for extra keys
      const extraKeys = langKeys.filter(k => !refKeys.includes(k))
      if (extraKeys.length > 0) {
        console.error(`❌ ${lang}/${namespace}.json has extra keys:`)
        extraKeys.forEach(k => console.error(`   - ${k}`))
        hasErrors = true
      }

      // Check interpolation variables match
      for (const key of refKeys) {
        if (!langKeys.includes(key)) continue

        const refValue = key.split('.').reduce((obj, k) => obj[k], refContent)
        const langValue = key.split('.').reduce((obj, k) => obj[k], langContent)

        if (typeof refValue === 'string' && typeof langValue === 'string') {
          const refVars = extractVars(refValue)
          const langVars = extractVars(langValue)

          if (JSON.stringify(refVars) !== JSON.stringify(langVars)) {
            console.error(`❌ ${lang}/${namespace}.json '${key}' has mismatched variables:`)
            console.error(`   Reference: ${refVars.join(', ') || '(none)'}`)
            console.error(`   Translation: ${langVars.join(', ') || '(none)'}`)
            hasErrors = true
          }
        }
      }
    }
  }

  if (!hasErrors) {
    console.log('✓ All translations are complete and consistent')
  }

  return !hasErrors
}

const success = checkCompleteness()
process.exit(success ? 0 : 1)
