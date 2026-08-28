<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Template refs, field keys, and label targets are non-visible implementation values; visible copy uses t(). */
import { useMutation } from "@tanstack/vue-query";
import { computed, nextTick, ref } from "vue";
import { Eye, EyeOff, KeyRound, LoaderCircle, UserRound } from "lucide-vue-next";
import Toast from "../components/Toast.vue";
import { newOperationId } from "../lib/opid";
import { session, useAuthClient } from "../lib/session";
import { useErrorHandler } from "../lib/useErrorHandler";
import { useI18n } from "../lib/useI18n";

const MIN_PASSWORD = 10;

type PasswordField = "current" | "new" | "confirm";
type FormErrors = Record<PasswordField, string>;

const { t } = useI18n();
const { formatError } = useErrorHandler();
const client = useAuthClient();

const currentPassword = ref("");
const newPassword = ref("");
const confirmPassword = ref("");
const visiblePasswords = ref<Record<PasswordField, boolean>>({ current: false, new: false, confirm: false });
const formErrors = ref<FormErrors>({ current: "", new: "", confirm: "" });
const apiError = ref("");
const showToast = ref(false);
const currentInput = ref<HTMLInputElement | null>(null);
const newInput = ref<HTMLInputElement | null>(null);
const confirmInput = ref<HTMLInputElement | null>(null);

const username = computed(() => session.value?.username ?? "");
const userId = computed(() => session.value?.userId ?? "");
const avatarInitial = computed(() => username.value.trim().charAt(0).toUpperCase() || "?");
const canSubmit = computed(() => Boolean(
  currentPassword.value && newPassword.value && confirmPassword.value && !changePassword.isPending.value,
));

function mutationMeta() {
  return { operationId: newOperationId(), operator: username.value };
}

function clearFieldError(field: PasswordField): void {
  formErrors.value[field] = "";
  apiError.value = "";
}

function togglePassword(field: PasswordField): void {
  visiblePasswords.value[field] = !visiblePasswords.value[field];
}

function resetForm(): void {
  currentPassword.value = "";
  newPassword.value = "";
  confirmPassword.value = "";
  formErrors.value = { current: "", new: "", confirm: "" };
  apiError.value = "";
}

async function focusFirstError(): Promise<void> {
  await nextTick();
  if (formErrors.value.current) currentInput.value?.focus();
  else if (formErrors.value.new) newInput.value?.focus();
  else if (formErrors.value.confirm) confirmInput.value?.focus();
}

function validate(): boolean {
  const errors: FormErrors = { current: "", new: "", confirm: "" };
  if (!currentPassword.value) errors.current = t("profile.currentPasswordRequired");
  if (newPassword.value.length < MIN_PASSWORD) {
    errors.new = t("profile.passwordTooShort");
  } else if (newPassword.value === currentPassword.value) {
    errors.new = t("profile.passwordSame");
  }
  if (confirmPassword.value !== newPassword.value) errors.confirm = t("profile.passwordMismatch");
  formErrors.value = errors;
  return !Object.values(errors).some(Boolean);
}

const changePassword = useMutation({
  mutationFn: () => client.changePassword({
    meta: mutationMeta(),
    currentPassword: currentPassword.value,
    newPassword: newPassword.value,
  }),
  onSuccess: () => {
    resetForm();
    showToast.value = true;
  },
  onError: (error: unknown) => {
    apiError.value = formatError(error);
    void nextTick(() => currentInput.value?.focus());
  },
});

async function onSubmit(): Promise<void> {
  apiError.value = "";
  if (!validate()) {
    await focusFirstError();
    return;
  }
  try {
    await changePassword.mutateAsync();
  } catch {
    // Mutation callbacks surface the localized error.
  }
}
</script>

