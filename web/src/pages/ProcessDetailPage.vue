<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { useRoute } from "vue-router";
import HistoryChart from "../components/HistoryChart.vue";
import { withTarget } from "../lib/headers";
import {
  historyWindow,
  isHistoryUnavailable,
  pointsFromSeries,
  stepSecForLayer,
  type HistoryRange,
} from "../lib/historyChart";
import { newOperationId } from "../lib/opid";
import { useMetricsClient, useNodeClient, useProcessClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { useProcessState } from "../lib/useProcessState";
import {
  flattenClusterProcesses,
  formatRemoteError,
  mapProcessDetail,
  rowsFromProcessViews,
} from "./processView";
import ProcessConfigPanel from "./ProcessConfigPanel.vue";
import ProcessLogsPanel from "./ProcessLogsPanel.vue";

const { t } = useI18n();
const { translateDesiredState, translateObservedState, translateHealthState } = useProcessState();

const POLL_MS = 5000;
const HISTORY_POLL_MS = 60_000;
const route = useRoute();
const nodes = useNodeClient();
const processes = useProcessClient();
const metrics = useMetricsClient();
const queryClient = useQueryClient();
const actionError = ref("");
const tab = ref<"overview" | "config" | "logs">("overview");
const range = ref<HistoryRange>("24h");

const idOrName = computed(() => String(route.params.idOrName ?? ""));
const routeNode = computed(() => {
  const raw = route.query.node;
  return typeof raw === "string" ? raw : "";
});

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canStart = computed(() => perms.value.has("process.start"));
const canStop = computed(() => perms.value.has("process.stop"));
const canRestart = computed(() => perms.value.has("process.restart"));

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

const gossipRows = computed(() => flattenClusterProcesses(nodesQuery.data.value?.nodes ?? [], Date.now()));
const listedRows = computed(() => rowsFromProcessViews(processesQuery.data.value?.processes ?? [], Date.now()));

const ownerNodeId = computed(() => {
  if (routeNode.value) {
    return routeNode.value;
  }
  const matches = gossipRows.value.filter((r) => r.name === idOrName.value);
  if (matches.length === 1) {
    return matches[0].ownerNodeId;
  }
  const listed = listedRows.value.filter((r) => r.name === idOrName.value);
  return listed.length === 1 ? listed[0].ownerNodeId : "";
});

const ownerHostname = computed(() => (
  nodesQuery.data.value?.nodes.find((node) => node.nodeId === ownerNodeId.value)?.hostname ?? ""
));

const ownerLabel = computed(() => {
  return ownerHostname.value || ownerNodeId.value;
});

const targetOpts = computed(() => ({ headers: withTarget(ownerNodeId.value) }));

const processQuery = useQuery({
  queryKey: computed(() => ["process", idOrName.value, ownerNodeId.value]),
  queryFn: () => processes.getProcess({ idOrName: idOrName.value }, targetOpts.value),
  refetchInterval: POLL_MS,
  enabled: computed(() => idOrName.value.length > 0),
});

const metricsQuery = useQuery({
  queryKey: computed(() => ["process-metrics", idOrName.value, ownerNodeId.value]),
  queryFn: () => metrics.getProcessMetrics({ idOrName: idOrName.value }, targetOpts.value),
  refetchInterval: POLL_MS,
  enabled: computed(() => idOrName.value.length > 0),
});

const historyQuery = useQuery({
  queryKey: computed(() => ["process-history", idOrName.value, ownerNodeId.value, range.value]),
  queryFn: () => {
    const { sinceUnix, untilUnix } = historyWindow(range.value);
    return metrics.getProcessHistory({ idOrName: idOrName.value, sinceUnix, untilUnix }, targetOpts.value);
  },
  refetchInterval: HISTORY_POLL_MS,
  enabled: computed(() => idOrName.value.length > 0),
  retry: false,
});

const historyStale = computed(() => isHistoryUnavailable(historyQuery.error.value));
const historyStepSec = computed(() =>
  stepSecForLayer(historyQuery.data.value?.layer || historyQuery.data.value?.series?.[0]?.layer || ""),
);
const cpuPoints = computed(() => pointsFromSeries(historyQuery.data.value?.series, "cpu_percent"));
const memPoints = computed(() => pointsFromSeries(historyQuery.data.value?.series, "memory_bytes"));

const detail = computed(() => {
  const raw = processQuery.data.value?.process;
  if (!raw) {
    return null;
  }
  return mapProcessDetail(raw, metricsQuery.data.value, Date.now(), ownerLabel.value);
});

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  if (processQuery.error.value) {
    return formatRemoteError(processQuery.error.value);
  }
  if (!ownerNodeId.value && nodesQuery.isFetched.value && processesQuery.isFetched.value && !processQuery.data.value) {
    return t("processDetail.ownerNodeRequired");
  }
  if (nodesQuery.error.value && processesQuery.error.value && !processQuery.data.value) {
    return formatRemoteError(nodesQuery.error.value);
  }
  return "";
});

