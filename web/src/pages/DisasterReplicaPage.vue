<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { CopyPlus, ShieldCheck } from "lucide-vue-next";
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import Drawer from "../components/Drawer.vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, formatAge, type Freshness } from "../lib/freshness";
import { newOperationId } from "../lib/opid";
import { useReplicationClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

type TopologyNode = {
  nodeId?: string;
  host?: string;
  rack?: string;
  zone?: string;
  capacityWeight?: number;
  admitted?: boolean;
  alive?: boolean;
};

type ReplicaRoute = {
  sourceNodeId?: string;
  targetNodeIds?: string[];
  warnings?: string[];
};

type ReplicaPolicy = {
  policyId?: string;
  name?: string;
  enabled?: boolean;
  sourceSelector?: string;
  sourceIds?: string[];
  replicaFactor?: number;
  routes?: ReplicaRoute[];
  trigger?: string;
  primaryPolicyIds?: string[];
  scheduleCron?: string;
  timezone?: string;
  retentionKeepLast?: number;
  retentionKeepDays?: number;
  retentionMaxBytes?: bigint | number;
  maxConcurrency?: number;
  verifyAfterCopy?: boolean;
  bandwidthLimit?: bigint | number;
  topologyConstraints?: Record<string, string>;
  revision?: bigint | number;
};

type ReplicaTask = {
  taskId?: string;
  runId?: string;
  sourceNodeId?: string;
  targetNodeIds?: string[];
  snapshotId?: string;
  sha256?: string;
  status?: string;
  bytes?: bigint | number;
  errorCode?: string;
  errorSummary?: string;
  startedAt?: bigint | number;
  finishedAt?: bigint | number;
};

type ReplicaRun = {
  runId?: string;
  policyId?: string;
  policyRevision?: bigint | number;
  status?: string;
  tasks?: ReplicaTask[];
  startedAt?: bigint | number;
  finishedAt?: bigint | number;
};

type PolicyDraft = {
  name?: string;
  enabled?: boolean;
  sourceSelector?: string;
  sourceIds?: string[];
  replicaFactor?: number;
  routes?: ReplicaRoute[];
  trigger?: string;
  primaryPolicyIds?: string[];
  scheduleCron?: string;
  timezone?: string;
  retentionKeepLast?: number;
  retentionKeepDays?: number;
  retentionMaxBytes?: bigint | number;
  maxConcurrency?: number;
  verifyAfterCopy?: boolean;
  bandwidthLimit?: bigint | number;
  topologyConstraints?: Record<string, string>;
  draftRevision?: bigint | number;
  draftHash?: string;
  globalWarnings?: string[];
  inboundLoad?: Record<string, number>;
  topologyHealth?: string;
};

type RecoverableSnapshot = {
  snapshotId?: string;
  clusterId?: string;
  sourceNodeId?: string;
  sha256?: string;
  createdAt?: bigint | number;
  processCount?: number;
  processIds?: string[];
};

const { t } = useI18n();
const POLL_MS = 5000;
const PREVIEW_PANEL_STYLE = { width: "min(100%, 40rem)", maxHeight: "90vh", overflow: "auto" } as const;
const client = useReplicationClient();
const queryClient = useQueryClient();
const actionError = ref("");
const actionNotice = ref("");
const selectedRunId = ref("");
const previewOpen = ref(false);
const replaceCurrent = ref(false);
const draft = ref<PolicyDraft | null>(null);
const appliedRevision = ref<bigint | number | "">("");
const primaryRunId = ref("");
const verifyNotice = ref("");

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canRead = computed(() => perms.value.has("replication.read"));
const canManage = computed(() => perms.value.has("replication.manage"));

const topologyQuery = useQuery({
  queryKey: ["replica-topology"],
  queryFn: () => client.getTopology({}),
  enabled: canRead,
  refetchInterval: POLL_MS,
});

const policyQuery = useQuery({
  queryKey: ["replica-policies"],
  queryFn: () => client.listPolicies({}),
  enabled: canRead,
  refetchInterval: POLL_MS,
});

const runQuery = useQuery({
  queryKey: ["replica-runs"],
  queryFn: () => client.listRuns({}),
  enabled: canRead,
  refetchInterval: POLL_MS,
});

const snapshotQuery = useQuery({
  queryKey: ["replica-snapshots"],
  queryFn: () => client.listRecoverableSnapshots({}),
  enabled: canRead,
  refetchInterval: POLL_MS,
});

const runDetailOpen = computed(() => selectedRunId.value.length > 0);

const runDetailQuery = useQuery({
  queryKey: ["replica-run", selectedRunId],
  queryFn: () => client.getRun({ runId: selectedRunId.value }),
  enabled: computed(() => canRead.value && runDetailOpen.value),
  refetchInterval: POLL_MS,
});

const topologyNodes = computed(() => (topologyQuery.data.value?.nodes ?? []) as TopologyNode[]);
const policies = computed(() => (policyQuery.data.value?.policies ?? []) as ReplicaPolicy[]);
const runs = computed(() => (runQuery.data.value?.runs ?? []) as ReplicaRun[]);
const snapshots = computed(() => (snapshotQuery.data.value?.snapshots ?? []) as RecoverableSnapshot[]);
const currentPolicy = computed(() => policies.value[0]);
const routes = computed(() => currentPolicy.value?.routes ?? []);
const selectedRun = computed(() => {
  const detailed = runDetailQuery.data.value?.run as ReplicaRun | undefined;
  if (detailed?.runId === selectedRunId.value) {
    return detailed;
  }
  return runs.value.find((run) => run.runId === selectedRunId.value);
});
const selectedTasks = computed(() => selectedRun.value?.tasks ?? []);
const latestRun = computed(() => latestRunOf(runs.value));
const latestTasks = computed(() => latestRun.value?.tasks ?? []);
const hasExistingRoutes = computed(() => routes.value.length > 0);
const acting = computed(
  () =>
    generateMut.isPending.value ||
    applyMut.isPending.value ||
    retryMut.isPending.value ||
    verifyMut.isPending.value ||
    startRunMut.isPending.value,
);

const topologyPending = computed(() => topologyQuery.isPending.value && !topologyQuery.data.value);
const policiesPending = computed(() => policyQuery.isPending.value && !policyQuery.data.value);
const runsPending = computed(() => runQuery.isPending.value && !runQuery.data.value);
const snapshotsPending = computed(() => snapshotQuery.isPending.value && !snapshotQuery.data.value);

const topologyUnreachable = computed(
  () => Boolean(topologyQuery.error.value) && !topologyPending.value && !topologyNodes.value.length,
);
const runsUnreachable = computed(
  () => Boolean(runQuery.error.value) && !runsPending.value && !runs.value.length,
);
const snapshotsUnreachable = computed(
  () => Boolean(snapshotQuery.error.value) && !snapshotsPending.value && !snapshots.value.length,
);
const topologyStale = computed(() => Boolean(topologyQuery.error.value) && topologyNodes.value.length > 0);
const policiesStale = computed(() => Boolean(policyQuery.error.value) && policies.value.length > 0);
const runsStale = computed(() => Boolean(runQuery.error.value) && runs.value.length > 0);
const snapshotsStale = computed(() => Boolean(snapshotQuery.error.value) && snapshots.value.length > 0);
const hasStale = computed(
  () => topologyStale.value || policiesStale.value || runsStale.value || snapshotsStale.value,
);
const hasPartialRun = computed(() => runs.value.some((run) => isPartial(run.status)));
const offlineAdmitted = computed(() => topologyNodes.value.filter((node) => node.admitted && !node.alive));
const shownRevision = computed(() => appliedRevision.value || currentPolicy.value?.revision || "");
const configFreshness = computed<Freshness>(() => {
  if (topologyUnreachable.value && !policies.value.length) {
    return UNKNOWN;
  }
  if (topologyStale.value || policiesStale.value) {
    return STALE;
  }
  return LIVE;
});
const runsFreshness = computed<Freshness>(() => {
  if (runsUnreachable.value) {
    return UNKNOWN;
  }
  if (runsStale.value) {
    return STALE;
  }
  return LIVE;
});
const snapshotsFreshness = computed<Freshness>(() => {
  if (snapshotsUnreachable.value) {
    return UNKNOWN;
  }
  if (snapshotsStale.value) {
    return STALE;
  }
  return LIVE;
});

const overview = computed(() => {
  const tasks = latestTasks.value;
  let healthy = 0;
  let lag = 0;
  let failed = 0;
  let lastSuccess = 0;
  for (const task of tasks) {
    const status = (task.status || "").toUpperCase();
    if (isSuccessStatus(status)) {
      healthy += 1;
      lastSuccess = Math.max(lastSuccess, unixNumber(task.finishedAt));
      continue;
    }
    if (status === "FAILED") {
      failed += 1;
      continue;
    }
    if (status === "UNAVAILABLE" || status === "TIMEOUT" || status === "PENDING" || status === "RUNNING") {
      lag += 1;
    }
  }
  return {
    routeCount: routes.value.length,
    healthy,
    lag,
    failed,
    lastSuccess: lastSuccess > 0 ? formatUnix(lastSuccess) : "—",
    recoverable: snapshots.value.length,
  };
});

const canApplyDraft = computed(() => {
  if (!canManage.value || !draft.value || acting.value) {
    return false;
  }
  if (hasExistingRoutes.value && !replaceCurrent.value) {
    return false;
  }
  return true;
});

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  const err =
    topologyQuery.error.value ??
    policyQuery.error.value ??
    runQuery.error.value ??
    snapshotQuery.error.value ??
    runDetailQuery.error.value;
  if (!err || hasStale.value) {
    return "";
  }
  return formatRemoteError(err);
});

