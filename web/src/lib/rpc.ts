import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import {
  AuditService,
  ClusterService,
  ConfigService,
  LogService,
  MetricsService,
  NodeService,
  ProcessService,
  RoleService,
  UserService,
} from "../gen/procmesh/v1/api_pb";
import { transport } from "./connect";

export type ClusterClient = Pick<Client<typeof ClusterService>, "overview">;
export type NodeClient = Pick<Client<typeof NodeService>, "listNodes" | "getNode" | "removeNode">;
export type ProcessClient = Pick<
  Client<typeof ProcessService>,
  "getProcess" | "startProcess" | "stopProcess" | "restartProcess" | "killProcess"
>;
export type MetricsClient = Pick<Client<typeof MetricsService>, "getProcessMetrics">;
export type ConfigClient = Pick<
  Client<typeof ConfigService>,
  "getConfig" | "updateConfig" | "history" | "diff" | "rollback"
>;
export type LogClient = Pick<Client<typeof LogService>, "tailLogs" | "streamLogs" | "downloadLogs">;
export type UserClient = Pick<Client<typeof UserService>, "listUsers" | "createUser" | "disableUser">;
export type RoleClient = Pick<Client<typeof RoleService>, "listRoles" | "createRole" | "grantRole">;
export type AuditClient = Pick<Client<typeof AuditService>, "listAudit">;

export function useClusterClient(): ClusterClient {
  return inject<ClusterClient | null>("clusterClient", null) ?? createClient(ClusterService, transport);
}

export function useNodeClient(): NodeClient {
  return inject<NodeClient | null>("nodeClient", null) ?? createClient(NodeService, transport);
}

export function useProcessClient(): ProcessClient {
  return inject<ProcessClient | null>("processClient", null) ?? createClient(ProcessService, transport);
}

export function useMetricsClient(): MetricsClient {
  return inject<MetricsClient | null>("metricsClient", null) ?? createClient(MetricsService, transport);
}

export function useConfigClient(): ConfigClient {
  return inject<ConfigClient | null>("configClient", null) ?? createClient(ConfigService, transport);
}

export function useLogClient(): LogClient {
  return inject<LogClient | null>("logClient", null) ?? createClient(LogService, transport);
}

export function useUserClient(): UserClient {
  return inject<UserClient | null>("userClient", null) ?? createClient(UserService, transport);
}

export function useRoleClient(): RoleClient {
  return inject<RoleClient | null>("roleClient", null) ?? createClient(RoleService, transport);
}

export function useAuditClient(): AuditClient {
  return inject<AuditClient | null>("auditClient", null) ?? createClient(AuditService, transport);
}
