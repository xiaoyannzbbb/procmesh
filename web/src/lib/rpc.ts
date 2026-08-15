import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { ClusterService, MetricsService, NodeService, ProcessService } from "../gen/procmesh/v1/api_pb";
import { transport } from "./connect";

export type ClusterClient = Pick<Client<typeof ClusterService>, "overview">;
export type NodeClient = Pick<Client<typeof NodeService>, "listNodes" | "getNode" | "removeNode">;
export type ProcessClient = Pick<
  Client<typeof ProcessService>,
  "getProcess" | "startProcess" | "stopProcess" | "restartProcess" | "killProcess"
>;
export type MetricsClient = Pick<Client<typeof MetricsService>, "getProcessMetrics">;

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
