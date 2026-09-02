import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { AlertService } from "../../gen/procmesh/v1/alert_pb";
import { transport } from "../connect";

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

export function useAlertClient(): AlertClient {
  return inject<AlertClient | null>("alertClient", null) ?? createClient(AlertService, transport);
}
