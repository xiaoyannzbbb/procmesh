import { useTranslation } from 'i18next-vue'
import { computed } from 'vue'

export function useI18n() {
  const { t, i18next } = useTranslation('common')

  const tError = (code: string, fallback: string, params?: Record<string, unknown>) => {
    const key = `errors:${code}`
    return t(key, { ...params, defaultValue: fallback })
  }

  const setLanguage = async (lang: 'en' | 'zh') => {
    await i18next.changeLanguage(lang)
    localStorage.setItem('procmesh_language', lang)
  }

  return {
    t,
    tError,
    currentLanguage: computed(() => i18next.resolvedLanguage ?? i18next.language),
    setLanguage,
  }
}
