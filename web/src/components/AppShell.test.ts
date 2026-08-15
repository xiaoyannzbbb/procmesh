import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import AppShell from "./AppShell.vue";
import { clearSession, session, type Me } from "../lib/session";

const Blank = defineComponent({ setup: () => () => h("div") });

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
          { path: "users", component: Blank },
          { path: "roles", component: Blank },
          { path: "audit", component: Blank },
        ],
      },
      { path: "/login", component: Blank },
    ],
  });
  await router.push("/");
  await router.isReady();
  return mount(AppShell, {
    global: {
      plugins: [router],
      provide,
      stubs: { RouterView: true },
    },
  });
}

describe("AppShell", () => {
  beforeEach(() => {
    clearSession();
  });

  it("renders Overview and Processes", async () => {
    const wrapper = await mountShell(me());
    expect(wrapper.text()).toContain("Overview");
    expect(wrapper.text()).toContain("Processes");
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

  it("shows Users, Roles, and Audit when permitted", async () => {
    const wrapper = await mountShell(me());
    expect(wrapper.text()).toContain("Users");
    expect(wrapper.text()).toContain("Roles");
    expect(wrapper.text()).toContain("Audit");
  });

  it("logs out with operation_id then goes to /login", async () => {
    const logout = vi.fn().mockResolvedValue({});
    const wrapper = await mountShell(me({ username: "admin" }), { authClient: { logout } });
    await wrapper.get("button.logout").trigger("click");
    await flushPromises();
    expect(logout).toHaveBeenCalled();
    const arg = logout.mock.calls[0][0] as { meta?: { operationId?: string } };
    expect(arg.meta?.operationId).toBeTruthy();
    expect(wrapper.vm.$router.currentRoute.value.path).toBe("/login");
  });
});