const routeRows = computed(() =>
  routes.value.map((route) => {
    const source = route.sourceNodeId || "";
    const tasks = latestTasks.value.filter((task) => task.sourceNodeId === source);
    const status = routeStatus(tasks);
    const lastSuccess = tasks
      .filter((task) => isSuccessStatus(task.status))
      .reduce((max, task) => Math.max(max, unixNumber(task.finishedAt)), 0);
    return {
      source,
      targets: (route.targetNodeIds ?? []).join(", ") || "—",
      warnings: route.warnings ?? [],
      status,
      lastSuccess: lastSuccess > 0 ? formatUnix(lastSuccess) : "—",
      lag: lastSuccess > 0 ? formatAge(Date.now(), lastSuccess * (lastSuccess > 1e12 ? 1 : 1000)) : "—",
      freshness: runsFreshness.value,
    };
  }),
);

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
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

function latestRunOf(list: ReplicaRun[]): ReplicaRun | undefined {
  let latest: ReplicaRun | undefined;
  let latestUnix = Number.NEGATIVE_INFINITY;
  let latestId = "";
  for (const run of list) {
    const started = unixNumber(run.startedAt);
    const runId = run.runId ?? "";
    if (!latest || started > latestUnix || (started === latestUnix && runId > latestId)) {
      latest = run;
      latestUnix = started;
      latestId = runId;
    }
  }
  return latest;
}

