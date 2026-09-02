import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { ConfigService, LogService, ProcessService } from "../../gen/procmesh/v1/process_pb";
import { transport } from "../connect";

export type ProcessClient = Pick<
  Client<typeof ProcessService>,
  | "listProcesses"
  | "getProcess"
  | "startProcess"
  | "stopProcess"
  | "restartProcess"
  | "killProcess"
  | "applyProcess"
  | "deleteProcess"
>;
export type ConfigClient = Pick<
  Client<typeof ConfigService>,
  "getConfig" | "updateConfig" | "history" | "diff" | "rollback"
>;
export type LogClient = Pick<Client<typeof LogService>, "tailLogs" | "streamLogs" | "downloadLogs">;

export function useProcessClient(): ProcessClient {
  return inject<ProcessClient | null>("processClient", null) ?? createClient(ProcessService, transport);
}

export function useConfigClient(): ConfigClient {
  return inject<ConfigClient | null>("configClient", null) ?? createClient(ConfigService, transport);
}

export function useLogClient(): LogClient {
  return inject<LogClient | null>("logClient", null) ?? createClient(LogService, transport);
}
