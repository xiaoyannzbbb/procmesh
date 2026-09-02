export type ProcessStateKind = "desired" | "observed" | "health";
export type ProcessStateTone = "ok" | "warn" | "neutral" | "danger";

export function processStateTone(kind: ProcessStateKind, state: string): ProcessStateTone {
  if (kind === "desired") {
    return state === "RUNNING" ? "ok" : "neutral";
  }
  if (kind === "health") {
    if (state === "HEALTHY") {
      return "ok";
    }
    if (state === "UNHEALTHY") {
      return "danger";
    }
    return "neutral";
  }
  switch (state) {
    case "RUNNING":
      return "ok";
    case "STARTING":
    case "STOPPING":
    case "BACKOFF":
    case "EXITED":
      return "warn";
    case "FATAL":
      return "danger";
    default:
      return "neutral";
  }
}