function isPartial(status: string | undefined): boolean {
  return (status || "").toUpperCase() === "PARTIAL";
}

function isSuccessStatus(status: string | undefined): boolean {
  const s = (status || "").toUpperCase();
  return s === "SUCCEEDED" || s === "SUCCESS";
}

function routeStatus(tasks: ReplicaTask[]): string {
  if (!tasks.length) {
    return "UNKNOWN";
  }
  const statuses = tasks.map((task) => (task.status || "").toUpperCase());
  if (statuses.some((status) => status === "FAILED")) {
    return "FAILED";
  }
  if (statuses.some((status) => status === "UNAVAILABLE" || status === "TIMEOUT")) {
    return "UNAVAILABLE";
  }
  if (statuses.some((status) => status === "PENDING" || status === "RUNNING")) {
    return "RUNNING";
  }
  if (statuses.every((status) => isSuccessStatus(status))) {
    return "SUCCEEDED";
  }
  return "UNKNOWN";
}

function statusClass(status: string | undefined): string {
  const s = (status || "").toUpperCase();
  if (isSuccessStatus(s)) {
    return "status-success";
  }
  if (s === "PARTIAL" || s === "TIMEOUT" || s === "PENDING" || s === "RUNNING" || s === "UNAVAILABLE") {
    return "status-partial";
  }
  if (s === "FAILED" || s === "CANCELED" || s === "CONFIG_MISSING") {
    return "status-failed";
  }
  return "status-unknown";
}

function statusStyle(status: string | undefined): string {
  const s = (status || "").toUpperCase();
  if (isSuccessStatus(s)) {
    return "background-color: #D1FAE5; color: #065F46";
  }
  if (s === "PARTIAL" || s === "TIMEOUT" || s === "PENDING" || s === "RUNNING" || s === "UNAVAILABLE") {
    return "background-color: #FEF3C7; color: #92400E";
  }
  if (s === "FAILED" || s === "CANCELED" || s === "CONFIG_MISSING") {
    return "background-color: #FEE2E2; color: #991B1B";
  }
  return "background-color: #E5E7EB; color: #374151";
}

function formatUnix(unix: bigint | number | undefined): string {
  const n = unixNumber(unix);
  if (n <= 0) {
    return "—";
  }
  const ms = n > 1e12 ? n : n * 1000;
  return new Date(ms).toISOString();
}

function shortSha(sha: string | undefined): string {
  if (!sha) {
    return "—";
  }
  return sha.length > 12 ? sha.slice(0, 12) : sha;
}

function asBigInt(value: bigint | number | undefined, fallback = 0n): bigint {
  if (typeof value === "bigint") {
    return value;
  }
  if (typeof value === "number" && Number.isFinite(value)) {
    return BigInt(Math.trunc(value));
  }
  return fallback;
}

