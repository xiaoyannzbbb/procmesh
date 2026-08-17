import { describe, expect, it } from "vitest";
import { splitSegments, stepSecForLayer } from "./historyChart";

describe("splitSegments", () => {
  it("breaks a 60s series when a minute is missing", () => {
    const segs = splitSegments(
      [
        { t: 0, v: 10 },
        { t: 60, v: 11 },
        { t: 180, v: 12 },
      ],
      60,
    );
    expect(segs).toHaveLength(2);
    expect(segs.flat().some((p) => p.t === 120 || p.v === 0)).toBe(false);
  });
});

describe("stepSecForLayer", () => {
  it("uses 300s for down_5m and 60s otherwise", () => {
    expect(stepSecForLayer("down_5m")).toBe(300);
    expect(stepSecForLayer("raw_min")).toBe(60);
    expect(stepSecForLayer("")).toBe(60);
  });
});
