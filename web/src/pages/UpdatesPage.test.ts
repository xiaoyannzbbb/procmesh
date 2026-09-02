import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import { selfUpdateHold, session } from "../lib/session";
import UpdatesPage from "./UpdatesPage.vue";

const Blank = defineComponent({ setup: () => () => h("div") });

let i18n: typeof i18next;
const mounted: Array<{ unmount: () => void }> = [];

const updatesI18n = {
  title: "Updates",
  eyebrow: "Agent updates",
  subtitle: "{{count}} nodes",
  latest: "Latest pin: {{tag}}",
  latestUnknown: "Latest pin unavailable",
  loading: "Loading node update status…",
  empty: "No nodes",
  emptyHint: "Join agents to this cluster to see update eligibility.",
  loadFailed: "Could not load update status",
  retry: "Retry",
  refresh: "Refresh",
  platformUnknown: "Unknown platform",
  table: {
    hostname: "Hostname",
    platform: "OS / Arch",
    version: "Version",
    freshness: "Freshness",
    updated: "Updated",
    status: "Status",
    actions: "Actions",
  },
  apply: "Update",
  confirmTitle: "Update this node?",
  confirmHostname: "Hostname",
  confirmPin: "Version pin",
  confirmNoRestart: "This update will not restart business processes.",
  confirmSelfWarning:
    "You are updating the entry node that serves this Web UI. The browser session will drop while the Agent restarts.",
  confirm: "Update",
  applyFailed: "Could not apply the update",
  overlayTitle: "Updating this Agent",
  overlayBody: "Waiting for this Agent to come back at {{tag}}. Stay on this page; the Web session may drop.",
  overlayTimeout: "Timed out waiting for this Agent to reach {{tag}}.",
  overlayRefresh: "Refresh now",
  updatedJustNow: "just now",
  updatedSeconds: "{{count}}s ago",
  updatedMinutes: "{{count}}m ago",
  updatedUnknown: "unknown",
  status: {
    eligible: "Eligible",
    badgeLabel: "Update status: {{status}}",
  },
  skip: {
    STALE: "Stale — not probed",
    UNKNOWN: "Unknown freshness — not probed",
    FAILED: "Node failed — skipped",
    SUSPECT: "Node suspect — skipped",
    UNSUPPORTED: "Agent does not support in-app update",
    MACOS: "macOS is not supported",
    DISABLED: "Updates disabled on this node",
    BUSY: "Update already in progress",
    CURRENT: "Already at latest",
    UNAVAILABLE: "Peer unreachable",
    TIMEOUT: "Probe timed out",
    CHECK_FAILED: "Latest version check failed",
    unknown: "Not eligible",
  },
  clusterUpdate: "Update cluster",
  clusterUpdateDisabledNoEligible: "No eligible nodes to update.",
  clusterUpdateDisabledNoPin: "Latest pin is unavailable.",
  clusterConfirmTitle: "Update the cluster?",
  clusterConfirmPin: "Pinned release: {{repository}} {{tag}}",
  clusterConfirmWillUpdate: "Nodes that will update",
  clusterConfirmSkipped: "Nodes that will be skipped",
  clusterConfirmSkipItem: "{{hostname}}: {{reason}}",
  clusterConfirmRaftWarning:
    "Fewer than 3 Raft voters. Control-plane writes may be unavailable while voters restart.",
  clusterCreateFailed: "Could not start the cluster update",
  jobs: {
    title: "Update jobs",
    localOnly: "Only jobs created on this entry agent are listed.",
    empty: "No update jobs",
    emptyHint: "Start a cluster update to roll out a pinned Agent release.",
    loading: "Loading update jobs…",
    loadFailed: "Could not load update jobs",
    status: "Status",
    pin: "Pin",
    counts: "Counts",
    created: "Created",
    expand: "Show targets",
    collapse: "Hide targets",
    cancelRemaining: "Cancel remaining",
    retry: "Retry job",
    cancelFailed: "Could not cancel remaining targets",
    retryFailed: "Could not retry the update job",
    targets: "Targets",
    hostname: "Hostname",
    skipReason: "Skip reason",
    error: "Error",
    countsSummary:
      "{{success}} succeeded, {{failed}} failed, {{timeout}} timed out, {{conflict}} conflicted, {{skipped}} skipped, {{cancelled}} cancelled",
    statusLabel: "Job status: {{status}}",
    targetStatusLabel: "Target status: {{status}}",
    job: {
      PENDING: "Pending",
      RUNNING: "Running",
      COMPLETED: "Completed",
      PARTIAL: "Partial",
      FAILED: "Failed",
    },
    target: {
      PENDING: "Pending",
      RUNNING: "Running",
      SUCCESS: "Succeeded",
      FAILED: "Failed",
      TIMEOUT: "Timed out",
      CONFLICT: "Conflict",
      SKIPPED: "Skipped",
      CANCELLED: "Cancelled",
    },
  },
};

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    resources: {
      en: {
        common: {
          updates: updatesI18n,
          status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
          actions: {
            retry: "Retry",
            cancel: "Cancel",
            confirm: "Confirm",
            expand: "Expand",
            collapse: "Collapse",
          },
        },
      },
    },
  });
});

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
  selfUpdateHold.value = false;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function sampleNodes() {
  return [
    {
      nodeId: "n-eligible",
      hostname: "agent-a",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: true,
      skipReason: "",
      busy: false,
    },
    {
      nodeId: "n-current",
      hostname: "agent-b",
      os: "linux",
      arch: "amd64",
      version: "0.2.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "CURRENT",
      busy: false,
    },
    {
      nodeId: "n-macos",
      hostname: "macbook",
      os: "darwin",
      arch: "arm64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "MACOS",
      busy: false,
    },
    {
      nodeId: "n-disabled",
      hostname: "agent-d",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "DISABLED",
      busy: false,
    },
    {
      nodeId: "n-busy",
      hostname: "agent-e",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "BUSY",
      busy: true,
    },
    {
      nodeId: "n-unsupported",
      hostname: "agent-f",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "UNSUPPORTED",
      busy: false,
    },
    {
      nodeId: "n-stale",
      hostname: "agent-g",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "STALE",
      lastUpdatedUnixMs: BigInt(Date.now() - 60_000),
      eligible: false,
      skipReason: "STALE",
      busy: false,
    },
    {
      nodeId: "n-timeout",
      hostname: "agent-h",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "TIMEOUT",
      busy: false,
    },
    {
      nodeId: "n-unavail",
      hostname: "agent-i",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "UNAVAILABLE",
      busy: false,
    },
    {
      nodeId: "n-check",
      hostname: "agent-j",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "CHECK_FAILED",
      busy: false,
    },
    {
      nodeId: "n-weird",
      hostname: "agent-k",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(Date.now()),
      eligible: false,
      skipReason: "SOME_NEW_CODE",
      busy: false,
    },
  ];
}

