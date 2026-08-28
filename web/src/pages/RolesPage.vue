<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { AlertCircle, Plus, Search, ShieldCheck, UserPlus } from "lucide-vue-next";
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
const PERM_CHIP_LIMIT = 6;
const SKELETON_ROWS = 5;
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

const PERMISSION_GROUPS: ReadonlyArray<{ key: string; perms: string[] }> = (() => {
  const groups = new Map<string, string[]>();
  for (const perm of ROLE_PERMISSIONS) {
    const key = perm.split(".")[0];
    const perms = groups.get(key) ?? [];
    perms.push(perm);
    groups.set(key, perms);
  }
  return Array.from(groups, ([key, perms]) => ({ key, perms }));
})();

const PERMISSION_GROUP_LABELS: Record<string, () => string> = {
  cluster: () => t("roles.createRole.groups.cluster"),
  node: () => t("roles.createRole.groups.node"),
  process: () => t("roles.createRole.groups.process"),
  user: () => t("roles.createRole.groups.user"),
  role: () => t("roles.createRole.groups.role"),
  audit: () => t("roles.createRole.groups.audit"),
  command: () => t("roles.createRole.groups.command"),
  batch: () => t("roles.createRole.groups.batch"),
  alert: () => t("roles.createRole.groups.alert"),
  backup: () => t("roles.createRole.groups.backup"),
};

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
const permSearch = ref("");
const expandedRoles = ref(new Set<string>());
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

const totalRoles = computed(() => roles.value.length);
const totalBindings = computed(() => bindings.value.length);
const selectedPermCount = computed(() => selectedPerms.value.length);

const bindingCountByRole = computed(() => {
  const counts = new Map<string, number>();
  for (const binding of bindings.value) {
    counts.set(binding.roleId, (counts.get(binding.roleId) ?? 0) + 1);
  }
  return counts;
});

const filteredGroups = computed(() => {
  const term = permSearch.value.trim().toLocaleLowerCase();
  if (!term) {
    return PERMISSION_GROUPS;
  }
  return PERMISSION_GROUPS
    .map((group) => ({
      key: group.key,
      perms: group.perms.filter((perm) => perm.toLocaleLowerCase().includes(term)),
    }))
    .filter((group) => group.perms.length > 0);
});

const visiblePermCount = computed(() =>
  filteredGroups.value.reduce((sum, group) => sum + group.perms.length, 0),
);

function mutationMeta() {
  return { operationId: newOperationId(), operator: session.value?.username ?? "" };
}

function isBuiltin(roleId: string): boolean {
  return BUILTIN_ROLE_IDS.has(roleId);
}

function roleLabel(roleId: string): string {
  return roles.value.find((role) => role.roleId === roleId)?.name || roleId;
}

function roleBindingCount(roleId: string): number {
  return bindingCountByRole.value.get(roleId) ?? 0;
}

function visiblePerms(roleId: string, perms: string[]): string[] {
  return expandedRoles.value.has(roleId) ? perms : perms.slice(0, PERM_CHIP_LIMIT);
}

function hiddenPermCount(perms: string[]): number {
  return Math.max(0, perms.length - PERM_CHIP_LIMIT);
}

function isPermsExpanded(roleId: string): boolean {
  return expandedRoles.value.has(roleId);
}

function togglePerms(roleId: string): void {
  const next = new Set(expandedRoles.value);
  if (!next.delete(roleId)) {
    next.add(roleId);
  }
  expandedRoles.value = next;
}

function scopeKey(scopeType: string): string {
  return scopeType || "CLUSTER";
}

function scopeLabel(scopeType: string): string {
  const key = scopeKey(scopeType);
  if (key === "CLUSTER") return t("role.scopeCluster");
  if (key === "AGENT") return t("role.scopeAgent");
  if (key === "AGENT_GROUP") return t("role.scopeAgentGroup");
  if (key === "PROCESS_GROUP") return t("role.scopeProcessGroup");
  return key;
}

function groupLabel(key: string): string {
  return PERMISSION_GROUP_LABELS[key]?.() ?? key;
}

function togglePerm(perm: string, checked: boolean): void {
  selectedPerms.value = checked
    ? Array.from(new Set([...selectedPerms.value, perm]))
    : selectedPerms.value.filter((item) => item !== perm);
}

