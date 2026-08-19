import { Code, ConnectError } from "@connectrpc/connect";
import { appCode, appMessage } from "../lib/connecterr";
import { LIVE, type Freshness } from "../lib/freshness";
import { mapNode } from "./clusterView";

export const RESTART_REQUIRED_BANNER = "Configuration changed. Restart required.";

export type ClusterProcessRow = {
  name: string;
  processId: string;
  group: string;
  ownerNodeId: string;
  ownerHostname: string;
  ownerState: string;
  desired: string;
  observed: string;
  health: string;
  latestRevision: number;
  activeRevision: number;
  freshness: Freshness;
  freshnessUnixMs: number;
};

export type ProcessInstanceRow = {
  instanceId: string;
  ordinal: number;
  desired: string;
  observed: string;
  health: string;
  pid: string;
  uptime: string;
  restartCount: number;
  exitCode: string;
  activeRevision: number;
  cpu: string;
  cpuNote: string;
  memory: string;
  memoryNote: string;
};

export type ProcessDetailView = {
  name: string;
  processId: string;
  owner: string;
  instances: number;
  desired: string;
  observed: string;
  health: string;
  pid: string;
  uptime: string;
  restartCount: number;
  exitCode: string;
  activeRevision: number;
  latestRevision: number;
  showRestartBanner: boolean;
  restartBanner: string;
  logPathPending: boolean;
  redirectStderr: boolean;
  cpu: string;
  cpuNote: string;
  memory: string;
  memoryNote: string;
  instanceRows: ProcessInstanceRow[];
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

function toStr(v: unknown): string {
  if (v === undefined || v === null) {
    return "";
  }
  return String(v);
}

function toBool(v: unknown): boolean {
  return v === true;
}

export function flattenClusterProcesses(nodes: unknown[], nowMs: number): ClusterProcessRow[] {
  const rows: ClusterProcessRow[] = [];
  for (const raw of nodes) {
    const node = mapNode(raw, nowMs);
    for (const proc of node.processes) {
      rows.push({
        name: proc.name,
        processId: "",
        group: proc.group,
        ownerNodeId: node.nodeId,
        ownerHostname: node.hostname,
        ownerState: node.state,
        desired: proc.desired,
        observed: proc.observed,
        health: proc.health,
        latestRevision: proc.latestRevision,
        activeRevision: proc.activeRevision,
        freshness: proc.freshness,
        freshnessUnixMs: proc.freshnessUnixMs,
      });
    }
  }
  rows.sort((a, b) => {
    const byName = a.name.localeCompare(b.name);
    if (byName !== 0) {
      return byName;
    }
    return a.ownerNodeId.localeCompare(b.ownerNodeId);
  });
  return rows;
}

export function rowsFromProcessViews(processes: unknown[], nowMs: number): ClusterProcessRow[] {
  const rows: ClusterProcessRow[] = [];
  for (const raw of processes) {
    const rec = asRecord(raw);
    const spec = pick(rec, "spec") ?? rec;
    const instancesRaw = pick(rec, "instances");
    const instances = Array.isArray(instancesRaw) ? instancesRaw : [];
    const first = asRecord(instances[0]);
    rows.push({
      name: toStr(pick(spec, "name")),
      processId: toStr(pick(rec, "processId", "process_id") ?? pick(spec, "processId", "process_id")),
      group: toStr(pick(spec, "group")),
      ownerNodeId: toStr(pick(spec, "ownerAgentId", "owner_agent_id")),
      ownerHostname: "",
      ownerState: "ALIVE",
      desired: toStr(pick(first, "desired")),
      observed: toStr(pick(first, "observed")),
      health: toStr(pick(first, "health")),
      latestRevision: toNum(pick(spec, "latestRevision", "latest_revision")),
      activeRevision: toNum(
        pick(first, "activeRevision", "active_revision") ?? pick(spec, "activeRevision", "active_revision"),
      ),
      freshness: LIVE,
      freshnessUnixMs: nowMs,
    });
  }
  return rows;
}

export function mergeProcessRows(gossip: ClusterProcessRow[], listed: ClusterProcessRow[]): ClusterProcessRow[] {
  const listedByKey = new Map(listed.map((row) => [rowKey(row), row]));
  const seen = new Set<string>();
  const out: ClusterProcessRow[] = [];
  for (const row of gossip) {
    const key = rowKey(row);
    seen.add(key);
    const extra = listedByKey.get(key);
    if (extra?.processId && !row.processId) {
      out.push({ ...row, processId: extra.processId });
    } else {
      out.push(row);
    }
  }
  for (const row of listed) {
    const key = rowKey(row);
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    out.push(row);
  }
  out.sort((a, b) => {
    const byName = a.name.localeCompare(b.name);
    if (byName !== 0) {
      return byName;
    }
    return a.ownerNodeId.localeCompare(b.ownerNodeId);
  });
  return out;
}

export function needsRestartBanner(latestRevision: number, activeRevision: number): boolean {
  return latestRevision !== activeRevision;
}

export function formatRemoteError(err: unknown): string {
  const app = appCode(err);
  if (app) {
    if (app === "DEGRADED") {
      return app;
    }
    const msg = appMessage(err);
    // Preserve the application state for DEGRADED, but show actionable details for other errors.
    return msg || app;
  }
  if (err instanceof ConnectError) {
    if (err.code === Code.DeadlineExceeded) {
      return "TIMEOUT";
    }
    if (err.code === Code.Unavailable) {
      return "UNAVAILABLE";
    }
    if (err.rawMessage.includes("TIMEOUT") || err.message.includes("TIMEOUT")) {
      return "TIMEOUT";
    }
    if (err.rawMessage.includes("UNAVAILABLE") || err.message.includes("UNAVAILABLE")) {
      return "UNAVAILABLE";
    }
    return err.rawMessage || err.message;
  }
  const text = err instanceof Error ? err.message : String(err);
  if (text.includes("TIMEOUT")) {
    return "TIMEOUT";
  }
  if (text.includes("UNAVAILABLE")) {
    return "UNAVAILABLE";
  }
  return text;
}

export function formatMetric(value: number | bigint, note = "", suffix = ""): { text: string; note: string } {
  const n = typeof value === "bigint" ? Number(value) : value;
  if (!Number.isFinite(n) || n < 0) {
    return { text: "unknown", note };
  }
  return { text: suffix ? `${n}${suffix}` : String(n), note: "" };
}

export function formatUptimeSeconds(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) {
    return "—";
  }
  const total = Math.floor(seconds);
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  if (days > 0) {
    return `${days}d ${hours}h`;
  }
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  if (minutes > 0) {
    return `${minutes}m ${secs}s`;
  }
  return `${secs}s`;
}

