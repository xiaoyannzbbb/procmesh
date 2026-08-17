<script setup lang="ts">
import { useQuery } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { Search, X } from "lucide-vue-next";
import { useUserClient } from "../lib/rpc";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "../pages/processView";

const props = defineProps<{
  modelValue?: string;
  disabled?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: string];
}>();

const { t } = useI18n();
const client = useUserClient();
const search = ref("");

const query = useQuery({
  queryKey: ["users"],
  queryFn: () => client.listUsers({}),
});

const users = computed(() => {
  const term = search.value.trim().toLocaleLowerCase();
  const list = query.data.value?.users ?? [];
  if (!term) {
    return list;
  }
  return list.filter((user) =>
    [user.username, user.displayName, user.email, user.userId].some((value) =>
      value.toLocaleLowerCase().includes(term),
    ),
  );
});

const selectedUser = computed(() => {
  return (query.data.value?.users ?? []).find((user) => user.userId === props.modelValue);
});

function selectUser(userId: string): void {
  emit("update:modelValue", userId);
}

function clearSelection(): void {
  emit("update:modelValue", "");
}
</script>

<template>
  <div class="user-selector">
    <div
      v-if="selectedUser"
      class="selected-user"
      :data-selected-user-id="selectedUser.userId"
    >
      <span class="user-copy">
        <span class="field-label">{{ t("roles.grant.selectedUser") }}</span>
        <strong>{{ selectedUser.displayName || selectedUser.username }}</strong>
        <span>{{ t("roles.grant.userMeta", { username: selectedUser.username, userId: selectedUser.userId }) }}</span>
      </span>
      <button
        type="button"
        class="clear-selection"
        :aria-label="t('roles.grant.clearSelection')"
        :disabled="disabled"
        @click="clearSelection"
      >
        <X :size="16" aria-hidden="true" />
      </button>
    </div>
    <label class="search-field">
      <span class="field-label">{{ t("roles.grant.searchUsers") }}</span>
      <span class="search-input-wrap">
        <Search :size="16" aria-hidden="true" />
        <input
          v-model="search"
          class="input search-input"
          name="user_search"
          type="search"
          :placeholder="t('roles.grant.searchPlaceholder')"
          :disabled="disabled || query.isPending.value"
          autocomplete="off"
        />
      </span>
    </label>

    <p v-if="query.isPending.value" class="selector-message">{{ t("users.loading") }}</p>
    <p v-else-if="query.error.value" class="selector-message error" role="alert">
      {{ formatRemoteError(query.error.value) }}
    </p>
    <fieldset v-else class="user-options">
      <legend class="sr-only">{{ t("roles.grant.selectUser") }}</legend>
      <label
        v-for="user in users"
        :key="user.userId"
        class="user-option"
        :class="{ selected: user.userId === modelValue }"
      >
        <input
          type="radio"
          name="user_id"
          :value="user.userId"
          :checked="user.userId === modelValue"
          :disabled="disabled"
          @change="selectUser(user.userId)"
        />
        <span class="user-copy">
          <strong>{{ user.displayName || user.username }}</strong>
          <span>{{ t("roles.grant.userMeta", { username: user.username, userId: user.userId }) }}</span>
        </span>
      </label>
      <p v-if="!users.length" class="selector-message">{{ t("roles.grant.noUsers") }}</p>
    </fieldset>
  </div>
</template>

<style scoped>
.user-selector,
.search-field,
.user-copy {
  display: flex;
  flex-direction: column;
}
.selected-user {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.75rem;
  border: 1px solid var(--color-accent);
  border-radius: var(--radius-sm);
  background: var(--color-hover);
}
.clear-selection {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  padding: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
}
.clear-selection:hover {
  background: var(--color-card);
  color: var(--color-text);
}
.clear-selection:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.clear-selection:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.user-selector,
.search-field {
  gap: 0.5rem;
}
.field-label {
  color: var(--color-muted);
  font-size: 0.875rem;
}
.search-input-wrap {
  position: relative;
  display: flex;
  align-items: center;
}
.search-input-wrap > svg {
  position: absolute;
  left: 0.75rem;
  color: var(--color-muted);
  pointer-events: none;
}
.search-input {
  width: 100%;
  padding-left: 2.25rem;
}
.user-options {
  max-height: 18rem;
  margin: 0;
  padding: 0.25rem;
  overflow-y: auto;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.user-option {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  padding: 0.75rem;
  border-radius: var(--radius-sm);
  cursor: pointer;
}
.user-option:hover,
.user-option.selected {
  background: var(--color-hover);
}
.user-option input {
  margin-top: 0.2rem;
}
.user-copy {
  min-width: 0;
  gap: 0.125rem;
  overflow-wrap: anywhere;
}
.user-copy strong {
  color: var(--color-text);
  font-size: 0.875rem;
}
.user-copy span,
.selector-message {
  color: var(--color-muted);
  font-size: 0.75rem;
}
.selector-message {
  margin: 0;
  padding: 0.75rem;
}
.error {
  color: var(--color-danger);
}
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
</style>
