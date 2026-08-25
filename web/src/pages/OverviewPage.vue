<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { computed } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, type Freshness } from "../lib/freshness";
import { useAlertClient, useBatchClient, useClusterClient, useNodeClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";
import { formatPercent, mapOverview } from "./clusterView";

const { t } = useI18n();

const POLL_MS = 5000;
const client = useClusterClient();
const nodeClient = useNodeClient();
const batchClient = useBatchClient();
const alertClient = useAlertClient();

const query = useQuery({
  queryKey: ["cluster", "overview"],
  queryFn: () => client.overview({}),
  refetchInterval: POLL_MS,
});

const recentQuery = useQuery({
  queryKey: ["batches", "recent"],
  queryFn: () => batchClient.listBatches({ limit: 5 }),
  refetchInterval: POLL_MS,
});

const recentBatches = computed(() => recentQuery.data.value?.batches ?? []);

const nodesQuery = useQuery({
  queryKey: ["nodes"],
  queryFn: () => nodeClient.listNodes({}),
  refetchInterval: POLL_MS,
});
const nodeHostnames = computed(
  () => new Map((nodesQuery.data.value?.nodes ?? []).map((node) => [node.nodeId, node.hostname])),
);

const recentAlertsQuery = useQuery({
  queryKey: ["alerts", "recent"],
  queryFn: () => alertClient.listAlerts({ limit: 5 }),
  refetchInterval: POLL_MS,
});

const recentAlertEntries = computed(() => recentAlertsQuery.data.value?.entries ?? []);
const recentAlertsError = computed(() => {
  const err = recentAlertsQuery.error.value;
  if (!err) {
    return "";
  }
  return err instanceof Error ? err.message : String(err);
});
const recentAlertHasStale = computed(() =>
  recentAlertEntries.value.some((e) => freshnessOf(e.freshness) === STALE),
);
const recentAlertRows = computed(() =>
  recentAlertEntries.value.map((entry, index) => mapRecentAlert(entry, index, nodeHostnames.value)),
);
const showRecentAlertsEmpty = computed(
  () =>
    !!recentAlertsQuery.data.value &&
    !recentAlertsError.value &&
    !recentAlertHasStale.value &&
    !recentAlertRows.value.length,
);

function freshnessOf(raw: string | undefined): Freshness {
  if (raw === LIVE || raw === STALE || raw === UNKNOWN) {
    return raw;
  }
  return UNKNOWN;
}

function mapRecentAlert(
  entry: {
    alert?: {
      alertId?: string;
      fingerprint?: string;
      type?: string;
      state?: string;
      nodeId?: string;
    };
    sourceNode?: string;
    freshness?: string;
  },
  index: number,
  hostnames: ReadonlyMap<string, string>,
) {
  const alert = entry.alert;
  const freshness = freshnessOf(entry.freshness);
  const nodeId = alert?.nodeId || entry.sourceNode || "";
  return {
    key: alert?.alertId || `${entry.sourceNode ?? "node"}:${index}`,
    fingerprint: alert?.fingerprint || "—",
    type: alert?.type || "—",
    state: (alert?.state || "").toUpperCase(),
    nodeId,
    node: hostnames.get(nodeId) || nodeId || "—",
    freshness,
  };
}

function alertStateLabel(state: string): string {
  if (state === "FIRING") {
    return t("alert.firing");
  }
  if (state === "RESOLVED") {
    return t("alert.resolved");
  }
  return state || "—";
}

const view = computed(() => {
  const data = query.data.value;
  return data ? mapOverview(data) : null;
});

const errorText = computed(() => {
  const err = query.error.value;
  if (!err) {
    return "";
  }
  return err instanceof Error ? err.message : String(err);
});
</script>

<template>
  <div class="page">
    <h1>{{ t("overview.title") }}</h1>
    <p v-if="query.isPending && !view" class="muted">{{ t("overview.loading") }}</p>
    <p v-else-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <template v-else-if="view">
      <div v-if="view.procMesh.platformNote" class="banner platform-note" role="status">
        {{ view.procMesh.platformNote }}
      </div>
      <div v-if="view.procMesh.degradedBanner" class="banner degraded" role="status">
        {{ view.procMesh.degradedBanner }}
      </div>

      <section class="card">
        <h2>{{ t("overview.procMesh.title") }}</h2>
        <p v-if="view.clusterId" class="muted cluster-id">{{ t("overview.cluster") }} {{ view.clusterId }}</p>
        <dl class="facts">
          <div>
            <dt>{{ t("overview.procMesh.controlQuorum") }}</dt>
            <dd class="quorum" :class="{ danger: !view.procMesh.controlQuorum }">
              {{ view.procMesh.controlQuorumLabel }}
            </dd>
          </div>
          <div>
            <dt>{{ t("overview.procMesh.gossip") }}</dt>
            <dd>{{ view.procMesh.gossipHealthy ? t("overview.procMesh.healthy") : t("overview.procMesh.unhealthy") }}</dd>
          </div>
          <div>
            <dt>{{ t("overview.procMesh.rpc") }}</dt>
            <dd>{{ view.procMesh.rpcHealthy ? t("overview.procMesh.healthy") : t("overview.procMesh.unhealthy") }}</dd>
          </div>
          <div>
            <dt>{{ t("overview.procMesh.certExpiry") }}</dt>
            <dd>{{ view.procMesh.certExpires || "—" }}</dd>
          </div>
          <div>
            <dt>{{ t("overview.procMesh.caExpiry") }}</dt>
            <dd>{{ view.procMesh.caExpires || "—" }}</dd>
          </div>
          <div v-if="view.procMesh.controlLeader">
            <dt>{{ t("overview.procMesh.controlLeader") }}</dt>
            <dd>{{ view.procMesh.controlLeader }}</dd>
          </div>
        </dl>
        <div v-if="view.procMesh.versionCounts.length" class="versions">
          <h3>{{ t("overview.procMesh.versions") }}</h3>
          <ul>
            <li v-for="item in view.procMesh.versionCounts" :key="item.version">
              {{ item.version }} × {{ item.count }}
            </li>
          </ul>
        </div>
      </section>

      <section class="card">
        <div class="title-row">
          <h2>{{ t("overview.workload.title") }}</h2>
          <FreshnessBadge :status="view.workload.freshness" />
          <span class="muted">{{ view.workload.lastUpdated }}</span>
        </div>
        <div class="stats">
          <div class="stat">
            <span class="stat-label">{{ t("overview.workload.agentTotal") }}</span>
            <span class="stat-value">{{ view.workload.agentTotal }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t("overview.workload.alive") }}</span>
            <span class="stat-value">{{ view.workload.agentAlive }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t("overview.workload.suspect") }}</span>
            <span class="stat-value">{{ view.workload.agentSuspect }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t("overview.workload.failed") }}</span>
            <span class="stat-value">{{ view.workload.agentFailed }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t("overview.workload.processTotal") }}</span>
            <span class="stat-value">{{ view.workload.processTotal }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t("overview.workload.running") }}</span>
            <span class="stat-value">{{ view.workload.processRunning }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t("overview.workload.unhealthy") }}</span>
            <span class="stat-value">{{ view.workload.processUnhealthy }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t("overview.workload.fatal") }}</span>
            <span class="stat-value">{{ view.workload.processFatal }}</span>
          </div>
        </div>
        <dl class="facts resources">
          <div>
            <dt>{{ t("overview.workload.cpu") }}</dt>
            <dd>{{ formatPercent(view.workload.cpuPercent) }}</dd>
          </div>
          <div>
            <dt>{{ t("overview.workload.memory") }}</dt>
            <dd>{{ formatPercent(view.workload.memoryPercent) }}</dd>
          </div>
          <div>
            <dt>{{ t("overview.workload.disk") }}</dt>
            <dd>{{ formatPercent(view.workload.diskPercent) }}</dd>
          </div>
        </dl>
      </section>

      <section class="card" data-testid="recent-alerts">
        <h2>{{ t("overview.recentAlerts") }}</h2>
        <p v-if="recentAlertsError" class="error" role="alert">{{ recentAlertsError }}</p>
        <div
          v-if="recentAlertsError || recentAlertHasStale"
          class="banner alert-stale-banner"
          role="status"
        >
          {{ t("alert.staleBanner") }}
        </div>
        <table v-if="recentAlertRows.length" class="table">
          <thead>
            <tr>
              <th>{{ t("alert.fingerprint") }}</th>
              <th>{{ t("alert.type") }}</th>
              <th>{{ t("alert.state") }}</th>
              <th>{{ t("alert.node") }}</th>
              <th>{{ t("alert.freshness") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in recentAlertRows"
              :key="row.key"
              :data-freshness="row.freshness"
              :class="{ 'row-stale': row.freshness === 'STALE' }"
            >
              <td class="mono">{{ row.fingerprint }}</td>
              <td>{{ row.type }}</td>
              <td>
                <span
                  class="alert-state"
                  :class="{ 'alert-firing': row.state === 'FIRING' }"
                  :data-state="row.state || undefined"
                >{{ alertStateLabel(row.state) }}</span>
              </td>
              <td class="mono">
                <RouterLink
                  v-if="row.nodeId"
                  data-testid="recent-alert-node"
                  :to="`/nodes/${encodeURIComponent(row.nodeId)}`"
                >{{ row.node }}</RouterLink>
                <span v-else>{{ row.node }}</span>
              </td>
              <td><FreshnessBadge :status="row.freshness" /></td>
            </tr>
          </tbody>
        </table>
        <p v-else-if="showRecentAlertsEmpty" class="muted">{{ t("alert.noAlerts") }}</p>
      </section>

      <section v-if="recentQuery.data" class="card">
        <h2>{{ t("overview.recentBatches") }}</h2>
        <p class="muted recent-hint">{{ t("overview.recentBatchesHint") }}</p>
        <table v-if="recentBatches.length" class="table">
          <thead>
            <tr>
              <th>{{ t("batch.batchId") }}</th>
              <th>{{ t("batch.type") }}</th>
              <th>{{ t("batch.status") }}</th>
              <th>{{ t("batch.timeout") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="batch in recentBatches" :key="batch.batchId">
              <td>
                <RouterLink :to="`/batches/${encodeURIComponent(batch.batchId)}`">{{ batch.batchId }}</RouterLink>
              </td>
              <td>{{ batch.type }}</td>
              <td>{{ batch.status }}</td>
              <td>
                <span
                  class="status-timeout"
                  data-status="TIMEOUT"
                  style="background-color: #FEF3C7; color: #92400E"
                >{{ t("batch.timeout") }} {{ batch.summary?.timeout ?? 0 }}</span>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-else class="muted">{{ t("batch.noBatches") }}</p>
      </section>
    </template>
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
h2 {
  margin: 0 0 0.75rem;
  font-size: 1.05rem;
  font-weight: 650;
}
.title-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 0.75rem;
}
.title-row h2 {
  margin: 0;
}
h3 {
  margin: 0 0 0.375rem;
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--color-muted);
}
.muted {
  color: var(--color-muted);
  font-size: 0.875rem;
}
.cluster-id {
  margin: -0.375rem 0 0.75rem;
}
.error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
}
.banner {
  border-radius: 10px;
  padding: 0.75rem 1rem;
  font-size: 0.875rem;
  line-height: 1.4;
}
.platform-note {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.degraded {
  background: #fee2e2;
  color: var(--color-danger);
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  padding: 1.25rem;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 0.75rem 1.25rem;
  margin: 0;
}
.facts div {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
}
.facts dt {
  font-size: 0.75rem;
  color: var(--color-muted);
}
.facts dd {
  margin: 0;
  font-size: 0.95rem;
  font-weight: 550;
}
.quorum.danger {
  color: var(--color-danger);
}
.versions {
  margin-top: 1rem;
}
.versions ul {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem 1rem;
  font-size: 0.875rem;
}
.stats {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(100px, 1fr));
  gap: 0.75rem;
}
.stat {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  padding: 0.75rem;
  border: 1px solid var(--color-border);
  border-radius: 10px;
}
.stat-label {
  font-size: 0.75rem;
  color: var(--color-muted);
}
.stat-value {
  font-size: 1.25rem;
  font-weight: 650;
}
.resources {
  margin-top: 1rem;
}
.recent-hint {
  margin: -0.375rem 0 0.75rem;
}
.status-timeout {
  display: inline-flex;
  align-items: center;
  border-radius: 999px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
}
.alert-stale-banner {
  background: var(--color-stale);
  color: var(--color-stale-fg);
  margin-bottom: 0.75rem;
}
.alert-state {
  font-weight: 600;
}
.alert-firing {
  color: var(--color-danger);
}
.row-stale,
tr[data-freshness="STALE"] {
  background-color: #fef3c7;
  color: #92400e;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
</style>
