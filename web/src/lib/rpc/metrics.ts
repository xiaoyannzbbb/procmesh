import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { MetricsService } from "../../gen/procmesh/v1/metrics_pb";
import { transport } from "../connect";

export type MetricsClient = Pick<
  Client<typeof MetricsService>,
  "getProcessMetrics" | "getNodeHistory" | "getProcessHistory"
>;

export function useMetricsClient(): MetricsClient {
  return inject<MetricsClient | null>("metricsClient", null) ?? createClient(MetricsService, transport);
}
