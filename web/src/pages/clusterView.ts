import { classify, formatAge, PROCESS_MAX_AGE_MS, STALE, UNKNOWN, type Freshness } from "../lib/freshness";

export const DEGRADED_BANNER =
  "Agent DEGRADED — local store impaired; business processes are not stopped.";

export const REMOVE_CONFIRM = "Remove node and revoke its certificate?";

export type VersionCount = { version: string; count: number };

export type ResourceView = {
  cpuPercent: number;
  memoryPercent: number;
  diskPercent: number;
  historyWritesPaused: boolean;
  historyPausePercent: number;
};

export type ProcessView = {
  name: string;
  processId: string;
  group: string;
  desired: string;
  observed: string;
  health: string;
  latestRevision: number;
  activeRevision: number;
  freshness: Freshness;
  freshnessUnixMs: number;
};

export const RAFT_LEADER = "LEADER";
export const RAFT_VOTER = "VOTER";
export const RAFT_NON_VOTER = "NON_VOTER";
export const RAFT_NOT_MEMBER = "NOT_MEMBER";
export const RAFT_ROLE_UNKNOWN = "UNKNOWN";

export type RaftRole =
  | typeof RAFT_LEADER
  | typeof RAFT_VOTER
  | typeof RAFT_NON_VOTER
  | typeof RAFT_NOT_MEMBER
  | typeof RAFT_ROLE_UNKNOWN;

export type NodeView = {
  nodeId: string;
  hostname: string;
  state: string;
  raftRole: RaftRole;
  raftRoleFreshness: Freshness;
  agentVersion: string;
  bootId: string;
  apiAddress: string;
  rpcAddress: string;
  gossipAddress: string;
  labels: { key: string; value: string }[];
  resources: ResourceView;
  processCount: number;
  processes: ProcessView[];
  freshness: Freshness;
  lastUpdatedUnixMs: number;
  lastUpdated: string;
};

export type OverviewView = {
  clusterId: string;
  procMesh: {
    controlQuorum: boolean;
    controlQuorumLabel: string;
    controlLeader: string;
    gossipHealthy: boolean;
    rpcHealthy: boolean;
    certExpires: string;
    caExpires: string;
    versionCounts: VersionCount[];
    agentDegraded: boolean;
    degradedBanner: string;
    platformNote: string;
  };
  workload: {
    agentTotal: number;
    agentAlive: number;
    agentSuspect: number;
    agentFailed: number;
    processTotal: number;
    processRunning: number;
    processUnhealthy: number;
    processFatal: number;
    cpuPercent: number;
    memoryPercent: number;
    diskPercent: number;
    lastUpdatedUnixMs: number;
    lastUpdated: string;
    freshness: Freshness;
  };
};

function asRecord(v: unknown): Record<string, unknown> {
  return v && typeof v === "object" ? (v as Record<string, unknown>) : {};
}

function pick(obj: unknown, ...keys: string[]): unknown {
  const rec = asRecord(obj);
  for (const key of keys) {
    if (rec[key] !== undefined && rec[key] !== null) {
      return rec[key];
    }
  }
  return undefined;
}

function toNum(v: unknown): number {
  if (typeof v === "bigint") {
    return Number(v);
  }
  if (typeof v === "number") {
    return Number.isFinite(v) ? v : 0;
  }
  if (typeof v === "string" && v !== "") {
    const n = Number(v);
    return Number.isFinite(n) ? n : 0;
  }
  return 0;
}

function toResourcePercent(v: unknown): number {
  if (v === undefined || v === null || v === "") {
    return -1;
  }
  return toNum(v);
}

function toStr(v: unknown): string {
  if (v === undefined || v === null) {
    return "";
  }
  return String(v);
}

function toBool(v: unknown): boolean {
  return v === true;
}

function toRaftRole(v: unknown): RaftRole {
  switch (toStr(v).toUpperCase()) {
    case RAFT_LEADER:
      return RAFT_LEADER;
    case RAFT_VOTER:
      return RAFT_VOTER;
    case RAFT_NON_VOTER:
      return RAFT_NON_VOTER;
    case RAFT_NOT_MEMBER:
      return RAFT_NOT_MEMBER;
    default:
      return RAFT_ROLE_UNKNOWN;
  }
}

function toFreshness(v: unknown): Freshness {
  switch (toStr(v).toUpperCase()) {
    case "LIVE":
      return "LIVE";
    case "STALE":
      return "STALE";
    default:
      return UNKNOWN;
  }
}

export function formatUnixSecondsISO(unix: unknown): string {
  const n = toNum(unix);
  if (n <= 0) {
    return "";
  }
  return new Date(n * 1000).toISOString();
}

export function formatPercent(n: number): string {
  if (!Number.isFinite(n) || n < 0) {
    return "unknown";
  }
  return `${n}%`;
}

export function formatResources(r: ResourceView): string {
  return `CPU ${formatPercent(r.cpuPercent)} · Mem ${formatPercent(r.memoryPercent)} · Disk ${formatPercent(r.diskPercent)}`;
}

export function workloadFreshness(
  nowMs: number,
  viewUnixMs: number,
  agentFailed: number,
  agentSuspect: number,
): Freshness {
  if (agentFailed > 0 || agentSuspect > 0) {
    return STALE;
  }
  if (viewUnixMs <= 0) {
    return UNKNOWN;
  }
  return classify(nowMs, viewUnixMs, "ALIVE");
}

