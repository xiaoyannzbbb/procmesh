import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import NodeDetailPage from "./NodeDetailPage.vue";

const Blank = defineComponent({ setup: () => () => h("div") });

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

async function mountNodeDetailPage(node: unknown = null) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/nodes", component: Blank },
      { path: "/nodes/:id", component: NodeDetailPage },
      { path: "/processes/:idOrName", component: Blank },
      { path: "/", component: Blank },
    ],
  });
  await router.push("/nodes/test-node-id");
  await router.isReady();

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const nodeClient = {
    getNode: vi.fn().mockResolvedValue({ node }),
    removeNode: vi.fn().mockResolvedValue({}),
  };
  const wrapper = mount(NodeDetailPage, {
    global: {
      plugins: [
        router,
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { nodeClient },
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

describe("NodeDetailPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      nodeDetail: {
        back: "← Nodes",
        loading: "Loading…",
        removeAgent: "Remove Agent",
        node: {
          title: "Node",
          hostname: "Hostname",
          nodeId: "Node ID",
          address: "Address",
          version: "Version",
          status: "Status",
          bootId: "Boot ID",
          cpu: "CPU",
          memory: "Memory",
          disk: "Disk",
          processCount: "Process Count",
          labels: "Labels",
        },
        processes: {
          title: "Processes",
          noProcesses: "No processes",
          table: {
            name: "Name",
            desired: "Desired",
            observed: "Observed",
            health: "Health",
            revisions: "Revisions",
            freshness: "Freshness",
          },
        },
      },
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });

    const wrapper = await mountNodeDetailPage();
    const text = wrapper.text();
    expect(text).toContain("← Nodes");
    expect(text).toContain("Loading…");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      nodeDetail: {
        back: "← 节点",
        loading: "加载中…",
        removeAgent: "移除代理",
        node: {
          title: "节点",
          hostname: "主机名",
          nodeId: "节点ID",
          address: "地址",
          version: "版本",
          status: "状态",
          bootId: "启动ID",
          cpu: "CPU",
          memory: "内存",
          disk: "磁盘",
          processCount: "进程数",
          labels: "标签",
        },
        processes: {
          title: "进程",
          noProcesses: "无进程",
          table: {
            name: "名称",
            desired: "期望状态",
            observed: "观察状态",
            health: "健康状态",
            revisions: "版本",
            freshness: "新鲜度",
          },
        },
      },
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });

    const wrapper = await mountNodeDetailPage();
    const text = wrapper.text();
    expect(text).toContain("← 节点");
    expect(text).toContain("加载中…");
  });
});

describe("NodeDetailPage process list", () => {
  it("links each process name to the process detail page", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      nodeDetail: {
        back: "← Nodes",
        loading: "Loading…",
        removeAgent: "Remove Agent",
        node: {
          title: "Node",
          hostname: "Hostname",
          nodeId: "Node ID",
          address: "Address",
          version: "Version",
          status: "Status",
          bootId: "Boot ID",
          cpu: "CPU",
          memory: "Memory",
          disk: "Disk",
          processCount: "Process Count",
          labels: "Labels",
        },
        processes: {
          title: "Processes",
          noProcesses: "No processes",
          table: {
            name: "Name",
            desired: "Desired",
            observed: "Observed",
            health: "Health",
            revisions: "Revisions",
            freshness: "Freshness",
          },
        },
      },
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });

    const wrapper = await mountNodeDetailPage({
      nodeId: "a0ba0978-70ed-4664-8d80-133c6c862f86",
      hostname: "agent-a",
      state: "ALIVE",
      lastUpdatedUnixMs: Date.now(),
      processes: [
        {
          name: "web-api",
          desired: "RUNNING",
          observed: "RUNNING",
          health: "HEALTHY",
          latestRevision: 2,
          activeRevision: 2,
        },
      ],
    });

    const link = wrapper.get('a[href="/processes/web-api?node=a0ba0978-70ed-4664-8d80-133c6c862f86"]');
    expect(link.text()).toBe("web-api");
  });
});
