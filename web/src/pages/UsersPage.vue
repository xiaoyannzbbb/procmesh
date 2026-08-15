<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { newOperationId } from "../lib/opid";
import { useUserClient } from "../lib/rpc";
import { session } from "../lib/session";
import { formatRemoteError } from "./processView";

const MIN_PASSWORD = 10;
const POLL_MS = 5000;
const client = useUserClient();
const queryClient = useQueryClient();
const actionError = ref("");

const username = ref("");
const password = ref("");
const displayName = ref("");
const email = ref("");

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canCreate = computed(() => perms.value.has("user.create"));
const canUpdate = computed(() => perms.value.has("user.update"));

const query = useQuery({
  queryKey: ["users"],
  queryFn: () => client.listUsers({}),
  refetchInterval: POLL_MS,
});

const users = computed(() => query.data.value?.users ?? []);

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

const createReady = computed(
  () => username.value.trim().length > 0 && password.value.length >= MIN_PASSWORD,
);

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function formatLastLogin(unix: bigint | number | undefined): string {
  const n = Number(unix ?? 0);
  if (!Number.isFinite(n) || n <= 0) {
    return "—";
  }
  return new Date(n * 1000).toISOString();
}

const createMut = useMutation({
  mutationFn: () =>
    client.createUser({
      meta: mutationMeta(),
      username: username.value.trim(),
      password: password.value,
      displayName: displayName.value.trim(),
      email: email.value.trim(),
    }),
  onSuccess: async () => {
    username.value = "";
    password.value = "";
    displayName.value = "";
    email.value = "";
    await queryClient.invalidateQueries({ queryKey: ["users"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const disableMut = useMutation({
  mutationFn: (userId: string) =>
    client.disableUser({
      meta: mutationMeta(),
      userId,
    }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ["users"] }),
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const acting = computed(() => createMut.isPending.value || disableMut.isPending.value);

async function onCreate(): Promise<void> {
  if (!canCreate.value || !createReady.value || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await createMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onDisable(userId: string): Promise<void> {
  if (!canUpdate.value || !userId || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await disableMut.mutateAsync(userId);
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page">
    <h1>Users</h1>
    <p v-if="query.isPending && !query.data" class="muted">Loading…</p>
    <p v-else-if="errorText && !query.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>Username</th>
              <th>Display</th>
              <th>Email</th>
              <th>Status</th>
              <th>Last login</th>
              <th v-if="canUpdate"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="user in users" :key="user.userId || user.username">
              <td>{{ user.username }}</td>
              <td>{{ user.displayName || "—" }}</td>
              <td>{{ user.email || "—" }}</td>
              <td>{{ user.status || "—" }}</td>
              <td>{{ formatLastLogin(user.lastLoginUnix) }}</td>
              <td v-if="canUpdate">
                <button
                  v-if="user.status !== 'DISABLED'"
                  type="button"
                  class="btn btn-danger"
                  :disabled="acting"
                  @click="onDisable(user.userId)"
                >
                  Disable
                </button>
              </td>
            </tr>
            <tr v-if="!users.length">
              <td :colspan="canUpdate ? 6 : 5" class="muted">No users</td>
            </tr>
          </tbody>
        </table>
      </div>

      <form v-if="canCreate" class="card create-user" @submit.prevent="onCreate">
        <h2>Create</h2>
        <label class="field">
          Username
          <input v-model="username" class="input" name="username" type="text" autocomplete="off" />
        </label>
        <label class="field">
          Password
          <input v-model="password" class="input" name="password" type="password" autocomplete="new-password" />
        </label>
        <label class="field">
          Display
          <input v-model="displayName" class="input" name="display_name" type="text" />
        </label>
        <label class="field">
          Email
          <input v-model="email" class="input" name="email" type="email" />
        </label>
        <button class="btn btn-primary" type="submit" :disabled="!createReady || acting">Create</button>
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
.create-user {
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
</style>
