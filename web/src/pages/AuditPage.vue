<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Template enum values and accessibility attributes are not visible copy. */
import { useQuery } from "@tanstack/vue-query";
import {
  AlertCircle,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Clipboard,
  FilterX,
  LoaderCircle,
  RefreshCw,
  Search,
  ShieldCheck,
} from "lucide-vue-next";
import { computed, nextTick, ref, watch } from "vue";
import Drawer from "../components/Drawer.vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, type Freshness } from "../lib/freshness";
import { useAuditClient } from "../lib/rpc/audit";
import { useAudit } from "../lib/useAudit";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const PAGE_SIZE_OPTIONS = [25, 50, 100] as const;
const SUCCESS_RESULTS = new Set(["OK", "SUCCESS"]);

type AuditEntryInput = {
  event?: {
    auditId?: string;
    timestampUnixMs?: bigint | number;
    userId?: string;
    username?: string;
    sourceIp?: string;
    sourceAgent?: string;
    targetAgent?: string;
    resource?: string;
    action?: string;
    operationId?: string;
    result?: string;
    metadataJson?: string;
  };
  sourceNode?: string;
  freshness?: string;
  lastUpdatedUnixMs?: bigint | number;
};

type AuditRow = {
  key: string;
  auditId: string;
  timestampUnixMs: number;
  time: string;
  userId: string;
  user: string;
  sourceIp: string;
  sourceAgent: string;
  targetAgent: string;
  resource: string;
  action: string;
  actionCode: string;
  operationId: string;
  result: string;
  resultCode: string;
  metadata: Record<string, unknown>;
  metadataText: string;
  sourceNode: string;
  freshness: Freshness;
  lastUpdatedUnixMs: number;
  unavailable: boolean;
};

const { t, currentLanguage } = useI18n();
const { formatAuditAction, formatAuditResult } = useAudit();
const client = useAuditClient();

const resourceDraft = ref("");
const appliedResource = ref("");
const targetNode = ref("");
const searchQuery = ref("");
const actionFilter = ref("ALL");
const resultFilter = ref("ALL");
const currentPage = ref(1);
const pageSize = ref<number>(PAGE_SIZE_OPTIONS[0]);
const knownNodes = ref<string[]>([]);
const detailRow = ref<AuditRow | null>(null);
const copyState = ref<"idle" | "copied" | "failed">("idle");
const workspaceRef = ref<HTMLElement | null>(null);
const pendingPageFocus = ref(false);

const query = useQuery({
  queryKey: computed(() => ["audit", appliedResource.value, targetNode.value, currentPage.value, pageSize.value]),
  queryFn: () =>
    client.listAudit({
      resource: appliedResource.value,
      limit: pageSize.value,
      targetNode: targetNode.value,
      page: currentPage.value,
    }),
});

const rawRows = computed(() => (query.data.value?.entries ?? []).map(mapEntry));
const hasServerPagination = computed(() => Boolean(query.data.value?.pageSize));
const serverTotal = computed(() => hasServerPagination.value ? toNumber(query.data.value?.total) : rawRows.value.length);
const responsePageSize = computed(() => query.data.value?.pageSize || pageSize.value);

watch(
  rawRows,
  (rows) => {
    const nodes = new Set(knownNodes.value);
    for (const row of rows) {
      if (row.sourceNode && row.sourceNode !== "—") {
        nodes.add(row.sourceNode);
      }
    }
    knownNodes.value = Array.from(nodes).sort((a, b) => a.localeCompare(b));
  },
  { immediate: true },
);

const actionOptions = computed(() => {
  const options = new Set(rawRows.value.map((row) => row.actionCode).filter(Boolean));
  if (actionFilter.value !== "ALL") {
    options.add(actionFilter.value);
  }
  return Array.from(options).sort();
});
const resultOptions = computed(() => {
  const options = new Set(rawRows.value.map((row) => row.resultCode).filter(Boolean));
  if (resultFilter.value !== "ALL") {
    options.add(resultFilter.value);
  }
  return Array.from(options).sort();
});

const entries = computed(() => {
  const needle = searchQuery.value.trim().toLocaleLowerCase();
  return rawRows.value.filter((row) => {
    if (actionFilter.value !== "ALL" && row.actionCode !== actionFilter.value) {
      return false;
    }
    if (resultFilter.value !== "ALL" && row.resultCode !== resultFilter.value) {
      return false;
    }
    if (!needle) {
      return true;
    }
    return [
      row.user,
      row.userId,
      row.action,
      row.actionCode,
      row.resource,
      row.sourceNode,
      row.sourceAgent,
      row.targetAgent,
      row.operationId,
      row.auditId,
    ].some((value) => value.toLocaleLowerCase().includes(needle));
  });
});

