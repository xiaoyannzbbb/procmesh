import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import Drawer from "../components/Drawer.vue";
import RolesPage from "./RolesPage.vue";

const EN_ROLE_SCOPES = {
  scopeCluster: "Entire cluster",
  scopeAgent: "Specific agent",
  scopeAgentGroup: "Agent group",
  scopeProcessGroup: "Process group",
};

const EN_ROLES = {
  title: "Roles",
  loading: "Loading…",
  stats: { roles: "Roles", bindings: "Bindings" },
  table: { name: "Name", type: "Type", permissions: "Permissions", bindings: "Bindings", actions: "Actions" },
  type: { builtin: "Built-in", custom: "Custom" },
  perms: { more: "+{{count}}", less: "Show less" },
  empty: {
    title: "No roles yet",
    hint: "Create a role to bundle permissions, then grant it to a user.",
  },
  bindings: {
    title: "Bindings",
    empty: { title: "No bindings yet", hint: "Grant a role to a user to see it here." },
    table: { userId: "User ID", role: "Role", scope: "Scope", scopeId: "Scope ID", actions: "Actions" },
  },
  error: { title: "Could not load roles", retry: "Retry" },
  createRole: {
    title: "Create role",
    name: "Name",
    permissions: "Permissions",
    search: "Search permissions",
    searchPlaceholder: "Filter by permission name",
    selectAll: "Select all",
    clearSelection: "Clear selection",
    selectedCount: "{{count}} permissions selected",
    noPermissionMatch: "No matching permissions",
    groups: {
      cluster: "Cluster",
      node: "Node",
      process: "Process",
      user: "User",
      role: "Role",
      audit: "Audit",
      command: "Command",
      batch: "Batch",
      alert: "Alert",
      backup: "Backup",
    },
    create: "Create",
    success: "Role created",
  },
  updateRole: {
    title: "Edit role",
    save: "Save changes",
    success: "Role updated",
  },
  deleteRole: {
    title: "Delete role?",
    message: "Delete role “{{name}}”? This action cannot be undone.",
    confirm: "Delete role",
    success: "Role deleted",
  },
  revoke: {
    action: "Unbind",
    title: "Remove binding?",
    message: "Remove {{role}} from user {{userId}}?",
    confirm: "Remove binding",
    success: "Binding removed",
  },
  builtinActionUnavailable: "Built-in roles and their bindings cannot be changed",
  grant: {
    title: "Grant role to user",
    searchUsers: "Select user",
    searchPlaceholder: "Search username, display name, email, or user ID",
    selectUser: "Select a user to grant",
    selectedUser: "Selected user",
    clearSelection: "Clear selected user",
    userMeta: "{{username}} · {{userId}}",
    noUsers: "No matching users",
    role: "Role",
    selectRole: "Select role",
    scope: "Scope",
    scopeId: "Scope ID",
    grant: "Grant role",
    success: "Role granted",
  },
  actions: { createRole: "Create role", grantRole: "Grant to user" },
};

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: { roles: EN_ROLES, role: EN_ROLE_SCOPES },
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
  listRolesImpl?: () => Promise<unknown>,
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
    listRoles: listRolesImpl
      ? vi.fn(listRolesImpl)
      : vi.fn().mockResolvedValue({ roles, bindings }),
    createRole: vi.fn().mockResolvedValue({}),
    updateRole: vi.fn().mockResolvedValue({}),
    deleteRole: vi.fn().mockResolvedValue({}),
    grantRole: vi.fn().mockResolvedValue({}),
    revokeRole: vi.fn().mockResolvedValue({}),
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

