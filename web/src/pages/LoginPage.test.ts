import { mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import LoginPage from "./LoginPage.vue";

const Blank = defineComponent({ setup: () => () => h("div") });

async function mountLogin(provide: Record<string, unknown> = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/login", component: LoginPage },
      { path: "/", component: Blank },
    ],
  });
  await router.push("/login");
  await router.isReady();
  return mount(LoginPage, {
    global: {
      plugins: [router],
      provide,
    },
  });
}

describe("LoginPage", () => {
  it("renders Username, Password, and a submit button", async () => {
    const wrapper = await mountLogin();
    expect(wrapper.text()).toContain("Username");
    expect(wrapper.text()).toContain("Password");
    expect(wrapper.get('button[type="submit"]').exists()).toBe(true);
  });

  it("does not call transport on empty submit", async () => {
    const login = vi.fn();
    const wrapper = await mountLogin({ authClient: { login } });
    await wrapper.get("form").trigger("submit");
    expect(login).not.toHaveBeenCalled();
  });

  it("shows Connect error phrases on failed login", async () => {
    const login = vi.fn().mockRejectedValue(new Error("[permission_denied] DENIED: invalid credentials"));
    const wrapper = await mountLogin({ authClient: { login } });
    await wrapper.get('input[name="username"]').setValue("admin");
    await wrapper.get('input[name="password"]').setValue("wrong");
    await wrapper.get("form").trigger("submit");
    await wrapper.vm.$nextTick();
    expect(login).toHaveBeenCalled();
    expect(wrapper.text()).toContain("invalid credentials");
  });
});
