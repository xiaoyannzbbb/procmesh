<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Template enums, data-* hooks, and comparison literals are not visible copy. */
import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { ArrowUpCircle, ChevronDown, ChevronRight, Github, Layers, LoaderCircle, RefreshCw, TriangleAlert } from "lucide-vue-next";
import { computed, onUnmounted, ref } from "vue";
import { useRoute } from "vue-router";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { stripV } from "../lib/agentVersion";
import { LIVE, STALE, UNKNOWN, type Freshness } from "../lib/freshness";
import { newOperationId } from "../lib/opid";
import { useNodeClient } from "../lib/rpc/cluster";
import { useUpdateClient } from "../lib/rpc/update";
import { selfUpdateHold, session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { RAFT_LEADER, RAFT_VOTER } from "./clusterView";

const SKIP_KEYS = {
  STALE: "updates.skip.STALE",
  UNKNOWN: "updates.skip.UNKNOWN",
  FAILED: "updates.skip.FAILED",
  SUSPECT: "updates.skip.SUSPECT",
  UNSUPPORTED: "updates.skip.UNSUPPORTED",
  MACOS: "updates.skip.MACOS",
  DISABLED: "updates.skip.DISABLED",
  BUSY: "updates.skip.BUSY",
  CURRENT: "updates.skip.CURRENT",
  UNAVAILABLE: "updates.skip.UNAVAILABLE",
  TIMEOUT: "updates.skip.TIMEOUT",
  CHECK_FAILED: "updates.skip.CHECK_FAILED",
} as const;

type SkipReason = keyof typeof SKIP_KEYS;

type StatusNode = {
  nodeId: string;
  hostname: string;
  os: string;
  arch: string;
  version: string;
  freshness: string;
  lastUpdatedUnixMs?: bigint | number;
  eligible: boolean;
  skipReason: string;
  busy: boolean;
};

type UpdatePin = {
  repository: string;
  tag: string;
  checksums: { [key: string]: string };
};

type JobSummary = {
  success?: number;
  failed?: number;
  timeout?: number;
  conflict?: number;
  skipped?: number;
  cancelled?: number;
};

type JobTarget = {
  operationId?: string;
  nodeId?: string;
  hostname?: string;
  status?: string;
  skipReason?: string;
  error?: string;
  orderIndex?: number;
};

type UpdateJobView = {
  jobId: string;
  status: string;
  pin?: UpdatePin;
  summary?: JobSummary;
  createdUnixMs?: bigint | number;
  targets?: JobTarget[];
};

const SELF_POLL_MS = 2000;
const SELF_TIMEOUT_MS = 120_000;
const JOB_POLL_MS = 3000;
const JOB_STATUS_KEYS = {
  PENDING: "updates.jobs.job.PENDING",
  RUNNING: "updates.jobs.job.RUNNING",
  COMPLETED: "updates.jobs.job.COMPLETED",
  PARTIAL: "updates.jobs.job.PARTIAL",
  FAILED: "updates.jobs.job.FAILED",
} as const;
const TARGET_STATUS_KEYS = {
  PENDING: "updates.jobs.target.PENDING",
  RUNNING: "updates.jobs.target.RUNNING",
  SUCCESS: "updates.jobs.target.SUCCESS",
  FAILED: "updates.jobs.target.FAILED",
  TIMEOUT: "updates.jobs.target.TIMEOUT",
  CONFLICT: "updates.jobs.target.CONFLICT",
  SKIPPED: "updates.jobs.target.SKIPPED",
  CANCELLED: "updates.jobs.target.CANCELLED",
} as const;

const { t } = useI18n();
const route = useRoute();
const queryClient = useQueryClient();
const client = useUpdateClient();
const nodeClient = useNodeClient();
const refreshing = ref(false);
const pendingNode = ref<StatusNode | null>(null);
const clusterConfirmOpen = ref(false);
const applyError = ref("");
const clusterError = ref("");
const jobActionError = ref("");
const expandedJobId = ref("");
const overlayOpen = ref(false);
const overlayTimedOut = ref(false);
const overlayPinTag = ref("");
const overlayStartedAt = ref(0);
let pollTimer: ReturnType<typeof setTimeout> | null = null;

const CHECK_LATEST_STALE_MS = 15 * 60 * 1000;

const permissions = computed(() => session.value?.permissions ?? []);
const canManage = computed(() => permissions.value.includes("node.manage"));
const canManageCluster = computed(() => permissions.value.includes("cluster.manage"));
const canReadJobs = computed(
  () => canManageCluster.value || permissions.value.includes("cluster.read"),
);

const checkLatestQuery = useQuery({
  queryKey: ["updates", "checkLatest"],
  queryFn: () => client.checkLatest({ refresh: false }),
  staleTime: CHECK_LATEST_STALE_MS,
});

const query = useQuery({
  queryKey: ["updates", "nodeStatus"],
  queryFn: () => client.listNodeUpdateStatus({}),
});

const localQuery = useQuery({
  queryKey: ["updates", "localInfo"],
  queryFn: () => client.getLocalUpdateInfo({}),
  staleTime: 60_000,
});

const raftQuery = useQuery({
  queryKey: ["nodes"],
  queryFn: () => nodeClient.listNodes({}),
  enabled: canManageCluster,
  staleTime: 15_000,
});

function jobIsActive(status: string): boolean {
  const value = status.toUpperCase();
  return value === "RUNNING" || value === "PENDING";
}

const jobsQuery = useQuery({
  queryKey: ["updates", "jobs"],
  queryFn: () => client.listUpdateJobs({}),
  enabled: canReadJobs,
  refetchInterval: (q) => {
    const jobs = q.state.data?.jobs ?? [];
    return jobs.some((job) => jobIsActive(job.status ?? "")) ? JOB_POLL_MS : false;
  },
});

const jobDetailQuery = useQuery({
  queryKey: computed(() => ["updates", "jobs", expandedJobId.value]),
  queryFn: () => client.getUpdateJob({ jobId: expandedJobId.value }),
  enabled: computed(() => expandedJobId.value.length > 0),
  refetchInterval: (q) => (jobIsActive(q.state.data?.job?.status ?? "") ? JOB_POLL_MS : false),
});

const nodes = computed(() => (query.data.value?.nodes ?? []) as StatusNode[]);
const loading = computed(() => query.isPending.value && !query.data.value);
const localNodeId = computed(() => localQuery.data.value?.nodeId ?? "");
const highlightedNodeId = computed(() => {
  const raw = route.query.node;
  if (typeof raw === "string") {
    return raw;
  }
  if (Array.isArray(raw) && typeof raw[0] === "string") {
    return raw[0];
  }
  return "";
});
const eligibleNodes = computed(() => nodes.value.filter((node) => node.eligible));
const skippedNodes = computed(() => nodes.value.filter((node) => !node.eligible));
const jobs = computed(() => (jobsQuery.data.value?.jobs ?? []) as UpdateJobView[]);
const jobsLoading = computed(() => canReadJobs.value && jobsQuery.isPending.value && !jobsQuery.data.value);
const pin = computed<UpdatePin | null>(() => {
  const latest = checkLatestQuery.data.value;
  if (!latest || latest.checkError) {
    return null;
  }
  const tag = latest.tag ?? "";
  const repository = latest.repository ?? "";
  if (!tag || !repository) {
    return null;
  }
  return {
    repository,
    tag,
    checksums: { ...(latest.checksums ?? {}) },
  };
});

const errorText = computed(() => {
  if (query.data.value) {
    return "";
  }
  if (!query.error.value) {
    return "";
  }
  return t("updates.loadFailed");
});

const latestTag = computed(() => {
  const fromList = query.data.value?.latestTag ?? "";
  if (fromList) {
    return fromList;
  }
  const fromCheck = checkLatestQuery.data.value;
  if (fromCheck && !fromCheck.checkError) {
    return fromCheck.tag ?? "";
  }
  return "";
});

const subtitle = computed(() => {
  if (latestTag.value) {
    return t("updates.latest", { tag: latestTag.value });
  }
  if (query.data.value?.checkError || checkLatestQuery.data.value?.checkError) {
    return t("updates.latestUnknown");
  }
  return t("updates.subtitle", { count: nodes.value.length });
});

function isGithubOwnerRepo(repository: string): boolean {
  return /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository);
}

