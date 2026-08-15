import { createClient, type Client } from "@connectrpc/connect";
import { inject, ref, type Ref } from "vue";
import { AuthService } from "../gen/procmesh/v1/api_pb";
import { transport } from "./connect";
import { clearCsrf, saveCsrf as persistCsrf } from "./csrf";

export type Me = {
  userId: string;
  username: string;
  csrfToken: string;
  permissions: string[];
};

export type AuthClient = Pick<Client<typeof AuthService>, "login" | "logout" | "getMe">;

export const session: Ref<Me | null> = ref(null);

export function saveCsrf(token: string): void {
  persistCsrf(token);
}

export function clearSession(): void {
  session.value = null;
  clearCsrf();
}

export function useAuthClient(): AuthClient {
  return inject<AuthClient | null>("authClient", null) ?? createClient(AuthService, transport);
}

export async function loadSession(): Promise<Me | null> {
  try {
    const me = await createClient(AuthService, transport).getMe({});
    if (me.csrfToken) {
      saveCsrf(me.csrfToken);
    }
    const next: Me = {
      userId: me.userId,
      username: me.username,
      csrfToken: me.csrfToken,
      permissions: [...me.permissions],
    };
    session.value = next;
    return next;
  } catch {
    clearSession();
    return null;
  }
}
