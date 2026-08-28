import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import ProfilePage from "./ProfilePage.vue";

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          actions: { save: "Save password", showPassword: "Show password", hidePassword: "Hide password" },
          profile: {
            title: "Personal profile",
            subtitle: "Manage your account and sign-in security.",
            identity: "Account",
            username: "Username",
            userId: "User ID",
            security: "Security",
            passwordTitle: "Change password",
            passwordDescription: "Use at least 10 characters.",
            currentPassword: "Current password",
            newPassword: "New password",
            confirmPassword: "Confirm new password",
            passwordHint: "At least 10 characters",
            passwordMismatch: "Passwords do not match.",
            passwordTooShort: "Password must be at least 10 characters.",
            currentPasswordRequired: "Enter your current password.",
            passwordChanged: "Password updated.",
          },
        },
      },
    },
  });
  session.value = {
    userId: "user-admin",
    username: "admin",
    csrfToken: "csrf",
    permissions: [],
  };
});

afterEach(() => {
  session.value = null;
});

function mountProfile(changePassword = vi.fn().mockResolvedValue({})) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = mount(ProfilePage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { authClient: { changePassword } },
      stubs: { Teleport: true },
    },
  });
  return { wrapper, changePassword };
}

describe("ProfilePage", () => {
  it("shows the signed-in identity", () => {
    const { wrapper } = mountProfile();
    expect(wrapper.text()).toContain("admin");
    expect(wrapper.text()).toContain("user-admin");
  });

  it("validates password confirmation before calling the API", async () => {
    const { wrapper, changePassword } = mountProfile();
    await wrapper.get('input[name="current_password"]').setValue("admin-pass-ok");
    await wrapper.get('input[name="new_password"]').setValue("new-admin-pass");
    await wrapper.get('input[name="confirm_password"]').setValue("different-pass");
    await wrapper.get("form").trigger("submit");

    expect(changePassword).not.toHaveBeenCalled();
    expect(wrapper.get("#confirm-password-error").text()).toBe("Passwords do not match.");
  });

  it("toggles password visibility with an accessible button", async () => {
    const { wrapper } = mountProfile();
    const input = wrapper.get('input[name="current_password"]');
    const toggle = wrapper.get('[data-toggle-password="current"]');

    expect(input.attributes("type")).toBe("password");
    expect(toggle.attributes("aria-label")).toBe("Show password");
    await toggle.trigger("click");
    expect(input.attributes("type")).toBe("text");
    expect(toggle.attributes("aria-label")).toBe("Hide password");
  });

  it("changes the password and clears the form", async () => {
    const { wrapper, changePassword } = mountProfile();
    await wrapper.get('input[name="current_password"]').setValue("admin-pass-ok");
    await wrapper.get('input[name="new_password"]').setValue("new-admin-pass");
    await wrapper.get('input[name="confirm_password"]').setValue("new-admin-pass");
    await wrapper.get("form").trigger("submit");
    await flushPromises();

    expect(changePassword).toHaveBeenCalledWith({
      meta: expect.objectContaining({ operationId: expect.any(String), operator: "admin" }),
      currentPassword: "admin-pass-ok",
      newPassword: "new-admin-pass",
    });
    expect(wrapper.get<HTMLInputElement>('input[name="current_password"]').element.value).toBe("");
    expect(wrapper.text()).toContain("Password updated.");
  });
});
