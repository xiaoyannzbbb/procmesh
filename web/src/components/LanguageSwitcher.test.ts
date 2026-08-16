import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import LanguageSwitcher from './LanguageSwitcher.vue'
import { I18NextVue } from '../lib/i18n'
import i18next from 'i18next'
import { nextTick } from 'vue'

// Create a test-specific i18n instance without HTTP backend
const testI18n = i18next.createInstance()
testI18n.init({
  lng: 'en',
  fallbackLng: 'en',
  supportedLngs: ['en', 'zh'],
  load: 'languageOnly',
  resources: {
    en: {
      common: { languageName: 'English' }
    },
    zh: {
      common: { languageName: '中文' }
    }
  },
  interpolation: {
    escapeValue: false
  }
})

describe('LanguageSwitcher', () => {
  beforeEach(async () => {
    localStorage.clear()
    // Set initial language to English using test i18n instance
    await testI18n.changeLanguage('en')
    await nextTick()
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('should display current language (English by default)', () => {
    const wrapper = mount(LanguageSwitcher, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]]
      }
    })

    expect(wrapper.text()).toContain('English')
    expect(wrapper.find('[data-testid="lang-en"]').classes()).toContain('active')
  })

  it('should mark English active for a regional English locale', async () => {
    await testI18n.changeLanguage('en-US')
    await nextTick()

    const wrapper = mount(LanguageSwitcher, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]]
      }
    })

    expect(wrapper.find('[data-testid="lang-en"]').classes()).toContain('active')
  })

  it('should switch to Chinese when clicked and verify integration', async () => {
    const wrapper = mount(LanguageSwitcher, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]]
      }
    })

    const zhButton = wrapper.find('[data-testid="lang-zh"]')

    // Set up a promise to wait for language change event
    const languageChanged = new Promise(resolve => {
      testI18n.on('languageChanged', resolve)
    })

    await zhButton.trigger('click')

    // Wait for i18n language change event
    await languageChanged
    await wrapper.vm.$nextTick()

    // Verify i18n instance language changed
    expect(testI18n.language).toBe('zh')

    // Verify localStorage was updated
    expect(localStorage.getItem('procmesh_language')).toBe('zh')

    // Verify UI updated: Chinese button now active, English not
    expect(wrapper.find('[data-testid="lang-zh"]').classes()).toContain('active')
    expect(wrapper.find('[data-testid="lang-en"]').classes()).not.toContain('active')
    expect(wrapper.find('[data-testid="lang-zh"]').text()).toContain('中文')
  })

  it('should switch to English and verify integration', async () => {
    const wrapper = mount(LanguageSwitcher, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]]
      }
    })

    // First switch to Chinese
    let zhButton = wrapper.find('[data-testid="lang-zh"]')

    let languageChanged = new Promise(resolve => {
      testI18n.on('languageChanged', resolve)
    })

    await zhButton.trigger('click')
    await languageChanged
    await wrapper.vm.$nextTick()

    // Verify initial state is Chinese
    expect(testI18n.language).toBe('zh')

    // Now click English
    const enButton = wrapper.find('[data-testid="lang-en"]')

    languageChanged = new Promise(resolve => {
      testI18n.on('languageChanged', resolve)
    })

    await enButton.trigger('click')
    await languageChanged
    await wrapper.vm.$nextTick()

    // Verify i18n instance language changed back to English
    expect(testI18n.language).toBe('en')

    // Verify localStorage was updated
    expect(localStorage.getItem('procmesh_language')).toBe('en')

    // Verify UI updated: English button now active, Chinese not
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="lang-en"]').classes()).toContain('active')
    expect(wrapper.find('[data-testid="lang-zh"]').classes()).not.toContain('active')
    expect(wrapper.find('[data-testid="lang-en"]').text()).toContain('English')
  })
})
