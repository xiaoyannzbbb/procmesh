<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { Layers, LoaderCircle, Play, Plus, RotateCw, Square } from "lucide-vue-next";
import { computed, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import BatchTargetPicker from "../components/BatchTargetPicker.vue";
import Drawer from "../components/Drawer.vue";
import Toast from "../components/Toast.vue";
import { newOperationId } from "../lib/opid";
import { useBatchClient } from "../lib/rpc/batch";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

type BatchType = "START" | "STOP" | "RESTART";
type SelectorKind = "processIds" | "processGroup" | "agentGroupId";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();

const POLL_MS = 5000;
const client = useBatchClient();
const queryClient = useQueryClient();
const actionError = ref("");

const createDrawerOpen = ref(false);
const selectorTouched = ref(false);
const createType = ref<BatchType>("START");
const selectorKind = ref<SelectorKind>("processIds");
const selectedProcessIds = ref<string[]>([]);
const selectedProcessGroup = ref("");
const selectedAgentGroupId = ref("");

const toastMessage = ref("");
const toastType = ref<"success" | "error" | "info" | "warning">("success");
const showToast = ref(false);

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canExecute = computed(() => perms.value.has("batch.execute"));

const batchId = computed(() => {
  const id = route.params.id;
  return typeof id === "string" ? id : "";
});
const isDetail = computed(() => batchId.value.length > 0);

const listQuery = useQuery({
  queryKey: ["batches"],
  queryFn: () => client.listBatches({}),
  refetchInterval: POLL_MS,
});

const detailQuery = useQuery({
  queryKey: ["batches", batchId],
  queryFn: () => client.getBatch({ batchId: batchId.value }),
  enabled: isDetail,
  refetchInterval: POLL_MS,
});

const batches = computed(() => listQuery.data.value?.batches ?? []);

const detail = computed(() => {
  const fromGet = detailQuery.data.value?.batch;
  if (fromGet) {
    return fromGet;
  }
  if (!batchId.value) {
    return undefined;
  }
  return batches.value.find((b) => b.batchId === batchId.value);
});

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  const err = isDetail.value ? detailQuery.error.value : listQuery.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

const listPending = computed(() => listQuery.isPending.value && !listQuery.data.value);
const detailPending = computed(() => isDetail.value && detailQuery.isPending.value && !detail.value);

const createReady = computed(() => {
  if (selectorKind.value === "processIds") {
    return selectedProcessIds.value.length > 0;
  }
  if (selectorKind.value === "processGroup") {
    return selectedProcessGroup.value.trim().length > 0;
  }
  return selectedAgentGroupId.value.trim().length > 0;
});

const selectorError = computed(() => {
  if (!selectorTouched.value || createReady.value) {
    return "";
  }
  return t("batch.selectorValueRequired");
});

const typeOptions = computed(() => [
  { value: "START" as const, label: t("batch.typeStart"), hint: t("batch.typeStartHint"), icon: Play },
  { value: "STOP" as const, label: t("batch.typeStop"), hint: t("batch.typeStopHint"), icon: Square },
  { value: "RESTART" as const, label: t("batch.typeRestart"), hint: t("batch.typeRestartHint"), icon: RotateCw },
]);

const selectorOptions = computed(() => [
  { value: "processIds" as const, label: t("batch.selectorProcessIds"), hint: t("batch.selectorProcessIdsHint") },
  { value: "processGroup" as const, label: t("batch.selectorProcessGroup"), hint: t("batch.selectorProcessGroupHint") },
  { value: "agentGroupId" as const, label: t("batch.selectorAgentGroup"), hint: t("batch.selectorAgentGroupHint") },
]);

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function buildSelector() {
  if (selectorKind.value === "processGroup") {
    return { processGroup: selectedProcessGroup.value.trim() };
  }
  if (selectorKind.value === "agentGroupId") {
    return { agentGroupId: selectedAgentGroupId.value.trim() };
  }
  return { processIds: [...selectedProcessIds.value] };
}

function resetTargetSelection(): void {
  selectedProcessIds.value = [];
  selectedProcessGroup.value = "";
  selectedAgentGroupId.value = "";
  selectorTouched.value = false;
}

function showToastNotification(message: string, type: "success" | "error" | "info" | "warning"): void {
  toastMessage.value = message;
  toastType.value = type;
  showToast.value = true;
}

function openCreateDrawer(): void {
  if (!canExecute.value) {
    return;
  }
  actionError.value = "";
  createType.value = "START";
  selectorKind.value = "processIds";
  resetTargetSelection();
  createDrawerOpen.value = true;
}

function closeCreateDrawer(): void {
  if (createMut.isPending.value) {
    return;
  }
  createDrawerOpen.value = false;
}

function selectType(value: BatchType): void {
  createType.value = value;
}

function selectSelectorKind(value: SelectorKind): void {
  selectorKind.value = value;
  selectorTouched.value = false;
}

function formatCreated(ms: bigint | number | undefined): string {
  const n = typeof ms === "bigint" ? Number(ms) : (ms ?? 0);
  if (!Number.isFinite(n) || n <= 0) {
    return "—";
  }
  return new Date(n).toISOString();
}

function statusClass(status: string): string {
  const s = (status || "").toUpperCase();
  if (s === "TIMEOUT") {
    return "status-timeout";
  }
  if (s === "SUCCESS" || s === "COMPLETED") {
    return "status-success";
  }
  if (s === "PARTIAL") {
    return "status-partial";
  }
  if (s === "PENDING" || s === "RUNNING") {
    return "status-pending";
  }
  if (s === "FAILED" || s === "DENIED" || s === "CONFLICT" || s === "UNAVAILABLE" || s === "INVALID") {
    return `status-${s.toLowerCase()}`;
  }
  return "status-unknown";
}

function summaryOf(batch: { summary?: { success?: number; failed?: number; timeout?: number; denied?: number } } | undefined) {
  return {
    success: batch?.summary?.success ?? 0,
    failed: batch?.summary?.failed ?? 0,
    timeout: batch?.summary?.timeout ?? 0,
    denied: batch?.summary?.denied ?? 0,
  };
}

const createMut = useMutation({
  mutationFn: () =>
    client.createBatch({
      meta: mutationMeta(),
      type: createType.value,
      selector: buildSelector(),
    }),
  onSuccess: async (resp) => {
    resetTargetSelection();
    createDrawerOpen.value = false;
    await queryClient.invalidateQueries({ queryKey: ["batches"] });
    showToastNotification(t("batch.createSuccess"), "success");
    const id = resp.batch?.batchId;
    if (id) {
      await router.push(`/batches/${encodeURIComponent(id)}`);
    }
  },
  onError: (err: unknown) => {
    const errorMsg = formatRemoteError(err);
    actionError.value = errorMsg;
    showToastNotification(errorMsg, "error");
  },
});

const retryMut = useMutation({
  mutationFn: () =>
    client.retryFailed({
      meta: mutationMeta(),
      batchId: batchId.value,
    }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ["batches"] }),
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const replayMut = useMutation({
  mutationFn: () =>
    client.replayTimeout({
      meta: mutationMeta(),
      batchId: batchId.value,
    }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ["batches"] }),
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const exportMut = useMutation({
  mutationFn: () => client.exportBatch({ batchId: batchId.value, format: "json" }),
  onSuccess: (resp) => {
    const blob = new Blob([new Uint8Array(resp.content)], {
      type: resp.contentType || "application/json",
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = resp.filename || `${batchId.value}.json`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const acting = computed(
  () =>
    createMut.isPending.value ||
    retryMut.isPending.value ||
    replayMut.isPending.value ||
    exportMut.isPending.value,
);

async function onCreate(): Promise<void> {
  selectorTouched.value = true;
  if (!canExecute.value || !createReady.value || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await createMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onRetry(): Promise<void> {
  if (!canExecute.value || !batchId.value || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await retryMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onReplay(): Promise<void> {
  if (!canExecute.value || !batchId.value || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await replayMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onExport(): Promise<void> {
  if (!batchId.value || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await exportMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page">
    <template v-if="isDetail">
      <RouterLink class="back" to="/batches">{{ t("batch.back") }}</RouterLink>
      <h1>{{ t("batch.title") }}</h1>
      <p v-if="detailPending" class="muted">{{ t("batch.loading") }}</p>
      <p v-else-if="errorText && !detail" class="error" role="alert">{{ errorText }}</p>
      <template v-else-if="detail">
        <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
        <section class="card facts-card">
          <dl class="facts">
            <div>
              <dt>{{ t("batch.batchId") }}</dt>
              <dd>{{ detail.batchId }}</dd>
            </div>
            <div>
              <dt>{{ t("batch.type") }}</dt>
              <dd>{{ detail.type }}</dd>
            </div>
            <div>
              <dt>{{ t("batch.status") }}</dt>
              <dd>
                <span
                  class="status-badge"
                  :class="statusClass(detail.status)"
                  :data-status="detail.status"
                >{{ detail.status }}</span>
              </dd>
            </div>
            <div>
              <dt>{{ t("batch.created") }}</dt>
              <dd>{{ formatCreated(detail.createdUnixMs) }}</dd>
            </div>
          </dl>
          <div class="counts">
            <span>{{ t("batch.success") }} {{ summaryOf(detail).success }}</span>
            <span>{{ t("batch.failed") }} {{ summaryOf(detail).failed }}</span>
            <span
              class="status-timeout"
              data-status="TIMEOUT"
            >{{ t("batch.timeout") }} {{ summaryOf(detail).timeout }}</span>
            <span>{{ t("batch.denied") }} {{ summaryOf(detail).denied }}</span>
          </div>
          <div class="actions">
            <button type="button" class="btn" :disabled="acting" @click="onRetry">
              {{ t("batch.retryFailed") }}
            </button>
            <button type="button" class="btn" :disabled="acting" @click="onReplay">
              {{ t("batch.replayTimeout") }}
            </button>
            <button type="button" class="btn" :disabled="acting" @click="onExport">
              {{ t("batch.export") }}
            </button>
          </div>
        </section>
        <section class="card">
          <h2>{{ t("batch.targets") }}</h2>
          <table class="table">
            <thead>
              <tr>
                <th>{{ t("batch.processId") }}</th>
                <th>{{ t("batch.processName") }}</th>
                <th>{{ t("batch.nodeId") }}</th>
                <th>{{ t("batch.status") }}</th>
                <th>{{ t("batch.error") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="target in detail.targets" :key="target.operationId || target.processId">
                <td>{{ target.processId }}</td>
                <td>{{ target.processName || "—" }}</td>
                <td>{{ target.nodeId || "—" }}</td>
                <td>
                  <span
                    class="status-badge"
                    :class="statusClass(target.status)"
                    :data-status="target.status"
                  >{{ target.status }}</span>
                </td>
                <td>{{ target.error || "—" }}</td>
              </tr>
            </tbody>
          </table>
        </section>
      </template>
    </template>

    <template v-else>
      <div class="page-header">
        <h1>{{ t("batch.title") }}</h1>
        <button
          v-if="canExecute"
          type="button"
          class="btn btn-primary"
          data-action="create-batch"
          @click="openCreateDrawer"
        >
          <Plus :size="18" aria-hidden="true" />
          {{ t("batch.create") }}
        </button>
      </div>
      <div class="banner" role="status">{{ t("batch.localOnly") }}</div>
      <p v-if="listPending" class="muted">{{ t("batch.loading") }}</p>
      <p v-else-if="errorText && !listQuery.data" class="error" role="alert">{{ errorText }}</p>
      <template v-else>
        <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
        <div v-if="!batches.length" class="empty-state" role="status">
          <Layers :size="28" aria-hidden="true" />
          <strong>{{ t("batch.emptyTitle") }}</strong>
          <span>{{ t("batch.emptyHint") }}</span>
          <button
            v-if="canExecute"
            type="button"
            class="btn btn-primary"
            data-action="create-batch-empty"
            @click="openCreateDrawer"
          >
            <Plus :size="18" aria-hidden="true" />
            {{ t("batch.create") }}
          </button>
        </div>
        <div v-else class="card">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t("batch.batchId") }}</th>
                <th>{{ t("batch.type") }}</th>
                <th>{{ t("batch.status") }}</th>
                <th>{{ t("batch.success") }}</th>
                <th>{{ t("batch.failed") }}</th>
                <th>{{ t("batch.timeout") }}</th>
                <th>{{ t("batch.created") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="batch in batches" :key="batch.batchId">
                <td>
                  <RouterLink :to="`/batches/${encodeURIComponent(batch.batchId)}`">{{ batch.batchId }}</RouterLink>
                </td>
                <td>{{ batch.type }}</td>
                <td>
                  <span
                    class="status-badge"
                    :class="statusClass(batch.status)"
                    :data-status="batch.status"
                  >{{ batch.status }}</span>
                </td>
                <td>{{ summaryOf(batch).success }}</td>
                <td>{{ summaryOf(batch).failed }}</td>
                <td>
                  <span
                    class="status-timeout"
                    data-status="TIMEOUT"
                  >{{ summaryOf(batch).timeout }}</span>
                </td>
                <td>{{ formatCreated(batch.createdUnixMs) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <Drawer
        :open="createDrawerOpen"
        :title="t('batch.create')"
        :close-label="t('actions.close')"
        @close="closeCreateDrawer"
      >
        <form class="drawer-form create-batch" @submit.prevent="onCreate">
          <p class="drawer-lead">{{ t("batch.createDescription") }}</p>
          <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>

          <fieldset class="field">
            <legend>
              {{ t("batch.type") }}
              <span class="required" aria-hidden="true">{{ t("batch.requiredMarker") }}</span>
            </legend>
            <p id="batch-type-hint" class="field-hint">{{ t("batch.typeHint") }}</p>
            <div class="choice-grid type-grid" role="radiogroup" :aria-label="t('batch.type')" aria-describedby="batch-type-hint">
              <button
                v-for="opt in typeOptions"
                :key="opt.value"
                type="button"
                class="choice-card"
                :class="{ selected: createType === opt.value, danger: opt.value === 'STOP' }"
                role="radio"
                :aria-checked="createType === opt.value"
                :name="`type-${opt.value}`"
                @click="selectType(opt.value)"
              >
                <component :is="opt.icon" :size="18" aria-hidden="true" />
                <span class="choice-copy">
                  <span class="choice-title">{{ opt.label }}</span>
                  <span class="choice-hint">{{ opt.hint }}</span>
                </span>
              </button>
            </div>
            <p v-if="createType === 'STOP'" class="field-warning" role="status">{{ t("batch.typeStopWarning") }}</p>
          </fieldset>

          <fieldset class="field">
            <legend>
              {{ t("batch.selector") }}
              <span class="required" aria-hidden="true">{{ t("batch.requiredMarker") }}</span>
            </legend>
            <p id="batch-selector-hint" class="field-hint">{{ t("batch.selectorHint") }}</p>
            <div class="choice-grid" role="radiogroup" :aria-label="t('batch.selector')" aria-describedby="batch-selector-hint">
              <button
                v-for="opt in selectorOptions"
                :key="opt.value"
                type="button"
                class="choice-card compact"
                :class="{ selected: selectorKind === opt.value }"
                role="radio"
                :aria-checked="selectorKind === opt.value"
                :name="`selectorKind-${opt.value}`"
                @click="selectSelectorKind(opt.value)"
              >
                <span class="choice-title">{{ opt.label }}</span>
              </button>
            </div>
          </fieldset>

          <div class="field">
            <span>
              {{ t("batch.selectorValue") }}
              <span class="required" aria-hidden="true">{{ t("batch.requiredMarker") }}</span>
            </span>
            <BatchTargetPicker
              :kind="selectorKind"
              :process-ids="selectedProcessIds"
              :process-group="selectedProcessGroup"
              :agent-group-id="selectedAgentGroupId"
              :active="createDrawerOpen"
              :disabled="acting"
              @update:process-ids="selectedProcessIds = $event"
              @update:process-group="selectedProcessGroup = $event"
              @update:agent-group-id="selectedAgentGroupId = $event"
            />
            <small v-if="selectorError" id="selector-error" class="field-error" role="alert">{{ selectorError }}</small>
          </div>

          <p class="cli-hint">{{ t("batch.configUpdateCli") }}</p>

          <div class="drawer-actions">
            <button type="button" class="btn" :disabled="acting" @click="closeCreateDrawer">
              {{ t("actions.cancel") }}
            </button>
            <button
              class="btn btn-primary"
              type="submit"
              :disabled="!createReady || acting"
              :aria-busy="createMut.isPending.value"
            >
              <LoaderCircle v-if="createMut.isPending.value" class="spin" :size="17" aria-hidden="true" />
              {{ createMut.isPending.value ? t("batch.creating") : t("batch.createSubmit") }}
            </button>
          </div>
        </form>
      </Drawer>
    </template>

    <Toast :show="showToast" :message="toastMessage" :type="toastType" @close="showToast = false" />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.page-header,
.drawer-actions {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.page-header {
  justify-content: space-between;
}
.drawer-actions {
  flex-wrap: wrap;
  justify-content: flex-end;
  margin-top: auto;
  padding-top: 1rem;
  border-top: 1px solid var(--color-border);
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
.back {
  font-size: 0.875rem;
  color: var(--color-muted);
  text-decoration: none;
  width: fit-content;
}
.back:hover {
  color: var(--color-text);
}
.muted {
  color: var(--color-muted);
  font-size: 0.875rem;
}
.error,
.field-error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
}
.field-error {
  font-size: 0.75rem;
}
.banner {
  border-radius: 10px;
  padding: 0.75rem 1rem;
  font-size: 0.875rem;
  line-height: 1.4;
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  overflow: auto;
}
.facts-card {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  padding: 1.25rem;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 0.75rem 1.25rem;
  margin: 0;
}
.facts dt {
  font-size: 0.75rem;
  color: var(--color-muted);
}
.facts dd {
  margin: 0.2rem 0 0;
  font-size: 0.95rem;
  font-weight: 550;
}
.counts {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem 1.25rem;
  font-size: 0.875rem;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.5rem;
  padding: 2.5rem 1.5rem;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  text-align: center;
  color: var(--color-muted);
}
.empty-state strong {
  color: var(--color-text);
  font-size: 1rem;
}
.empty-state span {
  max-width: 28rem;
  font-size: 0.875rem;
  line-height: 1.5;
}
.empty-state .btn {
  margin-top: 0.5rem;
}
.drawer-form {
  display: flex;
  flex-direction: column;
  gap: 1.125rem;
  min-height: 100%;
}
.drawer-lead {
  margin: 0;
  color: var(--color-muted);
  font-size: 0.875rem;
  line-height: 1.5;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  margin: 0;
  padding: 0;
  border: none;
  min-width: 0;
  font-size: 0.875rem;
  color: var(--color-muted);
}
.field legend,
.field > span {
  color: var(--color-text);
  font-weight: 550;
}
.required {
  color: var(--color-danger);
}
.field-hint {
  margin: 0;
  font-size: 0.75rem;
  line-height: 1.45;
  color: var(--color-muted);
}
.field-warning {
  margin: 0;
  padding: 0.625rem 0.75rem;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--color-stale) 70%, transparent);
  color: var(--color-stale-fg);
  font-size: 0.75rem;
  line-height: 1.45;
}
.choice-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.5rem;
}
.choice-card {
  display: flex;
  flex-direction: row;
  align-items: flex-start;
  gap: 0.5rem;
  min-height: 44px;
  padding: 0.625rem 0.75rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  color: var(--color-text);
  text-align: left;
  cursor: pointer;
  transition: border-color 150ms, background 150ms, box-shadow 150ms;
}
.choice-card.compact {
  align-items: center;
  justify-content: center;
  padding: 0.75rem 0.5rem;
  text-align: center;
}
.choice-card:hover {
  border-color: color-mix(in srgb, var(--color-text) 30%, transparent);
  background: color-mix(in srgb, var(--color-text) 4%, var(--color-card));
}
.choice-card.selected {
  border-color: var(--color-accent);
  background: color-mix(in srgb, var(--color-accent) 10%, var(--color-card));
  box-shadow: 0 0 0 1px var(--color-accent);
}
.choice-card.danger.selected {
  border-color: var(--color-danger);
  background: color-mix(in srgb, var(--color-danger) 8%, var(--color-card));
  box-shadow: 0 0 0 1px var(--color-danger);
}
.choice-card:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.choice-copy {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
}
.choice-title {
  font-size: 0.8125rem;
  font-weight: 600;
}
.choice-hint {
  font-size: 0.6875rem;
  line-height: 1.4;
  color: var(--color-muted);
  font-weight: 400;
}
.cli-hint {
  margin: 0;
  padding: 0.75rem 0.875rem;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--color-text) 4%, var(--color-card));
  color: var(--color-muted);
  font-size: 0.75rem;
  line-height: 1.5;
}
.spin {
  animation: spin 0.8s linear infinite;
}

.card > h2 {
  padding: 1.25rem 1.25rem 0;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
@media (max-width: 640px) {
  .page-header {
    align-items: flex-start;
  }
  .page-header .btn,
  .empty-state .btn,
  .drawer-actions .btn {
    min-height: 2.75rem;
  }
  .choice-grid {
    grid-template-columns: 1fr;
  }
  .type-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
  .type-grid .choice-card {
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.25rem;
    padding: 0.625rem 0.375rem;
    text-align: center;
  }
  .type-grid .choice-hint {
    display: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  .spin {
    animation: none;
  }
  .choice-card {
    transition: none;
  }
}
</style>