describe("RolesPage role table", () => {
  it("renders a type badge for built-in and custom roles", async () => {
    const roles = [
      { roleId: "super_admin", name: "Super admin", permissions: [] },
      { roleId: "r-custom", name: "On-call", permissions: [] },
    ];
    const { wrapper } = await mountRolesPage(roles, [], ["role.read"]);

    expect(wrapper.get('[data-role-id="super_admin"] [data-type="builtin"]').text()).toBe("Built-in");
    expect(wrapper.get('[data-role-id="r-custom"] [data-type="custom"]').text()).toBe("Custom");
  });

  it("aggregates binding counts per role", async () => {
    const roles = [
      { roleId: "operator", name: "Operator", permissions: ["process.read"] },
      { roleId: "viewer", name: "Viewer", permissions: ["cluster.read"] },
    ];
    const bindings = [
      { userId: "u1", roleId: "operator", scopeType: "CLUSTER", scopeId: "" },
      { userId: "u2", roleId: "operator", scopeType: "AGENT", scopeId: "agent-1" },
    ];
    const { wrapper } = await mountRolesPage(roles, bindings, ["role.read"]);

    expect(wrapper.get('[data-role-id="operator"] [data-cell="bindings"]').text()).toBe("2");
    expect(wrapper.get('[data-role-id="viewer"] [data-cell="bindings"]').text()).toBe("0");
  });

  it("collapses long permission lists behind a toggle", async () => {
    const permissions = Array.from({ length: 8 }, (_, index) => `perm.p${index}`);
    const { wrapper } = await mountRolesPage(
      [{ roleId: "r1", name: "Ops", permissions }],
      [],
      ["role.read"],
    );

    expect(wrapper.findAll('[data-role-id="r1"] [data-perm-chip]')).toHaveLength(6);
    const toggle = wrapper.get('[data-action="toggle-perms"]');
    expect(toggle.attributes("aria-expanded")).toBe("false");
    expect(toggle.text()).toContain("+2");

    await toggle.trigger("click");
    expect(wrapper.findAll('[data-role-id="r1"] [data-perm-chip]')).toHaveLength(8);
    expect(wrapper.get('[data-action="toggle-perms"]').attributes("aria-expanded")).toBe("true");

    await wrapper.get('[data-action="toggle-perms"]').trigger("click");
    expect(wrapper.findAll('[data-role-id="r1"] [data-perm-chip]')).toHaveLength(6);
  });

  it("shows a placeholder for roles without permissions", async () => {
    const { wrapper } = await mountRolesPage(
      [{ roleId: "r1", name: "Empty", permissions: [] }],
      [],
      ["role.read"],
    );

    expect(wrapper.get('[data-role-id="r1"] [data-cell="permissions"]').text()).toBe("—");
    expect(wrapper.find('[data-role-id="r1"] [data-perm-chip]').exists()).toBe(false);
  });

  it("enables custom-role actions and disables built-in role actions", async () => {
    const roles = [
      { roleId: "viewer", name: "Viewer", permissions: ["cluster.read"] },
      { roleId: "r-custom", name: "On-call", permissions: ["process.read"] },
    ];
    const { wrapper } = await mountRolesPage(roles, [], ["role.read", "role.manage"]);

    expect(wrapper.get("thead").text()).toContain("Actions");
    const builtin = wrapper.get('[data-role-id="viewer"]');
    expect((builtin.get('[data-action="edit-role"]').element as HTMLButtonElement).disabled).toBe(true);
    expect((builtin.get('[data-action="delete-role"]').element as HTMLButtonElement).disabled).toBe(true);

    const custom = wrapper.get('[data-role-id="r-custom"]');
    expect((custom.get('[data-action="edit-role"]').element as HTMLButtonElement).disabled).toBe(false);
    expect((custom.get('[data-action="delete-role"]').element as HTMLButtonElement).disabled).toBe(false);
  });

  it("edits and deletes a custom role", async () => {
    const roles = [{ roleId: "r-custom", name: "On-call", permissions: ["process.read"] }];
    const { wrapper, roleClient } = await mountRolesPage(roles, [], ["role.read", "role.manage"]);

    await wrapper.get('[data-role-id="r-custom"] [data-action="edit-role"]').trigger("click");
    const roleDrawer = wrapper.findAllComponents(Drawer)[0];
    expect(roleDrawer.props("open")).toBe(true);
    expect((roleDrawer.get('input[name="role_name"]').element as HTMLInputElement).value).toBe("On-call");
    expect((roleDrawer.get('input[name="perm-process.read"]').element as HTMLInputElement).checked).toBe(true);

    await roleDrawer.get('input[name="role_name"]').setValue("On-call operators");
    await roleDrawer.get("form").trigger("submit");
    await flushPromises();
    expect(roleClient.updateRole).toHaveBeenCalledWith(expect.objectContaining({
      roleId: "r-custom",
      name: "On-call operators",
      permissions: ["process.read"],
    }));

    await wrapper.get('[data-role-id="r-custom"] [data-action="delete-role"]').trigger("click");
    const confirm = wrapper.getComponent(ConfirmDialog);
    expect(confirm.props("open")).toBe(true);
    await confirm.vm.$emit("confirm");
    await flushPromises();
    expect(roleClient.deleteRole).toHaveBeenCalledWith(expect.objectContaining({ roleId: "r-custom" }));
  });

  it("does not expose role mutation buttons without role.manage", async () => {
    const { wrapper } = await mountRolesPage(
      [{ roleId: "r-custom", name: "On-call", permissions: [] }],
      [],
      ["role.read"],
    );

    expect(wrapper.find('[data-action="edit-role"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="delete-role"]').exists()).toBe(false);
  });
});