<template>
  <div class="profile-page page">
    <header class="page-heading">
      <div>
        <h1>{{ t("profile.title") }}</h1>
        <p>{{ t("profile.subtitle") }}</p>
      </div>
    </header>

    <section class="profile-section identity-section" aria-labelledby="identity-heading">
      <div class="section-heading">
        <span class="section-icon"><UserRound :size="20" aria-hidden="true" /></span>
        <div>
          <h2 id="identity-heading">{{ t("profile.identity") }}</h2>
          <p>{{ t("profile.identityDescription") }}</p>
        </div>
      </div>
      <div class="identity-content">
        <span class="profile-avatar" aria-hidden="true">{{ avatarInitial }}</span>
        <dl class="identity-list">
          <div>
            <dt>{{ t("profile.username") }}</dt>
            <dd>{{ username }}</dd>
          </div>
          <div>
            <dt>{{ t("profile.userId") }}</dt>
            <dd>{{ userId }}</dd>
          </div>
        </dl>
      </div>
    </section>

    <section class="profile-section" aria-labelledby="security-heading">
      <div class="section-heading">
        <span class="section-icon"><KeyRound :size="20" aria-hidden="true" /></span>
        <div>
          <h2 id="security-heading">{{ t("profile.passwordTitle") }}</h2>
          <p>{{ t("profile.passwordDescription") }}</p>
        </div>
      </div>

      <form class="password-form" novalidate @submit.prevent="onSubmit">
        <p v-if="apiError" class="form-alert" role="alert">{{ apiError }}</p>

        <div class="password-field">
          <label for="current-password">{{ t("profile.currentPassword") }}</label>
          <span class="password-input-wrap">
            <input
              id="current-password"
              ref="currentInput"
              v-model="currentPassword"
              class="input"
              name="current_password"
              :type="visiblePasswords.current ? 'text' : 'password'"
              autocomplete="current-password"
              required
              :aria-invalid="Boolean(formErrors.current)"
              :aria-describedby="formErrors.current ? 'current-password-error' : undefined"
              @input="clearFieldError('current')"
            />
            <button
              type="button"
              class="password-toggle"
              data-toggle-password="current"
              :aria-label="t(visiblePasswords.current ? 'actions.hidePassword' : 'actions.showPassword')"
              @click="togglePassword('current')"
            >
              <EyeOff v-if="visiblePasswords.current" :size="19" aria-hidden="true" />
              <Eye v-else :size="19" aria-hidden="true" />
            </button>
          </span>
          <small v-if="formErrors.current" id="current-password-error" class="field-error" role="alert">{{ formErrors.current }}</small>
        </div>

        <div class="password-field">
          <label for="new-password">{{ t("profile.newPassword") }}</label>
          <span class="password-input-wrap">
            <input
              id="new-password"
              ref="newInput"
              v-model="newPassword"
              class="input"
              name="new_password"
              :type="visiblePasswords.new ? 'text' : 'password'"
              autocomplete="new-password"
              :minlength="MIN_PASSWORD"
              required
              :aria-invalid="Boolean(formErrors.new)"
              aria-describedby="new-password-hint new-password-error"
              @input="clearFieldError('new')"
            />
            <button
              type="button"
              class="password-toggle"
              data-toggle-password="new"
              :aria-label="t(visiblePasswords.new ? 'actions.hidePassword' : 'actions.showPassword')"
              @click="togglePassword('new')"
            >
              <EyeOff v-if="visiblePasswords.new" :size="19" aria-hidden="true" />
              <Eye v-else :size="19" aria-hidden="true" />
            </button>
          </span>
          <small id="new-password-hint" class="field-hint">{{ t("profile.passwordHint") }}</small>
          <small v-if="formErrors.new" id="new-password-error" class="field-error" role="alert">{{ formErrors.new }}</small>
        </div>

        <div class="password-field">
          <label for="confirm-password">{{ t("profile.confirmPassword") }}</label>
          <span class="password-input-wrap">
            <input
              id="confirm-password"
              ref="confirmInput"
              v-model="confirmPassword"
              class="input"
              name="confirm_password"
              :type="visiblePasswords.confirm ? 'text' : 'password'"
              autocomplete="new-password"
              required
              :aria-invalid="Boolean(formErrors.confirm)"
              :aria-describedby="formErrors.confirm ? 'confirm-password-error' : undefined"
              @input="clearFieldError('confirm')"
            />
            <button
              type="button"
              class="password-toggle"
              data-toggle-password="confirm"
              :aria-label="t(visiblePasswords.confirm ? 'actions.hidePassword' : 'actions.showPassword')"
              @click="togglePassword('confirm')"
            >
              <EyeOff v-if="visiblePasswords.confirm" :size="19" aria-hidden="true" />
              <Eye v-else :size="19" aria-hidden="true" />
            </button>
          </span>
          <small v-if="formErrors.confirm" id="confirm-password-error" class="field-error" role="alert">{{ formErrors.confirm }}</small>
        </div>

        <div class="form-actions">
          <button type="submit" class="btn btn-primary" :disabled="!canSubmit">
            <LoaderCircle v-if="changePassword.isPending.value" :size="18" class="spinner" aria-hidden="true" />
            {{ changePassword.isPending.value ? t("actions.saving") : t("actions.save") }}
          </button>
        </div>
      </form>
    </section>

    <Toast
      :show="showToast"
      :message="t('profile.passwordChanged')"
      type="success"
      @close="showToast = false"
    />
  </div>
