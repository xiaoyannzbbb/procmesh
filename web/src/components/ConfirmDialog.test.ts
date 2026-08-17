import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import ConfirmDialog from "./ConfirmDialog.vue";

afterEach(() => {
  document.body.innerHTML = "";
  document.body.style.overflow = "";
});

const labels = {
  title: "Delete group?",
  message: "This action cannot be undone.",
  confirmLabel: "Delete group",
  cancelLabel: "Cancel",
};

describe("ConfirmDialog", () => {
  it("focuses the safe action when initially open", async () => {
    const wrapper = mount(ConfirmDialog, {
      attachTo: document.body,
      props: { open: true, ...labels },
    });
    await flushPromises();

    const cancelButton = Array.from(document.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "Cancel",
    );
    expect(document.activeElement).toBe(cancelButton);

    wrapper.unmount();
  });

  it("emits cancel when Escape is pressed", async () => {
    const wrapper = mount(ConfirmDialog, {
      attachTo: document.body,
      props: { open: true, ...labels },
    });
    await flushPromises();

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));

    expect(wrapper.emitted("cancel")).toHaveLength(1);
    expect(wrapper.emitted("confirm")).toBeUndefined();
    wrapper.unmount();
  });
});