function constraintText(constraints: Record<string, string> | undefined): string {
  const entries = Object.entries(constraints ?? {});
  if (!entries.length) {
    return t("replica.none");
  }
  return entries.map(([key, value]) => `${key}=${value}`).join(", ");
}

function warningLabel(code: string): string {
  if (code === "single-node-no-replica") {
    return t("replica.n1Warning");
  }
  if (code.startsWith("admitted-node-offline:")) {
    return t("replica.offlineWarning", { id: code.slice("admitted-node-offline:".length) });
  }
  if (code === "no-topology-labels") {
    return t("replica.noTopologyLabels");
  }
  if (code === "insufficient-candidates-degraded") {
    return t("replica.insufficientCandidates");
  }
  return code;
}

function isN1Warning(code: string): boolean {
  return code === "single-node-no-replica";
}

function canRetryRun(run: ReplicaRun | undefined): boolean {
  if (!run || !canManage.value) {
    return false;
  }
  const status = (run.status || "").toUpperCase();
  if (status !== "PARTIAL" && status !== "FAILED") {
    return false;
  }
  return (run.tasks ?? []).some((task) => {
    const s = (task.status || "").toUpperCase();
    return s === "FAILED" || s === "UNAVAILABLE" || s === "TIMEOUT" || s === "CONFIG_MISSING";
  });
}

function draftRequest(): PolicyDraft {
  const policy = currentPolicy.value;
  return {
    name: policy?.name || "cluster-replica",
    enabled: policy?.enabled ?? true,
    sourceSelector: policy?.sourceSelector || "ALL_ADMITTED",
    sourceIds: policy?.sourceIds ?? [],
    replicaFactor: policy?.replicaFactor || 1,
    trigger: policy?.trigger || "MANUAL",
    primaryPolicyIds: policy?.primaryPolicyIds ?? [],
    scheduleCron: policy?.scheduleCron ?? "",
    timezone: policy?.timezone || "UTC",
    retentionKeepLast: policy?.retentionKeepLast ?? 7,
    retentionKeepDays: policy?.retentionKeepDays ?? 30,
    retentionMaxBytes: asBigInt(policy?.retentionMaxBytes),
    maxConcurrency: policy?.maxConcurrency ?? 2,
    verifyAfterCopy: policy?.verifyAfterCopy ?? true,
    bandwidthLimit: asBigInt(policy?.bandwidthLimit),
    topologyConstraints: policy?.topologyConstraints ?? {},
  };
}

async function invalidateReplica(): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ["replica-topology"] }),
    queryClient.invalidateQueries({ queryKey: ["replica-policies"] }),
    queryClient.invalidateQueries({ queryKey: ["replica-runs"] }),
    queryClient.invalidateQueries({ queryKey: ["replica-run"] }),
    queryClient.invalidateQueries({ queryKey: ["replica-snapshots"] }),
  ]);
}

