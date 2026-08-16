import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'
import LanguageSwitcher from './LanguageSwitcher.vue'

vi.mock('../lib/useI18n', () => ({
  useI18n: () => ({
    currentLanguage: ref('en'),
    setLanguage: vi.fn(async (lang: string) => {
      // Mock implementation
      localStorage.setItem('procmesh_language', lang)
    })
  })
}))

describe('LanguageSwitcher', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
  })

  it('should display current language', () => {
    const wrapper = mount(LanguageSwitcher)
    expect(wrapper.text()).toContain('English')
  })

  it('should switch to Chinese when clicked', async () => {
    const wrapper = mount(LanguageSwitcher)
    const button = wrapper.find('[data-testid="lang-zh"]')
    await button.trigger('click')

    expect(localStorage.getItem('procmesh_language')).toBe('zh')
  })

  it('should switch to English when clicked', async () => {
    const wrapper = mount(LanguageSwitcher)
    const button = wrapper.find('[data-testid="lang-en"]')
    await button.trigger('click')

    expect(localStorage.getItem('procmesh_language')).toBe('en')
  })
})
