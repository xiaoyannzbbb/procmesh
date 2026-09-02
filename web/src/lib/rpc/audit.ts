import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { AuditService } from "../../gen/procmesh/v1/audit_pb";
import { transport } from "../connect";

export type AuditClient = Pick<Client<typeof AuditService>, "listAudit">;

export function useAuditClient(): AuditClient {
  return inject<AuditClient | null>("auditClient", null) ?? createClient(AuditService, transport);
}