const githubReleaseUrl = computed(() => {
  const tag = latestTag.value.trim();
  const repo = String(checkLatestQuery.data.value?.repository ?? "").trim();
  if (!tag || !isGithubOwnerRepo(repo)) {
    return "";
  }
  const [owner, name] = repo.split("/");
  return `https://github.com/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/releases/tag/${encodeURIComponent(tag)}`;
});

const isSelfTarget = computed(() => {
  const node = pendingNode.value;
  return Boolean(node && localNodeId.value && node.nodeId === localNodeId.value);
});

const emptyColspan = computed(() => (canManage.value ? 6 : 5));
const voterCount = computed(() =>
  (raftQuery.data.value?.nodes ?? []).filter((node) => {
    const role = String(node.raftRole ?? "").toUpperCase();
    const roleFreshness = String(node.raftRoleFreshness ?? "").toUpperCase();
    return (role === RAFT_LEADER || role === RAFT_VOTER) && roleFreshness === LIVE;
  }).length,
);
const showRaftWarning = computed(() => voterCount.value < 3);

const confirmMessage = computed(() => {
  const node = pendingNode.value;
  if (!node) {
    return "";
  }
  const hostname = node.hostname || node.nodeId;
  const tag = pin.value?.tag ?? "";
  const parts = [
    `${t("updates.confirmHostname")}: ${hostname}. ${t("updates.confirmPin")}: ${tag}. ${t("updates.confirmNoRestart")}`,
  ];
  if (isSelfTarget.value) {
    parts.push(t("updates.confirmSelfWarning"));
  }
  if (applyError.value) {
    parts.push(applyError.value);
  }
  return parts.join(" ");
});

const clusterConfirmMessage = computed(() => {
  const nextPin = pin.value;
  if (!nextPin) {
    return clusterError.value;
  }
  const parts = [
    t("updates.clusterConfirmPin", { repository: nextPin.repository, tag: nextPin.tag }),
    t("updates.confirmNoRestart"),
  ];
  if (showRaftWarning.value) {
    parts.push(t("updates.clusterConfirmRaftWarning"));
  }
  if (clusterError.value) {
    parts.push(clusterError.value);
  }
  return parts.join(" ");
});

const dialogOpen = computed(() => Boolean(pendingNode.value) || clusterConfirmOpen.value);
const dialogTitle = computed(() =>
  clusterConfirmOpen.value ? t("updates.clusterConfirmTitle") : t("updates.confirmTitle"),
);
const dialogMessage = computed(() =>
  clusterConfirmOpen.value ? clusterConfirmMessage.value : confirmMessage.value,
);

const applyMut = useMutation({
  mutationFn: (args: { nodeId: string; pin: UpdatePin }) =>
    client.applyNode({
      meta: { operationId: newOperationId() },
      nodeId: args.nodeId,
      pin: args.pin,
    }),
});

const clusterMut = useMutation({
  mutationFn: (nextPin: UpdatePin) =>
    client.createClusterUpdate({
      meta: { operationId: newOperationId() },
      pin: nextPin,
    }),
});

