import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { ClusterService, NodeService } from "../../gen/procmesh/v1/cluster_pb";
import { transport } from "../connect";

export type ClusterClient = Pick<Client<typeof ClusterService>, "overview">;
export type NodeClient = Pick<
  Client<typeof NodeService>,
  "listNodes" | "getNode" | "createJoinToken" | "removeNode"
>;

export function useClusterClient(): ClusterClient {
  return inject<ClusterClient | null>("clusterClient", null) ?? createClient(ClusterService, transport);
}

export function useNodeClient(): NodeClient {
  return inject<NodeClient | null>("nodeClient", null) ?? createClient(NodeService, transport);
}
