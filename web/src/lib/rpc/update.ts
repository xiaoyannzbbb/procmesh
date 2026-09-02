import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { UpdateService } from "../../gen/procmesh/v1/update_pb";
import { transport } from "../connect";

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

export function useUpdateClient(): UpdateClient {
  return inject<UpdateClient | null>("updateClient", null) ?? createClient(UpdateService, transport);
}
