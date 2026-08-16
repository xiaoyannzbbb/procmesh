import { describe, it, expect, beforeEach } from 'vitest'
import i18next from 'i18next'
import { I18NextVue } from './i18n'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

// Create a test-specific i18n instance with process namespace
const testI18n = i18next.createInstance()
testI18n.init({
  lng: 'en',
  fallbackLng: 'en',
  supportedLngs: ['en', 'zh'],
  defaultNS: 'common',
  ns: ['common', 'process'],
  resources: {
    en: {
      common: {},
      process: {
        desiredState: {
          RUNNING: 'Running',
          STOPPED: 'Stopped',
        },
        observedState: {
          STOPPED: 'Stopped',
          STARTING: 'Starting',
          RUNNING: 'Running',
          STOPPING: 'Stopping',
          EXITED: 'Exited',
          BACKOFF: 'Backoff',
          FATAL: 'Fatal',
          UNKNOWN: 'Unknown',
        },
        healthState: {
          HEALTHY: 'Healthy',
          UNHEALTHY: 'Unhealthy',
          UNKNOWN: 'Unknown',
        },
        labels: {
          name: 'Name',
          state: 'State',
          health: 'Health',
          uptime: 'Uptime',
          restarts: 'Restarts',
          pid: 'PID',
          cpu: 'CPU',
          memory: 'Memory',
        },
      },
    },
    zh: {
      common: {},
      process: {
        desiredState: {
          RUNNING: '运行中',
          STOPPED: '已停止',
        },
        observedState: {
          STOPPED: '已停止',
          STARTING: '启动中',
          RUNNING: '运行中',
          STOPPING: '停止中',
          EXITED: '已退出',
          BACKOFF: '退避',
          FATAL: '致命错误',
          UNKNOWN: '未知',
        },
        healthState: {
          HEALTHY: '健康',
          UNHEALTHY: '不健康',
          UNKNOWN: '未知',
        },
        labels: {
          name: '名称',
          state: '状态',
          health: '健康状态',
          uptime: '运行时间',
          restarts: '重启次数',
          pid: 'PID',
          cpu: 'CPU',
          memory: '内存',
        },
      },
    },
  },
  interpolation: {
    escapeValue: false,
  },
})

describe('Process State Translation', () => {
  it('should translate observed states in English', async () => {
    await testI18n.changeLanguage('en')

    const Component = defineComponent({
      setup() {
        return {
          running: testI18n.t('process:observedState.RUNNING'),
          stopped: testI18n.t('process:observedState.STOPPED'),
          fatal: testI18n.t('process:observedState.FATAL'),
          starting: testI18n.t('process:observedState.STARTING'),
        }
      },
      template: '<div></div>',
    })

    const wrapper = mount(Component, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]],
      },
    })

    expect(wrapper.vm.running).toBe('Running')
    expect(wrapper.vm.stopped).toBe('Stopped')
    expect(wrapper.vm.fatal).toBe('Fatal')
    expect(wrapper.vm.starting).toBe('Starting')
  })

  it('should translate observed states in Chinese', async () => {
    await testI18n.changeLanguage('zh')

    const Component = defineComponent({
      setup() {
        return {
          running: testI18n.t('process:observedState.RUNNING'),
          stopped: testI18n.t('process:observedState.STOPPED'),
          fatal: testI18n.t('process:observedState.FATAL'),
          starting: testI18n.t('process:observedState.STARTING'),
        }
      },
      template: '<div></div>',
    })

    const wrapper = mount(Component, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]],
      },
    })

    expect(wrapper.vm.running).toBe('运行中')
    expect(wrapper.vm.stopped).toBe('已停止')
    expect(wrapper.vm.fatal).toBe('致命错误')
    expect(wrapper.vm.starting).toBe('启动中')
  })

  it('should translate desired states in English', async () => {
    await testI18n.changeLanguage('en')

    const Component = defineComponent({
      setup() {
        return {
          running: testI18n.t('process:desiredState.RUNNING'),
          stopped: testI18n.t('process:desiredState.STOPPED'),
        }
      },
      template: '<div></div>',
    })

    const wrapper = mount(Component, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]],
      },
    })

    expect(wrapper.vm.running).toBe('Running')
    expect(wrapper.vm.stopped).toBe('Stopped')
  })

  it('should translate desired states in Chinese', async () => {
    await testI18n.changeLanguage('zh')

    const Component = defineComponent({
      setup() {
        return {
          running: testI18n.t('process:desiredState.RUNNING'),
          stopped: testI18n.t('process:desiredState.STOPPED'),
        }
      },
      template: '<div></div>',
    })

    const wrapper = mount(Component, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]],
      },
    })

    expect(wrapper.vm.running).toBe('运行中')
    expect(wrapper.vm.stopped).toBe('已停止')
  })

  it('should translate health states in English', async () => {
    await testI18n.changeLanguage('en')

    const Component = defineComponent({
      setup() {
        return {
          healthy: testI18n.t('process:healthState.HEALTHY'),
          unhealthy: testI18n.t('process:healthState.UNHEALTHY'),
        }
      },
      template: '<div></div>',
    })

    const wrapper = mount(Component, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]],
      },
    })

    expect(wrapper.vm.healthy).toBe('Healthy')
    expect(wrapper.vm.unhealthy).toBe('Unhealthy')
  })

  it('should translate health states in Chinese', async () => {
    await testI18n.changeLanguage('zh')

    const Component = defineComponent({
      setup() {
        return {
          healthy: testI18n.t('process:healthState.HEALTHY'),
          unhealthy: testI18n.t('process:healthState.UNHEALTHY'),
        }
      },
      template: '<div></div>',
    })

    const wrapper = mount(Component, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]],
      },
    })

    expect(wrapper.vm.healthy).toBe('健康')
    expect(wrapper.vm.unhealthy).toBe('不健康')
  })

  it('should translate process labels in English', async () => {
    await testI18n.changeLanguage('en')

    const Component = defineComponent({
      setup() {
        return {
          name: testI18n.t('process:labels.name'),
          state: testI18n.t('process:labels.state'),
          health: testI18n.t('process:labels.health'),
        }
      },
      template: '<div></div>',
    })

    const wrapper = mount(Component, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]],
      },
    })

    expect(wrapper.vm.name).toBe('Name')
    expect(wrapper.vm.state).toBe('State')
    expect(wrapper.vm.health).toBe('Health')
  })

  it('should translate process labels in Chinese', async () => {
    await testI18n.changeLanguage('zh')

    const Component = defineComponent({
      setup() {
        return {
          name: testI18n.t('process:labels.name'),
          state: testI18n.t('process:labels.state'),
          health: testI18n.t('process:labels.health'),
        }
      },
      template: '<div></div>',
    })

    const wrapper = mount(Component, {
      global: {
        plugins: [[I18NextVue, { i18next: testI18n }]],
      },
    })

    expect(wrapper.vm.name).toBe('名称')
    expect(wrapper.vm.state).toBe('状态')
    expect(wrapper.vm.health).toBe('健康状态')
  })
})
