import i18n from 'i18next'
import I18NextVue from 'i18next-vue'
import LanguageDetector from 'i18next-browser-languagedetector'
import Backend from 'i18next-http-backend'

i18n
  .use(Backend)
  .use(LanguageDetector)
  .init({
    fallbackLng: 'en',
    supportedLngs: ['en', 'zh'],
    defaultNS: 'common',
    ns: ['common', 'errors', 'process', 'audit'],
    load: 'languageOnly',

    detection: {
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
      lookupLocalStorage: 'procmesh_language',
    },

    backend: {
      loadPath: '/locales/{{lng}}/{{ns}}.json',
      requestOptions: { cache: 'default' },
    },

    interpolation: {
      escapeValue: false,
    },
  })

export interface I18nInstance {
  t(key: string, options?: Record<string, unknown>): string
  loadNamespaces(namespaces: string[]): Promise<void>
  unloadNamespaces(namespaces: string[]): void
}

const i18nWithMethods: I18nInstance = {
  t: (key: string, options?: Record<string, unknown>): string => {
    return i18n.t(key, options)
  },
  loadNamespaces: async (namespaces: string[]): Promise<void> => {
    await Promise.all(
      namespaces.map(ns =>
        i18n.loadNamespaces(ns).catch(() => {
          // Namespace already loaded
        })
      )
    )
  },
  unloadNamespaces: (namespaces: string[]): void => {
    namespaces.forEach(ns => {
      i18n.removeResourceBundle(i18n.language, ns)
    })
  },
}

export { i18n, I18NextVue, i18nWithMethods }
