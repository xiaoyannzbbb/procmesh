import { createClient, type Client } from "@connectrpc/connect";
import { inject } from "vue";
import { GroupService, RoleService, UserService } from "../../gen/procmesh/v1/access_pb";
import { transport } from "../connect";

export type UserClient = Pick<Client<typeof UserService>, "listUsers" | "createUser" | "disableUser" | "enableUser">;
export type RoleClient = Pick<
  Client<typeof RoleService>,
  "listRoles" | "createRole" | "updateRole" | "deleteRole" | "grantRole" | "revokeRole"
>;
export type GroupClient = Pick<
  Client<typeof GroupService>,
  "listAgentGroups" | "createAgentGroup" | "deleteAgentGroup" | "addAgentGroupMember" | "removeAgentGroupMember"
>;

export function useUserClient(): UserClient {
  return inject<UserClient | null>("userClient", null) ?? createClient(UserService, transport);
}

export function useRoleClient(): RoleClient {
  return inject<RoleClient | null>("roleClient", null) ?? createClient(RoleService, transport);
}

export function useGroupClient(): GroupClient {
  return inject<GroupClient | null>("groupClient", null) ?? createClient(GroupService, transport);
}
