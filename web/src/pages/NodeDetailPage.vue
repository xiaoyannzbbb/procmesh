<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { TriangleAlert } from "lucide-vue-next";
import { computed, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import HistoryChart from "../components/HistoryChart.vue";
import {
  historyWindow,
  isHistoryUnavailable,
  pointsFromSeries,
  stepSecForLayer,
  type HistoryRange,
} from "../lib/historyChart";
import { newOperationId } from "../lib/opid";
import { useMetricsClient, useNodeClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { useProcessState } from "../lib/useProcessState";
import { formatPercent, mapNode, REMOVE_CONFIRM } from "./clusterView";

const { t } = useI18n();
const { translateDesiredState, translateObservedState, translateHealthState } = useProcessState();

const POLL_MS = 5000;
const HISTORY_POLL_MS = 60_000;
const POLITE_LIVE_REGION = "polite";
const ATOMIC_LIVE_REGION = true;
const route = useRoute();
const router = useRouter();
const client = useNodeClient();
const metrics = useMetricsClient();
const queryClient = useQueryClient();
const actionError = ref("");
const range = ref<HistoryRange>("24h");

const id = computed(() => String(route.params.id ?? ""));
const canRemove = computed(() => (session.value?.permissions ?? []).includes("node.remove"));

const query = useQuery({
  queryKey: computed(() => ["nodes", id.value]),
  queryFn: () => client.getNode({ idOrHostname: id.value }),
  refetchInterval: POLL_MS,
  enabled: computed(() => id.value.length > 0),
});

const node = computed(() => {
  const raw = query.data.value?.node;
  return raw ? mapNode(raw, Date.now()) : null;
});

const historyQuery = useQuery({
  queryKey: computed(() => ["node-history", id.value, node.value?.nodeId ?? "", range.value]),
  queryFn: () => {
    const { sinceUnix, untilUnix } = historyWindow(range.value);
    return metrics.getNodeHistory({
      nodeId: node.value?.nodeId ?? "",
      sinceUnix,
      untilUnix,
    });
  },
  refetchInterval: HISTORY_POLL_MS,
  enabled: computed(() => Boolean(node.value?.nodeId)),
  retry: false,
});

const historyStale = computed(() => isHistoryUnavailable(historyQuery.error.value));
const historyStepSec = computed(() =>
  stepSecForLayer(historyQuery.data.value?.layer || historyQuery.data.value?.series?.[0]?.layer || ""),
);
const cpuPoints = computed(() => pointsFromSeries(historyQuery.data.value?.series, "cpu_percent"));
const memPoints = computed(() => pointsFromSeries(historyQuery.data.value?.series, "memory_percent"));
const diskPoints = computed(() => pointsFromSeries(historyQuery.data.value?.series, "disk_percent"));

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  const err = query.error.value;
  if (!err) {
    return "";
  }
  return err instanceof Error ? err.message : String(err);
});

const remove = useMutation({
  mutationFn: async (nodeId: string) => {
    return client.removeNode({
      meta: {
        operationId: newOperationId(),
        operator: session.value?.username ?? "",
      },
      nodeId,
    });
  },
  onSuccess: async () => {
    await queryClient.invalidateQueries({ queryKey: ["nodes"] });
    await router.push("/nodes");
  },
  onError: (err: unknown) => {
    actionError.value = err instanceof Error ? err.message : String(err);
  },
});

const removing = computed(() => remove.isPending.value);