const cancelMut = useMutation({
  mutationFn: (jobId: string) =>
    client.cancelRemaining({
      meta: { operationId: newOperationId() },
      jobId,
    }),
});

const retryMut = useMutation({
  mutationFn: (jobId: string) =>
    client.retryUpdateJob({
      meta: { operationId: newOperationId() },
      jobId,
    }),
});

const applying = computed(() => applyMut.isPending.value);
const clustering = computed(() => clusterMut.isPending.value);
const jobActing = computed(() => cancelMut.isPending.value || retryMut.isPending.value);
const dialogPending = computed(() => (clusterConfirmOpen.value ? clustering.value : applying.value));
const mutating = computed(
  () => applying.value || clustering.value || jobActing.value || overlayOpen.value,
);
const canClusterUpdate = computed(
  () =>
    canManageCluster.value &&
    eligibleNodes.value.length > 0 &&
    pin.value != null &&
    !mutating.value &&
    !loading.value,
);
const clusterDisableReason = computed(() => {
  if (!canManageCluster.value || loading.value || mutating.value) {
    return "";
  }
  if (!pin.value) {
    return t("updates.clusterUpdateDisabledNoPin");
  }
  if (!eligibleNodes.value.length) {
    return t("updates.clusterUpdateDisabledNoEligible");
  }
  return "";
});
const jobErrorText = computed(() => {
  if (jobActionError.value) {
    return jobActionError.value;
  }
  if (jobsQuery.data.value || !jobsQuery.error.value) {
    return "";
  }
  return t("updates.jobs.loadFailed");
});

function freshnessOf(value: string): Freshness {
  if (value === LIVE || value === STALE || value === UNKNOWN) {
    return value;
  }
  return UNKNOWN;
}

function platform(os: string, arch: string): string {
  if (os && arch) {
    return `${os}/${arch}`;
  }
  return os || arch || t("updates.platformUnknown");
}

function isSkipReason(value: string): value is SkipReason {
  return value in SKIP_KEYS;
}

function statusLabel(eligible: boolean, skipReason: string): string {
  if (eligible) {
    return t("updates.status.eligible");
  }
  if (isSkipReason(skipReason)) {
    return t(SKIP_KEYS[skipReason]);
  }
  return t("updates.skip.unknown");
}

function statusTone(eligible: boolean, skipReason: string, freshness: string): string {
  if (freshness === STALE || freshness === UNKNOWN) {
    return "warn";
  }
  if (eligible) {
    return "warn";
  }
  switch (skipReason) {
    case "FAILED":
      return "danger";
    case "CURRENT":
      return "ok";
    case "BUSY":
    case "STALE":
    case "SUSPECT":
    case "TIMEOUT":
    case "UNAVAILABLE":
    case "CHECK_FAILED":
      return "warn";
    default:
      return "neutral";
  }
}

function rowClass(freshness: string): string {
  if (freshness === STALE || freshness === UNKNOWN) {
    return "row-stale";
  }
  return "";
}

function nodeHref(nodeId: string): string {
  return `/nodes/${encodeURIComponent(nodeId)}`;
}

function formatNodeAge(unixMs: number | bigint | undefined): string {
  const ms = Number(unixMs ?? 0);
  if (!Number.isFinite(ms) || ms <= 0) {
    return t("updates.updatedUnknown");
  }
  const seconds = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (seconds < 5) {
    return t("updates.updatedJustNow");
  }
  if (seconds < 60) {
    return t("updates.updatedSeconds", { count: seconds });
  }
  return t("updates.updatedMinutes", { count: Math.floor(seconds / 60) });
}

function canApply(node: StatusNode): boolean {
  return canManage.value && node.eligible && pin.value != null && !mutating.value;
}

function isHighlighted(nodeId: string): boolean {
  return Boolean(highlightedNodeId.value) && highlightedNodeId.value === nodeId;
}

function jobStatusLabel(status: string): string {
  const key = status.toUpperCase();
  if (key in JOB_STATUS_KEYS) {
    return t(JOB_STATUS_KEYS[key as keyof typeof JOB_STATUS_KEYS]);
  }
  return status || "—";
}

function targetStatusLabel(status: string): string {
  const key = status.toUpperCase();
  if (key in TARGET_STATUS_KEYS) {
    return t(TARGET_STATUS_KEYS[key as keyof typeof TARGET_STATUS_KEYS]);
  }
  return status || "—";
}

function jobStatusTone(status: string): string {
  switch (status.toUpperCase()) {
    case "COMPLETED":
    case "SUCCESS":
      return "ok";
    case "RUNNING":
    case "PENDING":
    case "PARTIAL":
    case "TIMEOUT":
    case "CANCELLED":
      return "warn";
    case "FAILED":
    case "CONFLICT":
      return "danger";
    default:
      return "neutral";
  }
}

function jobCounts(job: UpdateJobView): string {
  const summary = job.summary ?? {};
  return t("updates.jobs.countsSummary", {
    success: summary.success ?? 0,
    failed: summary.failed ?? 0,
    timeout: summary.timeout ?? 0,
    conflict: summary.conflict ?? 0,
    skipped: summary.skipped ?? 0,
    cancelled: summary.cancelled ?? 0,
  });
}

function formatJobTime(unixMs: number | bigint | undefined): string {
  const ms = Number(unixMs ?? 0);
  if (!Number.isFinite(ms) || ms <= 0) {
    return "—";
  }
  return new Date(ms).toISOString();
}

function canCancelJob(job: UpdateJobView): boolean {
  return canManageCluster.value && job.status.toUpperCase() === "RUNNING";
}

function canRetryJob(job: UpdateJobView): boolean {
  if (!canManageCluster.value) {
    return false;
  }
  const status = job.status.toUpperCase();
  if (status === "RUNNING" || status === "PENDING") {
    return false;
  }
  if (status === "PARTIAL" || status === "FAILED") {
    return true;
  }
  return (job.summary?.cancelled ?? 0) > 0;
}

