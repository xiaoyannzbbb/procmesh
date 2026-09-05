<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Enum values and CLI syntax are protocol literals. */
import { Code, ConnectError } from "@connectrpc/connect";
import { Check, Clipboard, LoaderCircle, Plus, RefreshCw, ShieldAlert, Terminal } from "lucide-vue-next";
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import Drawer from "../components/Drawer.vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { appCode } from "../lib/connecterr";
import { newOperationId } from "../lib/opid";
import { useNodeClient } from "../lib/rpc/cluster";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";
import {
  buildCustomServerJoinTemplate,
  buildJoinCommand,
  parseJoinTokenParameters,
  type DurationUnit,
} from "./addNode";
import type { NodeView } from "./clusterView";

type JoinTokenResult = {
  tokenId: string;
  token: string;
  expiresUnix: bigint | number;
  uses: number;
  issuedTtlSeconds: bigint;
  issuedUses: number;
};

const props = defineProps<{
  open: boolean;
  nodes: NodeView[];
  nodesLoading: boolean;
  nodesError: string;
  canManage: boolean;
}>();

const emit = defineEmits<{
  close: [];
  refresh: [];
}>();

const { t, currentLanguage } = useI18n();
const client = useNodeClient();
const selectedNodeId = ref("");
const duration = ref("1");
const durationUnit = ref<DurationUnit>("hours");
const uses = ref("1");
const submitted = ref(false);
const creating = ref(false);
const createError = ref("");
const result = ref<JoinTokenResult | null>(null);
const closeConfirmOpen = ref(false);
const copyState = ref<"idle" | "copied" | "failed">("idle");
let copyTimer: ReturnType<typeof setTimeout> | null = null;
let lifecycleVersion = 0;
let routeLeaveResolve: ((allow: boolean) => void) | null = null;
const PERMISSION_DENIED_CODE = "DENIED";

const eligibleNodes = computed(() =>
  props.nodes.filter(
    (node) => node.state.toUpperCase() === "ALIVE" && node.apiAddress.trim().length > 0,
  ),
);
const selectedSeed = computed(() =>
  eligibleNodes.value.find((node) => node.nodeId === selectedNodeId.value),
);
const parameters = computed(() =>
  parseJoinTokenParameters(duration.value.trim(), durationUnit.value, uses.value.trim()),
);
const durationInvalid = computed(() => {
  if (!submitted.value) return false;
  return !parseJoinTokenParameters(duration.value.trim(), durationUnit.value, "1");
});
const usesInvalid = computed(() => {
  if (!submitted.value) return false;
  return !parseJoinTokenParameters("1", "seconds", uses.value.trim());
});
const canSubmit = computed(
  () => Boolean(props.canManage && selectedSeed.value && parameters.value && !creating.value),
);
const command = computed(() => {
  if (!result.value || !selectedSeed.value) return "";
  return buildJoinCommand(selectedSeed.value.apiAddress, result.value.token);
});
const customServerCommand = computed(() =>
  buildCustomServerJoinTemplate(selectedSeed.value?.apiAddress ?? "<SEED_API>"),
);
const parametersChanged = computed(() => {
  if (!result.value || !parameters.value) return false;
  return (
    parameters.value.ttlSeconds !== result.value.issuedTtlSeconds ||
    parameters.value.uses !== result.value.issuedUses
  );
});

function reset(): void {
  lifecycleVersion += 1;
  clearSensitiveResult();
  selectedNodeId.value = "";
  duration.value = "1";
  durationUnit.value = "hours";
  uses.value = "1";
  submitted.value = false;
  creating.value = false;
  createError.value = "";
  closeConfirmOpen.value = false;
}

function clearSensitiveResult(): void {
  result.value = null;
  copyState.value = "idle";
  if (copyTimer) {
    clearTimeout(copyTimer);
    copyTimer = null;
  }
}

function nodeLabel(node: NodeView): string {
  return `${node.hostname || node.nodeId} - ${node.apiAddress} [${node.freshness}]`;
}

function requestClose(): void {
  if (result.value || creating.value) {
    closeConfirmOpen.value = true;
    return;
  }
  emit("close");
}

function cancelClose(): void {
  closeConfirmOpen.value = false;
  const resolve = routeLeaveResolve;
  routeLeaveResolve = null;
  resolve?.(false);
}

function invalidateSensitiveLifecycle(): void {
  lifecycleVersion += 1;
  creating.value = false;
  clearSensitiveResult();
}

async function confirmClose(): Promise<void> {
  closeConfirmOpen.value = false;
  const resolve = routeLeaveResolve;
  routeLeaveResolve = null;
  invalidateSensitiveLifecycle();
  await nextTick();
  if (resolve) {
    resolve(true);
  } else {
    emit("close");
  }
}