export function mapOverview(input: unknown, nowMs = Date.now()): OverviewView {
  const quorum = toBool(pick(input, "controlQuorum", "control_quorum"));
  const degraded = toBool(pick(input, "agentDegraded", "agent_degraded"));
  const versions = asRecord(pick(input, "versionCounts", "version_counts"));
  const viewUnixMs = toNum(pick(input, "viewUnixMs", "view_unix_ms"));
  const agentFailed = toNum(pick(input, "failed"));
  const agentSuspect = toNum(pick(input, "suspect"));
  return {
    clusterId: toStr(pick(input, "clusterId", "cluster_id")),
    procMesh: {
      controlQuorum: quorum,
      controlQuorumLabel: quorum ? "Quorum" : "No quorum",
      controlLeader: toStr(pick(input, "controlLeader", "control_leader")),
      gossipHealthy: toBool(pick(input, "gossipHealthy", "gossip_healthy")),
      rpcHealthy: toBool(pick(input, "rpcHealthy", "rpc_healthy")),
      certExpires: formatUnixSecondsISO(pick(input, "certExpiresUnix", "cert_expires_unix")),
      caExpires: formatUnixSecondsISO(pick(input, "caExpiresUnix", "ca_expires_unix")),
      versionCounts: Object.entries(versions)
        .map(([version, count]) => ({ version, count: toNum(count) }))
        .sort((a, b) => a.version.localeCompare(b.version)),
      agentDegraded: degraded,
      degradedBanner: degraded ? DEGRADED_BANNER : "",
      platformNote: toStr(pick(input, "platformNote", "platform_note")),
    },
    workload: {
      agentTotal: toNum(pick(input, "members")),
      agentAlive: toNum(pick(input, "alive")),
      agentSuspect: toNum(pick(input, "suspect")),
      agentFailed: toNum(pick(input, "failed")),
      processTotal: toNum(pick(input, "processTotal", "process_total")),
      processRunning: toNum(pick(input, "processRunning", "process_running")),
      processUnhealthy: toNum(pick(input, "processUnhealthy", "process_unhealthy")),
      processFatal: toNum(pick(input, "processFatal", "process_fatal")),
      cpuPercent: toResourcePercent(pick(input, "cpuPercent", "cpu_percent")),
      memoryPercent: toResourcePercent(pick(input, "memoryPercent", "memory_percent")),
      diskPercent: toResourcePercent(pick(input, "diskPercent", "disk_percent")),
      lastUpdatedUnixMs: viewUnixMs,
      lastUpdated: formatAge(nowMs, viewUnixMs),
      freshness: workloadFreshness(nowMs, viewUnixMs, agentFailed, agentSuspect),
    },
  };
}

export function mapProcess(input: unknown, nodeState: string, nowMs: number): ProcessView {
  const freshnessUnixMs = toNum(pick(input, "freshnessUnixMs", "freshness_unix_ms"));
  return {
    name: toStr(pick(input, "name")),
    processId: toStr(pick(input, "processId", "process_id")),
    group: toStr(pick(input, "group")),
    desired: toStr(pick(input, "desired")),
    observed: toStr(pick(input, "observed")),
    health: toStr(pick(input, "health")),
    latestRevision: toNum(pick(input, "latestRevision", "latest_revision")),
    activeRevision: toNum(pick(input, "activeRevision", "active_revision")),
    freshness: classify(nowMs, freshnessUnixMs, nodeState, PROCESS_MAX_AGE_MS),
    freshnessUnixMs,
  };
}

export function mapNode(input: unknown, nowMs: number): NodeView {
  const state = toStr(pick(input, "state"));
  const lastUpdatedUnixMs = toNum(pick(input, "lastUpdatedUnixMs", "last_updated_unix_ms"));
  const resources = asRecord(pick(input, "resources"));
  const processesRaw = pick(input, "processes");
  const processes = Array.isArray(processesRaw)
    ? processesRaw.map((p) => mapProcess(p, state, nowMs))
    : [];
  const labels = Object.entries(asRecord(pick(input, "labels")))
    .map(([key, value]) => ({ key, value: toStr(value) }))
    .sort((a, b) => a.key.localeCompare(b.key));
  let raftRole = toRaftRole(pick(input, "raftRole", "raft_role"));
  let raftRoleFreshness = toFreshness(
    pick(input, "raftRoleFreshness", "raft_role_freshness"),
  );
  if (raftRole === RAFT_ROLE_UNKNOWN || raftRoleFreshness === UNKNOWN) {
    raftRole = RAFT_ROLE_UNKNOWN;
    raftRoleFreshness = UNKNOWN;
  }
  return {
    nodeId: toStr(pick(input, "nodeId", "node_id")),
    hostname: toStr(pick(input, "hostname")),
    state,
    raftRole,
    raftRoleFreshness,
    agentVersion: toStr(pick(input, "agentVersion", "agent_version")),
    bootId: toStr(pick(input, "bootId", "boot_id")),
    apiAddress: toStr(pick(input, "apiAddress", "api_address")),
    rpcAddress: toStr(pick(input, "rpcAddress", "rpc_address")),
    gossipAddress: toStr(pick(input, "gossipAddress", "gossip_address")),
    labels,
    resources: {
      cpuPercent: toResourcePercent(pick(resources, "cpuPercent", "cpu_percent")),
      memoryPercent: toResourcePercent(pick(resources, "memoryPercent", "memory_percent")),
      diskPercent: toResourcePercent(pick(resources, "diskPercent", "disk_percent")),
      historyWritesPaused: toBool(pick(resources, "historyWritesPaused", "history_writes_paused")),
      historyPausePercent: toNum(pick(resources, "historyPausePercent", "history_pause_percent")),
    },
    processCount: processes.length,
    processes,
    freshness: classify(nowMs, lastUpdatedUnixMs, state),
    lastUpdatedUnixMs,
    lastUpdated: formatAge(nowMs, lastUpdatedUnixMs),
  };
}
