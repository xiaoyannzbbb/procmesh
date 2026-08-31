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
};

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          updates: updatesI18n,
          status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
          actions: { retry: "Retry", cancel: "Cancel", confirm: "Confirm" },
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

async function mountUpdates(opts: {
  nodes?: unknown[];
  latestTag?: string;
  checkError?: boolean;
  list?: ReturnType<typeof vi.fn>;
  checkLatest?: ReturnType<typeof vi.fn>;
  getLocalUpdateInfo?: ReturnType<typeof vi.fn>;
  applyNode?: ReturnType<typeof vi.fn>;
  pending?: boolean;
  error?: Error;
  permissions?: string[];
  localNodeId?: string;
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
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/updates", component: UpdatesPage },
      { path: "/nodes/:id", component: Blank },
      { path: "/login", component: Blank },
    ],
  });
  await router.push("/updates");
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
        },
      },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, listNodeUpdateStatus, checkLatest, getLocalUpdateInfo, applyNode, queryClient };
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
});