function confirmRouteLeave(): Promise<boolean> {
  if (!props.open || (!result.value && !creating.value)) return Promise.resolve(true);
  if (routeLeaveResolve) routeLeaveResolve(false);
  closeConfirmOpen.value = true;
  return new Promise((resolve) => {
    routeLeaveResolve = resolve;
  });
}

function formatExpiry(value: bigint | number): string {
  const unix = typeof value === "bigint" ? Number(value) : value;
  if (!Number.isFinite(unix) || unix <= 0) return "-";
  return new Intl.DateTimeFormat(currentLanguage.value, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    timeZoneName: "short",
  }).format(new Date(unix * 1000));
}

function displayCreateError(error: unknown): string {
  if (error instanceof ConnectError && error.code === Code.PermissionDenied) {
    return t("nodes.add.permissionLost", { code: PERMISSION_DENIED_CODE });
  }
  if (appCode(error) === "TIMEOUT") {
    return t("nodes.add.createTimeout");
  }
  const detail = formatRemoteError(error);
  return t("nodes.add.createFailed", { detail });
}

async function generate(): Promise<void> {
  submitted.value = true;
  if (!canSubmit.value || !parameters.value || creating.value) return;

  const requestValues = parameters.value;
  const operationId = newOperationId();
  creating.value = true;
  const requestLifecycle = lifecycleVersion;
  createError.value = "";
  copyState.value = "idle";
  try {
    const response = await client.createJoinToken({
      meta: { operationId, operator: session.value?.username ?? "" },
      ttlSeconds: requestValues.ttlSeconds,
      uses: requestValues.uses,
    });
    if (requestLifecycle !== lifecycleVersion || !props.open) return;
    result.value = {
      tokenId: response.tokenId,
      token: response.token,
      expiresUnix: response.expiresUnix,
      uses: response.uses,
      issuedTtlSeconds: requestValues.ttlSeconds,
      issuedUses: requestValues.uses,
    };
  } catch (error) {
    if (requestLifecycle !== lifecycleVersion || !props.open) return;
    createError.value = displayCreateError(error);
  } finally {
    if (requestLifecycle === lifecycleVersion) creating.value = false;
  }
}

async function copyCommand(): Promise<void> {
  if (!command.value) return;
  try {
    if (!navigator.clipboard?.writeText) throw new Error("Clipboard API unavailable");
    await navigator.clipboard.writeText(command.value);
    copyState.value = "copied";
  } catch {
    copyState.value = "failed";
  }
  if (copyTimer) clearTimeout(copyTimer);
  copyTimer = setTimeout(() => {
    copyState.value = "idle";
    copyTimer = null;
  }, 4000);
}

watch(
  () => props.open,
  (open, wasOpen) => {
    if (open && !wasOpen) reset();
    if (!open) {
      lifecycleVersion += 1;
      creating.value = false;
      clearSensitiveResult();
    }
  },
  { immediate: true },
);

watch([selectedNodeId, duration, durationUnit, uses], () => {
  copyState.value = "idle";
  createError.value = "";
});

onUnmounted(() => {
  invalidateSensitiveLifecycle();
  const resolve = routeLeaveResolve;
  routeLeaveResolve = null;
  resolve?.(false);
});

defineExpose({ confirmRouteLeave });
</script>

