export const HEADER_TARGET_NODE = "Procmesh-Target-Node";

export function withTarget(nodeId: string): HeadersInit {
  if (!nodeId) {
    return {};
  }
  return { [HEADER_TARGET_NODE]: nodeId };
}
