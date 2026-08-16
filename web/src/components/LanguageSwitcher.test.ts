import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import { ref } from 'vue'

// Create shared mock state that persists across mounts
const createMockI18n = () => {
  const currentLanguageRef = ref('en')

  return {
    currentLanguageRef,
    setLanguage: vi.fn(async (lang: string) => {
      currentLanguageRef.value = lang
      localStorage.setItem('procmesh_language', lang)
    })
  }
}

let mockI18nState: any

// Mock useI18n before importing the component
vi.mock('../lib/useI18n', () => ({
  useI18n: () => ({
    currentLanguage: mockI18nState.currentLanguageRef,
    setLanguage: mockI18nState.setLanguage,
    t: (key: string) => key,
    tError: (code: string, fallback: string) => fallback
  })
}))

import { mount } from '@vue/test-utils'
import LanguageSwitcher from './LanguageSwitcher.vue'

describe('LanguageSwitcher', () => {
  let setItemSpy: any

  beforeEach(() => {
    localStorage.clear()
    mockI18nState = createMockI18n()
    setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    vi.clearAllMocks()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    localStorage.clear()
  })

  it('should display current language (English by default)', () => {
    const wrapper = mount(LanguageSwitcher)

    expect(wrapper.text()).toContain('English')
    expect(wrapper.find('[data-testid="lang-en"]').classes()).toContain('active')
  })

  it('should switch to Chinese when clicked and verify integration', async () => {
    const wrapper = mount(LanguageSwitcher)

    const zhButton = wrapper.find('[data-testid="lang-zh"]')
    await zhButton.trigger('click')

    // Verify setLanguage was called with 'zh'
    expect(mockI18nState.setLanguage).toHaveBeenCalledWith('zh')

    // Verify localStorage was updated with 'zh'
    expect(setItemSpy).toHaveBeenCalledWith('procmesh_language', 'zh')

    // Verify reactive state changed
    expect(mockI18nState.currentLanguageRef.value).toBe('zh')

    // Verify UI updated: Chinese button now active, English not
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="lang-en"]').classes()).not.toContain('active')
    expect(wrapper.find('[data-testid="lang-zh"]').classes()).toContain('active')
    expect(wrapper.find('[data-testid="lang-zh"]').text()).toContain('中文')
  })

  it('should switch to English and verify integration', async () => {
    // First switch to Chinese
    const wrapper = mount(LanguageSwitcher)

    let zhButton = wrapper.find('[data-testid="lang-zh"]')
    await zhButton.trigger('click')
    await wrapper.vm.$nextTick()

    // Verify initial state is Chinese
    expect(mockI18nState.currentLanguageRef.value).toBe('zh')

    // Now click English
    const enButton = wrapper.find('[data-testid="lang-en"]')
    await enButton.trigger('click')

    // Verify setLanguage was called with 'en'
    expect(mockI18nState.setLanguage).toHaveBeenCalledWith('en')

    // Verify localStorage was updated with 'en'
    expect(setItemSpy).toHaveBeenCalledWith('procmesh_language', 'en')

    // Verify reactive state changed
    expect(mockI18nState.currentLanguageRef.value).toBe('en')

    // Verify UI updated: English button now active, Chinese not
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="lang-en"]').classes()).toContain('active')
    expect(wrapper.find('[data-testid="lang-zh"]').classes()).not.toContain('active')
    expect(wrapper.find('[data-testid="lang-en"]').text()).toContain('English')
  })
})
