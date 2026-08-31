import { Code, ConnectError } from "@connectrpc/connect";
import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import NodeDetailPage from "./NodeDetailPage.vue";

const Blank = defineComponent({ setup: () => () => h("div") });

const GOSSIP_CPU = 87;

const STALE_COPY = "History unavailable (STALE). Live gossip summary is not a chart.";

const metricsHistoryEn = {
  title: "History",
  range24h: "24h",
  range7d: "7d",
  cpu: "CPU %",
  memory: "Memory",
  disk: "Disk %",
  empty: "No samples in this range",
  stale: STALE_COPY,
  loading: "Loading history…",
};

const nodeDetailEn = {
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
};

function gappedHistory() {
  return {
    nodeId: "a0ba0978-70ed-4664-8d80-133c6c862f86",
    layer: "raw_min",
    series: [
      {
        name: "cpu_percent",
        layer: "raw_min",
        points: [
          { tsUnix: 1_700_000_000n, value: 10 },
          { tsUnix: 1_700_000_060n, value: 11 },
          { tsUnix: 1_700_000_180n, value: 12 },
        ],
      },
    ],
  };
}

function sampleNode(overrides: Record<string, unknown> = {}) {
  return {
    nodeId: "a0ba0978-70ed-4664-8d80-133c6c862f86",
    hostname: "agent-a",
    state: "ALIVE",
    lastUpdatedUnixMs: Date.now(),
    resources: { cpuPercent: GOSSIP_CPU, memoryPercent: 21, diskPercent: 15 },
    processes: [],
    ...overrides,
  };
}

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

async function mountNodeDetailPage(
  node: unknown = null,
  history?: { getNodeHistory?: ReturnType<typeof vi.fn> },
) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/nodes", component: Blank },
      { path: "/nodes/:id", component: NodeDetailPage },
      { path: "/processes/new", component: Blank },
      { path: "/processes/:idOrName", component: Blank },
      { path: "/updates", component: Blank },
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
  const getNodeHistory =
    history?.getNodeHistory ?? vi.fn().mockResolvedValue({ nodeId: "", layer: "raw_min", series: [] });
  const metricsClient = { getNodeHistory };
  const wrapper = mount(NodeDetailPage, {
    global: {
      plugins: [
        router,
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { nodeClient, metricsClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
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

  it("links the agent version to /updates?node= and does not offer apply", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      nodeDetail: nodeDetailEn,
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });

    const wrapper = await mountNodeDetailPage(
      sampleNode({
        agentVersion: "0.1.27",
      }),
    );

    const link = wrapper.get('a[href="/updates?node=a0ba0978-70ed-4664-8d80-133c6c862f86"]');
    expect(link.text()).toBe("0.1.27");
    expect(wrapper.find('[data-action="update"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="update-cluster"]').exists()).toBe(false);
    expect(wrapper.findAll("button").filter((b) => /update|apply/i.test(b.text()))).toHaveLength(0);
  });
});

describe("NodeDetailPage history", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      nodeDetail: nodeDetailEn,
      metricsHistory: metricsHistoryEn,
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });
  });

  it("shows 24h/7d and requests more than a day after clicking 7d", async () => {
    const getNodeHistory = vi.fn().mockResolvedValue(gappedHistory());
    const wrapper = await mountNodeDetailPage(sampleNode(), { getNodeHistory });

    expect(wrapper.text()).toContain("24h");
    expect(wrapper.text()).toContain("7d");

    const sevenDay = wrapper.findAll("button").find((b) => b.text().trim() === "7d");
    expect(sevenDay).toBeTruthy();
    await sevenDay!.trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    await flushPromises();

    expect(getNodeHistory.mock.calls.length).toBeGreaterThanOrEqual(2);
    const last = getNodeHistory.mock.calls.at(-1)?.[0] as { sinceUnix: bigint | number; untilUnix: bigint | number };
    expect(Number(last.untilUnix) - Number(last.sinceUnix)).toBeGreaterThan(86400);
  });

  it("shows stale copy on UNAVAILABLE and does not draw Gossip CPU in SVG", async () => {
    const getNodeHistory = vi.fn().mockRejectedValue(new ConnectError("UNAVAILABLE", Code.Unavailable));
    const wrapper = await mountNodeDetailPage(sampleNode(), { getNodeHistory });

    expect(wrapper.text()).toContain(STALE_COPY);
    expect(wrapper.text()).toContain(`${GOSSIP_CPU}%`);
    for (const svg of wrapper.findAll("svg")) {
      expect(svg.html()).not.toContain(String(GOSSIP_CPU));
    }
    expect(wrapper.findAll("polyline")).toHaveLength(0);
  });
});
