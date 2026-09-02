<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Template enums, data-* hooks, and comparison literals are non-copy; visible copy uses t(). */
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import {
  ChevronDown,
  Database,
  HardDrive,
  LoaderCircle,
  Plus,
  Search,
  TriangleAlert,
  X,
} from "lucide-vue-next";
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import Drawer from "../components/Drawer.vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import Toast from "../components/Toast.vue";
import { LIVE, STALE, UNKNOWN, formatAge, type Freshness } from "../lib/freshness";
import { withTarget } from "../lib/headers";
import { newOperationId } from "../lib/opid";
import { useBackupClient, useClusterBackupClient, useNodeClient, useProcessClient } from "../lib/rpc";
import { session } from "../lib/session";
import { browserTimezone, timezoneLabel, timezonePickerOptions } from "../lib/timezones";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError, rowsFromProcessViews } from "./processView";

const NUMERIC_INPUT_MODE = "numeric" as const;
const SINKS = ["fs", "s3"] as const;
const POLICY_SINKS = ["fs", "s3"] as const;
const TARGET_SELECTORS = ["ALL_ADMITTED", "AGENT_GROUP", "EXPLICIT_NODES"] as const;
const UNAVAILABLE_POLICIES = ["RECORD_AND_CONTINUE", "FAIL_FAST"] as const;

type PolicySink = (typeof POLICY_SINKS)[number];
type TargetSelector = (typeof TARGET_SELECTORS)[number];
type UnavailablePolicy = (typeof UNAVAILABLE_POLICIES)[number];

type ClusterPolicy = {
  policyId?: string;
  name?: string;
  enabled?: boolean;
  scheduleCron?: string;
  timezone?: string;
  targetSelector?: string;
  targetNodeIds?: string[];
  sink?: string;
  destinationProfile?: string;
  retentionKeepLast?: number;
  retentionKeepDays?: number;
  retentionMaxBytes?: bigint;
  timeoutSeconds?: number;
  maxConcurrency?: number;
  unavailablePolicy?: string;
  revision?: bigint;
};

type ClusterTask = {
  runId?: string;
  taskId?: string;
  nodeId?: string;
  snapshotId?: string;
  sha256?: string;
  status?: string;
  bytes?: bigint | number;
  errorCode?: string;
  errorSummary?: string;
};

type ClusterRun = {
  runId?: string;
  policyId?: string;
  targetNodeIds?: string[];
  status?: string;
  success?: number;
  failed?: number;
  unavailable?: number;
  timeout?: number;
  createdUnix?: bigint | number;
  startedUnix?: bigint | number;
  finishedUnix?: bigint | number;
  tasks?: ClusterTask[];
};

const { t } = useI18n();
const DISK_PROTECTION_RE = /disk usage at or above 95%/i;
const route = useRoute();
const POLL_MS = 5000;
const client = useBackupClient();
const clusterClient = useClusterBackupClient();
const nodeClient = useNodeClient();
const processClient = useProcessClient();
const queryClient = useQueryClient();
const actionError = ref("");
const actionNotice = ref("");
const clusterError = ref("");
const toastMessage = ref("");
const toastType = ref<"success" | "error" | "info" | "warning">("success");
const showToast = ref(false);

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canManage = computed(() => perms.value.has("backup.manage"));

const createOpen = ref(false);
const createSink = ref<(typeof SINKS)[number]>("fs");
const createScope = ref<"all" | "selected">("all");
const createProcessIds = ref<string[]>([]);
const createProcessSearch = ref("");
const policyAdvancedOpen = ref(false);

type PendingDelete =
  | { kind: "snapshot"; snapshot: RestoreSnapshot }
  | { kind: "policy"; policy: ClusterPolicy }
  | null;
const pendingDelete = ref<PendingDelete>(null);

type RestoreTargetForm = { processId: string; expectedRevision: string };
type RestoreSnapshot = {
  snapshotId: string;
  nodeId: string;
  sink: string;
  sourceNodeId: string;
  processIds: string[];
  revisionRanges: { processId?: string; minRevision?: bigint | number; maxRevision?: bigint | number }[];
};

const restoreOpen = ref(false);
const restoreSnapshot = ref<RestoreSnapshot | null>(null);
const restoreTargets = ref<RestoreTargetForm[]>([]);
const restoreQueryApplied = ref("");
const lastPeerNodeIds = ref<string[]>([]);
const nodesUnavailable = ref(false);
const nodeNames = ref<Record<string, string>>({});

const policyOpen = ref(false);
const editingPolicyId = ref("");
const policyName = ref("");
const policyEnabled = ref(true);
const policyCron = ref("");
const policyTimezone = ref(defaultTimezone());
const policyTargetSelector = ref<TargetSelector>("ALL_ADMITTED");
const policyTargetIds = ref("");
const policySink = ref<PolicySink>("fs");
const policyProfile = ref("");
const policyKeepLast = ref("7");
const policyKeepDays = ref("0");
const policyMaxBytes = ref("0");
const policyTimeout = ref("30");
const policyConcurrency = ref("0");
const policyUnavailable = ref<UnavailablePolicy>("RECORD_AND_CONTINUE");
const policyRevision = ref(0n);
const selectedRunId = ref("");
const policyTimezonePicker = computed(() => timezonePickerOptions(policyTimezone.value));

async function collectAlivePeerIds(): Promise<string[]> {
  try {
    const res = await nodeClient.listNodes({});
    nodesUnavailable.value = false;
    const nodes = res.nodes ?? [];
    nodeNames.value = Object.fromEntries(
      nodes.filter((n) => Boolean(n.nodeId)).map((n) => [n.nodeId, n.hostname || n.nodeId]),
    );
    return nodes.filter((n) => (n.state || "").toUpperCase() === "ALIVE" && n.nodeId).map((n) => n.nodeId);
  } catch {
    nodesUnavailable.value = true;
    return [];
  }
}

function nodeName(nodeId: string): string {
  return nodeNames.value[nodeId] || nodeId || "—";
}

const listQuery = useQuery({
  queryKey: ["backups"],
  queryFn: async () => {
    const peerNodeIds = await collectAlivePeerIds();
    lastPeerNodeIds.value = peerNodeIds;
    return client.listBackups({
      includeS3: true,
      peerNodeIds,
    });
  },
  refetchInterval: POLL_MS,
});

const policyQuery = useQuery({
  queryKey: ["cluster-backup-policies"],
  queryFn: () => clusterClient.listPolicies({}),
  refetchInterval: POLL_MS,
});

const runQuery = useQuery({
  queryKey: ["cluster-backup-runs"],
  queryFn: () => clusterClient.listRuns({ limit: 50 }),
  refetchInterval: POLL_MS,
});

const runDetailOpen = computed(() => selectedRunId.value.length > 0);

const runDetailQuery = useQuery({
  queryKey: ["cluster-backup-run", selectedRunId],
  queryFn: () => clusterClient.getRun({ runId: selectedRunId.value }),
  enabled: runDetailOpen,
  refetchInterval: POLL_MS,
});

const policies = computed(() => (policyQuery.data.value?.policies ?? []) as ClusterPolicy[]);
const runs = computed(() => (runQuery.data.value?.runs ?? []) as ClusterRun[]);

const healthRequests = computed(() => {
  const seen = new Set<string>();
  const out: { sink: string; destinationProfile: string }[] = [];
  for (const policy of policies.value) {
    const sink = policy.sink || "fs";
    const destinationProfile = policy.destinationProfile || "";
    const key = `${sink}:${destinationProfile}`;
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    out.push({ sink, destinationProfile });
  }
  return out;
});

const healthKey = computed(() =>
  healthRequests.value.map((req) => `${req.sink}:${req.destinationProfile}`).join("|"),
);

const healthQuery = useQuery({
  queryKey: ["cluster-backup-destination-health", healthKey],
  queryFn: async () => {
    const results = await Promise.all(healthRequests.value.map((req) => clusterClient.getDestinationHealth(req)));
    return results.map((res) => res.health).filter((health) => health !== undefined);
  },
  enabled: computed(() => healthRequests.value.length > 0),
  refetchInterval: POLL_MS,
});

const healthRows = computed(() => healthQuery.data.value ?? []);

const localProcessQuery = useQuery({
  queryKey: ["backup-local-processes"],
  queryFn: () => processClient.listProcesses({}),
  enabled: createOpen,
  refetchInterval: POLL_MS,
});

const localProcesses = computed(() =>
  rowsFromProcessViews(localProcessQuery.data.value?.processes ?? [], Date.now())
    .filter((row) => row.processId.length > 0)
    .sort((a, b) => (a.name || a.processId).localeCompare(b.name || b.processId)),
);

