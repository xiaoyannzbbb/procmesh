<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { computed } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { useNodeClient } from "../lib/rpc";
import { formatResources, mapNode } from "./clusterView";

const POLL_MS = 5000;
const client = useNodeClient();

const query = useQuery({
  queryKey: ["nodes"],
  queryFn: () => client.listNodes({}),
  refetchInterval: POLL_MS,
});

const nodes = computed(() => {
  const list = query.data.value?.nodes ?? [];
  const now = Date.now();
  return list.map((n) => mapNode(n, now));
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
    <h1>Nodes</h1>
    <p v-if="query.isPending && !query.data" class="muted">Loading…</p>
    <p v-else-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <div v-else class="card">
      <table class="table">
        <thead>
          <tr>
            <th>Hostname</th>
            <th>Node ID</th>
            <th>State</th>
            <th>Version</th>
            <th>Resources</th>
            <th>Processes</th>
            <th>Freshness</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in nodes" :key="node.nodeId">
            <td>
              <RouterLink :to="`/nodes/${encodeURIComponent(node.nodeId)}`">{{ node.hostname || node.nodeId }}</RouterLink>
            </td>
            <td class="mono">{{ node.nodeId }}</td>
            <td>{{ node.state }}</td>
            <td>{{ node.agentVersion || "—" }}</td>
            <td>{{ formatResources(node.resources) }}</td>
            <td>
              <ul v-if="node.processes.length" class="proc-list">
                <li v-for="proc in node.processes" :key="proc.name">
                  <span>{{ proc.name }} {{ proc.observed }}</span>
                  <FreshnessBadge :status="proc.freshness" />
                </li>
              </ul>
              <span v-else class="muted">—</span>
            </td>
            <td>
              <FreshnessBadge :status="node.freshness" />
            </td>
            <td>{{ node.lastUpdated }}</td>
          </tr>
          <tr v-if="!nodes.length">
            <td colspan="8" class="muted">No nodes</td>
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
  border-radius: var(--radius-lg);
  background: var(--color-card);
  overflow: auto;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
a {
  color: var(--color-accent);
  text-decoration: none;
}
a:hover {
  text-decoration: underline;
}
.proc-list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
.proc-list li {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  white-space: nowrap;
}
</style>
