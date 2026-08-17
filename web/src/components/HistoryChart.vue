<script setup lang="ts">
import { computed } from "vue";
import { splitSegments, type ChartPoint } from "../lib/historyChart";
import { useI18n } from "../lib/useI18n";
import FreshnessBadge from "./FreshnessBadge.vue";

const { t } = useI18n();

const props = defineProps<{
  title: string;
  points: ChartPoint[];
  stepSec: number;
  stale: boolean;
}>();

const VIEW_W = 600;
const VIEW_H = 160;
const PAD_X = 8;
const PAD_Y = 12;

const segments = computed(() => (props.stale ? [] : splitSegments(props.points ?? [], props.stepSec)));

const polylines = computed(() => {
  const all = props.points ?? [];
  if (!all.length) {
    return [];
  }
  const ts = all.map((p) => p.t);
  const vs = all.map((p) => p.v);
  const tMin = Math.min(...ts);
  const tMax = Math.max(...ts);
  const vMin = Math.min(...vs);
  const vMax = Math.max(...vs);
  const tSpan = tMax - tMin || 1;
  const vSpan = vMax - vMin || 1;
  const w = VIEW_W - PAD_X * 2;
  const h = VIEW_H - PAD_Y * 2;
  return segments.value.map((seg) =>
    seg
      .map((p) => {
        const x = PAD_X + ((p.t - tMin) / tSpan) * w;
        const y = PAD_Y + (1 - (p.v - vMin) / vSpan) * h;
        return `${x.toFixed(2)},${y.toFixed(2)}`;
      })
      .join(" "),
  );
});
</script>

<template>
  <section class="history-chart">
    <div class="chart-head">
      <h3>{{ title }}</h3>
      <FreshnessBadge v-if="stale" status="STALE" />
    </div>
    <p v-if="stale" class="stale-msg">{{ t("metricsHistory.stale") }}</p>
    <p v-else-if="!points.length" class="empty">{{ t("metricsHistory.empty") }}</p>
    <svg
      v-else
      viewBox="0 0 600 160"
      preserveAspectRatio="none"
      role="img"
      :aria-label="title"
    >
      <polyline
        v-for="(pts, idx) in polylines"
        :key="idx"
        :points="pts"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linejoin="round"
        stroke-linecap="round"
      />
    </svg>
  </section>
</template>

<style scoped>
.history-chart {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.chart-head {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
h3 {
  margin: 0;
  font-size: 0.85rem;
  font-weight: 650;
}
svg {
  width: 100%;
  height: auto;
  display: block;
  color: var(--color-text);
}
.empty {
  margin: 0;
  color: var(--color-muted);
  font-size: 0.875rem;
}
.stale-msg {
  margin: 0;
  color: var(--color-stale-fg);
  background: var(--color-stale);
  border-radius: var(--radius-sm);
  padding: 0.5rem 0.75rem;
  font-size: 0.875rem;
}
</style>