async function onRemove(): Promise<void> {
  if (!node.value || !canRemove.value) {
    return;
  }
  if (!window.confirm(REMOVE_CONFIRM)) {
    return;
  }
  actionError.value = "";
  await remove.mutateAsync(node.value.nodeId);
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

function toneForDesired(state: string): string {
  return state === "RUNNING" ? "ok" : "neutral";
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
</script>

<template>
  <div class="page">
    <div class="head">
      <div>
        <RouterLink class="back" to="/nodes">{{ t("nodeDetail.back") }}</RouterLink>
        <div class="heading-row">
          <h1>{{ node?.hostname || id }}</h1>
          <span
            v-if="node?.state"
            class="state-pill"
            :class="stateTone(node.state)"
            :data-state="node.state"
            :aria-label="t('nodes.state.badgeLabel', { state: stateLabel(node.state) })"
          >
            {{ stateLabel(node.state) }}
          </span>
        </div>
      </div>
      <button v-if="canRemove && node" type="button" class="btn btn-danger" :disabled="removing" @click="onRemove">
        {{ t("nodeDetail.removeAgent") }}
      </button>
    </div>
    <p v-if="query.isPending && !node" class="muted">{{ t("nodeDetail.loading") }}</p>
    <p v-else-if="errorText && !node" class="error" role="alert">{{ errorText }}</p>
    <template v-else-if="node">
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <section class="card">
        <div class="title-row">
          <h2>{{ t("nodeDetail.node.title") }}</h2>
          <FreshnessBadge :status="node.freshness" />
          <span class="muted">{{ node.lastUpdated }}</span>
        </div>
        <dl class="facts">
          <div>
            <dt>{{ t("nodeDetail.node.hostname") }}</dt>
            <dd>{{ node.hostname || "—" }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.nodeId") }}</dt>
            <dd class="mono">{{ node.nodeId }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.address") }}</dt>
            <dd>
              <div>{{ t("nodeDetail.node.api") }} {{ node.apiAddress || "—" }}</div>
              <div>{{ t("nodeDetail.node.rpc") }} {{ node.rpcAddress || "—" }}</div>
              <div>{{ t("nodeDetail.node.gossip") }} {{ node.gossipAddress || "—" }}</div>
            </dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.version") }}</dt>
            <dd>{{ node.agentVersion || "—" }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.status") }}</dt>
            <dd>
              <span
                v-if="node.state"
                class="state-pill"
                :class="stateTone(node.state)"
                :data-state="node.state"
                :aria-label="t('nodes.state.badgeLabel', { state: stateLabel(node.state) })"
              >
                {{ stateLabel(node.state) }}
              </span>
              <span v-else>—</span>
            </dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.bootId") }}</dt>
            <dd class="mono">{{ node.bootId || "—" }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.cpu") }}</dt>
            <dd>{{ formatPercent(node.resources.cpuPercent) }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.memory") }}</dt>
            <dd>{{ formatPercent(node.resources.memoryPercent) }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.disk") }}</dt>
            <dd>{{ formatPercent(node.resources.diskPercent) }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.processCount") }}</dt>
            <dd>{{ node.processCount }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.labels") }}</dt>
            <dd>
              <ul v-if="node.labels.length" class="labels">
                <li v-for="label in node.labels" :key="label.key">{{ label.key }}={{ label.value }}</li>
              </ul>
              <span v-else>—</span>
            </dd>
          </div>
        </dl>
      </section>

      <section class="card">
        <div class="title-row">
          <h2>{{ t("metricsHistory.title") }}</h2>
          <div class="range-toggle">
            <button type="button" :class="{ active: range === '24h' }" @click="range = '24h'">
              {{ t("metricsHistory.range24h") }}
            </button>
            <button type="button" :class="{ active: range === '7d' }" @click="range = '7d'">
              {{ t("metricsHistory.range7d") }}
            </button>
          </div>
        </div>
        <div
          v-if="node.resources.historyWritesPaused && node.resources.historyPausePercent > 0"
          class="history-pause-notice"
          role="status"
          :aria-live="POLITE_LIVE_REGION"
          :aria-atomic="ATOMIC_LIVE_REGION"
        >
          <TriangleAlert class="history-pause-icon" :size="20" aria-hidden="true" />
          <div class="history-pause-content">
            <strong>{{ t("nodeDetail.historyPause.title") }}</strong>
            <p>
              {{
                t("nodeDetail.historyPause.description", {
                  current: node.resources.diskPercent,
                  threshold: node.resources.historyPausePercent,
                })
              }}
            </p>
          </div>
        </div>
        <p v-if="historyQuery.isPending && !historyQuery.data" class="muted">{{ t("metricsHistory.loading") }}</p>
        <div v-else class="charts">
          <HistoryChart
            :title="t('metricsHistory.cpu')"
            kind="cpu"
            unit="percent"
            :points="cpuPoints"
            :step-sec="historyStepSec"
            :stale="historyStale"
          />
          <HistoryChart
            :title="t('metricsHistory.memory')"
            kind="memory"
            unit="percent"
            :points="memPoints"
            :step-sec="historyStepSec"
            :stale="historyStale"
          />
          <HistoryChart
            :title="t('metricsHistory.disk')"
            kind="disk"
            unit="percent"
            :points="diskPoints"
            :step-sec="historyStepSec"
            :stale="historyStale"
          />
        </div>
      </section>

      <section class="card">
        <h2>{{ t("nodeDetail.processes.title") }}</h2>
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("nodeDetail.processes.table.name") }}</th>
              <th>{{ t("nodeDetail.processes.table.desired") }}</th>
              <th>{{ t("nodeDetail.processes.table.observed") }}</th>
              <th>{{ t("nodeDetail.processes.table.health") }}</th>
              <th>{{ t("nodeDetail.processes.table.revisions") }}</th>
              <th>{{ t("nodeDetail.processes.table.freshness") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="proc in node.processes" :key="proc.name">
              <td>
                <RouterLink
                  :to="{
                    path: `/processes/${encodeURIComponent(proc.name)}`,
                    query: { node: node.nodeId },
                  }"
                >
                  {{ proc.name }}
                </RouterLink>
              </td>
              <td>
                <span v-if="proc.desired" class="state-pill" :class="toneForDesired(proc.desired)">
                  {{ translateDesiredState(proc.desired) }}
                </span>
                <span v-else class="muted">—</span>
              </td>
              <td>
                <span v-if="proc.observed" class="state-pill" :class="toneForObserved(proc.observed)">
                  {{ translateObservedState(proc.observed) }}
                </span>
                <span v-else class="muted">—</span>
              </td>
              <td>
                <span v-if="proc.health" class="state-pill" :class="toneForHealth(proc.health)">
                  {{ translateHealthState(proc.health) }}
                </span>
                <span v-else class="muted">—</span>
              </td>
              <td>{{ proc.activeRevision }} / {{ proc.latestRevision }}</td>
              <td>
                <FreshnessBadge :status="proc.freshness" />
              </td>
            </tr>
            <tr v-if="!node.processes.length">
              <td colspan="6" class="muted">{{ t("nodeDetail.processes.noProcesses") }}</td>
            </tr>
          </tbody>
        </table>
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
.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
}
.heading-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.55rem;
  margin-top: 0.25rem;
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
.back {
  color: var(--color-muted);
  text-decoration: none;
  font-size: 0.8rem;
}
.back:hover {
  color: var(--color-text);
}
a:not(.back) {
  color: var(--color-accent);
  text-decoration: none;
}
a:not(.back):hover {
  text-decoration: underline;
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
  padding: 1.25rem;
  overflow: auto;
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
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 0.85rem 1.25rem;
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
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
  font-weight: 500;
}
.labels {
  margin: 0;
  padding: 0;
  list-style: none;
}
.range-toggle {
  display: flex;
  gap: 0.35rem;
  margin-left: auto;
}
.range-toggle button {
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  color: var(--color-text);
  padding: 0.35rem 0.75rem;
  font-size: 0.8rem;
  font-weight: 550;
  cursor: pointer;
}
.range-toggle button.active {
  border-color: var(--color-text);
}
.history-pause-notice {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 0.75rem;
  align-items: start;
  margin-bottom: 1rem;
  border: 1px solid color-mix(in srgb, var(--color-stale-fg) 24%, var(--color-border));
  border-radius: var(--radius-sm);
  background: var(--color-stale);
  color: var(--color-stale-fg);
  padding: 0.875rem 1rem;
}
.history-pause-icon {
  flex: none;
  margin-top: 0.1rem;
}
.history-pause-content {
  min-width: 0;
}
.history-pause-content strong {
  display: block;
  font-size: 0.9rem;
  font-weight: 650;
  line-height: 1.4;
}
.history-pause-content p {
  margin: 0.25rem 0 0;
  font-size: 0.85rem;
  font-variant-numeric: tabular-nums;
  line-height: 1.55;
  overflow-wrap: anywhere;
}
.charts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 0.85rem;
}
.table td {
  vertical-align: middle;
  padding: 0.5rem 0.4rem;
}
.state-pill {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  border-radius: 3px;
  padding: 0.2rem 0.55rem;
  font-size: 0.75rem;
  font-weight: 650;
  line-height: 1.4;
  white-space: nowrap;
}
.state-pill.ok {
  background: var(--color-live);
  color: var(--color-live-fg);
}
.state-pill.warn {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.state-pill.neutral {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}
.state-pill.danger {
  background: color-mix(in srgb, var(--color-danger) 14%, var(--color-card));
  color: var(--color-danger);
}
</style>
