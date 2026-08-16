import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
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

async function mountUsers(permissions: string[] = ["user.read", "user.create", "user.update"]) {
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
    listUsers: vi.fn().mockResolvedValue({ users: [] }),
    createUser: vi.fn(),
    disableUser: vi.fn(),
  };
  const wrapper = mount(UsersPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { userClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, userClient };
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
    await wrapper.get('input[name="username"]').setValue("alice");
    await wrapper.get('input[name="password"]').setValue("short");
    const submit = wrapper.get('form.create-user button[type="submit"]');
    expect(submit.attributes("disabled")).toBeDefined();
    await wrapper.get("form.create-user").trigger("submit");
    await flushPromises();
    expect(userClient.createUser).not.toHaveBeenCalled();
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
