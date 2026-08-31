import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import AppShell from "./AppShell.vue";
import shellSource from "./AppShell.vue?raw";
import { clearSession, session, type Me } from "../lib/session";
import { I18NextVue } from "../lib/i18n";
import i18next from 'i18next';

const Blank = defineComponent({ setup: () => () => h("div") });

// Create a test-specific i18n instance without HTTP backend
const testI18n = i18next.createInstance();
testI18n.init({
  lng: 'en',
  fallbackLng: 'en',
  supportedLngs: ['en', 'zh'],
  resources: {
    en: {
      common: {
        nav: {
          overview: 'Overview',
          nodes: 'Nodes',
          processes: 'Processes',
          groups: 'Groups',
          users: 'Users',
          roles: 'Roles',
          audit: 'Audit',
          batches: 'Batches',
          alerts: 'Alerts',
          backup: 'Backup',
          disasterReplica: 'Disaster replica',
          updates: 'Updates',
        },
        actions: {
          logout: 'Logout',
          profile: 'Profile',
          language: 'Language',
          accountMenu: 'Account menu',
          signedIn: 'Signed in',
          expand: 'Expand sidebar',
          collapse: 'Collapse sidebar',
        }
      }
    },
    zh: {
      common: {
        nav: {
          overview: '概览',
          nodes: '节点',
          processes: '进程',
          groups: '分组',
          users: '用户',
          roles: '角色',
          audit: '审计',
          batches: '批次',
          alerts: '告警',
          backup: '备份',
          disasterReplica: '灾备副本',
          updates: '更新',
        },
        actions: {
          logout: '退出登录',
          profile: '个人资料',
          language: '语言',
          accountMenu: '账户菜单',
          signedIn: '已登录',
          expand: '展开侧边栏',
          collapse: '收起侧边栏',
        }
      }
    }
  },
  interpolation: {
    escapeValue: false
  }
});

function me(partial: Partial<Me> = {}): Me {
  return {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf-token",
    permissions: ["user.read", "role.read", "audit.read", "process.read"],
    ...partial,
  };
}

async function mountShell(current: Me, provide: Record<string, unknown> = {}) {
  session.value = current;
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: "/",
        component: AppShell,
        children: [
          { path: "", component: Blank },
          { path: "nodes", component: Blank },
          { path: "processes", component: Blank },
          { path: "groups", component: Blank },
          { path: "batches", component: Blank },
          { path: "alerts", component: Blank },
          { path: "backup", component: Blank },
          { path: "disaster-replica", component: Blank },
          { path: "updates", component: Blank },
          { path: "users", component: Blank },
          { path: "roles", component: Blank },
          { path: "audit", component: Blank },
          { path: "profile", component: Blank },
        ],
      },
      { path: "/login", component: Blank },
    ],
  });
  await router.push("/");
  await router.isReady();
  return mount(AppShell, {
    global: {
      plugins: [router, [I18NextVue, { i18next: testI18n }]],
      provide,
      stubs: { RouterView: true },
    },
  });
}

