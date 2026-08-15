import { ConnectError } from "@connectrpc/connect";
import { ErrorInfoSchema } from "../gen/procmesh/v1/api_pb";

export function appCode(err: unknown): string {
  if (!(err instanceof ConnectError)) {
    return "";
  }
  return err.findDetails(ErrorInfoSchema)[0]?.code ?? "";
}

export function isConflict(err: unknown): boolean {
  return appCode(err) === "CONFLICT";
}
