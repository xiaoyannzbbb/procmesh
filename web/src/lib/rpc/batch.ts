import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { BatchService } from "../../gen/procmesh/v1/batch_pb";
import { transport } from "../connect";

export type BatchClient = Pick<
  Client<typeof BatchService>,
  "createBatch" | "getBatch" | "listBatches" | "retryFailed" | "replayTimeout" | "exportBatch"
>;

export function useBatchClient(): BatchClient {
  return inject<BatchClient | null>("batchClient", null) ?? createClient(BatchService, transport);
}
