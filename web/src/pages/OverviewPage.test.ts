import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import OverviewPage from "./OverviewPage.vue";

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
          overview: {
            title: "Overview",
            loading: "Loading…",
            cluster: "Cluster",
            procMesh: {
              title: "ProcMesh",
              controlQuorum: "Control quorum",
              gossip: "Gossip",
              rpc: "RPC",
              certExpiry: "Cert expiry",
              caExpiry: "CA expiry",
              controlLeader: "Control leader",
              healthy: "healthy",
              unhealthy: "unhealthy",
              versions: "Versions",
            },
            workload: {
              title: "Workload",
              agentTotal: "Agent Total",
              alive: "Alive",
              suspect: "Suspect",
              failed: "Failed",
              processTotal: "Process Total",
              running: "Running",
              unhealthy: "Unhealthy",
              fatal: "Fatal",
              cpu: "CPU",
              memory: "Memory",
              disk: "Disk",
            },
            recentBatches: "Recent batches",
            recentBatchesHint: "Only batches created on this entry agent.",
          },
          batch: { timeout: "timeout" },
        },
      },
    },
  });
});

const overview = {
  clusterId: "c1",
  members: 3,
  alive: 2,
  controlQuorum: false,
  controlLeader: "n1",
  suspect: 0,
  failed: 1,
  processTotal: 2,
  processRunning: 1,
  processUnhealthy: 0,
  processFatal: 0,
  cpuPercent: 10,
  memoryPercent: 20,
  diskPercent: 30,
  gossipHealthy: true,
  rpcHealthy: true,
  agentDegraded: true,
  certExpiresUnix: BigInt(1_700_000_000),
  caExpiresUnix: BigInt(1_800_000_000),
  viewUnixMs: BigInt(1_700_000_010_000),
  platformNote:
    "macOS: resource_limit ignored (no cgroup); Host reboot recovery depends on how the Agent is started.",
  versionCounts: { "1.0.0": 3 },
};

const mounted: Array<{ unmount: () => void }> = [];

async function mountOverview(
  overrides: Partial<typeof overview> = {},
  batches: unknown[] = [],
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const clusterClient = { overview: vi.fn().mockResolvedValue({ ...overview, ...overrides }) };
  const batchClient = { listBatches: vi.fn().mockResolvedValue({ batches }) };
  const wrapper = mount(OverviewPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { clusterClient, batchClient },
      stubs: {
        RouterLink: { template: "<a><slot /></a>" },
      },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, batchClient };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
});

describe("OverviewPage", () => {
  it("renders ProcMesh and Workload sections from mock overview", async () => {
    const { wrapper } = await mountOverview();
    const text = wrapper.text();
    expect(text).toContain("ProcMesh");
    expect(text).toContain("Workload");
  });

  it("shows No quorum when control_quorum is false", async () => {
    const { wrapper } = await mountOverview();
    expect(wrapper.text()).toContain("No quorum");
    const quorum = wrapper.get(".quorum");
    expect(quorum.classes()).toContain("danger");
    expect(quorum.text()).toContain("No quorum");
  });

  it("does not describe agent degraded as process down", async () => {
    const { wrapper } = await mountOverview();
    const text = wrapper.text();
    expect(text).toContain("Agent DEGRADED — local store impaired; business processes are not stopped.");
    expect(text.toLowerCase()).not.toMatch(/process (down|fault|failure)/);
    expect(text).not.toMatch(/Process 故障/);
  });

  it("shows STALE badge and last updated for FAILED last-known workload counts", async () => {
    const { wrapper } = await mountOverview();
    const badge = wrapper.get(".freshness-badge");
    expect(badge.text()).toBe("STALE");
    expect(badge.classes()).toContain("freshness-stale");
    expect(badge.classes()).not.toContain("freshness-live");
    const html = wrapper.html().toLowerCase();
    expect(html).not.toMatch(/green|#d1fae5|#10a37f|bg-green/);
    expect(wrapper.text()).toMatch(/\d+[smhd] ago|unknown/);
  });

  it("renders uncollected resources as unknown instead of 0%", async () => {
    const { wrapper } = await mountOverview({ cpuPercent: -1, memoryPercent: -1, diskPercent: -1 });
    const text = wrapper.text();
    expect(text).toContain("unknown");
    expect(text).not.toMatch(/CPU\s*0%/);
    expect(text).not.toMatch(/Memory\s*0%/);
  });

  it("shows recent batches hint and timeout count", async () => {
    const { wrapper, batchClient } = await mountOverview({}, [
      {
        batchId: "b-recent",
        type: "START",
        status: "PARTIAL",
        summary: { success: 1, failed: 0, timeout: 2, denied: 0, conflict: 0, unavailable: 0, invalid: 0 },
      },
    ]);
    expect(batchClient.listBatches).toHaveBeenCalledWith({ limit: 5 });
    expect(wrapper.text()).toContain("Recent batches");
    expect(wrapper.text()).toContain("Only batches created on this entry agent.");
    const timeout = wrapper.get('[data-status="TIMEOUT"]');
    expect(timeout.text()).toContain("2");
    expect(timeout.classes()).toContain("status-timeout");
    expect(timeout.classes()).not.toContain("status-success");
  });
});

describe("OverviewPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      overview: {
        title: "Overview",
        loading: "Loading…",
        cluster: "Cluster",
        procMesh: {
          title: "ProcMesh",
          controlQuorum: "Control quorum",
          gossip: "Gossip",
          rpc: "RPC",
          certExpiry: "Cert expiry",
          caExpiry: "CA expiry",
          controlLeader: "Control leader",
          healthy: "healthy",
          unhealthy: "unhealthy",
          versions: "Versions",
        },
        workload: {
          title: "Workload",
          agentTotal: "Agent Total",
          alive: "Alive",
          suspect: "Suspect",
          failed: "Failed",
          processTotal: "Process Total",
          running: "Running",
          unhealthy: "Unhealthy",
          fatal: "Fatal",
          cpu: "CPU",
          memory: "Memory",
          disk: "Disk",
        },
      },
      status: { stale: "STALE" },
    });

    const { wrapper } = await mountOverview();
    const text = wrapper.text();
    expect(text).toContain("Overview");
    expect(text).toContain("ProcMesh");
    expect(text).toContain("Workload");
    expect(text).toContain("Control quorum");
    expect(text).toContain("Gossip");
    expect(text).toContain("Agent Total");
    expect(text).toContain("Running");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      overview: {
        title: "概览",
        loading: "加载中…",
        cluster: "集群",
        procMesh: {
          title: "ProcMesh",
          controlQuorum: "控制仲裁",
          gossip: "Gossip",
          rpc: "RPC",
          certExpiry: "证书过期时间",
          caExpiry: "CA过期时间",
          controlLeader: "控制领导者",
          healthy: "健康",
          unhealthy: "不健康",
          versions: "版本",
        },
        workload: {
          title: "工作负载",
          agentTotal: "代理总数",
          alive: "存活",
          suspect: "可疑",
          failed: "失败",
          processTotal: "进程总数",
          running: "运行中",
          unhealthy: "不健康",
          fatal: "致命",
          cpu: "CPU",
          memory: "内存",
          disk: "磁盘",
        },
      },
      status: { stale: "STALE" },
    });

    const { wrapper } = await mountOverview();
    const text = wrapper.text();
    expect(text).toContain("概览");
    expect(text).toContain("工作负载");
    expect(text).toContain("控制仲裁");
    expect(text).toContain("代理总数");
    expect(text).toContain("运行中");
  });
});
