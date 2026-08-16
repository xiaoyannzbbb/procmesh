import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Plugin } from "vue";
import { session } from "../lib/session";
import AuditPage from "./AuditPage.vue";

const mounted: Array<{ unmount: () => void }> = [];

async function mountAudit(entries: unknown[], i18n?: typeof i18next) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: ["audit.read"],
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const auditClient = {
    listAudit: vi.fn().mockResolvedValue({ entries }),
  };

  const plugins: Array<Plugin | [Plugin, ...unknown[]]> = [[VueQueryPlugin, { queryClient }]];
  if (i18n) {
    plugins.push([I18NextVue, { i18next: i18n }]);
  }

  const wrapper = mount(AuditPage, {
    global: {
      plugins,
      provide: { auditClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, auditClient };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("AuditPage", () => {
  it("renders STALE badge without green for a STALE entry", async () => {
    const i18n = i18next.createInstance();
    await i18n.init({
      lng: "en",
      fallbackLng: "en",
      resources: {
        en: {
          common: {
            status: { stale: "STALE" },
            audit: {
              title: "Audit",
              notice: "Audit is per-node; unreachable nodes are marked STALE.",
              resourceLabel: "Resource",
              resourcePlaceholder: "Filter resource",
              loading: "Loading…",
              noEntries: "No audit entries",
              table: {
                time: "Time",
                user: "User",
                action: "Action",
                resource: "Resource",
                sourceNode: "Source node",
                targetAgent: "Target agent",
                result: "Result",
                freshness: "Freshness",
              },
            },
          },
        },
      },
    });

    const { wrapper } = await mountAudit(
      [
        {
          event: {
            auditId: "a1",
            timestampUnixMs: 1_700_000_000_000n,
            username: "admin",
            action: "user.create",
            resource: "user/alice",
            targetAgent: "n2",
            result: "OK",
          },
          sourceNode: "n1",
          freshness: "STALE",
          lastUpdatedUnixMs: 1_700_000_000_000n,
        },
      ],
      i18n
    );
    const badge = wrapper.get(".freshness-badge");
    expect(badge.text()).toBe("STALE");
    expect(badge.classes()).toContain("freshness-stale");
    expect(badge.classes()).not.toContain("freshness-live");
    const html = wrapper.html().toLowerCase();
    expect(html).not.toMatch(/green|#d1fae5|#10a37f|bg-green/);
  });
});

describe("AuditPage i18n", () => {
  it("should render in English", async () => {
    const i18n = i18next.createInstance();
    await i18n.init({
      lng: "en",
      fallbackLng: "en",
      resources: {
        en: {
          common: {
            audit: {
              title: "Audit",
              notice: "Audit is per-node; unreachable nodes are marked STALE.",
              resourceLabel: "Resource",
              resourcePlaceholder: "Filter resource",
              loading: "Loading…",
              noEntries: "No audit entries",
              table: {
                time: "Time",
                user: "User",
                action: "Action",
                resource: "Resource",
                sourceNode: "Source node",
                targetAgent: "Target agent",
                result: "Result",
                freshness: "Freshness",
              },
            },
          },
        },
      },
    });

    const { wrapper } = await mountAudit([], i18n);
    expect(wrapper.text()).toContain("Audit");
    expect(wrapper.text()).toContain("Audit is per-node; unreachable nodes are marked STALE.");
    expect(wrapper.text()).toContain("Resource");
    expect(wrapper.text()).toContain("No audit entries");
    expect(wrapper.text()).toContain("Time");
    expect(wrapper.text()).toContain("User");
    expect(wrapper.text()).toContain("Source node");
  });

  it("should render in Chinese", async () => {
    const i18n = i18next.createInstance();
    await i18n.init({
      lng: "zh",
      fallbackLng: "en",
      resources: {
        zh: {
          common: {
            audit: {
              title: "审计",
              notice: "审计按节点进行；无法访问的节点标记为 STALE。",
              resourceLabel: "资源",
              resourcePlaceholder: "过滤资源",
              loading: "加载中…",
              noEntries: "无审计条目",
              table: {
                time: "时间",
                user: "用户",
                action: "操作",
                resource: "资源",
                sourceNode: "源节点",
                targetAgent: "目标代理",
                result: "结果",
                freshness: "新鲜度",
              },
            },
          },
        },
      },
    });

    const { wrapper } = await mountAudit([], i18n);
    expect(wrapper.text()).toContain("审计");
    expect(wrapper.text()).toContain("审计按节点进行；无法访问的节点标记为 STALE。");
    expect(wrapper.text()).toContain("资源");
    expect(wrapper.text()).toContain("无审计条目");
    expect(wrapper.text()).toContain("时间");
    expect(wrapper.text()).toContain("用户");
    expect(wrapper.text()).toContain("源节点");
  });
});
