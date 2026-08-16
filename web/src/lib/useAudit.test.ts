import { mount } from '@vue/test-utils'
import { describe, it, expect } from 'vitest'
import { defineComponent } from 'vue'
import i18next from 'i18next'
import { I18NextVue } from './i18n'
import { useAudit } from './useAudit'

// Create a test-specific i18n instance with audit namespace
const testI18n = i18next.createInstance()
testI18n.init({
  lng: 'en',
  fallbackLng: 'en',
  supportedLngs: ['en', 'zh'],
  defaultNS: 'common',
  ns: ['common', 'audit'],
  resources: {
    en: {
      common: {},
      audit: {
        'action.LOGIN': 'User logged in',
        'action.LOGOUT': 'User logged out',
        'action.PROCESS_START': 'Started process {{name}}',
        'action.PROCESS_STOP': 'Stopped process {{name}}',
        'action.CONFIG_UPDATE': 'Updated configuration for {{name}} (rev {{revision}})',
        'result.SUCCESS': 'Success',
        'result.FAILED': 'Failed',
        'result.TIMEOUT': 'Timeout',
      },
    },
    zh: {
      common: {},
      audit: {
        'action.LOGIN': '用户登录',
        'action.LOGOUT': '用户登出',
        'action.PROCESS_START': '启动进程 {{name}}',
        'action.PROCESS_STOP': '停止进程 {{name}}',
        'action.CONFIG_UPDATE': '更新 {{name}} 配置（版本 {{revision}}）',
        'result.SUCCESS': '成功',
        'result.FAILED': '失败',
        'result.TIMEOUT': '超时',
      },
    },
  },
  interpolation: {
    escapeValue: false,
  },
})

describe('useAudit', () => {
  describe('formatAuditAction', () => {
    it('should format simple actions in English', async () => {
      await testI18n.changeLanguage('en')

      const Component = defineComponent({
        setup() {
          const { formatAuditAction } = useAudit()
          return {
            result1: formatAuditAction('LOGIN', {}),
            result2: formatAuditAction('LOGOUT', {}),
          }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.result1).toBe('User logged in')
      expect(wrapper.vm.result2).toBe('User logged out')
    })

    it('should format simple actions in Chinese', async () => {
      await testI18n.changeLanguage('zh')

      const Component = defineComponent({
        setup() {
          const { formatAuditAction } = useAudit()
          return {
            result1: formatAuditAction('LOGIN', {}),
            result2: formatAuditAction('LOGOUT', {}),
          }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.result1).toBe('用户登录')
      expect(wrapper.vm.result2).toBe('用户登出')
    })

    it('should interpolate parameters in English', async () => {
      await testI18n.changeLanguage('en')

      const Component = defineComponent({
        setup() {
          const { formatAuditAction } = useAudit()
          return { result: formatAuditAction('PROCESS_START', { name: 'nginx' }) }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.result).toBe('Started process nginx')
    })

    it('should interpolate parameters in Chinese', async () => {
      await testI18n.changeLanguage('zh')

      const Component = defineComponent({
        setup() {
          const { formatAuditAction } = useAudit()
          return { result: formatAuditAction('PROCESS_START', { name: 'nginx' }) }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.result).toBe('启动进程 nginx')
    })

    it('should handle multiple parameters', async () => {
      await testI18n.changeLanguage('en')

      const Component = defineComponent({
        setup() {
          const { formatAuditAction } = useAudit()
          return {
            result: formatAuditAction('CONFIG_UPDATE', {
              name: 'web-server',
              revision: '42',
            }),
          }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.result).toBe('Updated configuration for web-server (rev 42)')
    })

    it('should handle unknown actions gracefully', () => {
      const Component = defineComponent({
        setup() {
          const { formatAuditAction } = useAudit()
          return { result: formatAuditAction('UNKNOWN_ACTION', {}) }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.result).toBe('UNKNOWN_ACTION')
    })
  })

  describe('formatAuditResult', () => {
    it('should translate result codes', async () => {
      await testI18n.changeLanguage('en')

      const Component = defineComponent({
        setup() {
          const { formatAuditResult } = useAudit()
          return {
            result1: formatAuditResult('SUCCESS'),
            result2: formatAuditResult('FAILED'),
            result3: formatAuditResult('TIMEOUT'),
          }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.result1).toBe('Success')
      expect(wrapper.vm.result2).toBe('Failed')
      expect(wrapper.vm.result3).toBe('Timeout')
    })

    it('should translate result codes in Chinese', async () => {
      await testI18n.changeLanguage('zh')

      const Component = defineComponent({
        setup() {
          const { formatAuditResult } = useAudit()
          return {
            result1: formatAuditResult('SUCCESS'),
            result2: formatAuditResult('FAILED'),
            result3: formatAuditResult('TIMEOUT'),
          }
        },
        template: '<div></div>',
      })

      const wrapper = mount(Component, {
        global: {
          plugins: [[I18NextVue, { i18next: testI18n }]],
        },
      })

      expect(wrapper.vm.result1).toBe('成功')
      expect(wrapper.vm.result2).toBe('失败')
      expect(wrapper.vm.result3).toBe('超时')
    })
  })
})