</template>

<style scoped>
.profile-page {
  display: flex;
  max-width: 52rem;
  flex-direction: column;
  gap: 1.5rem;
}

.page-heading h1,
.section-heading h2,
.page-heading p,
.section-heading p {
  margin: 0;
}

.page-heading h1 {
  font-size: 1.5rem;
  font-weight: 700;
}

.page-heading p,
.section-heading p {
  margin-top: 0.375rem;
  color: var(--color-muted);
  font-size: 0.875rem;
  line-height: 1.5;
}

.profile-section {
  border-top: 1px solid var(--color-border);
  padding-top: 1.5rem;
}

.section-heading {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
}

.section-heading h2 {
  font-size: 1rem;
  font-weight: 650;
}

.section-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2.5rem;
  height: 2.5rem;
  flex: 0 0 2.5rem;
  border-radius: 8px;
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
}

.identity-content {
  display: flex;
  align-items: center;
  gap: 1.25rem;
  margin-top: 1.25rem;
  padding-left: 3.25rem;
}

.profile-avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 4rem;
  height: 4rem;
  flex: 0 0 4rem;
  border-radius: 8px;
  background: var(--color-accent);
  color: white;
  font-size: 1.375rem;
  font-weight: 700;
}

.identity-list {
  display: grid;
  min-width: 0;
  flex: 1;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem 2rem;
  margin: 0;
}

.identity-list div {
  min-width: 0;
}

.identity-list dt {
  color: var(--color-muted);
  font-size: 0.75rem;
}

.identity-list dd {
  margin: 0.25rem 0 0;
  overflow-wrap: anywhere;
  font-size: 0.9375rem;
  font-weight: 600;
}

.password-form {
  display: grid;
  max-width: 32rem;
  gap: 1rem;
  margin-top: 1.25rem;
  padding-left: 3.25rem;
}

.password-field {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  color: var(--color-text);
  font-size: 0.875rem;
  font-weight: 550;
}

.password-input-wrap {
  position: relative;
  display: block;
}

.password-input-wrap .input {
  padding-right: 3.25rem;
}

.password-toggle {
  position: absolute;
  top: 0;
  right: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2.75rem;
  height: 2.75rem;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
}

.password-toggle:hover {
  color: var(--color-text);
}

.field-hint,
.field-error {
  font-size: 0.75rem;
  font-weight: 400;
  line-height: 1.45;
}

.field-hint {
  color: var(--color-muted);
}

.field-error,
.form-alert {
  color: var(--color-danger);
}

.form-alert {
  margin: 0;
  padding: 0.75rem;
  border: 1px solid color-mix(in srgb, var(--color-danger) 35%, var(--color-border));
  border-radius: 6px;
  background: color-mix(in srgb, var(--color-danger) 7%, var(--color-card));
  font-size: 0.875rem;
  line-height: 1.5;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
  padding-top: 0.25rem;
}

.spinner {
  animation: spin 0.9s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

@media (max-width: 640px) {
  .profile-page { gap: 1.25rem; }
  .identity-content,
  .password-form { padding-left: 0; }
  .identity-list { grid-template-columns: 1fr; }
  .identity-content { align-items: flex-start; }
  .form-actions .btn { width: 100%; }
}

@media (prefers-reduced-motion: reduce) {
  .spinner { animation: none; }
}
</style>
