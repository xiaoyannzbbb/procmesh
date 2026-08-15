import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import FreshnessBadge from "./FreshnessBadge.vue";

const nowMs = 1_700_000_010_000;

function hexOrRgb(value: string): boolean {
  const v = value.replace(/\s/g, "").toLowerCase();
  return v === "#fef3c7" || v === "rgb(254,243,199)" || v === "rgba(254,243,199,1)";
}

describe("FreshnessBadge", () => {
  it("renders STALE without green", () => {
    const wrapper = mount(FreshnessBadge, {
      props: {
        nowMs,
        lastUpdatedUnixMs: 1_700_000_009_000,
        nodeState: "FAILED",
      },
    });
    expect(wrapper.text()).toContain("STALE");
    const html = wrapper.html().toLowerCase();
    expect(html).not.toMatch(/green|#d1fae5|#10a37f|bg-green/);
    const el = wrapper.get(".freshness-stale").element as HTMLElement;
    const computed = getComputedStyle(el).backgroundColor;
    const inline = el.style.backgroundColor;
    const bg = hexOrRgb(computed) ? computed : inline;
    expect(hexOrRgb(bg), `background=${computed}/${inline}`).toBe(true);
  });
});