const metricsNote = computed(() => {
  if (!metricsQuery.error.value) {
    return "";
  }
  return formatRemoteError(metricsQuery.error.value);
});

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

const acting = computed(
  () =>
    startMut.isPending.value ||
    stopMut.isPending.value ||
    restartMut.isPending.value ||
    killMut.isPending.value,
);

async function invalidateProcess(): Promise<void> {
  await queryClient.invalidateQueries({ queryKey: ["process", idOrName.value, ownerNodeId.value] });
  await queryClient.invalidateQueries({ queryKey: ["process-metrics", idOrName.value, ownerNodeId.value] });
  await queryClient.invalidateQueries({ queryKey: ["nodes"] });
}

function onActionError(err: unknown): void {
  actionError.value = formatRemoteError(err);
}

const startMut = useMutation({
  mutationFn: () =>
    processes.startProcess({ meta: mutationMeta(), idOrName: idOrName.value }, targetOpts.value),
  onSuccess: invalidateProcess,
  onError: onActionError,
});
const stopMut = useMutation({
  mutationFn: () =>
    processes.stopProcess({ meta: mutationMeta(), idOrName: idOrName.value }, targetOpts.value),
  onSuccess: invalidateProcess,
  onError: onActionError,
});
const restartMut = useMutation({
  mutationFn: () =>
    processes.restartProcess({ meta: mutationMeta(), idOrName: idOrName.value }, targetOpts.value),
  onSuccess: invalidateProcess,
  onError: onActionError,
});
const killMut = useMutation({
  mutationFn: () =>
    processes.killProcess({ meta: mutationMeta(), idOrName: idOrName.value }, targetOpts.value),
  onSuccess: invalidateProcess,
  onError: onActionError,
});

async function run(mut: { mutateAsync: () => Promise<unknown> }): Promise<void> {
  actionError.value = "";
  try {
    await mut.mutateAsync();
  } catch {
    // onError already recorded UNAVAILABLE / TIMEOUT
  }
}
</script>

