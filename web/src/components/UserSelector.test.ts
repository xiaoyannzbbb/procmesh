import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { describe, expect, it, vi } from "vitest";
import UserSelector from "./UserSelector.vue";

describe("UserSelector", () => {
  it("disables clearing the selected user while the form is busy", async () => {
    const i18n = i18next.createInstance();
    await i18n.init({ lng: "en", resources: { en: { common: {} } } });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const userClient = {
      listUsers: vi.fn().mockResolvedValue({
        users: [{ userId: "u-alice", username: "alice", displayName: "Alice", email: "", status: "ACTIVE" }],
      }),
    };
    const wrapper = mount(UserSelector, {
      props: { modelValue: "u-alice", disabled: true },
      global: {
        plugins: [
          [VueQueryPlugin, { queryClient }],
          [I18NextVue, { i18next: i18n }],
        ],
        provide: { userClient },
      },
    });
    await flushPromises();

    expect(wrapper.get(".clear-selection").attributes("disabled")).toBeDefined();
    wrapper.unmount();
  });
});
