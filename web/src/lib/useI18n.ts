import { useTranslation } from 'i18next-vue'
import { computed } from 'vue'
import { i18n } from './i18n'

export function useI18n() {
  const { t, i18next } = useTranslation('common')

  const tError = (code: string, fallback: string, params?: Record<string, any>) => {
    const key = `errors:${code}`
    const translated = t(key, params)
    // Check if translation wasn't found (returns the key or just the code)
    if (translated === key || translated === code) {
      return fallback
    }
    return translated
  }

  const setLanguage = async (lang: 'en' | 'zh') => {
    await i18next.changeLanguage(lang)
    localStorage.setItem('procmesh_language', lang)
  }

  return {
    t,
    tError,
    currentLanguage: computed(() => i18next.language),
    setLanguage,
  }
}

// Preload errors and process namespaces on module load
i18n.loadNamespaces(['errors', 'process'])

