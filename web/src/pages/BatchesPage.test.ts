import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import Drawer from "../components/Drawer.vue";
import { session } from "../lib/session";
import BatchesPage from "./BatchesPage.vue";

let i18n: typeof i18next;

const batchI18n = {
  title: "Batches",
  localOnly: "Only batches created on this entry agent are listed.",
  loading: "Loading…",
  noBatches: "No batches",
  create: "Create batch",
  type: "Type",
  status: "Status",
  created: "Created",
  success: "success",
  failed: "failed",
  timeout: "timeout",
  denied: "denied",
  retryFailed: "Retry Failed",
  replayTimeout: "Replay Timeout",
  export: "Export JSON",
  back: "← Batches",
  targets: "Targets",
  batchId: "Batch ID",
  processId: "Process ID",
  processName: "Name",
  nodeId: "Node",
  error: "Error",
  selector: "Selector",
  selectorProcessIds: "Process IDs",
  selectorProcessGroup: "Process group",
  selectorAgentGroup: "Agent group ID",
  processIdsPlaceholder: "id1, id2",
  configUpdateCli: "CONFIG_UPDATE uses CLI --file",
};

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          actions: { cancel: "Cancel", close: "Close" },
          batch: batchI18n,
        },
      },
    },
  });
});

const partialBatch = {
  batchId: "b1",
  type: "START",
  status: "PARTIAL",
  operator: "admin",
  sourceAgent: "n1",
  createdUnixMs: BigInt(1_700_000_000_000),
  summary: {
    success: 1,
    failed: 0,
    timeout: 1,
    denied: 0,
    conflict: 0,
    unavailable: 0,
    invalid: 0,
  },
  targets: [
    {
      operationId: "op-ok",
      nodeId: "n1",
      processId: "p-ok",
      processName: "ok",
      status: "SUCCESS",
      error: "",
    },
    {
      operationId: "op-to",
      nodeId: "n2",
      processId: "p-to",
      processName: "slow",
      status: "TIMEOUT",
      error: "deadline",
    },
  ],
};

const mounted: Array<{ unmount: () => void }> = [];

async function mountBatches(opts: {
  path?: string;
  permissions?: string[];
  batches?: unknown[];
  batch?: unknown;
} = {}) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: opts.permissions ?? ["batch.execute"],
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const batches = opts.batches ?? [partialBatch];
  const batchClient = {
    listBatches: vi.fn().mockResolvedValue({ batches }),
    getBatch: vi.fn().mockResolvedValue({ batch: opts.batch ?? batches[0] }),
    createBatch: vi.fn().mockResolvedValue({ batch: partialBatch }),
    retryFailed: vi.fn().mockResolvedValue({ batch: partialBatch }),
    replayTimeout: vi.fn().mockResolvedValue({ batch: partialBatch }),
    exportBatch: vi.fn().mockResolvedValue({
      content: new TextEncoder().encode("{}"),
      contentType: "application/json",
      filename: "b1.json",
    }),
  };
  const processClient = {
    listProcesses: vi.fn().mockResolvedValue({
      processes: [
        { processId: "p1", spec: { name: "api", group: "web" } },
        { processId: "p2", spec: { name: "worker", group: "jobs" } },
      ],
    }),
  };
  const nodeClient = {
    listNodes: vi.fn().mockResolvedValue({ nodes: [] }),
  };
  const groupClient = {
    listAgentGroups: vi.fn().mockResolvedValue({ groups: [] }),
  };
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/batches", component: BatchesPage },
      { path: "/batches/:id", component: BatchesPage },
    ],
  });
  await router.push(opts.path ?? "/batches");
  await router.isReady();
  const wrapper = mount(BatchesPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
        router,
      ],
      provide: { batchClient, processClient, nodeClient, groupClient },
      stubs: { Teleport: true },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, batchClient, router };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("BatchesPage", () => {
  it("renders TIMEOUT badge as amber, not success green", async () => {
    const { wrapper } = await mountBatches({ path: "/batches/b1" });
    const timeout = wrapper.get('[data-status="TIMEOUT"]');
    expect(timeout.classes()).toContain("status-timeout");
    expect(timeout.classes()).not.toContain("status-success");
  });

  it("shows create form when session has batch.execute", async () => {
    const { wrapper } = await mountBatches({ permissions: ["batch.execute"] });
    expect(wrapper.get('[data-action="create-batch"]').text()).toContain("Create batch");
    expect(wrapper.find("form.create-batch").exists()).toBe(false);
    await wrapper.get('[data-action="create-batch"]').trigger("click");
    expect(wrapper.find("form.create-batch").exists()).toBe(true);
  });

  it("hides create form without batch.execute", async () => {
    const { wrapper } = await mountBatches({ permissions: ["process.read"] });
    expect(wrapper.find('[data-action="create-batch"]').exists()).toBe(false);
    expect(wrapper.find("form.create-batch").exists()).toBe(false);
    expect(wrapper.text()).not.toContain("Create batch");
  });

  it("shows local-only banner on the list page", async () => {
    const { wrapper } = await mountBatches();
    expect(wrapper.text()).toContain("Only batches created on this entry agent are listed.");
  });

  it("create mutation sends operationId and operator", async () => {
    const { wrapper, batchClient } = await mountBatches();
    await wrapper.get('[data-action="create-batch"]').trigger("click");
    await flushPromises();
    const drawer = wrapper.getComponent(Drawer);
    await drawer.get('input[name="processId"][value="p1"]').setValue(true);
    await drawer.get('input[name="processId"][value="p2"]').setValue(true);
    await drawer.get("form.create-batch").trigger("submit");
    await flushPromises();
    expect(batchClient.createBatch).toHaveBeenCalled();
    const arg = batchClient.createBatch.mock.calls[0][0] as {
      meta?: { operationId?: string; operator?: string };
      type?: string;
      selector?: { processIds?: string[] };
    };
    expect(arg.meta?.operationId).toBeTruthy();
    expect(arg.meta?.operator).toBe("admin");
    expect(arg.type).toBe("START");
    expect(arg.selector?.processIds).toEqual(["p1", "p2"]);
  });
});
