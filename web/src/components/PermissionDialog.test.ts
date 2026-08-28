import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import PermissionDialog from "./PermissionDialog.vue";

const baseProps = {
  open: false,
  title: "Permissions for Ops",
  summary: "Permissions: 3",
  closeLabel: "Close",
  emptyLabel: "No permissions assigned",
  groups: [
    { key: "cluster", label: "Cluster", permissions: ["cluster.read"] },
    { key: "process", label: "Process", permissions: ["process.read", "process.restart"] },
  ],
};

afterEach(() => {
  document.body.innerHTML = "";
  document.body.style.overflow = "";
});

describe("PermissionDialog", () => {
  it("renders grouped permissions and closes with Escape", async () => {
    const wrapper = mount(PermissionDialog, {
      attachTo: document.body,
      props: { ...baseProps, open: true },
    });

    expect(document.querySelector('[data-permission-group="cluster"]')?.textContent).toContain("cluster.read");
    expect(document.querySelectorAll('[data-permission-group="process"] [data-permission]')).toHaveLength(2);

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(wrapper.emitted("close")).toHaveLength(1);
    wrapper.unmount();
  });

  it("moves focus inside on open and restores the trigger on close", async () => {
    const trigger = document.createElement("button");
    document.body.appendChild(trigger);
    trigger.focus();
    const wrapper = mount(PermissionDialog, {
      attachTo: document.body,
      props: baseProps,
    });

    await wrapper.setProps({ open: true });
    await flushPromises();
    expect(document.querySelector('[role="dialog"]')?.contains(document.activeElement)).toBe(true);

    await wrapper.setProps({ open: false });
    await flushPromises();
    expect(document.activeElement).toBe(trigger);
    wrapper.unmount();
  });
});