export function formatMemoryBytes(value: number | bigint, note = ""): { text: string; note: string } {
  const n = typeof value === "bigint" ? Number(value) : value;
  if (!Number.isFinite(n) || n < 0) {
    return { text: "unknown", note };
  }
  if (n >= 1024 * 1024 * 1024) {
    return { text: `${(n / (1024 * 1024 * 1024)).toFixed(1)} GiB`, note: "" };
  }
  if (n >= 1024 * 1024) {
    return { text: `${(n / (1024 * 1024)).toFixed(1)} MiB`, note: "" };
  }
  if (n >= 1024) {
    return { text: `${(n / 1024).toFixed(1)} KiB`, note: "" };
  }
  return { text: `${n} B`, note: "" };
}

function pidText(pid: number): string {
  return pid > 0 ? String(pid) : "—";
}

function exitText(hasExit: boolean, code: number): string {
  return hasExit ? String(code) : "—";
}

function uptimeFromStart(startedUnixMs: number, nowMs: number): string {
  if (startedUnixMs <= 0) {
    return "—";
  }
  return formatUptimeSeconds((nowMs - startedUnixMs) / 1000);
}

export function mapProcessDetail(
  input: unknown,
  metricsInput: unknown,
  nowMs: number,
  ownerLabel = "",
): ProcessDetailView {
  const rec = asRecord(input);
  const spec = pick(rec, "spec") ?? rec;
  const instancesRaw = pick(rec, "instances");
  const instances = Array.isArray(instancesRaw) ? instancesRaw : [];
  const metricsRaw = Array.isArray(metricsInput)
    ? metricsInput
    : pick(metricsInput, "metrics");
  const metrics = Array.isArray(metricsRaw) ? metricsRaw : [];
  const metricsById = new Map<string, Record<string, unknown>>();
  for (const m of metrics) {
    const row = asRecord(m);
    const id = toStr(pick(row, "instanceId", "instance_id"));
    if (id) {
      metricsById.set(id, row);
    }
  }

  const first = instances[0];
  const firstRec = asRecord(first);
  const firstMetric = asRecord(metrics[0]);
  const latestRevision = toNum(pick(spec, "latestRevision", "latest_revision"));
  const activeRevision = toNum(
    pick(firstRec, "activeRevision", "active_revision") ?? pick(spec, "activeRevision", "active_revision"),
  );
  const specInstances = toNum(pick(spec, "instances"));
  const instanceCount = specInstances > 0 ? specInstances : instances.length;
  const startedUnixMs = toNum(pick(firstRec, "startedUnixMs", "started_unix_ms"));
  const metricUptime = pick(firstMetric, "uptimeSeconds", "uptime_seconds");
  const uptime =
    metricUptime !== undefined
      ? formatUptimeSeconds(toNum(metricUptime))
      : uptimeFromStart(startedUnixMs, nowMs);
  const cpuMetric = formatMetric(toNum(pick(firstMetric, "cpuPercent", "cpu_percent") ?? -1), toStr(pick(firstMetric, "note")), "%");
  const memMetric = formatMemoryBytes(toNum(pick(firstMetric, "memoryBytes", "memory_bytes") ?? -1), toStr(pick(firstMetric, "note")));
  const hasMetrics = metrics.length > 0;

  const instanceRows: ProcessInstanceRow[] = instances.map((inst) => {
    const row = asRecord(inst);
    const id = toStr(pick(row, "instanceId", "instance_id"));
    const metric = metricsById.get(id) ?? {};
    const note = toStr(pick(metric, "note"));
    const started = toNum(pick(row, "startedUnixMs", "started_unix_ms"));
    const mUptime = pick(metric, "uptimeSeconds", "uptime_seconds");
    const cpu = Object.keys(metric).length
      ? formatMetric(toNum(pick(metric, "cpuPercent", "cpu_percent") ?? -1), note, "%")
      : { text: "—", note: "" };
    const memory = Object.keys(metric).length
      ? formatMemoryBytes(toNum(pick(metric, "memoryBytes", "memory_bytes") ?? -1), note)
      : { text: "—", note: "" };
    return {
      instanceId: id,
      ordinal: toNum(pick(row, "ordinal")),
      desired: toStr(pick(row, "desired")),
      observed: toStr(pick(row, "observed")),
      health: toStr(pick(row, "health")),
      pid: pidText(toNum(pick(row, "pid"))),
      uptime: mUptime !== undefined ? formatUptimeSeconds(toNum(mUptime)) : uptimeFromStart(started, nowMs),
      restartCount: toNum(pick(row, "restartCount", "restart_count")),
      exitCode: exitText(toBool(pick(row, "hasExitCode", "has_exit_code")), toNum(pick(row, "exitCode", "exit_code"))),
      activeRevision: toNum(pick(row, "activeRevision", "active_revision")),
      cpu: cpu.text,
      cpuNote: cpu.note,
      memory: memory.text,
      memoryNote: memory.note,
    };
  });

  const name = toStr(pick(spec, "name")) || toStr(pick(rec, "name"));
  const processId = toStr(pick(rec, "processId", "process_id")) || toStr(pick(spec, "processId", "process_id"));
  const owner = ownerLabel || toStr(pick(spec, "ownerAgentId", "owner_agent_id"));
  const showBanner = needsRestartBanner(latestRevision, activeRevision);
  const log = pick(spec, "log") ?? {};

  return {
    name,
    processId,
    owner,
    instances: instanceCount,
    desired: toStr(pick(firstRec, "desired")),
    observed: toStr(pick(firstRec, "observed")),
    health: toStr(pick(firstRec, "health")),
    pid: pidText(toNum(pick(firstRec, "pid"))),
    uptime,
    restartCount: toNum(pick(firstRec, "restartCount", "restart_count")),
    exitCode: exitText(toBool(pick(firstRec, "hasExitCode", "has_exit_code")), toNum(pick(firstRec, "exitCode", "exit_code"))),
    activeRevision,
    latestRevision,
    showRestartBanner: showBanner,
    restartBanner: showBanner ? RESTART_REQUIRED_BANNER : "",
    logPathPending: instances.some((inst) => toBool(pick(asRecord(inst), "logPathPending", "log_path_pending"))),
    redirectStderr: toBool(pick(asRecord(log), "redirectStderr", "redirect_stderr")),
    cpu: hasMetrics ? cpuMetric.text : "—",
    cpuNote: hasMetrics ? cpuMetric.note : "",
    memory: hasMetrics ? memMetric.text : "—",
    memoryNote: hasMetrics ? memMetric.note : "",
    instanceRows,
  };
}

export function ownerDisplay(hostname: string, nodeId: string): string {
  return hostname || nodeId || "—";
}

export function rowKey(row: ClusterProcessRow): string {
  return `${row.name}@${row.ownerNodeId}`;
}