const createSearchTerm = computed(() => createProcessSearch.value.trim().toLowerCase());
const filteredLocalProcesses = computed(() => {
  if (!createSearchTerm.value) {
    return localProcesses.value;
  }
  return localProcesses.value.filter((row) => {
    const haystack = [row.name, row.processId, row.group].join(" ").toLowerCase();
    return haystack.includes(createSearchTerm.value);
  });
});
const selectedProcessSet = computed(() => new Set(createProcessIds.value));
const selectedProcessRows = computed(() =>
  localProcesses.value.filter((row) => selectedProcessSet.value.has(row.processId)),
);
const visibleAllSelected = computed(() => {
  const visible = filteredLocalProcesses.value;
  return visible.length > 0 && visible.every((row) => selectedProcessSet.value.has(row.processId));
});
const localProcessesPending = computed(
  () => localProcessQuery.isPending.value && !localProcessQuery.data.value,
);
const localProcessesError = computed(() => {
  const err = localProcessQuery.error.value;
  return err ? formatRemoteError(err) : "";
});
const createReady = computed(() => createScope.value === "all" || createProcessIds.value.length > 0);
const showFsPageWarning = computed(() =>
  policies.value.some((policy) => (policy.sink || "fs") === "fs"),
);
const selectedRun = computed(() => {
  const detailed = runDetailQuery.data.value?.run as ClusterRun | undefined;
  if (detailed?.runId === selectedRunId.value) {
    return detailed;
  }
  return runs.value.find((run) => run.runId === selectedRunId.value);
});
const selectedTasks = computed(() => selectedRun.value?.tasks ?? []);
const hasPartialRun = computed(() => runs.value.some((run) => isPartial(run.status)));
const policyRows = computed(() =>
  policies.value.map((policy) => {
    const latest = latestRunFor(policy.policyId ?? "");
    return {
      policy,
      latestStatus: latest?.status || "—",
      nextRun: nextRunLabel(policy),
      retention: t("backup.retentionSummary", {
        last: policy.retentionKeepLast ?? 0,
        days: policy.retentionKeepDays ?? 0,
      }),
    };
  }),
);

const entries = computed(() => listQuery.data.value?.entries ?? []);
const hasStale = computed(() => entries.value.some((e) => freshnessOf(e.freshness) === STALE));
const rows = computed(() => entries.value.map(mapEntry));

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  const err = listQuery.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

