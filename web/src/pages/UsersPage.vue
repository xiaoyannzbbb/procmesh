<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { Plus, ShieldPlus, UserCheck, UserX } from "lucide-vue-next";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import Drawer from "../components/Drawer.vue";
import Toast from "../components/Toast.vue";
import { newOperationId } from "../lib/opid";
import { useRoleClient, useUserClient } from "../lib/rpc/access";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const { t } = useI18n();

const MIN_PASSWORD = 10;
const POLL_MS = 5000;
type GrantScope = "CLUSTER" | "AGENT" | "AGENT_GROUP" | "PROCESS_GROUP";

const userClient = useUserClient();
const roleClient = useRoleClient();
const queryClient = useQueryClient();
const actionError = ref("");
const createDrawerOpen = ref(false);
const bindDrawerOpen = ref(false);
const pendingDisable = ref<{ userId: string; name: string } | null>(null);
const toastMessage = ref("");
const toastType = ref<"success" | "error">("success");
const showToast = ref(false);

const username = ref("");
const password = ref("");
const displayName = ref("");
const email = ref("");
const selectedUserId = ref("");
const selectedUsername = ref("");
const grantRoleId = ref("");
const grantScope = ref<GrantScope>("CLUSTER");
const grantScopeId = ref("");

const permissions = computed(() => new Set(session.value?.permissions ?? []));
const canCreate = computed(() => permissions.value.has("user.create"));
const canUpdate = computed(() => permissions.value.has("user.update"));
const canReadRoles = computed(() => permissions.value.has("role.read"));
const roleDataReady = computed(() => canReadRoles.value && roleQuery.isSuccess.value);
const canBindRoles = computed(() => roleDataReady.value && permissions.value.has("role.manage"));
const hasRowActions = computed(() => canUpdate.value || canBindRoles.value);

const query = useQuery({
  queryKey: ["users"],
  queryFn: () => userClient.listUsers({}),
  refetchInterval: POLL_MS,
});

const roleQuery = useQuery({
  queryKey: ["roles"],
  queryFn: () => roleClient.listRoles({}),
  enabled: canReadRoles,
  refetchInterval: POLL_MS,
});

const users = computed(() => query.data.value?.users ?? []);
const roles = computed(() => roleQuery.data.value?.roles ?? []);
const bindings = computed(() => roleQuery.data.value?.bindings ?? []);
const errorText = computed(() => query.error.value ? formatRemoteError(query.error.value) : "");
const roleErrorText = computed(() => roleQuery.error.value ? formatRemoteError(roleQuery.error.value) : "");
const tableColumnCount = computed(() => 5 + (canReadRoles.value ? 1 : 0) + (hasRowActions.value ? 1 : 0));
const createReady = computed(() => username.value.trim().length > 0 && password.value.length >= MIN_PASSWORD);
const scopeIdRequired = computed(() => grantScope.value !== "CLUSTER");
const grantReady = computed(() => Boolean(
  selectedUserId.value && grantRoleId.value && (!scopeIdRequired.value || grantScopeId.value.trim()),
));
const activeSuperAdminIds = computed(() => {
  if (!roleDataReady.value) return new Set<string>();
  const activeUserIds = new Set(users.value.filter((user) => user.status === "ACTIVE").map((user) => user.userId));
  return new Set(bindings.value
    .filter((binding) => binding.roleId === "super_admin" && binding.scopeType === "CLUSTER" && !binding.scopeId && activeUserIds.has(binding.userId))
    .map((binding) => binding.userId));
});

function mutationMeta() {
  return { operationId: newOperationId(), operator: session.value?.username ?? "" };
}

function formatLastLogin(unix: bigint | number | undefined): string {
  const value = Number(unix ?? 0);
  return Number.isFinite(value) && value > 0 ? new Date(value * 1000).toISOString() : "—";
}

function roleLabel(roleId: string): string {
  return roles.value.find((role) => role.roleId === roleId)?.name || roleId;
}

function scopeLabel(scopeType: string, scopeId: string): string {
  const labels: Record<string, string> = {
    CLUSTER: t("role.scopeCluster"),
    AGENT: t("role.scopeAgent"),
    AGENT_GROUP: t("role.scopeAgentGroup"),
    PROCESS_GROUP: t("role.scopeProcessGroup"),
  };
  const label = labels[scopeType || "CLUSTER"] || scopeType;
  return scopeId ? `${label}: ${scopeId}` : label;
}

