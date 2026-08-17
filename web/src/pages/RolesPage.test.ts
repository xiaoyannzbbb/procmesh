import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import Drawer from "../components/Drawer.vue";
import RolesPage from "./RolesPage.vue";

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {},
      },
    },
  });
});

const mounted: Array<{ unmount: () => void }> = [];

async function mountRolesPage(
  roles: unknown[] = [],
  bindings: unknown[] = [],
  permissions: string[] = [],
  users: unknown[] = [],
) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions,
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const roleClient = {
    listRoles: vi.fn().mockResolvedValue({ roles, bindings }),
    createRole: vi.fn().mockResolvedValue({}),
    grantRole: vi.fn().mockResolvedValue({}),
  };
  const userClient = {
    listUsers: vi.fn().mockResolvedValue({ users }),
  };
  const wrapper = mount(RolesPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { roleClient, userClient },
      stubs: { Teleport: true },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, roleClient, userClient };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("RolesPage grant scope", () => {
  it("includes AGENT_GROUP and PROCESS_GROUP in the scope dropdown", async () => {
    const { wrapper } = await mountRolesPage([], [], ["role.read", "role.manage", "user.read"]);
    await wrapper.get('[data-action="grant-role"]').trigger("click");
    const grantDrawer = wrapper.findAllComponents(Drawer)[1];
    const values = grantDrawer.findAll('select[name="scope_type"] option').map((opt) => {
      return (opt.element as HTMLOptionElement).value;
    });
    expect(values).toContain("AGENT_GROUP");
    expect(values).toContain("PROCESS_GROUP");
  });
});

describe("RolesPage drawers", () => {
  it("opens create and grant forms from page header actions", async () => {
    const { wrapper } = await mountRolesPage([], [], ["role.read", "role.manage", "user.read"]);
    const drawers = wrapper.findAllComponents(Drawer);

    expect(drawers).toHaveLength(2);
    expect(drawers[0].props("open")).toBe(false);
    expect(drawers[1].props("open")).toBe(false);

    await wrapper.get('[data-action="create-role"]').trigger("click");
    expect(drawers[0].props("open")).toBe(true);

    await wrapper.get('[data-action="grant-role"]').trigger("click");
    expect(drawers[1].props("open")).toBe(true);
  });

  it("searches users and grants the selected user a role", async () => {
    const roles = [{ roleId: "operator", name: "Operator", permissions: [] }];
    const users = [
      { userId: "u-alice", username: "alice", displayName: "Alice Chen", email: "alice@example.com", status: "ACTIVE" },
      { userId: "u-bob", username: "bob", displayName: "Bob Li", email: "bob@example.com", status: "ACTIVE" },
    ];
    const { wrapper, roleClient } = await mountRolesPage(
      roles,
      [],
      ["role.read", "role.manage", "user.read"],
      users,
    );

    await wrapper.get('[data-action="grant-role"]').trigger("click");
    await flushPromises();
    const grantDrawer = wrapper.findAllComponents(Drawer)[1];
    const searchInput = grantDrawer.get('input[name="user_search"]');
    await searchInput.setValue("Alice Chen");
    await wrapper.vm.$nextTick();
    const filteredGrantDrawer = wrapper.findAllComponents(Drawer)[1];
    expect(filteredGrantDrawer.find('input[name="user_id"][value="u-alice"]').exists()).toBe(true);
    expect(filteredGrantDrawer.find('input[name="user_id"][value="u-bob"]').exists()).toBe(false);

    await filteredGrantDrawer.get('input[name="user_id"][value="u-alice"]').setValue(true);
    await filteredGrantDrawer.get('input[name="user_search"]').setValue("bob");
    await wrapper.vm.$nextTick();
    const refilteredGrantDrawer = wrapper.findAllComponents(Drawer)[1];
    expect(refilteredGrantDrawer.find('[data-selected-user-id="u-alice"]').exists()).toBe(true);
    expect(refilteredGrantDrawer.find('input[name="user_id"][value="u-bob"]').exists()).toBe(true);

    await refilteredGrantDrawer.get('select[name="role_id"]').setValue("operator");
    await refilteredGrantDrawer.get("form").trigger("submit");
    await flushPromises();

    expect(roleClient.grantRole).toHaveBeenCalledWith(
      expect.objectContaining({
        userId: "u-alice",
        roleId: "operator",
        scopeType: "CLUSTER",
        scopeId: "",
      }),
    );
    expect(grantDrawer.props("open")).toBe(false);
  });
});

describe("RolesPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      roles: {
        title: "Roles",
        loading: "Loading…",
        noRoles: "No roles",
        table: {
          name: "Name",
          type: "Type",
          permissions: "Permissions",
        },
        bindings: {
          title: "Bindings",
          noBindings: "No bindings",
        },
      },
    });

    const { wrapper } = await mountRolesPage();
    const text = wrapper.text();
    expect(text).toContain("Roles");
    expect(text).toContain("Name");
    expect(text).toContain("Type");
    expect(text).toContain("Permissions");
    expect(text).toContain("Bindings");
    expect(text).toContain("No roles");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      roles: {
        title: "角色",
        loading: "加载中…",
        noRoles: "无角色",
        table: {
          name: "名称",
          type: "类型",
          permissions: "权限",
        },
        bindings: {
          title: "绑定",
          noBindings: "无绑定",
        },
      },
    });

    const { wrapper } = await mountRolesPage();
    const text = wrapper.text();
    expect(text).toContain("角色");
    expect(text).toContain("名称");
    expect(text).toContain("类型");
    expect(text).toContain("权限");
    expect(text).toContain("绑定");
    expect(text).toContain("无角色");
  });
});