<template>
  <Drawer
    :open="open"
    :title="t('nodes.add.title')"
    :close-label="t('nodes.add.close')"
    size="wide"
    @close="requestClose"
  >
    <form class="join-form" :aria-busy="creating" @submit.prevent="generate">
      <div class="intro" role="note">
        <Terminal :size="20" aria-hidden="true" />
        <p>{{ t("nodes.add.intro") }}</p>
      </div>

      <div v-if="nodesLoading && !nodes.length" class="state-message" aria-live="polite">
        <LoaderCircle class="spin" :size="18" aria-hidden="true" />
        <span>{{ t("nodes.add.nodesLoading") }}</span>
      </div>
      <div v-else-if="nodesError && !nodes.length" class="state-message danger" role="alert">
        <ShieldAlert :size="18" aria-hidden="true" />
        <span>{{ t("nodes.add.nodesFailed", { detail: nodesError }) }}</span>
        <button type="button" class="btn btn-sm" @click="emit('refresh')">
          <RefreshCw :size="15" aria-hidden="true" />
          {{ t("nodes.add.refresh") }}
        </button>
      </div>
      <div v-else-if="nodesError" class="state-message warning" role="status">
        <ShieldAlert :size="18" aria-hidden="true" />
        <span>{{ t("nodes.add.cachedWarning", { detail: nodesError }) }}</span>
        <button type="button" class="btn btn-sm" @click="emit('refresh')">
          <RefreshCw :size="15" aria-hidden="true" />
          {{ t("nodes.add.refresh") }}
        </button>
      </div>

      <label class="field">
        <span class="field-label">{{ t("nodes.add.seed") }}</span>
        <select v-model="selectedNodeId" name="seed" class="input" :disabled="creating || !eligibleNodes.length">
          <option value="">{{ t("nodes.add.selectSeed") }}</option>
          <option v-for="node in eligibleNodes" :key="node.nodeId" :value="node.nodeId">
            {{ nodeLabel(node) }}
          </option>
        </select>
      </label>
      <p
        v-if="!nodesLoading && (!nodesError || nodes.length > 0) && !eligibleNodes.length"
        class="field-message warning"
        role="status"
      >
        {{ t("nodes.add.noSeeds") }}
      </p>
      <p
        v-if="selectedSeed && selectedSeed.freshness !== 'LIVE'"
        class="field-message warning freshness-warning"
        role="status"
      >
        <FreshnessBadge :status="selectedSeed.freshness" />
        {{ t("nodes.add.freshnessWarning", { freshness: selectedSeed.freshness }) }}
      </p>
      <p v-if="result && !selectedSeed" class="field-message danger" role="alert">
        {{ t("nodes.add.seedInvalid") }}
      </p>

      <div class="parameter-grid">
        <label class="field duration-field">
          <span class="field-label">{{ t("nodes.add.duration") }}</span>
          <input
            v-model="duration"
            name="duration"
            class="input"
            inputmode="numeric"
            type="text"
            :disabled="creating"
            :aria-invalid="durationInvalid"
            :aria-describedby="durationInvalid ? 'join-duration-error' : 'join-duration-hint'"
            @blur="submitted = true"
          />
          <small id="join-duration-hint">{{ t("nodes.add.durationHint") }}</small>
          <small v-if="durationInvalid" id="join-duration-error" class="field-error" role="alert">
            {{ t("nodes.add.invalidDuration") }}
          </small>
        </label>
        <label class="field unit-field">
          <span class="field-label">{{ t("nodes.add.unit") }}</span>
          <select v-model="durationUnit" name="durationUnit" class="input" :disabled="creating">
            <option value="seconds">{{ t("nodes.add.units.seconds") }}</option>
            <option value="minutes">{{ t("nodes.add.units.minutes") }}</option>
            <option value="hours">{{ t("nodes.add.units.hours") }}</option>
            <option value="days">{{ t("nodes.add.units.days") }}</option>
          </select>
        </label>
        <label class="field uses-field">
          <span class="field-label">{{ t("nodes.add.uses") }}</span>
          <input
            v-model="uses"
            name="uses"
            class="input"
            inputmode="numeric"
            type="text"
            :disabled="creating"
            :aria-invalid="usesInvalid"
            :aria-describedby="usesInvalid ? 'join-uses-error' : 'join-uses-hint'"
            @blur="submitted = true"
          />
          <small id="join-uses-hint">{{ t("nodes.add.usesHint") }}</small>
          <small v-if="usesInvalid" id="join-uses-error" class="field-error" role="alert">
            {{ t("nodes.add.invalidUses") }}
          </small>
        </label>
      </div>

      <p v-if="!canManage" class="field-message danger" role="alert">
        {{ t("nodes.add.permissionLost", { code: PERMISSION_DENIED_CODE }) }}
      </p>
      <p v-if="parametersChanged" class="field-message warning" role="status">
        {{ t("nodes.add.parametersChanged") }}
      </p>
      <p v-if="createError" class="field-message danger" role="alert">{{ createError }}</p>

      <button type="submit" class="btn btn-primary generate-button" :disabled="!canSubmit" :aria-busy="creating">
        <LoaderCircle v-if="creating" class="spin" :size="17" aria-hidden="true" />
        <Plus v-else :size="17" aria-hidden="true" />
        {{ creating ? t("nodes.add.generating") : result ? t("nodes.add.regenerate") : t("nodes.add.generate") }}
      </button>

      <section v-if="result" class="result" aria-live="polite">
        <div class="result-heading">
          <Check :size="20" aria-hidden="true" />
          <strong>{{ t("nodes.add.executeOnNewNode") }}</strong>
        </div>
        <dl class="result-meta">
          <div><dt>{{ t("nodes.add.tokenId") }}</dt><dd class="mono">{{ result.tokenId }}</dd></div>
          <div><dt>{{ t("nodes.add.expires") }}</dt><dd>{{ formatExpiry(result.expiresUnix) }}</dd></div>
          <div><dt>{{ t("nodes.add.remainingUses") }}</dt><dd>{{ result.uses }}</dd></div>
        </dl>
        <p class="secret-warning"><ShieldAlert :size="17" aria-hidden="true" />{{ t("nodes.add.secretWarning") }}</p>
        <div class="command-block">
          <span class="field-label">{{ t("nodes.add.commandLabel") }}</span>
          <code tabindex="0">{{ command }}</code>
          <button type="button" class="btn copy-button" :disabled="!command" @click="copyCommand">
            <Check v-if="copyState === 'copied'" :size="16" aria-hidden="true" />
            <Clipboard v-else :size="16" aria-hidden="true" />
            {{ copyState === "copied" ? t("nodes.add.copied") : t("nodes.add.copy") }}
          </button>
          <p class="copy-status" :class="{ danger: copyState === 'failed' }" aria-live="polite">
            {{ copyState === "failed" ? t("nodes.add.copyFailed") : "" }}
          </p>
        </div>
        <div class="server-help">
          <strong>{{ t("nodes.add.customServerTitle") }}</strong>
          <code>{{ customServerCommand }}</code>
          <p>{{ t("nodes.add.customServerHint") }}</p>
        </div>
      </section>
    </form>
  </Drawer>

  <ConfirmDialog
    :open="closeConfirmOpen"
    :title="t('nodes.add.closeTitle')"
    :message="creating && !result ? t('nodes.add.closePendingMessage') : t('nodes.add.closeMessage')"
    :confirm-label="t('nodes.add.closeConfirm')"
    :cancel-label="t('nodes.add.cancel')"
    @cancel="cancelClose"
    @confirm="confirmClose"
  />
