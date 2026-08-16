<script setup lang="ts">
import { computed } from "vue";
import { classify, LIVE, STALE, UNKNOWN, type Freshness } from "../lib/freshness";
import { useI18n } from "../lib/useI18n";

const { t } = useI18n();

const props = defineProps<{
  nowMs?: number;
  lastUpdatedUnixMs?: number;
  nodeState?: string;
  status?: Freshness;
}>();

const value = computed<Freshness>(() => {
  if (props.status === LIVE || props.status === STALE || props.status === UNKNOWN) {
    return props.status;
  }
  return classify(props.nowMs ?? 0, props.lastUpdatedUnixMs ?? 0, props.nodeState ?? "");
});

const displayText = computed(() => {
  switch (value.value) {
    case LIVE:
      return t("status.live");
    case STALE:
      return t("status.stale");
    case UNKNOWN:
      return t("status.unknown");
    default:
      return value.value;
  }
});

const badgeClass = computed(() => "freshness-" + value.value.toLowerCase());

const badgeStyle = computed(() => {
  switch (value.value) {
    case STALE:
      return { backgroundColor: "#FEF3C7", color: "#92400E" };
    case LIVE:
      return { backgroundColor: "#D1FAE5", color: "#065F46" };
    default:
      return { backgroundColor: "#E5E7EB", color: "#374151" };
  }
});
</script>

<template>
  <span :class="['freshness-badge', badgeClass]" :style="badgeStyle">{{ displayText }}</span>
</template>

<style>
.freshness-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 3px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0.02em;
}
.freshness-live {
  background-color: #d1fae5;
  color: #065f46;
}
.freshness-stale {
  background-color: #fef3c7;
  color: #92400e;
}
.freshness-unknown {
  background-color: #e5e7eb;
  color: #374151;
}
</style>