function jobTargets(job: UpdateJobView): JobTarget[] {
  if (expandedJobId.value === job.jobId && jobDetailQuery.data.value?.job?.targets?.length) {
    return jobDetailQuery.data.value.job.targets;
  }
  return job.targets ?? [];
}

function skipItemLabel(node: StatusNode): string {
  return t("updates.clusterConfirmSkipItem", {
    hostname: node.hostname || node.nodeId,
    reason: statusLabel(false, node.skipReason),
  });
}

function isHardApplyFailure(err: unknown): boolean {
  if (!(err instanceof ConnectError)) {
    return false;
  }
  return (
    err.code === Code.InvalidArgument ||
    err.code === Code.PermissionDenied ||
    err.code === Code.FailedPrecondition
  );
}

function versionMatchesPin(version: string, tag: string): boolean {
  const current = stripV(version.trim());
  const target = stripV(tag.trim());
  return current !== "" && target !== "" && current === target;
}

function clearPoll(): void {
  if (pollTimer != null) {
    clearTimeout(pollTimer);
    pollTimer = null;
  }
}

function stopOverlay(): void {
  clearPoll();
  overlayOpen.value = false;
  overlayTimedOut.value = false;
  overlayPinTag.value = "";
  overlayStartedAt.value = 0;
  selfUpdateHold.value = false;
}

async function refreshStatus(): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ["updates", "checkLatest"] }),
    queryClient.invalidateQueries({ queryKey: ["updates", "nodeStatus"] }),
    queryClient.invalidateQueries({ queryKey: ["updates", "localInfo"] }),
  ]);
}

function finishOverlaySuccess(): void {
  reloadPage();
}

async function pollSelfUpdate(): Promise<void> {
  if (!overlayOpen.value) {
    return;
  }
  if (Date.now() - overlayStartedAt.value >= SELF_TIMEOUT_MS) {
    overlayTimedOut.value = true;
    clearPoll();
    return;
  }
  let matched = false;
  try {
    const info = await client.getLocalUpdateInfo({});
    matched = versionMatchesPin(info.version ?? "", overlayPinTag.value);
  } catch {
    // Agent may be restarting; keep the overlay and do not send the user to /login.
  }
  if (matched) {
    finishOverlaySuccess();
    return;
  }
  pollTimer = setTimeout(() => {
    void pollSelfUpdate();
  }, SELF_POLL_MS);
}

function startOverlay(tag: string): void {
  clearPoll();
  overlayPinTag.value = tag;
  overlayTimedOut.value = false;
  overlayOpen.value = true;
  overlayStartedAt.value = Date.now();
  selfUpdateHold.value = true;
  pendingNode.value = null;
  void pollSelfUpdate();
}

function openApply(node: StatusNode): void {
  if (!canApply(node) || overlayOpen.value || applying.value) {
    return;
  }
  applyError.value = "";
  clusterConfirmOpen.value = false;
  clusterError.value = "";
  pendingNode.value = node;
}

function openClusterConfirm(): void {
  if (!canClusterUpdate.value || overlayOpen.value) {
    return;
  }
  clusterError.value = "";
  applyError.value = "";
  pendingNode.value = null;
  clusterConfirmOpen.value = true;
}

function closeDialog(): void {
  if (applying.value || clustering.value) {
    return;
  }
  pendingNode.value = null;
  clusterConfirmOpen.value = false;
  applyError.value = "";
  clusterError.value = "";
}

async function refreshJobs(): Promise<void> {
  await queryClient.invalidateQueries({ queryKey: ["updates", "jobs"] });
}

async function onConfirmCluster(): Promise<void> {
  const nextPin = pin.value;
  if (!nextPin || clustering.value) {
    return;
  }
  clusterError.value = "";
  try {
    const resp = await clusterMut.mutateAsync(nextPin);
    clusterConfirmOpen.value = false;
    const jobId = resp.job?.jobId ?? "";
    if (jobId) {
      expandedJobId.value = jobId;
    }
    await Promise.all([refreshJobs(), refreshStatus()]);
  } catch {
    clusterError.value = t("updates.clusterCreateFailed");
  }
}

async function onConfirm(): Promise<void> {
  if (clusterConfirmOpen.value) {
    await onConfirmCluster();
    return;
  }
  const node = pendingNode.value;
  const nextPin = pin.value;
  if (!node || !nextPin || applying.value) {
    return;
  }
  const self = Boolean(localNodeId.value && node.nodeId === localNodeId.value);
  applyError.value = "";
  if (self) {
    startOverlay(nextPin.tag);
  }
  try {
    await applyMut.mutateAsync({ nodeId: node.nodeId, pin: nextPin });
    if (!self) {
      pendingNode.value = null;
      await refreshStatus();
    }
  } catch (err) {
    if (self && !isHardApplyFailure(err)) {
      return;
    }
    stopOverlay();
    applyError.value = t("updates.applyFailed");
    pendingNode.value = node;
  }
}

function toggleJob(jobId: string): void {
  expandedJobId.value = expandedJobId.value === jobId ? "" : jobId;
}

async function onCancelRemaining(jobId: string): Promise<void> {
  if (!canManageCluster.value || jobActing.value) {
    return;
  }
  jobActionError.value = "";
  try {
    await cancelMut.mutateAsync(jobId);
    if (expandedJobId.value === jobId) {
      await queryClient.invalidateQueries({ queryKey: ["updates", "jobs", jobId] });
    }
    await refreshJobs();
  } catch {
    jobActionError.value = t("updates.jobs.cancelFailed");
  }
}

