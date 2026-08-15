import { describe, expect, it } from "vitest";
import { LIVE, STALE, UNKNOWN } from "../lib/freshness";
import {
  DEGRADED_BANNER,
  formatPercent,
  formatResources,
  mapNode,
  mapOverview,
  mapProcess,
  workloadFreshness,
} from "./clusterView";

const nowMs = 1_700_000_010_000;

describe("mapOverview", () => {
  it("maps a proto-like overview into ProcMesh and Workload view-model", () => {
    const view = mapOverview({
      clusterId: "c1",
      members: 3,
      alive: 2,
      controlQuorum: false,
      controlLeader: "n1",
      suspect: 0,
      failed: 1,
      processTotal: 4,
      processRunning: 3,
      processUnhealthy: 1,
      processFatal: 0,
      cpuPercent: 12,
      memoryPercent: 34,
      diskPercent: 56,
      gossipHealthy: true,
      rpcHealthy: false,
      agentDegraded: true,
      certExpiresUnix: 1_700_000_000,
      caExpiresUnix: 1_800_000_000,
      platformNote:
        "macOS: resource_limit ignored (no cgroup); Host reboot recovery depends on how the Agent is started.",
      versionCounts: { "0.1.0": 2, "0.1.1": 1 },
    });

    expect(view.procMesh.controlQuorum).toBe(false);
    expect(view.procMesh.controlQuorumLabel).toBe("No quorum");
    expect(view.procMesh.gossipHealthy).toBe(true);
    expect(view.procMesh.rpcHealthy).toBe(false);
    expect(view.procMesh.certExpires).toBe(new Date(1_700_000_000 * 1000).toISOString());
    expect(view.procMesh.caExpires).toBe(new Date(1_800_000_000 * 1000).toISOString());
    expect(view.procMesh.degradedBanner).toBe(DEGRADED_BANNER);
    expect(view.procMesh.platformNote).toContain("macOS: resource_limit ignored");
    expect(view.procMesh.versionCounts).toEqual([
      { version: "0.1.0", count: 2 },
      { version: "0.1.1", count: 1 },
    ]);

    expect(view.workload.agentTotal).toBe(3);
    expect(view.workload.agentAlive).toBe(2);
    expect(view.workload.agentSuspect).toBe(0);
    expect(view.workload.agentFailed).toBe(1);
    expect(view.workload.processTotal).toBe(4);
    expect(view.workload.processRunning).toBe(3);
    expect(view.workload.processUnhealthy).toBe(1);
    expect(view.workload.processFatal).toBe(0);
    expect(view.workload.cpuPercent).toBe(12);
    expect(view.workload.memoryPercent).toBe(34);
    expect(view.workload.diskPercent).toBe(56);
    expect(view.workload.freshness).toBe(STALE);
  });

  it("marks missing resource percents as unknown, not 0%", () => {
    const view = mapOverview({ members: 1, alive: 1 }, nowMs);
    expect(view.workload.cpuPercent).toBe(-1);
    expect(view.workload.memoryPercent).toBe(-1);
    expect(view.workload.diskPercent).toBe(-1);
    expect(formatPercent(view.workload.cpuPercent)).toBe("unknown");
    expect(formatPercent(0)).toBe("0%");
  });

  it("marks FAILED last-known workload counts as STALE, not LIVE", () => {
    const view = mapOverview(
      {
        members: 2,
        alive: 1,
        failed: 1,
        processRunning: 2,
        viewUnixMs: nowMs,
      },
      nowMs,
    );
    expect(view.workload.processRunning).toBe(2);
    expect(view.workload.freshness).toBe(STALE);
    expect(view.workload.freshness).not.toBe(LIVE);
    expect(view.workload.lastUpdatedUnixMs).toBe(nowMs);
    expect(view.workload.lastUpdated).toBe("0s ago");
  });

  it("does not treat lost quorum or degraded as a process fault", () => {
    const view = mapOverview({ controlQuorum: false, agentDegraded: true });
    expect(view.procMesh.controlQuorumLabel).toBe("No quorum");
    expect(view.procMesh.degradedBanner).toBe(DEGRADED_BANNER);
    expect(view.procMesh.degradedBanner.toLowerCase()).not.toMatch(/process (down|fault|failure)/);
  });

  it("hides empty platform note and degraded banner", () => {
    const view = mapOverview({ controlQuorum: true, platformNote: "", agentDegraded: false });
    expect(view.procMesh.controlQuorumLabel).toBe("Quorum");
    expect(view.procMesh.platformNote).toBe("");
    expect(view.procMesh.degradedBanner).toBe("");
  });
});

describe("mapProcess / mapNode", () => {
  it("marks a RUNNING process on a FAILED node as STALE", () => {
    const row = mapProcess(
      {
        name: "api",
        desired: "RUNNING",
        observed: "RUNNING",
        health: "HEALTHY",
        latestRevision: 3,
        activeRevision: 2,
        freshnessUnixMs: nowMs - 1_000,
      },
      "FAILED",
      nowMs,
    );
    expect(row.observed).toBe("RUNNING");
    expect(row.freshness).toBe(STALE);
  });

  it("does not classify FAILED node process summaries as LIVE", () => {
    const node = mapNode(
      {
        nodeId: "n-failed",
        hostname: "agent-c",
        state: "FAILED",
        agentVersion: "0.1.0",
        lastUpdatedUnixMs: nowMs - 60_000,
        resources: { cpuPercent: 1, memoryPercent: 2, diskPercent: 3 },
        processes: [
          {
            name: "api",
            desired: "RUNNING",
            observed: "RUNNING",
            health: "HEALTHY",
            freshnessUnixMs: nowMs - 1_000,
          },
        ],
      },
      nowMs,
    );
    expect(node.freshness).toBe(STALE);
    expect(node.processes).toHaveLength(1);
    expect(node.processes[0]?.freshness).toBe(STALE);
    expect(node.processes[0]?.freshness).not.toBe("LIVE");
  });

  it("formats uncollected node resources as unknown", () => {
    const node = mapNode({ nodeId: "n1", state: "ALIVE", lastUpdatedUnixMs: nowMs }, nowMs);
    expect(formatResources(node.resources)).toBe("CPU unknown · Mem unknown · Disk unknown");
    expect(formatPercent(node.resources.cpuPercent)).toBe("unknown");
  });
});

describe("workloadFreshness", () => {
  it("is UNKNOWN without view timestamp", () => {
    expect(workloadFreshness(nowMs, 0, 0, 0)).toBe(UNKNOWN);
  });

  it("is LIVE when all members are ALIVE and the view is fresh", () => {
    expect(workloadFreshness(nowMs, nowMs - 1_000, 0, 0)).toBe(LIVE);
  });
});
