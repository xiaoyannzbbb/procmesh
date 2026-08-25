import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import AlertsPage from "./AlertsPage.vue";

let i18n: typeof i18next;

const alertI18n = {
  title: "Alerts",
  staleBanner: "Some nodes are unreachable. This is not an empty inbox.",
  channels: "Channels",
  policy: "Policy",
  noAlerts: "No alerts",
  firing: "FIRING",
  resolved: "RESOLVED",
  loading: "Loading…",
  fingerprint: "Fingerprint",
  type: "Type",
  severity: "Severity",
  state: "State",
  node: "Node",
  process: "Process",
  freshness: "Freshness",
  lastUpdated: "Last updated",
  name: "Name",
  enabled: "Enabled",
  disabled: "Disabled",
  config: "Config",
  save: "Save",
  delete: "Delete",
  createChannel: "Create channel",
  editChannel: "Edit channel",
  testChannel: "Send test",
  channelTestSent: "Test message sent.",
  noChannels: "No channels",
  dedupWindowSec: "Dedup window (sec)",
  notifyOnResolve: "Notify on resolve",
  cpuHighPercent: "CPU high (%)",
  memoryHighPercent: "Memory high (%)",
  diskHighPercent: "Disk high (%)",
  highConsecutiveMins: "High consecutive (min)",
  suspectTooLongSec: "Suspect too long (sec)",
};

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          alert: alertI18n,
          status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
          actions: { save: "Save", delete: "Delete" },
        },
      },
    },
  });
});

const firingLive = {
  alert: {
    alertId: "a1",
    fingerprint: "fp-exit",
    type: "PROCESS_EXIT",
    severity: "critical",
    nodeId: "n1",
    processId: "web",
    state: "FIRING",
    lastUnixMs: BigInt(1_700_000_000_000),
  },
  sourceNode: "n1",
  freshness: "LIVE",
  lastUpdatedUnixMs: BigInt(1_700_000_010_000),
};

const stalePlaceholder = {
  alert: undefined,
  sourceNode: "n2",
  freshness: "STALE",
  lastUpdatedUnixMs: BigInt(0),
};

const mounted: Array<{ unmount: () => void }> = [];

