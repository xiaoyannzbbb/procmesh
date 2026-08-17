<script setup lang="ts">
import { computed, ref, useId } from "vue";
import {
  formatChartTime,
  formatChartValue,
  nearestPoint,
  niceYScale,
  splitSegments,
  type ChartKind,
  type ChartPoint,
  type ChartUnit,
} from "../lib/historyChart";
import { useI18n } from "../lib/useI18n";
import FreshnessBadge from "./FreshnessBadge.vue";

const { t } = useI18n();

const props = withDefaults(
  defineProps<{
    title: string;
    points: ChartPoint[];
    stepSec: number;
    stale: boolean;
    unit?: ChartUnit;
    kind?: ChartKind;
  }>(),
  { unit: "percent", kind: "cpu" },
);

const PLOT_W = 600;
const PLOT_H = 140;

const fillId = `chart-fill-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;

const segments = computed(() => (props.stale ? [] : splitSegments(props.points ?? [], props.stepSec)));
const scale = computed(() => niceYScale((props.points ?? []).map((p) => p.v), props.unit));

const tMin = computed(() => {
  const ts = (props.points ?? []).map((p) => p.t);
  return ts.length ? Math.min(...ts) : 0;
});
const tMax = computed(() => {
  const ts = (props.points ?? []).map((p) => p.t);
  return ts.length ? Math.max(...ts) : 1;
});
const tSpan = computed(() => tMax.value - tMin.value || 1);

function xOf(t: number): number {
  return ((t - tMin.value) / tSpan.value) * PLOT_W;
}

function yOf(v: number): number {
  const span = scale.value.max - scale.value.min || 1;
  return (1 - (v - scale.value.min) / span) * PLOT_H;
}

const polylines = computed(() =>
  segments.value.map((seg) => seg.map((p) => `${xOf(p.t).toFixed(2)},${yOf(p.v).toFixed(2)}`).join(" ")),
);

const areas = computed(() =>
  segments.value.map((seg) => {
    if (!seg.length) {
      return "";
    }
    const line = seg.map((p) => `${xOf(p.t).toFixed(2)},${yOf(p.v).toFixed(2)}`).join(" L ");
    const firstX = xOf(seg[0].t).toFixed(2);
    const lastX = xOf(seg[seg.length - 1].t).toFixed(2);
    return `M ${firstX},${PLOT_H.toFixed(2)} L ${line} L ${lastX},${PLOT_H.toFixed(2)} Z`;
  }),
);

const values = computed(() => (props.points ?? []).map((p) => p.v));
const last = computed(() => {
  const pts = props.points ?? [];
  return pts.length ? pts[pts.length - 1] : null;
});
const lastLabel = computed(() => (last.value ? formatChartValue(last.value.v, props.unit) : "—"));
const minLabel = computed(() => (values.value.length ? formatChartValue(Math.min(...values.value), props.unit) : "—"));
const maxLabel = computed(() => (values.value.length ? formatChartValue(Math.max(...values.value), props.unit) : "—"));

const yTicks = computed(() =>
  [...scale.value.ticks]
    .slice()
    .reverse()
    .map((v) => ({ v, label: formatChartValue(v, props.unit) })),
);

const xTicks = computed(() => {
  if (!props.points?.length) {
    return [];
  }
  if (tMax.value === tMin.value) {
    return [{ t: tMin.value, label: formatChartTime(tMin.value, tSpan.value) }];
  }
  const n = 3;
  const ticks = [];
  for (let i = 0; i <= n; i++) {
    const time = tMin.value + (tSpan.value * i) / n;
    ticks.push({ t: time, label: formatChartTime(time, tSpan.value) });
  }
  return ticks;
});

const ariaLabel = computed(() =>
  t("metricsHistory.summary", {
    title: props.title,
    last: lastLabel.value,
    min: minLabel.value,
    max: maxLabel.value,
  }),
);

const hover = ref<ChartPoint | null>(null);

function onPointerMove(ev: PointerEvent) {
  const el = ev.currentTarget as SVGSVGElement;
  const rect = el.getBoundingClientRect();
  const width = rect.width || 1;
  const x = ((ev.clientX - rect.left) / width) * PLOT_W;
  hover.value = nearestPoint(props.points ?? [], tMin.value, tMax.value, x, PLOT_W);
}

function onPointerLeave() {
  hover.value = null;
}

function onKeydown(ev: KeyboardEvent) {
  const pts = props.points ?? [];
  if (!pts.length) {
    return;
  }
  if (ev.key === "Escape") {
    hover.value = null;
    return;
  }
  if (ev.key !== "ArrowLeft" && ev.key !== "ArrowRight") {
    return;
  }
  ev.preventDefault();
  const cur = hover.value;
  let idx = cur ? pts.findIndex((p) => p.t === cur.t && p.v === cur.v) : pts.length - 1;
  if (idx < 0) {
    idx = pts.length - 1;
  }
  idx = ev.key === "ArrowLeft" ? Math.max(0, idx - 1) : Math.min(pts.length - 1, idx + 1);
  hover.value = pts[idx];
}

const hoverPos = computed(() => {
  if (!hover.value) {
    return null;
  }
  return { x: xOf(hover.value.t), y: yOf(hover.value.v) };
});
</script>

<template>
  <section class="history-chart" :data-kind="kind">
    <div class="chart-head">
      <div>
        <h3>{{ title }}</h3>
        <p v-if="!stale && last" class="chart-value">{{ lastLabel }}</p>
        <p v-if="!stale && points.length" class="chart-range">{{ minLabel }} – {{ maxLabel }}</p>
      </div>
      <FreshnessBadge v-if="stale" status="STALE" />
    </div>
    <p v-if="stale" class="stale-msg">{{ t("metricsHistory.stale") }}</p>
    <p v-else-if="!points.length" class="empty">{{ t("metricsHistory.empty") }}</p>
    <div v-else class="plot">
      <div class="y-ticks" aria-hidden="true">
        <span v-for="tick in yTicks" :key="tick.v">{{ tick.label }}</span>
      </div>
      <div class="plot-main">
        <svg
          :viewBox="`0 0 ${PLOT_W} ${PLOT_H}`"
          preserveAspectRatio="none"
          role="img"
          :aria-label="ariaLabel"
          tabindex="0"
          @pointermove="onPointerMove"
          @pointerleave="onPointerLeave"
          @keydown="onKeydown"
        >
          <defs>
            <linearGradient :id="fillId" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stop-color="currentColor" stop-opacity="0.28" />
              <stop offset="100%" stop-color="currentColor" stop-opacity="0.03" />
            </linearGradient>
          </defs>
          <line
            v-for="tick in scale.ticks"
            :key="`g-${tick}`"
            class="grid"
            x1="0"
            :x2="PLOT_W"
            :y1="yOf(tick)"
            :y2="yOf(tick)"
          />
          <path
            v-for="(d, idx) in areas"
            :key="`a-${idx}`"
            class="area"
            :d="d"
            :fill="`url(#${fillId})`"
          />
          <polyline
            v-for="(pts, idx) in polylines"
            :key="`l-${idx}`"
            :points="pts"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linejoin="round"
            stroke-linecap="round"
            vector-effect="non-scaling-stroke"
          />
          <template v-if="hover && hoverPos">
            <line
              class="crosshair"
              :x1="hoverPos.x"
              :x2="hoverPos.x"
              y1="0"
              :y2="PLOT_H"
              vector-effect="non-scaling-stroke"
            />
            <circle :cx="hoverPos.x" :cy="hoverPos.y" r="4" fill="currentColor" />
          </template>
        </svg>
        <div
          v-if="hover && hoverPos"
          class="tooltip"
          data-testid="chart-tooltip"
          role="status"
          :style="{ left: `${(hoverPos.x / PLOT_W) * 100}%` }"
        >
          <span class="tooltip-time">{{ formatChartTime(hover.t, tSpan) }}</span>
          <span class="tooltip-value">{{ formatChartValue(hover.v, unit) }}</span>
        </div>
      </div>
      <div class="x-ticks" aria-hidden="true">
        <span v-for="tick in xTicks" :key="tick.t">{{ tick.label }}</span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.history-chart {
  --chart-line: #2563eb;
  display: flex;
  flex-direction: column;
  gap: 0.7rem;
  min-width: 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--color-card) 92%, var(--color-bg));
  padding: 0.9rem 1rem 0.8rem;
}
.history-chart[data-kind="cpu"] {
  --chart-line: #2563eb;
}
.history-chart[data-kind="memory"] {
  --chart-line: #7c3aed;
}
.history-chart[data-kind="disk"] {
  --chart-line: #d97706;
}
.chart-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.5rem;
}
h3 {
  margin: 0;
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--color-muted);
  letter-spacing: 0.01em;
}
.chart-value {
  margin: 0.2rem 0 0;
  font-size: 1.45rem;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.02em;
  line-height: 1.15;
}
.chart-range {
  margin: 0.2rem 0 0;
  color: var(--color-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}
.plot {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  grid-template-rows: 1fr auto;
  column-gap: 0.45rem;
  row-gap: 0.3rem;
}
.y-ticks {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  align-items: flex-end;
  padding: 0.05rem 0;
  color: var(--color-muted);
  font-size: 0.65rem;
  font-variant-numeric: tabular-nums;
  line-height: 1;
  white-space: nowrap;
}
.plot-main {
  position: relative;
  min-width: 0;
}
svg {
  width: 100%;
  height: 128px;
  display: block;
  color: var(--chart-line);
  cursor: crosshair;
  outline-offset: 2px;
}
.grid {
  stroke: var(--color-border);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}
.crosshair {
  stroke: color-mix(in srgb, var(--color-text) 35%, transparent);
  stroke-width: 1;
  stroke-dasharray: 3 3;
}
.x-ticks {
  grid-column: 2;
  display: flex;
  justify-content: space-between;
  color: var(--color-muted);
  font-size: 0.65rem;
  font-variant-numeric: tabular-nums;
}
.tooltip {
  position: absolute;
  top: 0.35rem;
  z-index: 1;
  transform: translateX(-50%);
  display: flex;
  gap: 0.45rem;
  align-items: baseline;
  border: 1px solid var(--color-border);
  border-radius: 6px;
  background: var(--color-card);
  color: var(--color-text);
  box-shadow: var(--shadow-sm);
  padding: 0.25rem 0.5rem;
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  pointer-events: none;
}
.tooltip-time {
  color: var(--color-muted);
}
.tooltip-value {
  font-weight: 650;
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

@media (prefers-reduced-motion: reduce) {
  .tooltip {
    transition: none;
  }
}
</style>