describe("RolesPage bindings table", () => {
  it("renders localized scope badges instead of raw scope types", async () => {
    const roles = [{ roleId: "operator", name: "Operator", permissions: [] }];
    const bindings = [
      { userId: "u1", roleId: "operator", scopeType: "AGENT_GROUP", scopeId: "group-a" },
      { userId: "u1", roleId: "operator", scopeType: "CLUSTER", scopeId: "" },
    ];
    const { wrapper } = await mountRolesPage(roles, bindings, ["role.read"]);

    const scopeCells = wrapper.findAll("[data-scope]");
    expect(scopeCells.map((cell) => cell.attributes("data-scope"))).toEqual([
      "AGENT_GROUP",
      "CLUSTER",
    ]);
    expect(scopeCells[0].text()).toBe("Agent group");
    expect(scopeCells[1].text()).toBe("Entire cluster");
  });

  it("only allows custom-role bindings to be removed", async () => {
    const roles = [
      { roleId: "viewer", name: "Viewer", permissions: [] },
      { roleId: "r-custom", name: "On-call", permissions: [] },
    ];
    const bindings = [
      { userId: "u1", roleId: "viewer", scopeType: "CLUSTER", scopeId: "" },
      { userId: "u2", roleId: "r-custom", scopeType: "AGENT", scopeId: "node-1" },
    ];
    const { wrapper, roleClient } = await mountRolesPage(roles, bindings, ["role.read", "role.manage"]);

    const rows = wrapper.findAll(".bindings-card tbody tr");
    expect((rows[0].get('[data-action="revoke-role"]').element as HTMLButtonElement).disabled).toBe(true);
    expect((rows[1].get('[data-action="revoke-role"]').element as HTMLButtonElement).disabled).toBe(false);

    await rows[1].get('[data-action="revoke-role"]').trigger("click");
    const confirm = wrapper.getComponent(ConfirmDialog);
    expect(confirm.props("open")).toBe(true);
    await confirm.vm.$emit("confirm");
    await flushPromises();
    expect(roleClient.revokeRole).toHaveBeenCalledWith(expect.objectContaining({
      userId: "u2",
      roleId: "r-custom",
      scopeType: "AGENT",
      scopeId: "node-1",
    }));
  });
});

describe("RolesPage summary stats", () => {
  it("reports the number of roles and bindings", async () => {
    const roles = [{ roleId: "operator", name: "Operator", permissions: [] }];
    const bindings = [
      { userId: "u1", roleId: "operator", scopeType: "CLUSTER", scopeId: "" },
      { userId: "u2", roleId: "operator", scopeType: "CLUSTER", scopeId: "" },
      { userId: "u3", roleId: "operator", scopeType: "CLUSTER", scopeId: "" },
    ];
    const { wrapper } = await mountRolesPage(roles, bindings, ["role.read"]);

    expect(wrapper.get('[data-stat="roles"] .summary-value').text()).toBe("1");
    expect(wrapper.get('[data-stat="bindings"] .summary-value').text()).toBe("3");
  });
});

describe("RolesPage load states", () => {
  it("shows a skeleton while the first load is pending", async () => {
    const { wrapper } = await mountRolesPage([], [], ["role.read"], [], () => new Promise(() => {}));

    const skeleton = wrapper.get('[data-testid="roles-skeleton"]');
    expect(skeleton.attributes("aria-busy")).toBe("true");
    expect(wrapper.find("table").exists()).toBe(false);
  });

  it("offers a retry action when loading fails", async () => {
    let calls = 0;
    const listRoles = () => {
      calls += 1;
      if (calls === 1) {
        return Promise.reject(new Error("boom"));
      }
      return Promise.resolve({ roles: [{ roleId: "r1", name: "Ops", permissions: [] }], bindings: [] });
    };
    const { wrapper, roleClient } = await mountRolesPage([], [], ["role.read"], [], listRoles);

    const errorCard = wrapper.get('[data-state="roles-error"]');
    expect(errorCard.attributes("role")).toBe("alert");
    expect(errorCard.text()).toContain("boom");
    expect(wrapper.find("table").exists()).toBe(false);

    await wrapper.get('[data-action="retry-roles"]').trigger("click");
    await flushPromises();

    expect(roleClient.listRoles).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-state="roles-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-role-id="r1"]').exists()).toBe(true);
  });
});

describe("RolesPage empty states", () => {
  it("renders guidance with a create action when the user can manage roles", async () => {
    const { wrapper } = await mountRolesPage([], [], ["role.read", "role.manage"]);

    const rolesEmpty = wrapper.get('[data-empty="roles"]');
    expect(rolesEmpty.text()).toContain("No roles yet");
    expect(wrapper.get('[data-empty="bindings"]').text()).toContain("No bindings yet");

    await rolesEmpty.get('[data-action="create-role"]').trigger("click");
    expect(wrapper.findAllComponents(Drawer)[0].props("open")).toBe(true);
  });

  it("hides the create action in the empty state without role.manage", async () => {
    const { wrapper } = await mountRolesPage([], [], ["role.read"]);

    expect(wrapper.get('[data-empty="roles"]').text()).toContain("No roles yet");
    expect(wrapper.find('[data-empty="roles"] [data-action="create-role"]').exists()).toBe(false);
  });
});