<template>
  <div class="page">
    <div class="head">
      <div>
        <RouterLink class="back" to="/processes">{{ t("processDetail.back") }}</RouterLink>
        <h1>{{ detail?.name || idOrName }}</h1>
      </div>
      <div class="actions">
        <button type="button" class="btn" :disabled="!canStart || acting || !ownerNodeId" @click="run(startMut)">{{ t("processDetail.actions.start") }}</button>
        <button type="button" class="btn" :disabled="!canStop || acting || !ownerNodeId" @click="run(stopMut)">{{ t("processDetail.actions.stop") }}</button>
        <button type="button" class="btn" :disabled="!canRestart || acting || !ownerNodeId" @click="run(restartMut)">{{ t("processDetail.actions.restart") }}</button>
        <button type="button" class="btn btn-danger" :disabled="!canStop || acting || !ownerNodeId" @click="run(killMut)">
          {{ t("processDetail.actions.forceStop") }}
        </button>
      </div>
    </div>

    <p v-if="processQuery.isPending && !detail && ownerNodeId" class="muted">{{ t("processDetail.loading") }}</p>
    <p v-else-if="errorText && !detail" class="error" role="alert">{{ errorText }}</p>
    <template v-else-if="detail">
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <div v-if="detail.showRestartBanner" class="banner restart" role="status">
        {{ detail.restartBanner }}
      </div>
      <div v-if="detail.logPathPending" class="banner" role="status">
        {{ t("processDetail.logPathPending") }}
      </div>
      <div v-if="detail.lastError" class="banner fail" role="alert">
        {{ t("processDetail.process.lastError") }}: {{ detail.lastError }}
      </div>
      <div class="tabs" role="tablist">
        <button
          type="button"
          role="tab"
          :class="{ active: tab === 'overview' }"
          :aria-selected="tab === 'overview'"
          @click="tab = 'overview'"
        >
          {{ t("processDetail.tabs.overview") }}
        </button>
        <button
          type="button"
          role="tab"
          :class="{ active: tab === 'config' }"
          :aria-selected="tab === 'config'"
          @click="tab = 'config'"
        >
          {{ t("processDetail.tabs.config") }}
        </button>
        <button
          type="button"
          role="tab"
          :class="{ active: tab === 'logs' }"
          :aria-selected="tab === 'logs'"
          @click="tab = 'logs'"
        >
          {{ t("processDetail.tabs.logs") }}
        </button>
      </div>
      <ProcessConfigPanel
        v-if="tab === 'config'"
        :id-or-name="idOrName"
        :target-node-id="ownerNodeId"
        :owner-node-hostname="ownerHostname"
      />
      <ProcessLogsPanel
        v-else-if="tab === 'logs'"
        :id-or-name="idOrName"
        :target-node-id="ownerNodeId"
        :instances="detail.instanceRows.map((r) => r.instanceId).filter(Boolean)"
        :redirect-stderr="detail.redirectStderr"
        :log-path-pending="detail.logPathPending"
      />
      <template v-else>
      <section class="card">
        <h2>{{ t("processDetail.process.title") }}</h2>
        <dl class="facts">
          <div>
            <dt>{{ t("processDetail.process.name") }}</dt>
            <dd>{{ detail.name || "—" }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.processId") }}</dt>
            <dd class="mono">{{ detail.processId || "—" }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.group") }}</dt>
            <dd>{{ detail.group || "—" }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.owner") }}</dt>
            <dd>
              <RouterLink
                v-if="ownerNodeId"
                :to="`/nodes/${encodeURIComponent(ownerNodeId)}`"
              >
                {{ ownerHostname || ownerNodeId }}
              </RouterLink>
              <span v-else>{{ detail.owner || "—" }}</span>
            </dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.instances") }}</dt>
            <dd>{{ detail.instances }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.desired") }}</dt>
            <dd>{{ detail.desired ? translateDesiredState(detail.desired) : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.observed") }}</dt>
            <dd>{{ detail.observed ? translateObservedState(detail.observed) : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.health") }}</dt>
            <dd>{{ detail.health ? translateHealthState(detail.health) : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.pid") }}</dt>
            <dd>{{ detail.pid }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.uptime") }}</dt>
            <dd>{{ detail.uptime }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.restartCount") }}</dt>
            <dd>{{ detail.restartCount }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.exitCode") }}</dt>
            <dd>{{ detail.exitCode }}</dd>
          </div>
          <div v-if="detail.lastError">
            <dt>{{ t("processDetail.process.lastError") }}</dt>
            <dd class="error-text">{{ detail.lastError }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.activeRevision") }}</dt>
            <dd>{{ detail.activeRevision }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.latestRevision") }}</dt>
            <dd>{{ detail.latestRevision }}</dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.cpu") }}</dt>
            <dd>
              {{ detail.cpu }}
              <span v-if="detail.cpuNote || metricsNote" class="muted note">{{ detail.cpuNote || metricsNote }}</span>
            </dd>
          </div>
          <div>
            <dt>{{ t("processDetail.process.memory") }}</dt>
            <dd>
              {{ detail.memory }}
              <span v-if="detail.memoryNote || metricsNote" class="muted note">{{ detail.memoryNote || metricsNote }}</span>
            </dd>
          </div>
        </dl>
      </section>

      <section v-if="detail.instanceRows.length" class="card">
        <h2>{{ t("processDetail.instances.title") }}</h2>
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("processDetail.instances.table.instance") }}</th>
              <th>{{ t("processDetail.instances.table.desired") }}</th>
              <th>{{ t("processDetail.instances.table.observed") }}</th>
              <th>{{ t("processDetail.instances.table.health") }}</th>
              <th>{{ t("processDetail.instances.table.pid") }}</th>
              <th>{{ t("processDetail.instances.table.uptime") }}</th>
              <th>{{ t("processDetail.instances.table.restarts") }}</th>
              <th>{{ t("processDetail.instances.table.exit") }}</th>
              <th>{{ t("processDetail.instances.table.lastError") }}</th>
              <th>{{ t("processDetail.instances.table.revision") }}</th>
              <th>{{ t("processDetail.instances.table.cpu") }}</th>
              <th>{{ t("processDetail.instances.table.memory") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="inst in detail.instanceRows" :key="inst.instanceId || String(inst.ordinal)">
              <td class="mono">{{ inst.instanceId || inst.ordinal }}</td>
              <td>{{ inst.desired ? translateDesiredState(inst.desired) : '—' }}</td>
              <td>{{ inst.observed ? translateObservedState(inst.observed) : '—' }}</td>
              <td>{{ inst.health ? translateHealthState(inst.health) : '—' }}</td>
              <td>{{ inst.pid }}</td>
              <td>{{ inst.uptime }}</td>
              <td>{{ inst.restartCount }}</td>
              <td>{{ inst.exitCode }}</td>
              <td class="error-text">{{ inst.lastError || "—" }}</td>
              <td>{{ inst.activeRevision }}</td>
              <td>
                {{ inst.cpu }}
                <span v-if="inst.cpuNote" class="muted note">{{ inst.cpuNote }}</span>
              </td>
              <td>
                {{ inst.memory }}
                <span v-if="inst.memoryNote" class="muted note">{{ inst.memoryNote }}</span>
              </td>
            </tr>
          </tbody>
        </table>
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
            unit="bytes"
            :points="memPoints"
            :step-sec="historyStepSec"
            :stale="historyStale"
          />
        </div>
      </section>
      </template>
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
h1 {
  margin: 0.25rem 0 0;
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
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
.tabs {
  display: flex;
  gap: 0.25rem;
  border-bottom: 1px solid var(--color-border);
}
.tabs button {
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--color-muted);
  padding: 0.5rem 0.85rem;
  font-size: 0.875rem;
  font-weight: 550;
  cursor: pointer;
  margin-bottom: -1px;
}
.tabs button.active {
  color: var(--color-text);
  border-bottom-color: var(--color-accent);
}
.muted {
  color: var(--color-muted);
  font-size: 0.875rem;
}
.note {
  display: block;
  font-weight: 400;
  font-size: 0.75rem;
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
.restart {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.fail {
  background: color-mix(in srgb, var(--color-danger) 16%, transparent);
  color: var(--color-danger);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  white-space: pre-wrap;
  word-break: break-word;
}
.error-text {
  color: var(--color-danger);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  white-space: pre-wrap;
  word-break: break-word;
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  padding: 1.25rem;
  overflow: auto;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
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
.title-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 0.75rem;
}
.title-row h2 {
  margin: 0;
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
.charts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 0.85rem;
}
</style>
