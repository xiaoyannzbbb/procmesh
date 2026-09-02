import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import FreshnessBadge from "./FreshnessBadge.vue";
import I18NextVue from 'i18next-vue';
import i18next from 'i18next';

const nowMs = 1_700_000_010_000;

describe("FreshnessBadge", () => {
  it("renders STALE without green", async () => {
    const i18n = i18next.createInstance()
    await i18n.init({
      lng: 'en',
      fallbackLng: 'en',
      resources: {
        en: {
          common: {
            status: {
              stale: 'STALE'
            }
          }
        }
      }
    })

    const wrapper = mount(FreshnessBadge, {
      props: {
        nowMs,
        lastUpdatedUnixMs: 1_700_000_009_000,
        nodeState: "FAILED",
      },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]]
      }
    });
    expect(wrapper.text()).toContain("STALE");
    expect(wrapper.classes()).toContain("freshness-stale");
    expect(wrapper.classes()).not.toContain("freshness-live");
    const html = wrapper.html().toLowerCase();
    expect(html).not.toMatch(/green|#d1fae5|#10a37f|bg-green/);
  });
});

describe('FreshnessBadge i18n', () => {
  const setupI18n = async (lng: string) => {
    const i18n = i18next.createInstance()
    await i18n.init({
      lng,
      fallbackLng: 'en',
      resources: {
        en: {
          common: {
            status: {
              live: 'LIVE',
              stale: 'STALE',
              unknown: 'UNKNOWN'
            }
          }
        },
        zh: {
          common: {
            status: {
              live: '在线',
              stale: '过期',
              unknown: '未知'
            }
          }
        }
      }
    })
    return i18n
  }

  it('should render LIVE status in English', async () => {
    const i18n = await setupI18n('en')

    const wrapper = mount(FreshnessBadge, {
      props: { status: 'LIVE' },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]]
      }
    })

    expect(wrapper.text()).toBe('LIVE')
  })

  it('should render LIVE status in Chinese', async () => {
    const i18n = await setupI18n('zh')

    const wrapper = mount(FreshnessBadge, {
      props: { status: 'LIVE' },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]]
      }
    })

    expect(wrapper.text()).toBe('在线')
  })

  it('should render STALE status in English', async () => {
    const i18n = await setupI18n('en')

    const wrapper = mount(FreshnessBadge, {
      props: { status: 'STALE' },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]]
      }
    })

    expect(wrapper.text()).toBe('STALE')
  })

  it('should render STALE status in Chinese', async () => {
    const i18n = await setupI18n('zh')

    const wrapper = mount(FreshnessBadge, {
      props: { status: 'STALE' },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]]
      }
    })

    expect(wrapper.text()).toBe('过期')
  })

  it('should render UNKNOWN status in English', async () => {
    const i18n = await setupI18n('en')

    const wrapper = mount(FreshnessBadge, {
      props: { status: 'UNKNOWN' },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]]
      }
    })

    expect(wrapper.text()).toBe('UNKNOWN')
  })

  it('should render UNKNOWN status in Chinese', async () => {
    const i18n = await setupI18n('zh')

    const wrapper = mount(FreshnessBadge, {
      props: { status: 'UNKNOWN' },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]]
      }
    })

    expect(wrapper.text()).toBe('未知')
  })
});
