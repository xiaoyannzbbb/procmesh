import { describe, expect, it } from "vitest";
import { classify, formatAge, LIVE, STALE, UNKNOWN } from "./freshness";

const nowMs = 1_700_000_010_000;

describe("classify", () => {
  it("LIVE when ALIVE and recent", () => {
    expect(classify(nowMs, 1_700_000_005_000, "ALIVE")).toBe(LIVE);
  });

  it("LIVE when ALIVE and age == 10s", () => {
    expect(classify(nowMs, 1_700_000_000_000, "ALIVE")).toBe(LIVE);
  });

  it("STALE when ALIVE and old", () => {
    expect(classify(nowMs, 1_699_999_999_000, "ALIVE")).toBe(STALE);
  });

  it("STALE when FAILED even if recent", () => {
    expect(classify(nowMs, 1_700_000_009_000, "FAILED")).toBe(STALE);
  });

  it("UNKNOWN when no timestamp", () => {
    expect(classify(nowMs, 0, "ALIVE")).toBe(UNKNOWN);
  });

  it("UNKNOWN when REVOKED", () => {
    expect(classify(nowMs, 1_700_000_009_000, "REVOKED")).toBe(UNKNOWN);
  });

  it("STALE when SUSPECT even if recent", () => {
    expect(classify(nowMs, 1_700_000_009_000, "SUSPECT")).toBe(STALE);
  });

  it("STALE when LEFT even if recent", () => {
    expect(classify(nowMs, 1_700_000_009_000, "LEFT")).toBe(STALE);
  });

  it("STALE when JOINING with timestamp", () => {
    expect(classify(nowMs, 1_700_000_009_000, "JOINING")).toBe(STALE);
  });
});

describe("formatAge", () => {
  it("formats 10s age", () => {
    const got = formatAge(1_700_000_010_000, 1_700_000_000_000);
    expect(got.includes("10s ago") || got.includes("10s")).toBe(true);
  });
});
