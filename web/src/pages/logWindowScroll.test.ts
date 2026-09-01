import { describe, expect, it } from "vitest";
import { isPinnedToBottom, pinToBottom } from "./logWindowScroll";

describe("log window stick-to-bottom", () => {
  it("treats a window at the bottom as pinned", () => {
    expect(
      isPinnedToBottom({ scrollTop: 300, scrollHeight: 400, clientHeight: 100 }),
    ).toBe(true);
  });

  it("treats a window within the pin threshold as pinned", () => {
    expect(
      isPinnedToBottom({ scrollTop: 270, scrollHeight: 400, clientHeight: 100 }),
    ).toBe(true);
  });

  it("does not treat a window scrolled away from the bottom as pinned", () => {
    expect(
      isPinnedToBottom({ scrollTop: 40, scrollHeight: 400, clientHeight: 100 }),
    ).toBe(false);
  });

  it("pins by moving scrollTop to the content end", () => {
    const el = { scrollTop: 40, scrollHeight: 400 };
    pinToBottom(el);
    expect(el.scrollTop).toBe(400);
  });
});