const clusterErrorText = computed(() => {
  if (clusterError.value) {
    return clusterError.value;
  }
  const err = policyQuery.error.value ?? runQuery.error.value ?? healthQuery.error.value ?? runDetailQuery.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

const listPending = computed(() => listQuery.isPending.value && !listQuery.data.value);
const policiesPending = computed(() => policyQuery.isPending.value && !policyQuery.data.value);
const runsPending = computed(() => runQuery.isPending.value && !runQuery.data.value);
const policiesUnreachable = computed(
  () => Boolean(policyQuery.error.value) && !policiesPending.value && !policies.value.length,
);
const runsUnreachable = computed(
  () => Boolean(runQuery.error.value) && !runsPending.value && !runs.value.length,
);
const policiesStale = computed(() => Boolean(policyQuery.error.value) && policies.value.length > 0);
const runsStale = computed(() => Boolean(runQuery.error.value) && runs.value.length > 0);
const showEmptyCatalog = computed(() => !listPending.value && !hasStale.value && !rows.value.length);
const showPeerHint = computed(
  () => !listPending.value && (nodesUnavailable.value || lastPeerNodeIds.value.length === 0),
);

const policyReady = computed(() => {
  if (!policyName.value.trim()) {
    return false;
  }
  if (policySink.value === "s3" && !policyProfile.value.trim()) {
    return false;
  }
  if (policyTargetSelector.value !== "ALL_ADMITTED" && parseLines(policyTargetIds.value).length === 0) {
    return false;
  }
  return true;
});

const restoreOwner = computed(
  () => restoreSnapshot.value?.nodeId || restoreSnapshot.value?.sourceNodeId || "",
);
const restoreReady = computed(() => {
  if (!restoreOpen.value || !restoreOwner.value) {
    return false;
  }
  if (!restoreTargets.value.length) {
    return false;
  }
  return restoreTargets.value.every((target) => target.expectedRevision.trim() !== "");
});

function defaultTimezone(): string {
  return browserTimezone();
}

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function freshnessOf(raw: string | undefined): Freshness {
  if (raw === LIVE || raw === STALE || raw === UNKNOWN) {
    return raw;
  }
  return UNKNOWN;
}

function parseLines(raw: string): string[] {
  return raw
    .split(/[\n,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function parseIntField(raw: string | number, fallback = 0): number {
  const n = typeof raw === "number" ? raw : Number.parseInt(String(raw).trim(), 10);
  return Number.isFinite(n) ? n : fallback;
}

function shortSha(sha: string): string {
  if (!sha) {
    return "—";
  }
  return sha.length > 12 ? sha.slice(0, 12) : sha;
}

function isPartial(status: string | undefined): boolean {
  return (status || "").toUpperCase() === "PARTIAL";
}

function isSuccessStatus(status: string | undefined): boolean {
  const s = (status || "").toUpperCase();
  return s === "SUCCEEDED" || s === "SUCCESS";
}

function unixNumber(unix: bigint | number | undefined): number {
  if (typeof unix === "bigint") {
    const n = Number(unix);
    return Number.isFinite(n) ? n : 0;
  }
  if (typeof unix === "number" && Number.isFinite(unix)) {
    return unix;
  }
  return 0;
}

function latestRunFor(policyId: string): ClusterRun | undefined {
  if (!policyId) {
    return undefined;
  }
  let latest: ClusterRun | undefined;
  let latestUnix = Number.NEGATIVE_INFINITY;
  let latestId = "";
  for (const run of runs.value) {
    if (run.policyId !== policyId) {
      continue;
    }
    const created = unixNumber(run.createdUnix);
    const runId = run.runId ?? "";
    if (!latest || created > latestUnix || (created === latestUnix && runId > latestId)) {
      latest = run;
      latestUnix = created;
      latestId = runId;
    }
  }
  return latest;
}

function nextRunLabel(policy: ClusterPolicy): string {
  if (!policy.scheduleCron) {
    return t("backup.manualOnly");
  }
  const zone = policy.timezone || "UTC";
  return `${policy.scheduleCron} (${zone})`;
}

function formatUnix(unix: bigint | number | undefined): string {
  const n = typeof unix === "bigint" ? Number(unix) : (unix ?? 0);
  if (!Number.isFinite(n) || n <= 0) {
    return "—";
  }
  const ms = n > 1e12 ? n : n * 1000;
  const date = new Date(ms);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  return date.toLocaleString();
}

function formatBytes(bytes: bigint | number | undefined): string {
  if (bytes === undefined || bytes === null) {
    return "—";
  }
  return String(bytes);
}

function targetCount(run: ClusterRun): number {
  if (run.targetNodeIds?.length) {
    return run.targetNodeIds.length;
  }
  return (run.success ?? 0) + (run.failed ?? 0) + (run.unavailable ?? 0) + (run.timeout ?? 0);
}

function canRetryRun(run: ClusterRun | undefined): boolean {
  if (!run || !canManage.value) {
    return false;
  }
  const status = (run.status || "").toUpperCase();
  if (status !== "PARTIAL" && status !== "FAILED") {
    return false;
  }
  return (run.failed ?? 0) + (run.unavailable ?? 0) + (run.timeout ?? 0) > 0;
}

function statusClass(status: string | undefined): string {
  const s = (status || "").toUpperCase();
  if (isSuccessStatus(s)) {
    return "status-success";
  }
  if (s === "PARTIAL" || s === "TIMEOUT") {
    return "status-partial";
  }
  if (s === "FAILED" || s === "UNAVAILABLE" || s === "CANCELED" || s === "CONFIG_MISSING") {
    return "status-failed";
  }
  if (s === "PENDING" || s === "RUNNING") {
    return "status-pending";
  }
  return "status-unknown";
}


function buildPolicyPayload(policyId: string): ClusterPolicy {
  return {
    policyId,
    name: policyName.value.trim(),
    enabled: policyEnabled.value,
    scheduleCron: policyCron.value.trim(),
    timezone: policyTimezone.value.trim() || "UTC",
    targetSelector: policyTargetSelector.value,
    targetNodeIds: policyTargetSelector.value === "ALL_ADMITTED" ? [] : parseLines(policyTargetIds.value),
    sink: policySink.value,
    destinationProfile: policySink.value === "s3" ? policyProfile.value.trim() : "",
    retentionKeepLast: parseIntField(policyKeepLast.value),
    retentionKeepDays: parseIntField(policyKeepDays.value),
    retentionMaxBytes: BigInt(parseIntField(policyMaxBytes.value)),
    timeoutSeconds: parseIntField(policyTimeout.value, 30),
    maxConcurrency: parseIntField(policyConcurrency.value),
    unavailablePolicy: policyUnavailable.value,
    revision: policyRevision.value,
  };
}

function resetPolicyForm(): void {
  editingPolicyId.value = "";
  policyName.value = "";
  policyEnabled.value = true;
  policyCron.value = "";
  policyTimezone.value = defaultTimezone();
  policyTargetSelector.value = "ALL_ADMITTED";
  policyTargetIds.value = "";
  policySink.value = "fs";
  policyProfile.value = "";
  policyKeepLast.value = "7";
  policyKeepDays.value = "0";
  policyMaxBytes.value = "0";
  policyTimeout.value = "30";
  policyConcurrency.value = "0";
  policyUnavailable.value = "RECORD_AND_CONTINUE";
  policyRevision.value = 0n;
}

function fillPolicyForm(policy: ClusterPolicy): void {
  editingPolicyId.value = policy.policyId ?? "";
  policyName.value = policy.name ?? "";
  policyEnabled.value = Boolean(policy.enabled);
  policyCron.value = policy.scheduleCron ?? "";
  policyTimezone.value = policy.timezone || defaultTimezone();
  policyTargetSelector.value = TARGET_SELECTORS.includes(policy.targetSelector as TargetSelector)
    ? (policy.targetSelector as TargetSelector)
    : "ALL_ADMITTED";
  policyTargetIds.value = (policy.targetNodeIds ?? []).join("\n");
  policySink.value = policy.sink === "s3" ? "s3" : "fs";
  policyProfile.value = policy.destinationProfile ?? "";
  policyKeepLast.value = String(policy.retentionKeepLast ?? 7);
  policyKeepDays.value = String(policy.retentionKeepDays ?? 0);
  policyMaxBytes.value = String(policy.retentionMaxBytes ?? 0);
  policyTimeout.value = String(policy.timeoutSeconds ?? 30);
  policyConcurrency.value = String(policy.maxConcurrency ?? 0);
  policyUnavailable.value = UNAVAILABLE_POLICIES.includes(policy.unavailablePolicy as UnavailablePolicy)
    ? (policy.unavailablePolicy as UnavailablePolicy)
    : "RECORD_AND_CONTINUE";
  policyRevision.value = typeof policy.revision === "bigint" ? policy.revision : BigInt(policy.revision ?? 0);
}

function mapEntry(
  entry: {
    snapshot?: {
      snapshotId?: string;
      nodeId?: string;
      sink?: string;
      location?: string;
      sourceNodeId?: string;
      sha256?: string;
      processIds?: string[];
      revisionRanges?: { processId?: string; minRevision?: bigint | number; maxRevision?: bigint | number }[];
    };
    sourceNode?: string;
    freshness?: string;
    lastUpdatedUnixMs?: bigint | number;
  },
  index: number,
) {
  const snapshot = entry.snapshot;
  const freshness = freshnessOf(entry.freshness);
  const lastUpdatedUnixMs = Number(entry.lastUpdatedUnixMs ?? 0);
  return {
    key: snapshot?.snapshotId || `${entry.sourceNode ?? "node"}:${index}`,
    snapshotId: snapshot?.snapshotId || "—",
    sink: snapshot?.sink || "—",
    node: nodeName(snapshot?.nodeId || entry.sourceNode || ""),
    nodeId: snapshot?.nodeId || entry.sourceNode || "",
    processCount: snapshot?.processIds?.length ?? 0,
    sha256: shortSha(snapshot?.sha256 ?? ""),
    freshness,
    lastUpdated: formatAge(Date.now(), lastUpdatedUnixMs),
    canAct: Boolean(canManage.value && snapshot?.snapshotId && canRestoreEntry(entry, snapshot)),
    snapshot: snapshot
      ? {
          snapshotId: snapshot.snapshotId ?? "",
          nodeId: snapshot.nodeId ?? "",
          sink: snapshot.sink ?? "",
          sourceNodeId: snapshot.sourceNodeId || entry.sourceNode || "",
          processIds: snapshot.processIds ?? [],
          revisionRanges: snapshot.revisionRanges ?? [],
        }
      : null,
  };
}

function canRestoreEntry(
  entry: { sourceNode?: string },
  snapshot: { sink?: string; sourceNodeId?: string },
): boolean {
  const sink = snapshot.sink || "";
  if (sink !== "s3") {
    return true;
  }
  const source = entry.sourceNode || snapshot.sourceNodeId || "";
  return source !== "" && source !== "s3";
}

function prefillRevision(
  processId: string,
  ranges: RestoreSnapshot["revisionRanges"],
): string {
  const range = ranges.find((r) => r.processId === processId);
  if (range?.maxRevision === undefined || range.maxRevision === null) {
    return "";
  }
  return String(range.maxRevision);
}

function liveRevision(raw: unknown): string {
  if (raw === undefined || raw === null || raw === "") {
    return "";
  }
  return String(raw);
}

async function fetchLiveRevision(processId: string, ownerId: string, fallback: string): Promise<string> {
  try {
    const res = await processClient.getProcess({ idOrName: processId }, { headers: withTarget(ownerId) });
    const live = liveRevision(res.process?.spec?.latestRevision);
    if (live !== "") {
      return live;
    }
    return fallback;
  } catch {
    return fallback;
  }
}

async function openRestore(snapshot: RestoreSnapshot, expectedRevisionOverride?: string): Promise<void> {
  if (!canManage.value || !snapshot.snapshotId) {
    return;
  }
  actionError.value = "";
  actionNotice.value = "";
  restoreSnapshot.value = snapshot;
  restoreTargets.value = [];
  restoreOpen.value = true;
  const processIds = snapshot.processIds.length ? snapshot.processIds : [];
  const ownerId = snapshot.nodeId || snapshot.sourceNodeId;
  const revisionOverride = expectedRevisionOverride?.trim() ?? "";
  restoreTargets.value = await Promise.all(
    processIds.map(async (processId) => ({
      processId,
      expectedRevision:
        revisionOverride ||
        (await fetchLiveRevision(
          processId,
          ownerId,
          prefillRevision(processId, snapshot.revisionRanges),
        )),
    })),
  );
}

function queryParam(value: unknown): string {
  if (Array.isArray(value)) {
    return String(value[0] ?? "").trim();
  }
  if (value === undefined || value === null) {
    return "";
  }
  return String(value).trim();
}

function snapshotMatchesRestoreQuery(
  row: { snapshotId: string; nodeId: string; snapshot: RestoreSnapshot | null },
  owner: string,
  snapshotId: string,
): boolean {
  if (!row.snapshot || row.snapshot.snapshotId !== snapshotId) {
    return false;
  }
  return [row.snapshot.nodeId, row.snapshot.sourceNodeId, row.nodeId].includes(owner);
}

// Disaster-replica deep links open Owner restore; peer replicas are not auto-applied
// and Owner restore still reads FS/S3 (not a peer payload pull).
async function applyRestoreQuery(): Promise<void> {
  const owner = queryParam(route.query.owner);
  const snapshotId = queryParam(route.query.snapshot);
  const key = `${owner}\0${snapshotId}`;
  if (!owner || !snapshotId || restoreQueryApplied.value === key) {
    return;
  }
  if (!listQuery.isSuccess.value) {
    return;
  }
  const row = rows.value.find((entry) => snapshotMatchesRestoreQuery(entry, owner, snapshotId));
  if (!row?.snapshot) {
    restoreQueryApplied.value = key;
    return;
  }
  restoreQueryApplied.value = key;
  await openRestore(
    { ...row.snapshot, nodeId: row.snapshot.nodeId || owner },
    queryParam(route.query.expectedRevision),
  );
}

watch(
  () => [
    listQuery.isSuccess.value,
    queryParam(route.query.owner),
    queryParam(route.query.snapshot),
    queryParam(route.query.expectedRevision),
    rows.value.map((row) => row.key).join("|"),
  ],
  () => {
    void applyRestoreQuery();
  },
  { immediate: true },
);

function closeRestore(): void {
  restoreOpen.value = false;
  restoreSnapshot.value = null;
  restoreTargets.value = [];
}

function openCreatePolicy(): void {
  if (!canManage.value) {
    return;
  }
  createOpen.value = false;
  selectedRunId.value = "";
  clusterError.value = "";
  policyAdvancedOpen.value = false;
  resetPolicyForm();
  policyOpen.value = true;
}

function openEditPolicy(policy: ClusterPolicy): void {
  if (!canManage.value || !policy.policyId) {
    return;
  }
  createOpen.value = false;
  selectedRunId.value = "";
  clusterError.value = "";
  fillPolicyForm(policy);
  policyOpen.value = true;
}

function closePolicy(): void {
  if (createPolicyMut.isPending.value || updatePolicyMut.isPending.value) {
    return;
  }
  policyOpen.value = false;
  policyAdvancedOpen.value = false;
  resetPolicyForm();
}

function resetCreateForm(): void {
  createSink.value = "fs";
  createScope.value = "all";
  createProcessIds.value = [];
  createProcessSearch.value = "";
}

function openCreate(): void {
  if (!canManage.value) {
    return;
  }
  policyOpen.value = false;
  selectedRunId.value = "";
  actionError.value = "";
  actionNotice.value = "";
  resetCreateForm();
  createOpen.value = true;
}

function closeCreate(): void {
  createOpen.value = false;
  resetCreateForm();
}

function onAdvancedToggle(event: Event): void {
  const target = event.currentTarget;
  policyAdvancedOpen.value = target instanceof HTMLDetailsElement && target.open;
}

function toggleCreateProcess(processId: string, checked: boolean): void {
  const next = new Set(createProcessIds.value);
  if (checked) {
    next.add(processId);
  } else {
    next.delete(processId);
  }
  createProcessIds.value = [...next];
}

function onCreateProcessChange(processId: string, event: Event): void {
  toggleCreateProcess(processId, (event.target as HTMLInputElement).checked);
}

function selectAllVisibleProcesses(): void {
  const next = new Set(createProcessIds.value);
  for (const row of filteredLocalProcesses.value) {
    next.add(row.processId);
  }
  createProcessIds.value = [...next];
}

function processMeta(row: { group: string; observed: string; desired: string }): string {
  const status = row.observed || row.desired || "—";
  return row.group ? `${row.group} · ${status}` : status;
}

function onRunKeydown(event: KeyboardEvent, runId: string): void {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    openRun(runId);
  }
}

function formatBackupActionError(err: unknown): string {
  const detail = formatRemoteError(err);
  if (DISK_PROTECTION_RE.test(detail)) {
    return t("backup.createDiskFull");
  }
  return detail;
}

function notify(message: string, type: "success" | "error" | "info" | "warning" = "success"): void {
  toastMessage.value = message;
  toastType.value = type;
  showToast.value = true;
}

function openRun(runId: string): void {
  if (!runId) {
    return;
  }
  createOpen.value = false;
  policyOpen.value = false;
  selectedRunId.value = runId;
}

function closeRun(): void {
  selectedRunId.value = "";
}

async function invalidateCluster(): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ["cluster-backup-policies"] }),
    queryClient.invalidateQueries({ queryKey: ["cluster-backup-runs"] }),
    queryClient.invalidateQueries({ queryKey: ["cluster-backup-run"] }),
    queryClient.invalidateQueries({ queryKey: ["cluster-backup-destination-health"] }),
  ]);
}

