<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { computed } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { STALE } from "../lib/freshness";
import { useNodeClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";
import {
  formatResources,
  mapNode,
  RAFT_LEADER,
  RAFT_NON_VOTER,
  RAFT_NOT_MEMBER,
  RAFT_VOTER,
  type RaftRole,
} from "./clusterView";

const { t } = useI18n();

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

function raftRoleLabel(role: RaftRole): string {
  switch (role) {
    case RAFT_LEADER:
      return t("nodes.raftRole.leader");
    case RAFT_VOTER:
      return t("nodes.raftRole.voter");
    case RAFT_NON_VOTER:
      return t("nodes.raftRole.nonVoter");
    case RAFT_NOT_MEMBER:
      return t("nodes.raftRole.notMember");
    default:
      return t("nodes.raftRole.unknown");
  }
}

function raftRoleClass(role: RaftRole): string {
  return `raft-role-${role.toLowerCase().replace("_", "-")}`;
}
</script>

<template>
  <div class="page">
    <h1>{{ t("nodes.title") }}</h1>
    <p v-if="query.isPending && !query.data" class="muted">{{ t("nodes.loading") }}</p>
    <p v-else-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <div v-else class="card">
      <table class="table">
        <thead>
          <tr>
            <th>{{ t("nodes.table.hostname") }}</th>
            <th>{{ t("nodes.table.state") }}</th>
            <th>{{ t("nodes.table.raftRole") }}</th>
            <th>{{ t("nodes.table.version") }}</th>
            <th>{{ t("nodes.table.resources") }}</th>
            <th>{{ t("nodes.table.processes") }}</th>
            <th>{{ t("nodes.table.freshness") }}</th>
            <th>{{ t("nodes.table.updated") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in nodes" :key="node.nodeId">
            <td>
              <div class="node-identity">
                <RouterLink :to="`/nodes/${encodeURIComponent(node.nodeId)}`">{{ node.hostname || node.nodeId }}</RouterLink>
                <div v-if="node.hostname && node.hostname !== node.nodeId" class="mono muted node-id">{{ node.nodeId }}</div>
              </div>
            </td>
            <td>{{ node.state }}</td>
            <td class="raft-role-cell">
              <div class="raft-role-content">
                <span
                  :class="['raft-role-badge', raftRoleClass(node.raftRole)]"
                  :aria-label="t('nodes.raftRole.badgeLabel', { role: raftRoleLabel(node.raftRole) })"
                >
                  {{ raftRoleLabel(node.raftRole) }}
                </span>
                <FreshnessBadge v-if="node.raftRoleFreshness === STALE" :status="node.raftRoleFreshness" />
              </div>
            </td>
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
            <td colspan="8" class="muted">{{ t("nodes.noNodes") }}</td>
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
.node-identity {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
  min-width: 0;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
.node-id {
  overflow-wrap: anywhere;
  font-size: 0.75rem;
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
.raft-role-cell {
  white-space: nowrap;
}
.raft-role-content {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  min-width: 7rem;
}
.raft-role-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 3px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0;
  white-space: nowrap;
}
.raft-role-leader {
  background: var(--color-live);
  color: var(--color-live-fg);
}
.raft-role-voter {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}
.raft-role-non-voter {
  background: color-mix(in srgb, var(--color-accent) 12%, var(--color-card));
  color: var(--color-text);
}
.raft-role-not-member {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.raft-role-unknown {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}
</style>
