import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

async function mountRolesPage(roles: any[] = [], bindings: any[] = []) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const roleClient = {
    listRoles: vi.fn().mockResolvedValue({ roles, bindings }),
    createRole: vi.fn().mockResolvedValue({}),
    grantRole: vi.fn().mockResolvedValue({}),
  };
  const wrapper = mount(RolesPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { roleClient },
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

    const wrapper = await mountRolesPage();
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

    const wrapper = await mountRolesPage();
    const text = wrapper.text();
    expect(text).toContain("角色");
    expect(text).toContain("名称");
    expect(text).toContain("类型");
    expect(text).toContain("权限");
    expect(text).toContain("绑定");
    expect(text).toContain("无角色");
  });
});
