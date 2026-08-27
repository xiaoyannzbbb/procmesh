<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Template enums, data-* hooks, and comparison literals are not visible copy. */
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { Crown, LoaderCircle, RefreshCw, Search, Server, TriangleAlert, X } from "lucide-vue-next";
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { STALE, UNKNOWN } from "../lib/freshness";
import { useNodeClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";
import { useProcessState } from "../lib/useProcessState";
import {
  mapNode,
  RAFT_LEADER,
  RAFT_NON_VOTER,
  RAFT_NOT_MEMBER,
  RAFT_VOTER,
  type NodeView,
  type RaftRole,
  type ResourceView,
} from "./clusterView";
import { formatRemoteError } from "./processView";

type StatusFilter = "all" | "alive" | "suspect" | "failed" | "stale";
type ResourceKey = "cpu" | "memory" | "disk";

const STATUS_FILTERS: StatusFilter[] = ["all", "alive", "suspect", "failed", "stale"];
const POLL_MS = 5000;
const MAX_PROCESS_CHIPS = 4;

const { t } = useI18n();
const { translateObservedState } = useProcessState();
const route = useRoute();
const router = useRouter();
const queryClient = useQueryClient();
const client = useNodeClient();

const nowMs = ref(Date.now());
const searchQuery = ref(queryString(route.query.q));
const statusFilter = ref<StatusFilter>(parseStatus(route.query.status));
const refreshing = ref(false);

const query = useQuery({
  queryKey: ["nodes"],
  queryFn: () => client.listNodes({}),
  refetchInterval: POLL_MS,
});

const allNodes = computed(() => {
  void nowMs.value;
  const list = query.data.value?.nodes ?? [];
  return list
    .map((node) => mapNode(node, nowMs.value))
    .sort((a, b) => {
      const left = a.hostname || a.nodeId;
      const right = b.hostname || b.nodeId;
      return left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" });
    });
});

const stats = computed(() => {
  const list = allNodes.value;
  return {
    total: list.length,
    alive: list.filter((node) => node.state.toUpperCase() === "ALIVE").length,
    suspect: list.filter((node) => node.state.toUpperCase() === "SUSPECT").length,
    failed: list.filter((node) => node.state.toUpperCase() === "FAILED").length,
    stale: list.filter((node) => node.freshness === STALE).length,
  };
});

const filtersActive = computed(
  () => Boolean(searchQuery.value.trim() || statusFilter.value !== "all"),
);

const nodes = computed(() => {
  const needle = searchQuery.value.trim().toLowerCase();
  return allNodes.value.filter((node) => {
    if (statusFilter.value === "alive" && node.state.toUpperCase() !== "ALIVE") {
      return false;
    }
    if (statusFilter.value === "suspect" && node.state.toUpperCase() !== "SUSPECT") {
      return false;
    }
    if (statusFilter.value === "failed" && node.state.toUpperCase() !== "FAILED") {
      return false;
    }
    if (statusFilter.value === "stale" && node.freshness !== STALE) {
      return false;
    }
    if (needle) {
      const haystack = [node.hostname, node.nodeId, node.agentVersion].join(" ").toLowerCase();
      if (!haystack.includes(needle)) {
        return false;
      }
    }
    return true;
  });
});

const loading = computed(() => query.isPending.value && !query.data.value);

const errorText = computed(() => {
  if (query.data.value) {
    return "";
  }
  const err = query.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

const staleBanner = computed(() => stats.value.stale > 0);

const subtitle = computed(() => {
  if (filtersActive.value) {
    return t("nodes.showing", { shown: nodes.value.length, total: stats.value.total });
  }
  return t("nodes.subtitle", { count: stats.value.total });
});

const lastUpdatedLabel = computed(() => {
  void nowMs.value;
  const stamp = query.dataUpdatedAt.value || 0;
  if (!stamp) {
    return t("nodes.updatedJustNow");
  }
  const seconds = Math.max(0, Math.floor((Date.now() - stamp) / 1000));
  if (seconds < 5) {
    return t("nodes.updatedJustNow");
  }
  if (seconds < 60) {
    return t("nodes.updatedSeconds", { count: seconds });
  }
  return t("nodes.updatedMinutes", { count: Math.floor(seconds / 60) });
});

watch([searchQuery, statusFilter], () => {
  const next: Record<string, string> = {};
  const search = searchQuery.value.trim();
  if (search) {
    next.q = search;
  }
  if (statusFilter.value !== "all") {
    next.status = statusFilter.value;
  }
  const same =
    queryString(route.query.q) === (next.q ?? "") &&
    queryString(route.query.status) === (next.status ?? "");
  if (!same) {
    void router.replace({ query: next });
  }
});

function queryString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function parseStatus(value: unknown): StatusFilter {
  return STATUS_FILTERS.includes(value as StatusFilter) ? (value as StatusFilter) : "all";
}

function setStatusFilter(next: StatusFilter): void {
  statusFilter.value = statusFilter.value === next ? "all" : next;
}

function clearFilters(): void {
  searchQuery.value = "";
  statusFilter.value = "all";
}

function nodeHref(node: NodeView): string {
  return `/nodes/${encodeURIComponent(node.nodeId)}`;
}

function openRow(event: MouseEvent, node: NodeView): void {
  const target = event.target as HTMLElement | null;
  if (target?.closest("a, button, input, label")) {
    return;
  }
  void router.push(nodeHref(node));
}

function raftRoleLabel(role: RaftRole): string {
  switch (role) {
    case RAFT_LEADER:
      return t("nodes.raftRole.leader");
    case RAFT_VOTER:
      return t("nodes.raftRole.voter");
    case RAFT_NON_VOTER:
      return t("nodes.raftRole.nonVoter");
    case RAFT_NOT_MEMBER:
      return t("nodes.raftRole.notMember");
    default:
      return t("nodes.raftRole.unknown");
  }
}

function raftRoleClass(role: RaftRole): string {
  return `raft-role-${role.toLowerCase().replace("_", "-")}`;
}

function stateLabel(state: string): string {
  switch (state.toUpperCase()) {
    case "ALIVE":
      return t("nodes.state.alive");
    case "SUSPECT":
      return t("nodes.state.suspect");
    case "FAILED":
      return t("nodes.state.failed");
    case "LEFT":
      return t("nodes.state.left");
    case "REMOVED":
      return t("nodes.state.removed");
    case "REVOKED":
      return t("nodes.state.revoked");
    default:
      return t("nodes.state.unknown");
  }
}

function stateTone(state: string): string {
  switch (state.toUpperCase()) {
    case "ALIVE":
      return "ok";
    case "SUSPECT":
      return "warn";
    case "FAILED":
      return "danger";
    default:
      return "neutral";
  }
}

function toneForObserved(state: string, freshness: string): string {
  if (freshness === STALE || freshness === UNKNOWN) {
    return "warn";
  }
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

function resourceTone(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return "unknown";
  }
  if (value >= 90) {
    return "danger";
  }
  if (value >= 85) {
    return "warn";
  }
  return "neutral";
}

function resourceValue(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return t("nodes.resources.unknown");
  }
  return `${Math.round(value)}%`;
}

function resourceWidth(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return "0%";
  }
  return `${Math.min(100, Math.max(0, value))}%`;
}

function resourceItems(resources: ResourceView) {
  const items: { key: ResourceKey; label: string; value: string; width: string; tone: string }[] = [
    {
      key: "cpu",
      label: t("nodes.resources.cpu"),
      value: resourceValue(resources.cpuPercent),
      width: resourceWidth(resources.cpuPercent),
      tone: resourceTone(resources.cpuPercent),
    },
    {
      key: "memory",
      label: t("nodes.resources.memory"),
      value: resourceValue(resources.memoryPercent),
      width: resourceWidth(resources.memoryPercent),
      tone: resourceTone(resources.memoryPercent),
    },
    {
      key: "disk",
      label: t("nodes.resources.disk"),
      value: resourceValue(resources.diskPercent),
      width: resourceWidth(resources.diskPercent),
      tone: resourceTone(resources.diskPercent),
    },
  ];
  return items;
}

function visibleProcesses(node: NodeView) {
  return node.processes.slice(0, MAX_PROCESS_CHIPS);
}

function hiddenProcessCount(node: NodeView): number {
  return Math.max(0, node.processes.length - MAX_PROCESS_CHIPS);
}

function formatNodeAge(unixMs: number): string {
  void nowMs.value;
  if (unixMs <= 0) {
    return t("nodes.resources.unknown");
  }
  const seconds = Math.max(0, Math.floor((nowMs.value - unixMs) / 1000));
  if (seconds < 5) {
    return t("nodes.updatedJustNow");
  }
  if (seconds < 60) {
    return t("nodes.updatedSeconds", { count: seconds });
  }
  return t("nodes.updatedMinutes", { count: Math.floor(seconds / 60) });
}

function rowClass(node: NodeView): string {
  if (node.state.toUpperCase() === "FAILED") {
    return "row-failed";
  }
  if (node.freshness === STALE || node.state.toUpperCase() === "SUSPECT") {
    return "row-stale";
  }
  return "";
}

async function refresh(): Promise<void> {
  if (refreshing.value) {
    return;
  }
  refreshing.value = true;
  try {
    await queryClient.invalidateQueries({ queryKey: ["nodes"] });
  } finally {
    refreshing.value = false;
    nowMs.value = Date.now();
  }
}

let tick: ReturnType<typeof setInterval> | undefined;

onMounted(() => {
  tick = setInterval(() => {
    nowMs.value = Date.now();
  }, POLL_MS);
});

onUnmounted(() => {
  if (tick) {
    clearInterval(tick);
  }
});
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <div class="eyebrow">{{ t("nodes.eyebrow") }}</div>
        <h1>{{ t("nodes.title") }}</h1>
        <p class="subtitle">{{ subtitle }}</p>
      </div>
      <div class="header-actions">
        <span class="updated">{{ t("nodes.lastUpdated", { age: lastUpdatedLabel }) }}</span>
        <button type="button" class="btn" :disabled="refreshing || loading" @click="refresh">
          <LoaderCircle v-if="refreshing" class="spin" :size="16" aria-hidden="true" />
          <RefreshCw v-else :size="16" aria-hidden="true" />
          {{ t("nodes.refresh") }}
        </button>
      </div>
    </header>

    <div class="summary" role="toolbar" :aria-label="t('nodes.title')">
      <button
        type="button"
        class="summary-item"
        data-stat="total"
        :class="{ active: statusFilter === 'all' }"
        :aria-pressed="statusFilter === 'all'"
        @click="statusFilter = 'all'"
      >
        <span class="summary-value">{{ stats.total }}</span>
        <span class="summary-label">{{ t("nodes.stats.total") }}</span>
      </button>
      <button
        type="button"
        class="summary-item"
        data-stat="alive"
        :class="{ active: statusFilter === 'alive' }"
        :aria-pressed="statusFilter === 'alive'"
        @click="setStatusFilter('alive')"
      >
        <span class="summary-value" :class="{ ok: stats.alive > 0 }">{{ stats.alive }}</span>
        <span class="summary-label">{{ t("nodes.stats.alive") }}</span>
      </button>
      <button
        type="button"
        class="summary-item"
        data-stat="suspect"
        :class="{ active: statusFilter === 'suspect', alert: stats.suspect > 0 }"
        :aria-pressed="statusFilter === 'suspect'"
        @click="setStatusFilter('suspect')"
      >
        <span class="summary-value" :class="{ warn: stats.suspect > 0 }">{{ stats.suspect }}</span>
        <span class="summary-label">{{ t("nodes.stats.suspect") }}</span>
      </button>
      <button
        type="button"
        class="summary-item"
        data-stat="failed"
        :class="{ active: statusFilter === 'failed', alert: stats.failed > 0 }"
        :aria-pressed="statusFilter === 'failed'"
        @click="setStatusFilter('failed')"
      >
        <span class="summary-value" :class="{ danger: stats.failed > 0 }">{{ stats.failed }}</span>
        <span class="summary-label">{{ t("nodes.stats.failed") }}</span>
      </button>
      <button
        type="button"
        class="summary-item"
        data-stat="stale"
        :class="{ active: statusFilter === 'stale', alert: stats.stale > 0 }"
        :aria-pressed="statusFilter === 'stale'"
        @click="setStatusFilter('stale')"
      >
        <span class="summary-value" :class="{ warn: stats.stale > 0 }">{{ stats.stale }}</span>
        <span class="summary-label">{{ t("nodes.stats.stale") }}</span>
      </button>
    </div>

    <div v-if="staleBanner" class="banner" role="status">
      <TriangleAlert :size="16" aria-hidden="true" />
      <span>{{ t("nodes.staleBanner") }}</span>
    </div>

    <div class="toolbar">
      <label class="search">
        <span class="field-label">{{ t("nodes.search") }}</span>
        <span class="search-wrap">
          <Search class="search-icon" :size="16" aria-hidden="true" />
          <input
            v-model="searchQuery"
            class="input search-input"
            name="search"
            type="search"
            :placeholder="t('nodes.searchPlaceholder')"
            autocomplete="off"
          />
        </span>
      </label>
      <button v-if="filtersActive" type="button" class="btn clear-btn" @click="clearFilters">
        <X :size="16" aria-hidden="true" />
        {{ t("nodes.clearFilters") }}
      </button>
    </div>

    <p v-if="errorText && !loading" class="error" role="alert">{{ errorText }}</p>
    <div v-else-if="loading" class="card table-card" aria-busy="true" :aria-label="t('nodes.loading')">
      <div class="skeleton-table">
        <div v-for="n in 5" :key="n" class="skeleton-row" />
      </div>
    </div>
    <div v-else class="card table-card">
      <table class="table nodes-table">
        <thead>
          <tr>
            <th>{{ t("nodes.table.hostname") }}</th>
            <th>{{ t("nodes.table.state") }}</th>
            <th>{{ t("nodes.table.raftRole") }}</th>
            <th>{{ t("nodes.table.version") }}</th>
            <th>{{ t("nodes.table.resources") }}</th>
            <th>{{ t("nodes.table.processes") }}</th>
            <th>{{ t("nodes.table.freshness") }}</th>
            <th>{{ t("nodes.table.updated") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="node in nodes"
            :key="node.nodeId"
            class="data-row"
            :class="rowClass(node)"
            @click="openRow($event, node)"
          >
            <td>
              <div class="node-identity">
                <div class="name-row">
                  <Crown
                    v-if="node.raftRole === RAFT_LEADER"
                    class="leader-icon"
                    :size="14"
                    aria-hidden="true"
                  />
                  <RouterLink class="name-link" :to="nodeHref(node)">
                    {{ node.hostname || node.nodeId }}
                  </RouterLink>
                </div>
                <div v-if="node.hostname && node.hostname !== node.nodeId" class="mono muted node-id">
                  {{ node.nodeId }}
                </div>
              </div>
            </td>
            <td>
              <span
                class="state-pill"
                :class="stateTone(node.state)"
                :data-state="node.state"
                :aria-label="t('nodes.state.badgeLabel', { state: stateLabel(node.state) })"
              >
                {{ stateLabel(node.state) }}
              </span>
            </td>
            <td class="raft-role-cell">
              <div class="raft-role-content">
                <span
                  :class="['raft-role-badge', raftRoleClass(node.raftRole)]"
                  :aria-label="t('nodes.raftRole.badgeLabel', { role: raftRoleLabel(node.raftRole) })"
                >
                  {{ raftRoleLabel(node.raftRole) }}
                </span>
                <FreshnessBadge v-if="node.raftRoleFreshness === STALE" :status="node.raftRoleFreshness" />
              </div>
            </td>
            <td class="cell-version">{{ node.agentVersion || "—" }}</td>
            <td>
              <div class="resource-stack">
                <div
                  v-for="item in resourceItems(node.resources)"
                  :key="item.key"
                  class="resource-meter"
                  :class="item.tone"
                  :data-resource="item.key"
                  :aria-label="t('nodes.resources.meterLabel', { name: item.label, value: item.value })"
                >
                  <span class="resource-name">{{ item.label }}</span>
                  <span class="resource-track" aria-hidden="true">
                    <span class="resource-fill" :style="{ width: item.width }" />
                  </span>
                  <span class="resource-value">{{ item.value }}</span>
                </div>
                <span v-if="node.resources.historyWritesPaused" class="pause-chip">
                  {{ t("nodes.diskPaused") }}
                </span>
              </div>
            </td>
            <td>
              <div v-if="node.processes.length" class="proc-wrap">
                <span
                  v-for="proc in visibleProcesses(node)"
                  :key="proc.processId || proc.name"
                  class="proc-chip"
                  :class="toneForObserved(proc.observed, proc.freshness)"
                >
                  <span>{{ proc.name }}</span>
                  <span class="proc-obs">{{ translateObservedState(proc.observed) }}</span>
                  <FreshnessBadge v-if="proc.freshness === STALE" :status="proc.freshness" />
                </span>
                <span v-if="hiddenProcessCount(node)" class="muted more">
                  {{ t("nodes.processesMore", { count: hiddenProcessCount(node) }) }}
                </span>
              </div>
              <span v-else class="muted">—</span>
            </td>
            <td>
              <FreshnessBadge :status="node.freshness" />
            </td>
            <td class="cell-updated">{{ formatNodeAge(node.lastUpdatedUnixMs) }}</td>
          </tr>
          <tr v-if="!nodes.length" class="empty-row">
            <td colspan="8" class="empty-cell">
              <div class="empty">
                <Server :size="28" aria-hidden="true" />
                <p>{{ t("nodes.noNodes") }}</p>
                <p class="muted">{{ filtersActive ? t("nodes.emptyHint") : t("nodes.emptyNone") }}</p>
                <button v-if="filtersActive" type="button" class="btn" @click="clearFilters">
                  {{ t("nodes.emptyClear") }}
                </button>
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
          :class="rowClass(node)"
          @click="openRow($event, node)"
        >
          <div class="node-card-head">
            <div class="node-identity">
              <div class="name-row">
                <Crown
                  v-if="node.raftRole === RAFT_LEADER"
                  class="leader-icon"
                  :size="14"
                  aria-hidden="true"
                />
                <RouterLink class="name-link" :to="nodeHref(node)">
                  {{ node.hostname || node.nodeId }}
                </RouterLink>
              </div>
              <div v-if="node.hostname && node.hostname !== node.nodeId" class="mono muted node-id">
                {{ node.nodeId }}
              </div>
            </div>
            <span
              class="state-pill"
              :class="stateTone(node.state)"
              :data-state="node.state"
              :aria-label="t('nodes.state.badgeLabel', { state: stateLabel(node.state) })"
            >
              {{ stateLabel(node.state) }}
            </span>
          </div>
          <div class="node-card-pills">
            <span
              :class="['raft-role-badge', raftRoleClass(node.raftRole)]"
              :aria-label="t('nodes.raftRole.badgeLabel', { role: raftRoleLabel(node.raftRole) })"
            >
              {{ raftRoleLabel(node.raftRole) }}
            </span>
            <FreshnessBadge :status="node.freshness" />
            <span class="muted">{{ formatNodeAge(node.lastUpdatedUnixMs) }}</span>
          </div>
          <div class="resource-stack">
            <div
              v-for="item in resourceItems(node.resources)"
              :key="item.key"
              class="resource-meter"
              :class="item.tone"
              :data-resource="item.key"
              :aria-label="t('nodes.resources.meterLabel', { name: item.label, value: item.value })"
            >
              <span class="resource-name">{{ item.label }}</span>
              <span class="resource-track" aria-hidden="true">
                <span class="resource-fill" :style="{ width: item.width }" />
              </span>
              <span class="resource-value">{{ item.value }}</span>
            </div>
          </div>
          <div class="node-card-meta">
            <span>{{ node.agentVersion || "—" }}</span>
            <span>{{ t("nodes.processCount", { count: node.processCount }) }}</span>
          </div>
        </li>
      </ul>
      <div v-if="!nodes.length" class="mobile-empty">
        <div class="empty">
          <Server :size="28" aria-hidden="true" />
          <p>{{ t("nodes.noNodes") }}</p>
          <p class="muted">{{ filtersActive ? t("nodes.emptyHint") : t("nodes.emptyNone") }}</p>
          <button v-if="filtersActive" type="button" class="btn" @click="clearFilters">
            {{ t("nodes.emptyClear") }}
          </button>
        </div>
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
  flex: 1 1 0;
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

.banner {
  display: flex;
  align-items: flex-start;
  gap: 0.5rem;
  padding: 0.75rem 1rem;
  border: 1px solid var(--color-stale);
  border-radius: 8px;
  background: var(--color-stale);
  color: var(--color-stale-fg);
  font-size: 0.875rem;
  line-height: 1.5;
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

.field-label {
  display: block;
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
  padding: 0;
  overflow-x: auto;
}

.table-card:hover {
  box-shadow: var(--shadow-sm);
}

.nodes-table {
  width: 100%;
  min-width: 56rem;
}

.nodes-table th,
.nodes-table td {
  padding: 0.65rem 0.7rem;
  vertical-align: middle;
}

.nodes-table th:last-child,
.nodes-table td:last-child {
  padding-right: 1rem;
}

.nodes-table thead th {
  position: sticky;
  top: 0;
  z-index: 3;
  background: var(--color-card);
  box-shadow: inset 0 -1px 0 var(--color-border);
}

.nodes-table tbody tr.data-row {
  cursor: pointer;
}

.nodes-table tbody tr.row-stale {
  background: color-mix(in srgb, var(--color-stale) 35%, transparent);
}

.nodes-table tbody tr.row-failed {
  background: color-mix(in srgb, var(--color-danger) 6%, transparent);
}

.node-identity {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
  min-width: 0;
}

.name-row {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  min-width: 0;
}

.leader-icon {
  flex: 0 0 auto;
  color: var(--color-accent);
}

.name-link {
  font-weight: 600;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}

.node-id {
  overflow-wrap: anywhere;
  font-size: 0.75rem;
}

.cell-version,
.cell-updated {
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.raft-role-cell {
  white-space: nowrap;
}

.raft-role-content {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  min-width: 7rem;
}

.raft-role-badge,
.state-pill,
.proc-chip,
.pause-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  border-radius: 999px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
  line-height: 1.4;
  white-space: nowrap;
}

.raft-role-badge {
  border-radius: 3px;
}

.raft-role-leader {
  background: var(--color-live);
  color: var(--color-live-fg);
}

.raft-role-voter {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}

.raft-role-non-voter {
  background: color-mix(in srgb, var(--color-accent) 12%, var(--color-card));
  color: var(--color-text);
}

.raft-role-not-member {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}

.raft-role-unknown {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}

.state-pill.ok,
.proc-chip.ok {
  background: var(--color-live);
  color: var(--color-live-fg);
}

.state-pill.warn,
.proc-chip.warn,
.pause-chip {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}

.state-pill.neutral,
.proc-chip.neutral {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}

.state-pill.danger,
.proc-chip.danger {
  background: color-mix(in srgb, var(--color-danger) 14%, white);
  color: var(--color-danger);
}

.resource-stack {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  min-width: 9.5rem;
}

.resource-meter {
  display: grid;
  grid-template-columns: 3.4rem minmax(3.5rem, 1fr) auto;
  align-items: center;
  gap: 0.4rem;
}

.resource-name {
  color: var(--color-muted);
  font-size: 0.7rem;
  font-weight: 650;
  letter-spacing: 0.02em;
  text-transform: uppercase;
}

.resource-track {
  display: block;
  height: 0.4rem;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-text) 8%, transparent);
}

.resource-fill {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--color-unknown-fg);
}

.resource-meter.warn .resource-fill {
  background: var(--color-stale-fg);
}

.resource-meter.danger .resource-fill {
  background: var(--color-danger);
}

.resource-meter.unknown .resource-fill {
  width: 0;
}

.resource-value {
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.proc-wrap {
  display: flex;
  flex-wrap: wrap;
  gap: 0.35rem;
  max-width: 14rem;
}

.proc-obs {
  color: inherit;
  opacity: 0.8;
  font-weight: 550;
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
  }

  .summary-item {
    flex: 1 1 30%;
    max-width: 33.34%;
    border-right: 0;
    border-bottom: 1px solid var(--color-border);
  }

  .nodes-table {
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
    cursor: pointer;
  }

  .node-card:last-child {
    border-bottom: 0;
  }

  .node-card.row-stale {
    background: color-mix(in srgb, var(--color-stale) 35%, transparent);
  }

  .node-card.row-failed {
    background: color-mix(in srgb, var(--color-danger) 6%, transparent);
  }

  .node-card-head,
  .node-card-pills,
  .node-card-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.35rem 0.5rem;
  }

  .node-card-head {
    justify-content: space-between;
  }

  .node-card-meta {
    color: var(--color-muted);
    font-size: 0.8rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .summary-item,
  .skeleton-row,
  .spin {
    animation: none;
    transition: none;
  }
}
</style>
