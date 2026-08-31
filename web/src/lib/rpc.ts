import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import {
  AlertService,
  AuditService,
  BackupService,
  BatchService,
  ClusterBackupService,
  ClusterService,
  ConfigService,
  DisasterReplicationService,
  GroupService,
  LogService,
  MetricsService,
  NodeService,
  ProcessService,
  RoleService,
  UpdateService,
  UserService,
} from "../gen/procmesh/v1/api_pb";
import { transport } from "./connect";

export type ClusterClient = Pick<Client<typeof ClusterService>, "overview">;
export type NodeClient = Pick<Client<typeof NodeService>, "listNodes" | "getNode" | "removeNode">;
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
export type MetricsClient = Pick<
  Client<typeof MetricsService>,
  "getProcessMetrics" | "getNodeHistory" | "getProcessHistory"
>;
export type ConfigClient = Pick<
  Client<typeof ConfigService>,
  "getConfig" | "updateConfig" | "history" | "diff" | "rollback"
>;
export type LogClient = Pick<Client<typeof LogService>, "tailLogs" | "streamLogs" | "downloadLogs">;
export type UserClient = Pick<Client<typeof UserService>, "listUsers" | "createUser" | "disableUser" | "enableUser">;
export type RoleClient = Pick<
  Client<typeof RoleService>,
  "listRoles" | "createRole" | "updateRole" | "deleteRole" | "grantRole" | "revokeRole"
>;
export type GroupClient = Pick<
  Client<typeof GroupService>,
  "listAgentGroups" | "createAgentGroup" | "deleteAgentGroup" | "addAgentGroupMember" | "removeAgentGroupMember"
>;
export type AuditClient = Pick<Client<typeof AuditService>, "listAudit">;
export type BatchClient = Pick<
  Client<typeof BatchService>,
  "createBatch" | "getBatch" | "listBatches" | "retryFailed" | "replayTimeout" | "exportBatch"
>;
export type AlertClient = Pick<
  Client<typeof AlertService>,
  | "listAlerts"
  | "getAlert"
  | "listAlertChannels"
  | "putAlertChannel"
  | "deleteAlertChannel"
  | "testAlertChannel"
  | "getAlertPolicy"
  | "putAlertPolicy"
>;
export type BackupClient = Pick<
  Client<typeof BackupService>,
  "listBackups" | "createBackup" | "deleteBackup" | "restoreBackup" | "getBackup"
>;
export type ClusterBackupClient = Pick<
  Client<typeof ClusterBackupService>,
  | "createPolicy"
  | "updatePolicy"
  | "deletePolicy"
  | "listPolicies"
  | "validatePolicy"
  | "startRun"
  | "getRun"
  | "listRuns"
  | "retryFailedTasks"
  | "getDestinationHealth"
>;
export type ReplicationClient = Pick<
  Client<typeof DisasterReplicationService>,
  | "getTopology"
  | "generatePolicyDraft"
  | "applyPolicyDraft"
  | "listPolicies"
  | "getPolicy"
  | "updatePolicy"
  | "deletePolicy"
  | "startRun"
  | "getRun"
  | "listRuns"
  | "retryFailedRoutes"
  | "verifyReplica"
  | "listRecoverableSnapshots"
  | "prepareRecoverableSnapshotRestore"
  | "restoreRecoverableSnapshot"
>;
export type UpdateClient = Pick<
  Client<typeof UpdateService>,
  | "checkLatest"
  | "listNodeUpdateStatus"
  | "applyNode"
  | "getLocalUpdateInfo"
  | "createClusterUpdate"
  | "listUpdateJobs"
  | "getUpdateJob"
  | "cancelRemaining"
  | "retryUpdateJob"
>;

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

export function useGroupClient(): GroupClient {
  return inject<GroupClient | null>("groupClient", null) ?? createClient(GroupService, transport);
}

export function useAuditClient(): AuditClient {
  return inject<AuditClient | null>("auditClient", null) ?? createClient(AuditService, transport);
}

export function useBatchClient(): BatchClient {
  return inject<BatchClient | null>("batchClient", null) ?? createClient(BatchService, transport);
}

export function useAlertClient(): AlertClient {
  return inject<AlertClient | null>("alertClient", null) ?? createClient(AlertService, transport);
}

export function useBackupClient(): BackupClient {
  return inject<BackupClient | null>("backupClient", null) ?? createClient(BackupService, transport);
}

export function useClusterBackupClient(): ClusterBackupClient {
  return (
    inject<ClusterBackupClient | null>("clusterBackupClient", null) ??
    createClient(ClusterBackupService, transport)
  );
}

export function useReplicationClient(): ReplicationClient {
  return (
    inject<ReplicationClient | null>("replicationClient", null) ??
    createClient(DisasterReplicationService, transport)
  );
}

export function useUpdateClient(): UpdateClient {
  return inject<UpdateClient | null>("updateClient", null) ?? createClient(UpdateService, transport);
}