function userBindings(userId: string) {
  return bindings.value.filter((binding) => binding.userId === userId);
}

function disableBlockReason(userId: string): string {
  if (userId === session.value?.userId) return t("users.disableCurrentUser");
  if (activeSuperAdminIds.value.size === 1 && activeSuperAdminIds.value.has(userId)) {
    return t("users.disableLastSuperAdmin");
  }
  return "";
}

function notify(message: string, type: "success" | "error"): void {
  toastMessage.value = message;
  toastType.value = type;
  showToast.value = true;
}

function resetCreateForm(): void {
  username.value = "";
  password.value = "";
  displayName.value = "";
  email.value = "";
}

function resetGrantForm(): void {
  grantRoleId.value = "";
  grantScope.value = "CLUSTER";
  grantScopeId.value = "";
}

function openCreateDrawer(): void {
  actionError.value = "";
  resetCreateForm();
  bindDrawerOpen.value = false;
  createDrawerOpen.value = true;
}

function openBindDrawer(userId: string, name: string): void {
  actionError.value = "";
  resetGrantForm();
  selectedUserId.value = userId;
  selectedUsername.value = name;
  createDrawerOpen.value = false;
  bindDrawerOpen.value = true;
}

const createMut = useMutation({
  mutationFn: () => userClient.createUser({
    meta: mutationMeta(),
    username: username.value.trim(),
    password: password.value,
    displayName: displayName.value.trim(),
    email: email.value.trim(),
  }),
  onSuccess: async () => {
    createDrawerOpen.value = false;
    resetCreateForm();
    await queryClient.invalidateQueries({ queryKey: ["users"] });
    notify(t("users.createUser.success"), "success");
  },
  onError: (error: unknown) => { actionError.value = formatRemoteError(error); },
});

const disableMut = useMutation({
  mutationFn: (userId: string) => userClient.disableUser({ meta: mutationMeta(), userId }),
  onSuccess: async () => {
    pendingDisable.value = null;
    await queryClient.invalidateQueries({ queryKey: ["users"] });
    notify(t("users.disableSuccess"), "success");
  },
  onError: (error: unknown) => {
    pendingDisable.value = null;
    notify(formatRemoteError(error), "error");
  },
});

const enableMut = useMutation({
  mutationFn: (userId: string) => userClient.enableUser({ meta: mutationMeta(), userId }),
  onSuccess: async () => {
    await queryClient.invalidateQueries({ queryKey: ["users"] });
    notify(t("users.enableSuccess"), "success");
  },
  onError: (error: unknown) => { notify(formatRemoteError(error), "error"); },
});

const grantMut = useMutation({
  mutationFn: () => roleClient.grantRole({
    meta: mutationMeta(),
    userId: selectedUserId.value,
    roleId: grantRoleId.value,
    scopeType: grantScope.value,
    scopeId: grantScopeId.value.trim(),
  }),
  onSuccess: async () => {
    bindDrawerOpen.value = false;
    resetGrantForm();
    await queryClient.invalidateQueries({ queryKey: ["roles"] });
    notify(t("users.bindRole.success"), "success");
  },
  onError: (error: unknown) => { actionError.value = formatRemoteError(error); },
});

const acting = computed(() => createMut.isPending.value || disableMut.isPending.value || enableMut.isPending.value || grantMut.isPending.value);

async function onCreate(): Promise<void> {
  if (!canCreate.value || !createReady.value || acting.value) return;
  actionError.value = "";
  try { await createMut.mutateAsync(); } catch { /* handled by mutation */ }
}

function requestDisable(userId: string, name: string): void {
  if (!canUpdate.value || !userId || acting.value || disableBlockReason(userId)) return;
  pendingDisable.value = { userId, name };
}

async function confirmDisable(): Promise<void> {
  const pending = pendingDisable.value;
  if (!pending || acting.value) return;
  try { await disableMut.mutateAsync(pending.userId); } catch { /* handled by mutation */ }
}

function cancelDisable(): void {
  if (!acting.value) pendingDisable.value = null;
}

async function onEnable(userId: string): Promise<void> {
  if (!canUpdate.value || !userId || acting.value) return;
  try { await enableMut.mutateAsync(userId); } catch { /* handled by mutation */ }
}

