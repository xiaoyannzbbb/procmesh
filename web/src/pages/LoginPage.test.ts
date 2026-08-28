import { mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import LoginPage from "./LoginPage.vue";
import { I18NextVue } from "../lib/i18n";
import i18next from 'i18next';

const Blank = defineComponent({ setup: () => () => h("div") });

// Create a test-specific i18n instance without HTTP backend
const testI18n = i18next.createInstance();
testI18n.init({
  lng: 'en',
  fallbackLng: 'en',
  supportedLngs: ['en', 'zh'],
  resources: {
    en: {
      common: {
        login: {
          title: 'ProcMesh',
          username: 'Username',
          password: 'Password',
          invalidCredentials: 'invalid credentials',
          rateLimited: 'login rate limited',
          userLocked: 'user locked'
        },
        actions: {
          signIn: 'Sign in'
        }
      }
    },
    zh: {
      common: {
        login: {
          title: 'ProcMesh',
          username: '用户名',
          password: '密码',
          invalidCredentials: '用户名或密码错误',
          rateLimited: '登录请求过于频繁',
          userLocked: '用户已锁定'
        },
        actions: {
          signIn: '登录'
        }
      }
    }
  },
  interpolation: {
    escapeValue: false
  }
});

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
      plugins: [router, [I18NextVue, { i18next: testI18n }]],
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

  it("renders the brand mark above the login form", async () => {
    const wrapper = await mountLogin();
    expect(wrapper.find(".login-brand .brand-mark").exists()).toBe(true);
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

  it("renders login form in English", async () => {
    await testI18n.changeLanguage('en');
    const wrapper = await mountLogin();
    expect(wrapper.text()).toContain('Username');
    expect(wrapper.text()).toContain('Password');
    expect(wrapper.text()).toContain('Sign in');
  });

  it("renders login form in Chinese", async () => {
    await testI18n.changeLanguage('zh');
    const wrapper = await mountLogin();
    expect(wrapper.text()).toContain('用户名');
    expect(wrapper.text()).toContain('密码');
    expect(wrapper.text()).toContain('登录');
  });
});
