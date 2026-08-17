export type ChartPoint = { t: number; v: number };

export type HistoryRange = "24h" | "7d";

export type ChartUnit = "percent" | "bytes";

export type ChartKind = "cpu" | "memory" | "disk";

export type YScale = { min: number; max: number; ticks: number[] };

export function splitSegments(points: ChartPoint[], stepSec: number): ChartPoint[][] {
  const out: ChartPoint[][] = [];
  let cur: ChartPoint[] = [];
  for (const p of points) {
    if (cur.length && p.t - cur[cur.length - 1].t > stepSec * 1.5) {
      out.push(cur);
      cur = [];
    }
    cur.push(p);
  }
  if (cur.length) out.push(cur);
  return out;
}

export function stepSecForLayer(layer: string): number {
  return layer === "down_5m" ? 300 : 60;
}

export function historyWindow(
  range: HistoryRange,
  nowSec = Math.floor(Date.now() / 1000),
): { sinceUnix: bigint; untilUnix: bigint } {
  const span = range === "7d" ? 7 * 86400 : 86400;
  return { sinceUnix: BigInt(nowSec - span), untilUnix: BigInt(nowSec) };
}

export function pointsFromSeries(
  series: ReadonlyArray<{ name?: string; points?: ReadonlyArray<{ tsUnix?: bigint | number; value?: number }> }> | undefined,
  name: string,
): ChartPoint[] {
  const found = series?.find((s) => s.name === name);
  if (!found?.points?.length) {
    return [];
  }
  return found.points.map((p) => ({
    t: Number(p.tsUnix ?? 0),
    v: Number(p.value ?? 0),
  }));
}

export function isHistoryUnavailable(err: unknown): boolean {
  if (err == null) {
    return false;
  }
  if (typeof err === "object" && "code" in err && (err as { code: unknown }).code === 14) {
    return true;
  }
  const text = err instanceof Error ? err.message : String(err);
  return /unavailable/i.test(text);
}

function niceCeil(value: number): number {
  if (value <= 0) {
    return 1;
  }
  const exp = Math.floor(Math.log10(value));
  const frac = value / 10 ** exp;
  const nice = frac <= 1 ? 1 : frac <= 2 ? 2 : frac <= 2.5 ? 2.5 : frac <= 5 ? 5 : 10;
  return nice * 10 ** exp;
}

function niceBytesCeil(value: number): number {
  if (value <= 0) {
    return 1024;
  }
  const unit = value >= 1024 ** 4 ? 1024 ** 4 : value >= 1024 ** 3 ? 1024 ** 3 : value >= 1024 ** 2 ? 1024 ** 2 : value >= 1024 ? 1024 : 1;
  const n = value / unit;
  const nice = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : n <= 8 ? 8 : 10;
  return nice * unit;
}

function ticksFor(max: number, count: number): number[] {
  const ticks: number[] = [];
  for (let i = 0; i <= count; i++) {
    ticks.push((max * i) / count);
  }
  return ticks;
}

export function niceYScale(values: number[], unit: ChartUnit): YScale {
  const dataMax = values.reduce((m, v) => (Number.isFinite(v) && v > m ? v : m), 0);
  if (unit === "percent") {
    const padded = dataMax <= 0 ? 5 : Math.min(100, dataMax * 1.25);
    const max = Math.min(100, Math.max(niceCeil(padded), dataMax <= 0 ? 5 : dataMax));
    return { min: 0, max, ticks: ticksFor(max, 4) };
  }
  const padded = dataMax <= 0 ? 1024 : dataMax * 1.25;
  const max = Math.max(niceBytesCeil(padded), dataMax);
  return { min: 0, max, ticks: ticksFor(max, 4) };
}

export function formatChartValue(value: number, unit: ChartUnit): string {
  if (!Number.isFinite(value)) {
    return "—";
  }
  if (unit === "percent") {
    if (value === 0) {
      return "0.00%";
    }
    if (value > 0 && value < 10) {
      return `${value.toFixed(1)}%`;
    }
    return `${Number.isInteger(value) ? String(value) : value.toFixed(1)}%`;
  }
  const abs = Math.abs(value);
  if (abs >= 1024 ** 3) {
    return `${(value / 1024 ** 3).toFixed(1)} GiB`;
  }
  if (abs >= 1024 ** 2) {
    return `${(value / 1024 ** 2).toFixed(1)} MiB`;
  }
  if (abs >= 1024) {
    return `${(value / 1024).toFixed(1)} KiB`;
  }
  return `${Math.round(value)} B`;
}

export function formatChartTime(tsSec: number, spanSec: number, timeZone?: string): string {
  const d = new Date(tsSec * 1000);
  if (spanSec > 36 * 3600) {
    return new Intl.DateTimeFormat("en-GB", { month: "short", day: "numeric", timeZone }).format(d);
  }
  return new Intl.DateTimeFormat("en-GB", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
    timeZone,
  }).format(d);
}

export function nearestPoint(
  points: ChartPoint[],
  tMin: number,
  tMax: number,
  x: number,
  plotW: number,
): ChartPoint | null {
  if (!points.length || plotW <= 0) {
    return null;
  }
  const span = tMax - tMin || 1;
  const t = tMin + (x / plotW) * span;
  let best = points[0];
  let bestDist = Math.abs(points[0].t - t);
  for (let i = 1; i < points.length; i++) {
    const dist = Math.abs(points[i].t - t);
    if (dist < bestDist) {
      best = points[i];
      bestDist = dist;
    }
  }
  return best;
}