async function onGrant(): Promise<void> {
  if (!canBindRoles.value || !grantReady.value || acting.value) return;
  actionError.value = "";
  try { await grantMut.mutateAsync(); } catch { /* handled by mutation */ }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h1>{{ t("users.title") }}</h1>
      <button v-if="canCreate" type="button" class="btn btn-primary" data-action="create-user" @click="openCreateDrawer">
        <Plus :size="18" aria-hidden="true" />
        {{ t("users.createUser.title") }}
      </button>
    </div>

    <p v-if="query.isPending && !query.data" class="muted">{{ t("users.loading") }}</p>
    <p v-else-if="errorText && !query.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <p v-if="roleErrorText" class="error" role="alert">{{ t("users.roles.loadError", { error: roleErrorText }) }}</p>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("users.table.username") }}</th>
              <th>{{ t("users.table.display") }}</th>
              <th>{{ t("users.table.email") }}</th>
              <th v-if="canReadRoles" class="roles-column-header">{{ t("users.table.roles") }}</th>
              <th>{{ t("users.table.status") }}</th>
              <th>{{ t("users.table.lastLogin") }}</th>
              <th v-if="hasRowActions">{{ t("users.table.actions") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="user in users" :key="user.userId || user.username" :data-user-id="user.userId">
              <td>{{ user.username }}</td>
              <td>{{ user.displayName || "—" }}</td>
              <td>{{ user.email || "—" }}</td>
              <td v-if="canReadRoles">
                <span v-if="roleQuery.isPending.value" class="muted">{{ t("users.roles.loading") }}</span>
                <span v-else-if="roleErrorText" class="error">{{ t("users.roles.unavailable") }}</span>
                <div v-else-if="userBindings(user.userId).length" class="role-tags">
                  <span v-for="(binding, index) in userBindings(user.userId)" :key="`${binding.roleId}:${binding.scopeType}:${binding.scopeId}:${index}`" class="role-tag">
                    <strong>{{ roleLabel(binding.roleId) }}</strong>
                    <span>{{ scopeLabel(binding.scopeType, binding.scopeId) }}</span>
                  </span>
                </div>
                <span v-else class="muted">—</span>
              </td>
              <td>{{ user.status || "—" }}</td>
              <td>{{ formatLastLogin(user.lastLoginUnix) }}</td>
              <td v-if="hasRowActions">
                <div class="row-actions">
                  <button v-if="canBindRoles" type="button" class="btn btn-xs" data-action="bind-role" :disabled="acting" @click="openBindDrawer(user.userId, user.displayName || user.username)">
                    <ShieldPlus :size="16" aria-hidden="true" />
                    {{ t("users.bindRole.action") }}
                  </button>
                  <button v-if="canUpdate && user.status === 'DISABLED'" type="button" class="btn btn-xs" data-action="enable-user" :disabled="acting" @click="onEnable(user.userId)">
                    <UserCheck :size="16" aria-hidden="true" />
                    {{ t("users.enable") }}
                  </button>
                  <button
                    v-else-if="canUpdate"
                    type="button"
                    class="btn btn-xs btn-danger"
                    data-action="disable-user"
                    :disabled="acting || Boolean(disableBlockReason(user.userId))"
                    :title="disableBlockReason(user.userId)"
                    @click="requestDisable(user.userId, user.displayName || user.username)"
                  >
                    <UserX :size="16" aria-hidden="true" />
                    {{ t("users.disable") }}
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!users.length"><td :colspan="tableColumnCount" class="muted">{{ t("users.noUsers") }}</td></tr>
          </tbody>
        </table>
      </div>
    </template>

    <Drawer :open="createDrawerOpen" :title="t('users.createUser.title')" :close-label="t('actions.close')" @close="createDrawerOpen = false">
      <form class="drawer-form" @submit.prevent="onCreate">
        <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
        <label class="field"><span>{{ t("users.createUser.username") }} <span class="required" aria-hidden="true"></span></span><input v-model="username" class="input" name="username" type="text" autocomplete="off" required /></label>
        <label class="field"><span>{{ t("users.createUser.password") }} <span class="required" aria-hidden="true"></span></span><input v-model="password" class="input" name="password" type="password" autocomplete="new-password" :minlength="MIN_PASSWORD" required /><small>{{ t("users.createUser.passwordHint") }}</small></label>
        <label class="field"><span>{{ t("users.createUser.display") }}</span><input v-model="displayName" class="input" name="display_name" type="text" autocomplete="name" /></label>
        <label class="field"><span>{{ t("users.createUser.email") }}</span><input v-model="email" class="input" name="email" type="email" autocomplete="email" /></label>
        <div class="drawer-actions">
          <button type="button" class="btn" :disabled="acting" @click="createDrawerOpen = false">{{ t("actions.cancel") }}</button>
          <button class="btn btn-primary" type="submit" :disabled="!createReady || acting">{{ t("users.createUser.create") }}</button>
        </div>
      </form>
    </Drawer>

    <Drawer :open="bindDrawerOpen" :title="t('users.bindRole.title')" :close-label="t('actions.close')" @close="bindDrawerOpen = false">
      <form class="drawer-form" @submit.prevent="onGrant">
        <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
        <div class="selected-user"><span>{{ t("users.bindRole.user") }}</span><strong>{{ selectedUsername }}</strong><small>{{ selectedUserId }}</small></div>
        <label class="field"><span>{{ t("roles.grant.role") }} <span class="required" aria-hidden="true"></span></span><select v-model="grantRoleId" class="input" name="role_id" required><option value="">{{ t("roles.grant.selectRole") }}</option><option v-for="role in roles" :key="role.roleId" :value="role.roleId">{{ role.name }}</option></select></label>
        <label class="field"><span>{{ t("roles.grant.scope") }}</span><select v-model="grantScope" class="input" name="scope_type"><option value="CLUSTER">{{ t("role.scopeCluster") }}</option><option value="AGENT">{{ t("role.scopeAgent") }}</option><option value="AGENT_GROUP">{{ t("role.scopeAgentGroup") }}</option><option value="PROCESS_GROUP">{{ t("role.scopeProcessGroup") }}</option></select></label>
        <label v-if="scopeIdRequired" class="field"><span>{{ t("roles.grant.scopeId") }} <span class="required" aria-hidden="true"></span></span><input v-model="grantScopeId" class="input" name="scope_id" type="text" required /></label>
        <div class="drawer-actions"><button type="button" class="btn" :disabled="acting" @click="bindDrawerOpen = false">{{ t("actions.cancel") }}</button><button class="btn btn-primary" type="submit" :disabled="!grantReady || acting">{{ t("users.bindRole.submit") }}</button></div>
      </form>
    </Drawer>

    <ConfirmDialog
      :open="Boolean(pendingDisable)"
      :title="t('users.disableConfirmTitle')"
      :message="t('users.disableConfirmMessage', { name: pendingDisable?.name ?? '' })"
      :confirm-label="t('users.disableConfirm')"
      :cancel-label="t('actions.cancel')"
      :pending="disableMut.isPending.value"
      @cancel="cancelDisable"
      @confirm="confirmDisable"
    />

    <Toast :show="showToast" :message="toastMessage" :type="toastType" @close="showToast = false" />
  </div>
</template>

<style scoped>
.page { display: flex; flex-direction: column; gap: 1rem; }
.page-header, .row-actions, .drawer-actions { display: flex; align-items: center; gap: 0.75rem; }
.page-header { justify-content: space-between; }
.row-actions, .drawer-actions { flex-wrap: wrap; }
.drawer-actions { justify-content: flex-end; margin-top: auto; padding-top: 1rem; border-top: 1px solid var(--color-border); }
.btn { display: inline-flex; align-items: center; gap: 0.375rem; }
h1 { margin: 0; font-size: 1.35rem; font-weight: 650; }
.muted { color: var(--color-muted); font-size: 0.875rem; }
.error { margin: 0; color: var(--color-danger); font-size: 0.875rem; }
.card { border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-card); overflow: auto; }
.drawer-form { display: flex; flex-direction: column; gap: 1rem; min-height: 100%; }
.field, .selected-user { display: flex; flex-direction: column; gap: 0.375rem; color: var(--color-muted); font-size: 0.875rem; }
.field small, .selected-user small { font-size: 0.75rem; overflow-wrap: anywhere; }
.required::after { color: var(--color-danger); content: "*"; }
.selected-user { padding: 0.875rem 1rem; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-hover); }
.selected-user strong { color: var(--color-text); font-size: 1rem; }
.role-tags { display: flex; flex-wrap: wrap; gap: 0.375rem; min-width: 10rem; }
.role-tag { display: inline-flex; flex-direction: column; gap: 0.0625rem; max-width: 14rem; padding: 0.3rem 0.5rem; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-hover); overflow-wrap: anywhere; }
.role-tag strong { color: var(--color-text); font-size: 0.75rem; font-weight: 600; }
.role-tag span { color: var(--color-muted); font-size: 0.6875rem; }
@media (max-width: 640px) {
  .page-header { align-items: flex-start; }
  .page-header .btn { min-height: 2.75rem; }
}
</style>
