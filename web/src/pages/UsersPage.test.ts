import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import Drawer from "../components/Drawer.vue";
import UsersPage from "./UsersPage.vue";

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

async function mountUsers(
  permissions: string[] = ["user.read", "user.create", "user.update", "role.read", "role.manage"],
  users: unknown[] = [],
  roles: unknown[] = [],
  bindings: unknown[] = [],
  roleState: "resolved" | "pending" | "error" = "resolved",
) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions,
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const userClient = {
    listUsers: vi.fn().mockResolvedValue({ users }),
    createUser: vi.fn(),
    disableUser: vi.fn(),
  };
  const roleClient = {
    listRoles: roleState === "pending"
      ? vi.fn().mockReturnValue(new Promise(() => undefined))
      : roleState === "error"
        ? vi.fn().mockRejectedValue(new Error("role service unavailable"))
        : vi.fn().mockResolvedValue({ roles, bindings }),
    grantRole: vi.fn().mockResolvedValue({}),
  };
  const wrapper = mount(UsersPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { userClient, roleClient },
      stubs: { Teleport: true },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, userClient, roleClient };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("UsersPage", () => {
  it("disables Create when password is shorter than 10", async () => {
    const { wrapper, userClient } = await mountUsers();
    await wrapper.get('[data-action="create-user"]').trigger("click");
    const createDrawer = wrapper.findAllComponents(Drawer)[0];
    await createDrawer.get('input[name="username"]').setValue("alice");
    await createDrawer.get('input[name="password"]').setValue("short");
    const submit = createDrawer.get('button[type="submit"]');
    expect(submit.attributes("disabled")).toBeDefined();
    await createDrawer.get("form").trigger("submit");
    await flushPromises();
    expect(userClient.createUser).not.toHaveBeenCalled();
  });

  it("shows bound roles and grants a role from the selected user row", async () => {
    const users = [
      { userId: "u-alice", username: "alice", displayName: "Alice", email: "alice@example.com", status: "ACTIVE", lastLoginUnix: 0 },
    ];
    const roles = [{ roleId: "operator", name: "Operator", permissions: [] }];
    const bindings = [{ userId: "u-alice", roleId: "operator", scopeType: "AGENT_GROUP", scopeId: "finance" }];
    const { wrapper, roleClient } = await mountUsers(undefined, users, roles, bindings);

    const row = wrapper.get('tr[data-user-id="u-alice"]');
    expect(row.text()).toContain("Operator");
    expect(row.text()).toContain("finance");

    await row.get('[data-action="bind-role"]').trigger("click");
    const bindDrawer = wrapper.findAllComponents(Drawer)[1];
    expect(bindDrawer.text()).toContain("alice");
    await bindDrawer.get('select[name="role_id"]').setValue("operator");
    await bindDrawer.get("form").trigger("submit");
    await flushPromises();

    expect(roleClient.grantRole).toHaveBeenCalledWith(
      expect.objectContaining({
        userId: "u-alice",
        roleId: "operator",
        scopeType: "CLUSTER",
        scopeId: "",
      }),
    );
    expect(bindDrawer.props("open")).toBe(false);
  });

  it("shows role loading without exposing a bind action", async () => {
    const users = [{ userId: "u-alice", username: "alice", displayName: "Alice", status: "ACTIVE", lastLoginUnix: 0 }];
    const { wrapper } = await mountUsers(undefined, users, [], [], "pending");
    const row = wrapper.get('tr[data-user-id="u-alice"]');

    expect(row.text()).toContain("users.roles.loading");
    expect(row.find('[data-action="bind-role"]').exists()).toBe(false);
  });

  it("shows unavailable role data instead of an empty binding state", async () => {
    const users = [{ userId: "u-alice", username: "alice", displayName: "Alice", status: "ACTIVE", lastLoginUnix: 0 }];
    const { wrapper } = await mountUsers(undefined, users, [], [], "error");
    const row = wrapper.get('tr[data-user-id="u-alice"]');

    expect(row.text()).toContain("users.roles.unavailable");
    expect(row.find('[data-action="bind-role"]').exists()).toBe(false);
  });

  it("hides role data when the operator cannot read roles", async () => {
    const users = [{ userId: "u-alice", username: "alice", displayName: "Alice", status: "ACTIVE", lastLoginUnix: 0 }];
    const { wrapper, roleClient } = await mountUsers(["user.read", "user.update"], users);

    expect(wrapper.find(".roles-column-header").exists()).toBe(false);
    expect(wrapper.find('[data-action="bind-role"]').exists()).toBe(false);
    expect(roleClient.listRoles).not.toHaveBeenCalled();
  });
});

describe("UsersPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      users: {
        title: "Users",
        loading: "Loading…",
        noUsers: "No users",
        table: {
          username: "Username",
          display: "Display",
          email: "Email",
          status: "Status",
          lastLogin: "Last login",
        },
        createUser: {
          title: "Create",
          username: "Username",
          password: "Password",
        },
      },
    });

    const { wrapper } = await mountUsers();
    const text = wrapper.text();
    expect(text).toContain("Users");
    expect(text).toContain("Username");
    expect(text).toContain("Display");
    expect(text).toContain("Email");
    expect(text).toContain("No users");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      users: {
        title: "用户",
        loading: "加载中…",
        noUsers: "无用户",
        table: {
          username: "用户名",
          display: "显示名称",
          email: "邮箱",
          status: "状态",
          lastLogin: "最后登录",
        },
        createUser: {
          title: "创建",
          username: "用户名",
          password: "密码",
        },
      },
    });

    const { wrapper } = await mountUsers();
    const text = wrapper.text();
    expect(text).toContain("用户");
    expect(text).toContain("用户名");
    expect(text).toContain("显示名称");
    expect(text).toContain("邮箱");
    expect(text).toContain("无用户");
  });
});
