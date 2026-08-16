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
    ns: ['common', 'errors', 'process'],

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

export { i18n, I18NextVue }
