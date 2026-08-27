import { describe, expect, it } from "vitest";
import { LIVE, STALE, UNKNOWN } from "./freshness";
import {
  processDeletable,
  remoteCreateBlocked,
  remoteDeleteBlocked,
  remoteUpdateBlocked,
} from "./remoteProcess";

const liveAllow = {
  freshness: LIVE,
  disableRemoteCreate: false,
  disableRemoteUpdate: false,
  disableRemoteDelete: false,
} as const;

describe("remote process flags", () => {
  it("allows LIVE nodes that have not disabled remote writes", () => {
    expect(remoteCreateBlocked(liveAllow)).toBe(false);
    expect(remoteUpdateBlocked(liveAllow)).toBe(false);
    expect(remoteDeleteBlocked(liveAllow)).toBe(false);
  });

  it("blocks STALE, UNKNOWN, missing, and explicit disable", () => {
    expect(remoteCreateBlocked(undefined)).toBe(true);
    expect(remoteCreateBlocked({ ...liveAllow, freshness: STALE })).toBe(true);
    expect(remoteUpdateBlocked({ ...liveAllow, freshness: UNKNOWN })).toBe(true);
    expect(remoteDeleteBlocked({ ...liveAllow, disableRemoteDelete: true })).toBe(true);
    expect(remoteCreateBlocked({ ...liveAllow, disableRemoteCreate: true })).toBe(true);
  });

  it("only allows delete when desired is STOPPED and observed is terminal", () => {
    expect(processDeletable("STOPPED", "STOPPED")).toBe(true);
    expect(processDeletable("FATAL", "STOPPED")).toBe(true);
    expect(processDeletable("RUNNING", "STOPPED")).toBe(false);
    expect(processDeletable("STOPPED", "RUNNING")).toBe(false);
  });
});
