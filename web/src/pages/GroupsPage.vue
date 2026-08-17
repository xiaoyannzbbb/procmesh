<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { Plus } from "lucide-vue-next";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import Drawer from "../components/Drawer.vue";
import NodeSelector from "../components/NodeSelector.vue";
import Toast from "../components/Toast.vue";
import { newOperationId } from "../lib/opid";
import { useGroupClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const { t } = useI18n();

const POLL_MS = 5000;
const client = useGroupClient();
const queryClient = useQueryClient();
const actionError = ref("");

const toastMessage = ref("");
const toastType = ref<"success" | "error" | "info" | "warning">("info");
const showToast = ref(false);

const isDrawerOpen = ref(false);
const name = ref("");
const description = ref("");
const addNodeId = ref<Record<string, string>>({});
const pendingDelete = ref<{ groupId: string; name: string } | null>(null);

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canManage = computed(() => perms.value.has("node.manage"));

// Always show cluster notice to inform users about the requirement
const showClusterNotice = computed(() => {
  // Show if user has manage permission and data is loaded
  return canManage.value && !query.isPending.value;
});

const query = useQuery({
  queryKey: ["groups"],
  queryFn: () => client.listAgentGroups({}),
  refetchInterval: POLL_MS,
});

const groups = computed(() => query.data.value?.groups ?? []);

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

const createReady = computed(() => name.value.trim().length > 0);

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function showToastNotification(message: string, type: "success" | "error" | "info" | "warning"): void {
  toastMessage.value = message;
  toastType.value = type;
  showToast.value = true;
}

const createMut = useMutation({
  mutationFn: () =>
    client.createAgentGroup({
      meta: mutationMeta(),
      name: name.value.trim(),
      description: description.value.trim(),
    }),
  onSuccess: async () => {
    name.value = "";
    description.value = "";
    isDrawerOpen.value = false;
    await queryClient.invalidateQueries({ queryKey: ["groups"] });
    showToastNotification(t("group.createSuccess", { name: name.value }), "success");
  },
  onError: (err: unknown) => {
    const errorMsg = formatRemoteError(err);
    showToastNotification(errorMsg, "error");
    actionError.value = errorMsg;
  },
});

const deleteMut = useMutation({
  mutationFn: (groupId: string) =>
    client.deleteAgentGroup({
      meta: mutationMeta(),
      groupId,
    }),
  onSuccess: () => {
    queryClient.invalidateQueries({ queryKey: ["groups"] });
    showToastNotification(t("group.deleteSuccess"), "success");
  },
  onError: (err: unknown) => {
    showToastNotification(formatRemoteError(err), "error");
  },
});

const addMemberMut = useMutation({
  mutationFn: (args: { groupId: string; nodeId: string }) =>
    client.addAgentGroupMember({
      meta: mutationMeta(),
      groupId: args.groupId,
      nodeId: args.nodeId,
    }),
  onSuccess: (_, variables) => {
    addNodeId.value = { ...addNodeId.value, [variables.groupId]: "" };
    queryClient.invalidateQueries({ queryKey: ["groups"] });
    showToastNotification(t("group.addMemberSuccess"), "success");
  },
  onError: (err: unknown) => {
    showToastNotification(formatRemoteError(err), "error");
  },
});

const removeMemberMut = useMutation({
  mutationFn: (args: { groupId: string; nodeId: string }) =>
    client.removeAgentGroupMember({
      meta: mutationMeta(),
      groupId: args.groupId,
      nodeId: args.nodeId,
    }),
  onSuccess: () => {
    queryClient.invalidateQueries({ queryKey: ["groups"] });
    showToastNotification(t("group.removeMemberSuccess"), "success");
  },
  onError: (err: unknown) => {
    showToastNotification(formatRemoteError(err), "error");
  },
});

function openDrawer(): void {
  if (!canManage.value) {
    return;
  }
  actionError.value = "";
  name.value = "";
  description.value = "";
  isDrawerOpen.value = true;
}

function closeDrawer(): void {
  isDrawerOpen.value = false;
}

const acting = computed(
  () =>
    createMut.isPending.value ||
    deleteMut.isPending.value ||
    addMemberMut.isPending.value ||
    removeMemberMut.isPending.value,
);

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

function requestDelete(groupId: string, groupName: string): void {
  if (!canManage.value || !groupId || acting.value) {
    return;
  }
  pendingDelete.value = { groupId, name: groupName };
}

function cancelDelete(): void {
  if (!deleteMut.isPending.value) {
    pendingDelete.value = null;
  }
}

async function confirmDelete(): Promise<void> {
  const target = pendingDelete.value;
  if (!canManage.value || !target || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await deleteMut.mutateAsync(target.groupId);
    pendingDelete.value = null;
  } catch {
    // onError already recorded
  }
}

function rowNodeId(groupId: string): string {
  return (addNodeId.value[groupId] ?? "").trim();
}

async function onAddMember(groupId: string): Promise<void> {
  const nodeId = rowNodeId(groupId);
  if (!canManage.value || !groupId || !nodeId || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await addMemberMut.mutateAsync({ groupId, nodeId });
  } catch {
    // onError already recorded
  }
}

async function onRemoveMember(groupId: string, nodeId: string): Promise<void> {
  if (!canManage.value || !groupId || !nodeId || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await removeMemberMut.mutateAsync({ groupId, nodeId });
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h1>{{ t("group.title") }}</h1>
      <button v-if="canManage" type="button" class="btn btn-primary" @click="openDrawer">
        <Plus :size="18" />
        {{ t("group.createGroup") }}
      </button>
    </div>

    <div v-if="showClusterNotice" class="notice-banner">
      <div class="notice-icon">ℹ️</div>
      <div class="notice-content" v-html="t('group.clusterRequiredNotice')"></div>
    </div>

    <p v-if="query.isPending && !query.data" class="muted">{{ t("group.loading") }}</p>
    <p v-else-if="errorText && !query.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("group.name") }}</th>
              <th>{{ t("group.description") }}</th>
              <th>{{ t("group.members") }}</th>
              <th v-if="canManage"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="group in groups" :key="group.groupId || group.name">
              <td>{{ group.name }}</td>
              <td>{{ group.description || "—" }}</td>
              <td>
                <div v-if="group.memberNodeIds?.length" class="members-list">
                  <span v-for="nodeId in group.memberNodeIds" :key="nodeId" class="member-tag">
                    {{ nodeId }}
                    <button
                      v-if="canManage"
                      type="button"
                      class="member-remove"
                      :disabled="acting"
                      :aria-label="`Remove ${nodeId}`"
                      @click="onRemoveMember(group.groupId, nodeId)"
                    >
                      ×
                    </button>
                  </span>
                </div>
                <span v-else class="muted">—</span>
              </td>
              <td v-if="canManage">
                <div class="row-actions">
                  <NodeSelector
                    v-model="addNodeId[group.groupId]"
                    :exclude-node-ids="group.memberNodeIds"
                    :placeholder="t('group.selectNodeToAdd')"
                    :disabled="acting"
                  />
                  <button
                    type="button"
                    class="btn btn-sm"
                    :disabled="acting || !rowNodeId(group.groupId)"
                    @click="onAddMember(group.groupId)"
                  >
                    {{ t("group.addMember") }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-sm btn-danger"
                    :disabled="acting"
                    @click="requestDelete(group.groupId, group.name)"
                  >
                    {{ t("group.delete") }}
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!groups.length">
              <td :colspan="canManage ? 4 : 3" class="muted">{{ t("group.noGroups") }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <Drawer :open="isDrawerOpen" :title="t('group.createGroup')" @close="closeDrawer">
      <form class="drawer-form" @submit.prevent="onCreate">
        <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
        <label class="field">
          <span class="field-label">{{ t("group.name") }} <span class="required">*</span></span>
          <input v-model="name" class="input" name="name" type="text" autocomplete="off" required />
        </label>
        <label class="field">
          <span class="field-label">{{ t("group.description") }}</span>
          <input v-model="description" class="input" name="description" type="text" />
        </label>
        <div class="drawer-actions">
          <button type="button" class="btn" :disabled="acting" @click="closeDrawer">{{ t("actions.cancel") }}</button>
          <button class="btn btn-primary" type="submit" :disabled="!createReady || acting">
            {{ t("group.create") }}
          </button>
        </div>
      </form>
    </Drawer>

    <ConfirmDialog
      :open="Boolean(pendingDelete)"
      :title="t('group.deleteConfirmTitle')"
      :message="t('group.deleteConfirmMessage', { name: pendingDelete?.name ?? '' })"
      :confirm-label="t('group.confirmDelete')"
      :cancel-label="t('actions.cancel')"
      :pending="deleteMut.isPending.value"
      @cancel="cancelDelete"
      @confirm="confirmDelete"
    />

    <Toast
      :show="showToast"
      :message="toastMessage"
      :type="toastType"
      @close="showToast = false"
    />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}

h1 {
  margin: 0;
  font-size: 1.35rem;
  font-weight: 650;
}

.btn {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
}

.notice-banner {
  display: flex;
  gap: 0.75rem;
  padding: 0.875rem 1rem;
  background: rgba(56, 189, 248, 0.1);
  border: 1px solid rgba(56, 189, 248, 0.3);
  border-radius: var(--radius-sm);
  font-size: 0.875rem;
  line-height: 1.5;
}

.notice-icon {
  flex-shrink: 0;
  font-size: 1.25rem;
}

.notice-content {
  color: var(--color-text);
}

.notice-content :deep(code) {
  padding: 0.125rem 0.375rem;
  background: rgba(0, 0, 0, 0.1);
  border-radius: 3px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8125rem;
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
  border-radius: var(--radius-sm);
  background: var(--color-card);
  overflow: auto;
}

.members-list {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}

.member-tag {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.25rem 0.5rem;
  background: var(--color-hover);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  font-size: 0.75rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.member-remove {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 1rem;
  height: 1rem;
  padding: 0;
  border: none;
  border-radius: 2px;
  background: transparent;
  color: var(--color-muted);
  font-size: 1rem;
  line-height: 1;
  cursor: pointer;
  transition: all 150ms;
}

.member-remove:hover:not(:disabled) {
  background: var(--color-danger);
  color: white;
}

.member-remove:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.row-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}

.btn-sm {
  padding: 0.375rem 0.75rem;
  font-size: 0.8125rem;
}

.drawer-form {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.field-label {
  font-size: 0.875rem;
  font-weight: 500;
  color: var(--color-text);
}

.required {
  color: var(--color-danger);
}

.drawer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.75rem;
  padding-top: 0.5rem;
  margin-top: auto;
}

@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
  }

  .row-actions {
    flex-direction: column;
    align-items: stretch;
  }

  .notice-banner {
    font-size: 0.8125rem;
  }
}
</style>
