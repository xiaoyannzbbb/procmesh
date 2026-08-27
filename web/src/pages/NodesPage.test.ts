import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import NodesPage from "./NodesPage.vue";

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

async function mountNodesPage(nodes: unknown[] = []) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const nodeClient = { listNodes: vi.fn().mockResolvedValue({ nodes }) };
  const wrapper = mount(NodesPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { nodeClient },
      stubs: {
        RouterLink: {
          props: ["to"],
          template: `<a :href="typeof to === 'string' ? to : to?.path"><slot /></a>`,
        },
      },
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

async function addNodesTranslations(language: "en" | "zh") {
  const chinese = language === "zh";
  await i18n.changeLanguage(language);
  await i18n.addResourceBundle(language, "common", {
    nodes: {
      title: chinese ? "节点" : "Nodes",
      loading: chinese ? "加载中…" : "Loading…",
      noNodes: chinese ? "无节点" : "No nodes",
      table: {
        hostname: chinese ? "主机名" : "Hostname",
        nodeId: chinese ? "节点ID" : "Node ID",
        state: chinese ? "状态" : "State",
        raftRole: chinese ? "Raft 角色" : "Raft role",
        version: chinese ? "版本" : "Version",
        resources: chinese ? "资源" : "Resources",
        processes: chinese ? "进程" : "Processes",
        freshness: chinese ? "新鲜度" : "Freshness",
        updated: chinese ? "更新时间" : "Updated",
      },
      raftRole: {
        leader: chinese ? "领导者" : "Leader",
        voter: chinese ? "投票成员" : "Voter",
        nonVoter: chinese ? "非投票成员" : "Non-voter",
        notMember: chinese ? "非成员" : "Not member",
        unknown: chinese ? "未知" : "Unknown",
        badgeLabel: chinese ? "Raft 角色：{{role}}" : "Raft role: {{role}}",
      },
    },
    status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
  });
}

describe("NodesPage i18n", () => {
  it("should render in English", async () => {
    await addNodesTranslations("en");

    const wrapper = await mountNodesPage([]);
    const text = wrapper.text();
    expect(text).toContain("Nodes");
    expect(text).toContain("Hostname");
    expect(text).toContain("Raft role");
    expect(wrapper.findAll("thead th").map((th) => th.text())).not.toContain("Node ID");
    expect(text).toContain("No nodes");
    expect(wrapper.get("tbody td").attributes("colspan")).toBe("8");
  });

  it("should render in Chinese", async () => {
    await addNodesTranslations("zh");

    const wrapper = await mountNodesPage([]);
    const text = wrapper.text();
    expect(text).toContain("节点");
    expect(text).toContain("主机名");
    expect(text).toContain("Raft 角色");
    expect(wrapper.findAll("thead th").map((th) => th.text())).not.toContain("节点ID");
    expect(text).toContain("无节点");
  });
});

describe("NodesPage identity column", () => {
  it("shows hostname and node id stacked in the same column", async () => {
    await addNodesTranslations("en");

    const wrapper = await mountNodesPage([
      {
        nodeId: "n-a",
        hostname: "agent-a",
        state: "ALIVE",
        lastUpdatedUnixMs: Date.now(),
        processes: [],
      },
    ]);

    const headers = wrapper.findAll("thead th").map((th) => th.text());
    expect(headers).toContain("Hostname");
    expect(headers).not.toContain("Node ID");
    expect(headers).toHaveLength(8);

    const identity = wrapper.get("tbody tr td");
    expect(identity.get("a").text()).toBe("agent-a");
    expect(identity.get("a").attributes("href")).toBe("/nodes/n-a");
    expect(identity.get(".node-id").text()).toBe("n-a");
  });

  it("shows localized accessible Raft badges and stale freshness independently", async () => {
    await addNodesTranslations("en");

    const wrapper = await mountNodesPage([
      {
        nodeId: "n-a",
        hostname: "agent-a",
        state: "FAILED",
        raftRole: "VOTER",
        raftRoleFreshness: "STALE",
        lastUpdatedUnixMs: Date.now(),
        processes: [],
      },
    ]);
    const badge = wrapper.get(".raft-role-badge");
    expect(badge.text()).toBe("Voter");
    expect(badge.attributes("aria-label")).toBe("Raft role: Voter");
    expect(wrapper.get(".raft-role-cell").text()).toContain("STALE");
    expect(wrapper.text()).toContain("FAILED");
  });
});
