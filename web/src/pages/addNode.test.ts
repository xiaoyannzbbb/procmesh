import { describe, expect, it } from "vitest";
import { buildCustomServerJoinTemplate, buildJoinCommand, parseJoinTokenParameters } from "./addNode";

describe("add node input normalization", () => {
  it("converts a duration and uses to protobuf-safe values", () => {
    expect(parseJoinTokenParameters("1", "hours", "2")).toEqual({
      ttlSeconds: 3600n,
      uses: 2,
    });
  });

  it("rejects invalid and overflowing values", () => {
    expect(parseJoinTokenParameters("0", "seconds", "1")).toBeNull();
    expect(parseJoinTokenParameters("1.5", "hours", "1")).toBeNull();
    expect(parseJoinTokenParameters("1", "seconds", "2147483648")).toBeNull();
    expect(parseJoinTokenParameters("9223372036", "seconds", "1")).toEqual({
      ttlSeconds: 9_223_372_036n,
      uses: 1,
    });
    expect(parseJoinTokenParameters("9223372037", "seconds", "1")).toBeNull();
    expect(parseJoinTokenParameters("2562047", "hours", "1")).toEqual({
      ttlSeconds: 9_223_369_200n,
      uses: 1,
    });
    expect(parseJoinTokenParameters("2562048", "hours", "1")).toBeNull();
    expect(parseJoinTokenParameters("106751", "days", "1")).toEqual({
      ttlSeconds: 9_223_286_400n,
      uses: 1,
    });
    expect(parseJoinTokenParameters("106752", "days", "1")).toBeNull();
  });
});

describe("join command generation", () => {
  it("keeps ordinary seed addresses readable and always quotes the token", () => {
    expect(buildJoinCommand("10.0.0.11:18680", "pmj_example")).toBe(
      "procmesh agent join --seed 10.0.0.11:18680 --token 'pmj_example'",
    );
  });

  it("preserves shell argument boundaries for unusual server values", () => {
    expect(buildJoinCommand("seed host:18680", "value'with-quote")).toBe(
      `procmesh agent join --seed 'seed host:18680' --token 'value'"'"'with-quote'`,
    );
    expect(buildJoinCommand("--unexpected", "-token")).toBe(
      `procmesh agent join --seed='--unexpected' --token='-token'`,
    );
    expect(buildCustomServerJoinTemplate("seed host:18680")).toBe(
      `procmesh --server <NEW_AGENT_API> agent join --seed 'seed host:18680' --token '<JOIN_TOKEN>'`,
    );
  });
});
