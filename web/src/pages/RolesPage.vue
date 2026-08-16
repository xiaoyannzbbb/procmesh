<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { newOperationId } from "../lib/opid";
import { useRoleClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const { t } = useI18n();

const POLL_MS = 5000;
const BUILTIN_ROLE_IDS = new Set(["super_admin", "cluster_admin", "operator", "viewer"]);

/** V1.0 permissions from internal/auth/perm.go; excludes batch.execute and alert.*. */
const ROLE_PERMISSIONS = [
  "cluster.read",
  "cluster.manage",
  "node.read",
  "node.manage",
  "node.remove",
  "process.read",
  "process.create",
  "process.update",
  "process.delete",
  "process.start",
  "process.stop",
  "process.restart",
  "process.config.read",
  "process.config.update",
  "process.logs.read",
  "process.logs.download",
  "user.read",
  "user.create",
  "user.update",
  "user.delete",
  "role.read",
  "role.manage",
  "audit.read",
  "command.execute",
  "command.execute.batch",
] as const;

const client = useRoleClient();
const queryClient = useQueryClient();
const actionError = ref("");

const roleName = ref("");
const selectedPerms = ref<string[]>([]);
const grantUserId = ref("");
const grantRoleId = ref("");
const grantScope = ref<"CLUSTER" | "AGENT">("CLUSTER");
const grantScopeId = ref("");

const canManage = computed(() => (session.value?.permissions ?? []).includes("role.manage"));

const query = useQuery({
  queryKey: ["roles"],
  queryFn: () => client.listRoles({}),
  refetchInterval: POLL_MS,
});

const roles = computed(() => query.data.value?.roles ?? []);
const bindings = computed(() => query.data.value?.bindings ?? []);

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  const err = query.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

const createReady = computed(() => roleName.value.trim().length > 0);
const grantReady = computed(() => {
  if (!grantUserId.value.trim() || !grantRoleId.value) {
    return false;
  }
  if (grantScope.value === "AGENT" && !grantScopeId.value.trim()) {
    return false;
  }
  return true;
});

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function isBuiltin(roleId: string): boolean {
  return BUILTIN_ROLE_IDS.has(roleId);
}

function roleLabel(roleId: string): string {
  return roles.value.find((r) => r.roleId === roleId)?.name || roleId;
}

function togglePerm(perm: string, checked: boolean): void {
  if (checked) {
    if (!selectedPerms.value.includes(perm)) {
      selectedPerms.value = [...selectedPerms.value, perm];
    }
    return;
  }
  selectedPerms.value = selectedPerms.value.filter((p) => p !== perm);
}

const createMut = useMutation({
  mutationFn: () =>
    client.createRole({
      meta: mutationMeta(),
      name: roleName.value.trim(),
      permissions: [...selectedPerms.value],
    }),
  onSuccess: async () => {
    roleName.value = "";
    selectedPerms.value = [];
    await queryClient.invalidateQueries({ queryKey: ["roles"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const grantMut = useMutation({
  mutationFn: () =>
    client.grantRole({
      meta: mutationMeta(),
      userId: grantUserId.value.trim(),
      roleId: grantRoleId.value,
      scopeType: grantScope.value,
      scopeId: grantScopeId.value.trim(),
    }),
  onSuccess: async () => {
    grantUserId.value = "";
    grantRoleId.value = "";
    grantScope.value = "CLUSTER";
    grantScopeId.value = "";
    await queryClient.invalidateQueries({ queryKey: ["roles"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const acting = computed(() => createMut.isPending.value || grantMut.isPending.value);

async function onCreate(): Promise<void> {
  if (!canManage.value || !createReady.value || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await createMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onGrant(): Promise<void> {
  if (!canManage.value || !grantReady.value || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await grantMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page">
    <h1>{{ t("roles.title") }}</h1>
    <p v-if="query.isPending && !query.data" class="muted">{{ t("roles.loading") }}</p>
    <p v-else-if="errorText && !query.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("roles.table.name") }}</th>
              <th>{{ t("roles.table.type") }}</th>
              <th>{{ t("roles.table.permissions") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="role in roles" :key="role.roleId">
              <td>{{ role.name }}</td>
              <td>{{ isBuiltin(role.roleId) ? t("roles.type.builtin") : t("roles.type.custom") }}</td>
              <td class="perms">{{ role.permissions.length ? role.permissions.join(", ") : "—" }}</td>
            </tr>
            <tr v-if="!roles.length">
              <td colspan="3" class="muted">{{ t("roles.noRoles") }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="card">
        <h2>{{ t("roles.bindings.title") }}</h2>
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("roles.bindings.table.userId") }}</th>
              <th>{{ t("roles.bindings.table.role") }}</th>
              <th>{{ t("roles.bindings.table.scope") }}</th>
              <th>{{ t("roles.bindings.table.scopeId") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(b, i) in bindings" :key="`${b.userId}:${b.roleId}:${b.scopeType}:${b.scopeId}:${i}`">
              <td class="mono">{{ b.userId }}</td>
              <td>{{ roleLabel(b.roleId) }}</td>
              <td>{{ b.scopeType || "CLUSTER" }}</td>
              <td>{{ b.scopeId || "—" }}</td>
            </tr>
            <tr v-if="!bindings.length">
              <td colspan="4" class="muted">{{ t("roles.bindings.noBindings") }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <form v-if="canManage" class="card form-card" @submit.prevent="onCreate">
        <h2>{{ t("roles.createRole.title") }}</h2>
        <label class="field">
          {{ t("roles.createRole.name") }}
          <input v-model="roleName" class="input" name="role_name" type="text" />
        </label>
        <fieldset class="perms-fieldset">
          <legend>{{ t("roles.createRole.permissions") }}</legend>
          <label v-for="perm in ROLE_PERMISSIONS" :key="perm" class="check">
            <input
              type="checkbox"
              :name="`perm-${perm}`"
              :value="perm"
              :checked="selectedPerms.includes(perm)"
              @change="togglePerm(perm, ($event.target as HTMLInputElement).checked)"
            />
            {{ perm }}
          </label>
        </fieldset>
        <button class="btn btn-primary" type="submit" :disabled="!createReady || acting">{{ t("roles.createRole.create") }}</button>
      </form>

      <form v-if="canManage" class="card form-card" @submit.prevent="onGrant">
        <h2>{{ t("roles.grant.title") }}</h2>
        <label class="field">
          {{ t("roles.grant.userId") }}
          <input v-model="grantUserId" class="input" name="user_id" type="text" />
        </label>
        <label class="field">
          {{ t("roles.grant.role") }}
          <select v-model="grantRoleId" class="input" name="role_id">
            <option value="">{{ t("roles.grant.selectRole") }}</option>
            <option v-for="role in roles" :key="role.roleId" :value="role.roleId">{{ role.name }}</option>
          </select>
        </label>
        <label class="field">
          {{ t("roles.grant.scope") }}
          <select v-model="grantScope" class="input" name="scope_type">
            <option value="CLUSTER">CLUSTER</option>
            <option value="AGENT">AGENT</option>
          </select>
        </label>
        <label class="field">
          {{ t("roles.grant.scopeId") }}
          <input v-model="grantScopeId" class="input" name="scope_id" type="text" :placeholder="grantScope === 'AGENT' ? t('roles.grant.scopeIdPlaceholder.required') : t('roles.grant.scopeIdPlaceholder.optional')" />
        </label>
        <button class="btn btn-primary" type="submit" :disabled="!grantReady || acting">{{ t("roles.grant.grant") }}</button>
      </form>
    </template>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
h1 {
  margin: 0;
  font-size: 1.35rem;
  font-weight: 650;
}
h2 {
  margin: 0 0 0.75rem;
  font-size: 1.05rem;
  font-weight: 650;
}
.muted {
  color: var(--color-muted);
  font-size: 0.875rem;
}
.error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-card);
  overflow: auto;
}
.form-card {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 1.25rem;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  font-size: 0.875rem;
  color: var(--color-muted);
}
.perms {
  font-size: 0.8rem;
  word-break: break-word;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
.perms-fieldset {
  border: 1px solid var(--color-border);
  border-radius: 8px;
  padding: 0.75rem;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 0.375rem 0.75rem;
}
.perms-fieldset legend {
  font-size: 0.875rem;
  color: var(--color-muted);
  padding: 0 0.25rem;
}
.check {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  font-size: 0.8rem;
}
</style>