const pageCount = computed(() => Math.max(1, Math.ceil(serverTotal.value / responsePageSize.value)));
const hasMore = computed(() => hasServerPagination.value && Boolean(query.data.value?.hasMore));
const pageStart = computed(() => rawRows.value.length ? (currentPage.value - 1) * responsePageSize.value + 1 : 0);
const pageEnd = computed(() => rawRows.value.length ? pageStart.value + rawRows.value.length - 1 : 0);
const visiblePages = computed<Array<number | string>>(() => {
  const count = pageCount.value;
  const current = currentPage.value;
  if (count <= 7) {
    return Array.from({ length: count }, (_, index) => index + 1);
  }
  if (current <= 4) {
    return [1, 2, 3, 4, 5, "end-gap", count];
  }
  if (current >= count - 3) {
    return [1, "start-gap", count - 4, count - 3, count - 2, count - 1, count];
  }
  return [1, "start-gap", current - 1, current, current + 1, "end-gap", count];
});

watch(
  [appliedResource, targetNode, pageSize],
  () => {
    currentPage.value = 1;
  },
);

watch(
  [pageCount, hasServerPagination, () => query.isFetching.value],
  ([count, paginated, fetching]) => {
    if (paginated && !fetching && currentPage.value > count) {
      currentPage.value = count;
    }
  },
);

watch(
  () => query.dataUpdatedAt.value,
  async () => {
    const responsePage = query.data.value?.page || currentPage.value;
    if (responsePage !== currentPage.value || !pendingPageFocus.value) {
      return;
    }
    pendingPageFocus.value = false;
    await nextTick();
    const firstVisibleEntry = Array.from(
      workspaceRef.value?.querySelectorAll<HTMLElement>("[data-page-entry]") ?? [],
    ).find((element) => element.offsetParent !== null);
    firstVisibleEntry?.focus();
  },
);

const problemCount = computed(
  () => rawRows.value.filter((row) => !row.unavailable && row.resultCode && !SUCCESS_RESULTS.has(row.resultCode)).length,
);
const dataIssueCount = computed(
  () => rawRows.value.filter((row) => row.unavailable || row.freshness !== LIVE).length,
);
const actorCount = computed(
  () => new Set(rawRows.value.filter((row) => !row.unavailable && row.user !== "—").map((row) => row.user)).size,
);
const sourceCount = computed(
  () => new Set(rawRows.value.map((row) => row.sourceNode).filter((node) => node !== "—")).size,
);
const hasIncompleteData = computed(() => dataIssueCount.value > 0);
const isFetching = computed(() => query.isFetching.value);
const isInitialLoading = computed(() => query.isPending.value && !query.data.value);
const hasQueryData = computed(() => Boolean(query.data.value));
const isFiltered = computed(
  () => Boolean(appliedResource.value || targetNode.value || searchQuery.value.trim() || actionFilter.value !== "ALL" || resultFilter.value !== "ALL"),
);
const errorText = computed(() => (query.error.value ? formatRemoteError(query.error.value) : ""));
const lastRefresh = computed(() => formatMs(query.dataUpdatedAt.value));

function applyResource(): void {
  const next = resourceDraft.value.trim();
  if (next === appliedResource.value) {
    void query.refetch();
    return;
  }
  appliedResource.value = next;
}

function resetFilters(): void {
  resourceDraft.value = "";
  appliedResource.value = "";
  targetNode.value = "";
  searchQuery.value = "";
  actionFilter.value = "ALL";
  resultFilter.value = "ALL";
}

function openDetail(row: AuditRow): void {
  copyState.value = "idle";
  detailRow.value = row;
}

function closeDetail(): void {
  detailRow.value = null;
  copyState.value = "idle";
}

async function refresh(): Promise<void> {
  await query.refetch();
}

function goToPage(page: number): void {
  const next = Math.min(Math.max(page, 1), pageCount.value);
  if (next === currentPage.value) {
    return;
  }
  pendingPageFocus.value = true;
  currentPage.value = next;
}

async function copyOperationId(): Promise<void> {
  const value = detailRow.value?.operationId;
  if (!value) {
    return;
  }
  try {
    await navigator.clipboard.writeText(value);
    copyState.value = "copied";
  } catch {
    copyState.value = "failed";
  }
}

