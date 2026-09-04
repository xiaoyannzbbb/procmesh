import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { createMemoryHistory, createRouter, RouterView } from "vue-router";
import { session } from "../lib/session";
import NodesPage from "./NodesPage.vue";

const Blank = defineComponent({ setup: () => () => h("div") });

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

async function mountNodesPage(
  nodes: unknown[] = [],
  query: Record<string, string> = {},
  createJoinToken = vi.fn(),
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const nodeClient = { listNodes: vi.fn().mockResolvedValue({ nodes }), createJoinToken };
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/nodes", component: NodesPage },
      { path: "/nodes/:id", component: Blank },
      { path: "/other", component: Blank },
    ],
  });
  await router.push({ path: "/nodes", query });
  await router.isReady();
  const wrapper = mount(RouterView, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
        router,
      ],
      provide: { nodeClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, nodeClient, router };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
  document.body.innerHTML = "";
  document.body.style.overflow = "";
});

async function addNodesTranslations(language: "en" | "zh") {
  const chinese = language === "zh";
  await i18n.changeLanguage(language);
  await i18n.addResourceBundle(language, "common", {
    nodes: {
      title: chinese ? "节点" : "Nodes",
      subtitle: chinese ? "共 {{count}} 个节点" : "{{count}} total",
      showing: chinese ? "显示 {{shown}} / {{total}}" : "Showing {{shown}} of {{total}}",
      eyebrow: chinese ? "集群成员" : "Cluster members",
      loading: chinese ? "加载中…" : "Loading…",
      noNodes: chinese ? "无节点" : "No nodes",
      emptyHint: chinese ? "没有符合当前筛选条件的节点。" : "No nodes match the current filters.",
      emptyNone: chinese ? "集群尚未加入任何 Agent。" : "No agents have joined this cluster yet.",
      emptyClear: chinese ? "清除筛选" : "Clear filters",
      search: chinese ? "搜索" : "Search",
      searchPlaceholder: chinese ? "搜索主机名或节点 ID" : "Search hostname or node ID",
      clearFilters: chinese ? "清除筛选" : "Clear filters",
      refresh: chinese ? "刷新" : "Refresh",
      lastUpdated: chinese ? "更新于 {{age}}" : "Updated {{age}}",
      updatedJustNow: chinese ? "刚刚" : "just now",
      updatedSeconds: chinese ? "{{count}} 秒前" : "{{count}}s ago",
      updatedMinutes: chinese ? "{{count}} 分钟前" : "{{count}}m ago",
      openRow: chinese ? "打开 {{name}}" : "Open {{name}}",
      staleBanner: chinese
        ? "部分节点数据为 STALE，不能当作实时健康状态。"
        : "Some node data is STALE. Do not treat it as live health.",
      processesMore: chinese ? "还有 {{count}} 个" : "+{{count}} more",
      processCount: chinese ? "{{count}} 个进程" : "{{count}} processes",
      diskPaused: chinese ? "历史写入已暂停" : "History writes paused",
      add: {
        open: chinese ? "添加节点" : "Add node",
        title: chinese ? "添加节点" : "Add node",
        close: chinese ? "关闭" : "Close",
        intro: chinese ? "在新节点运行命令。" : "Run this command on the new node.",
        seed: chinese ? "种子节点" : "Seed node",
        selectSeed: chinese ? "选择种子节点" : "Select a seed node",
        noSeeds: chinese ? "没有可用种子节点。" : "No eligible seed nodes are available.",
        nodesLoading: chinese ? "加载中" : "Loading seed nodes...",
        nodesFailed: chinese ? "加载失败：{{detail}}" : "Could not load seed nodes: {{detail}}",
        cachedWarning: chinese ? "刷新失败：{{detail}}" : "Refresh failed: {{detail}}",
        refresh: chinese ? "重试" : "Retry",
        freshnessWarning: "{{freshness}}",
        duration: chinese ? "有效期" : "Valid for",
        durationHint: chinese ? "正整数" : "Positive whole number",
        unit: chinese ? "单位" : "Duration unit",
        units: { seconds: "seconds", minutes: "minutes", hours: "hours", days: "days" },
        uses: chinese ? "次数" : "Maximum uses",
        usesHint: chinese ? "正整数" : "Positive whole number",
        invalidDuration: chinese ? "无效有效期" : "Invalid duration",
        invalidUses: chinese ? "无效次数" : "Invalid uses",
        generate: chinese ? "生成" : "Generate join command",
        regenerate: chinese ? "重新生成" : "Generate a new token",
        generating: chinese ? "生成中" : "Generating...",
        permissionLost: chinese ? "权限已撤销。代码：{{code}}" : "Permission revoked. Code: {{code}}",
        createFailed: chinese ? "创建失败：{{detail}}" : "Could not create token: {{detail}}",
        tokenId: chinese ? "令牌 ID" : "Token ID",
        expires: chinese ? "到期" : "Expires",
        remainingUses: chinese ? "剩余次数" : "Remaining uses",
        secretWarning: chinese ? "仅显示一次。" : "Shown only once.",
        executeOnNewNode: chinese ? "在新节点运行" : "Run on the new node",
        commandLabel: chinese ? "加入命令" : "Join command",
        copy: chinese ? "复制" : "Copy command",
        copied: chinese ? "已复制" : "Command copied",
        copyFailed: chinese ? "复制失败" : "Copy failed",
        customServerTitle: chinese ? "自定义服务" : "Custom server",
        customServerHint: chinese ? "服务参数说明" : "Server option help",
        parametersChanged: chinese ? "参数已变更" : "Parameters changed",
        seedInvalid: chinese ? "种子节点不可用" : "Seed unavailable",
        closeTitle: chinese ? "关闭并丢失令牌？" : "Close and lose the token?",
        closeMessage: chinese ? "明文不可恢复。" : "The plaintext cannot be recovered.",
        closePendingMessage: chinese ? "请求可能仍会完成。" : "The request may still complete.",
        closeConfirm: chinese ? "确认关闭" : "Close drawer",
        cancel: chinese ? "保持打开" : "Keep open",
      },
      stats: {
        total: chinese ? "全部" : "Total",
        alive: chinese ? "存活" : "Alive",
        suspect: chinese ? "可疑" : "Suspect",
        failed: chinese ? "失败" : "Failed",
        stale: chinese ? "过期" : "Stale",
      },
      state: {
        alive: chinese ? "存活" : "Alive",
        suspect: chinese ? "可疑" : "Suspect",
        failed: chinese ? "失败" : "Failed",
        left: chinese ? "已离开" : "Left",
        removed: chinese ? "已移除" : "Removed",
        revoked: chinese ? "已吊销" : "Revoked",
        unknown: chinese ? "未知" : "Unknown",
        badgeLabel: chinese ? "节点状态：{{state}}" : "Node state: {{state}}",
      },
      resources: {
        cpu: chinese ? "CPU" : "CPU",
        memory: chinese ? "内存" : "Memory",
        disk: chinese ? "磁盘" : "Disk",
        unknown: chinese ? "未知" : "unknown",
        meterLabel: "{{name}} {{value}}",
      },
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

function sampleNodes(now = Date.now()) {
  return [
    {
      nodeId: "n-a",
      hostname: "agent-a",
      state: "ALIVE",
      raftRole: "LEADER",
      raftRoleFreshness: "LIVE",
      agentVersion: "0.1.15",
      lastUpdatedUnixMs: now,
      resources: { cpuPercent: 12, memoryPercent: 34, diskPercent: 56 },
      processes: [
        { name: "api", observed: "RUNNING", freshnessUnixMs: now },
        { name: "worker", observed: "STOPPED", freshnessUnixMs: now },
      ],
    },
    {
      nodeId: "n-b",
      hostname: "agent-b",
      state: "SUSPECT",
      raftRole: "VOTER",
      raftRoleFreshness: "STALE",
      agentVersion: "0.1.14",
      lastUpdatedUnixMs: now - 30_000,
      resources: { cpuPercent: 80, memoryPercent: 86, diskPercent: 91 },
      processes: [],
    },
    {
      nodeId: "n-c",
      hostname: "agent-c",
      state: "FAILED",
      raftRole: "VOTER",
      raftRoleFreshness: "STALE",
      lastUpdatedUnixMs: now - 60_000,
      resources: { cpuPercent: 1, memoryPercent: 2, diskPercent: 3 },
      processes: [{ name: "sleep", observed: "RUNNING", freshnessUnixMs: now - 60_000 }],
    },
  ];
}

describe("NodesPage i18n", () => {
  it("should render in English", async () => {
    await addNodesTranslations("en");

    const { wrapper } = await mountNodesPage([]);
    const text = wrapper.text();
    expect(text).toContain("Nodes");
    expect(text).toContain("Hostname");
    expect(text).toContain("Raft role");
    expect(wrapper.findAll("thead th").map((th) => th.text())).not.toContain("Node ID");
    expect(text).toContain("No nodes");
    expect(wrapper.findAll("thead th").map((th) => th.text())).not.toContain("Updated");
    expect(wrapper.get("tbody td").attributes("colspan")).toBe("7");
  });

  it("should render in Chinese", async () => {
    await addNodesTranslations("zh");

    const { wrapper } = await mountNodesPage([]);
    const text = wrapper.text();
    expect(text).toContain("节点");
    expect(text).toContain("主机名");
    expect(text).toContain("Raft 角色");
    expect(wrapper.findAll("thead th").map((th) => th.text())).not.toContain("节点ID");
    expect(wrapper.findAll("thead th").map((th) => th.text())).not.toContain("更新时间");
    expect(text).toContain("无节点");
  });
});

describe("NodesPage identity column", () => {
  it("shows hostname and node id stacked in the same column", async () => {
    await addNodesTranslations("en");

    const { wrapper } = await mountNodesPage([
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
    expect(headers).toContain("Freshness");
    expect(headers).not.toContain("Updated");
    expect(headers).toHaveLength(7);

    const identity = wrapper.get("tbody tr.data-row td");
    expect(identity.get("a").text()).toBe("agent-a");
    expect(identity.get("a").attributes("href")).toBe("/nodes/n-a");
    expect(identity.get(".node-id").text()).toBe("n-a");
  });

  it("shows freshness and update age stacked in the same column", async () => {
    await addNodesTranslations("en");

    const { wrapper } = await mountNodesPage(sampleNodes(Date.now()));
    const rows = wrapper.findAll("tbody tr.data-row");
    const live = rows[0]?.get(".freshness-cell");
    const stale = rows[1]?.get(".freshness-cell");

    expect(live?.get(".freshness-badge").text()).toBe("LIVE");
    expect(live?.get(".cell-updated").text()).toBe("just now");
    expect(stale?.get(".freshness-badge").text()).toBe("STALE");
    expect(stale?.get(".cell-updated").text()).toBe("30s ago");
    expect(wrapper.find("td.cell-updated").exists()).toBe(false);
  });

  it("shows localized accessible Raft badges and stale freshness independently", async () => {
    await addNodesTranslations("en");

    const { wrapper } = await mountNodesPage([
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
    const statePill = wrapper.get(".state-pill");
    expect(statePill.text()).toBe("Failed");
    expect(statePill.attributes("data-state")).toBe("FAILED");
    expect(statePill.classes()).toContain("danger");
    expect(statePill.classes()).not.toContain("ok");
  });
});

describe("NodesPage cluster summary", () => {
  it("shows membership and freshness counts without painting stale as healthy", async () => {
    await addNodesTranslations("en");
    const { wrapper } = await mountNodesPage(sampleNodes());

    expect(wrapper.get('[data-stat="total"] .summary-value').text()).toBe("3");
    expect(wrapper.get('[data-stat="alive"] .summary-value').text()).toBe("1");
    expect(wrapper.get('[data-stat="suspect"] .summary-value').text()).toBe("1");
    expect(wrapper.get('[data-stat="failed"] .summary-value').text()).toBe("1");
    expect(wrapper.get('[data-stat="stale"] .summary-value').text()).toBe("2");
    expect(wrapper.get('[data-stat="stale"] .summary-value').classes()).toContain("warn");
    expect(wrapper.get('[data-stat="stale"] .summary-value').classes()).not.toContain("ok");
    expect(wrapper.get('[role="status"]').text()).toContain("STALE");
  });

  it("filters by failed membership from the summary chips", async () => {
    await addNodesTranslations("en");
    const { wrapper } = await mountNodesPage(sampleNodes());

    await wrapper.get('[data-stat="failed"]').trigger("click");
    await wrapper.vm.$nextTick();

    const rows = wrapper.findAll("tbody tr.data-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.text()).toContain("agent-c");
    expect(wrapper.text()).toContain("Showing 1 of 3");
  });

  it("filters by hostname search", async () => {
    await addNodesTranslations("en");
    const { wrapper } = await mountNodesPage(sampleNodes());

    await wrapper.get('input[name="search"]').setValue("agent-b");
    await wrapper.vm.$nextTick();

    const rows = wrapper.findAll("tbody tr.data-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.text()).toContain("agent-b");
    expect(rows[0]?.text()).not.toContain("agent-a");
  });
});

describe("NodesPage resources and processes", () => {
  it("renders resource meters with numeric labels and threshold tones", async () => {
    await addNodesTranslations("en");
    const { wrapper } = await mountNodesPage(sampleNodes());

    const cpu = wrapper.get('[data-resource="cpu"]');
    expect(cpu.text()).toContain("12%");
    expect(cpu.attributes("aria-label")).toContain("CPU");

    const disk = wrapper.findAll('[data-resource="disk"]')[1];
    expect(disk?.text()).toContain("91%");
    expect(disk?.classes()).toContain("danger");
    expect(disk?.classes()).not.toContain("ok");
  });

  it("shows compact process chips instead of a raw nested dump", async () => {
    await addNodesTranslations("en");
    const { wrapper } = await mountNodesPage(sampleNodes());

    const rows = wrapper.findAll("tbody tr.data-row");
    const firstRow = rows[0];
    expect(firstRow?.text()).toContain("api");
    expect(firstRow?.text()).toContain("worker");
    expect(firstRow?.find(".proc-chip").exists()).toBe(true);
    const staleChip = rows[2]?.get(".proc-chip");
    expect(staleChip?.text()).toContain("sleep");
    expect(staleChip?.classes()).toContain("warn");
    expect(staleChip?.classes()).not.toContain("ok");
  });
});

describe("NodesPage empty and navigation", () => {
  it("explains an unfiltered empty cluster", async () => {
    await addNodesTranslations("en");
    const { wrapper } = await mountNodesPage([]);
    expect(wrapper.text()).toContain("No agents have joined this cluster yet.");
  });

  it("opens the node detail route from a row click", async () => {
    await addNodesTranslations("en");
    const { wrapper, router } = await mountNodesPage(sampleNodes());
    await wrapper.get("tbody tr.data-row").trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(router.currentRoute.value.path).toBe("/nodes/n-a");
  });
});

describe("NodesPage add node permission", () => {
  it("shows the entry only with node.manage and opens the drawer", async () => {
    await addNodesTranslations("en");
    session.value = {
      userId: "u-1",
      username: "admin",
      csrfToken: "csrf",
      permissions: ["node.manage"],
    };
    const { wrapper } = await mountNodesPage(sampleNodes());

    const button = wrapper.get('[data-action="add-node"]');
    expect(button.text()).toBe("Add node");
    await button.trigger("click");
    await flushPromises();
    expect(document.body.querySelector('[role="dialog"]')).not.toBeNull();
  });

  it("does not render the entry without node.manage", async () => {
    await addNodesTranslations("en");
    session.value = {
      userId: "u-1",
      username: "reader",
      csrfToken: "csrf",
      permissions: ["node.read"],
    };
    const { wrapper } = await mountNodesPage(sampleNodes());
    expect(wrapper.find('[data-action="add-node"]').exists()).toBe(false);
  });
});

describe("NodesPage add node route lifecycle", () => {
  const eligibleNode = {
    nodeId: "n-a",
    hostname: "agent-a",
    state: "ALIVE",
    apiAddress: "10.0.0.11:18680",
    lastUpdatedUnixMs: Date.now(),
    processes: [],
  };

  async function startCreate(createJoinToken: ReturnType<typeof vi.fn>) {
    await addNodesTranslations("en");
    session.value = {
      userId: "u-1",
      username: "admin",
      csrfToken: "csrf",
      permissions: ["node.manage"],
    };
    const mountedPage = await mountNodesPage([eligibleNode], {}, createJoinToken);
    await mountedPage.wrapper.get('[data-action="add-node"]').trigger("click");
    await flushPromises();
    const seed = document.body.querySelector<HTMLSelectElement>('select[name="seed"]')!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    document.body.querySelector<HTMLFormElement>("form.join-form")!.dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    );
    await flushPromises();
    return mountedPage;
  }

  it("cancels result navigation without clearing the token and clears it on confirmation", async () => {
    const page = await startCreate(
      vi.fn().mockResolvedValue({
        tokenId: "jt-route",
        token: "pmj_route_secret",
        expiresUnix: 1_800_000_000n,
        uses: 1,
      }),
    );
    expect(document.body.textContent).toContain("pmj_route_secret");

    const cancelledNavigation = page.router.push("/other");
    await flushPromises();
    expect(document.body.textContent).toContain("Close and lose the token?");
    const keepOpen = Array.from(document.body.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "Keep open",
    ) as HTMLButtonElement;
    keepOpen.click();
    await cancelledNavigation;
    await flushPromises();
    expect(page.router.currentRoute.value.path).toBe("/nodes");
    expect(document.body.textContent).toContain("pmj_route_secret");

    const confirmedNavigation = page.router.push("/other");
    await flushPromises();
    const confirm = Array.from(document.body.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "Close drawer",
    ) as HTMLButtonElement;
    confirm.click();
    await confirmedNavigation;
    await flushPromises();
    expect(page.router.currentRoute.value.path).toBe("/other");
    expect(document.body.textContent).not.toContain("pmj_route_secret");
  });

  it("guards a pending request and ignores its response after confirmed navigation", async () => {
    let resolve!: (value: unknown) => void;
    const createJoinToken = vi.fn().mockImplementation(
      () => new Promise((done) => { resolve = done; }),
    );
    const page = await startCreate(createJoinToken);

    const cancelledNavigation = page.router.push("/other");
    await flushPromises();
    expect(document.body.textContent).toContain("The request may still complete.");
    const keepOpen = Array.from(document.body.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "Keep open",
    ) as HTMLButtonElement;
    keepOpen.click();
    await cancelledNavigation;
    expect(page.router.currentRoute.value.path).toBe("/nodes");
    expect(document.body.textContent).toContain("Generating...");

    const confirmedNavigation = page.router.push("/other");
    await flushPromises();
    const confirm = Array.from(document.body.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "Close drawer",
    ) as HTMLButtonElement;
    confirm.click();
    await confirmedNavigation;
    resolve({ tokenId: "jt-late", token: "pmj_late", expiresUnix: 1_800_000_000n, uses: 1 });
    await flushPromises();
    expect(page.router.currentRoute.value.path).toBe("/other");
    expect(document.body.textContent).not.toContain("pmj_late");
    expect(document.body.textContent).not.toContain("jt-late");
  });
});
