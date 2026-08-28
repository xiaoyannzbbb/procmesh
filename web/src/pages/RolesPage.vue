<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { Plus, UserPlus } from "lucide-vue-next";
import Drawer from "../components/Drawer.vue";
import Toast from "../components/Toast.vue";
import UserSelector from "../components/UserSelector.vue";
import { newOperationId } from "../lib/opid";
import { useRoleClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const { t } = useI18n();

const POLL_MS = 5000;
const BUILTIN_ROLE_IDS = new Set(["super_admin", "cluster_admin", "operator", "viewer"]);
const ROLE_PERMISSIONS = [
  "cluster.read", "cluster.manage", "node.read", "node.manage", "node.remove",
  "process.read", "process.create", "process.update", "process.delete", "process.start",
  "process.stop", "process.restart", "process.config.read", "process.config.update",
  "process.logs.read", "process.logs.download", "user.read", "user.create", "user.update",
  "user.delete", "role.read", "role.manage", "audit.read", "command.execute",
  "command.execute.batch", "batch.execute", "alert.read", "alert.manage", "backup.read",
  "backup.manage",
] as const;

type GrantScope = "CLUSTER" | "AGENT" | "AGENT_GROUP" | "PROCESS_GROUP";

const client = useRoleClient();
const queryClient = useQueryClient();
const actionError = ref("");
const createDrawerOpen = ref(false);
const grantDrawerOpen = ref(false);
const toastMessage = ref("");
const toastType = ref<"success" | "error">("success");
const showToast = ref(false);

const roleName = ref("");
const selectedPerms = ref<string[]>([]);
const grantUserId = ref("");
const grantRoleId = ref("");
const grantScope = ref<GrantScope>("CLUSTER");
const grantScopeId = ref("");

const permissions = computed(() => new Set(session.value?.permissions ?? []));
const canManage = computed(() => permissions.value.has("role.manage"));
const canSelectUsers = computed(() => permissions.value.has("user.read"));

const query = useQuery({
  queryKey: ["roles"],
  queryFn: () => client.listRoles({}),
  refetchInterval: POLL_MS,
});

const roles = computed(() => query.data.value?.roles ?? []);
const bindings = computed(() => query.data.value?.bindings ?? []);
const errorText = computed(() => query.error.value ? formatRemoteError(query.error.value) : "");
const createReady = computed(() => roleName.value.trim().length > 0);
const scopeIdRequired = computed(() => grantScope.value !== "CLUSTER");
const grantReady = computed(() => Boolean(
  grantUserId.value &&
  grantRoleId.value &&
  (!scopeIdRequired.value || grantScopeId.value.trim()),
));

function mutationMeta() {
  return { operationId: newOperationId(), operator: session.value?.username ?? "" };
}

function isBuiltin(roleId: string): boolean {
  return BUILTIN_ROLE_IDS.has(roleId);
}

function roleLabel(roleId: string): string {
  return roles.value.find((role) => role.roleId === roleId)?.name || roleId;
}

function togglePerm(perm: string, checked: boolean): void {
  selectedPerms.value = checked
    ? Array.from(new Set([...selectedPerms.value, perm]))
    : selectedPerms.value.filter((item) => item !== perm);
}

function notify(message: string, type: "success" | "error"): void {
  toastMessage.value = message;
  toastType.value = type;
  showToast.value = true;
}

function resetCreateForm(): void {
  roleName.value = "";
  selectedPerms.value = [];
}

function resetGrantForm(): void {
  grantUserId.value = "";
  grantRoleId.value = "";
  grantScope.value = "CLUSTER";
  grantScopeId.value = "";
}

function openCreateDrawer(): void {
  actionError.value = "";
  resetCreateForm();
  grantDrawerOpen.value = false;
  createDrawerOpen.value = true;
}

function openGrantDrawer(): void {
  actionError.value = "";
  resetGrantForm();
  createDrawerOpen.value = false;
  grantDrawerOpen.value = true;
}

const createMut = useMutation({
  mutationFn: () => client.createRole({
    meta: mutationMeta(),
    name: roleName.value.trim(),
    permissions: [...selectedPerms.value],
  }),
  onSuccess: async () => {
    createDrawerOpen.value = false;
    resetCreateForm();
    await queryClient.invalidateQueries({ queryKey: ["roles"] });
    notify(t("roles.createRole.success"), "success");
  },
  onError: (error: unknown) => {
    actionError.value = formatRemoteError(error);
  },
});

const grantMut = useMutation({
  mutationFn: () => client.grantRole({
    meta: mutationMeta(),
    userId: grantUserId.value,
    roleId: grantRoleId.value,
    scopeType: grantScope.value,
    scopeId: grantScopeId.value.trim(),
  }),
  onSuccess: async () => {
    grantDrawerOpen.value = false;
    resetGrantForm();
    await queryClient.invalidateQueries({ queryKey: ["roles"] });
    notify(t("roles.grant.success"), "success");
  },
  onError: (error: unknown) => {
    actionError.value = formatRemoteError(error);
  },
});

const acting = computed(() => createMut.isPending.value || grantMut.isPending.value);

async function onCreate(): Promise<void> {
  if (!canManage.value || !createReady.value || acting.value) return;
  actionError.value = "";
  try { await createMut.mutateAsync(); } catch { /* handled by mutation */ }
}

async function onGrant(): Promise<void> {
  if (!canManage.value || !grantReady.value || acting.value) return;
  actionError.value = "";
  try { await grantMut.mutateAsync(); } catch { /* handled by mutation */ }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h1>{{ t("roles.title") }}</h1>
      <div v-if="canManage" class="header-actions">
        <button
          v-if="canSelectUsers"
          type="button"
          class="btn"
          data-action="grant-role"
          @click="openGrantDrawer"
        >
          <UserPlus :size="18" aria-hidden="true" />
          {{ t("roles.actions.grantRole") }}
        </button>
        <button type="button" class="btn btn-primary" data-action="create-role" @click="openCreateDrawer">
          <Plus :size="18" aria-hidden="true" />
          {{ t("roles.actions.createRole") }}
        </button>
      </div>
    </div>

    <p v-if="query.isPending && !query.data" class="muted">{{ t("roles.loading") }}</p>
    <p v-else-if="errorText && !query.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <div class="card">
        <table class="table">
          <thead><tr><th>{{ t("roles.table.name") }}</th><th>{{ t("roles.table.type") }}</th><th>{{ t("roles.table.permissions") }}</th></tr></thead>
          <tbody>
            <tr v-for="role in roles" :key="role.roleId">
              <td>{{ role.name }}</td>
              <td>{{ isBuiltin(role.roleId) ? t("roles.type.builtin") : t("roles.type.custom") }}</td>
              <td class="perms">{{ role.permissions.length ? role.permissions.join(", ") : "—" }}</td>
            </tr>
            <tr v-if="!roles.length"><td colspan="3" class="muted">{{ t("roles.noRoles") }}</td></tr>
          </tbody>
        </table>
      </div>

      <div class="card bindings-card">
        <h2>{{ t("roles.bindings.title") }}</h2>
        <table class="table">
          <thead><tr><th>{{ t("roles.bindings.table.userId") }}</th><th>{{ t("roles.bindings.table.role") }}</th><th>{{ t("roles.bindings.table.scope") }}</th><th>{{ t("roles.bindings.table.scopeId") }}</th></tr></thead>
          <tbody>
            <tr v-for="(binding, index) in bindings" :key="`${binding.userId}:${binding.roleId}:${binding.scopeType}:${binding.scopeId}:${index}`">
              <td class="mono">{{ binding.userId }}</td>
              <td>{{ roleLabel(binding.roleId) }}</td>
              <td>{{ binding.scopeType || "CLUSTER" }}</td>
              <td>{{ binding.scopeId || "—" }}</td>
            </tr>
            <tr v-if="!bindings.length"><td colspan="4" class="muted">{{ t("roles.bindings.noBindings") }}</td></tr>
          </tbody>
        </table>
      </div>
    </template>

    <Drawer :open="createDrawerOpen" :title="t('roles.createRole.title')" :close-label="t('actions.close')" @close="createDrawerOpen = false">
      <form class="drawer-form" @submit.prevent="onCreate">
        <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
        <label class="field">
          <span>{{ t("roles.createRole.name") }} <span class="required" aria-hidden="true"></span></span>
          <input v-model="roleName" class="input" name="role_name" type="text" autocomplete="off" required />
        </label>
        <fieldset class="perms-fieldset">
          <legend>{{ t("roles.createRole.permissions") }}</legend>
          <label v-for="perm in ROLE_PERMISSIONS" :key="perm" class="check">
            <input type="checkbox" :name="`perm-${perm}`" :value="perm" :checked="selectedPerms.includes(perm)" @change="togglePerm(perm, ($event.target as HTMLInputElement).checked)" />
            {{ perm }}
          </label>
        </fieldset>
        <div class="drawer-actions">
          <button type="button" class="btn" :disabled="acting" @click="createDrawerOpen = false">{{ t("actions.cancel") }}</button>
          <button class="btn btn-primary" type="submit" :disabled="!createReady || acting">{{ t("roles.createRole.create") }}</button>
        </div>
      </form>
    </Drawer>

    <Drawer :open="grantDrawerOpen" :title="t('roles.grant.title')" :close-label="t('actions.close')" @close="grantDrawerOpen = false">
      <form class="drawer-form" @submit.prevent="onGrant">
        <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
        <UserSelector v-model="grantUserId" :disabled="acting" />
        <label class="field">
          <span>{{ t("roles.grant.role") }} <span class="required" aria-hidden="true"></span></span>
          <select v-model="grantRoleId" class="input" name="role_id" required>
            <option value="">{{ t("roles.grant.selectRole") }}</option>
            <option v-for="role in roles" :key="role.roleId" :value="role.roleId">{{ role.name }}</option>
          </select>
        </label>
        <label class="field"><span>{{ t("roles.grant.scope") }}</span><select v-model="grantScope" class="input" name="scope_type"><option value="CLUSTER">{{ t("role.scopeCluster") }}</option><option value="AGENT">{{ t("role.scopeAgent") }}</option><option value="AGENT_GROUP">{{ t("role.scopeAgentGroup") }}</option><option value="PROCESS_GROUP">{{ t("role.scopeProcessGroup") }}</option></select></label>
        <label v-if="scopeIdRequired" class="field"><span>{{ t("roles.grant.scopeId") }} <span class="required" aria-hidden="true"></span></span><input v-model="grantScopeId" class="input" name="scope_id" type="text" required /></label>
        <div class="drawer-actions">
          <button type="button" class="btn" :disabled="acting" @click="grantDrawerOpen = false">{{ t("actions.cancel") }}</button>
          <button class="btn btn-primary" type="submit" :disabled="!grantReady || acting">{{ t("roles.grant.grant") }}</button>
        </div>
      </form>
    </Drawer>

    <Toast :show="showToast" :message="toastMessage" :type="toastType" @close="showToast = false" />
  </div>
</template>

<style scoped>
.page { display: flex; flex-direction: column; gap: 1rem; }
.page-header, .header-actions, .drawer-actions { display: flex; align-items: center; gap: 0.75rem; }
.page-header { justify-content: space-between; }
.header-actions, .drawer-actions { flex-wrap: wrap; }
.btn { display: inline-flex; align-items: center; gap: 0.375rem; }
h1 { margin: 0; font-size: 1.35rem; font-weight: 650; }
h2 { margin: 0; font-size: 1.05rem; font-weight: 650; }
.muted { color: var(--color-muted); font-size: 0.875rem; }
.error { margin: 0; color: var(--color-danger); font-size: 0.875rem; }
.card { border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-card); overflow: auto; }
.bindings-card h2 { padding: 1.25rem 1.25rem 0.5rem; }
.drawer-form { display: flex; flex-direction: column; gap: 1rem; min-height: 100%; }
.drawer-actions { justify-content: flex-end; margin-top: auto; padding-top: 1rem; border-top: 1px solid var(--color-border); }
.field { display: flex; flex-direction: column; gap: 0.375rem; color: var(--color-muted); font-size: 0.875rem; }
.required::after { color: var(--color-danger); content: "*"; }
.perms { font-size: 0.8rem; overflow-wrap: anywhere; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8rem; }
.perms-fieldset { margin: 0; padding: 0.75rem; display: grid; grid-template-columns: repeat(auto-fill, minmax(12rem, 1fr)); gap: 0.5rem 0.75rem; border: 1px solid var(--color-border); border-radius: var(--radius-sm); }
.perms-fieldset legend { padding: 0 0.25rem; color: var(--color-muted); font-size: 0.875rem; }
.check { display: flex; align-items: center; gap: 0.375rem; min-width: 0; font-size: 0.8rem; overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .page-header { align-items: flex-start; }
  .header-actions { justify-content: flex-end; }
  .header-actions .btn { min-height: 2.75rem; }
}
.table th, .table td {
  min-width: 100px;
}
</style>