function mapEntry(entry: AuditEntryInput, index: number): AuditRow {
  const ev = entry.event ?? {};
  const resultCode = (ev.result ?? "").toUpperCase();
  const actionCode = ev.action ?? "";
  const resource = ev.resource || "—";
  const metadata = parseMetadata(ev.metadataJson);
  const timestampUnixMs = toNumber(ev.timestampUnixMs);
  const sourceNode = entry.sourceNode || "—";
  const freshness = auditFreshness(entry.freshness, resultCode);
  return {
    key: ev.auditId || `${sourceNode}:${actionCode}:${timestampUnixMs}:${index}`,
    auditId: ev.auditId || "—",
    timestampUnixMs,
    time: formatMs(timestampUnixMs),
    userId: ev.userId || "—",
    user: ev.username || ev.userId || "—",
    sourceIp: ev.sourceIp || "—",
    sourceAgent: ev.sourceAgent || "—",
    targetAgent: ev.targetAgent || "—",
    resource,
    action: actionCode ? formatAuditAction(actionCode, { name: resource, ...metadata }) : "—",
    actionCode,
    operationId: ev.operationId || "—",
    result: resultCode ? formatAuditResult(resultCode) : "—",
    resultCode,
    metadata,
    metadataText: formatMetadata(ev.metadataJson, metadata),
    sourceNode,
    freshness,
    lastUpdatedUnixMs: toNumber(entry.lastUpdatedUnixMs),
    unavailable: actionCode === "unavailable" || resultCode === "UNAVAILABLE",
  };
}

function parseMetadata(raw: string | undefined): Record<string, unknown> {
  if (!raw?.trim()) {
    return {};
  }
  try {
    const value: unknown = JSON.parse(raw);
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : { value };
  } catch {
    return { raw };
  }
}

function formatMetadata(raw: string | undefined, metadata: Record<string, unknown>): string {
  if (!raw?.trim()) {
    return "{}";
  }
  if (Object.keys(metadata).length === 1 && metadata.raw === raw) {
    return raw;
  }
  return JSON.stringify(metadata, null, 2);
}

function auditFreshness(raw: string | undefined, result: string): Freshness {
  const value = raw === LIVE || raw === STALE || raw === UNKNOWN ? raw : UNKNOWN;
  return result === "UNAVAILABLE" && value === LIVE ? STALE : value;
}

function toNumber(value: bigint | number | undefined): number {
  const number = Number(value ?? 0);
  return Number.isFinite(number) ? number : 0;
}

function formatMs(value: bigint | number | undefined): string {
  const ms = toNumber(value);
  if (ms <= 0) {
    return "—";
  }
  return new Intl.DateTimeFormat(currentLanguage.value === "zh" ? "zh-CN" : "en", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(ms));
}

function shortId(value: string): string {
  if (!value || value === "—" || value.length <= 24) {
    return value || "—";
  }
  return `${value.slice(0, 11)}…${value.slice(-9)}`;
}

function resultTone(code: string): string {
  if (SUCCESS_RESULTS.has(code)) {
    return "success";
  }
  if (["PARTIAL", "TIMEOUT", "UNAVAILABLE"].includes(code)) {
    return "warning";
  }
  if (["FAILED", "ERROR", "DENIED"].includes(code)) {
    return "danger";
  }
  return "neutral";
}
</script>

