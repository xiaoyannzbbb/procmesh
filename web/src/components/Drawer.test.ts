import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import Drawer from "./Drawer.vue";
import drawerSource from "./Drawer.vue?raw";

afterEach(() => {
  document.body.innerHTML = "";
  document.body.style.overflow = "";
  document.body.removeAttribute("tabindex");
});

describe("Drawer focus management", () => {
  it("supports a wide panel for dense editors", () => {
    const wrapper = mount(Drawer, {
      attachTo: document.body,
      props: { open: true, title: "Edit configuration", size: "wide" },
    });

    expect(document.querySelector(".drawer-panel")?.classList.contains("drawer-panel-wide")).toBe(true);

    wrapper.unmount();
  });

  it("moves focus inside when opened and restores it when closed", async () => {
    const trigger = document.createElement("button");
    document.body.appendChild(trigger);
    trigger.focus();

    const wrapper = mount(Drawer, {
      attachTo: document.body,
      props: { open: false, title: "Example" },
      slots: { default: '<button data-first type="button">First</button><button type="button">Last</button>' },
    });

    await wrapper.setProps({ open: true });
    await flushPromises();
    expect(document.querySelector('[role="dialog"]')?.contains(document.activeElement)).toBe(true);

    await wrapper.setProps({ open: false });
    await flushPromises();
    expect(document.activeElement).toBe(trigger);

    wrapper.unmount();
  });

  it("renders above the mobile application sidebar", () => {
    const wrapper = mount(Drawer, {
      attachTo: document.body,
      props: { open: true, title: "Example" },
    });

    const backdrop = document.querySelector<HTMLElement>(".drawer-backdrop");
    expect(Number(getComputedStyle(backdrop!).zIndex)).toBeGreaterThan(1000);

    wrapper.unmount();
  });

  it("keeps the narrow drawer close target at least 44 pixels", () => {
    expect(drawerSource).toMatch(
      /@media \(max-width:\s*640px\)[\s\S]*\.drawer-close\s*\{(?=[^}]*min-width:\s*44px)(?=[^}]*min-height:\s*44px)[^}]*\}/s,
    );
  });

  it("recaptures focus when the focused control becomes disabled", async () => {
    const wrapper = mount(Drawer, {
      attachTo: document.body,
      props: { open: true, title: "Example" },
      slots: { default: '<button data-last type="button">Submit</button>' },
    });
    await flushPromises();

    const closeButton = document.querySelector<HTMLElement>(".drawer-close")!;
    const submitButton = document.querySelector<HTMLButtonElement>("[data-last]")!;
    submitButton.focus();
    submitButton.disabled = true;
    document.body.tabIndex = -1;
    document.body.focus();
    expect(document.activeElement).toBe(document.body);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));

    expect(document.activeElement).toBe(closeButton);
    wrapper.unmount();
  });
});
