<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { newOperationId } from "../lib/opid";
import { useNodeClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatPercent, mapNode, REMOVE_CONFIRM } from "./clusterView";

const { t } = useI18n();

const POLL_MS = 5000;
const route = useRoute();
const router = useRouter();
const client = useNodeClient();
const queryClient = useQueryClient();
const actionError = ref("");

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
</script>

<template>
  <div class="page">
    <div class="head">
      <div>
        <RouterLink class="back" to="/nodes">{{ t("nodeDetail.back") }}</RouterLink>
        <h1>{{ node?.hostname || id }}</h1>
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
              <div>api {{ node.apiAddress || "—" }}</div>
              <div>rpc {{ node.rpcAddress || "—" }}</div>
              <div>gossip {{ node.gossipAddress || "—" }}</div>
            </dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.version") }}</dt>
            <dd>{{ node.agentVersion || "—" }}</dd>
          </div>
          <div>
            <dt>{{ t("nodeDetail.node.status") }}</dt>
            <dd>{{ node.state || "—" }}</dd>
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
              <td>{{ proc.desired || "—" }}</td>
              <td>{{ proc.observed || "—" }}</td>
              <td>{{ proc.health || "—" }}</td>
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
</style>
