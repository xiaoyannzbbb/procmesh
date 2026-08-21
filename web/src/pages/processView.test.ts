import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { ErrorInfoSchema } from "../gen/procmesh/v1/api_pb";
import { STALE } from "../lib/freshness";
import {
  flattenClusterProcesses,
  formatMetric,
  formatRemoteError,
  mapProcessDetail,
  needsRestartBanner,
  RESTART_REQUIRED_BANNER,
  ownerDisplay,
  rowsFromProcessViews,
} from "./processView";

const nowMs = 1_700_000_010_000;

const nodes = [
  {
    nodeId: "n-a",
    hostname: "agent-a",
    state: "ALIVE",
    lastUpdatedUnixMs: nowMs - 1_000,
    processes: [
      {
        name: "api",
        desired: "RUNNING",
        observed: "RUNNING",
        health: "HEALTHY",
        latestRevision: 3,
        activeRevision: 3,
        freshnessUnixMs: nowMs - 1_000,
      },
    ],
  },
  {
    nodeId: "n-b",
    hostname: "agent-b",
    state: "FAILED",
    lastUpdatedUnixMs: nowMs - 60_000,
    processes: [
      {
        name: "api",
        desired: "RUNNING",
        observed: "RUNNING",
        health: "HEALTHY",
        latestRevision: 2,
        activeRevision: 2,
        freshnessUnixMs: nowMs - 1_000,
      },
      {
        name: "worker",
        desired: "RUNNING",
        observed: "EXITED",
        health: "UNHEALTHY",
        latestRevision: 4,
        activeRevision: 3,
        freshnessUnixMs: nowMs - 2_000,
      },
    ],
  },
];

describe("flattenClusterProcesses", () => {
  it("flattens processes from nodes[] gossip summaries", () => {
    const rows = flattenClusterProcesses(nodes, nowMs);
    expect(rows).toHaveLength(3);
    expect(rows.map((r) => r.name).sort()).toEqual(["api", "api", "worker"]);
  });

  it("keeps same-name processes on different nodes as two rows", () => {
    const rows = flattenClusterProcesses(nodes, nowMs);
    const apis = rows.filter((r) => r.name === "api");
    expect(apis).toHaveLength(2);
    expect(apis.map((r) => r.ownerNodeId).sort()).toEqual(["n-a", "n-b"]);
    expect(apis.map((r) => r.ownerHostname).sort()).toEqual(["agent-a", "agent-b"]);
  });

  it("marks FAILED owner observed=RUNNING as STALE", () => {
    const rows = flattenClusterProcesses(nodes, nowMs);
    const failedApi = rows.find((r) => r.name === "api" && r.ownerNodeId === "n-b");
    expect(failedApi).toBeTruthy();
    expect(failedApi?.observed).toBe("RUNNING");
    expect(failedApi?.freshness).toBe(STALE);
    expect(failedApi?.freshness).not.toBe("LIVE");
  });
});

describe("rowsFromProcessViews", () => {
  it("maps ListProcesses views without requiring node.read", () => {
    const rows = rowsFromProcessViews(
      [
        {
          processId: "p-pay",
          spec: { name: "pay", group: "finance", ownerAgentId: "node-fin", latestRevision: 2 },
          instances: [{ desired: "RUNNING", observed: "RUNNING", health: "HEALTHY", activeRevision: 2 }],
        },
      ],
      nowMs,
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].name).toBe("pay");
    expect(rows[0].group).toBe("finance");
    expect(rows[0].ownerNodeId).toBe("node-fin");
  });
});

describe("mapProcessDetail", () => {
  it("surfaces instance lastError on the process and instance row", () => {
    const detail = mapProcessDetail(
      {
        processId: "p1",
        spec: { name: "web", latestRevision: 1 },
        instances: [
          {
            instanceId: "p1:0",
            desired: "RUNNING",
            observed: "BACKOFF",
            lastError: "chdir /missing: no such file or directory",
            activeRevision: 1,
          },
        ],
      },
      [],
      nowMs,
    );
    expect(detail.lastError).toBe("chdir /missing: no such file or directory");
    expect(detail.instanceRows[0]?.lastError).toBe("chdir /missing: no such file or directory");
  });
});

describe("needsRestartBanner", () => {
  it("returns the restart banner when latest != active", () => {
    expect(needsRestartBanner(3, 2)).toBe(true);
    expect(needsRestartBanner(3, 3)).toBe(false);
    expect(RESTART_REQUIRED_BANNER).toBe("Configuration changed. Restart required.");
  });
});

describe("formatRemoteError", () => {
  it("surfaces UNAVAILABLE and TIMEOUT instead of a local success", () => {
    expect(formatRemoteError(new ConnectError("owner unreachable", Code.Unavailable))).toBe("UNAVAILABLE");
    expect(formatRemoteError(new ConnectError("rpc timed out", Code.DeadlineExceeded))).toBe("TIMEOUT");
  });

  it("surfaces DEGRADED from ErrorInfo even when Connect code is Unavailable", () => {
    const err = new ConnectError("store impaired", Code.Unavailable, undefined, [
      { desc: ErrorInfoSchema, value: { code: "DEGRADED", message: "store impaired" } },
    ]);
    expect(formatRemoteError(err)).toBe("DEGRADED");
    expect(formatRemoteError(err)).not.toBe("UNAVAILABLE");
  });

  it("keeps detailed messages for non-DEGRADED application errors", () => {
    const err = new ConnectError("invalid node", Code.InvalidArgument, undefined, [
      {
        desc: ErrorInfoSchema,
        value: { code: "INVALID", message: "INVALID: node is not an admitted member" },
      },
    ]);
    expect(formatRemoteError(err)).toBe("INVALID: node is not an admitted member");
  });
});

describe("ownerDisplay", () => {
  it("shows hostname only and never appends the node id", () => {
    expect(ownerDisplay("agent-a", "n-a")).toBe("agent-a");
    expect(ownerDisplay("", "n-a")).toBe("n-a");
    expect(ownerDisplay("", "")).toBe("—");
  });
});

describe("formatMetric", () => {
  it("shows unknown plus note when the metric is -1", () => {
    expect(formatMetric(-1, "macos: process cpu/memory unavailable")).toEqual({
      text: "unknown",
      note: "macos: process cpu/memory unavailable",
    });
  });
});