describe("AppShell", () => {
  beforeEach(() => {
    clearSession();
    localStorage.removeItem("procmesh-sidebar-collapsed");
  });

  it("renders Overview and Processes", async () => {
    const wrapper = await mountShell(me());
    expect(wrapper.text()).toContain("Overview");
    expect(wrapper.text()).toContain("Processes");
  });

  it("renders the brand mark in the sidebar header and mobile topbar", async () => {
    const wrapper = await mountShell(me());
    expect(wrapper.find(".sidebar-header .brand-mark").exists()).toBe(true);
    expect(wrapper.find(".mobile-brand .brand-mark").exists()).toBe(true);
  });

  it("hides Users, Roles, and Audit without read permissions", async () => {
    const wrapper = await mountShell(me({ permissions: ["cluster.read", "node.read", "process.read"] }));
    expect(wrapper.text()).toContain("Overview");
    expect(wrapper.text()).toContain("Nodes");
    expect(wrapper.text()).toContain("Processes");
    expect(wrapper.text()).not.toContain("Users");
    expect(wrapper.text()).not.toContain("Roles");
    expect(wrapper.text()).not.toContain("Audit");
  });

  it("shows Groups nav when node.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["node.read"] }));
    expect(wrapper.text()).toContain("Groups");
  });

  it("hides Groups nav without node.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["process.read"] }));
    expect(wrapper.text()).not.toContain("Groups");
  });

  it("shows Batches nav when batch.execute", async () => {
    const wrapper = await mountShell(me({ permissions: ["node.read", "batch.execute"] }));
    expect(wrapper.text()).toContain("Batches");
  });

  it("hides Batches nav without batch.execute", async () => {
    const wrapper = await mountShell(me({ permissions: ["node.read", "user.read"] }));
    expect(wrapper.text()).not.toContain("Batches");
  });

  it("places Batches after Groups and before Users", async () => {
    const wrapper = await mountShell(
      me({ permissions: ["node.read", "batch.execute", "user.read"] }),
    );
    const labels = wrapper.findAll(".nav-label").map((n) => n.text());
    expect(labels.indexOf("Groups")).toBeGreaterThan(-1);
    expect(labels.indexOf("Batches")).toBeGreaterThan(labels.indexOf("Groups"));
    expect(labels.indexOf("Users")).toBeGreaterThan(labels.indexOf("Batches"));
  });

  it("shows Alerts nav when alert.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["alert.read"] }));
    expect(wrapper.text()).toContain("Alerts");
  });

  it("hides Alerts nav without alert.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["node.read", "user.read"] }));
    expect(wrapper.text()).not.toContain("Alerts");
  });

  it("places Alerts after Batches and before Users", async () => {
    const wrapper = await mountShell(
      me({ permissions: ["node.read", "batch.execute", "alert.read", "user.read"] }),
    );
    const labels = wrapper.findAll(".nav-label").map((n) => n.text());
    expect(labels.indexOf("Alerts")).toBeGreaterThan(labels.indexOf("Batches"));
    expect(labels.indexOf("Users")).toBeGreaterThan(labels.indexOf("Alerts"));
  });

  it("shows Backup nav when backup.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["backup.read"] }));
    expect(wrapper.text()).toContain("Backup");
  });

  it("hides Backup nav without backup.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["node.read", "alert.read", "user.read"] }));
    expect(wrapper.text()).not.toContain("Backup");
  });

  it("places Backup after Alerts and before Users", async () => {
    const wrapper = await mountShell(
      me({ permissions: ["node.read", "alert.read", "backup.read", "user.read"] }),
    );
    const labels = wrapper.findAll(".nav-label").map((n) => n.text());
    expect(labels.indexOf("Backup")).toBeGreaterThan(labels.indexOf("Alerts"));
    expect(labels.indexOf("Users")).toBeGreaterThan(labels.indexOf("Backup"));
  });

  it("shows Disaster replica nav when replication.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["replication.read"] }));
    expect(wrapper.text()).toContain("Disaster replica");
  });

  it("hides Disaster replica nav without replication.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["node.read", "backup.read", "user.read"] }));
    expect(wrapper.text()).not.toContain("Disaster replica");
  });

  it("places Disaster replica after Backup and before Users", async () => {
    const wrapper = await mountShell(
      me({ permissions: ["node.read", "backup.read", "replication.read", "user.read"] }),
    );
    const labels = wrapper.findAll(".nav-label").map((n) => n.text());
    expect(labels.indexOf("Disaster replica")).toBeGreaterThan(labels.indexOf("Backup"));
    expect(labels.indexOf("Users")).toBeGreaterThan(labels.indexOf("Disaster replica"));
  });

  it("shows Updates nav when node.manage", async () => {
    const wrapper = await mountShell(me({ permissions: ["node.manage"] }));
    expect(wrapper.text()).toContain("Updates");
    const updates = wrapper.findAll(".nav-link").find((n) => n.text().includes("Updates"));
    expect(updates?.find(".nav-badge").exists()).toBe(false);
  });

  it("shows Updates nav when cluster.manage", async () => {
    const wrapper = await mountShell(me({ permissions: ["cluster.manage"] }));
    expect(wrapper.text()).toContain("Updates");
  });

  it("hides Updates nav with only cluster.read", async () => {
    const wrapper = await mountShell(me({ permissions: ["cluster.read", "node.read"] }));
    expect(wrapper.text()).not.toContain("Updates");
  });

  it("places Updates after Audit at the bottom of the nav", async () => {
    const wrapper = await mountShell(
      me({
        permissions: [
          "node.read",
          "node.manage",
          "replication.read",
          "user.read",
          "role.read",
          "audit.read",
        ],
      }),
    );
    const labels = wrapper.findAll(".nav-label").map((n) => n.text());
    expect(labels.indexOf("Updates")).toBeGreaterThan(labels.indexOf("Audit"));
    expect(labels.indexOf("Updates")).toBe(labels.length - 1);
  });

  it("highlights Updates when the route is /updates", async () => {
    const wrapper = await mountShell(me({ permissions: ["node.manage"] }));
    await wrapper.vm.$router.push("/updates");
    await flushPromises();
    const updates = wrapper.findAll(".nav-link").find((n) => n.text().includes("Updates"));
    expect(updates?.classes()).toContain("active");
  });

  it("shows Users, Roles, and Audit when permitted", async () => {
    const wrapper = await mountShell(me());
    expect(wrapper.text()).toContain("Users");
    expect(wrapper.text()).toContain("Roles");
    expect(wrapper.text()).toContain("Audit");
  });

  it("logs out with operation_id then goes to /login", async () => {
    const logout = vi.fn().mockResolvedValue({});
    const wrapper = await mountShell(me({ username: "admin" }), { authClient: { logout } });
    await wrapper.get("button.account-trigger").trigger("click");
    await wrapper.get("button.logout").trigger("click");
    await flushPromises();
    expect(logout).toHaveBeenCalled();
    const arg = logout.mock.calls[0][0] as { meta?: { operationId?: string } };
    expect(arg.meta?.operationId).toBeTruthy();
    expect(wrapper.vm.$router.currentRoute.value.path).toBe("/login");
  });

  it("opens an accessible account menu with profile, language, and logout actions", async () => {
    await testI18n.changeLanguage("en");
    const wrapper = await mountShell(me({ username: "admin" }));
    const trigger = wrapper.get("button.account-trigger");

    expect(trigger.attributes("aria-expanded")).toBe("false");
    await trigger.trigger("click");
    expect(trigger.attributes("aria-expanded")).toBe("true");
    expect(wrapper.get(".account-menu").text()).toContain("Profile");
    expect(wrapper.get(".account-menu").text()).toContain("Language");
    expect(wrapper.get(".account-menu").text()).toContain("Logout");

    await wrapper.get('[data-action="profile"]').trigger("click");
    await flushPromises();
    expect(wrapper.vm.$router.currentRoute.value.path).toBe("/profile");
  });

  it("closes the account menu with Escape and returns focus to its trigger", async () => {
    const wrapper = await mountShell(me());
    const trigger = wrapper.get<HTMLButtonElement>("button.account-trigger");
    const focus = vi.spyOn(trigger.element, "focus");
    await trigger.trigger("click");
    await wrapper.get(".account-menu").trigger("keydown", { key: "Escape" });
    await flushPromises();

    expect(wrapper.find(".account-menu").exists()).toBe(false);
    expect(focus).toHaveBeenCalled();
  });

  it("renders navigation in English", async () => {
    await testI18n.changeLanguage('en');
    const wrapper = await mountShell(me());
    expect(wrapper.text()).toContain('Overview');
    expect(wrapper.text()).toContain('Nodes');
    expect(wrapper.text()).toContain('Processes');
    await wrapper.get("button.account-trigger").trigger("click");
    expect(wrapper.text()).toContain('Logout');
  });

  it("renders navigation in Chinese", async () => {
    await testI18n.changeLanguage('zh');
    const wrapper = await mountShell(me());
    expect(wrapper.text()).toContain('概览');
    expect(wrapper.text()).toContain('节点');
    expect(wrapper.text()).toContain('进程');
    await wrapper.get("button.account-trigger").trigger("click");
    expect(wrapper.text()).toContain('退出登录');
  });

  it("locks the shell to the viewport so logout stays on screen", () => {
    expect(shellSource).toMatch(/\.app-shell\s*\{[^}]*height:\s*100dvh/s);
    expect(shellSource).toMatch(/\.content\s*\{[^}]*overflow-y:\s*auto/s);
  });

  it("keeps the brand mark visible when the sidebar is collapsed", async () => {
    localStorage.setItem("procmesh-sidebar-collapsed", "true");
    const wrapper = await mountShell(me());
    expect(wrapper.find(".sidebar.collapsed .brand-mark").exists()).toBe(true);
    expect(wrapper.find(".sidebar.collapsed .brand-full").exists()).toBe(true);
  });

  it("keeps the compact brand visible without horizontal navigation overflow", () => {
    expect(shellSource).toMatch(/\.collapsed \.sidebar-header\s*\{[^}]*position:\s*relative/s);
    expect(shellSource).toMatch(
      /\.collapsed \.collapse-btn\s*\{[^}]*position:\s*absolute[^}]*right:\s*0[^}]*transform:\s*translate\(50%,\s*-50%\)/s,
    );
    expect(shellSource).toMatch(/\.nav\s*\{[^}]*overflow-x:\s*hidden/s);
    expect(shellSource).toMatch(/\.collapsed \.nav-label\s*\{[^}]*display:\s*none/s);
  });

  it("shows matching tooltips for collapsed sidebar controls", async () => {
    await testI18n.changeLanguage("en");
    localStorage.setItem("procmesh-sidebar-collapsed", "true");
    const wrapper = await mountShell(me());
    const overviewLink = wrapper.findAll(".nav-link")[0];
    const expandButton = wrapper.get(".collapse-btn");
    const accountButton = wrapper.get(".account-trigger");

    expect(expandButton.attributes("aria-label")).toBe("Expand sidebar");
    expect(expandButton.attributes("title")).toBeUndefined();
    await expandButton.trigger("mouseenter");
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe("Expand sidebar");
    await expandButton.trigger("mouseleave");

    expect(overviewLink.attributes("aria-label")).toBe("Overview");
    expect(overviewLink.attributes("title")).toBeUndefined();
    await overviewLink.trigger("mouseenter");
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe("Overview");
    await overviewLink.trigger("mouseleave");
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull();

    expect(accountButton.attributes("aria-label")).toBe("Account menu");
    expect(accountButton.attributes("title")).toBeUndefined();
    await accountButton.trigger("mouseenter");
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe("Account menu");
    await accountButton.trigger("mouseleave");

    await overviewLink.trigger("focus");
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe("Overview");
    await overviewLink.trigger("blur");
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull();
    wrapper.unmount();
  });
});
