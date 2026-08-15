import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import OverviewPage from "./OverviewPage.vue";

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

async function mountOverview() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const clusterClient = { overview: vi.fn().mockResolvedValue(overview) };
  const wrapper = mount(OverviewPage, {
    global: {
      plugins: [[VueQueryPlugin, { queryClient }]],
      provide: { clusterClient },
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

describe("OverviewPage", () => {
  it("renders ProcMesh and Workload sections from mock overview", async () => {
    const wrapper = await mountOverview();
    const text = wrapper.text();
    expect(text).toContain("ProcMesh");
    expect(text).toContain("Workload");
  });

  it("shows No quorum when control_quorum is false", async () => {
    const wrapper = await mountOverview();
    expect(wrapper.text()).toContain("No quorum");
    const quorum = wrapper.get(".quorum");
    expect(quorum.classes()).toContain("danger");
    expect(quorum.text()).toContain("No quorum");
  });

  it("does not describe agent degraded as process down", async () => {
    const wrapper = await mountOverview();
    const text = wrapper.text();
    expect(text).toContain("Agent DEGRADED — local store impaired; business processes are not stopped.");
    expect(text.toLowerCase()).not.toMatch(/process (down|fault|failure)/);
    expect(text).not.toMatch(/Process 故障/);
  });
});
