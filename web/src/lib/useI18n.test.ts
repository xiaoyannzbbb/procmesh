import { mount } from '@vue/test-utils'
import { describe, it, expect } from 'vitest'
import { defineComponent } from 'vue'
import i18next from 'i18next'
import { I18NextVue } from './i18n'
import { useI18n } from './useI18n'

// Create a test-specific i18n instance with errors namespace
const testI18n = i18next.createInstance()
testI18n.init({
  lng: 'en',
  fallbackLng: 'en',
  supportedLngs: ['en', 'zh'],
  defaultNS: 'common',
  ns: ['common', 'errors'],
  resources: {
    en: {
      common: {},
      errors: {
        PROCESS_NOT_FOUND: 'Process {{name}} not found',
        INVALID_CREDENTIALS: 'Invalid username or password',
        CONFLICT: 'Configuration conflict: expected revision {{expected}}, got {{actual}}',
        UNAVAILABLE: 'Target agent {{agent}} is unavailable',
        DENIED: 'Permission denied: {{action}} requires {{permission}}',
        TIMEOUT: 'Operation timed out after {{seconds}}s',
        DUPLICATE_NODE_ID: 'Node ID {{nodeId}} already exists',
        DEGRADED: 'Agent is in degraded mode: {{reason}}',
        UNKNOWN_ERROR: 'An unexpected error occurred',
      },
    },
    zh: {
      common: {},
      errors: {
        PROCESS_NOT_FOUND: '未找到进程 {{name}}',
        INVALID_CREDENTIALS: '用户名或密码无效',
        CONFLICT: '配置冲突：期望版本 {{expected}}，实际版本 {{actual}}',
        UNAVAILABLE: '目标代理 {{agent}} 不可用',
        DENIED: '权限被拒绝：{{action}} 需要 {{permission}} 权限',
        TIMEOUT: '操作超时（{{seconds}}秒）',
        DUPLICATE_NODE_ID: '节点 ID {{nodeId}} 已存在',
        DEGRADED: '代理处于降级模式：{{reason}}',
        UNKNOWN_ERROR: '发生未知错误',
      },
    },
  },
  interpolation: {
    escapeValue: false,
  },
})

describe('useI18n', () => {
  describe('tError', () => {
    it('should translate known error codes in English', async () => {
      await testI18n.changeLanguage('en')

      const Component = defineComponent({
        setup() {
          const { tError } = useI18n()
          return { message: tError('PROCESS_NOT_FOUND', 'Process not found', { name: 'nginx' }) }
        },
        template: '<div>{{ message }}</div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.message).toBe('Process nginx not found')
    })

    it('should translate known error codes in Chinese', async () => {
      await testI18n.changeLanguage('zh')

      const Component = defineComponent({
        setup() {
          const { tError } = useI18n()
          return { message: tError('PROCESS_NOT_FOUND', 'Process not found', { name: 'nginx' }) }
        },
        template: '<div>{{ message }}</div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.message).toBe('未找到进程 nginx')
    })

    it('should use fallback for unknown error codes', () => {
      const Component = defineComponent({
        setup() {
          const { tError } = useI18n()
          return { message: tError('UNKNOWN_CODE', 'Fallback message', {}) }
        },
        template: '<div>{{ message }}</div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.message).toBe('Fallback message')
    })

    it('should interpolate multiple parameters', async () => {
      await testI18n.changeLanguage('en')

      const Component = defineComponent({
        setup() {
          const { tError } = useI18n()
          return { message: tError('CONFLICT', 'Conflict', { expected: '5', actual: '3' }) }
        },
        template: '<div>{{ message }}</div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.message).toBe('Configuration conflict: expected revision 5, got 3')
    })
  })

  describe('setLanguage', () => {
    it('should change language and persist to localStorage', async () => {
      const Component = defineComponent({
        setup() {
          const { setLanguage } = useI18n()
          return { setLanguage }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      await wrapper.vm.setLanguage('zh')
      expect(testI18n.language).toBe('zh')
      expect(localStorage.getItem('procmesh_language')).toBe('zh')
    })
  })
})
