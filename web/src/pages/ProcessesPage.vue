<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { useNodeClient, useProcessClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";
import { useProcessState } from "../lib/useProcessState";
import {
  flattenClusterProcesses,
  formatRemoteError,
  mergeProcessRows,
  ownerDisplay,
  rowKey,
  rowsFromProcessViews,
} from "./processView";

const { t } = useI18n();
const { translateDesiredState, translateObservedState, translateHealthState } = useProcessState();

const POLL_MS = 5000;
const nodes = useNodeClient();
const processes = useProcessClient();
const groupFilter = ref("");

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

const rows = computed(() => {
  const gossip = flattenClusterProcesses(nodesQuery.data.value?.nodes ?? [], Date.now());
  const listed = rowsFromProcessViews(processesQuery.data.value?.processes ?? [], Date.now());
  const all = mergeProcessRows(gossip, listed);
  const filter = groupFilter.value.trim();
  if (!filter) {
    return all;
  }
  return all.filter((row) => row.group === filter);
});

const loading = computed(
  () =>
    (nodesQuery.isPending.value && !nodesQuery.data.value) &&
    (processesQuery.isPending.value && !processesQuery.data.value),
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
</script>

<template>
  <div class="page">
    <h1>{{ t("processes.title") }}</h1>
    <label class="field">
      {{ t("processes.filterGroup") }}
      <input
        v-model="groupFilter"
        class="input"
        name="group"
        type="text"
        :placeholder="t('processes.filterGroupPlaceholder')"
      />
    </label>
    <p v-if="loading" class="muted">{{ t("processes.loading") }}</p>
    <p v-else-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <div v-else class="card">
      <table class="table">
        <thead>
          <tr>
            <th>{{ t("processes.table.name") }}</th>
            <th>{{ t("processes.table.owner") }}</th>
            <th>{{ t("processes.table.desired") }}</th>
            <th>{{ t("processes.table.observed") }}</th>
            <th>{{ t("processes.table.health") }}</th>
            <th>{{ t("processes.table.revisions") }}</th>
            <th>{{ t("processes.table.freshness") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="rowKey(row)">
            <td>
              <RouterLink
                :to="{
                  path: `/processes/${encodeURIComponent(row.name)}`,
                  query: { node: row.ownerNodeId },
                }"
              >
                {{ row.name }}
              </RouterLink>
            </td>
            <td>
              <div>{{ ownerDisplay(row.ownerHostname, row.ownerNodeId) }}</div>
            </td>
            <td>{{ row.desired ? translateDesiredState(row.desired) : '—' }}</td>
            <td>{{ row.observed ? translateObservedState(row.observed) : '—' }}</td>
            <td>{{ row.health ? translateHealthState(row.health) : '—' }}</td>
            <td>{{ row.activeRevision }} / {{ row.latestRevision }}</td>
            <td>
              <FreshnessBadge :status="row.freshness" />
            </td>
          </tr>
          <tr v-if="!rows.length">
            <td colspan="7" class="muted">{{ t("processes.noProcesses") }}</td>
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
.error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  overflow: auto;
}
a {
  color: var(--color-accent);
  text-decoration: none;
}
a:hover {
  text-decoration: underline;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  font-size: 0.875rem;
  color: var(--color-muted);
  max-width: 20rem;
}
</style>
