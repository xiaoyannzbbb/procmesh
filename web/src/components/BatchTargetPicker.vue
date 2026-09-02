<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { Search, X } from "lucide-vue-next";
import { computed, ref } from "vue";
import { useGroupClient } from "../lib/rpc/access";
import { useNodeClient } from "../lib/rpc/cluster";
import { useProcessClient } from "../lib/rpc/process";
import { useI18n } from "../lib/useI18n";
import { mapNode } from "../pages/clusterView";
import {
  flattenClusterProcesses,
  formatRemoteError,
  mergeProcessRows,
  rowsFromProcessViews,
  type ClusterProcessRow,
} from "../pages/processView";

type SelectorKind = "processIds" | "processGroup" | "agentGroupId";

const props = defineProps<{
  kind: SelectorKind;
  processIds: string[];
  processGroup: string;
  agentGroupId: string;
  active?: boolean;
  disabled?: boolean;
}>();

const emit = defineEmits<{
  "update:processIds": [value: string[]];
  "update:processGroup": [value: string];
  "update:agentGroupId": [value: string];
}>();

const POLL_MS = 5000;
const { t } = useI18n();
const processes = useProcessClient();
const nodes = useNodeClient();
const groups = useGroupClient();
const search = ref("");

const fetchEnabled = computed(() => Boolean(props.active));

const nodesQuery = useQuery({
  queryKey: ["nodes"],
  queryFn: () => nodes.listNodes({}),
  enabled: fetchEnabled,
  refetchInterval: POLL_MS,
});

const processesQuery = useQuery({
  queryKey: ["processes"],
  queryFn: () => processes.listProcesses({}),
  enabled: fetchEnabled,
  refetchInterval: POLL_MS,
});

const groupsQuery = useQuery({
  queryKey: ["groups"],
  queryFn: () => groups.listAgentGroups({}),
  enabled: fetchEnabled,
  refetchInterval: POLL_MS,
});

const processRows = computed(() => {
  const now = Date.now();
  const nodeList = nodesQuery.data.value?.nodes ?? [];
  const gossip = flattenClusterProcesses(nodeList, now);
  const listed = rowsFromProcessViews(processesQuery.data.value?.processes ?? [], now);
  const hosts = new Map(
    nodeList.map((raw) => {
      const node = mapNode(raw, now);
      return [node.nodeId, node.hostname] as const;
    }),
  );
  return mergeProcessRows(gossip, listed)
    .map((row) => ({
      ...row,
      ownerHostname: row.ownerHostname || hosts.get(row.ownerNodeId) || "",
    }))
    .filter((row) => row.processId.length > 0)
    .sort((a, b) => (a.name || a.processId).localeCompare(b.name || b.processId));
});

