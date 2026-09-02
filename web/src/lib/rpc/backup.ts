import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { BackupService } from "../../gen/procmesh/v1/backup_pb";
import { ClusterBackupService } from "../../gen/procmesh/v1/cluster_backup_pb";
import { transport } from "../connect";

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

export function useBackupClient(): BackupClient {
  return inject<BackupClient | null>("backupClient", null) ?? createClient(BackupService, transport);
}

export function useClusterBackupClient(): ClusterBackupClient {
  return (
    inject<ClusterBackupClient | null>("clusterBackupClient", null) ??
    createClient(ClusterBackupService, transport)
  );
}
