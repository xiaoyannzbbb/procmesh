<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Template enums, data-* hooks, and comparison literals are not visible copy. */
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { ArrowUpCircle, LoaderCircle, RefreshCw } from "lucide-vue-next";
import { computed, ref } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, type Freshness } from "../lib/freshness";
import { useUpdateClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";

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

const { t } = useI18n();
const queryClient = useQueryClient();
const client = useUpdateClient();
const refreshing = ref(false);

const CHECK_LATEST_STALE_MS = 15 * 60 * 1000;

const checkLatestQuery = useQuery({
  queryKey: ["updates", "checkLatest"],
  queryFn: () => client.checkLatest({ refresh: false }),
  staleTime: CHECK_LATEST_STALE_MS,
});

const query = useQuery({
  queryKey: ["updates", "nodeStatus"],
  queryFn: () => client.listNodeUpdateStatus({}),
});

const nodes = computed(() => query.data.value?.nodes ?? []);
const loading = computed(() => query.isPending.value && !query.data.value);

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
    return "ok";
  }
  switch (skipReason) {
    case "FAILED":
      return "danger";
    case "BUSY":
    case "STALE":
    case "SUSPECT":
    case "CURRENT":
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
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <div class="eyebrow">{{ t("updates.eyebrow") }}</div>
        <h1>{{ t("updates.title") }}</h1>
        <p class="subtitle">{{ subtitle }}</p>
      </div>
      <div class="header-actions">
        <button
          type="button"
          class="btn"
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
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="node in nodes"
            :key="node.nodeId"
            class="data-row"
            :class="rowClass(node.freshness)"
            :data-node="node.nodeId"
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
          </tr>
          <tr v-if="!nodes.length" class="empty-row">
            <td colspan="5" class="empty-cell">
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
          :class="rowClass(node.freshness)"
          :data-node="node.nodeId"
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

.status-pill {
  display: inline-flex;
  align-items: center;
  min-height: 1.5rem;
  border-radius: 999px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
  line-height: 1.4;
}

.status-ok {
  background: var(--color-live);
  color: var(--color-live-fg);
}

.status-warn {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}

.status-danger {
  background: color-mix(in srgb, var(--color-danger) 12%, var(--color-card));
  color: var(--color-danger);
}

.status-neutral {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}

.updates-table tbody tr.row-stale,
.node-card.row-stale {
  background: color-mix(in srgb, var(--color-stale) 35%, transparent);
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
}

@media (prefers-reduced-motion: reduce) {
  .skeleton-row,
  .spin {
    animation: none;
    transition: none;
  }
}
</style>
