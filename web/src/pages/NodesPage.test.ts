import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import NodesPage from "./NodesPage.vue";

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

async function mountNodesPage(nodes: any[] = []) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const nodeClient = { listNodes: vi.fn().mockResolvedValue({ nodes }) };
  const wrapper = mount(NodesPage, {
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

describe("NodesPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      nodes: {
        title: "Nodes",
        loading: "Loading…",
        noNodes: "No nodes",
        table: {
          hostname: "Hostname",
          nodeId: "Node ID",
          state: "State",
          version: "Version",
          resources: "Resources",
          processes: "Processes",
          freshness: "Freshness",
          updated: "Updated",
        },
      },
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });

    const wrapper = await mountNodesPage([]);
    const text = wrapper.text();
    expect(text).toContain("Nodes");
    expect(text).toContain("Hostname");
    expect(text).toContain("Node ID");
    expect(text).toContain("No nodes");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      nodes: {
        title: "节点",
        loading: "加载中…",
        noNodes: "无节点",
        table: {
          hostname: "主机名",
          nodeId: "节点ID",
          state: "状态",
          version: "版本",
          resources: "资源",
          processes: "进程",
          freshness: "新鲜度",
          updated: "更新时间",
        },
      },
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });

    const wrapper = await mountNodesPage([]);
    const text = wrapper.text();
    expect(text).toContain("节点");
    expect(text).toContain("主机名");
    expect(text).toContain("节点ID");
    expect(text).toContain("无节点");
  });
});
