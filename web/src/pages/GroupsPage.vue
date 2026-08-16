<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
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

const name = ref("");
const description = ref("");
const memberNodeId = ref<Record<string, string>>({});

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canManage = computed(() => perms.value.has("node.manage"));

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

function formatMembers(ids: string[] | undefined): string {
  if (!ids?.length) {
    return "—";
  }
  return ids.join(", ");
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
    await queryClient.invalidateQueries({ queryKey: ["groups"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const deleteMut = useMutation({
  mutationFn: (groupId: string) =>
    client.deleteAgentGroup({
      meta: mutationMeta(),
      groupId,
    }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ["groups"] }),
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const addMemberMut = useMutation({
  mutationFn: (args: { groupId: string; nodeId: string }) =>
    client.addAgentGroupMember({
      meta: mutationMeta(),
      groupId: args.groupId,
      nodeId: args.nodeId,
    }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ["groups"] }),
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const removeMemberMut = useMutation({
  mutationFn: (args: { groupId: string; nodeId: string }) =>
    client.removeAgentGroupMember({
      meta: mutationMeta(),
      groupId: args.groupId,
      nodeId: args.nodeId,
    }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ["groups"] }),
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

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

async function onDelete(groupId: string): Promise<void> {
  if (!canManage.value || !groupId || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await deleteMut.mutateAsync(groupId);
  } catch {
    // onError already recorded
  }
}

function rowNodeId(groupId: string): string {
  return (memberNodeId.value[groupId] ?? "").trim();
}

async function onAddMember(groupId: string): Promise<void> {
  const nodeId = rowNodeId(groupId);
  if (!canManage.value || !groupId || !nodeId || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await addMemberMut.mutateAsync({ groupId, nodeId });
    memberNodeId.value = { ...memberNodeId.value, [groupId]: "" };
  } catch {
    // onError already recorded
  }
}

async function onRemoveMember(groupId: string): Promise<void> {
  const nodeId = rowNodeId(groupId);
  if (!canManage.value || !groupId || !nodeId || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await removeMemberMut.mutateAsync({ groupId, nodeId });
    memberNodeId.value = { ...memberNodeId.value, [groupId]: "" };
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page">
    <h1>{{ t("group.title") }}</h1>
    <p v-if="query.isPending && !query.data" class="muted">{{ t("group.loading") }}</p>
    <p v-else-if="errorText && !query.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("group.name") }}</th>
              <th>{{ t("group.members") }}</th>
              <th v-if="canManage"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="group in groups" :key="group.groupId || group.name">
              <td>{{ group.name }}</td>
              <td>{{ formatMembers(group.memberNodeIds) }}</td>
              <td v-if="canManage">
                <div class="row-actions">
                  <input
                    v-model="memberNodeId[group.groupId]"
                    class="input"
                    :name="`node_id-${group.groupId}`"
                    type="text"
                    :placeholder="t('group.nodeId')"
                  />
                  <button
                    type="button"
                    class="btn"
                    :disabled="acting || !rowNodeId(group.groupId)"
                    @click="onAddMember(group.groupId)"
                  >
                    {{ t("group.addMember") }}
                  </button>
                  <button
                    type="button"
                    class="btn"
                    :disabled="acting || !rowNodeId(group.groupId)"
                    @click="onRemoveMember(group.groupId)"
                  >
                    {{ t("group.removeMember") }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-danger"
                    :disabled="acting"
                    @click="onDelete(group.groupId)"
                  >
                    {{ t("group.delete") }}
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!groups.length">
              <td :colspan="canManage ? 3 : 2" class="muted">{{ t("group.noGroups") }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <form v-if="canManage" class="card create-group" @submit.prevent="onCreate">
        <h2>{{ t("group.create") }}</h2>
        <label class="field">
          {{ t("group.name") }}
          <input v-model="name" class="input" name="name" type="text" autocomplete="off" />
        </label>
        <label class="field">
          {{ t("group.description") }}
          <input v-model="description" class="input" name="description" type="text" />
        </label>
        <button class="btn btn-primary" type="submit" :disabled="!createReady || acting">{{ t("group.create") }}</button>
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
  border-radius: var(--radius-sm);
  background: var(--color-card);
  overflow: auto;
}
.create-group {
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
.row-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}
.row-actions .input {
  width: 10rem;
  min-width: 8rem;
}
</style>
