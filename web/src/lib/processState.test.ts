import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { join } from 'path'

describe('Process State Translation', () => {
  it('English process.json has correct structure', () => {
    const enPath = join(process.cwd(), 'public/locales/en/process.json')
    const enContent = readFileSync(enPath, 'utf-8')
    const data = JSON.parse(enContent)

    expect(data).toHaveProperty('desiredState')
    expect(data).toHaveProperty('observedState')
    expect(data).toHaveProperty('healthState')
    expect(data).toHaveProperty('labels')

    expect(data.observedState.RUNNING).toBe('Running')
    expect(data.observedState.STOPPED).toBe('Stopped')
    expect(data.observedState.FATAL).toBe('Fatal')

    expect(data.healthState.HEALTHY).toBe('Healthy')
    expect(data.healthState.UNHEALTHY).toBe('Unhealthy')

    expect(data.labels.name).toBe('Name')
    expect(data.labels.state).toBe('State')
    expect(data.labels.health).toBe('Health')
  })

  it('Chinese process.json has correct structure', () => {
    const zhPath = join(process.cwd(), 'public/locales/zh/process.json')
    const zhContent = readFileSync(zhPath, 'utf-8')
    const data = JSON.parse(zhContent)

    expect(data).toHaveProperty('desiredState')
    expect(data).toHaveProperty('observedState')
    expect(data).toHaveProperty('healthState')
    expect(data).toHaveProperty('labels')

    expect(data.observedState.RUNNING).toBe('运行中')
    expect(data.observedState.STOPPED).toBe('已停止')
    expect(data.observedState.FATAL).toBe('致命错误')

    expect(data.healthState.HEALTHY).toBe('健康')
    expect(data.healthState.UNHEALTHY).toBe('不健康')

    expect(data.labels.name).toBe('名称')
    expect(data.labels.state).toBe('状态')
    expect(data.labels.health).toBe('健康状态')
  })

  it('en and zh have same top-level keys', () => {
    const enPath = join(process.cwd(), 'public/locales/en/process.json')
    const enContent = readFileSync(enPath, 'utf-8')
    const enData = JSON.parse(enContent)

    const zhPath = join(process.cwd(), 'public/locales/zh/process.json')
    const zhContent = readFileSync(zhPath, 'utf-8')
    const zhData = JSON.parse(zhContent)

    const enKeys = Object.keys(enData).sort()
    const zhKeys = Object.keys(zhData).sort()

    expect(enKeys).toEqual(zhKeys)
  })

  it('English and Chinese have same nested keys for each section', () => {
    const enPath = join(process.cwd(), 'public/locales/en/process.json')
    const enContent = readFileSync(enPath, 'utf-8')
    const enData = JSON.parse(enContent)

    const zhPath = join(process.cwd(), 'public/locales/zh/process.json')
    const zhContent = readFileSync(zhPath, 'utf-8')
    const zhData = JSON.parse(zhContent)

    const checkNesting = (enObj: any, zhObj: any) => {
      for (const key in enObj) {
        expect(zhObj).toHaveProperty(key)
        if (typeof enObj[key] === 'object') {
          checkNesting(enObj[key], zhObj[key])
        }
      }
    }

    checkNesting(enData, zhData)
  })
})
