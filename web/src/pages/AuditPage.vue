<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, type Freshness } from "../lib/freshness";
import { useAuditClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";
import { useAudit } from "../lib/useAudit";
import { formatRemoteError } from "./processView";

const { t } = useI18n();
const { formatAuditAction, formatAuditResult } = useAudit();
const POLL_MS = 5000;
const client = useAuditClient();
const resource = ref("");

const query = useQuery({
  queryKey: computed(() => ["audit", resource.value.trim()]),
  queryFn: () =>
    client.listAudit({
      resource: resource.value.trim(),
      limit: 50,
      targetNode: "",
    }),
  refetchInterval: POLL_MS,
});

const entries = computed(() => (query.data.value?.entries ?? []).map(mapEntry));

const errorText = computed(() => {
  const err = query.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

type AuditRow = {
  key: string;
  time: string;
  user: string;
  action: string;
  actionCode: string;
  resource: string;
  sourceNode: string;
  targetAgent: string;
  result: string;
  resultCode: string;
  freshness: Freshness;
};

function mapEntry(entry: {
  event?: {
    auditId?: string;
    timestampUnixMs?: bigint | number;
    username?: string;
    userId?: string;
    action?: string;
    resource?: string;
    targetAgent?: string;
    result?: string;
  };
  sourceNode?: string;
  freshness?: string;
  lastUpdatedUnixMs?: bigint | number;
}, index: number): AuditRow {
  const ev = entry.event ?? {};
  const resultCode = ev.result ?? "";
  const actionCode = ev.action ?? "";
  return {
    key: ev.auditId || `${entry.sourceNode ?? ""}:${index}`,
    time: formatMs(ev.timestampUnixMs),
    user: ev.username || ev.userId || "—",
    action: actionCode ? formatAuditAction(actionCode, {}) : "—",
    actionCode,
    resource: ev.resource || "—",
    sourceNode: entry.sourceNode || "—",
    targetAgent: ev.targetAgent || "—",
    result: resultCode ? formatAuditResult(resultCode) : "—",
    resultCode,
    freshness: auditFreshness(entry.freshness, resultCode),
  };
}

function auditFreshness(raw: string | undefined, result: string): Freshness {
  const value = raw === LIVE || raw === STALE || raw === UNKNOWN ? raw : UNKNOWN;
  if (result === "UNAVAILABLE" && value === LIVE) {
    return STALE;
  }
  return value;
}

function formatMs(ms: bigint | number | undefined): string {
  const n = Number(ms ?? 0);
  if (!Number.isFinite(n) || n <= 0) {
    return "—";
  }
  return new Date(n).toISOString();
}
</script>

<template>
  <div class="page">
    <h1>{{ t("common:audit.title") }}</h1>
    <p class="muted notice">{{ t("common:audit.notice") }}</p>
    <label class="field">
      {{ t("common:audit.resourceLabel") }}
      <input v-model="resource" class="input" name="resource" type="text" :placeholder="t('common:audit.resourcePlaceholder')" />
    </label>
    <p v-if="query.isPending && !query.data" class="muted">{{ t("common:audit.loading") }}</p>
    <p v-else-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <div v-else class="card">
      <table class="table">
        <thead>
          <tr>
            <th>{{ t("common:audit.table.time") }}</th>
            <th>{{ t("common:audit.table.user") }}</th>
            <th>{{ t("common:audit.table.action") }}</th>
            <th>{{ t("common:audit.table.resource") }}</th>
            <th>{{ t("common:audit.table.sourceNode") }}</th>
            <th>{{ t("common:audit.table.targetAgent") }}</th>
            <th>{{ t("common:audit.table.result") }}</th>
            <th>{{ t("common:audit.table.freshness") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in entries" :key="row.key">
            <td>{{ row.time }}</td>
            <td>{{ row.user }}</td>
            <td>{{ row.action }}</td>
            <td>{{ row.resource }}</td>
            <td class="mono">{{ row.sourceNode }}</td>
            <td class="mono">{{ row.targetAgent }}</td>
            <td>{{ row.result }}</td>
            <td>
              <FreshnessBadge :status="row.freshness" />
            </td>
          </tr>
          <tr v-if="!entries.length">
            <td colspan="8" class="muted">{{ t("common:audit.noEntries") }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
h1 {
  margin: 0;
  font-size: 1.35rem;
  font-weight: 650;
}
.muted {
  color: var(--color-muted);
  font-size: 0.875rem;
}
.notice {
  margin: 0;
}
.error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  max-width: 360px;
  font-size: 0.875rem;
  color: var(--color-muted);
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  overflow: auto;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
</style>