const processGroups = computed(() => {
  const counts = new Map<string, number>();
  for (const row of processRows.value) {
    if (!row.group) {
      continue;
    }
    counts.set(row.group, (counts.get(row.group) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => a.name.localeCompare(b.name));
});

const agentGroups = computed(() => {
  return [...(groupsQuery.data.value?.groups ?? [])].sort((a, b) =>
    (a.name || a.groupId).localeCompare(b.name || b.groupId),
  );
});

const term = computed(() => search.value.trim().toLowerCase());

const filteredProcesses = computed(() => {
  if (!term.value) {
    return processRows.value;
  }
  return processRows.value.filter((row) => {
    const haystack = [row.name, row.processId, row.group, row.ownerHostname, row.ownerNodeId]
      .join(" ")
      .toLowerCase();
    return haystack.includes(term.value);
  });
});

const filteredProcessGroups = computed(() => {
  if (!term.value) {
    return processGroups.value;
  }
  return processGroups.value.filter((group) => group.name.toLowerCase().includes(term.value));
});

const filteredAgentGroups = computed(() => {
  if (!term.value) {
    return agentGroups.value;
  }
  return agentGroups.value.filter((group) =>
    [group.name, group.groupId, group.description].join(" ").toLowerCase().includes(term.value),
  );
});

const selectedIdSet = computed(() => new Set(props.processIds));

const selectedProcessRows = computed(() => {
  const selected = selectedIdSet.value;
  return processRows.value.filter((row) => selected.has(row.processId));
});

const visibleAllSelected = computed(() => {
  const visible = filteredProcesses.value;
  return visible.length > 0 && visible.every((row) => selectedIdSet.value.has(row.processId));
});

const processesPending = computed(
  () =>
    (processesQuery.isPending.value && !processesQuery.data.value) ||
    (nodesQuery.isPending.value && !nodesQuery.data.value),
);

const processesError = computed(() => {
  const err = processesQuery.error.value ?? nodesQuery.error.value;
  return err ? formatRemoteError(err) : "";
});

const groupsPending = computed(() => groupsQuery.isPending.value && !groupsQuery.data.value);
const groupsError = computed(() => {
  const err = groupsQuery.error.value;
  return err ? formatRemoteError(err) : "";
});

const searchPlaceholder = computed(() => {
  if (props.kind === "processGroup") {
    return t("batch.searchProcessGroups");
  }
  if (props.kind === "agentGroupId") {
    return t("batch.searchAgentGroups");
  }
  return t("batch.searchProcesses");
});

const helper = computed(() => {
  if (props.kind === "processGroup") {
    return t("batch.selectorProcessGroupHint");
  }
  if (props.kind === "agentGroupId") {
    return t("batch.selectorAgentGroupHint");
  }
  return t("batch.selectorProcessIdsHint");
});

function nodeLabel(row: ClusterProcessRow): string {
  return row.ownerHostname || row.ownerNodeId || "—";
}

function processMeta(row: ClusterProcessRow): string {
  if (row.group) {
    return t("batch.processMetaWithGroup", {
      group: row.group,
      node: nodeLabel(row),
      status: row.observed || row.desired || "—",
    });
  }
  return t("batch.processMeta", {
    node: nodeLabel(row),
    status: row.observed || row.desired || "—",
  });
}

function toggleProcess(processId: string, checked: boolean): void {
  const next = new Set(props.processIds);
  if (checked) {
    next.add(processId);
  } else {
    next.delete(processId);
  }
  emit("update:processIds", [...next]);
}

function selectAllVisible(): void {
  const next = new Set(props.processIds);
  for (const row of filteredProcesses.value) {
    next.add(row.processId);
  }
  emit("update:processIds", [...next]);
}

function clearProcessSelection(): void {
  emit("update:processIds", []);
}

function onProcessChange(processId: string, event: Event): void {
  const target = event.target as HTMLInputElement;
  toggleProcess(processId, target.checked);
}

function selectProcessGroup(name: string): void {
  emit("update:processGroup", name);
}

function selectAgentGroup(groupId: string): void {
  emit("update:agentGroupId", groupId);
}
</script>

<template>
  <div class="target-picker">
    <p class="field-hint">{{ helper }}</p>
    <label class="search-field">
      <span class="sr-only">{{ searchPlaceholder }}</span>
      <span class="search-input-wrap">
        <Search :size="16" aria-hidden="true" />
        <input
          v-model="search"
          class="input search-input"
          name="target_search"
          type="search"
          :placeholder="searchPlaceholder"
          :disabled="disabled"
          autocomplete="off"
        />
      </span>
    </label>

    <template v-if="kind === 'processIds'">
      <p v-if="processesPending" class="picker-message">{{ t("batch.loading") }}</p>
      <p v-else-if="processesError" class="picker-message error" role="alert">
        {{ t("batch.loadProcessesError", { error: processesError }) }}
      </p>
      <template v-else>
        <div class="picker-toolbar">
          <span>{{ t("batch.selectedCount", { count: processIds.length }) }}</span>
          <div class="picker-toolbar-actions">
            <button
              type="button"
              class="link-btn"
              :disabled="disabled || !filteredProcesses.length || visibleAllSelected"
              @click="selectAllVisible"
            >
              {{ t("batch.selectAllVisible") }}
            </button>
            <button
              type="button"
              class="link-btn"
              :disabled="disabled || !processIds.length"
              @click="clearProcessSelection"
            >
              {{ t("batch.clearSelection") }}
            </button>
          </div>
        </div>
        <div v-if="selectedProcessRows.length" class="chip-row">
          <span v-for="row in selectedProcessRows" :key="row.processId" class="chip">
            {{ row.name || row.processId }}
            <button
              type="button"
              class="chip-remove"
              :disabled="disabled"
              :aria-label="t('batch.clearSelection')"
              @click="toggleProcess(row.processId, false)"
            >
              <X :size="14" aria-hidden="true" />
            </button>
          </span>
        </div>
        <fieldset class="option-list" :aria-label="t('batch.selectorProcessIds')">
          <label
            v-for="row in filteredProcesses"
            :key="row.processId"
            class="option-row"
            :class="{ selected: selectedIdSet.has(row.processId) }"
          >
            <input
              type="checkbox"
              name="processId"
              :value="row.processId"
              :checked="selectedIdSet.has(row.processId)"
              :disabled="disabled"
              :data-process-id="row.processId"
              @change="onProcessChange(row.processId, $event)"
            />
            <span class="option-copy">
              <strong>{{ row.name || row.processId }}</strong>
              <span>{{ processMeta(row) }}</span>
            </span>
          </label>
          <p v-if="!processRows.length" class="picker-message">{{ t("batch.noProcesses") }}</p>
          <p v-else-if="!filteredProcesses.length" class="picker-message">{{ t("batch.noProcessMatch") }}</p>
        </fieldset>
      </template>
    </template>

    <template v-else-if="kind === 'processGroup'">
      <p v-if="processesPending" class="picker-message">{{ t("batch.loading") }}</p>
      <p v-else-if="processesError" class="picker-message error" role="alert">
        {{ t("batch.loadProcessesError", { error: processesError }) }}
      </p>
      <fieldset v-else class="option-list" :aria-label="t('batch.selectorProcessGroup')">
        <label
          v-for="group in filteredProcessGroups"
          :key="group.name"
          class="option-row"
          :class="{ selected: processGroup === group.name }"
        >
          <input
            type="radio"
            name="processGroup"
            :value="group.name"
            :checked="processGroup === group.name"
            :disabled="disabled"
            @change="selectProcessGroup(group.name)"
          />
          <span class="option-copy">
            <strong>{{ group.name }}</strong>
            <span>{{ t("batch.groupProcessCount", { count: group.count }) }}</span>
          </span>
        </label>
        <p v-if="!processGroups.length" class="picker-message">{{ t("batch.noProcessGroups") }}</p>
        <p v-else-if="!filteredProcessGroups.length" class="picker-message">{{ t("batch.noProcessGroupMatch") }}</p>
      </fieldset>
    </template>

    <template v-else>
      <p v-if="groupsPending" class="picker-message">{{ t("batch.loading") }}</p>
      <p v-else-if="groupsError" class="picker-message error" role="alert">
        {{ t("batch.loadGroupsError", { error: groupsError }) }}
      </p>
      <fieldset v-else class="option-list" :aria-label="t('batch.selectorAgentGroup')">
        <label
          v-for="group in filteredAgentGroups"
          :key="group.groupId"
          class="option-row"
          :class="{ selected: agentGroupId === group.groupId }"
        >
          <input
            type="radio"
            name="agentGroupId"
            :value="group.groupId"
            :checked="agentGroupId === group.groupId"
            :disabled="disabled"
            @change="selectAgentGroup(group.groupId)"
          />
          <span class="option-copy">
            <strong>{{ group.name || group.groupId }}</strong>
            <span>
              {{ t("batch.agentGroupMembers", { count: group.memberNodeIds?.length ?? 0 }) }}
              <template v-if="group.description && group.description !== group.name"> · {{ group.description }}</template>
            </span>
          </span>
        </label>
        <p v-if="!agentGroups.length" class="picker-message">{{ t("batch.noAgentGroups") }}</p>
        <p v-else-if="!filteredAgentGroups.length" class="picker-message">{{ t("batch.noAgentGroupMatch") }}</p>
      </fieldset>
    </template>
  </div>
</template>

<style scoped>
.target-picker,
.search-field,
.option-copy {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.field-hint {
  margin: 0;
  font-size: 0.75rem;
  line-height: 1.45;
  color: var(--color-muted);
}
.search-input-wrap {
  position: relative;
  display: flex;
  align-items: center;
}
.search-input-wrap > svg {
  position: absolute;
  left: 0.75rem;
  color: var(--color-muted);
  pointer-events: none;
}
.search-input {
  width: 100%;
  padding-left: 2.25rem;
}
.picker-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  color: var(--color-muted);
  font-size: 0.75rem;
}
.picker-toolbar-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
.link-btn {
  border: none;
  padding: 0.25rem 0;
  background: transparent;
  color: var(--color-accent);
  font-size: 0.75rem;
  font-weight: 600;
  cursor: pointer;
}
.link-btn:hover:not(:disabled) {
  text-decoration: underline;
}
.link-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  max-width: 100%;
  padding: 0.25rem 0.375rem 0.25rem 0.5rem;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-accent) 8%, var(--color-card));
  color: var(--color-text);
  font-size: 0.75rem;
  overflow-wrap: anywhere;
}
.chip-remove {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.5rem;
  height: 1.5rem;
  padding: 0;
  border: none;
  border-radius: 999px;
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
}
.chip-remove:hover:not(:disabled) {
  background: var(--color-card);
  color: var(--color-text);
}
.option-list {
  max-height: 16rem;
  margin: 0;
  padding: 0.25rem;
  overflow-y: auto;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  min-width: 0;
}
.option-row {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  min-height: 44px;
  padding: 0.625rem 0.75rem;
  border-radius: 8px;
  cursor: pointer;
}
.option-row:hover,
.option-row.selected {
  background: color-mix(in srgb, var(--color-text) 4%, var(--color-card));
}
.option-row.selected {
  background: color-mix(in srgb, var(--color-accent) 10%, var(--color-card));
}
.option-row input {
  margin-top: 0.2rem;
}
.option-copy {
  min-width: 0;
  gap: 0.125rem;
  overflow-wrap: anywhere;
}
.option-copy strong {
  color: var(--color-text);
  font-size: 0.875rem;
}
.option-copy span,
.picker-message {
  color: var(--color-muted);
  font-size: 0.75rem;
  line-height: 1.45;
}
.picker-message {
  margin: 0;
  padding: 0.75rem;
}
.error {
  color: var(--color-danger);
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
@media (max-width: 640px) {
  .picker-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
