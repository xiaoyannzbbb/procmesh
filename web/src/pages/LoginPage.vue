<script setup lang="ts">
import { ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { saveCsrf, useAuthClient } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { useErrorHandler } from "../lib/useErrorHandler";
import BrandMark from "../components/BrandMark.vue";

const { t } = useI18n();
const { formatError } = useErrorHandler();
const username = ref("");
const password = ref("");
const error = ref("");
const pending = ref(false);
const router = useRouter();
const route = useRoute();
const client = useAuthClient();

function nextPath(): string {
  const raw = route.query.next;
  if (typeof raw !== "string" || !raw.startsWith("/") || raw.startsWith("//")) {
    return "/";
  }
  return raw;
}

async function onSubmit(): Promise<void> {
  if (!username.value.trim() || password.value === "") {
    return;
  }
  error.value = "";
  pending.value = true;
  try {
    const resp = await client.login({
      username: username.value.trim(),
      password: password.value,
    });
    saveCsrf(resp.csrfToken);
    await router.replace(nextPath());
  } catch (err) {
    error.value = formatError(err);
  } finally {
    pending.value = false;
  }
}
</script>

<template>
  <div class="login-page">
    <form class="login-card" @submit.prevent="onSubmit">
      <div class="login-brand">
        <BrandMark :size="32" />
        <h1>{{ t("login.title") }}</h1>
      </div>
      <label class="field">
        {{ t("login.username") }}
        <input v-model="username" class="input" name="username" type="text" autocomplete="username" />
      </label>
      <label class="field">
        {{ t("login.password") }}
        <input v-model="password" class="input" name="password" type="password" autocomplete="current-password" />
      </label>
      <p v-if="error" class="login-error" role="alert">{{ error }}</p>
      <button class="btn btn-primary" type="submit" :disabled="pending">{{ t("actions.signIn") }}</button>
    </form>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1.5rem;
  background: var(--color-bg);
}
.login-card {
  width: 100%;
  max-width: 360px;
  display: flex;
  flex-direction: column;
  gap: 0.875rem;
  padding: 1.75rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
}
.login-brand {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 0.25rem;
}

.login-card h1 {
  margin: 0;
  font-size: 1.25rem;
  font-weight: 600;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  font-size: 0.875rem;
  color: var(--color-muted);
}
.login-error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
}
</style>
