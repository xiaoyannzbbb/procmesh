import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import AuditPage from "./AuditPage.vue";

const mounted: Array<{ unmount: () => void }> = [];

async function mountAudit(entries: unknown[]) {
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
  const wrapper = mount(AuditPage, {
    global: {
      plugins: [[VueQueryPlugin, { queryClient }]],
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
    const { wrapper } = await mountAudit([
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
    ]);
    const badge = wrapper.get(".freshness-badge");
    expect(badge.text()).toBe("STALE");
    expect(badge.classes()).toContain("freshness-stale");
    expect(badge.classes()).not.toContain("freshness-live");
    const html = wrapper.html().toLowerCase();
    expect(html).not.toMatch(/green|#d1fae5|#10a37f|bg-green/);
  });
});
