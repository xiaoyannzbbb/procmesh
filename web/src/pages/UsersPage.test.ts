import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import UsersPage from "./UsersPage.vue";

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
      plugins: [[VueQueryPlugin, { queryClient }]],
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