</template>

<style scoped>
.join-form { display: flex; flex-direction: column; gap: 1rem; }
.intro, .state-message, .secret-warning, .result-heading, .freshness-warning {
  display: flex; align-items: flex-start; gap: 0.6rem;
}
.intro { padding: 0.9rem; border-left: 3px solid var(--color-accent); background: color-mix(in srgb, var(--color-accent) 7%, var(--color-card)); }
.intro p, .state-message span, .secret-warning, .server-help p, .copy-status { margin: 0; line-height: 1.5; }
.state-message { flex-wrap: wrap; align-items: center; padding: 0.75rem; border: 1px solid var(--color-border); border-radius: 8px; }
.state-message span { flex: 1 1 14rem; }
.warning { color: var(--color-stale-fg); }
.danger, .field-error { color: var(--color-danger); }
.field { display: flex; flex-direction: column; gap: 0.35rem; min-width: 0; }
.field-label { color: var(--color-text); font-size: 0.8rem; font-weight: 650; }
.field small { color: var(--color-muted); line-height: 1.4; }
.field small.field-error { color: var(--color-danger); }
.parameter-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(8rem, 0.8fr) minmax(0, 1fr); gap: 0.75rem; align-items: start; }
.field-message { margin: -0.35rem 0 0; font-size: 0.875rem; line-height: 1.5; }
.generate-button { align-self: flex-start; min-height: 44px; }
.result { display: flex; flex-direction: column; gap: 1rem; padding-top: 1rem; border-top: 1px solid var(--color-border); }
.result-heading { align-items: center; color: var(--color-live-fg); }
.result-meta { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.75rem; margin: 0; }
.result-meta div { min-width: 0; padding: 0.75rem; border: 1px solid var(--color-border); border-radius: 8px; }
.result-meta dt { color: var(--color-muted); font-size: 0.75rem; }
.result-meta dd { margin: 0.25rem 0 0; overflow-wrap: anywhere; font-size: 0.875rem; }
.secret-warning { padding: 0.75rem; border: 1px solid var(--color-stale); border-radius: 8px; background: var(--color-stale); color: var(--color-stale-fg); }
.command-block, .server-help { display: flex; flex-direction: column; gap: 0.55rem; min-width: 0; }
code { display: block; max-width: 100%; padding: 0.75rem; overflow-x: auto; border: 1px solid var(--color-border); border-radius: 8px; background: color-mix(in srgb, var(--color-text) 4%, var(--color-card)); color: var(--color-text); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8rem; line-height: 1.55; overflow-wrap: anywhere; white-space: pre-wrap; }
.copy-button { align-self: flex-start; min-height: 44px; }
.copy-status { min-height: 1.4rem; font-size: 0.8rem; }
.server-help { padding: 0.85rem; border: 1px solid var(--color-border); border-radius: 8px; }
.server-help strong { font-size: 0.875rem; }
.server-help p { color: var(--color-muted); font-size: 0.825rem; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.spin { flex: 0 0 auto; animation: spin 800ms linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-width: 640px) {
  .parameter-grid, .result-meta { grid-template-columns: 1fr; }
  .generate-button, .copy-button { width: 100%; justify-content: center; }
}
@media (prefers-reduced-motion: reduce) { .spin { animation: none; } }
</style>