<template>
  <div class="page audit-page">
    <header class="page-header">
      <div>
        <div class="eyebrow"><ShieldCheck :size="15" aria-hidden="true" /> {{ t("audit.eyebrow") }}</div>
        <h1>{{ t("audit.title") }}</h1>
        <p class="page-subtitle">{{ t("audit.notice") }}</p>
      </div>
      <button type="button" class="btn btn-primary" :disabled="isFetching" @click="refresh">
        <LoaderCircle v-if="isFetching" class="spin" :size="17" aria-hidden="true" />
        <RefreshCw v-else :size="17" aria-hidden="true" />
        {{ isFetching ? t("audit.refreshing") : t("audit.refresh") }}
      </button>
    </header>

    <section class="summary-strip" :aria-label="t('audit.summary.label')" aria-live="polite">
      <div class="summary-item">
        <span class="summary-value">{{ rawRows.length }}</span>
        <span class="summary-label">{{ t("audit.summary.loaded") }}</span>
      </div>
      <div class="summary-item" :class="{ 'summary-danger': problemCount > 0 }">
        <span class="summary-value">{{ problemCount }}</span>
        <span class="summary-label">{{ t("audit.summary.problems") }}</span>
      </div>
      <div class="summary-item">
        <span class="summary-value">{{ actorCount }}</span>
        <span class="summary-label">{{ t("audit.summary.actors") }}</span>
      </div>
      <div class="summary-item" :class="{ 'summary-warning': dataIssueCount > 0 }">
        <span class="summary-value">{{ dataIssueCount }}</span>
        <span class="summary-label">{{ t("audit.summary.dataIssues") }}</span>
      </div>
      <div class="summary-meta">
        <span>{{ t("audit.lastRefresh", { time: lastRefresh }) }}</span>
        <span>{{ t("audit.summary.sources", { count: sourceCount }) }}</span>
      </div>
    </section>

    <div v-if="hasIncompleteData" class="coverage-banner" role="status">
      <AlertCircle :size="19" aria-hidden="true" />
      <div>
        <strong>{{ t("audit.incompleteTitle") }}</strong>
        <span>{{ t("audit.incompleteMessage") }}</span>
      </div>
    </div>

    <section ref="workspaceRef" class="workspace-panel" aria-labelledby="audit-log-title">
      <div class="section-heading">
        <div>
          <h2 id="audit-log-title">{{ t("audit.logTitle") }}</h2>
          <p>{{ t("audit.logHint") }}</p>
        </div>
        <span class="result-count">{{ t("audit.resultCount", { start: pageStart, end: pageEnd, total: serverTotal, shown: entries.length }) }}</span>
      </div>

      <form class="filter-toolbar" :aria-label="t('audit.filters')" @submit.prevent="applyResource">
        <label class="filter-control resource-control">
          <span>{{ t("audit.resourceLabel") }}</span>
          <div class="input-with-action">
            <input v-model="resourceDraft" class="input" name="resource" type="search" :placeholder="t('audit.resourcePlaceholder')" />
            <button type="submit" class="apply-button">{{ t("audit.applyFilters") }}</button>
          </div>
        </label>
        <label class="filter-control search-control">
          <span>{{ t("audit.searchLabel") }}</span>
          <div class="input-with-icon">
            <Search :size="16" aria-hidden="true" />
            <input v-model="searchQuery" type="search" :placeholder="t('audit.searchPlaceholder')" />
          </div>
        </label>
        <label class="filter-control">
          <span>{{ t("audit.nodeLabel") }}</span>
          <select v-model="targetNode">
            <option value="">{{ t("audit.allNodes") }}</option>
            <option v-for="node in knownNodes" :key="node" :value="node">{{ shortId(node) }}</option>
          </select>
        </label>
        <label class="filter-control">
          <span>{{ t("audit.actionLabel") }}</span>
          <select v-model="actionFilter">
            <option value="ALL">{{ t("audit.allActions") }}</option>
            <option v-for="action in actionOptions" :key="action" :value="action">{{ action }}</option>
          </select>
        </label>
        <label class="filter-control">
          <span>{{ t("audit.resultLabel") }}</span>
          <select v-model="resultFilter">
            <option value="ALL">{{ t("audit.allResults") }}</option>
            <option v-for="result in resultOptions" :key="result" :value="result">{{ formatAuditResult(result) }}</option>
          </select>
        </label>
        <button v-if="isFiltered" type="button" class="btn clear-filters" @click="resetFilters">
          <FilterX :size="16" aria-hidden="true" />
          {{ t("audit.clearFilters") }}
        </button>
      </form>

      <div v-if="isInitialLoading" class="loading-state" role="status">
        <LoaderCircle class="spin" :size="20" aria-hidden="true" />
        <span>{{ t("audit.loading") }}</span>
      </div>
      <div v-else-if="errorText && !hasQueryData" class="error-state" role="alert">
        <AlertCircle :size="21" aria-hidden="true" />
        <div>
          <strong>{{ t("audit.loadFailed") }}</strong>
          <p>{{ errorText }}</p>
          <button type="button" class="btn" @click="refresh">{{ t("audit.retry") }}</button>
        </div>
      </div>

      <template v-else>
        <div class="table-scroll desktop-audit-table">
          <table class="table audit-table">
            <caption class="sr-only">{{ t("audit.tableCaption") }}</caption>
            <thead>
              <tr>
                <th>{{ t("audit.table.time") }}</th>
                <th>{{ t("audit.table.user") }}</th>
                <th>{{ t("audit.table.action") }}</th>
                <th>{{ t("audit.table.resource") }} / {{ t("audit.table.sourceNode") }}</th>
                <th>{{ t("audit.table.result") }}</th>
                <th>{{ t("audit.table.freshness") }}</th>
                <th><span class="sr-only">{{ t("audit.table.details") }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in entries" :key="row.key" :class="{ 'row-data-issue': row.unavailable || row.freshness !== LIVE }">
                <td class="time-cell">{{ row.time }}</td>
                <td><div class="primary-secondary"><strong>{{ row.user }}</strong><span class="mono" :title="row.userId">{{ shortId(row.userId) }}</span></div></td>
                <td class="action-cell">
                  <button type="button" class="action-button" data-action="view-audit" data-page-entry @click="openDetail(row)">
                    <strong>{{ row.action }}</strong><span class="mono" :title="row.actionCode">{{ row.actionCode }}</span>
                  </button>
                </td>
                <td><div class="primary-secondary"><strong class="resource-value" :title="row.resource">{{ row.resource }}</strong><span class="mono" :title="row.sourceNode">{{ shortId(row.sourceNode) }}</span></div></td>
                <td><span class="result-badge" :class="`result-${resultTone(row.resultCode)}`"><span class="status-dot" aria-hidden="true" />{{ row.result }}</span></td>
                <td><FreshnessBadge :status="row.freshness" /></td>
                <td><button type="button" class="view-button" :aria-label="`${t('audit.viewDetails')}: ${row.action}`" @click="openDetail(row)">{{ t("audit.viewDetails") }} <ChevronRight :size="16" aria-hidden="true" /></button></td>
              </tr>
              <tr v-if="!entries.length">
                <td colspan="7" class="empty-cell"><div class="empty-state"><CheckCircle2 :size="28" aria-hidden="true" /><strong>{{ isFiltered ? t("audit.noMatches") : t("audit.noEntries") }}</strong><span>{{ isFiltered ? t("audit.noMatchesHint") : t("audit.noEntriesHint") }}</span><button v-if="isFiltered" type="button" class="btn" @click="resetFilters">{{ t("audit.clearFilters") }}</button></div></td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="mobile-audit-list">
          <button v-for="row in entries" :key="row.key" type="button" class="mobile-audit-item" data-page-entry @click="openDetail(row)">
            <span class="mobile-topline"><strong>{{ row.action }}</strong><span class="result-badge" :class="`result-${resultTone(row.resultCode)}`"><span class="status-dot" aria-hidden="true" />{{ row.result }}</span></span>
            <span class="resource-value">{{ row.resource }}</span>
            <span class="mobile-meta"><span>{{ row.user }}</span><span>{{ shortId(row.sourceNode) }}</span><span>{{ row.time }}</span></span>
            <span class="mobile-bottomline"><span class="mono">{{ row.actionCode }}</span><FreshnessBadge :status="row.freshness" /></span>
          </button>
          <div v-if="!entries.length" class="empty-state mobile-empty-state"><CheckCircle2 :size="28" aria-hidden="true" /><strong>{{ isFiltered ? t("audit.noMatches") : t("audit.noEntries") }}</strong><span>{{ isFiltered ? t("audit.noMatchesHint") : t("audit.noEntriesHint") }}</span><button v-if="isFiltered" type="button" class="btn" @click="resetFilters">{{ t("audit.clearFilters") }}</button></div>
        </div>

        <nav v-if="serverTotal || rawRows.length" class="pagination" :aria-label="t('audit.pagination.label')">
          <label class="page-size-control">
            <span>{{ t("audit.pagination.rowsPerPage") }}</span>
            <select v-model.number="pageSize">
              <option v-for="size in PAGE_SIZE_OPTIONS" :key="size" :value="size">{{ size }}</option>
            </select>
          </label>
          <div class="pagination-controls">
            <button
              type="button"
              class="pagination-arrow"
              :disabled="currentPage === 1"
              :aria-label="t('audit.pagination.previous')"
              :title="t('audit.pagination.previous')"
              @click="goToPage(currentPage - 1)"
            >
              <ChevronLeft :size="18" aria-hidden="true" />
            </button>
            <div class="page-numbers" :aria-label="t('audit.pagination.pages')">
              <template v-for="item in visiblePages" :key="item">
                <button
                  v-if="typeof item === 'number'"
                  type="button"
                  class="page-number"
                  :class="{ active: item === currentPage }"
                  :aria-current="item === currentPage ? 'page' : undefined"
                  :aria-label="t('audit.pagination.goToPage', { page: item })"
                  @click="goToPage(item)"
                >{{ item }}</button>
                <span v-else class="page-gap" aria-hidden="true">…</span>
              </template>
            </div>
            <span class="mobile-page-status">{{ t("audit.pagination.status", { page: currentPage, total: pageCount }) }}</span>
            <button
              type="button"
              class="pagination-arrow"
              :disabled="!hasMore"
              :aria-label="t('audit.pagination.next')"
              :title="t('audit.pagination.next')"
              @click="goToPage(currentPage + 1)"
            >
              <ChevronRight :size="18" aria-hidden="true" />
            </button>
          </div>
        </nav>
      </template>
    </section>

    <Drawer :open="Boolean(detailRow)" :title="t('audit.detailsTitle')" :close-label="t('actions.close')" size="wide" @close="closeDetail">
      <div v-if="detailRow" class="detail-content">
        <div class="detail-lead">
          <div><span class="detail-label">{{ t("audit.detail.action") }}</span><h3>{{ detailRow.action }}</h3><code>{{ detailRow.actionCode }}</code></div>
          <div class="detail-statuses"><span class="result-badge" :class="`result-${resultTone(detailRow.resultCode)}`"><span class="status-dot" aria-hidden="true" />{{ detailRow.result }}</span><FreshnessBadge :status="detailRow.freshness" /></div>
        </div>
        <dl class="evidence-grid">
          <div><dt>{{ t("audit.detail.timestamp") }}</dt><dd>{{ detailRow.time }}</dd></div>
          <div><dt>{{ t("audit.detail.actor") }}</dt><dd>{{ detailRow.user }} <code>{{ detailRow.userId }}</code></dd></div>
          <div><dt>{{ t("audit.detail.resource") }}</dt><dd><code>{{ detailRow.resource }}</code></dd></div>
          <div><dt>{{ t("audit.detail.sourceNode") }}</dt><dd><code>{{ detailRow.sourceNode }}</code></dd></div>
          <div><dt>{{ t("audit.detail.sourceAgent") }}</dt><dd><code>{{ detailRow.sourceAgent }}</code></dd></div>
          <div><dt>{{ t("audit.detail.targetAgent") }}</dt><dd><code>{{ detailRow.targetAgent }}</code></dd></div>
          <div><dt>{{ t("audit.detail.sourceIp") }}</dt><dd><code>{{ detailRow.sourceIp }}</code></dd></div>
          <div><dt>{{ t("audit.detail.auditId") }}</dt><dd><code>{{ detailRow.auditId }}</code></dd></div>
        </dl>
        <section class="operation-section">
          <div><span class="detail-label">{{ t("audit.detail.operationId") }}</span><code>{{ detailRow.operationId }}</code></div>
          <button v-if="detailRow.operationId !== '—'" type="button" class="btn" @click="copyOperationId"><Clipboard :size="16" aria-hidden="true" />{{ copyState === "copied" ? t("audit.copied") : copyState === "failed" ? t("audit.copyFailed") : t("audit.copyOperationId") }}</button>
        </section>
        <section class="metadata-section"><h3>{{ t("audit.detail.metadata") }}</h3><pre><code>{{ detailRow.metadataText }}</code></pre></section>
      </div>
    </Drawer>
  </div>
</template>

<style scoped>
.audit-page { display: flex; flex-direction: column; gap: 1rem; }
.page-header, .section-heading, .summary-strip, .summary-meta, .coverage-banner, .detail-lead, .detail-statuses, .operation-section { display: flex; align-items: center; }
.page-header, .section-heading, .detail-lead, .operation-section { justify-content: space-between; gap: 1rem; }
.eyebrow { display: inline-flex; align-items: center; gap: 0.35rem; margin-bottom: 0.35rem; color: var(--color-accent); font-size: 0.75rem; font-weight: 700; text-transform: uppercase; }
h1, h2, h3, p { margin-top: 0; }
h1 { margin-bottom: 0.35rem; font-size: 1.55rem; font-weight: 700; }
.page-subtitle, .section-heading p { margin-bottom: 0; color: var(--color-muted); font-size: 0.85rem; line-height: 1.5; }
.summary-strip { min-height: 4.5rem; overflow: hidden; border: 1px solid var(--color-border); border-radius: 8px; background: var(--color-card); box-shadow: var(--shadow-sm); }
.summary-item { display: flex; min-width: 7.5rem; align-self: stretch; flex-direction: column; justify-content: center; padding: 0.75rem 1rem; border-right: 1px solid var(--color-border); }
.summary-value { font-size: 1.2rem; font-weight: 700; font-variant-numeric: tabular-nums; }
.summary-label, .summary-meta, .result-count { color: var(--color-muted); font-size: 0.75rem; }
.summary-danger .summary-value { color: var(--color-danger); }
.summary-warning .summary-value { color: #92400e; }
.summary-meta { min-width: 0; flex: 1; justify-content: flex-end; gap: 1rem; padding: 0.75rem 1rem; text-align: right; }
.coverage-banner { gap: 0.75rem; padding: 0.8rem 1rem; border: 1px solid #f1cf70; border-radius: 7px; background: #fffbeb; color: #7c5208; }
.coverage-banner svg { flex: 0 0 auto; }
.coverage-banner div { display: flex; flex-direction: column; gap: 0.15rem; }
.coverage-banner strong { font-size: 0.85rem; }
.coverage-banner span { font-size: 0.8rem; }
.workspace-panel { min-width: 0; overflow: hidden; border: 1px solid var(--color-border); border-radius: 8px; background: var(--color-card); box-shadow: var(--shadow-sm); }
.section-heading { padding: 1.1rem 1.25rem; }
.section-heading h2 { margin-bottom: 0.25rem; font-size: 1rem; }
.result-count { flex: 0 0 auto; font-variant-numeric: tabular-nums; }
.filter-toolbar { display: grid; grid-template-columns: minmax(18rem, 1.5fr) minmax(15rem, 1.2fr) repeat(3, minmax(8.5rem, 0.7fr)) auto; align-items: end; gap: 0.65rem; padding: 1rem 1.25rem; border-top: 1px solid var(--color-border); border-bottom: 1px solid var(--color-border); background: color-mix(in srgb, var(--color-bg) 55%, var(--color-card)); }
.filter-control { display: flex; min-width: 0; flex-direction: column; gap: 0.3rem; color: var(--color-muted); font-size: 0.75rem; font-weight: 600; }
.filter-control select, .filter-control input { min-width: 0; min-height: 2.75rem; border: 1px solid var(--color-border); border-radius: 7px; background: var(--color-card); color: var(--color-text); padding: 0.55rem 0.7rem; font: inherit; font-size: 0.85rem; }
.input-with-action, .input-with-icon { position: relative; display: flex; min-width: 0; }
.input-with-action .input { padding-right: 4.5rem; }
.apply-button { position: absolute; top: 0.25rem; right: 0.25rem; bottom: 0.25rem; border: 0; border-radius: 5px; background: var(--color-text); color: var(--color-card); padding: 0 0.75rem; font-size: 0.78rem; font-weight: 650; cursor: pointer; }
.input-with-icon svg { position: absolute; left: 0.7rem; top: 50%; color: var(--color-muted); pointer-events: none; transform: translateY(-50%); }
.input-with-icon input { width: 100%; padding-left: 2.1rem; }
.clear-filters { min-height: 2.75rem; white-space: nowrap; }
.table-scroll { overflow-x: auto; }
.audit-table { min-width: 69rem; }
.audit-table th, .audit-table td { padding: 0.8rem; vertical-align: middle; }
.audit-table th { white-space: nowrap; text-transform: none; letter-spacing: 0; }
.audit-table tbody tr:last-child td { border-bottom: 0; }
.row-data-issue { background: color-mix(in srgb, var(--color-stale) 24%, var(--color-card)); }
.time-cell { width: 11rem; color: var(--color-muted); font-size: 0.78rem; font-variant-numeric: tabular-nums; white-space: nowrap; }
.primary-secondary { display: flex; min-width: 0; flex-direction: column; gap: 0.2rem; }
.primary-secondary strong { font-size: 0.83rem; }
.primary-secondary span, .action-button span { max-width: 13rem; overflow: hidden; color: var(--color-muted); font-size: 0.72rem; text-overflow: ellipsis; white-space: nowrap; }
.action-cell { min-width: 14rem; }
.action-button { display: flex; min-width: 0; flex-direction: column; align-items: flex-start; gap: 0.2rem; border: 0; background: transparent; color: var(--color-text); padding: 0; text-align: left; cursor: pointer; }
.action-button:hover strong { color: var(--color-accent); }
.resource-value { display: block; max-width: 16rem; overflow: hidden; overflow-wrap: anywhere; text-overflow: ellipsis; }

.view-button { display: inline-flex; min-height: 2.75rem; align-items: center; gap: 0.2rem; border: 0; border-radius: 6px; background: transparent; color: var(--color-accent); font-size: 0.78rem; font-weight: 650; cursor: pointer; }
.view-button:hover { background: color-mix(in srgb, var(--color-accent) 10%, transparent); }
.loading-state, .error-state, .empty-state { display: flex; align-items: center; justify-content: center; gap: 0.6rem; color: var(--color-muted); }
.loading-state { min-height: 14rem; }
.error-state { min-height: 14rem; align-items: flex-start; padding: 2rem; color: var(--color-danger); }
.error-state p { margin: 0.35rem 0 0.85rem; color: var(--color-muted); }
.empty-cell { padding: 2.75rem 1rem !important; }
.empty-state { flex-direction: column; text-align: center; }
.empty-state strong { color: var(--color-text); }
.empty-state span { font-size: 0.8rem; }
.mobile-audit-list { display: none; }
.pagination { display: flex; min-height: 4.5rem; align-items: center; justify-content: space-between; gap: 1rem; padding: 0.75rem 1.25rem; border-top: 1px solid var(--color-border); background: color-mix(in srgb, var(--color-bg) 35%, var(--color-card)); }
.page-size-control { display: flex; align-items: center; gap: 0.55rem; color: var(--color-muted); font-size: 0.78rem; font-weight: 600; }
.page-size-control select { min-width: 4.5rem; min-height: 2.75rem; border: 1px solid var(--color-border); border-radius: 7px; background: var(--color-card); color: var(--color-text); padding: 0.45rem 1.8rem 0.45rem 0.65rem; font: inherit; }
.pagination-controls, .page-numbers { display: flex; align-items: center; gap: 0.35rem; }
.pagination-arrow, .page-number { display: inline-flex; width: 2.75rem; height: 2.75rem; align-items: center; justify-content: center; border: 1px solid var(--color-border); border-radius: 7px; background: var(--color-card); color: var(--color-text); font-size: 0.8rem; font-weight: 650; cursor: pointer; }
.page-gap { display: inline-flex; width: 2.75rem; height: 2.75rem; align-items: center; justify-content: center; color: var(--color-muted); }
.pagination-arrow:hover:not(:disabled), .page-number:hover:not(.active) { border-color: color-mix(in srgb, var(--color-text) 25%, var(--color-border)); background: color-mix(in srgb, var(--color-text) 5%, var(--color-card)); }
.page-number.active { border-color: var(--color-text); background: var(--color-text); color: var(--color-card); }
.pagination-arrow:disabled { opacity: 0.42; cursor: not-allowed; }
.pagination-arrow:focus-visible, .page-number:focus-visible, .page-size-control select:focus-visible { outline: 3px solid color-mix(in srgb, var(--color-accent) 55%, transparent); outline-offset: 2px; }
.mobile-page-status { display: none; min-width: 5.5rem; color: var(--color-muted); font-size: 0.78rem; text-align: center; }
.detail-content { display: flex; flex-direction: column; gap: 1.25rem; }
.detail-lead { align-items: flex-start; padding-bottom: 1rem; border-bottom: 1px solid var(--color-border); }
.detail-lead h3 { margin: 0.2rem 0 0.35rem; font-size: 1.05rem; }
.detail-label, .evidence-grid dt { color: var(--color-muted); font-size: 0.72rem; font-weight: 650; text-transform: uppercase; }
.detail-statuses { flex-wrap: wrap; justify-content: flex-end; gap: 0.5rem; }
.evidence-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0; margin: 0; border: 1px solid var(--color-border); border-radius: 7px; overflow: hidden; }
.evidence-grid > div { min-width: 0; padding: 0.85rem 1rem; border-right: 1px solid var(--color-border); border-bottom: 1px solid var(--color-border); }
.evidence-grid > div:nth-child(2n) { border-right: 0; }
.evidence-grid > div:nth-last-child(-n + 2) { border-bottom: 0; }
.evidence-grid dd { display: flex; min-width: 0; flex-direction: column; gap: 0.2rem; margin: 0.3rem 0 0; overflow-wrap: anywhere; }
code, .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.operation-section { padding: 0.9rem 1rem; border: 1px solid var(--color-border); border-radius: 7px; background: var(--color-bg); }
.operation-section > div { display: flex; min-width: 0; flex-direction: column; gap: 0.3rem; }
.operation-section code { overflow-wrap: anywhere; }
.metadata-section h3 { margin-bottom: 0.55rem; font-size: 0.9rem; }
.metadata-section pre { max-height: 22rem; overflow: auto; margin: 0; border: 1px solid var(--color-border); border-radius: 7px; background: #171717; color: #f5f5f5; padding: 1rem; font-size: 0.78rem; line-height: 1.55; white-space: pre-wrap; overflow-wrap: anywhere; }
.spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 1180px) { .filter-toolbar { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media (max-width: 768px) {
  .audit-page { gap: 0.8rem; }
  .page-header { align-items: flex-start; }
  .page-header .btn { min-width: 2.75rem; padding-inline: 0.75rem; }
  .summary-strip { display: grid; grid-template-columns: repeat(2, 1fr); }
  .summary-item { min-width: 0; border-bottom: 1px solid var(--color-border); }
  .summary-item:nth-child(2n) { border-right: 0; }
  .summary-meta { grid-column: 1 / -1; justify-content: space-between; text-align: left; }
  .section-heading { align-items: flex-start; }
  .filter-toolbar { grid-template-columns: 1fr; }
  .clear-filters { width: 100%; }
  .desktop-audit-table { display: none; }
  .mobile-audit-list { display: flex; flex-direction: column; }
  .mobile-audit-item { display: flex; min-width: 0; width: 100%; flex-direction: column; gap: 0.5rem; border: 0; border-bottom: 1px solid var(--color-border); background: var(--color-card); color: var(--color-text); padding: 1rem; text-align: left; cursor: pointer; }
  .mobile-audit-item:last-child { border-bottom: 0; }
  .mobile-audit-item:active { background: color-mix(in srgb, var(--color-text) 5%, var(--color-card)); }
  .mobile-topline, .mobile-bottomline, .mobile-meta { display: flex; min-width: 0; align-items: center; justify-content: space-between; gap: 0.6rem; }
  .mobile-topline strong { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mobile-meta { justify-content: flex-start; flex-wrap: wrap; color: var(--color-muted); font-size: 0.75rem; }
  .mobile-meta span + span::before { margin-right: 0.6rem; content: "·"; }
  .mobile-bottomline .mono { max-width: 65%; overflow: hidden; color: var(--color-muted); font-size: 0.72rem; text-overflow: ellipsis; white-space: nowrap; }
  .mobile-empty-state { min-height: 14rem; padding: 2rem 1rem; }
  .pagination { align-items: stretch; flex-direction: column; }
  .page-size-control { justify-content: space-between; }
  .pagination-controls { justify-content: space-between; }
  .page-numbers { display: none; }
  .mobile-page-status { display: inline; }
  .evidence-grid { grid-template-columns: 1fr; }
  .evidence-grid > div, .evidence-grid > div:nth-child(2n), .evidence-grid > div:nth-last-child(-n + 2) { border-right: 0; border-bottom: 1px solid var(--color-border); }
  .evidence-grid > div:last-child { border-bottom: 0; }
  .operation-section { align-items: stretch; flex-direction: column; }
}
@media (prefers-reduced-motion: reduce) { .spin { animation: none; } }
</style>