const createMut = useMutation({
  mutationFn: () =>
    client.createBackup({
      meta: mutationMeta(),
      sink: createSink.value,
      processIds: createScope.value === "all" ? [] : [...createProcessIds.value],
      targetNodeIds: [],
    }),
  onSuccess: async () => {
    actionError.value = "";
    actionNotice.value = "";
    createOpen.value = false;
    resetCreateForm();
    notify(t("backup.createSuccess"));
    await queryClient.invalidateQueries({ queryKey: ["backups"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatBackupActionError(err);
  },
});

const deleteMut = useMutation({
  mutationFn: (row: { snapshotId: string; sink: string; sourceNodeId: string }) =>
    client.deleteBackup({
      meta: mutationMeta(),
      snapshotId: row.snapshotId,
      sink: row.sink,
      sourceNodeId: row.sourceNodeId,
    }),
  onSuccess: async () => {
    await queryClient.invalidateQueries({ queryKey: ["backups"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const restoreMut = useMutation({
  mutationFn: () => {
    const snap = restoreSnapshot.value;
    if (!snap) {
      throw new Error("missing snapshot");
    }
    return client.restoreBackup({
      meta: mutationMeta(),
      snapshotId: snap.snapshotId,
      sink: snap.sink,
      sourceNodeId: snap.sourceNodeId,
      targets: restoreTargets.value.map((target) => ({
        processId: target.processId,
        expectedRevision: BigInt(target.expectedRevision.trim()),
      })),
    });
  },
  onSuccess: async (res) => {
    const results = res.results ?? [];
    const conflicts = results.filter((r) => (r.status || "").toUpperCase() === "CONFLICT");
    const failures = results.filter((r) => {
      const status = (r.status || "").toUpperCase();
      return status && status !== "SUCCESS";
    });
    if (conflicts.length) {
      actionNotice.value = "";
      actionError.value = t("backup.restoreConflict", {
        detail: conflicts.map((r) => `${r.processId}: ${r.error || r.status}`).join("; "),
      });
      return;
    }
    if (failures.length) {
      actionNotice.value = "";
      actionError.value = t("backup.restoreFailed", {
        detail: failures.map((r) => `${r.processId}: ${r.error || r.status}`).join("; "),
      });
      return;
    }
    actionError.value = "";
    actionNotice.value = t("backup.restoreSuccess");
    closeRestore();
    await queryClient.invalidateQueries({ queryKey: ["backups"] });
  },
  onError: (err: unknown) => {
    actionNotice.value = "";
    actionError.value = formatRemoteError(err);
  },
});

const createPolicyMut = useMutation({
  mutationFn: () =>
    clusterClient.createPolicy({
      meta: mutationMeta(),
      policy: buildPolicyPayload(newOperationId()),
    }),
  onSuccess: async () => {
    policyOpen.value = false;
    policyAdvancedOpen.value = false;
    resetPolicyForm();
    notify(t("backup.createPolicy"));
    await invalidateCluster();
  },
  onError: (err: unknown) => {
    clusterError.value = formatRemoteError(err);
  },
});

const updatePolicyMut = useMutation({
  mutationFn: () =>
    clusterClient.updatePolicy({
      meta: mutationMeta(),
      policy: buildPolicyPayload(editingPolicyId.value),
    }),
  onSuccess: async () => {
    policyOpen.value = false;
    policyAdvancedOpen.value = false;
    resetPolicyForm();
    notify(t("backup.savePolicy"));
    await invalidateCluster();
  },
  onError: (err: unknown) => {
    clusterError.value = formatRemoteError(err);
  },
});

const deletePolicyMut = useMutation({
  mutationFn: (policyId: string) =>
    clusterClient.deletePolicy({
      meta: mutationMeta(),
      policyId,
    }),
  onSuccess: async () => {
    await invalidateCluster();
  },
  onError: (err: unknown) => {
    clusterError.value = formatRemoteError(err);
  },
});

const startRunMut = useMutation({
  mutationFn: (policyId: string) =>
    clusterClient.startRun({
      meta: mutationMeta(),
      policyId,
    }),
  onSuccess: async () => {
    await invalidateCluster();
  },
  onError: (err: unknown) => {
    clusterError.value = formatRemoteError(err);
  },
});

const retryMut = useMutation({
  mutationFn: (runId: string) =>
    clusterClient.retryFailedTasks({
      meta: mutationMeta(),
      runId,
    }),
  onSuccess: async () => {
    await invalidateCluster();
  },
  onError: (err: unknown) => {
    clusterError.value = formatRemoteError(err);
  },
});

const acting = computed(
  () =>
    createMut.isPending.value ||
    deleteMut.isPending.value ||
    restoreMut.isPending.value ||
    createPolicyMut.isPending.value ||
    updatePolicyMut.isPending.value ||
    deletePolicyMut.isPending.value ||
    startRunMut.isPending.value ||
    retryMut.isPending.value,
);

function onRestoreKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape" && restoreOpen.value && !acting.value) {
    closeRestore();
  }
}

onMounted(() => document.addEventListener("keydown", onRestoreKeydown));
onUnmounted(() => document.removeEventListener("keydown", onRestoreKeydown));

async function onCreate(): Promise<void> {
  if (!canManage.value || !createReady.value || acting.value) {
    return;
  }
  actionError.value = "";
  actionNotice.value = "";
  try {
    await createMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

function requestDeleteSnapshot(snapshot: RestoreSnapshot): void {
  if (!canManage.value || acting.value) {
    return;
  }
  pendingDelete.value = { kind: "snapshot", snapshot };
}

async function onDelete(row: { snapshot: RestoreSnapshot | null }): Promise<void> {
  if (!row.snapshot) {
    return;
  }
  requestDeleteSnapshot(row.snapshot);
}

async function onConfirmRestore(): Promise<void> {
  if (!canManage.value || !restoreReady.value || acting.value) {
    return;
  }
  actionError.value = "";
  actionNotice.value = "";
  try {
    await restoreMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onSavePolicy(): Promise<void> {
  if (!canManage.value || !policyReady.value || acting.value) {
    return;
  }
  clusterError.value = "";
  try {
    if (editingPolicyId.value) {
      await updatePolicyMut.mutateAsync();
    } else {
      await createPolicyMut.mutateAsync();
    }
  } catch {
    // onError already recorded
  }
}

async function onDeletePolicy(policy: ClusterPolicy): Promise<void> {
  if (!canManage.value || !policy.policyId || acting.value) {
    return;
  }
  pendingDelete.value = { kind: "policy", policy };
}

const deleteDialogTitle = computed(() => {
  if (pendingDelete.value?.kind === "policy") {
    return t("backup.deletePolicyTitle");
  }
  return t("backup.deleteSnapshotTitle");
});

const deleteDialogMessage = computed(() => {
  const pending = pendingDelete.value;
  if (pending?.kind === "policy") {
    return t("backup.deletePolicyMessage", { name: pending.policy.name ?? pending.policy.policyId });
  }
  if (pending?.kind === "snapshot") {
    return t("backup.deleteSnapshotMessage", { id: pending.snapshot.snapshotId });
  }
  return "";
});

const deleteDialogPending = computed(
  () => deleteMut.isPending.value || deletePolicyMut.isPending.value,
);

function cancelPendingDelete(): void {
  if (deleteDialogPending.value) {
    return;
  }
  pendingDelete.value = null;
}

async function confirmPendingDelete(): Promise<void> {
  const pending = pendingDelete.value;
  if (!pending || acting.value) {
    return;
  }
  if (pending.kind === "snapshot") {
    actionError.value = "";
    actionNotice.value = "";
    try {
      await deleteMut.mutateAsync({
        snapshotId: pending.snapshot.snapshotId,
        sink: pending.snapshot.sink,
        sourceNodeId: pending.snapshot.sourceNodeId,
      });
      pendingDelete.value = null;
    } catch {
      pendingDelete.value = null;
    }
    return;
  }
  if (!pending.policy.policyId) {
    pendingDelete.value = null;
    return;
  }
  clusterError.value = "";
  try {
    await deletePolicyMut.mutateAsync(pending.policy.policyId);
    pendingDelete.value = null;
  } catch {
    pendingDelete.value = null;
  }
}

async function onStartRun(policyId: string): Promise<void> {
  if (!canManage.value || !policyId || acting.value) {
    return;
  }
  clusterError.value = "";
  try {
    await startRunMut.mutateAsync(policyId);
  } catch {
    // onError already recorded
  }
}

async function onRetryFailed(): Promise<void> {
  const runId = selectedRun.value?.runId || selectedRunId.value;
  if (!canRetryRun(selectedRun.value) || !runId || acting.value) {
    return;
  }
  clusterError.value = "";
  try {
    await retryMut.mutateAsync(runId);
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1>{{ t("backup.title") }}</h1>
        <p class="subtitle">{{ t("backup.subtitle") }}</p>
      </div>
      <div v-if="canManage" class="header-actions">
        <button
          type="button"
          class="btn"
          data-action="open-create"
          :disabled="acting"
          @click="openCreate"
        >
          <Plus :size="18" aria-hidden="true" />
          {{ t("backup.create") }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          data-action="create-policy"
          :disabled="acting"
          @click="openCreatePolicy"
        >
          <Plus :size="18" aria-hidden="true" />
          {{ t("backup.createPolicy") }}
        </button>
      </div>
    </header>

    <div v-if="showFsPageWarning" class="banner warning-banner" data-fs-warning role="status">
      <TriangleAlert :size="18" aria-hidden="true" />
      <span>{{ t("backup.fsHostLossWarning") }}</span>
    </div>
    <div v-if="hasStale" class="banner backup-stale-banner" role="status">{{ t("backup.staleBanner") }}</div>
    <p v-if="showPeerHint" class="muted">{{ t("backup.peerHint") }}</p>
    <p v-if="clusterErrorText" class="error" role="alert">{{ clusterErrorText }}</p>
    <p v-if="actionNotice" class="notice" role="status">{{ actionNotice }}</p>

    <section class="section" data-section="policies">
      <div class="section-header">
        <div>
          <h2>{{ t("backup.policies") }}</h2>
          <p class="section-hint">{{ t("backup.policiesHint") }}</p>
        </div>
        <FreshnessBadge v-if="policiesStale" :status="STALE" />
      </div>
      <p v-if="policiesPending" class="muted">{{ t("backup.loading") }}</p>
      <div
        v-else-if="policiesUnreachable"
        class="banner warning-banner"
        data-policies-unreachable
        role="status"
      >
        <FreshnessBadge :status="UNKNOWN" />
        {{ t("backup.policiesUnreachable") }}
      </div>
      <div v-else class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("backup.policyName") }}</th>
              <th>{{ t("backup.enabled") }}</th>
              <th>{{ t("backup.sink") }}</th>
              <th>{{ t("backup.nextRun") }}</th>
              <th>{{ t("backup.latestRun") }}</th>
              <th>{{ t("backup.retention") }}</th>
              <th v-if="canManage"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in policyRows" :key="row.policy.policyId || row.policy.name">
              <td>{{ row.policy.name }}</td>
              <td>{{ row.policy.enabled ? t("backup.enabled") : t("backup.disabled") }}</td>
              <td>{{ row.policy.sink || "—" }}</td>
              <td class="mono">{{ row.nextRun }}</td>
              <td>
                <span
                  class="status-badge"
                  data-latest-run
                  :class="statusClass(row.latestStatus)"
                  :data-status="row.latestStatus"
                >{{ row.latestStatus }}</span>
              </td>
              <td>{{ row.retention }}</td>
              <td v-if="canManage">
                <div class="row-actions">
                  <button
                    type="button"
                    class="btn btn-xs"
                    data-action="start-run"
                    :disabled="acting"
                    @click="onStartRun(row.policy.policyId || '')"
                  >
                    {{ t("backup.startRun") }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-xs"
                    data-action="edit-policy"
                    :disabled="acting"
                    @click="openEditPolicy(row.policy)"
                  >
                    {{ t("backup.editPolicy") }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-xs btn-danger"
                    data-action="delete-policy"
                    :disabled="acting"
                    @click="onDeletePolicy(row.policy)"
                  >
                    {{ t("backup.deletePolicy") }}
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!policyRows.length">
              <td :colspan="canManage ? 7 : 6">
                <div class="empty-state">
                  <HardDrive :size="28" aria-hidden="true" />
                  <strong>{{ t("backup.noPolicies") }}</strong>
                  <span>{{ t("backup.emptyPoliciesHint") }}</span>
                  <button
                    v-if="canManage"
                    type="button"
                    class="btn btn-primary"
                    :disabled="acting"
                    @click="openCreatePolicy"
                  >
                    <Plus :size="18" aria-hidden="true" />
                    {{ t("backup.createPolicy") }}
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="section" data-section="runs">
      <div class="section-header">
        <div>
          <h2>{{ t("backup.runs") }}</h2>
          <p class="section-hint">{{ t("backup.runsHint") }}</p>
        </div>
        <FreshnessBadge v-if="runsStale" :status="STALE" />
      </div>
      <div v-if="hasPartialRun" class="banner warning-banner" data-partial-warning role="status">
        {{ t("backup.partialWarning") }}
      </div>
      <p v-if="runsPending" class="muted">{{ t("backup.loading") }}</p>
      <div
        v-else-if="runsUnreachable"
        class="banner warning-banner"
        data-runs-unreachable
        role="status"
      >
        <FreshnessBadge :status="UNKNOWN" />
        {{ t("backup.runsUnreachable") }}
      </div>
      <div v-else class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("backup.runId") }}</th>
              <th>{{ t("backup.targetCount") }}</th>
              <th>{{ t("backup.successCount") }}</th>
              <th>{{ t("backup.failedCount") }}</th>
              <th>{{ t("backup.unavailableCount") }}</th>
              <th>{{ t("backup.started") }}</th>
              <th>{{ t("backup.finished") }}</th>
              <th>{{ t("backup.latestRun") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="run in runs"
              :key="run.runId"
              class="clickable"
              :data-run-id="run.runId"
              tabindex="0"
              :aria-label="t('backup.openRun', { id: run.runId || '' })"
              @click="openRun(run.runId || '')"
              @keydown="onRunKeydown($event, run.runId || '')"
            >
              <td class="mono">{{ run.runId }}</td>
              <td>{{ targetCount(run) }}</td>
              <td>{{ run.success ?? 0 }}</td>
              <td>{{ run.failed ?? 0 }}</td>
              <td>{{ run.unavailable ?? 0 }}</td>
              <td>{{ formatUnix(run.startedUnix) }}</td>
              <td>{{ formatUnix(run.finishedUnix) }}</td>
              <td>
                <span
                  class="status-badge"
                  :class="statusClass(run.status)"
                  :data-status="run.status"
                >{{ run.status }}</span>
              </td>
            </tr>
            <tr v-if="!runs.length">
              <td colspan="8">
                <div class="empty-state">
                  <strong>{{ t("backup.noRuns") }}</strong>
                  <span>{{ t("backup.emptyRunsHint") }}</span>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section v-if="healthRows.length" class="section" data-section="destination-health">
      <div class="section-header">
        <div>
          <h2>{{ t("backup.destinationHealth") }}</h2>
          <p class="section-hint">{{ t("backup.destinationHealthHint") }}</p>
        </div>
      </div>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("backup.destinationProfile") }}</th>
              <th>{{ t("backup.endpointHost") }}</th>
              <th>{{ t("backup.healthStatus") }}</th>
              <th>{{ t("backup.sink") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="health in healthRows" :key="`${health.sink}:${health.destinationProfile}`">
              <td>{{ health.destinationProfile || "—" }}</td>
              <td class="mono">{{ health.endpointHost || "—" }}</td>
              <td>
                <span
                  class="status-badge"
                  :class="statusClass(health.status)"
                  :data-status="health.status"
                >{{ health.status }}</span>
              </td>
              <td>{{ health.sink || "—" }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <p v-if="listPending" class="muted">{{ t("backup.loading") }}</p>
    <p v-else-if="errorText && !listQuery.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <section class="section" data-section="snapshots">
        <div class="section-header">
          <div>
            <h2>{{ t("backup.snapshots") }}</h2>
            <p class="section-hint">{{ t("backup.snapshotsHint") }}</p>
          </div>
        </div>
        <div class="card">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t("backup.snapshotId") }}</th>
                <th>{{ t("backup.sink") }}</th>
                <th>{{ t("backup.node") }}</th>
                <th>{{ t("backup.processCount") }}</th>
                <th>{{ t("backup.sha256") }}</th>
                <th>{{ t("backup.freshness") }}</th>
                <th>{{ t("backup.lastUpdated") }}</th>
                <th v-if="canManage"></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in rows"
                :key="row.key"
                :data-freshness="row.freshness"
                :class="{ 'row-stale': row.freshness === 'STALE' }"
              >
                <td class="mono">{{ row.snapshotId }}</td>
                <td>{{ row.sink }}</td>
                <td>{{ row.node }}</td>
                <td>{{ row.processCount }}</td>
                <td class="mono">{{ row.sha256 }}</td>
                <td><FreshnessBadge :status="row.freshness" /></td>
                <td>{{ row.lastUpdated }}</td>
                <td v-if="canManage">
                  <div v-if="row.canAct" class="row-actions">
                    <button type="button" class="btn btn-xs" data-action="restore" :disabled="acting" @click="openRestore(row.snapshot!)">
                      {{ t("backup.restore") }}
                    </button>
                    <button type="button" class="btn btn-xs btn-danger" data-action="delete" :disabled="acting" @click="onDelete(row)">
                      {{ t("backup.delete") }}
                    </button>
                  </div>
                </td>
              </tr>
              <tr v-if="showEmptyCatalog">
                <td :colspan="canManage ? 8 : 7" class="empty-catalog">
                  <div class="empty-state">
                    <HardDrive :size="28" aria-hidden="true" />
                    <strong>{{ t("backup.noBackups") }}</strong>
                    <span>{{ t("backup.emptySnapshotsHint") }}</span>
                    <button
                      v-if="canManage"
                      type="button"
                      class="btn btn-primary"
                      :disabled="acting"
                      @click="openCreate"
                    >
                      <Plus :size="18" aria-hidden="true" />
                      {{ t("backup.create") }}
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <Drawer
      :open="createOpen"
      :title="t('backup.create')"
      :close-label="t('actions.close')"
      @close="closeCreate"
    >
      <form class="drawer-form create-backup" @submit.prevent="onCreate">
        <p class="drawer-lead">{{ t("backup.createHint") }}</p>
        <p v-if="actionError && createOpen" class="error-banner" role="alert" data-error="create">
          <TriangleAlert :size="18" aria-hidden="true" />
          <span>{{ actionError }}</span>
        </p>

        <fieldset class="field">
          <legend>
            {{ t("backup.sink") }}
            <span class="required" aria-hidden="true">{{ t("backup.requiredMarker") }}</span>
          </legend>
          <p class="field-hint">{{ t("backup.sinkHint") }}</p>
          <select v-model="createSink" class="sr-only" name="sink" tabindex="-1" aria-hidden="true">
            <option v-for="sink in SINKS" :key="sink" :value="sink">{{ sink }}</option>
          </select>
          <div class="choice-grid" role="radiogroup" :aria-label="t('backup.sink')">
            <button
              type="button"
              class="choice-card"
              :class="{ selected: createSink === 'fs' }"
              role="radio"
              :aria-checked="createSink === 'fs'"
              @click="createSink = 'fs'"
            >
              <HardDrive :size="18" aria-hidden="true" />
              <span class="choice-copy">
                <span class="choice-title">{{ t("backup.sinkFs") }}</span>
                <span class="choice-hint">{{ t("backup.sinkFsHint") }}</span>
              </span>
            </button>
            <button
              type="button"
              class="choice-card"
              :class="{ selected: createSink === 's3' }"
              role="radio"
              :aria-checked="createSink === 's3'"
              @click="createSink = 's3'"
            >
              <Database :size="18" aria-hidden="true" />
              <span class="choice-copy">
                <span class="choice-title">{{ t("backup.sinkS3") }}</span>
                <span class="choice-hint">{{ t("backup.sinkS3Hint") }}</span>
              </span>
            </button>
          </div>
          <p v-if="createSink === 'fs'" class="field-warning" data-create-fs-warning role="status">
            {{ t("backup.fsHostLossWarning") }}
          </p>
        </fieldset>

        <fieldset class="field">
          <legend>{{ t("backup.processScope") }}</legend>
          <div class="choice-grid compact-grid" role="radiogroup" :aria-label="t('backup.processScope')">
            <button
              type="button"
              class="choice-card compact"
              :class="{ selected: createScope === 'all' }"
              role="radio"
              :aria-checked="createScope === 'all'"
              @click="createScope = 'all'"
            >
              <span class="choice-title">{{ t("backup.allLocalProcesses") }}</span>
              <span class="choice-hint">{{ t("backup.allLocalProcessesHint") }}</span>
            </button>
            <button
              type="button"
              class="choice-card compact"
              :class="{ selected: createScope === 'selected' }"
              role="radio"
              :aria-checked="createScope === 'selected'"
              @click="createScope = 'selected'"
            >
              <span class="choice-title">{{ t("backup.selectedProcesses") }}</span>
              <span class="choice-hint">{{ t("backup.selectedProcessesHint") }}</span>
            </button>
          </div>
        </fieldset>

        <div v-if="createScope === 'selected'" class="process-picker">
          <p v-if="localProcessesPending" class="picker-message">{{ t("backup.loading") }}</p>
          <p v-else-if="localProcessesError" class="picker-message error" role="alert">
            {{ t("backup.loadProcessesError", { error: localProcessesError }) }}
          </p>
          <template v-else>
            <label class="search-field">
              <span class="sr-only">{{ t("backup.searchProcesses") }}</span>
              <span class="search-input-wrap">
                <Search :size="16" aria-hidden="true" />
                <input
                  v-model="createProcessSearch"
                  class="input search-input"
                  name="processSearch"
                  type="search"
                  :placeholder="t('backup.searchProcesses')"
                  :disabled="acting"
                  autocomplete="off"
                />
              </span>
            </label>
            <div class="picker-toolbar">
              <span>{{ t("backup.selectedCount", { count: createProcessIds.length }) }}</span>
              <div class="picker-toolbar-actions">
                <button
                  type="button"
                  class="link-btn"
                  :disabled="acting || !filteredLocalProcesses.length || visibleAllSelected"
                  @click="selectAllVisibleProcesses"
                >
                  {{ t("backup.selectAllVisible") }}
                </button>
                <button
                  type="button"
                  class="link-btn"
                  :disabled="acting || !createProcessIds.length"
                  @click="createProcessIds = []"
                >
                  {{ t("backup.clearSelection") }}
                </button>
              </div>
            </div>
            <div v-if="selectedProcessRows.length" class="chip-row">
              <span v-for="row in selectedProcessRows" :key="row.processId" class="chip">
                {{ row.name || row.processId }}
                <button
                  type="button"
                  class="chip-remove"
                  :disabled="acting"
                  :aria-label="t('backup.clearSelection')"
                  @click="toggleCreateProcess(row.processId, false)"
                >
                  <X :size="14" aria-hidden="true" />
                </button>
              </span>
            </div>
            <fieldset class="option-list" :aria-label="t('backup.processIds')">
              <label
                v-for="row in filteredLocalProcesses"
                :key="row.processId"
                class="option-row"
                :class="{ selected: selectedProcessSet.has(row.processId) }"
              >
                <input
                  type="checkbox"
                  name="processId"
                  :value="row.processId"
                  :checked="selectedProcessSet.has(row.processId)"
                  :disabled="acting"
                  :data-process-id="row.processId"
                  @change="onCreateProcessChange(row.processId, $event)"
                />
                <span class="option-copy">
                  <strong>{{ row.name || row.processId }}</strong>
                  <span>{{ processMeta(row) }}</span>
                </span>
              </label>
              <p v-if="!localProcesses.length" class="picker-message">{{ t("backup.noLocalProcesses") }}</p>
              <p v-else-if="!filteredLocalProcesses.length" class="picker-message">{{ t("backup.noProcessMatch") }}</p>
            </fieldset>
          </template>
        </div>

        <div class="drawer-actions">
          <button type="button" class="btn" :disabled="acting" @click="closeCreate">{{ t("actions.cancel") }}</button>
          <button class="btn btn-primary" type="submit" data-action="create" :disabled="!createReady || acting">
            <LoaderCircle v-if="acting" class="spin" :size="16" aria-hidden="true" />
            {{ t("backup.create") }}
          </button>
        </div>
      </form>
    </Drawer>

    <div v-if="restoreOpen && restoreSnapshot" class="restore-backdrop" data-restore-dialog>
      <section class="restore-panel" role="dialog" :aria-modal="true">
        <h2>{{ t("backup.restoreConfirm") }}</h2>
        <dl class="facts">
          <div>
            <dt>{{ t("backup.snapshotId") }}</dt>
            <dd class="mono">{{ restoreSnapshot.snapshotId }}</dd>
          </div>
          <div data-restore-owner>
            <dt>{{ t("backup.owner") }}</dt>
            <dd class="mono">{{ restoreSnapshot.nodeId }}</dd>
          </div>
          <div>
            <dt>{{ t("backup.sink") }}</dt>
            <dd>{{ restoreSnapshot.sink }}</dd>
          </div>
        </dl>
        <div v-for="target in restoreTargets" :key="target.processId" class="field">
          <label>
            {{ t("backup.process") }} {{ target.processId }} — {{ t("backup.expectedRevision") }}
            <input
              v-model="target.expectedRevision"
              class="input"
              name="expectedRevision"
              type="text"
              :inputmode="NUMERIC_INPUT_MODE"
              autocomplete="off"
            />
          </label>
        </div>
        <div class="restore-actions">
          <button type="button" class="btn" :disabled="acting" @click="closeRestore">
            {{ t("backup.cancel") }}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            data-action="confirm-restore"
            :disabled="!restoreReady || acting"
            @click="onConfirmRestore"
          >
            {{ t("backup.confirm") }}
          </button>
        </div>
      </section>
    </div>

    <Drawer
      :open="policyOpen"
      :title="editingPolicyId ? t('backup.editPolicy') : t('backup.createPolicy')"
      :close-label="t('actions.close')"
      @close="closePolicy"
    >
      <form class="drawer-form policy-form" @submit.prevent="onSavePolicy">
        <p v-if="clusterError" class="error" role="alert">{{ clusterError }}</p>

        <fieldset class="form-section">
          <legend>{{ t("backup.basics") }}</legend>
          <label class="field">
            <span>
              {{ t("backup.policyName") }}
              <span class="required" aria-hidden="true">{{ t("backup.requiredMarker") }}</span>
            </span>
            <input v-model="policyName" class="input" name="policyName" type="text" autocomplete="off" />
          </label>
          <label class="field checkbox">
            <input v-model="policyEnabled" name="policyEnabled" type="checkbox" />
            {{ t("backup.enabled") }}
          </label>
        </fieldset>

        <fieldset class="form-section">
          <legend>{{ t("backup.targetSet") }}</legend>
          <p class="field-hint">{{ t("backup.targetSelectorHint") }}</p>
          <label class="field">
            {{ t("backup.targetSelector") }}
            <select v-model="policyTargetSelector" class="input" name="targetSelector">
              <option value="ALL_ADMITTED">{{ t("backup.targetAllAdmitted") }}</option>
              <option value="AGENT_GROUP">{{ t("backup.targetAgentGroup") }}</option>
              <option value="EXPLICIT_NODES">{{ t("backup.targetExplicitNodes") }}</option>
            </select>
          </label>
          <label v-if="policyTargetSelector !== 'ALL_ADMITTED'" class="field">
            {{ t("backup.targetNodeIds") }}
            <textarea
              v-model="policyTargetIds"
              class="input textarea"
              name="targetNodeIds"
              rows="3"
              :placeholder="t('backup.targetNodeIdsPlaceholder')"
            />
          </label>
        </fieldset>

        <fieldset class="form-section">
          <legend>{{ t("backup.destination") }}</legend>
          <p class="field-hint">{{ t("backup.sinkHint") }}</p>
          <label class="field">
            {{ t("backup.sink") }}
            <select v-model="policySink" class="input" name="policySink">
              <option v-for="sink in POLICY_SINKS" :key="sink" :value="sink">{{ sink }}</option>
            </select>
          </label>
          <p v-if="policySink === 'fs'" class="field-warning">{{ t("backup.fsHostLossWarning") }}</p>
          <label v-if="policySink === 's3'" class="field">
            {{ t("backup.destinationProfile") }}
            <input v-model="policyProfile" class="input" name="destinationProfile" type="text" autocomplete="off" />
            <span class="field-hint">{{ t("backup.profileHint") }}</span>
          </label>
        </fieldset>

        <fieldset class="form-section">
          <legend>{{ t("backup.schedule") }}</legend>
          <label class="field">
            {{ t("backup.scheduleCron") }}
            <input v-model="policyCron" class="input" name="scheduleCron" type="text" autocomplete="off" />
            <span class="field-hint">{{ t("backup.scheduleHint") }}</span>
          </label>
          <label class="field">
            {{ t("backup.timezone") }}
            <select
              v-model="policyTimezone"
              class="input"
              name="timezone"
              aria-describedby="backup-timezone-hint"
            >
              <optgroup :label="t('backup.timezoneSuggested')">
                <option :value="policyTimezonePicker.browser">
                  {{ t("backup.timezoneBrowser", { zone: policyTimezonePicker.browser }) }}
                </option>
                <option
                  v-for="zone in policyTimezonePicker.suggested"
                  :key="`suggested-${zone}`"
                  :value="zone"
                >
                  {{ timezoneLabel(zone) }}
                </option>
              </optgroup>
              <optgroup :label="t('backup.timezoneAll')">
                <option v-for="zone in policyTimezonePicker.remaining" :key="zone" :value="zone">
                  {{ timezoneLabel(zone) }}
                </option>
              </optgroup>
            </select>
            <span id="backup-timezone-hint" class="field-hint">{{ t("backup.timezoneHint") }}</span>
          </label>
        </fieldset>

        <fieldset class="form-section">
          <legend>{{ t("backup.retention") }}</legend>
          <p class="field-hint">{{ t("backup.retentionHint") }}</p>
          <label class="field">
            {{ t("backup.retentionKeepLast") }}
            <input v-model="policyKeepLast" class="input" name="retentionKeepLast" type="number" min="0" />
          </label>
          <label class="field">
            {{ t("backup.retentionKeepDays") }}
            <input v-model="policyKeepDays" class="input" name="retentionKeepDays" type="number" min="0" />
          </label>
          <label class="field">
            {{ t("backup.retentionMaxBytes") }}
            <input v-model="policyMaxBytes" class="input" name="retentionMaxBytes" type="number" min="0" />
          </label>
        </fieldset>

        <details class="advanced" :open="policyAdvancedOpen" @toggle="onAdvancedToggle">
          <summary>
            <ChevronDown :size="16" aria-hidden="true" />
            {{ t("backup.advanced") }}
          </summary>
          <label class="field">
            {{ t("backup.timeoutSeconds") }}
            <input v-model="policyTimeout" class="input" name="timeoutSeconds" type="number" min="0" />
          </label>
          <label class="field">
            {{ t("backup.maxConcurrency") }}
            <input v-model="policyConcurrency" class="input" name="maxConcurrency" type="number" min="0" />
          </label>
          <label class="field">
            {{ t("backup.unavailablePolicy") }}
            <select v-model="policyUnavailable" class="input" name="unavailablePolicy">
              <option value="RECORD_AND_CONTINUE">{{ t("backup.unavailableRecordContinue") }}</option>
              <option value="FAIL_FAST">{{ t("backup.unavailableFailFast") }}</option>
            </select>
          </label>
        </details>

        <div class="drawer-actions">
          <button type="button" class="btn" :disabled="acting" @click="closePolicy">{{ t("actions.cancel") }}</button>
          <button class="btn btn-primary" type="submit" :disabled="!policyReady || acting">
            <LoaderCircle v-if="acting" class="spin" :size="16" aria-hidden="true" />
            {{ editingPolicyId ? t("backup.savePolicy") : t("backup.createPolicy") }}
          </button>
        </div>
      </form>
    </Drawer>

    <Drawer
      :open="runDetailOpen"
      :title="t('backup.runDetail')"
      :close-label="t('actions.close')"
      size="wide"
      @close="closeRun"
    >
      <div v-if="selectedRun" class="run-detail" data-run-detail>
        <p v-if="isPartial(selectedRun.status)" class="banner warning-banner" role="status">
          {{ t("backup.partialWarning") }}
        </p>
        <dl class="facts">
          <div>
            <dt>{{ t("backup.runId") }}</dt>
            <dd class="mono">{{ selectedRun.runId }}</dd>
          </div>
          <div>
            <dt>{{ t("backup.latestRun") }}</dt>
            <dd>
              <span
                class="status-badge"
                :class="statusClass(selectedRun.status)"
                :data-status="selectedRun.status"
              >{{ selectedRun.status }}</span>
            </dd>
          </div>
          <div>
            <dt>{{ t("backup.targetCount") }}</dt>
            <dd>{{ targetCount(selectedRun) }}</dd>
          </div>
          <div>
            <dt>{{ t("backup.successCount") }}</dt>
            <dd>{{ selectedRun.success ?? 0 }}</dd>
          </div>
          <div>
            <dt>{{ t("backup.failedCount") }}</dt>
            <dd>{{ selectedRun.failed ?? 0 }}</dd>
          </div>
          <div>
            <dt>{{ t("backup.unavailableCount") }}</dt>
            <dd>{{ selectedRun.unavailable ?? 0 }}</dd>
          </div>
          <div>
            <dt>{{ t("backup.started") }}</dt>
            <dd>{{ formatUnix(selectedRun.startedUnix) }}</dd>
          </div>
          <div>
            <dt>{{ t("backup.finished") }}</dt>
            <dd>{{ formatUnix(selectedRun.finishedUnix) }}</dd>
          </div>
        </dl>
        <div v-if="canRetryRun(selectedRun)" class="row-actions">
          <button type="button" class="btn" data-action="retry-failed" :disabled="acting" @click="onRetryFailed">
            {{ t("backup.retryFailed") }}
          </button>
        </div>
        <h3>{{ t("backup.agentStatus") }}</h3>
        <div class="card">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t("backup.node") }}</th>
                <th>{{ t("backup.healthStatus") }}</th>
                <th>{{ t("backup.snapshotId") }}</th>
                <th>{{ t("backup.bytes") }}</th>
                <th>{{ t("backup.checksum") }}</th>
                <th>{{ t("backup.errorSummary") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="task in selectedTasks" :key="task.taskId || task.nodeId">
                <td>{{ nodeName(task.nodeId ?? "") }}</td>
                <td>
                  <span
                    class="status-badge"
                    :class="statusClass(task.status)"
                    :data-status="task.status"
                  >{{ task.status }}</span>
                </td>
                <td class="mono">{{ task.snapshotId || "—" }}</td>
                <td>{{ formatBytes(task.bytes) }}</td>
                <td class="mono">{{ shortSha(task.sha256 ?? "") }}</td>
                <td>{{ task.errorSummary || "—" }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </Drawer>

    <ConfirmDialog
      :open="Boolean(pendingDelete)"
      :title="deleteDialogTitle"
      :message="deleteDialogMessage"
      :confirm-label="t('actions.delete')"
      :cancel-label="t('actions.cancel')"
      :pending="deleteDialogPending"
      @cancel="cancelPendingDelete"
      @confirm="confirmPendingDelete"
    />

    <Toast :show="showToast" :message="toastMessage" :type="toastType" @close="showToast = false" />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
}
.page-header,
.header-actions,
.section-header,
.drawer-actions,
.row-actions,
.picker-toolbar,
.picker-toolbar-actions {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.page-header,
.section-header,
.picker-toolbar {
  justify-content: space-between;
}
.page-header,
.section-header {
  align-items: flex-start;
}
.header-actions,
.row-actions,
.drawer-actions,
.picker-toolbar-actions {
  flex-wrap: wrap;
}
.drawer-actions {
  justify-content: flex-end;
  position: sticky;
  bottom: 0;
  z-index: 1;
  margin-top: auto;
  padding: 1rem 0 0;
  background: var(--color-card);
  border-top: 1px solid var(--color-border);
}
.section {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  min-width: 0;
}
h1 {
  margin: 0;
  font-size: 1.35rem;
  font-weight: 650;
}
h2 {
  margin: 0 0 0.75rem;
  font-size: 1.05rem;
  font-weight: 650;
}
h3 {
  margin: 0.5rem 0 0.5rem;
  font-size: 0.95rem;
  font-weight: 600;
}
.section-header h2,
.subtitle,
.section-hint,
.drawer-lead,
.field-hint {
  margin: 0;
}
.subtitle,
.section-hint,
.drawer-lead,
.field-hint {
  color: var(--color-muted);
  font-size: 0.8125rem;
  line-height: 1.45;
}
.subtitle {
  max-width: 48rem;
  margin-top: 0.35rem;
}
.muted {
  color: var(--color-muted);
  font-size: 0.875rem;
}
.error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
}
.error-banner {
  display: flex;
  align-items: flex-start;
  gap: 0.625rem;
  margin: 0;
  border-radius: 10px;
  padding: 0.75rem 1rem;
  font-size: 0.875rem;
  line-height: 1.45;
  background: color-mix(in srgb, var(--color-danger) 12%, var(--color-card));
  color: var(--color-danger);
}
.error-banner svg {
  flex-shrink: 0;
  margin-top: 0.1rem;
}
.notice {
  margin: 0;
  color: var(--color-live-fg);
  font-size: 0.875rem;
}
.banner {
  display: flex;
  align-items: flex-start;
  gap: 0.625rem;
  border-radius: 10px;
  padding: 0.75rem 1rem;
  font-size: 0.875rem;
  line-height: 1.4;
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.banner svg {
  flex-shrink: 0;
  margin-top: 0.1rem;
}
.warning-banner {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  overflow: auto;
}
.table th,
.table td {
  padding: 0.75rem 1rem;
  vertical-align: middle;
}
.form-card,
.drawer-form {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  padding: 1.25rem;
}
.drawer-form {
  padding: 0;
  min-height: 100%;
}
.field,
.form-section,
.choice-copy,
.option-copy,
.search-field,
.process-picker {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
}
.field,
.form-section {
  font-size: 0.875rem;
  color: var(--color-muted);
}
.form-section,
.advanced {
  margin: 0;
  padding: 0;
  border: 0;
}
.form-section legend,
.advanced summary {
  margin-bottom: 0.5rem;
  color: var(--color-text);
  font-size: 0.8125rem;
  font-weight: 650;
}
.checkbox {
  flex-direction: row;
  align-items: center;
  gap: 0.5rem;
}
.required {
  color: var(--color-danger);
}
.choice-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 0.5rem;
}
.choice-card {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  min-height: 44px;
  padding: 0.75rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  color: var(--color-text);
  text-align: left;
  cursor: pointer;
  transition: border-color 150ms, background 150ms, box-shadow 150ms;
}
.choice-card:hover,
.choice-card.selected {
  border-color: var(--color-accent);
  background: color-mix(in srgb, var(--color-accent) 8%, var(--color-card));
}
.choice-card.selected {
  box-shadow: 0 0 0 1px var(--color-accent);
}
.choice-card svg {
  flex-shrink: 0;
  margin-top: 0.1rem;
  color: var(--color-accent);
}
.choice-title {
  display: block;
  font-size: 0.875rem;
  font-weight: 600;
}
.choice-hint {
  display: block;
  color: var(--color-muted);
  font-size: 0.75rem;
  line-height: 1.4;
}
.search-input-wrap {
  position: relative;
  display: flex;
  align-items: center;
}
.search-input-wrap > svg {
  position: absolute;
  left: 0.75rem;
  color: var(--color-muted);
  pointer-events: none;
}
.search-input {
  width: 100%;
  padding-left: 2.25rem;
}
.picker-toolbar {
  color: var(--color-muted);
  font-size: 0.75rem;
}
.link-btn {
  border: none;
  padding: 0.25rem 0;
  background: transparent;
  color: var(--color-accent);
  font-size: 0.75rem;
  font-weight: 600;
  cursor: pointer;
}
.link-btn:hover:not(:disabled) {
  text-decoration: underline;
}
.link-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  max-width: 100%;
  padding: 0.25rem 0.375rem 0.25rem 0.5rem;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-accent) 8%, var(--color-card));
  color: var(--color-text);
  font-size: 0.75rem;
  overflow-wrap: anywhere;
}
.chip-remove {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.5rem;
  height: 1.5rem;
  padding: 0;
  border: none;
  border-radius: 999px;
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
}
.option-list {
  max-height: 16rem;
  margin: 0;
  padding: 0.25rem;
  overflow-y: auto;
  border: 1px solid var(--color-border);
  border-radius: 8px;
}
.option-row {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  min-height: 44px;
  padding: 0.625rem 0.75rem;
  border-radius: 8px;
  cursor: pointer;
}
.option-row:hover,
.option-row.selected {
  background: color-mix(in srgb, var(--color-accent) 8%, transparent);
}
.option-copy strong {
  color: var(--color-text);
}
.picker-message {
  margin: 0;
  padding: 0.5rem 0.25rem;
  color: var(--color-muted);
  font-size: 0.8125rem;
}
.advanced {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.advanced summary {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  min-height: 44px;
  cursor: pointer;
  list-style: none;
}
.advanced summary::-webkit-details-marker {
  display: none;
}
.advanced[open] summary svg {
  transform: rotate(180deg);
}
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.5rem;
  padding: 1.75rem 1rem;
  color: var(--color-muted);
  text-align: center;
}
.empty-state strong {
  color: var(--color-text);
}
.empty-state span {
  max-width: 28rem;
  font-size: 0.8125rem;
  line-height: 1.45;
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
.spin {
  animation: backup-spin 0.8s linear infinite;
}
@keyframes backup-spin {
  to {
    transform: rotate(360deg);
  }
}
.textarea {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  min-height: 4rem;
  resize: vertical;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
.row-stale {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
tr[data-freshness="STALE"] {
  background-color: #fef3c7;
  color: #92400e;
}
.row-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
}
.clickable {
  cursor: pointer;
}
.clickable:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: -2px;
}

.field-warning {
  margin: 0;
  padding: 0.625rem 0.75rem;
  border-radius: var(--radius-sm);
  background: #fef3c7;
  color: #92400e;
  font-size: 0.75rem;
  line-height: 1.45;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 0.75rem 1.25rem;
  margin: 0 0 1rem;
}
.facts dt {
  font-size: 0.75rem;
  color: var(--color-muted);
}
.facts dd {
  margin: 0.2rem 0 0;
  font-size: 0.95rem;
  font-weight: 550;
}
.run-detail {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  min-width: 0;
}
.restore-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: grid;
  place-items: center;
  padding: 1rem;
  background: rgba(0, 0, 0, 0.55);
}
.restore-panel {
  width: min(100%, 32rem);
  padding: 1.5rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  box-shadow: 0 1rem 3rem rgba(0, 0, 0, 0.3);
  color: var(--color-text);
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.restore-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.75rem;
}
@media (min-width: 640px) {
  .choice-grid:not(.compact-grid) {
    grid-template-columns: 1fr 1fr;
  }
}
@media (max-width: 640px) {
  .page-header,
  .section-header,
  .picker-toolbar {
    flex-direction: column;
    align-items: stretch;
  }
  .header-actions .btn,
  .empty-state .btn {
    min-height: 2.75rem;
  }
}
@media (prefers-reduced-motion: reduce) {
  .spin,
  .choice-card {
    animation: none;
    transition: none;
  }
}
</style>
