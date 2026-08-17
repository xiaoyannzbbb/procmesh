<script setup lang="ts">
import { computed } from "vue";
import { useQuery } from "@tanstack/vue-query";
import { useNodeClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";
import { mapNode } from "../pages/clusterView";

const { t } = useI18n();

const props = defineProps<{
  modelValue?: string;
  excludeNodeIds?: string[];
  placeholder?: string;
  disabled?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: string];
}>();

const POLL_MS = 5000;
const client = useNodeClient();

const query = useQuery({
  queryKey: ["nodes"],
  queryFn: () => client.listNodes({}),
  refetchInterval: POLL_MS,
});

const nodes = computed(() => {
  const response = query.data.value;
  const list = response?.nodes ?? [];
  const now = Date.now();
  const excludeSet = new Set(props.excludeNodeIds ?? []);

  return list
    .map((n) => mapNode(n, now))
    .filter((n) => {
      // Only show admitted members: exclude JOINING, LEFT, REMOVED, REVOKED, FAILED
      // Only ALIVE and SUSPECT are considered admitted members in the cluster
      const isAdmitted = n.state === "ALIVE" || n.state === "SUSPECT";
      const isNotExcluded = !excludeSet.has(n.nodeId);
      return isAdmitted && isNotExcluded;
    });
});

const selectedNode = computed(() => {
  return nodes.value.find((n) => n.nodeId === props.modelValue);
});

function onSelect(event: Event): void {
  const target = event.target as HTMLSelectElement;
  emit("update:modelValue", target.value);
}
</script>

<template>
  <div class="node-selector">
    <select
      :value="modelValue || ''"
      class="select"
      :disabled="disabled || query.isPending.value"
      @change="onSelect"
    >
      <option value="">{{ placeholder || t("group.selectNode") }}</option>
      <option v-for="node in nodes" :key="node.nodeId" :value="node.nodeId">
        {{ node.hostname || node.nodeId }} ({{ node.state }})
      </option>
    </select>
    <p v-if="query.isPending.value" class="hint">{{ t("nodes.loading") }}</p>
    <p v-else-if="!nodes.length" class="hint warning">{{ t("group.noAdmittedNodes") }}</p>
  </div>
</template>

<style scoped>
.node-selector {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
}

.select {
  width: 100%;
  padding: 0.5rem 0.75rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-input);
  color: var(--color-text);
  font-size: 0.875rem;
  transition: all 150ms;
}

.select:hover:not(:disabled) {
  border-color: var(--color-accent);
}

.select:focus {
  outline: none;
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px rgba(56, 189, 248, 0.1);
}

.select:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.hint {
  margin: 0;
  font-size: 0.75rem;
  color: var(--color-muted);
}

.warning {
  color: var(--color-warning, #f59e0b);
}
</style>
