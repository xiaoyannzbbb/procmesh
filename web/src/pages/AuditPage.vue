<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, type Freshness } from "../lib/freshness";
import { useAuditClient } from "../lib/rpc";
import { formatRemoteError } from "./processView";

const POLL_MS = 5000;
const NOTICE = "Audit is per-node; unreachable nodes are marked STALE.";
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
  resource: string;
  sourceNode: string;
  targetAgent: string;
  result: string;
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
  const result = ev.result ?? "";
  return {
    key: ev.auditId || `${entry.sourceNode ?? ""}:${index}`,
    time: formatMs(ev.timestampUnixMs),
    user: ev.username || ev.userId || "—",
    action: ev.action || "—",
    resource: ev.resource || "—",
    sourceNode: entry.sourceNode || "—",
    targetAgent: ev.targetAgent || "—",
    result: result || "—",
    freshness: auditFreshness(entry.freshness, result),
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
    <h1>Audit</h1>
    <p class="muted notice">{{ NOTICE }}</p>
    <label class="field">
      Resource
      <input v-model="resource" class="input" name="resource" type="text" placeholder="Filter resource" />
    </label>
    <p v-if="query.isPending && !query.data" class="muted">Loading…</p>
    <p v-else-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <div v-else class="card">
      <table class="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>User</th>
            <th>Action</th>
            <th>Resource</th>
            <th>Source node</th>
            <th>Target agent</th>
            <th>Result</th>
            <th>Freshness</th>
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
            <td colspan="8" class="muted">No audit entries</td>
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
  border-radius: var(--radius-lg);
  background: var(--color-card);
  overflow: auto;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
</style>
