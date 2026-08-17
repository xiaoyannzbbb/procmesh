import { describe, expect, it } from "vitest";
import {
  formatChartTime,
  formatChartValue,
  nearestPoint,
  niceYScale,
  splitSegments,
  stepSecForLayer,
} from "./historyChart";

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

describe("niceYScale", () => {
  it("keeps a readable percent range when every sample is 0", () => {
    const scale = niceYScale([0, 0, 0], "percent");
    expect(scale.min).toBe(0);
    expect(scale.max).toBeGreaterThanOrEqual(5);
    expect(scale.max).toBeLessThanOrEqual(10);
    expect(scale.ticks[0]).toBe(0);
    expect(scale.ticks.at(-1)).toBe(scale.max);
  });

  it("zooms a ~6% series instead of pinning 0–100", () => {
    const scale = niceYScale([6.3, 6.2, 6.4], "percent");
    expect(scale.min).toBe(0);
    expect(scale.max).toBeGreaterThan(6.4);
    expect(scale.max).toBeLessThanOrEqual(15);
    expect(6.3 / scale.max).toBeGreaterThan(0.4);
  });

  it("caps a high percent series at 100", () => {
    expect(niceYScale([0, 92], "percent").max).toBe(100);
  });

  it("uses a byte ceiling just above the series", () => {
    const gib = 1024 ** 3;
    const scale = niceYScale([gib * 0.9], "bytes");
    expect(scale.min).toBe(0);
    expect(scale.max).toBeGreaterThanOrEqual(gib * 0.9);
    expect(scale.max).toBeLessThanOrEqual(gib * 2);
  });
});

describe("formatChartValue", () => {
  it("formats idle percent with two decimals", () => {
    expect(formatChartValue(0, "percent")).toBe("0.00%");
  });

  it("formats small percents with one decimal", () => {
    expect(formatChartValue(6.3, "percent")).toBe("6.3%");
  });

  it("formats whole percents without a trailing decimal", () => {
    expect(formatChartValue(40, "percent")).toBe("40%");
  });

  it("formats bytes with binary units", () => {
    expect(formatChartValue(1024 ** 3, "bytes")).toBe("1.0 GiB");
    expect(formatChartValue(1536 * 1024 * 1024, "bytes")).toBe("1.5 GiB");
    expect(formatChartValue(512, "bytes")).toBe("512 B");
  });
});

describe("formatChartTime", () => {
  it("uses 24h clock for a same-day window", () => {
    expect(formatChartTime(1_700_000_000, 3600, "UTC")).toBe("22:13");
  });

  it("uses a short date for a multi-day window", () => {
    expect(formatChartTime(1_700_000_000, 7 * 86400, "UTC")).toBe("14 Nov");
  });
});

describe("nearestPoint", () => {
  const points = [
    { t: 100, v: 1 },
    { t: 200, v: 2 },
    { t: 300, v: 3 },
  ];

  it("picks the sample closest to the pointer x", () => {
    expect(nearestPoint(points, 100, 300, 0, 200)?.t).toBe(100);
    expect(nearestPoint(points, 100, 300, 100, 200)?.t).toBe(200);
    expect(nearestPoint(points, 100, 300, 200, 200)?.t).toBe(300);
  });

  it("returns null when there are no points", () => {
    expect(nearestPoint([], 0, 1, 10, 100)).toBeNull();
  });
});