describe("RolesPage permission picker", () => {
  it("groups permissions by resource", async () => {
    const { wrapper } = await mountRolesPage([], [], ["role.read", "role.manage"]);
    await wrapper.get('[data-action="create-role"]').trigger("click");
    const createDrawer = wrapper.findAllComponents(Drawer)[0];

    expect(createDrawer.props("size")).toBe("wide");
    expect(createDrawer.get('[data-group="process"]').findAll('input[type="checkbox"]')).toHaveLength(11);
    expect(createDrawer.get('[data-group="cluster"]').findAll('input[type="checkbox"]')).toHaveLength(2);
    expect(createDrawer.text()).toContain("Process");
  });

  it("filters permissions and bulk selects only the visible ones", async () => {
    const { wrapper } = await mountRolesPage([], [], ["role.read", "role.manage"]);
    await wrapper.get('[data-action="create-role"]').trigger("click");
    const createDrawer = wrapper.findAllComponents(Drawer)[0];

    expect(createDrawer.get("[data-selected-count]").text()).toContain("0");

    await createDrawer.get('input[name="perm_search"]').setValue("alert");
    expect(createDrawer.findAll('input[type="checkbox"][name^="perm-"]')).toHaveLength(2);
    expect(createDrawer.find("[data-no-perm-match]").exists()).toBe(false);

    await createDrawer.get('[data-action="perm-select-all"]').trigger("click");
    expect(createDrawer.get("[data-selected-count]").text()).toContain("2");
    expect(
      createDrawer
        .findAll('input[type="checkbox"][name^="perm-"]')
        .filter((input) => (input.element as HTMLInputElement).checked),
    ).toHaveLength(2);

    await createDrawer.get('[data-action="perm-clear"]').trigger("click");
    expect(createDrawer.get("[data-selected-count]").text()).toContain("0");
  });

  it("reports when no permission matches the search", async () => {
    const { wrapper } = await mountRolesPage([], [], ["role.read", "role.manage"]);
    await wrapper.get('[data-action="create-role"]').trigger("click");
    const createDrawer = wrapper.findAllComponents(Drawer)[0];

    await createDrawer.get('input[name="perm_search"]').setValue("nope");
    expect(createDrawer.findAll('input[type="checkbox"][name^="perm-"]')).toHaveLength(0);
    expect(createDrawer.get("[data-no-perm-match]").text()).toContain("No matching permissions");
  });

  it("submits every selected permission across groups", async () => {
    const { wrapper, roleClient } = await mountRolesPage([], [], ["role.read", "role.manage"]);
    await wrapper.get('[data-action="create-role"]').trigger("click");
    const createDrawer = wrapper.findAllComponents(Drawer)[0];

    await createDrawer.get('input[name="role_name"]').setValue("Ops");
    await createDrawer.get('[data-action="perm-select-all"]').trigger("click");
    await createDrawer.get("form").trigger("submit");
    await flushPromises();

    expect(roleClient.createRole).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "Ops",
        permissions: expect.arrayContaining(["cluster.read", "alert.manage", "backup.manage"]),
      }),
    );
    expect((roleClient.createRole.mock.calls[0][0] as { permissions: string[] }).permissions).toHaveLength(30);
  });
});

describe("RolesPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      roles: {
        title: "Roles",
        loading: "Loading…",
        table: {
          name: "Name",
          type: "Type",
          permissions: "Permissions",
        },
        bindings: {
          title: "Bindings",
        },
        empty: {
          title: "No roles yet",
        },
      },
    });

    const { wrapper } = await mountRolesPage([
      { roleId: "r1", name: "Ops", permissions: ["process.read"] },
    ]);
    const text = wrapper.text();
    expect(text).toContain("Roles");
    expect(text).toContain("Name");
    expect(text).toContain("Type");
    expect(text).toContain("Permissions");
    expect(text).toContain("Bindings");

    const empty = await mountRolesPage();
    expect(empty.wrapper.text()).toContain("No roles yet");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      roles: {
        title: "角色",
        loading: "加载中…",
        table: {
          name: "名称",
          type: "类型",
          permissions: "权限",
        },
        bindings: {
          title: "绑定",
        },
        empty: {
          title: "暂无角色",
        },
      },
    });

    const { wrapper } = await mountRolesPage([
      { roleId: "r1", name: "运维", permissions: ["process.read"] },
    ]);
    const text = wrapper.text();
    expect(text).toContain("角色");
    expect(text).toContain("名称");
    expect(text).toContain("类型");
    expect(text).toContain("权限");
    expect(text).toContain("绑定");

    const empty = await mountRolesPage();
    expect(empty.wrapper.text()).toContain("暂无角色");
  });
});