const defaultPin = {
  repository: "ghcr.io/example/procmesh",
  tag: "v0.2.0",
  checksums: {
    "linux/amd64": "aaa",
    "linux/arm64": "bbb",
    "linux/armv7": "ccc",
  },
};

function sampleVoters(count = 3, freshness = "LIVE") {
  return Array.from({ length: count }, (_, i) => ({
    nodeId: `raft-${i + 1}`,
    hostname: `raft-host-${i + 1}`,
    raftRole: i === 0 ? "LEADER" : "VOTER",
    raftRoleFreshness: freshness,
  }));
}

function sampleJob(overrides: Record<string, unknown> = {}) {
  return {
    jobId: "job-1",
    operator: "admin",
    sourceAgent: "n-entry",
    pin: defaultPin,
    status: "RUNNING",
    summary: {
      success: 0,
      failed: 0,
      timeout: 0,
      conflict: 0,
      skipped: 1,
      cancelled: 0,
    },
    createdUnixMs: BigInt(Date.now()),
    targets: [],
    ...overrides,
  };
}

async function mountUpdates(opts: {
  nodes?: unknown[];
  latestTag?: string;
  checkError?: boolean;
  list?: ReturnType<typeof vi.fn>;
  checkLatest?: ReturnType<typeof vi.fn>;
  getLocalUpdateInfo?: ReturnType<typeof vi.fn>;
  applyNode?: ReturnType<typeof vi.fn>;
  createClusterUpdate?: ReturnType<typeof vi.fn>;
  listUpdateJobs?: ReturnType<typeof vi.fn>;
  getUpdateJob?: ReturnType<typeof vi.fn>;
  cancelRemaining?: ReturnType<typeof vi.fn>;
  retryUpdateJob?: ReturnType<typeof vi.fn>;
  listNodes?: ReturnType<typeof vi.fn> | unknown[];
  jobs?: unknown[];
  pending?: boolean;
  error?: Error;
  permissions?: string[];
  localNodeId?: string;
  query?: Record<string, string>;
} = {}) {
  if (opts.permissions) {
    session.value = {
      userId: "u1",
      username: "admin",
      csrfToken: "csrf",
      permissions: opts.permissions,
    };
  }
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const listNodeUpdateStatus =
    opts.list ??
    (opts.pending
      ? vi.fn().mockImplementation(() => new Promise(() => {}))
      : opts.error
        ? vi.fn().mockRejectedValue(opts.error)
        : vi.fn().mockResolvedValue({
            nodes: opts.nodes ?? sampleNodes(),
            latestTag: opts.latestTag ?? defaultPin.tag,
            checkError: opts.checkError ?? false,
          }));
  const checkLatest =
    opts.checkLatest ??
    vi.fn().mockResolvedValue({
      ...defaultPin,
      tag: opts.latestTag ?? defaultPin.tag,
      checkError: opts.checkError ?? false,
    });
  const getLocalUpdateInfo =
    opts.getLocalUpdateInfo ??
    vi.fn().mockResolvedValue({
      nodeId: opts.localNodeId ?? "n-entry",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      enabled: true,
      busy: false,
    });
  const applyNode = opts.applyNode ?? vi.fn().mockResolvedValue({});
  const createClusterUpdate = opts.createClusterUpdate ?? vi.fn().mockResolvedValue({ job: sampleJob({ status: "RUNNING" }) });
  const listUpdateJobs =
    opts.listUpdateJobs ??
    vi.fn().mockResolvedValue({
      jobs: opts.jobs ?? [],
    });
  const getUpdateJob =
    opts.getUpdateJob ??
    vi.fn().mockImplementation((req: { jobId: string }) => {
      const listed = (opts.jobs ?? []) as Array<{ jobId: string }>;
      const found = listed.find((job) => job.jobId === req.jobId) ?? sampleJob({ jobId: req.jobId });
      return Promise.resolve({ job: found });
    });
  const cancelRemaining = opts.cancelRemaining ?? vi.fn().mockResolvedValue({ job: sampleJob({ status: "PARTIAL" }) });
  const retryUpdateJob = opts.retryUpdateJob ?? vi.fn().mockResolvedValue({ job: sampleJob({ status: "RUNNING" }) });
  const listNodes =
    typeof opts.listNodes === "function"
      ? opts.listNodes
      : vi.fn().mockResolvedValue({ nodes: opts.listNodes ?? sampleVoters() });
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/updates", component: UpdatesPage },
      { path: "/nodes/:id", component: Blank },
      { path: "/login", component: Blank },
    ],
  });
  await router.push({ path: "/updates", query: opts.query });
  await router.isReady();
  const wrapper = mount(UpdatesPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
        router,
      ],
      provide: {
        updateClient: {
          listNodeUpdateStatus,
          checkLatest,
          getLocalUpdateInfo,
          applyNode,
          createClusterUpdate,
          listUpdateJobs,
          getUpdateJob,
          cancelRemaining,
          retryUpdateJob,
        },
        nodeClient: {
          listNodes,
          getNode: vi.fn(),
          removeNode: vi.fn(),
        },
      },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return {
    wrapper,
    listNodeUpdateStatus,
    checkLatest,
    getLocalUpdateInfo,
    applyNode,
    createClusterUpdate,
    listUpdateJobs,
    getUpdateJob,
    cancelRemaining,
    retryUpdateJob,
    listNodes,
    queryClient,
  };
}

