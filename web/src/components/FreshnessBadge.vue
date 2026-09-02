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
</script>

<template>
  <span :class="['freshness-badge', badgeClass]">{{ displayText }}</span>
</template>
