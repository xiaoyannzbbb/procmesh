export const LIVE = "LIVE";
export const STALE = "STALE";
export const UNKNOWN = "UNKNOWN";
export const MAX_AGE_MS = 10_000;
export const PROCESS_MAX_AGE_MS = 40_000;

export type Freshness = typeof LIVE | typeof STALE | typeof UNKNOWN;

export function classify(
  nowMs: number,
  lastUpdatedUnixMs: number,
  nodeState: string,
  maxAgeMs: number = MAX_AGE_MS,
): Freshness {
  if (lastUpdatedUnixMs <= 0) {
    return UNKNOWN;
  }
  switch (nodeState) {
    case "REMOVED":
    case "REVOKED":
      return UNKNOWN;
  }
  const age = nowMs - lastUpdatedUnixMs;
  if (nodeState === "ALIVE" && age <= maxAgeMs) {
    return LIVE;
  }
  return STALE;
}

export function formatAge(nowMs: number, lastUpdatedUnixMs: number): string {
  if (lastUpdatedUnixMs <= 0) {
    return "unknown";
  }
  const ageMs = Math.max(0, nowMs - lastUpdatedUnixMs);
  const seconds = Math.floor(ageMs / 1000);
  if (seconds < 60) {
    return `${seconds}s ago`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m ago`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${hours}h ago`;
  }
  return `${Math.floor(hours / 24)}d ago`;
}