function selectAllVisible(): void {
  const next = new Set(selectedPerms.value);
  for (const group of filteredGroups.value) {
    for (const perm of group.perms) {
      next.add(perm);
    }
  }
  selectedPerms.value = ROLE_PERMISSIONS.filter((perm) => next.has(perm));
}

function clearPermSelection(): void {
  selectedPerms.value = [];
}

function retryLoad(): void {
  void query.refetch();
}

function notify(message: string, type: "success" | "error"): void {
  toastMessage.value = message;
  toastType.value = type;
  showToast.value = true;
}

function resetCreateForm(): void {
  roleName.value = "";
  selectedPerms.value = [];
  permSearch.value = "";
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

    <dl class="summary">
      <div class="summary-item" data-stat="roles">
        <dt class="summary-label">{{ t("roles.stats.roles") }}</dt>
        <dd class="summary-value">{{ totalRoles }}</dd>
      </div>
      <div class="summary-item" data-stat="bindings">
        <dt class="summary-label">{{ t("roles.stats.bindings") }}</dt>
        <dd class="summary-value">{{ totalBindings }}</dd>
      </div>
    </dl>

    <div v-if="errorText && !query.data.value" class="error-state" data-state="roles-error" role="alert">
      <AlertCircle :size="20" aria-hidden="true" />
      <div class="error-copy">
        <strong>{{ t("roles.error.title") }}</strong>
        <p>{{ errorText }}</p>
      </div>
      <button type="button" class="btn" data-action="retry-roles" @click="retryLoad">
        {{ t("roles.error.retry") }}
      </button>
    </div>

    <div v-else-if="query.isPending.value && !query.data.value" class="card">
      <div class="skeleton-table" data-testid="roles-skeleton" role="status" aria-busy="true">
        <span class="sr-only">{{ t("roles.loading") }}</span>
        <div v-for="row in SKELETON_ROWS" :key="row" class="skeleton-row"></div>
      </div>
    </div>

    <template v-else>
      <div class="card">
        <table v-if="roles.length" class="table">
          <caption class="sr-only">{{ t("roles.title") }}</caption>
          <thead>
            <tr>
              <th>{{ t("roles.table.name") }}</th>
              <th>{{ t("roles.table.type") }}</th>
              <th class="perms-column">{{ t("roles.table.permissions") }}</th>
              <th class="count-column">{{ t("roles.table.bindings") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="role in roles" :key="role.roleId" :data-role-id="role.roleId">
              <td>{{ role.name }}</td>
              <td data-cell="type">
                <span
                  class="badge type-badge"
                  :data-type="isBuiltin(role.roleId) ? 'builtin' : 'custom'"
                >
                  {{ isBuiltin(role.roleId) ? t("roles.type.builtin") : t("roles.type.custom") }}
                </span>
              </td>
              <td data-cell="permissions">
                <template v-if="role.permissions.length">
                  <ul :id="`role-perms-${role.roleId}`" class="perm-chips">
                    <li
                      v-for="perm in visiblePerms(role.roleId, role.permissions)"
                      :key="perm"
                      class="badge perm-chip"
                      data-perm-chip
                    >
                      {{ perm }}
                    </li>
                  </ul>
                  <button
                    v-if="hiddenPermCount(role.permissions) || isPermsExpanded(role.roleId)"
                    type="button"
                    class="btn btn-xs perm-toggle"
                    data-action="toggle-perms"
                    :aria-expanded="isPermsExpanded(role.roleId)"
                    :aria-controls="`role-perms-${role.roleId}`"
                    @click="togglePerms(role.roleId)"
                  >
                    {{ isPermsExpanded(role.roleId)
                      ? t("roles.perms.less")
                      : t("roles.perms.more", { count: hiddenPermCount(role.permissions) }) }}
                  </button>
                </template>
                <span v-else class="muted">—</span>
              </td>
              <td data-cell="bindings" class="count-cell">{{ roleBindingCount(role.roleId) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty" data-empty="roles">
          <ShieldCheck :size="28" aria-hidden="true" />
          <p>{{ t("roles.empty.title") }}</p>
          <p class="muted">{{ t("roles.empty.hint") }}</p>
          <button
            v-if="canManage"
            type="button"
            class="btn btn-primary"
            data-action="create-role"
            @click="openCreateDrawer"
          >
            <Plus :size="18" aria-hidden="true" />
            {{ t("roles.actions.createRole") }}
          </button>
        </div>
      </div>

      <div class="card bindings-card">
        <div class="card-header">
          <h2>{{ t("roles.bindings.title") }}</h2>
        </div>
        <table v-if="bindings.length" class="table">
          <caption class="sr-only">{{ t("roles.bindings.title") }}</caption>
          <thead>
            <tr>
              <th>{{ t("roles.bindings.table.userId") }}</th>
              <th>{{ t("roles.bindings.table.role") }}</th>
              <th>{{ t("roles.bindings.table.scope") }}</th>
              <th>{{ t("roles.bindings.table.scopeId") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(binding, index) in bindings"
              :key="`${binding.userId}:${binding.roleId}:${binding.scopeType}:${binding.scopeId}:${index}`"
            >
              <td class="mono">{{ binding.userId }}</td>
              <td>{{ roleLabel(binding.roleId) }}</td>
              <td data-cell="scope">
                <span class="badge scope-badge" :data-scope="scopeKey(binding.scopeType)">
                  {{ scopeLabel(binding.scopeType) }}
                </span>
              </td>
              <td>{{ binding.scopeId || "—" }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty" data-empty="bindings">
          <UserPlus :size="28" aria-hidden="true" />
          <p>{{ t("roles.bindings.empty.title") }}</p>
          <p class="muted">{{ t("roles.bindings.empty.hint") }}</p>
        </div>
      </div>
    </template>

    <Drawer :open="createDrawerOpen" :title="t('roles.createRole.title')" :close-label="t('actions.close')" size="wide" @close="createDrawerOpen = false">
      <form class="drawer-form" @submit.prevent="onCreate">
        <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
        <label class="field">
          <span>{{ t("roles.createRole.name") }} <span class="required" aria-hidden="true"></span></span>
          <input v-model="roleName" class="input" name="role_name" type="text" autocomplete="off" required />
        </label>
        <div class="perms-picker">
          <div class="perms-toolbar">
            <label class="field perms-search">
              <span>{{ t("roles.createRole.search") }}</span>
              <span class="search-input-wrap">
                <Search :size="16" aria-hidden="true" />
                <input
                  v-model="permSearch"
                  class="input search-input"
                  name="perm_search"
                  type="search"
                  :placeholder="t('roles.createRole.searchPlaceholder')"
                  autocomplete="off"
                />
              </span>
            </label>
            <div class="perms-toolbar-actions">
              <button
                type="button"
                class="btn btn-xs"
                data-action="perm-select-all"
                :disabled="acting || !visiblePermCount"
                @click="selectAllVisible"
              >
                {{ t("roles.createRole.selectAll") }}
              </button>
              <button
                type="button"
                class="btn btn-xs"
                data-action="perm-clear"
                :disabled="acting || !selectedPermCount"
                @click="clearPermSelection"
              >
                {{ t("roles.createRole.clearSelection") }}
              </button>
            </div>
          </div>
          <p class="perms-count" data-selected-count>
            {{ t("roles.createRole.selectedCount", { count: selectedPermCount }) }}
          </p>
          <p v-if="!filteredGroups.length" class="muted" data-no-perm-match>
            {{ t("roles.createRole.noPermissionMatch") }}
          </p>
          <fieldset v-for="group in filteredGroups" :key="group.key" class="perms-fieldset" :data-group="group.key">
            <legend>{{ groupLabel(group.key) }}</legend>
            <label v-for="perm in group.perms" :key="perm" class="check">
              <input type="checkbox" :name="`perm-${perm}`" :value="perm" :checked="selectedPerms.includes(perm)" @change="togglePerm(perm, ($event.target as HTMLInputElement).checked)" />
              {{ perm }}
            </label>
          </fieldset>
        </div>
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
.page-header, .header-actions, .drawer-actions, .perms-toolbar-actions { display: flex; align-items: center; gap: 0.75rem; }
.page-header { justify-content: space-between; }
.header-actions, .drawer-actions { flex-wrap: wrap; }
.btn { display: inline-flex; align-items: center; gap: 0.375rem; }
h1 { margin: 0; font-size: 1.35rem; font-weight: 650; }
.card { border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-card); overflow: auto; }
.card-header { padding: 1.25rem 1.25rem 0.5rem; }
h2 { margin: 0; font-size: 1.05rem; font-weight: 650; }
.muted { color: var(--color-muted); font-size: 0.875rem; }
.error { margin: 0; color: var(--color-danger); font-size: 0.875rem; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8rem; }
.field { display: flex; flex-direction: column; gap: 0.375rem; color: var(--color-muted); font-size: 0.875rem; }
.required::after { color: var(--color-danger); content: "*"; }
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

.summary { display: flex; flex-wrap: wrap; gap: 0.75rem; margin: 0; }
.summary-item {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
  min-width: 7rem;
  padding: 0.625rem 0.875rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
}
.summary-label {
  color: var(--color-muted);
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.025em;
}
.summary-value {
  margin: 0;
  font-size: 1.25rem;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
}

.error-state {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  padding: 1rem;
  border: 1px solid color-mix(in srgb, var(--color-danger) 30%, var(--color-border));
  border-radius: var(--radius-sm);
  background: var(--color-card);
}
.error-state > svg { flex-shrink: 0; margin-top: 0.125rem; color: var(--color-danger); }
.error-copy { display: flex; flex: 1; flex-direction: column; gap: 0.25rem; min-width: 0; }
.error-copy p { margin: 0; color: var(--color-muted); font-size: 0.875rem; overflow-wrap: anywhere; }

.skeleton-table { display: flex; flex-direction: column; gap: 0.75rem; padding: 1rem; }
.skeleton-row {
  height: 2.5rem;
  border-radius: 8px;
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
  animation: pulse 1.2s ease-in-out infinite;
}
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}

.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.5rem;
  padding: 2.5rem 1.5rem;
  text-align: center;
}
.empty > svg { color: var(--color-muted); }
.empty p { margin: 0; }
.empty p:first-of-type { color: var(--color-text); font-weight: 600; }
.empty .btn { margin-top: 0.5rem; }

.badge {
  border: 1px solid var(--color-border);
  background: color-mix(in srgb, var(--color-text) 4%, transparent);
}
.type-badge, .scope-badge { 
  font-weight: 500; 
}
.type-badge[data-type="builtin"] {
  border-color: color-mix(in srgb, var(--color-accent) 45%, transparent);
  background: color-mix(in srgb, var(--color-accent) 10%, transparent);
  color: color-mix(in srgb, var(--color-accent) 70%, var(--color-text));
}
/* custom 类型与 scope 用 info 蓝，与 builtin 的 accent 绿明确区分，蓝色沿用 Toast/图表中的信息色 */
.type-badge[data-type="custom"], .scope-badge {
  border-color: color-mix(in srgb, var(--color-info-fg) 30%, transparent);
  background: var(--color-info);
  color: var(--color-info-fg);
}

.perms-column { min-width: 18rem; }
.perm-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem;
  margin: 0;
  padding: 0;
  list-style: none;
}
.perm-chip {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-weight: 500;
  /* color: var(--color-muted); */
  overflow-wrap: anywhere;
}
.perm-toggle { margin-top: 0.25rem; }
.count-column, .count-cell { text-align: right; }
.count-cell { font-variant-numeric: tabular-nums; }

.perms-picker { display: flex; flex-direction: column; gap: 0.75rem; }
.perms-toolbar {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 0.75rem;
}
.perms-search { flex: 1 1 14rem; }
.search-input-wrap { position: relative; display: flex; align-items: center; }
.search-input-wrap > svg {
  position: absolute;
  left: 0.75rem;
  color: var(--color-muted);
  pointer-events: none;
}
.search-input { padding-left: 2.25rem; }
.perms-count { margin: 0; color: var(--color-muted); font-size: 0.8125rem; }
.perms-fieldset {
  margin: 0;
  padding: 0.75rem;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
  gap: 0.5rem 0.75rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.perms-fieldset legend {
  padding: 0 0.25rem;
  color: var(--color-text);
  font-size: 0.875rem;
  font-weight: 600;
}
.check { display: flex; align-items: center; gap: 0.375rem; min-width: 0; font-size: 0.8rem; overflow-wrap: anywhere; }
.drawer-form { display: flex; flex-direction: column; gap: 1rem; min-height: 100%; }
.drawer-actions { justify-content: flex-end; margin-top: auto; padding-top: 1rem; border-top: 1px solid var(--color-border); }

@media (max-width: 640px) {
  .page-header { align-items: flex-start; }
  .header-actions { justify-content: flex-end; }
  .header-actions .btn { min-height: 2.75rem; }
  .error-state { flex-wrap: wrap; }
  .perms-fieldset { grid-template-columns: 1fr; }
}

@media (prefers-reduced-motion: reduce) {
  .skeleton-row { animation: none; }
}
</style>
