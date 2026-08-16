<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { computed } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { useClusterClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";
import { formatPercent, mapOverview } from "./clusterView";

const { t } = useI18n();

const POLL_MS = 5000;
const client = useClusterClient();

const query = useQuery({
  queryKey: ["cluster", "overview"],
  queryFn: () => client.overview({}),
  refetchInterval: POLL_MS,
});

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
  grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
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
</style>
