import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ProcessesPage from "./ProcessesPage.vue";

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
        },
      },
    },
  });
});

const mounted: Array<{ unmount: () => void }> = [];

async function mountProcessesPage(nodes: unknown[] = []) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const nodeClient = { listNodes: vi.fn().mockResolvedValue({ nodes }) };
  const wrapper = mount(ProcessesPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { nodeClient },
      stubs: {
        RouterLink: {
          template: '<a><slot /></a>',
        },
      },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return wrapper;
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
});

describe("ProcessesPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      processes: {
        title: "Processes",
        loading: "Loading…",
        noProcesses: "No processes",
        table: {
          name: "Name",
          owner: "Owner",
          desired: "Desired",
          observed: "Observed",
          health: "Health",
          revisions: "Revisions",
          freshness: "Freshness",
        },
      },
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });

    const wrapper = await mountProcessesPage([]);
    const text = wrapper.text();
    expect(text).toContain("Processes");
    expect(text).toContain("Name");
    expect(text).toContain("Owner");
    expect(text).toContain("No processes");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      processes: {
        title: "进程",
        loading: "加载中…",
        noProcesses: "无进程",
        table: {
          name: "名称",
          owner: "所有者",
          desired: "期望状态",
          observed: "观察状态",
          health: "健康状态",
          revisions: "版本",
          freshness: "新鲜度",
        },
      },
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });

    const wrapper = await mountProcessesPage([]);
    const text = wrapper.text();
    expect(text).toContain("进程");
    expect(text).toContain("名称");
    expect(text).toContain("所有者");
    expect(text).toContain("无进程");
  });
});