async function onRetryJob(jobId: string): Promise<void> {
  if (!canManageCluster.value || jobActing.value) {
    return;
  }
  jobActionError.value = "";
  try {
    const resp = await retryMut.mutateAsync(jobId);
    const nextId = resp.job?.jobId || jobId;
    expandedJobId.value = nextId;
    await Promise.all([refreshJobs(), refreshStatus()]);
  } catch {
    jobActionError.value = t("updates.jobs.retryFailed");
  }
}

function reloadPage(): void {
  window.location.reload();
}

async function refresh(): Promise<void> {
  if (refreshing.value || loading.value) {
    return;
  }
  refreshing.value = true;
  try {
    await client.checkLatest({ refresh: true });
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["updates", "checkLatest"] }),
      queryClient.invalidateQueries({ queryKey: ["updates", "nodeStatus"] }),
    ]);
  } finally {
    refreshing.value = false;
  }
}

onUnmounted(() => {
  stopOverlay();
});
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <div class="eyebrow">{{ t("updates.eyebrow") }}</div>
        <h1>{{ t("updates.title") }}</h1>
        <p class="subtitle">
          <span>{{ subtitle }}</span>
          <a
            v-if="githubReleaseUrl"
            class="github-release-link cursor-pointer"
            data-action="github-release"
            :href="githubReleaseUrl"
            target="_blank"
            rel="noopener noreferrer"
            :aria-label="t('updates.githubRelease', { tag: latestTag })"
            :title="t('updates.githubRelease', { tag: latestTag })"
          >
            <Github :size="16" aria-hidden="true" />
          </a>
        </p>
      </div>
      <div class="header-actions">
          <span v-if="clusterDisableReason" id="cluster-update-reason" class="muted cluster-disabled-hint">
            {{ clusterDisableReason }}
          </span>
        <div v-if="canManageCluster" class="cluster-cta">
          <button
            type="button"
            class="btn btn-primary cursor-pointer"
            data-action="update-cluster"
            :disabled="!canClusterUpdate"
            :aria-describedby="clusterDisableReason ? 'cluster-update-reason' : undefined"
            :title="clusterDisableReason || undefined"
            :aria-busy="clustering ? true : undefined"
            @click="openClusterConfirm"
          >
            <LoaderCircle v-if="clustering" class="spin" :size="16" aria-hidden="true" />
            {{ t("updates.clusterUpdate") }}
          </button>
        </div>
        <button
          type="button"
          class="btn cursor-pointer"
          :disabled="refreshing || loading"
          :aria-busy="refreshing ? true : undefined"
          @click="refresh"
        >
          <LoaderCircle v-if="refreshing" class="spin" :size="16" aria-hidden="true" />
          <RefreshCw v-else :size="16" aria-hidden="true" />
          {{ t("updates.refresh") }}
        </button>
      </div>
    </header>

    <p v-if="errorText && !loading" class="error" role="alert">{{ errorText }}</p>
    <div
      v-else-if="loading"
      class="card table-card"
      aria-busy="true"
      :aria-label="t('updates.loading')"
    >
      <div class="skeleton-table">
        <div v-for="n in 5" :key="n" class="skeleton-row" />
      </div>
    </div>
    <div v-else class="card table-card">
      <table class="table updates-table">
        <thead>
          <tr>
            <th>{{ t("updates.table.hostname") }}</th>
            <th>{{ t("updates.table.platform") }}</th>
            <th>{{ t("updates.table.version") }}</th>
            <th>{{ t("updates.table.freshness") }}</th>
            <th>{{ t("updates.table.status") }}</th>
            <th v-if="canManage">{{ t("updates.table.actions") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="node in nodes"
            :key="node.nodeId"
            class="data-row"
            :class="[rowClass(node.freshness), { 'row-highlight': isHighlighted(node.nodeId) }]"
            :data-node="node.nodeId"
            :data-highlight="isHighlighted(node.nodeId) ? true : undefined"
          >
            <td>
              <div class="node-identity">
                <RouterLink class="name-link" :to="nodeHref(node.nodeId)">
                  {{ node.hostname || node.nodeId }}
                </RouterLink>
                <div v-if="node.hostname && node.hostname !== node.nodeId" class="mono muted node-id">
                  {{ node.nodeId }}
                </div>
              </div>
            </td>
            <td class="cell-platform">{{ platform(node.os, node.arch) }}</td>
            <td class="cell-version">{{ node.version || "—" }}</td>
            <td class="freshness-cell">
              <div class="freshness-wrap">
                <FreshnessBadge :status="freshnessOf(node.freshness)" />
                <span class="muted cell-updated" :title="t('updates.table.updated')">{{
                  formatNodeAge(node.lastUpdatedUnixMs)
                }}</span>
              </div>
            </td>
            <td>
              <span
                class="status-pill"
                :class="'status-' + statusTone(node.eligible, node.skipReason, node.freshness)"
                data-status
                :aria-label="t('updates.status.badgeLabel', { status: statusLabel(node.eligible, node.skipReason) })"
              >
                {{ statusLabel(node.eligible, node.skipReason) }}
              </span>
            </td>
            <td v-if="canManage" class="cell-actions">
              <button
                v-if="canApply(node)"
                type="button"
                class="btn btn-xs cursor-pointer"
                data-action="update"
                :disabled="applying || overlayOpen"
                @click="openApply(node)"
              >
                {{ t("updates.apply") }}
              </button>
            </td>
          </tr>
          <tr v-if="!nodes.length" class="empty-row">
            <td :colspan="emptyColspan" class="empty-cell">
              <div class="empty">
                <ArrowUpCircle :size="28" aria-hidden="true" />
                <p>{{ t("updates.empty") }}</p>
                <p class="muted">{{ t("updates.emptyHint") }}</p>
              </div>
            </td>
          </tr>
        </tbody>
      </table>

      <ul v-if="nodes.length" class="mobile-list">
        <li
          v-for="node in nodes"
          :key="'m-' + node.nodeId"
          class="node-card"
          :class="[rowClass(node.freshness), { 'row-highlight': isHighlighted(node.nodeId) }]"
          :data-node="node.nodeId"
          :data-highlight="isHighlighted(node.nodeId) ? true : undefined"
        >
          <div class="node-card-head">
            <div class="node-identity">
              <RouterLink class="name-link" :to="nodeHref(node.nodeId)">
                {{ node.hostname || node.nodeId }}
              </RouterLink>
              <div v-if="node.hostname && node.hostname !== node.nodeId" class="mono muted node-id">
                {{ node.nodeId }}
              </div>
            </div>
            <span
              class="status-pill"
              :class="'status-' + statusTone(node.eligible, node.skipReason, node.freshness)"
              data-status
              :aria-label="t('updates.status.badgeLabel', { status: statusLabel(node.eligible, node.skipReason) })"
            >
              {{ statusLabel(node.eligible, node.skipReason) }}
            </span>
          </div>
          <div class="node-card-pills">
            <span class="freshness-cell">
              <span class="freshness-wrap">
                <FreshnessBadge :status="freshnessOf(node.freshness)" />
                <span class="muted cell-updated" :title="t('updates.table.updated')">{{
                  formatNodeAge(node.lastUpdatedUnixMs)
                }}</span>
              </span>
            </span>
            <span class="muted">{{ platform(node.os, node.arch) }}</span>
            <span class="muted">{{ node.version || "—" }}</span>
          </div>
          <div v-if="canApply(node)" class="node-card-actions">
            <button
              type="button"
              class="btn btn-xs cursor-pointer"
              data-action="update"
              :disabled="applying || overlayOpen"
              @click="openApply(node)"
            >
              {{ t("updates.apply") }}
            </button>
          </div>
        </li>
      </ul>
      <div v-if="!nodes.length" class="mobile-empty">
        <div class="empty">
          <ArrowUpCircle :size="28" aria-hidden="true" />
          <p>{{ t("updates.empty") }}</p>
          <p class="muted">{{ t("updates.emptyHint") }}</p>
        </div>
      </div>
    </div>

    <section v-if="canReadJobs" class="jobs-section">
      <div class="jobs-header">
        <h2>{{ t("updates.jobs.title") }}</h2>
        <p class="muted">{{ t("updates.jobs.localOnly") }}</p>
      </div>
      <p v-if="jobErrorText" class="error" role="alert">{{ jobErrorText }}</p>
      <div
        v-if="jobsLoading"
        class="card table-card"
        aria-busy="true"
        :aria-label="t('updates.jobs.loading')"
      >
        <div class="skeleton-table">
          <div v-for="n in 3" :key="'job-skel-' + n" class="skeleton-row" />
        </div>
      </div>
      <div v-else-if="!jobs.length" class="empty-state" role="status">
        <Layers :size="28" aria-hidden="true" />
        <strong>{{ t("updates.jobs.empty") }}</strong>
        <span>{{ t("updates.jobs.emptyHint") }}</span>
      </div>
      <div v-else class="card table-card">
        <table class="table updates-table jobs-table">
          <thead>
            <tr>
              <th>{{ t("updates.jobs.status") }}</th>
              <th>{{ t("updates.jobs.pin") }}</th>
              <th>{{ t("updates.jobs.counts") }}</th>
              <th>{{ t("updates.jobs.created") }}</th>
              <th v-if="canManageCluster">{{ t("updates.table.actions") }}</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="job in jobs" :key="job.jobId">
              <tr class="data-row" :data-job="job.jobId">
                <td>
                  <div class="job-status-cell">
                    <button
                      type="button"
                      class="btn btn-xs ghost-btn cursor-pointer"
                      data-action="expand-job"
                      :aria-expanded="expandedJobId === job.jobId"
                      @click="toggleJob(job.jobId)"
                    >
                      <ChevronDown v-if="expandedJobId === job.jobId" :size="16" aria-hidden="true" />
                      <ChevronRight v-else :size="16" aria-hidden="true" />
                      {{ expandedJobId === job.jobId ? t("updates.jobs.collapse") : t("updates.jobs.expand") }}
                    </button>
                    <span
                      class="status-pill"
                      :class="'status-' + jobStatusTone(job.status)"
                      :aria-label="t('updates.jobs.statusLabel', { status: jobStatusLabel(job.status) })"
                    >
                      {{ jobStatusLabel(job.status) }}
                    </span>
                  </div>
                </td>
                <td class="cell-version">{{ job.pin?.tag || "—" }}</td>
                <td>{{ jobCounts(job) }}</td>
                <td class="cell-updated">{{ formatJobTime(job.createdUnixMs) }}</td>
                <td v-if="canManageCluster" class="cell-actions">
                  <button
                    v-if="canCancelJob(job)"
                    type="button"
                    class="btn btn-xs cursor-pointer"
                    data-action="cancel-remaining"
                    :disabled="jobActing"
                    :aria-busy="cancelMut.isPending ? true : undefined"
                    @click="onCancelRemaining(job.jobId)"
                  >
                    {{ t("updates.jobs.cancelRemaining") }}
                  </button>
                  <button
                    v-if="canRetryJob(job)"
                    type="button"
                    class="btn btn-xs cursor-pointer"
                    data-action="retry-job"
                    :disabled="jobActing"
                    :aria-busy="retryMut.isPending ? true : undefined"
                    @click="onRetryJob(job.jobId)"
                  >
                    {{ t("updates.jobs.retry") }}
                  </button>
                </td>
              </tr>
              <tr v-if="expandedJobId === job.jobId" class="job-detail-row" :data-job-detail="job.jobId">
                <td :colspan="canManageCluster ? 5 : 4">
                  <p v-if="jobDetailQuery.isPending && !jobTargets(job).length" class="muted">
                    {{ t("updates.jobs.loading") }}
                  </p>
                  <table v-else class="table job-targets-table">
                    <thead>
                      <tr>
                        <th>{{ t("updates.jobs.hostname") }}</th>
                        <th>{{ t("updates.jobs.status") }}</th>
                        <th>{{ t("updates.jobs.skipReason") }}</th>
                        <th>{{ t("updates.jobs.error") }}</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="target in jobTargets(job)" :key="target.operationId || target.nodeId">
                        <td>{{ target.hostname || target.nodeId || "—" }}</td>
                        <td>
                          <span
                            class="status-pill"
                            :class="'status-' + jobStatusTone(target.status || '')"
                            :aria-label="t('updates.jobs.targetStatusLabel', { status: targetStatusLabel(target.status || '') })"
                          >
                            {{ targetStatusLabel(target.status || "") }}
                          </span>
                        </td>
                        <td>{{ target.skipReason ? statusLabel(false, target.skipReason) : "—" }}</td>
                        <td>{{ target.error || "—" }}</td>
                      </tr>
                      <tr v-if="!jobTargets(job).length">
                        <td colspan="4" class="muted">{{ t("updates.jobs.empty") }}</td>
                      </tr>
                    </tbody>
                  </table>
                </td>
              </tr>
            </template>
          </tbody>
        </table>

        <ul class="mobile-list job-mobile-list">
          <li v-for="job in jobs" :key="'m-job-' + job.jobId" class="node-card" :data-job="job.jobId">
            <div class="node-card-head">
              <span
                class="status-pill"
                :class="'status-' + jobStatusTone(job.status)"
                :aria-label="t('updates.jobs.statusLabel', { status: jobStatusLabel(job.status) })"
              >
                {{ jobStatusLabel(job.status) }}
              </span>
              <span class="muted">{{ job.pin?.tag || "—" }}</span>
            </div>
            <div class="node-card-pills">
              <span class="muted">{{ jobCounts(job) }}</span>
              <span class="muted">{{ formatJobTime(job.createdUnixMs) }}</span>
            </div>
            <div class="node-card-actions">
              <button
                type="button"
                class="btn btn-xs cursor-pointer"
                data-action="expand-job"
                :aria-expanded="expandedJobId === job.jobId"
                @click="toggleJob(job.jobId)"
              >
                {{ expandedJobId === job.jobId ? t("updates.jobs.collapse") : t("updates.jobs.expand") }}
              </button>
              <button
                v-if="canCancelJob(job)"
                type="button"
                class="btn btn-xs cursor-pointer"
                data-action="cancel-remaining"
                :disabled="jobActing"
                @click="onCancelRemaining(job.jobId)"
              >
                {{ t("updates.jobs.cancelRemaining") }}
              </button>
              <button
                v-if="canRetryJob(job)"
                type="button"
                class="btn btn-xs cursor-pointer"
                data-action="retry-job"
                :disabled="jobActing"
                @click="onRetryJob(job.jobId)"
              >
                {{ t("updates.jobs.retry") }}
              </button>
            </div>
            <div v-if="expandedJobId === job.jobId" class="job-mobile-targets">
              <div v-for="target in jobTargets(job)" :key="target.operationId || target.nodeId" class="muted">
                {{ target.hostname || target.nodeId || "—" }}
                · {{ targetStatusLabel(target.status || "") }}
                <span v-if="target.skipReason"> · {{ statusLabel(false, target.skipReason) }}</span>
                <span v-if="target.error"> · {{ target.error }}</span>
              </div>
            </div>
          </li>
        </ul>
      </div>
    </section>

    <ConfirmDialog
      :open="dialogOpen"
      :title="dialogTitle"
      :message="dialogMessage"
      :confirm-label="t('updates.confirm')"
      :cancel-label="t('actions.cancel')"
      :pending="dialogPending"
      @cancel="closeDialog"
      @confirm="onConfirm"
    >
      <template v-if="clusterConfirmOpen" #extra>
        <div>
          <h3>{{ t("updates.clusterConfirmWillUpdate") }}</h3>
          <ul data-cluster-will-update>
            <li v-for="node in eligibleNodes" :key="'will-' + node.nodeId">
              {{ node.hostname || node.nodeId }}
            </li>
          </ul>
        </div>
        <div>
          <h3>{{ t("updates.clusterConfirmSkipped") }}</h3>
          <ul data-cluster-skipped>
            <li v-for="node in skippedNodes" :key="'skip-' + node.nodeId">
              {{ skipItemLabel(node) }}
            </li>
          </ul>
        </div>
      </template>
    </ConfirmDialog>

    <div
      v-if="overlayOpen"
      class="self-update-overlay"
      data-self-update-overlay
      role="status"
      aria-live="polite"
    >
      <div class="self-update-panel">
        <LoaderCircle v-if="!overlayTimedOut" class="spin overlay-icon" :size="28" aria-hidden="true" />
        <TriangleAlert v-else class="overlay-icon" :size="28" aria-hidden="true" />
        <h2>{{ overlayTimedOut ? t("updates.overlayTimeout", { tag: overlayPinTag }) : t("updates.overlayTitle") }}</h2>
        <p v-if="!overlayTimedOut">{{ t("updates.overlayBody", { tag: overlayPinTag }) }}</p>
        <button
          v-if="overlayTimedOut"
          type="button"
          class="btn btn-primary cursor-pointer"
          data-action="reload-after-update"
          @click="reloadPage"
        >
          {{ t("updates.overlayRefresh") }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.page-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 1rem;
}

.eyebrow {
  color: var(--color-accent);
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

h1 {
  margin: 0.2rem 0 0;
  font-size: 1.5rem;
  font-weight: 700;
  letter-spacing: -0.02em;
}

.subtitle {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  margin: 0.3rem 0 0;
  color: var(--color-muted);
  font-size: 0.875rem;
  line-height: 1.5;
}

.github-release-link {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 1.75rem;
  height: 1.75rem;
  margin: -0.25rem 0;
  color: var(--color-muted);
  border-radius: 0.375rem;
}

.github-release-link:hover {
  color: var(--color-text);
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
  text-decoration: none;
}

.github-release-link:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.cluster-cta {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 0.25rem;
}

.cluster-disabled-hint {
  margin: 0;
  max-width: 16rem;
  text-align: right;
  font-size: 0.75rem;
  line-height: 1.4;
}

.error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
}

.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
}

.table-card {
  padding: 0;
  overflow-x: auto;
}

.updates-table {
  width: 100%;
  min-width: 40rem;
}

.updates-table th,
.updates-table td {
  padding: 0.65rem 0.7rem;
  vertical-align: middle;
}

.updates-table th:last-child,
.updates-table td:last-child {
  padding-right: 1rem;
}

.updates-table thead th {
  position: sticky;
  top: 0;
  z-index: 3;
  background: var(--color-card);
  box-shadow: inset 0 -1px 0 var(--color-border);
}

.node-identity {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
  min-width: 0;
}

.name-link {
  font-weight: 600;
  cursor: pointer;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}

.node-id {
  overflow-wrap: anywhere;
  font-size: 0.75rem;
}

.cell-platform,
.cell-version,
.cell-updated {
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.cell-actions {
  white-space: nowrap;
}

.freshness-wrap {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem 0.5rem;
  flex-wrap: wrap;
}

.muted {
  color: var(--color-muted);
  font-size: 0.875rem;
}

.updates-table tbody tr.row-stale,
.node-card.row-stale {
  background: color-mix(in srgb, var(--color-stale) 35%, transparent);
}

.updates-table tbody tr.row-highlight,
.node-card.row-highlight {
  outline: 2px solid var(--color-accent);
  outline-offset: -2px;
  background: color-mix(in srgb, var(--color-accent) 10%, var(--color-card));
}

.empty-cell {
  padding: 2.5rem 1rem !important;
}

.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.45rem;
  color: var(--color-muted);
  text-align: center;
}

.empty p {
  margin: 0;
}

.empty p:first-of-type {
  color: var(--color-text);
  font-weight: 600;
}

.skeleton-table {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 1rem;
}

.skeleton-row {
  height: 2.5rem;
  border-radius: 8px;
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
  animation: pulse 1.2s ease-in-out infinite;
}

.spin {
  animation: spin 800ms linear infinite;
}

a {
  color: var(--color-accent);
  text-decoration: none;
}

a:hover {
  text-decoration: underline;
}

.mobile-list,
.mobile-empty {
  display: none;
}

.self-update-overlay {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: grid;
  place-items: center;
  padding: 1rem;
  background: rgba(0, 0, 0, 0.55);
}

.self-update-panel {
  width: min(100%, 32rem);
  padding: 1.5rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  box-shadow: 0 1rem 3rem rgba(0, 0, 0, 0.3);
  color: var(--color-text);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.75rem;
  text-align: center;
}

.self-update-panel h2 {
  margin: 0;
  font-size: 1.125rem;
  font-weight: 650;
}

.self-update-panel p {
  margin: 0;
  color: var(--color-muted);
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.self-update-panel .btn {
  min-height: 2.75rem;
}

.overlay-icon {
  color: var(--color-accent);
}

.node-card-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}

.jobs-section {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

.jobs-header h2 {
  margin: 0;
  font-size: 1.05rem;
  font-weight: 650;
}

.jobs-header p {
  margin: 0.25rem 0 0;
}

.job-status-cell {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}

.ghost-btn {
  min-height: 2rem;
  padding: 0.2rem 0.5rem;
}

.job-detail-row td {
  background: color-mix(in srgb, var(--color-text) 3%, var(--color-card));
}

.job-targets-table {
  min-width: 28rem;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.5rem;
  padding: 2.5rem 1.5rem;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  text-align: center;
  color: var(--color-muted);
}

.empty-state strong {
  color: var(--color-text);
  font-size: 1rem;
}

.empty-state span {
  max-width: 28rem;
  font-size: 0.875rem;
  line-height: 1.5;
}

.job-mobile-targets {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
}

@keyframes pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.55;
  }
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
  }

  .header-actions {
    justify-content: space-between;
    flex-wrap: wrap;
  }

  .cluster-cta {
    align-items: stretch;
    flex: 1 1 12rem;
  }

  .cluster-disabled-hint {
    max-width: none;
    text-align: left;
  }

  .cluster-cta .btn,
  .header-actions > .btn {
    width: 100%;
    min-height: 2.75rem;
  }

  .updates-table {
    display: none;
  }

  .mobile-list {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .mobile-empty {
    display: block;
    padding: 2rem 1rem;
  }

  .node-card {
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
    padding: 0.9rem 0.85rem;
    border-bottom: 1px solid var(--color-border);
  }

  .node-card:last-child {
    border-bottom: 0;
  }

  .node-card-head,
  .node-card-pills {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.35rem 0.5rem;
  }

  .node-card-head {
    justify-content: space-between;
  }

  .node-card-actions .btn,
  .self-update-panel .btn {
    width: 100%;
    min-height: 2.75rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .skeleton-row,
  .spin {
    animation: none;
    transition: none;
  }
}
</style>