async function mountAlerts(
  opts: {
    permissions?: string[];
    entries?: unknown[];
    channels?: unknown[];
    policy?: unknown;
  } = {},
) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: opts.permissions ?? ["alert.read", "alert.manage"],
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const entries = opts.entries ?? [firingLive, stalePlaceholder];
  const alertClient = {
    listAlerts: vi.fn().mockResolvedValue({ entries }),
    getAlert: vi.fn(),
    listAlertChannels: vi.fn().mockResolvedValue({ channels: opts.channels ?? [] }),
    putAlertChannel: vi.fn().mockResolvedValue({
      channel: { channelId: "c-new", type: "WEBHOOK", name: "hook", enabled: true, configJson: "{}" },
    }),
    deleteAlertChannel: vi.fn().mockResolvedValue({}),
    testAlertChannel: vi.fn().mockResolvedValue({}),
    getAlertPolicy: vi.fn().mockResolvedValue({
      policy: opts.policy ?? {
        dedupWindowSec: BigInt(600),
        notifyOnResolve: true,
        cpuHighPercent: 90,
        memoryHighPercent: 90,
        diskHighPercent: 90,
        highConsecutiveMins: 2,
        suspectTooLongSec: BigInt(120),
      },
    }),
    putAlertPolicy: vi.fn().mockResolvedValue({ policy: opts.policy ?? {} }),
  };
  const wrapper = mount(AlertsPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { alertClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, alertClient, queryClient };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("AlertsPage", () => {
  it("shows the alert event age instead of the source freshness age", async () => {
    const now = 1_700_000_120_000;
    const nowSpy = vi.spyOn(Date, "now").mockReturnValue(now);
    try {
      const { wrapper } = await mountAlerts({
        entries: [
          {
            ...firingLive,
            alert: {
              ...firingLive.alert,
              lastUnixMs: BigInt(now - 120_000),
            },
            lastUpdatedUnixMs: BigInt(now),
          },
        ],
      });

      const timeCell = wrapper.get('[data-freshness="LIVE"] .time-cell');
      expect(timeCell.text()).toBe("2m ago");
      expect(timeCell.attributes("title")).toContain("2023");
    } finally {
      nowSpy.mockRestore();
    }
  });

  it("shows STALE badge and banner, not empty inbox, for FIRING LIVE plus STALE placeholder", async () => {
    const { wrapper } = await mountAlerts();
    const text = wrapper.text();
    expect(text).toContain("Some nodes are unreachable. This is not an empty inbox.");
    expect(wrapper.find(".freshness-stale").exists()).toBe(true);
    expect(text).not.toContain("No alerts");
    const staleRow = wrapper.get('[data-freshness="STALE"]');
    const html = staleRow.html().toLowerCase();
    const style = (staleRow.attributes("style") ?? "").toLowerCase();
    expect(html).not.toMatch(/#d1fae5|rgb\(209,\s*250,\s*229\)/);
    expect(style).not.toMatch(/#d1fae5|rgb\(209,\s*250,\s*229\)/);
    const el = staleRow.element as HTMLElement;
    const bg = (getComputedStyle(el).backgroundColor || el.style.backgroundColor).toLowerCase();
    expect(bg).not.toMatch(/rgb\(209,\s*250,\s*229\)|#d1fae5/);
    const firing = wrapper.get('[data-state="FIRING"]');
    expect(firing.classes()).toContain("alert-firing");
    const firingStyle = (firing.attributes("style") ?? "").toLowerCase();
    expect(firingStyle).not.toMatch(/#d1fae5|#10a37f|bg-green|rgb\(209,\s*250,\s*229\)/);
    expect(firing.html().toLowerCase()).not.toMatch(/#d1fae5|#10a37f|bg-green/);
  });

  it("hides save and delete without alert.manage", async () => {
    const { wrapper } = await mountAlerts({
      permissions: ["alert.read"],
      channels: [
        {
          channelId: "c1",
          type: "WEBHOOK",
          name: "hook",
          enabled: true,
          configJson: '{"url":"https://hooks.example.com"}',
        },
      ],
    });
    expect(wrapper.find('[data-action="save-channel"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="delete-channel"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="save-policy"]').exists()).toBe(false);
  });

  it("shows localized channel enabled states", async () => {
    const { wrapper } = await mountAlerts({
      channels: [
        { channelId: "c1", type: "WEBHOOK", name: "on", enabled: true, configJson: "{}" },
        { channelId: "c2", type: "WEBHOOK", name: "off", enabled: false, configJson: "{}" },
      ],
    });
    const rows = wrapper.findAll(".channel-row");

    expect(rows).toHaveLength(2);
    expect(rows[0].text()).toContain("Enabled");
    expect(rows[0].text()).not.toContain("true");
    expect(rows[1].text()).toContain("Disabled");
    expect(rows[1].text()).not.toContain("false");
  });

  it("shows save when session has alert.manage", async () => {
    const { wrapper } = await mountAlerts({ permissions: ["alert.read", "alert.manage"] });
    expect(wrapper.find('[data-action="create-channel"]').exists()).toBe(true);
    expect(wrapper.find('[data-action="save-policy"]').exists()).toBe(true);
  });

  it("opens create and edit channel forms in a right-side drawer", async () => {
    const { wrapper } = await mountAlerts({
      channels: [
        { channelId: "c1", type: "WEBHOOK", name: "hook", enabled: true, configJson: '{"url":"https://hooks.example.com"}' },
      ],
    });
    expect(document.querySelector("form.channel-form")).toBeNull();

    await wrapper.get('[data-action="create-channel"]').trigger("click");
    await flushPromises();
    expect(document.querySelector('[role="dialog"]')?.getAttribute("aria-label")).toBe("Create channel");
    expect(document.querySelector("form.channel-form")).not.toBeNull();

    (document.querySelector(".drawer-close") as HTMLButtonElement).click();
    await flushPromises();
    await wrapper.get(".channel-main").trigger("click");
    await flushPromises();
    expect(document.querySelector('[role="dialog"]')?.getAttribute("aria-label")).toBe("Edit channel");
    expect((document.querySelector('input[name="channelName"]') as HTMLInputElement).value).toBe("hook");
  });

  it("sends a test message for a saved external channel", async () => {
    const { wrapper, alertClient } = await mountAlerts({
      channels: [
        { channelId: "ding-1", type: "DINGTALK", name: "ops", enabled: false, configJson: '{"webhook_url":"https://example.com"}' },
      ],
    });

    await wrapper.get('[data-action="test-channel"]').trigger("click");
    await flushPromises();

    expect(alertClient.testAlertChannel).toHaveBeenCalledWith({
      meta: expect.objectContaining({ operationId: expect.any(String), operator: "admin" }),
      channelId: "ding-1",
    });
    expect(wrapper.text()).toContain("Test message sent.");
  });

  it("put channel sends operationId", async () => {
    const { wrapper, alertClient } = await mountAlerts();
    await wrapper.get('[data-action="create-channel"]').trigger("click");
    await flushPromises();
    const name = document.querySelector('input[name="channelName"]') as HTMLInputElement;
    name.value = "hook";
    name.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector("form.channel-form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await flushPromises();
    expect(alertClient.putAlertChannel).toHaveBeenCalled();
    const arg = alertClient.putAlertChannel.mock.calls[0][0] as {
      meta?: { operationId?: string; operator?: string };
      type?: string;
      name?: string;
    };
    expect(arg.meta?.operationId).toBeTruthy();
    expect(arg.meta?.operator).toBe("admin");
    expect(arg.name).toBe("hook");
  });

  it("does not echo hmac_secret, password, or secret in channel config", async () => {
    const { wrapper } = await mountAlerts({
      channels: [
        {
          channelId: "c1",
          type: "WEBHOOK",
          name: "hook",
          enabled: true,
          configJson: '{"url":"https://hooks.example.com","hmac_secret":"s3cret","password":"p","secret":"s"}',
        },
      ],
    });
    vi.useFakeTimers();
    try {
      const channel = wrapper.get(".channel-row");
      expect(channel.text()).not.toContain("s3cret");

      await channel.get(".channel-main").trigger("click");
      await wrapper.vm.$nextTick();

      const config = document.querySelector('textarea[name="channelConfig"]') as HTMLTextAreaElement;
      const secret = document.querySelector('input[name="channelSecret"]') as HTMLInputElement;
      expect(JSON.parse(config.value)).toEqual({ url: "https://hooks.example.com" });
      expect(secret.value).toBe("");
    } finally {
      vi.clearAllTimers();
      vi.useRealTimers();
    }
  });

  it("does not clobber dirty policy fields on refetch", async () => {
    const { wrapper, alertClient, queryClient } = await mountAlerts();
    const cpu = wrapper.get('input[name="cpuHighPercent"]');
    expect((cpu.element as HTMLInputElement).value).toBe("90");
    await cpu.setValue("70");
    alertClient.getAlertPolicy.mockResolvedValue({
      policy: {
        dedupWindowSec: BigInt(600),
        notifyOnResolve: true,
        cpuHighPercent: 80,
        memoryHighPercent: 90,
        diskHighPercent: 90,
        highConsecutiveMins: 2,
        suspectTooLongSec: BigInt(120),
      },
    });
    await queryClient.invalidateQueries({ queryKey: ["alert-policy"] });
    await flushPromises();
    expect((wrapper.get('input[name="cpuHighPercent"]').element as HTMLInputElement).value).toBe("70");
  });
});
