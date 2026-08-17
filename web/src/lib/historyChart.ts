export type ChartPoint = { t: number; v: number };

export type HistoryRange = "24h" | "7d";

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