describe("UpdatesPage", () => {
  it("renders an eligible row without apply controls", async () => {
    const { wrapper } = await mountUpdates();
    const row = wrapper.get('[data-node="n-eligible"]');
    expect(row.text()).toContain("agent-a");
    expect(row.text()).toContain("linux/amd64");
    expect(row.text()).toContain("0.1.0");
    expect(row.text()).toContain("Eligible");
    expect(wrapper.text()).not.toMatch(/apply/i);
    const applyButtons = wrapper.findAll("button").filter((b) => /apply/i.test(b.text()));
    expect(applyButtons).toHaveLength(0);
    expect(wrapper.find("form").exists()).toBe(false);
  });

  it("shows skip reasons as text plus tone, not color-only", async () => {
    const { wrapper } = await mountUpdates();
    const cases: Array<[string, string]> = [
      ["n-current", "Already at latest"],
      ["n-macos", "macOS is not supported"],
      ["n-disabled", "Updates disabled on this node"],
      ["n-busy", "Update already in progress"],
      ["n-unsupported", "Agent does not support in-app update"],
      ["n-stale", "Stale — not probed"],
      ["n-timeout", "Probe timed out"],
      ["n-unavail", "Peer unreachable"],
      ["n-check", "Latest version check failed"],
    ];
    for (const [id, label] of cases) {
      const row = wrapper.get(`[data-node="${id}"]`);
      expect(row.text()).toContain(label);
      expect(row.get("[data-status]").classes().join(" ")).toMatch(/status-/);
    }
    const staleRow = wrapper.get('[data-node="n-stale"]');
    expect(staleRow.classes().join(" ")).not.toMatch(/ok|live/);
    const timeoutRow = wrapper.get('[data-node="n-timeout"]');
    expect(timeoutRow.text()).not.toContain("Agent does not support in-app update");
    const unavailRow = wrapper.get('[data-node="n-unavail"]');
    expect(unavailRow.text()).not.toContain("Agent does not support in-app update");
  });

  it("uses green for already-current and yellow for updatable", async () => {
    const { wrapper } = await mountUpdates();
    const currentBadges = wrapper.findAll('[data-node="n-current"] [data-status]');
    const eligibleBadges = wrapper.findAll('[data-node="n-eligible"] [data-status]');
    expect(currentBadges.length).toBeGreaterThan(0);
    expect(eligibleBadges.length).toBeGreaterThan(0);
    for (const badge of currentBadges) {
      expect(badge.classes()).toContain("status-ok");
      expect(badge.classes()).not.toContain("status-warn");
    }
    for (const badge of eligibleBadges) {
      expect(badge.classes()).toContain("status-warn");
      expect(badge.classes()).not.toContain("status-ok");
    }
  });

  it("shows last_updated relative time next to FreshnessBadge", async () => {
    const { wrapper } = await mountUpdates();
    const staleRow = wrapper.get('[data-node="n-stale"]');
    expect(staleRow.text()).toContain("1m ago");
    expect(staleRow.get(".freshness-cell").text()).toMatch(/STALE/);
    expect(staleRow.get(".freshness-cell").text()).toContain("1m ago");
    const liveRow = wrapper.get('[data-node="n-eligible"]');
    expect(liveRow.get(".freshness-cell").text()).toContain("just now");
  });

  it("uses a generic i18n fallback for unknown skip reasons", async () => {
    const { wrapper } = await mountUpdates();
    const row = wrapper.get('[data-node="n-weird"]');
    expect(row.get("[data-status]").text()).toBe("Not eligible");
    expect(row.text()).not.toContain("SOME_NEW_CODE");
  });

  it("is read-only: refresh is allowed, apply is not", async () => {
    const { wrapper } = await mountUpdates();
    const buttons = wrapper.findAll("button").map((b) => b.text().trim());
    expect(buttons.some((text) => /refresh/i.test(text))).toBe(true);
    expect(buttons.some((text) => /apply|update node|install/i.test(text))).toBe(false);
    expect(wrapper.find('[data-action="apply"]').exists()).toBe(false);
  });

  it("shows a loading skeleton with aria-busy", async () => {
    const { wrapper } = await mountUpdates({ pending: true });
    const busy = wrapper.get("[aria-busy='true']");
    expect(busy.exists()).toBe(true);
    expect(wrapper.find(".skeleton-table").exists()).toBe(true);
    expect(wrapper.find("table tbody tr.data-row").exists()).toBe(false);
  });

  it("shows an i18n alert when the list fails, not raw error codes", async () => {
    const { wrapper } = await mountUpdates({
      error: new ConnectError("owner unreachable", Code.Unavailable),
    });
    const alert = wrapper.get('[role="alert"]');
    expect(alert.text()).toBe("Could not load update status");
    expect(alert.text()).not.toContain("UNAVAILABLE");
    expect(alert.text()).not.toContain("owner unreachable");
  });

  it("shows an empty state with helpful copy", async () => {
    const { wrapper } = await mountUpdates({ nodes: [] });
    expect(wrapper.text()).toContain("No nodes");
    expect(wrapper.text()).toContain("Join agents to this cluster to see update eligibility.");
  });

  it("does not poll CheckLatest on a 5s overview interval", async () => {
    const { listNodeUpdateStatus, checkLatest } = await mountUpdates();
    expect(listNodeUpdateStatus).toHaveBeenCalledTimes(1);
    expect(listNodeUpdateStatus.mock.calls[0]?.[0]).toEqual({});
    expect(checkLatest).toHaveBeenCalledWith({ refresh: false });
    expect(checkLatest).not.toHaveBeenCalledWith({ refresh: true });
  });

  it("refresh calls CheckLatest with refresh true and marks the button busy", async () => {
    let release!: (value: unknown) => void;
    const checkLatest = vi.fn().mockImplementation((req: { refresh?: boolean }) => {
      if (req.refresh) {
        return new Promise((resolve) => {
          release = resolve;
        });
      }
      return Promise.resolve({ tag: "v0.2.0", checkError: false });
    });
    const { wrapper, listNodeUpdateStatus } = await mountUpdates({ checkLatest });
    expect(listNodeUpdateStatus).toHaveBeenCalledTimes(1);
    const button = wrapper.get("header button");
    expect(button.attributes("disabled")).toBeUndefined();
    expect(button.attributes("aria-busy")).toBeFalsy();

    await button.trigger("click");
    await wrapper.vm.$nextTick();
    expect(checkLatest).toHaveBeenCalledWith({ refresh: true });
    expect(button.attributes("disabled")).toBeDefined();
    expect(button.attributes("aria-busy")).toBe("true");

    release({ tag: "v0.3.0", checkError: false });
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(listNodeUpdateStatus.mock.calls.length).toBeGreaterThan(1);
    expect(button.attributes("aria-busy")).toBeFalsy();
    expect(button.attributes("disabled")).toBeUndefined();
  });

  it("hides the Update button without node.manage", async () => {
    const { wrapper } = await mountUpdates({ permissions: ["node.read", "cluster.manage"] });
    expect(wrapper.find('[data-action="update"]').exists()).toBe(false);
    expect(wrapper.getComponent(ConfirmDialog).props("open")).toBe(false);
  });

  it("shows Update only on eligible rows when the session has node.manage", async () => {
    const { wrapper } = await mountUpdates({ permissions: ["node.manage"] });
    expect(wrapper.find('[data-node="n-eligible"] [data-action="update"]').exists()).toBe(true);
    expect(wrapper.find('[data-node="n-macos"] [data-action="update"]').exists()).toBe(false);
    expect(wrapper.find('[data-node="n-current"] [data-action="update"]').exists()).toBe(false);
    expect(wrapper.find('[data-node="n-disabled"] [data-action="update"]').exists()).toBe(false);
  });

  it("does not apply until the confirm dialog is confirmed", async () => {
    const { wrapper, applyNode } = await mountUpdates({ permissions: ["node.manage"] });
    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    const dialog = wrapper.getComponent(ConfirmDialog);
    expect(dialog.props("open")).toBe(true);
    expect(dialog.props("message")).toContain("agent-a");
    expect(dialog.props("message")).toContain("v0.2.0");
    expect(dialog.props("message")).toContain("This update will not restart business processes.");
    expect(dialog.props("message")).not.toContain("browser session will drop");
    expect(applyNode).not.toHaveBeenCalled();

    await dialog.vm.$emit("cancel");
    await wrapper.vm.$nextTick();
    expect(wrapper.getComponent(ConfirmDialog).props("open")).toBe(false);
    expect(applyNode).not.toHaveBeenCalled();

    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    await wrapper.getComponent(ConfirmDialog).vm.$emit("confirm");
    await flushPromises();
    expect(applyNode).toHaveBeenCalledTimes(1);
    expect(applyNode).toHaveBeenCalledWith({
      meta: { operationId: expect.any(String) },
      nodeId: "n-eligible",
      pin: {
        repository: defaultPin.repository,
        tag: defaultPin.tag,
        checksums: defaultPin.checksums,
      },
    });
    expect(wrapper.find("[data-self-update-overlay]").exists()).toBe(false);
    expect(selfUpdateHold.value).toBe(false);
  });

  it("closes the confirm dialog on Escape without applying", async () => {
    const { wrapper, applyNode } = await mountUpdates({ permissions: ["node.manage"] });
    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    expect(wrapper.getComponent(ConfirmDialog).props("open")).toBe(true);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(wrapper.getComponent(ConfirmDialog).props("open")).toBe(false);
    expect(applyNode).not.toHaveBeenCalled();
  });

  it("warns when the target node_id matches the local entry Agent", async () => {
    const { wrapper } = await mountUpdates({
      permissions: ["node.manage"],
      localNodeId: "n-eligible",
    });
    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    const dialog = wrapper.getComponent(ConfirmDialog);
    expect(dialog.props("message")).toContain("browser session will drop");
    expect(dialog.props("message")).toContain("agent-a");
    expect(dialog.props("message")).toContain("v0.2.0");
  });

  it("disables confirm while applyNode is pending", async () => {
    let release!: (value: unknown) => void;
    const applyNode = vi.fn().mockImplementation(
      () =>
        new Promise((resolve) => {
          release = resolve;
        }),
    );
    const { wrapper } = await mountUpdates({ permissions: ["node.manage"], applyNode });
    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    expect(wrapper.getComponent(ConfirmDialog).props("pending")).toBe(false);
    await wrapper.getComponent(ConfirmDialog).vm.$emit("confirm");
    await wrapper.vm.$nextTick();
    await flushPromises();
    expect(wrapper.getComponent(ConfirmDialog).props("pending")).toBe(true);
    expect(applyNode).toHaveBeenCalledTimes(1);
    release({});
    await flushPromises();
  });

  it("shows apply errors in the confirm dialog and does not overlay remote nodes", async () => {
    const applyNode = vi.fn().mockRejectedValue(new ConnectError("owner unreachable", Code.Unavailable));
    const { wrapper } = await mountUpdates({ permissions: ["node.manage"], applyNode });
    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    await wrapper.getComponent(ConfirmDialog).vm.$emit("confirm");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(wrapper.find("[data-self-update-overlay]").exists()).toBe(false);
    expect(selfUpdateHold.value).toBe(false);
    const dialog = wrapper.getComponent(ConfirmDialog);
    expect(dialog.props("open")).toBe(true);
    expect(dialog.props("message")).toContain("Could not apply the update");
  });

  it("shows a self-update overlay and does not redirect to /login on disconnect", async () => {
    const applyNode = vi.fn().mockRejectedValue(new ConnectError("agent restarting", Code.Unavailable));
    const getLocalUpdateInfo = vi.fn().mockResolvedValue({
      nodeId: "n-eligible",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      enabled: true,
      busy: true,
    });
    const { wrapper } = await mountUpdates({
      permissions: ["node.manage"],
      localNodeId: "n-eligible",
      applyNode,
      getLocalUpdateInfo,
    });
    vi.useFakeTimers();
    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    await wrapper.getComponent(ConfirmDialog).vm.$emit("confirm");
    await flushPromises();
    const overlay = wrapper.get("[data-self-update-overlay]");
    expect(overlay.attributes("role")).toBe("status");
    expect(overlay.text()).toContain("Updating this Agent");
    expect(overlay.text()).toContain("v0.2.0");
    expect(wrapper.vm.$router.currentRoute.value.path).toBe("/updates");

    getLocalUpdateInfo.mockRejectedValue(new ConnectError("offline", Code.Unavailable));
    await vi.advanceTimersByTimeAsync(2_000);
    await flushPromises();
    expect(wrapper.find("[data-self-update-overlay]").exists()).toBe(true);
    expect(wrapper.vm.$router.currentRoute.value.path).not.toBe("/login");
    expect(selfUpdateHold.value).toBe(true);
  });

  it("reloads the page when the self-updated Agent matches the pin", async () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { reload });
    let version = "0.1.0";
    const getLocalUpdateInfo = vi.fn().mockImplementation(() =>
      Promise.resolve({
        nodeId: "n-eligible",
        os: "linux",
        arch: "amd64",
        version,
        enabled: true,
        busy: false,
      }),
    );
    const { wrapper, listNodeUpdateStatus } = await mountUpdates({
      permissions: ["node.manage"],
      localNodeId: "n-eligible",
      getLocalUpdateInfo,
    });
    vi.useFakeTimers();
    const listCalls = listNodeUpdateStatus.mock.calls.length;
    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    await wrapper.getComponent(ConfirmDialog).vm.$emit("confirm");
    await flushPromises();
    expect(wrapper.find("[data-self-update-overlay]").exists()).toBe(true);
    expect(reload).not.toHaveBeenCalled();

    version = "v0.2.0";
    await vi.advanceTimersByTimeAsync(2_000);
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(reload).toHaveBeenCalled();
    expect(listNodeUpdateStatus.mock.calls.length).toBe(listCalls);
  });

  it("shows timeout copy and a manual refresh button after two minutes", async () => {
    const getLocalUpdateInfo = vi.fn().mockResolvedValue({
      nodeId: "n-eligible",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      enabled: true,
      busy: true,
    });
    const { wrapper } = await mountUpdates({
      permissions: ["node.manage"],
      localNodeId: "n-eligible",
      getLocalUpdateInfo,
    });
    vi.useFakeTimers();
    await wrapper.find('[data-node="n-eligible"] [data-action="update"]').trigger("click");
    await wrapper.vm.$nextTick();
    await wrapper.getComponent(ConfirmDialog).vm.$emit("confirm");
    await flushPromises();
    await vi.advanceTimersByTimeAsync(120_000);
    await flushPromises();
    await wrapper.vm.$nextTick();
    const overlay = wrapper.get("[data-self-update-overlay]");
    expect(overlay.text()).toContain("Timed out waiting for this Agent to reach v0.2.0.");
    expect(wrapper.get('[data-action="reload-after-update"]').text()).toContain("Refresh now");
  });

  it("hides Update cluster without cluster.manage", async () => {
    const { wrapper } = await mountUpdates({ permissions: ["node.manage"] });
    expect(wrapper.find('[data-action="update-cluster"]').exists()).toBe(false);
  });

  it("disables Update cluster without eligible nodes and explains why", async () => {
    const { wrapper } = await mountUpdates({
      permissions: ["cluster.manage"],
      nodes: sampleNodes().filter((node) => !node.eligible),
    });
    const button = wrapper.get('[data-action="update-cluster"]');
    expect(button.attributes("disabled")).toBeDefined();
    expect(wrapper.text()).toContain("No eligible nodes to update.");
  });

  it("shows an enabled primary Update cluster button when cluster.manage and a node is eligible", async () => {
    const { wrapper } = await mountUpdates({ permissions: ["cluster.manage", "node.manage"] });
    const cluster = wrapper.get('[data-action="update-cluster"]');
    expect(cluster.attributes("disabled")).toBeUndefined();
    expect(cluster.classes().join(" ")).toContain("btn-primary");
    const rowUpdate = wrapper.get('[data-node="n-eligible"] [data-action="update"]');
    expect(rowUpdate.classes().join(" ")).not.toContain("btn-primary");
  });

  it("lists pin, hostnames, skip reasons, no-restart, and raft warning in cluster confirm", async () => {
    const { wrapper, createClusterUpdate } = await mountUpdates({
      permissions: ["cluster.manage"],
      listNodes: sampleVoters(2),
    });
    await wrapper.get('[data-action="update-cluster"]').trigger("click");
    await wrapper.vm.$nextTick();
    const dialog = wrapper.getComponent(ConfirmDialog);
    expect(dialog.props("open")).toBe(true);
    expect(dialog.props("title")).toBe("Update the cluster?");
    const body = document.body.textContent ?? "";
    expect(body).toContain("ghcr.io/example/procmesh");
    expect(body).toContain("v0.2.0");
    expect(body).toContain("agent-a");
    expect(body).toContain("macbook");
    expect(body).toContain("macOS is not supported");
    expect(body).toContain("This update will not restart business processes.");
    expect(body).toContain("Fewer than 3 Raft voters");
    expect(createClusterUpdate).not.toHaveBeenCalled();
  });

  it("warns when three Raft voters are STALE rather than LIVE", async () => {
    const { wrapper } = await mountUpdates({
      permissions: ["cluster.manage"],
      listNodes: sampleVoters(3, "STALE"),
    });
    await wrapper.get('[data-action="update-cluster"]').trigger("click");
    await wrapper.vm.$nextTick();
    expect(document.body.textContent ?? "").toContain("Fewer than 3 Raft voters");
  });

  it("creates a cluster update with the CheckLatest pin after confirm", async () => {
    const { wrapper, createClusterUpdate } = await mountUpdates({
      permissions: ["cluster.manage"],
    });
    await wrapper.get('[data-action="update-cluster"]').trigger("click");
    await wrapper.vm.$nextTick();
    await wrapper.getComponent(ConfirmDialog).vm.$emit("confirm");
    await flushPromises();
    expect(createClusterUpdate).toHaveBeenCalledTimes(1);
    expect(createClusterUpdate).toHaveBeenCalledWith({
      meta: { operationId: expect.any(String) },
      pin: {
        repository: defaultPin.repository,
        tag: defaultPin.tag,
        checksums: defaultPin.checksums,
      },
    });
  });

  it("shows cancel remaining on RUNNING jobs and retry on PARTIAL/FAILED/cancelled", async () => {
    const jobs = [
      sampleJob({ jobId: "job-running", status: "RUNNING" }),
      sampleJob({ jobId: "job-partial", status: "PARTIAL", summary: { success: 1, failed: 1, timeout: 0, conflict: 0, skipped: 0, cancelled: 0 } }),
      sampleJob({ jobId: "job-failed", status: "FAILED", summary: { success: 0, failed: 2, timeout: 0, conflict: 0, skipped: 0, cancelled: 0 } }),
      sampleJob({
        jobId: "job-cancelled",
        status: "COMPLETED",
        summary: { success: 1, failed: 0, timeout: 0, conflict: 0, skipped: 0, cancelled: 2 },
      }),
    ];
    const { wrapper, cancelRemaining, retryUpdateJob } = await mountUpdates({
      permissions: ["cluster.manage"],
      jobs,
    });
    expect(wrapper.get('[data-job="job-running"] [data-action="cancel-remaining"]').exists()).toBe(true);
    expect(wrapper.find('[data-job="job-running"] [data-action="retry-job"]').exists()).toBe(false);
    expect(wrapper.get('[data-job="job-partial"] [data-action="retry-job"]').exists()).toBe(true);
    expect(wrapper.get('[data-job="job-failed"] [data-action="retry-job"]').exists()).toBe(true);
    expect(wrapper.get('[data-job="job-cancelled"] [data-action="retry-job"]').exists()).toBe(true);

    await wrapper.get('[data-job="job-running"] [data-action="cancel-remaining"]').trigger("click");
    await flushPromises();
    expect(cancelRemaining).toHaveBeenCalledWith({
      meta: { operationId: expect.any(String) },
      jobId: "job-running",
    });

    await wrapper.get('[data-job="job-partial"] [data-action="retry-job"]').trigger("click");
    await flushPromises();
    expect(retryUpdateJob).toHaveBeenCalledWith({
      meta: { operationId: expect.any(String) },
      jobId: "job-partial",
    });
  });

  it("expands a job to load targets and polls Get/List while RUNNING, not CheckLatest", async () => {
    vi.useFakeTimers();
    const running = sampleJob({
      jobId: "job-run",
      status: "RUNNING",
      pin: defaultPin,
      targets: [
        {
          operationId: "op-1",
          nodeId: "n-eligible",
          hostname: "agent-a",
          status: "RUNNING",
          skipReason: "",
          error: "",
          orderIndex: 0,
        },
      ],
    });
    const listUpdateJobs = vi.fn().mockResolvedValue({ jobs: [{ ...running, targets: [] }] });
    const getUpdateJob = vi.fn().mockResolvedValue({ job: running });
    const { wrapper, checkLatest } = await mountUpdates({
      permissions: ["cluster.manage"],
      listUpdateJobs,
      getUpdateJob,
    });
    expect(listUpdateJobs).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-job="job-run"]').text()).toContain("Running");
    expect(wrapper.get('[data-job="job-run"]').text()).toContain("v0.2.0");

    await wrapper.get('[data-job="job-run"] [data-action="expand-job"]').trigger("click");
    await flushPromises();
    expect(getUpdateJob).toHaveBeenCalledWith({ jobId: "job-run" });
    expect(wrapper.text()).toContain("agent-a");

    const listCalls = listUpdateJobs.mock.calls.length;
    const getCalls = getUpdateJob.mock.calls.length;
    const checkCalls = checkLatest.mock.calls.length;
    await vi.advanceTimersByTimeAsync(5_000);
    await flushPromises();
    expect(listUpdateJobs.mock.calls.length).toBeGreaterThan(listCalls);
    expect(getUpdateJob.mock.calls.length).toBeGreaterThan(getCalls);
    expect(checkLatest.mock.calls.length).toBe(checkCalls);
    expect(checkLatest).not.toHaveBeenCalledWith({ refresh: true });
  });

  it("highlights the node from the node query", async () => {
    const { wrapper } = await mountUpdates({ query: { node: "n-eligible" } });
    expect(wrapper.get('[data-node="n-eligible"]').attributes("data-highlight")).toBe("true");
    expect(wrapper.get('[data-node="n-current"]').attributes("data-highlight")).toBeUndefined();
  });

  it("shows an empty jobs state for this entry and job errors as alerts", async () => {
    const retryUpdateJob = vi.fn().mockRejectedValue(new ConnectError("unavailable", Code.Unavailable));
    const { wrapper } = await mountUpdates({
      permissions: ["cluster.manage"],
      jobs: [],
    });
    expect(wrapper.text()).toContain("No update jobs");
    expect(wrapper.text()).toContain("Only jobs created on this entry agent are listed.");

    const { wrapper: retryWrapper } = await mountUpdates({
      permissions: ["cluster.manage"],
      jobs: [sampleJob({ jobId: "job-failed", status: "FAILED" })],
      retryUpdateJob,
    });
    await retryWrapper.get('[data-action="retry-job"]').trigger("click");
    await flushPromises();
    await retryWrapper.vm.$nextTick();
    const alert = retryWrapper.get('[role="alert"]');
    expect(alert.text()).toContain("Could not retry the update job");
  });
});
