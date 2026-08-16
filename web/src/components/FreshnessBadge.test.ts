import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import FreshnessBadge from "./FreshnessBadge.vue";
import I18NextVue from 'i18next-vue';
import i18next from 'i18next';

const nowMs = 1_700_000_010_000;

function hexOrRgb(value: string): boolean {
  const v = value.replace(/\s/g, "").toLowerCase();
  return v === "#fef3c7" || v === "rgb(254,243,199)" || v === "rgba(254,243,199,1)";
}

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
    const html = wrapper.html().toLowerCase();
    expect(html).not.toMatch(/green|#d1fae5|#10a37f|bg-green/);
    const el = wrapper.get(".freshness-stale").element as HTMLElement;
    const computed = getComputedStyle(el).backgroundColor;
    const inline = el.style.backgroundColor;
    const bg = hexOrRgb(computed) ? computed : inline;
    expect(hexOrRgb(bg), `background=${computed}/${inline}`).toBe(true);
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
