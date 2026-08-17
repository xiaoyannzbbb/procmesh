import { Code, ConnectError } from "@connectrpc/connect";
import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRouter, createMemoryHistory } from "vue-router";
import ProcessDetailPage from "./ProcessDetailPage.vue";

const GOSSIP_CPU = 73;

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

const processDetailEn = {
  back: "← Processes",
  loading: "Loading…",
  actions: {
    start: "Start",
    stop: "Stop",
    restart: "Restart",
    forceStop: "Force Stop",
  },
  tabs: {
    overview: "Overview",
    config: "Config",
    logs: "Logs",
  },
  process: {
    title: "Process",
    name: "Name",
    processId: "Process ID",
  },
  instances: {
    title: "Instances",
  },
};

function sampleProcess() {
  return {
    processId: "proc-1",
    spec: { name: "test-process", ownerAgentId: "node1", latestRevision: 1 },
    instances: [
      {
        instanceId: "inst-1",
        desired: "RUNNING",
        observed: "RUNNING",
        health: "HEALTHY",
        pid: 42,
        activeRevision: 1,
      },
    ],
  };
}

function gappedHistory() {
  return {
    processId: "proc-1",
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

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {},
      },
    },
  });
});

const mounted: Array<{ unmount: () => void }> = [];

async function mountProcessDetailPage(
  process: unknown = null,
  nodes: unknown[] = [],
  history?: { getProcessHistory?: ReturnType<typeof vi.fn>; getProcessMetrics?: ReturnType<typeof vi.fn> },
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const processClient = {
    listProcesses: vi.fn().mockResolvedValue({ processes: [] }),
    getProcess: vi.fn().mockResolvedValue({ process }),
    startProcess: vi.fn().mockResolvedValue({}),
    stopProcess: vi.fn().mockResolvedValue({}),
    restartProcess: vi.fn().mockResolvedValue({}),
    killProcess: vi.fn().mockResolvedValue({}),
  };
  const nodeClient = {
    listNodes: vi.fn().mockResolvedValue({ nodes }),
  };
  const metricsClient = {
    getProcessMetrics:
      history?.getProcessMetrics ??
      vi.fn().mockResolvedValue({
        metrics: [{ instanceId: "inst-1", cpuPercent: GOSSIP_CPU, memoryBytes: 1024 }],
      }),
    getProcessHistory:
      history?.getProcessHistory ?? vi.fn().mockResolvedValue({ processId: "", layer: "raw_min", series: [] }),
  };

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: "/processes/:idOrName",
        component: ProcessDetailPage,
      },
    ],
  });

  await router.push("/processes/test-process?node=node1");
  await router.isReady();

  const wrapper = mount(ProcessDetailPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
        router,
      ],
      provide: { processClient, nodeClient, metricsClient },
      stubs: {
        RouterLink: {
          template: '<a><slot /></a>',
        },
        ProcessConfigPanel: {
          template: '<div>ProcessConfigPanel</div>',
        },
        ProcessLogsPanel: {
          template: '<div>ProcessLogsPanel</div>',
        },
      },
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

describe("ProcessDetailPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      processDetail: {
        back: "← Processes",
        loading: "Loading…",
        actions: {
          start: "Start",
          stop: "Stop",
          restart: "Restart",
          forceStop: "Force Stop",
        },
        tabs: {
          overview: "Overview",
          config: "Config",
          logs: "Logs",
        },
        process: {
          title: "Process",
          name: "Name",
          processId: "Process ID",
        },
        instances: {
          title: "Instances",
        },
      },
    });

    const wrapper = await mountProcessDetailPage();
    const text = wrapper.text();
    expect(text).toContain("← Processes");
    expect(text).toContain("Start");
    expect(text).toContain("Stop");
    expect(text).toContain("Restart");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      processDetail: {
        back: "← 进程",
        loading: "加载中…",
        actions: {
          start: "启动",
          stop: "停止",
          restart: "重启",
          forceStop: "强制停止",
        },
        tabs: {
          overview: "概览",
          config: "配置",
          logs: "日志",
        },
        process: {
          title: "进程",
          name: "名称",
          processId: "进程ID",
        },
        instances: {
          title: "实例",
        },
      },
    });

    const wrapper = await mountProcessDetailPage();
    const text = wrapper.text();
    expect(text).toContain("← 进程");
    expect(text).toContain("启动");
    expect(text).toContain("停止");
    expect(text).toContain("重启");
  });
});

describe("ProcessDetailPage history", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      processDetail: processDetailEn,
      metricsHistory: metricsHistoryEn,
      status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
    });
  });

  it("shows 24h/7d and requests more than a day after clicking 7d", async () => {
    const getProcessHistory = vi.fn().mockResolvedValue(gappedHistory());
    const wrapper = await mountProcessDetailPage(sampleProcess(), [], { getProcessHistory });

    expect(wrapper.text()).toContain("24h");
    expect(wrapper.text()).toContain("7d");

    const sevenDay = wrapper.findAll("button").find((b) => b.text().trim() === "7d");
    expect(sevenDay).toBeTruthy();
    await sevenDay!.trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    await flushPromises();

    expect(getProcessHistory.mock.calls.length).toBeGreaterThanOrEqual(2);
    const last = getProcessHistory.mock.calls.at(-1)?.[0] as { sinceUnix: bigint | number; untilUnix: bigint | number };
    expect(Number(last.untilUnix) - Number(last.sinceUnix)).toBeGreaterThan(86400);
    const opts = getProcessHistory.mock.calls.at(-1)?.[1] as { headers?: Record<string, string> };
    expect(opts?.headers?.["Procmesh-Target-Node"]).toBe("node1");
  });

  it("shows stale copy on UNAVAILABLE and does not draw Gossip CPU in SVG", async () => {
    const getProcessHistory = vi.fn().mockRejectedValue(new ConnectError("UNAVAILABLE", Code.Unavailable));
    const wrapper = await mountProcessDetailPage(sampleProcess(), [], { getProcessHistory });

    expect(wrapper.text()).toContain(STALE_COPY);
    expect(wrapper.text()).toContain(`${GOSSIP_CPU}%`);
    for (const svg of wrapper.findAll("svg")) {
      expect(svg.html()).not.toContain(String(GOSSIP_CPU));
    }
    expect(wrapper.findAll("polyline")).toHaveLength(0);
  });
});
