import { LIVE, type Freshness } from "./freshness";

export type RemoteProcessNode = {
  freshness: Freshness;
  disableRemoteCreate: boolean;
  disableRemoteUpdate: boolean;
  disableRemoteDelete: boolean;
};

export function remoteCreateBlocked(node?: RemoteProcessNode | null): boolean {
  if (!node || node.freshness !== LIVE) {
    return true;
  }
  return node.disableRemoteCreate;
}

export function remoteUpdateBlocked(node?: RemoteProcessNode | null): boolean {
  if (!node || node.freshness !== LIVE) {
    return true;
  }
  return node.disableRemoteUpdate;
}

export function remoteDeleteBlocked(node?: RemoteProcessNode | null): boolean {
  if (!node || node.freshness !== LIVE) {
    return true;
  }
  return node.disableRemoteDelete;
}

export function processDeletable(observed: string, desired: string): boolean {
  if (desired !== "STOPPED") {
    return false;
  }
  return observed === "STOPPED" || observed === "FATAL" || observed === "UNKNOWN";
}
