import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { DisasterReplicationService } from "../../gen/procmesh/v1/disaster_replication_pb";
import { transport } from "../connect";

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

export function useReplicationClient(): ReplicationClient {
  return (
    inject<ReplicationClient | null>("replicationClient", null) ??
    createClient(DisasterReplicationService, transport)
  );
}
