<script setup lang="ts">
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { ArrowDown, ArrowUp, Layers, LoaderCircle, RefreshCw, Search, X } from "lucide-vue-next";
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import ProcessRowActions from "../components/ProcessRowActions.vue";
import Toast from "../components/Toast.vue";
import { STALE } from "../lib/freshness";
import { withTarget } from "../lib/headers";
import { newOperationId } from "../lib/opid";
import { useNodeClient, useProcessClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { useProcessState } from "../lib/useProcessState";
import { mapNode } from "./clusterView";
import {
  flattenClusterProcesses,
  formatRemoteError,
  mergeProcessRows,
  ownerDisplay,
  rowKey,
  rowsFromProcessViews,
  type ClusterProcessRow,
} from "./processView";

type ProcessAction = "start" | "stop" | "restart" | "kill";
type StatusFilter = "all" | "running" | "stopped" | "unhealthy" | "stale";
type SortKey = "name" | "owner" | "observed" | "health" | "revision" | "freshness";
type SortDir = "asc" | "desc";
type ToastType = "success" | "error" | "info" | "warning";

const ACTION_CONCURRENCY = 4;
const STATUS_FILTERS: StatusFilter[] = ["all", "running", "stopped", "unhealthy", "stale"];
const SORT_KEYS: SortKey[] = ["name", "owner", "observed", "health", "revision", "freshness"];

const { t } = useI18n();
const { translateDesiredState, translateObservedState, translateHealthState } = useProcessState();
const route = useRoute();
const router = useRouter();

const POLL_MS = 5000;
const nodes = useNodeClient();
const processes = useProcessClient();
const queryClient = useQueryClient();

function queryString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function parseStatus(value: unknown): StatusFilter {
  return STATUS_FILTERS.includes(value as StatusFilter) ? (value as StatusFilter) : "all";
}

function parseSort(value: unknown): SortKey {
  return SORT_KEYS.includes(value as SortKey) ? (value as SortKey) : "name";
}

function parseDir(value: unknown): SortDir {
  return value === "desc" ? "desc" : "asc";
}

const searchQuery = ref(queryString(route.query.q));
const groupFilter = ref(queryString(route.query.group));
const statusFilter = ref<StatusFilter>(parseStatus(route.query.status));
const sortKey = ref<SortKey>(parseSort(route.query.sort));
const sortDir = ref<SortDir>(parseDir(route.query.dir));
const selectedKeys = ref<string[]>([]);
const actingKeys = ref<string[]>([]);
const pending = ref<{ action: "stop" | "kill"; targets: ClusterProcessRow[] } | null>(null);
const refreshing = ref(false);
const nowMs = ref(Date.now());

const toastMessage = ref("");
const toastType = ref<ToastType>("info");
const showToast = ref(false);

const nodesQuery = useQuery({
  queryKey: ["nodes"],
  queryFn: () => nodes.listNodes({}),
  refetchInterval: POLL_MS,
});

const processesQuery = useQuery({
  queryKey: ["processes"],
  queryFn: () => processes.listProcesses({}),
  refetchInterval: POLL_MS,
});

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canStart = computed(() => perms.value.has("process.start"));
const canStop = computed(() => perms.value.has("process.stop"));
const canRestart = computed(() => perms.value.has("process.restart"));

const allRows = computed(() => {
  const now = Date.now();
  const nodeList = nodesQuery.data.value?.nodes ?? [];
  const gossip = flattenClusterProcesses(nodeList, now);
  const listed = rowsFromProcessViews(processesQuery.data.value?.processes ?? [], now);
  const hosts = new Map(
    nodeList.map((raw) => {
      const node = mapNode(raw, now);
      return [node.nodeId, node.hostname] as const;
    }),
  );
  return mergeProcessRows(gossip, listed).map((row) => ({
    ...row,
    ownerHostname: row.ownerHostname || hosts.get(row.ownerNodeId) || "",
  }));
});

const groupOptions = computed(() =>
  [...new Set(allRows.value.map((row) => row.group).filter(Boolean))].sort((a, b) => a.localeCompare(b)),
);

const stats = computed(() => {
  const list = allRows.value;
  return {
    total: list.length,
    running: list.filter((row) => row.observed === "RUNNING").length,
    stopped: list.filter((row) => row.observed === "STOPPED").length,
    unhealthy: list.filter(
      (row) => row.health === "UNHEALTHY" || row.observed === "FATAL" || row.observed === "BACKOFF",
    ).length,
    stale: list.filter((row) => row.freshness === STALE).length,
  };
});

const filteredRows = computed(() => {
  const query = searchQuery.value.trim().toLowerCase();
  return allRows.value.filter((row) => {
    if (groupFilter.value && row.group !== groupFilter.value) {
      return false;
    }
    if (query) {
      const haystack = [row.name, row.processId, row.group, row.ownerHostname, row.ownerNodeId]
        .join(" ")
        .toLowerCase();
      if (!haystack.includes(query)) {
        return false;
      }
    }
    if (statusFilter.value === "running" && row.observed !== "RUNNING") {
      return false;
    }
    if (statusFilter.value === "stopped" && row.observed !== "STOPPED") {
      return false;
    }
    if (
      statusFilter.value === "unhealthy" &&
      row.health !== "UNHEALTHY" &&
      row.observed !== "FATAL" &&
      row.observed !== "BACKOFF"
    ) {
      return false;
    }
    if (statusFilter.value === "stale" && row.freshness !== STALE) {
      return false;
    }
    return true;
  });
});

const rows = computed(() => {
  const list = filteredRows.value.slice();
  const dir = sortDir.value === "asc" ? 1 : -1;
  list.sort((a, b) => compareRows(a, b, sortKey.value) * dir);
  return list;
});

const loading = computed(
  () =>
    nodesQuery.isPending.value &&
    !nodesQuery.data.value &&
    processesQuery.isPending.value &&
    !processesQuery.data.value,
);

const errorText = computed(() => {
  if (processesQuery.data.value || nodesQuery.data.value) {
    return "";
  }
  const err = processesQuery.error.value ?? nodesQuery.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

const filtersActive = computed(
  () => Boolean(searchQuery.value.trim() || groupFilter.value || statusFilter.value !== "all"),
);

const visibleKeys = computed(() => rows.value.map(rowKey));
const selectedVisibleCount = computed(
  () => visibleKeys.value.filter((key) => selectedKeys.value.includes(key)).length,
);
const allVisibleSelected = computed(
  () => visibleKeys.value.length > 0 && selectedVisibleCount.value === visibleKeys.value.length,
);
const someVisibleSelected = computed(() => selectedVisibleCount.value > 0 && !allVisibleSelected.value);

const selectedRows = computed(() => {
  const selected = new Set(selectedKeys.value);
  return allRows.value.filter((row) => selected.has(rowKey(row)));
});

const acting = computed(() => actingKeys.value.length > 0);
const confirmOpen = computed(() => Boolean(pending.value));
const confirmIsForce = computed(() => pending.value?.action === "kill");
const confirmTitle = computed(() => {
  const current = pending.value;
  if (!current) {
    return "";
  }
  if (current.action === "kill") {
    return current.targets.length === 1 ? t("processes.confirm.forceStopTitle") : t("processes.confirm.bulkForceStopTitle");
  }
  return current.targets.length === 1 ? t("processes.confirm.stopTitle") : t("processes.confirm.bulkStopTitle");
});
const confirmMessage = computed(() => {
  const current = pending.value;
  if (!current) {
    return "";
  }
  const name = current.targets[0]?.name ?? "";
  if (current.action === "kill") {
    return current.targets.length === 1
      ? t("processes.confirm.forceStopMessage", { name })
      : t("processes.confirm.bulkForceStopMessage", { count: current.targets.length });
  }
  return current.targets.length === 1
    ? t("processes.confirm.stopMessage", { name })
    : t("processes.confirm.bulkStopMessage", { count: current.targets.length });
});
const confirmLabel = computed(() =>
  confirmIsForce.value ? t("processes.confirm.confirmForceStop") : t("processes.confirm.confirmStop"),
);

const lastUpdatedLabel = computed(() => {
  nowMs.value;
  const stamp = Math.max(nodesQuery.dataUpdatedAt.value || 0, processesQuery.dataUpdatedAt.value || 0);
  if (!stamp) {
    return t("processes.updatedJustNow");
  }
  const seconds = Math.max(0, Math.floor((Date.now() - stamp) / 1000));
  if (seconds < 5) {
    return t("processes.updatedJustNow");
  }
  if (seconds < 60) {
    return t("processes.updatedSeconds", { count: seconds });
  }
  return t("processes.updatedMinutes", { count: Math.floor(seconds / 60) });
});

const subtitle = computed(() => {
  if (filtersActive.value) {
    return t("processes.showing", { shown: rows.value.length, total: stats.value.total });
  }
  return t("processes.subtitle", { count: stats.value.total });
});

watch(allRows, (next) => {
  const valid = new Set(next.map(rowKey));
  if (selectedKeys.value.some((key) => !valid.has(key))) {
    selectedKeys.value = selectedKeys.value.filter((key) => valid.has(key));
  }
});

watch(
  [searchQuery, groupFilter, statusFilter, sortKey, sortDir],
  () => {
    const query: Record<string, string> = {};
    const search = searchQuery.value.trim();
    if (search) {
      query.q = search;
    }
    if (groupFilter.value) {
      query.group = groupFilter.value;
    }
    if (statusFilter.value !== "all") {
      query.status = statusFilter.value;
    }
    if (sortKey.value !== "name") {
      query.sort = sortKey.value;
    }
    if (sortDir.value !== "asc") {
      query.dir = sortDir.value;
    }
    const same =
      queryString(route.query.q) === (query.q ?? "") &&
      queryString(route.query.group) === (query.group ?? "") &&
      queryString(route.query.status) === (query.status ?? "") &&
      queryString(route.query.sort) === (query.sort ?? "") &&
      queryString(route.query.dir) === (query.dir ?? "");
    if (!same) {
      void router.replace({ query });
    }
  },
);

function compareRows(a: ClusterProcessRow, b: ClusterProcessRow, key: SortKey): number {
  if (key === "revision") {
    return a.latestRevision - b.latestRevision || a.activeRevision - b.activeRevision;
  }
  if (key === "freshness") {
    return a.freshnessUnixMs - b.freshnessUnixMs;
  }
  const left =
    key === "owner" ? ownerDisplay(a.ownerHostname, a.ownerNodeId) : String(a[key] ?? "");
  const right =
    key === "owner" ? ownerDisplay(b.ownerHostname, b.ownerNodeId) : String(b[key] ?? "");
  return left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" });
}

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function notify(message: string, type: ToastType): void {
  toastMessage.value = message;
  toastType.value = type;
  showToast.value = false;
  requestAnimationFrame(() => {
    showToast.value = true;
  });
}

function isSelected(key: string): boolean {
  return selectedKeys.value.includes(key);
}

function toggleRow(key: string): void {
  if (selectedKeys.value.includes(key)) {
    selectedKeys.value = selectedKeys.value.filter((item) => item !== key);
    return;
  }
  selectedKeys.value = [...selectedKeys.value, key];
}

function toggleSelectAll(): void {
  if (allVisibleSelected.value) {
    const drop = new Set(visibleKeys.value);
    selectedKeys.value = selectedKeys.value.filter((key) => !drop.has(key));
    return;
  }
  const next = new Set(selectedKeys.value);
  for (const key of visibleKeys.value) {
    next.add(key);
  }
  selectedKeys.value = [...next];
}

function clearSelection(): void {
  selectedKeys.value = [];
}

function clearFilters(): void {
  searchQuery.value = "";
  groupFilter.value = "";
  statusFilter.value = "all";
}

function setStatusFilter(next: StatusFilter): void {
  statusFilter.value = statusFilter.value === next ? "all" : next;
}

function toggleSort(key: SortKey): void {
  if (sortKey.value === key) {
    sortDir.value = sortDir.value === "asc" ? "desc" : "asc";
    return;
  }
  sortKey.value = key;
  sortDir.value = "asc";
}

function sortAria(key: SortKey): "ascending" | "descending" | "none" {
  if (sortKey.value !== key) {
    return "none";
  }
  return sortDir.value === "asc" ? "ascending" : "descending";
}

function sortLabel(key: SortKey, column: string): string {
  const nextDir = sortKey.value === key && sortDir.value === "asc" ? "desc" : "asc";
  return nextDir === "asc" ? t("processes.sortAsc", { column }) : t("processes.sortDesc", { column });
}

function processHref(row: ClusterProcessRow): { path: string; query: { node?: string } } {
  return {
    path: `/processes/${encodeURIComponent(row.name)}`,
    query: row.ownerNodeId ? { node: row.ownerNodeId } : {},
  };
}

function openRow(event: MouseEvent, row: ClusterProcessRow): void {
  const target = event.target as HTMLElement | null;
  if (target?.closest("a, button, input, label, .row-actions")) {
    return;
  }
  void router.push(processHref(row));
}

function toneForObserved(state: string): string {
  switch (state) {
    case "RUNNING":
      return "ok";
    case "STARTING":
    case "STOPPING":
    case "BACKOFF":
    case "EXITED":
      return "warn";
    case "FATAL":
      return "danger";
    default:
      return "neutral";
  }
}

function toneForHealth(state: string): string {
  if (state === "HEALTHY") {
    return "ok";
  }
  if (state === "UNHEALTHY") {
    return "danger";
  }
  return "neutral";
}

function hasPermission(action: ProcessAction): boolean {
  if (action === "start") {
    return canStart.value;
  }
  if (action === "restart") {
    return canRestart.value;
  }
  return canStop.value;
}

function callAction(action: ProcessAction, row: ClusterProcessRow) {
  const opts = { headers: withTarget(row.ownerNodeId) };
  const req = { meta: mutationMeta(), idOrName: row.name };
  if (action === "start") {
    return processes.startProcess(req, opts);
  }
  if (action === "stop") {
    return processes.stopProcess(req, opts);
  }
  if (action === "restart") {
    return processes.restartProcess(req, opts);
  }
  return processes.killProcess(req, opts);
}

async function runOnTargets(action: ProcessAction, targets: ClusterProcessRow[]): Promise<void> {
  const eligible: ClusterProcessRow[] = [];
  const skipped: string[] = [];
  for (const row of targets) {
    if (!row.ownerNodeId) {
      skipped.push(t("processes.toast.missingOwner", { name: row.name }));
      continue;
    }
    if (!hasPermission(action)) {
      skipped.push(t("processes.toast.noPermission"));
      continue;
    }
    eligible.push(row);
  }

  if (!eligible.length) {
    notify(skipped[0] || t("processes.toast.failed", { detail: t("processes.toast.noPermission") }), "error");
    return;
  }

  actingKeys.value = eligible.map(rowKey);
  const failures: string[] = [...skipped];
  let success = 0;

  try {
    for (let i = 0; i < eligible.length; i += ACTION_CONCURRENCY) {
      const chunk = eligible.slice(i, i + ACTION_CONCURRENCY);
      const settled = await Promise.allSettled(chunk.map((row) => callAction(action, row)));
      settled.forEach((result, index) => {
        const row = chunk[index];
        if (result.status === "fulfilled") {
          success += 1;
          return;
        }
        failures.push(`${row.name}: ${formatRemoteError(result.reason)}`);
      });
    }
  } finally {
    actingKeys.value = [];
    await queryClient.invalidateQueries({ queryKey: ["processes"] });
    await queryClient.invalidateQueries({ queryKey: ["nodes"] });
  }

  const total = eligible.length + skipped.length;
  if (success > 0 && failures.length === 0) {
    notify(successToast(action, targets, success), "success");
    if (targets.length > 1) {
      clearSelection();
    }
    return;
  }
  if (success > 0) {
    notify(
      t("processes.toast.partial", { success, total, detail: failures.slice(0, 3).join(" · ") }),
      "warning",
    );
    return;
  }
  notify(t("processes.toast.failed", { detail: failures.slice(0, 3).join(" · ") }), "error");
}

function successToast(action: ProcessAction, targets: ClusterProcessRow[], success: number): string {
  if (success === 1) {
    const name = targets[0]?.name ?? "";
    if (action === "start") {
      return t("processes.toast.startOne", { name });
    }
    if (action === "stop") {
      return t("processes.toast.stopOne", { name });
    }
    if (action === "restart") {
      return t("processes.toast.restartOne", { name });
    }
    return t("processes.toast.forceStopOne", { name });
  }
  if (action === "start") {
    return t("processes.toast.startMany", { count: success });
  }
  if (action === "stop") {
    return t("processes.toast.stopMany", { count: success });
  }
  if (action === "restart") {
    return t("processes.toast.restartMany", { count: success });
  }
  return t("processes.toast.forceStopMany", { count: success });
}

function requestAction(action: ProcessAction, targets: ClusterProcessRow[]): void {
  if (acting.value || !targets.length) {
    return;
  }
  if (action === "stop" || action === "kill") {
    pending.value = { action, targets };
    return;
  }
  void runOnTargets(action, targets);
}

function cancelPending(): void {
  if (!acting.value) {
    pending.value = null;
  }
}

async function confirmPending(): Promise<void> {
  const current = pending.value;
  if (!current || acting.value) {
    return;
  }
  pending.value = null;
  await runOnTargets(current.action, current.targets);
}

async function refresh(): Promise<void> {
  if (refreshing.value) {
    return;
  }
  refreshing.value = true;
  try {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["processes"] }),
      queryClient.invalidateQueries({ queryKey: ["nodes"] }),
    ]);
  } finally {
    refreshing.value = false;
    nowMs.value = Date.now();
  }
}

function onDocumentKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape" && !confirmOpen.value && selectedKeys.value.length) {
    clearSelection();
  }
}

let tick: ReturnType<typeof setInterval> | undefined;

onMounted(() => {
  document.addEventListener("keydown", onDocumentKeydown);
  tick = setInterval(() => {
    nowMs.value = Date.now();
  }, 5000);
});

onUnmounted(() => {
  document.removeEventListener("keydown", onDocumentKeydown);
  if (tick) {
    clearInterval(tick);
  }
});
</script>

<template>
  <div class="page" :class="{ 'has-bulk': selectedRows.length }">
    <header class="page-header">
      <div>
        <div class="eyebrow">{{ t("processes.eyebrow") }}</div>
        <h1>{{ t("processes.title") }}</h1>
        <p class="subtitle">{{ subtitle }}</p>
      </div>
      <div class="header-actions">
        <span class="updated">{{ t("processes.lastUpdated", { age: lastUpdatedLabel }) }}</span>
        <button type="button" class="btn" :disabled="refreshing || loading" @click="refresh">
          <LoaderCircle v-if="refreshing" class="spin" :size="16" aria-hidden="true" />
          <RefreshCw v-else :size="16" aria-hidden="true" />
          {{ t("processes.refresh") }}
        </button>
      </div>
    </header>

    <div class="summary" role="toolbar" :aria-label="t('processes.title')">
      <button
        type="button"
        class="summary-item"
        :class="{ active: statusFilter === 'all' }"
        :aria-pressed="statusFilter === 'all'"
        @click="statusFilter = 'all'"
      >
        <span class="summary-value">{{ stats.total }}</span>
        <span class="summary-label">{{ t("processes.stats.total") }}</span>
      </button>
      <button
        type="button"
        class="summary-item"
        :class="{ active: statusFilter === 'running' }"
        :aria-pressed="statusFilter === 'running'"
        @click="setStatusFilter('running')"
      >
        <span class="summary-value" :class="{ ok: stats.running > 0 }">{{ stats.running }}</span>
        <span class="summary-label">{{ t("processes.stats.running") }}</span>
      </button>
      <button
        type="button"
        class="summary-item"
        :class="{ active: statusFilter === 'stopped' }"
        :aria-pressed="statusFilter === 'stopped'"
        @click="setStatusFilter('stopped')"
      >
        <span class="summary-value">{{ stats.stopped }}</span>
        <span class="summary-label">{{ t("processes.stats.stopped") }}</span>
      </button>
      <button
        type="button"
        class="summary-item"
        :class="{ active: statusFilter === 'unhealthy', alert: stats.unhealthy > 0 }"
        :aria-pressed="statusFilter === 'unhealthy'"
        @click="setStatusFilter('unhealthy')"
      >
        <span class="summary-value" :class="{ danger: stats.unhealthy > 0 }">{{ stats.unhealthy }}</span>
        <span class="summary-label">{{ t("processes.stats.unhealthy") }}</span>
      </button>
      <button
        type="button"
        class="summary-item"
        :class="{ active: statusFilter === 'stale', alert: stats.stale > 0 }"
        :aria-pressed="statusFilter === 'stale'"
        @click="setStatusFilter('stale')"
      >
        <span class="summary-value" :class="{ warn: stats.stale > 0 }">{{ stats.stale }}</span>
        <span class="summary-label">{{ t("processes.stats.stale") }}</span>
      </button>
    </div>

    <div class="toolbar">
      <label class="search">
        <span class="field-label">{{ t("processes.search") }}</span>
        <span class="search-wrap">
          <Search class="search-icon" :size="16" aria-hidden="true" />
          <input
            v-model="searchQuery"
            class="input search-input"
            name="search"
            type="search"
            :placeholder="t('processes.searchPlaceholder')"
            autocomplete="off"
          />
        </span>
      </label>
      <label class="field">
        <span class="field-label">{{ t("processes.filterGroup") }}</span>
        <select v-model="groupFilter" class="input" name="group">
          <option value="">{{ t("processes.filterAllGroups") }}</option>
          <option v-for="group in groupOptions" :key="group" :value="group">{{ group }}</option>
        </select>
      </label>
      <button
        v-if="filtersActive"
        type="button"
        class="btn clear-btn"
        @click="clearFilters"
      >
        <X :size="16" aria-hidden="true" />
        {{ t("processes.clearFilters") }}
      </button>
    </div>

    <p v-if="errorText && !loading" class="error" role="alert">{{ errorText }}</p>
    <div v-else-if="loading" class="card table-card" aria-busy="true" :aria-label="t('processes.loading')">
      <div class="skeleton-table">
        <div v-for="n in 5" :key="n" class="skeleton-row" />
      </div>
    </div>
    <div v-else class="card table-card">
      <div v-if="rows.length" class="mobile-toolbar">
        <label class="check-wrap">
          <input
            class="row-check"
            type="checkbox"
            :checked="allVisibleSelected"
            :indeterminate.prop="someVisibleSelected"
            :aria-label="t('processes.selectAll')"
            @change="toggleSelectAll"
          />
          <span>{{ t("processes.selectVisible") }}</span>
        </label>
        <span class="muted">{{ rows.length }}</span>
      </div>
      <ul v-if="rows.length" class="mobile-list">
        <li
          v-for="row in rows"
          :key="'m-' + rowKey(row)"
          class="proc-card"
          :class="{ selected: isSelected(rowKey(row)), acting: actingKeys.includes(rowKey(row)) }"
          @click="openRow($event, row)"
        >
          <label class="check-wrap">
            <input
              class="row-check"
              type="checkbox"
              :checked="isSelected(rowKey(row))"
              :aria-label="t('processes.selectRow', { name: row.name })"
              @change="toggleRow(rowKey(row))"
            />
          </label>
          <div class="proc-card-body">
            <div class="proc-card-head">
              <RouterLink class="name-link" :to="processHref(row)">{{ row.name }}</RouterLink>
              <span
                v-if="row.activeRevision !== row.latestRevision"
                class="restart-chip"
                :title="t('processes.restartRequired')"
              >
                {{ t("processes.restartRequired") }}
              </span>
            </div>
            <div class="proc-card-meta">
              <RouterLink
                v-if="row.ownerNodeId"
                :to="`/nodes/${encodeURIComponent(row.ownerNodeId)}`"
              >
                {{ ownerDisplay(row.ownerHostname, row.ownerNodeId) }}
              </RouterLink>
              <span v-else>{{ ownerDisplay(row.ownerHostname, row.ownerNodeId) }}</span>
              <span v-if="row.group" class="group-chip">{{ row.group }}</span>
            </div>
            <div class="proc-card-pills">
              <span v-if="row.observed" class="state-pill" :class="toneForObserved(row.observed)">
                {{ translateObservedState(row.observed) }}
              </span>
              <span
                v-if="row.desired && row.desired !== row.observed"
                class="desired-hint"
              >
                {{ t("processes.desiredMismatch", { state: translateDesiredState(row.desired) }) }}
              </span>
              <span v-if="row.health" class="state-pill" :class="toneForHealth(row.health)">
                {{ translateHealthState(row.health) }}
              </span>
              <FreshnessBadge :status="row.freshness" />
            </div>
          </div>
          <ProcessRowActions
            :row="row"
            :acting="acting"
            :can-start="canStart"
            :can-stop="canStop"
            :can-restart="canRestart"
            @action="requestAction($event, [row])"
          />
        </li>
      </ul>
      <table class="table process-table">
        <thead>
          <tr>
            <th class="cell-select">
              <label class="check-wrap">
                <input
                  class="row-check"
                  type="checkbox"
                  :checked="allVisibleSelected"
                  :indeterminate.prop="someVisibleSelected"
                  :disabled="!rows.length"
                  :aria-label="t('processes.selectAll')"
                  @change="toggleSelectAll"
                />
              </label>
            </th>
            <th :aria-sort="sortAria('name')">
              <button type="button" class="sort-btn" :aria-label="sortLabel('name', t('processes.table.name'))" @click="toggleSort('name')">
                {{ t("processes.table.name") }}
                <ArrowUp v-if="sortKey === 'name' && sortDir === 'asc'" :size="14" />
                <ArrowDown v-else-if="sortKey === 'name'" :size="14" />
              </button>
            </th>
            <th :aria-sort="sortAria('owner')">
              <button type="button" class="sort-btn" :aria-label="sortLabel('owner', t('processes.table.owner'))" @click="toggleSort('owner')">
                {{ t("processes.table.owner") }}
                <ArrowUp v-if="sortKey === 'owner' && sortDir === 'asc'" :size="14" />
                <ArrowDown v-else-if="sortKey === 'owner'" :size="14" />
              </button>
            </th>
            <th :aria-sort="sortAria('observed')">
              <button type="button" class="sort-btn" :aria-label="sortLabel('observed', t('processes.table.observed'))" @click="toggleSort('observed')">
                {{ t("processes.table.observed") }}
                <ArrowUp v-if="sortKey === 'observed' && sortDir === 'asc'" :size="14" />
                <ArrowDown v-else-if="sortKey === 'observed'" :size="14" />
              </button>
            </th>
            <th :aria-sort="sortAria('health')">
              <button type="button" class="sort-btn" :aria-label="sortLabel('health', t('processes.table.health'))" @click="toggleSort('health')">
                {{ t("processes.table.health") }}
                <ArrowUp v-if="sortKey === 'health' && sortDir === 'asc'" :size="14" />
                <ArrowDown v-else-if="sortKey === 'health'" :size="14" />
              </button>
            </th>
            <th :aria-sort="sortAria('revision')">
              <button type="button" class="sort-btn" :aria-label="sortLabel('revision', t('processes.table.revisions'))" @click="toggleSort('revision')">
                {{ t("processes.table.revisions") }}
                <ArrowUp v-if="sortKey === 'revision' && sortDir === 'asc'" :size="14" />
                <ArrowDown v-else-if="sortKey === 'revision'" :size="14" />
              </button>
            </th>
            <th :aria-sort="sortAria('freshness')">
              <button type="button" class="sort-btn" :aria-label="sortLabel('freshness', t('processes.table.freshness'))" @click="toggleSort('freshness')">
                {{ t("processes.table.freshness") }}
                <ArrowUp v-if="sortKey === 'freshness' && sortDir === 'asc'" :size="14" />
                <ArrowDown v-else-if="sortKey === 'freshness'" :size="14" />
              </button>
            </th>
            <th class="cell-actions">{{ t("processes.table.actions") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in rows"
            :key="rowKey(row)"
            class="data-row"
            :class="{ selected: isSelected(rowKey(row)), acting: actingKeys.includes(rowKey(row)) }"
            @click="openRow($event, row)"
          >
            <td class="cell-select">
              <label class="check-wrap">
                <input
                  class="row-check"
                  type="checkbox"
                  :checked="isSelected(rowKey(row))"
                  :aria-label="t('processes.selectRow', { name: row.name })"
                  @change="toggleRow(rowKey(row))"
                />
              </label>
            </td>
            <td>
              <div class="name-cell">
                <RouterLink class="name-link" :to="processHref(row)">{{ row.name }}</RouterLink>
                <div v-if="row.group || row.activeRevision !== row.latestRevision" class="name-meta">
                  <span v-if="row.group" class="group-chip">{{ row.group }}</span>
                  <span
                    v-if="row.activeRevision !== row.latestRevision"
                    class="restart-chip"
                    :title="t('processes.restartRequired')"
                  >
                    {{ t("processes.restartRequired") }}
                  </span>
                </div>
              </div>
            </td>
            <td class="cell-owner">
              <RouterLink
                v-if="row.ownerNodeId"
                :to="`/nodes/${encodeURIComponent(row.ownerNodeId)}`"
                :title="ownerDisplay(row.ownerHostname, row.ownerNodeId)"
              >
                {{ ownerDisplay(row.ownerHostname, row.ownerNodeId) }}
              </RouterLink>
              <span v-else :title="ownerDisplay(row.ownerHostname, row.ownerNodeId)">
                {{ ownerDisplay(row.ownerHostname, row.ownerNodeId) }}
              </span>
            </td>
            <td>
              <div class="status-stack">
                <span v-if="row.observed" class="state-pill" :class="toneForObserved(row.observed)">
                  {{ translateObservedState(row.observed) }}
                </span>
                <span v-else class="muted">—</span>
                <span
                  v-if="row.desired && row.desired !== row.observed"
                  class="desired-hint"
                >
                  {{ t("processes.desiredMismatch", { state: translateDesiredState(row.desired) }) }}
                </span>
              </div>
            </td>
            <td>
              <span v-if="row.health" class="state-pill" :class="toneForHealth(row.health)">
                {{ translateHealthState(row.health) }}
              </span>
              <span v-else class="muted">—</span>
            </td>
            <td>
              <span class="revision" :class="{ mismatch: row.activeRevision !== row.latestRevision }">
                {{ row.activeRevision }} / {{ row.latestRevision }}
              </span>
            </td>
            <td>
              <FreshnessBadge :status="row.freshness" />
            </td>
            <td class="cell-actions">
              <ProcessRowActions
                :row="row"
                :acting="acting"
                :can-start="canStart"
                :can-stop="canStop"
                :can-restart="canRestart"
                @action="requestAction($event, [row])"
              />
            </td>
          </tr>
          <tr v-if="!rows.length" class="empty-row">
            <td colspan="8" class="empty-cell">
              <div class="empty">
                <Layers :size="28" aria-hidden="true" />
                <p>{{ t("processes.noProcesses") }}</p>
                <p class="muted">{{ filtersActive ? t("processes.emptyHint") : t("processes.emptyNone") }}</p>
                <button v-if="filtersActive" type="button" class="btn" @click="clearFilters">
                  {{ t("processes.emptyClear") }}
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="!rows.length" class="mobile-empty">
        <div class="empty">
          <Layers :size="28" aria-hidden="true" />
          <p>{{ t("processes.noProcesses") }}</p>
          <p class="muted">{{ filtersActive ? t("processes.emptyHint") : t("processes.emptyNone") }}</p>
          <button v-if="filtersActive" type="button" class="btn" @click="clearFilters">
            {{ t("processes.emptyClear") }}
          </button>
        </div>
      </div>
    </div>

    <Transition name="bulk-bar">
      <div
        v-if="selectedRows.length && !confirmOpen"
        class="bulk-bar"
        role="region"
        :aria-label="t('processes.bulk.selected', { count: selectedRows.length })"
      >
        <div class="bulk-info">
          <span class="bulk-count">{{ t("processes.bulk.selected", { count: selectedRows.length }) }}</span>
          <button type="button" class="link-btn" :disabled="acting" @click="clearSelection">
            {{ t("processes.bulk.clear") }}
          </button>
        </div>
        <div class="bulk-actions">
          <button
            type="button"
            class="btn"
            :disabled="acting || !canStart"
            @click="requestAction('start', selectedRows)"
          >
            <LoaderCircle v-if="acting" class="spin" :size="16" aria-hidden="true" />
            {{ t("processes.bulk.start") }}
          </button>
          <button
            type="button"
            class="btn"
            :disabled="acting || !canStop"
            @click="requestAction('stop', selectedRows)"
          >
            {{ t("processes.bulk.stop") }}
          </button>
          <button
            type="button"
            class="btn"
            :disabled="acting || !canRestart"
            @click="requestAction('restart', selectedRows)"
          >
            {{ t("processes.bulk.restart") }}
          </button>
          <button
            type="button"
            class="btn btn-danger"
            :disabled="acting || !canStop"
            @click="requestAction('kill', selectedRows)"
          >
            {{ t("processes.bulk.forceStop") }}
          </button>
        </div>
      </div>
    </Transition>

    <ConfirmDialog
      :open="confirmOpen"
      :title="confirmTitle"
      :message="confirmMessage"
      :confirm-label="confirmLabel"
      :cancel-label="t('actions.cancel')"
      :pending="acting"
      @cancel="cancelPending"
      @confirm="confirmPending"
    />

    <Toast :show="showToast" :message="toastMessage" :type="toastType" @close="showToast = false" />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.page.has-bulk {
  padding-bottom: 5.5rem;
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

.subtitle,
.updated {
  margin: 0.3rem 0 0;
  color: var(--color-muted);
  font-size: 0.875rem;
  line-height: 1.5;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.summary {
  display: flex;
  flex-wrap: wrap;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  overflow: hidden;
}

.summary-item {
  display: flex;
  align-items: baseline;
  gap: 0.45rem;
  min-height: 44px;
  padding: 0.75rem 1rem;
  border: 0;
  border-right: 1px solid var(--color-border);
  background: transparent;
  color: inherit;
  cursor: pointer;
  text-align: left;
}

.summary-item:last-child {
  border-right: 0;
}

.summary-item:hover {
  background: color-mix(in srgb, var(--color-text) 4%, transparent);
}

.summary-item.active {
  background: color-mix(in srgb, var(--color-accent) 8%, transparent);
}

.summary-value {
  font-size: 1.2rem;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.summary-label {
  color: var(--color-muted);
  font-size: 0.8rem;
}

.summary-value.ok {
  color: var(--color-live-fg);
}

.summary-value.warn,
.summary-item.alert .summary-value.warn {
  color: var(--color-stale-fg);
}

.summary-value.danger {
  color: var(--color-danger);
}

.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 0.75rem;
}

.search {
  flex: 1 1 16rem;
  min-width: 12rem;
}

.search-wrap {
  position: relative;
  display: block;
}

.search-icon {
  position: absolute;
  top: 50%;
  left: 0.75rem;
  color: var(--color-muted);
  transform: translateY(-50%);
  pointer-events: none;
}

.search-input {
  padding-left: 2.25rem;
}

.field {
  display: flex;
  flex-direction: column;
  min-width: 11rem;
}

.field-label {
  margin-bottom: 0.35rem;
  color: var(--color-muted);
  font-size: 0.75rem;
  font-weight: 600;
}

.clear-btn {
  min-height: 44px;
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

.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
}

.table-card {
  overflow-x: auto;
}

.table-card:hover {
  box-shadow: var(--shadow-sm);
}

.process-table {
  min-width: 62rem;
}

.process-table th,
.process-table td {
  vertical-align: middle;
}

.process-table thead th {
  position: sticky;
  top: 0;
  z-index: 3;
  background: var(--color-card);
  box-shadow: inset 0 -1px 0 var(--color-border);
}

.process-table th:nth-child(n + 3),
.process-table td:nth-child(n + 3) {
  width: 1%;
  white-space: nowrap;
}

.process-table tbody tr.data-row {
  cursor: pointer;
}

.process-table tbody tr.selected {
  background: color-mix(in srgb, var(--color-accent) 7%, transparent);
}

.process-table tbody tr.acting {
  opacity: 0.72;
}

.cell-select {
  width: 3rem;
  min-width: 3rem;
  text-align: center;
  position: sticky;
  left: 0;
  z-index: 1;
  background: var(--color-card);
}

.process-table thead th.cell-select,
.process-table thead th.cell-actions {
  z-index: 4;
}

.cell-actions {
  width: 11.5rem;
  min-width: 11.5rem;
  text-align: center;
  position: sticky;
  right: 0;
  z-index: 1;
  background: var(--color-card);
  box-shadow: -8px 0 12px -12px rgba(0, 0, 0, 0.18);
}

.process-table tbody tr:hover .cell-select,
.process-table tbody tr:hover .cell-actions,
.process-table tbody tr.selected .cell-select,
.process-table tbody tr.selected .cell-actions {
  background: inherit;
}

.check-wrap {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 0.4rem;
  min-width: 44px;
  min-height: 44px;
  cursor: pointer;
}

.row-check {
  width: 1rem;
  height: 1rem;
  margin: 0;
  accent-color: var(--color-accent);
  cursor: pointer;
}

.name-cell {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  min-width: 7rem;
}

.name-link {
  font-weight: 600;
}

.name-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 0.35rem;
}

.cell-owner {
  max-width: 13rem;
}

.cell-owner a,
.cell-owner span {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.group-chip,
.restart-chip,
.state-pill {
  display: inline-flex;
  align-items: center;
  border-radius: 999px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
  line-height: 1.4;
}

.group-chip {
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
  color: var(--color-muted);
}

.restart-chip,
.state-pill.warn,
.revision.mismatch {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}

.state-pill.ok {
  background: var(--color-live);
  color: var(--color-live-fg);
}

.state-pill.neutral {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}

.state-pill.danger {
  background: color-mix(in srgb, var(--color-danger) 14%, white);
  color: var(--color-danger);
}

.status-stack {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0.2rem;
}

.desired-hint {
  color: var(--color-muted);
  font-size: 0.72rem;
  font-weight: 600;
}

.revision {
  font-variant-numeric: tabular-nums;
}

.revision.mismatch {
  border-radius: 999px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
}

.sort-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  min-height: 32px;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  letter-spacing: inherit;
  text-transform: inherit;
  cursor: pointer;
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

.bulk-bar {
  position: sticky;
  bottom: 1rem;
  z-index: 30;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem 1rem;
  padding: 0.75rem 1rem;
  border: 1px solid color-mix(in srgb, var(--color-text) 14%, var(--color-border));
  border-radius: 12px;
  background: var(--color-card);
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.12);
}

.bulk-info,
.bulk-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem 0.75rem;
}

.bulk-count {
  font-size: 0.9rem;
  font-weight: 650;
}

.link-btn {
  border: 0;
  background: transparent;
  color: var(--color-accent);
  font-size: 0.875rem;
  font-weight: 550;
  cursor: pointer;
  min-height: 44px;
}

.link-btn:hover:not(:disabled) {
  text-decoration: underline;
}

.link-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
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

.bulk-bar-enter-active,
.bulk-bar-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}

.bulk-bar-enter-from,
.bulk-bar-leave-to {
  opacity: 0;
  transform: translateY(0.5rem);
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

.mobile-toolbar,
.mobile-list,
.mobile-empty {
  display: none;
}

@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
  }

  .header-actions {
    justify-content: space-between;
  }

  .summary-item {
    flex: 1 1 40%;
    max-width: 50%;
    border-right: 0;
    border-bottom: 1px solid var(--color-border);
  }

  .bulk-bar {
    bottom: 0.75rem;
  }

  .process-table {
    display: none;
  }

  .mobile-toolbar,
  .mobile-list {
    display: flex;
  }

  .mobile-toolbar {
    align-items: center;
    justify-content: space-between;
    padding: 0.25rem 0.75rem;
    border-bottom: 1px solid var(--color-border);
  }

  .mobile-list {
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .mobile-empty {
    display: block;
    padding: 2rem 1rem;
  }

  .proc-card {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 0.35rem 0.5rem;
    padding: 0.75rem 0.75rem 0.5rem;
    border-bottom: 1px solid var(--color-border);
    cursor: pointer;
  }

  .proc-card:last-child {
    border-bottom: 0;
  }

  .proc-card.selected {
    background: color-mix(in srgb, var(--color-accent) 7%, transparent);
  }

  .proc-card .check-wrap {
    grid-row: 1;
    align-self: start;
  }

  .proc-card-body {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .proc-card-head,
  .proc-card-meta,
  .proc-card-pills {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.35rem 0.5rem;
  }

  .proc-card-meta {
    color: var(--color-muted);
    font-size: 0.8rem;
  }

  .proc-card :deep(.row-actions) {
    grid-column: 1 / -1;
    justify-content: flex-end;
  }
}

@media (prefers-reduced-motion: reduce) {
  .summary-item,
  .skeleton-row,
  .bulk-bar-enter-active,
  .bulk-bar-leave-active,
  .spin {
    animation: none;
    transition: none;
  }
}
</style>
