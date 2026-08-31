import { describe, expect, it } from "vitest";
import { anyLiveLinuxBehind, isBehind } from "./agentVersion";

describe("isBehind", () => {
  it("treats semver current behind latest (with/without v)", () => {
    expect(isBehind("0.1.0", "v0.2.0")).toBe(true);
    expect(isBehind("v0.1.0", "0.1.0")).toBe(false);
    expect(isBehind("0.2.0", "0.1.0")).toBe(false);
    expect(isBehind("v0.2.0", "v0.2.0")).toBe(false);
  });

  it("uses semver order for prerelease when both sides are valid", () => {
    // 0.2.0-dev is valid semver (prerelease). Patch base 0.2.0 > 0.1.0 → not behind.
    expect(isBehind("0.2.0-dev", "0.1.0")).toBe(false);
    expect(isBehind("0.2.0-dev", "0.2.0")).toBe(true);
  });

  it("treats unequal non-semver strings as behind", () => {
    expect(isBehind("garbage", "0.1.0")).toBe(true);
    expect(isBehind("garbage", "garbage")).toBe(false);
    expect(isBehind("0.1.0", "not-a-version")).toBe(true);
  });

  it("returns false when both sides empty", () => {
    expect(isBehind("", "")).toBe(false);
    expect(isBehind("  ", "\t")).toBe(false);
  });
});

describe("anyLiveLinuxBehind", () => {
  const nowMs = 1_700_000_010_000;
  const live = nowMs;
  const stale = nowMs - 60_000;
  const members = [
    { state: "ALIVE", os: "linux", agentVersion: "0.1.0", lastUpdatedUnixMs: live },
    { state: "ALIVE", os: "darwin", agentVersion: "0.1.0", lastUpdatedUnixMs: live },
    { state: "FAILED", os: "linux", agentVersion: "0.1.0", lastUpdatedUnixMs: live },
    { state: "ALIVE", os: "", agentVersion: "0.1.0", lastUpdatedUnixMs: live },
    { state: "ALIVE", os: "linux", agentVersion: "0.2.0", lastUpdatedUnixMs: live },
    { state: "ALIVE", os: "linux", agentVersion: "0.1.0", lastUpdatedUnixMs: stale },
  ];

  it("returns true when a LIVE linux node is behind latest", () => {
    expect(anyLiveLinuxBehind(members, "v0.2.0", nowMs)).toBe(true);
  });

  it("returns false when nobody is behind", () => {
    expect(anyLiveLinuxBehind(members, "v0.1.0", nowMs)).toBe(false);
  });

  it("ignores darwin and empty os even when LIVE and behind", () => {
    const onlyMac = [
      { state: "ALIVE", os: "darwin", agentVersion: "0.1.0", lastUpdatedUnixMs: live },
      { state: "ALIVE", os: "", agentVersion: "0.1.0", lastUpdatedUnixMs: live },
    ];
    expect(anyLiveLinuxBehind(onlyMac, "v0.9.0", nowMs)).toBe(false);
  });

  it("ignores STALE ALIVE linux nodes that are behind", () => {
    const onlyStale = [
      { state: "ALIVE", os: "linux", agentVersion: "0.1.0", lastUpdatedUnixMs: stale },
    ];
    expect(anyLiveLinuxBehind(onlyStale, "v0.9.0", nowMs)).toBe(false);
  });

  it("ignores UNKNOWN linux nodes that are behind", () => {
    const unknown = [{ state: "ALIVE", os: "linux", agentVersion: "0.1.0" }];
    expect(anyLiveLinuxBehind(unknown, "v0.9.0", nowMs)).toBe(false);
  });
});
