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
          actions: {
            cancel: "Cancel",
          },
          group: {
            title: "Groups",
            loading: "Loading…",
            noGroups: "No groups",
            create: "Create",
            createGroup: "Create Group",
            members: "Members",
            name: "Name",
            description: "Description",
            addMember: "Add member",
            removeMember: "Remove member",
            delete: "Delete",
            deleteConfirmTitle: "Delete group?",
            deleteConfirmMessage: 'Delete group "{{name}}"? This action cannot be undone.',
            confirmDelete: "Delete group",
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

  it("does not delete a group when the confirmation dialog is cancelled", async () => {
    const { wrapper, groupClient } = await mountGroups(["node.read", "node.manage"]);

    const deleteButton = wrapper.findAll("button").find((button) => button.text() === "Delete");
    await deleteButton?.trigger("click");
    await flushPromises();

    const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
    expect(dialog?.textContent).toContain('Delete group "finance"? This action cannot be undone.');
    expect(groupClient.deleteAgentGroup).not.toHaveBeenCalled();

    const cancelButton = Array.from(dialog?.querySelectorAll("button") ?? []).find(
      (button) => button.textContent?.trim() === "Cancel",
    );
    cancelButton?.click();
    await flushPromises();

    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(groupClient.deleteAgentGroup).not.toHaveBeenCalled();
  });

  it("deletes a group only after the confirmation dialog is confirmed", async () => {
    const { wrapper, groupClient } = await mountGroups(["node.read", "node.manage"]);

    const deleteButton = wrapper.findAll("button").find((button) => button.text() === "Delete");
    await deleteButton?.trigger("click");
    await flushPromises();

    const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(groupClient.deleteAgentGroup).not.toHaveBeenCalled();
    const confirmButton = Array.from(dialog?.querySelectorAll("button") ?? []).find(
      (button) => button.textContent?.trim() === "Delete group",
    );
    expect(confirmButton).toBeDefined();
    confirmButton?.click();
    await flushPromises();

    expect(groupClient.deleteAgentGroup).toHaveBeenCalledTimes(1);
    expect(groupClient.deleteAgentGroup).toHaveBeenCalledWith(
      expect.objectContaining({ groupId: "g1" }),
    );
  });
});