const generateMut = useMutation({
  mutationFn: () => client.generatePolicyDraft(draftRequest()),
  onSuccess: (res) => {
    draft.value = (res.draft ?? null) as PolicyDraft | null;
    previewOpen.value = Boolean(draft.value);
    replaceCurrent.value = false;
    actionError.value = "";
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const applyMut = useMutation({
  mutationFn: () => {
    const current = draft.value;
    if (!current) {
      throw new Error("draft required");
    }
    return client.applyPolicyDraft({
      policyId: currentPolicy.value?.policyId || newOperationId(),
      draft: current,
      draftRevision: asBigInt(current.draftRevision),
      draftHash: current.draftHash || "",
      expectedRevision: asBigInt(currentPolicy.value?.revision),
      meta: mutationMeta(),
    });
  },
  onSuccess: async (res) => {
    appliedRevision.value = res.revision;
    previewOpen.value = false;
    replaceCurrent.value = false;
    draft.value = null;
    actionNotice.value = t("replica.applied", { revision: String(res.revision) });
    await invalidateReplica();
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const retryMut = useMutation({
  mutationFn: (runId: string) =>
    client.retryFailedRoutes({
      runId,
      meta: mutationMeta(),
    }),
  onSuccess: async () => {
    await invalidateReplica();
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const verifyMut = useMutation({
  mutationFn: (snap: RecoverableSnapshot) =>
    client.verifyReplica({
      sourceNodeId: snap.sourceNodeId || "",
      snapshotId: snap.snapshotId || "",
    }),
  onSuccess: (res) => {
    verifyNotice.value = res.valid ? t("replica.verifyValid") : t("replica.verifyInvalid");
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const startRunMut = useMutation({
  mutationFn: () =>
    client.startRun({
      policyId: currentPolicy.value?.policyId || "",
      primaryRunId: primaryRunId.value.trim(),
      meta: mutationMeta(),
    }),
  onSuccess: async () => {
    primaryRunId.value = "";
    await invalidateReplica();
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

async function onGenerate(): Promise<void> {
  if (!canManage.value || acting.value) {
    return;
  }
  actionError.value = "";
  actionNotice.value = "";
  try {
    await generateMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onApplyDraft(): Promise<void> {
  if (!canApplyDraft.value) {
    return;
  }
  actionError.value = "";
  try {
    await applyMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

function closePreview(): void {
  if (applyMut.isPending.value) {
    return;
  }
  previewOpen.value = false;
}

function openRun(runId: string): void {
  if (!runId) {
    return;
  }
  selectedRunId.value = runId;
}

function closeRun(): void {
  selectedRunId.value = "";
}

async function onRetryFailed(): Promise<void> {
  const runId = selectedRun.value?.runId || selectedRunId.value;
  if (!canRetryRun(selectedRun.value) || !runId || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await retryMut.mutateAsync(runId);
  } catch {
    // onError already recorded
  }
}

async function onVerify(snap: RecoverableSnapshot): Promise<void> {
  if (!canManage.value || acting.value) {
    return;
  }
  actionError.value = "";
  verifyNotice.value = "";
  try {
    await verifyMut.mutateAsync(snap);
  } catch {
    // onError already recorded
  }
}

async function onStartRun(): Promise<void> {
  if (!canManage.value || !currentPolicy.value?.policyId || !primaryRunId.value.trim() || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await startRunMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page" :data-permission="canRead ? 'granted' : 'denied'">
    <h1>{{ t("nav.disasterReplica") }}</h1>
    <template v-if="canRead">
      <div v-if="hasStale" class="banner" role="status">{{ t("replica.staleBanner") }}</div>
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <p v-else-if="actionNotice" class="notice" role="status">{{ actionNotice }}</p>
      <p v-else-if="verifyNotice" class="notice" role="status">{{ verifyNotice }}</p>

      <section class="section" data-section="overview">
        <div class="section-header">
          <h2>{{ t("replica.overview") }}</h2>
          <FreshnessBadge :status="hasStale ? STALE : LIVE" />
        </div>
        <dl class="facts">
          <div>
            <dt>{{ t("replica.routeCount") }}</dt>
            <dd data-route-count>{{ overview.routeCount }}</dd>
          </div>
          <div>
            <dt>{{ t("replica.healthyCount") }}</dt>
            <dd data-healthy-count>{{ overview.healthy }}</dd>
          </div>
          <div>
            <dt>{{ t("replica.lagCount") }}</dt>
            <dd data-lag-count>{{ overview.lag }}</dd>
          </div>
          <div>
            <dt>{{ t("replica.failedCount") }}</dt>
            <dd data-failed-count>{{ overview.failed }}</dd>
          </div>
          <div>
            <dt>{{ t("replica.lastSuccess") }}</dt>
            <dd data-last-success>{{ overview.lastSuccess }}</dd>
          </div>
          <div>
            <dt>{{ t("replica.recoverableCount") }}</dt>
            <dd data-recoverable-count>{{ overview.recoverable }}</dd>
          </div>
        </dl>
      </section>

      <section class="section" data-section="config">
        <div class="section-header">
          <h2>{{ t("replica.config") }}</h2>
          <FreshnessBadge v-if="configFreshness !== LIVE" :status="configFreshness" />
          <button
            v-if="canManage"
            type="button"
            class="btn btn-primary"
            data-action="generate"
            :aria-label="t('replica.generate')"
            :disabled="acting"
            @click="onGenerate"
          >
            <CopyPlus :size="18" aria-hidden="true" />
            {{ t("replica.generate") }}
          </button>
        </div>
        <p v-if="shownRevision !== ''" class="muted" data-policy-revision>
          {{ t("replica.policyRevision") }}: {{ shownRevision }}
        </p>
        <p
          v-for="node in offlineAdmitted"
          :key="`offline-${node.nodeId}`"
          class="banner warning-banner"
          data-offline-warning
          role="status"
        >
          {{ t("replica.offlineWarning", { id: node.nodeId }) }}
        </p>
        <p v-if="topologyPending || policiesPending" class="muted">{{ t("replica.loading") }}</p>
        <div
          v-else-if="topologyUnreachable"
          class="banner warning-banner"
          data-topology-unreachable
          role="status"
        >
          <FreshnessBadge :status="UNKNOWN" />
          {{ t("replica.topologyUnreachable") }}
        </div>
        <template v-else>
          <dl class="facts">
            <div>
              <dt>{{ t("replica.replicaFactor") }}</dt>
              <dd>{{ currentPolicy?.replicaFactor ?? "—" }}</dd>
            </div>
            <div>
              <dt>{{ t("replica.trigger") }}</dt>
              <dd>{{ currentPolicy?.trigger || t("replica.none") }}</dd>
            </div>
            <div>
              <dt>{{ t("replica.retention") }}</dt>
              <dd>
                {{
                  t("replica.retentionSummary", {
                    last: currentPolicy?.retentionKeepLast ?? 0,
                    days: currentPolicy?.retentionKeepDays ?? 0,
                  })
                }}
              </dd>
            </div>
            <div>
              <dt>{{ t("replica.concurrency") }}</dt>
              <dd>{{ currentPolicy?.maxConcurrency ?? "—" }}</dd>
            </div>
            <div>
              <dt>{{ t("replica.topologyConstraints") }}</dt>
              <dd>{{ constraintText(currentPolicy?.topologyConstraints) }}</dd>
            </div>
          </dl>
          <div class="card">
            <table class="table">
              <thead>
                <tr>
                  <th>{{ t("replica.node") }}</th>
                  <th>{{ t("replica.host") }}</th>
                  <th>{{ t("replica.rack") }}</th>
                  <th>{{ t("replica.zone") }}</th>
                  <th>{{ t("replica.admitted") }}</th>
                  <th>{{ t("replica.alive") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="node in topologyNodes"
                  :key="node.nodeId"
                  :data-node-id="node.nodeId"
                  :data-admitted="node.admitted ? 'true' : 'false'"
                  :data-alive="node.alive ? 'true' : 'false'"
                >
                  <td class="mono">{{ node.nodeId }}</td>
                  <td class="mono">{{ node.host || "—" }}</td>
                  <td>{{ node.rack || "—" }}</td>
                  <td>{{ node.zone || "—" }}</td>
                  <td>{{ node.admitted ? t("replica.admitted") : t("replica.none") }}</td>
                  <td>{{ node.alive ? t("replica.alive") : t("replica.none") }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="card">
            <table class="table">
              <thead>
                <tr>
                  <th>{{ t("replica.source") }}</th>
                  <th>{{ t("replica.targets") }}</th>
                  <th>{{ t("replica.health") }}</th>
                  <th>{{ t("replica.lastSuccess") }}</th>
                  <th>{{ t("replica.lag") }}</th>
                  <th>{{ t("replica.freshness") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in routeRows" :key="row.source">
                  <td class="mono">{{ row.source }}</td>
                  <td class="mono">{{ row.targets }}</td>
                  <td>
                    <span
                      class="status-badge"
                      :class="statusClass(row.status)"
                      :data-status="row.status"
                      :style="statusStyle(row.status)"
                    >{{ row.status }}</span>
                  </td>
                  <td>{{ row.lastSuccess }}</td>
                  <td>{{ row.lag }}</td>
                  <td><FreshnessBadge :status="row.freshness" /></td>
                </tr>
                <tr v-if="!routeRows.length">
                  <td colspan="6" class="muted">{{ t("replica.noRoutes") }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </section>

      <section class="section" data-section="runs">
        <div class="section-header">
          <h2>{{ t("replica.runs") }}</h2>
          <FreshnessBadge v-if="runsFreshness !== LIVE" :status="runsFreshness" />
        </div>
        <div v-if="hasPartialRun" class="banner warning-banner" data-partial-warning role="status">
          {{ t("replica.partialWarning") }}
        </div>
        <form v-if="canManage && currentPolicy?.policyId" class="inline-form" @submit.prevent="onStartRun">
          <label class="field">
            {{ t("replica.primaryRunId") }}
            <input
              v-model="primaryRunId"
              class="input"
              name="primaryRunId"
              type="text"
              autocomplete="off"
            />
          </label>
          <button class="btn" type="submit" data-action="start-run" :disabled="!primaryRunId.trim() || acting">
            {{ t("replica.startRun") }}
          </button>
        </form>
        <p v-if="runsPending" class="muted">{{ t("replica.loading") }}</p>
        <div
          v-else-if="runsUnreachable"
          class="banner warning-banner"
          data-runs-unreachable
          role="status"
        >
          <FreshnessBadge :status="UNKNOWN" />
          {{ t("replica.runsUnreachable") }}
        </div>
        <div v-else class="card">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t("replica.runId") }}</th>
                <th>{{ t("replica.status") }}</th>
                <th>{{ t("replica.lastSuccess") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="run in runs"
                :key="run.runId"
                class="clickable"
                :data-run-id="run.runId"
                @click="openRun(run.runId || '')"
              >
                <td class="mono">{{ run.runId }}</td>
                <td>
                  <span
                    class="status-badge"
                    :class="statusClass(run.status)"
                    :data-status="run.status"
                    :style="statusStyle(run.status)"
                  >{{ run.status }}</span>
                </td>
                <td>{{ formatUnix(run.finishedAt) }}</td>
              </tr>
              <tr v-if="!runs.length">
                <td colspan="3" class="muted">{{ t("replica.noRuns") }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="section" data-section="recovery">
        <div class="section-header">
          <h2>{{ t("replica.recovery") }}</h2>
          <FreshnessBadge v-if="snapshotsFreshness !== LIVE" :status="snapshotsFreshness" />
        </div>
        <p class="muted">{{ t("replica.restoreHint") }}</p>
        <p v-if="snapshotsPending" class="muted">{{ t("replica.loading") }}</p>
        <div
          v-else-if="snapshotsUnreachable"
          class="banner warning-banner"
          data-snapshots-unreachable
          role="status"
        >
          <FreshnessBadge :status="UNKNOWN" />
          {{ t("replica.snapshotsUnreachable") }}
        </div>
        <div v-else class="card">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t("replica.snapshotId") }}</th>
                <th>{{ t("replica.owner") }}</th>
                <th>{{ t("replica.checksum") }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="snap in snapshots" :key="`${snap.sourceNodeId}:${snap.snapshotId}`">
                <td class="mono">{{ snap.snapshotId }}</td>
                <td class="mono" data-snapshot-owner>{{ snap.sourceNodeId }}</td>
                <td class="mono">{{ shortSha(snap.sha256) }}</td>
                <td>
                  <div class="row-actions">
                    <button
                      v-if="canManage"
                      type="button"
                      class="btn"
                      data-action="verify"
                      :aria-label="t('replica.verify')"
                      :disabled="acting"
                      @click="onVerify(snap)"
                    >
                      <ShieldCheck :size="16" aria-hidden="true" />
                      {{ t("replica.verify") }}
                    </button>
                    <RouterLink
                      class="btn"
                      data-action="restore-owner"
                      :to="{ path: '/backup', query: { owner: snap.sourceNodeId || '', snapshot: snap.snapshotId || '' } }"
                    >
                      {{ t("replica.restoreOwner") }}
                    </RouterLink>
                  </div>
                </td>
              </tr>
              <tr v-if="!snapshots.length">
                <td colspan="4" class="muted">{{ t("replica.noSnapshots") }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

  <div v-if="previewOpen && draft" class="preview-backdrop">
    <section
      class="preview-panel"
      data-preview-dialog
      data-responsive="true"
      role="dialog"
      :aria-modal="true"
      :aria-label="t('replica.preview')"
      :style="PREVIEW_PANEL_STYLE"
    >
      <h2>{{ t("replica.preview") }}</h2>
      <h3>{{ t("replica.generationRules") }}</h3>
      <dl class="facts">
        <div>
          <dt>{{ t("replica.sourceSelector") }}</dt>
          <dd>{{ draft.sourceSelector }}</dd>
        </div>
        <div>
          <dt>{{ t("replica.replicaFactor") }}</dt>
          <dd>{{ draft.replicaFactor }}</dd>
        </div>
        <div>
          <dt>{{ t("replica.trigger") }}</dt>
          <dd>{{ draft.trigger }}</dd>
        </div>
        <div>
          <dt>{{ t("replica.retention") }}</dt>
          <dd>
            {{
              t("replica.retentionSummary", {
                last: draft.retentionKeepLast ?? 0,
                days: draft.retentionKeepDays ?? 0,
              })
            }}
          </dd>
        </div>
      </dl>
      <h3>{{ t("replica.routeTable") }}</h3>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("replica.source") }}</th>
              <th>{{ t("replica.targets") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="route in draft.routes ?? []" :key="route.sourceNodeId">
              <td class="mono">{{ route.sourceNodeId }}</td>
              <td class="mono">{{ (route.targetNodeIds ?? []).join(", ") }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <h3>{{ t("replica.warnings") }}</h3>
      <ul class="warning-list">
        <li
          v-for="code in draft.globalWarnings ?? []"
          :key="code"
          :data-n1-warning="isN1Warning(code) ? 'true' : undefined"
        >
          {{ warningLabel(code) }}
        </li>
      </ul>
      <h3>{{ t("replica.costEstimate") }}</h3>
      <p>{{ t("replica.inboundLoad") }}</p>
      <ul>
        <li v-for="(load, nodeId) in draft.inboundLoad ?? {}" :key="nodeId">
          <span class="mono">{{ nodeId }}</span>: {{ load }}
        </li>
      </ul>
      <label v-if="hasExistingRoutes" class="field checkbox" data-replace-current>
        <input v-model="replaceCurrent" name="replaceCurrent" type="checkbox" />
        {{ t("replica.replaceCurrent") }}
      </label>
      <p v-if="hasExistingRoutes" class="muted">{{ t("replica.replaceHint") }}</p>
      <div class="preview-actions">
        <button type="button" class="btn" :disabled="acting" @click="closePreview">
          {{ t("replica.cancel") }}
        </button>
        <button
          v-if="canManage"
          type="button"
          class="btn btn-primary"
          data-action="apply-draft"
          :disabled="!canApplyDraft"
          @click="onApplyDraft"
        >
          {{ t("replica.apply") }}
        </button>
      </div>
    </section>
  </div>

  <Drawer
    :open="runDetailOpen"
    :title="t('replica.runDetail')"
    :close-label="t('actions.close')"
    size="wide"
    @close="closeRun"
  >
    <div v-if="selectedRun" class="run-detail" data-run-detail>
      <p v-if="isPartial(selectedRun.status)" class="banner warning-banner" role="status">
        {{ t("replica.partialWarning") }}
      </p>
      <dl class="facts">
        <div>
          <dt>{{ t("replica.runId") }}</dt>
          <dd class="mono">{{ selectedRun.runId }}</dd>
        </div>
        <div>
          <dt>{{ t("replica.status") }}</dt>
          <dd>
            <span
              class="status-badge"
              :class="statusClass(selectedRun.status)"
              :data-status="selectedRun.status"
              :style="statusStyle(selectedRun.status)"
            >{{ selectedRun.status }}</span>
          </dd>
        </div>
      </dl>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("replica.source") }}</th>
              <th>{{ t("replica.targets") }}</th>
              <th>{{ t("replica.status") }}</th>
              <th>{{ t("replica.snapshotId") }}</th>
              <th>{{ t("replica.bytes") }}</th>
              <th>{{ t("replica.checksum") }}</th>
              <th>{{ t("replica.errorSummary") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="task in selectedTasks" :key="task.taskId">
              <td class="mono">{{ task.sourceNodeId }}</td>
              <td class="mono">{{ (task.targetNodeIds ?? []).join(", ") }}</td>
              <td>
                <span
                  class="status-badge"
                  :class="statusClass(task.status)"
                  :data-status="task.status"
                  :data-route-status="task.status"
                  :style="statusStyle(task.status)"
                >{{ task.status }}</span>
              </td>
              <td class="mono">{{ task.snapshotId || "—" }}</td>
              <td>{{ task.bytes ?? "—" }}</td>
              <td class="mono">{{ shortSha(task.sha256) }}</td>
              <td>{{ task.errorSummary || "—" }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="drawer-actions">
        <button
          v-if="canRetryRun(selectedRun)"
          type="button"
          class="btn"
          data-action="retry-failed"
          :disabled="acting"
          @click="onRetryFailed"
        >
          {{ t("replica.retryFailed") }}
        </button>
      </div>
    </div>
  </Drawer>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.section {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  min-width: 0;
}
.section-header,
.drawer-actions,
.preview-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  flex-wrap: wrap;
}
.drawer-actions,
.preview-actions {
  justify-content: flex-end;
  margin-top: auto;
  padding-top: 1rem;
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
  margin: 0.5rem 0;
  font-size: 0.95rem;
  font-weight: 600;
}
.section-header h2 {
  margin: 0;
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
.notice {
  margin: 0;
  color: var(--color-live-fg);
  font-size: 0.875rem;
}
.banner {
  border-radius: 10px;
  padding: 0.75rem 1rem;
  font-size: 0.875rem;
  line-height: 1.4;
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.warning-banner {
  background: #fef3c7;
  color: #92400e;
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  overflow: auto;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  font-size: 0.875rem;
  color: var(--color-muted);
}
.checkbox {
  flex-direction: row;
  align-items: center;
  gap: 0.5rem;
}
.inline-form {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: flex-end;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
.row-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
}
.clickable {
  cursor: pointer;
}
.status-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 3px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0.02em;
}
.status-success {
  background-color: #d1fae5;
  color: #065f46;
}
.status-partial,
.status-pending {
  background-color: #fef3c7;
  color: #92400e;
}
.status-failed {
  background-color: #fee2e2;
  color: #991b1b;
}
.status-unknown {
  background-color: #e5e7eb;
  color: #374151;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 0.75rem 1.25rem;
  margin: 0;
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
.preview-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: grid;
  place-items: center;
  padding: 1rem;
  background: rgba(0, 0, 0, 0.55);
}
.preview-panel {
  width: min(100%, 40rem);
  max-height: 90vh;
  overflow: auto;
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
.warning-list {
  margin: 0;
  padding-left: 1.25rem;
}
.btn {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
}
</style>
