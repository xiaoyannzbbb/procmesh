import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import GroupsPage from "./GroupsPage.vue";

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          group: {
            title: "Groups",
            loading: "Loading…",
            noGroups: "No groups",
            create: "Create",
            members: "Members",
            name: "Name",
            description: "Description",
            addMember: "Add member",
            removeMember: "Remove member",
            delete: "Delete",
            nodeId: "Node ID",
          },
        },
      },
    },
  });
});

const mounted: Array<{ unmount: () => void }> = [];

async function mountGroups(
  permissions: string[] = ["node.read"],
  groups: unknown[] = [{ groupId: "g1", name: "finance", memberNodeIds: ["n1"] }],
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
  const groupClient = {
    listAgentGroups: vi.fn().mockResolvedValue({ groups }),
    createAgentGroup: vi.fn().mockResolvedValue({}),
    deleteAgentGroup: vi.fn().mockResolvedValue({}),
    addAgentGroupMember: vi.fn().mockResolvedValue({}),
    removeAgentGroupMember: vi.fn().mockResolvedValue({}),
  };
  const wrapper = mount(GroupsPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { groupClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, groupClient };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("GroupsPage", () => {
  it("shows group name from listAgentGroups", async () => {
    const { wrapper } = await mountGroups();
    expect(wrapper.text()).toContain("finance");
  });

  it("shows Create when node.manage", async () => {
    const { wrapper } = await mountGroups(["node.read", "node.manage"]);
    expect(wrapper.text()).toContain("Create");
  });

  it("hides Create without node.manage", async () => {
    const { wrapper } = await mountGroups(["node.read"]);
    expect(wrapper.text()).not.toContain("Create");
  });
});
