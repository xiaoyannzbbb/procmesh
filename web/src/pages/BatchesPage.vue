<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { newOperationId } from "../lib/opid";
import { useBatchClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();

const POLL_MS = 5000;
const client = useBatchClient();
const queryClient = useQueryClient();
const actionError = ref("");

const createType = ref("START");
const selectorKind = ref<"processIds" | "processGroup" | "agentGroupId">("processIds");
const selectorValue = ref("");

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

const createReady = computed(() => selectorValue.value.trim().length > 0);

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function buildSelector() {
  const value = selectorValue.value.trim();
  if (selectorKind.value === "processGroup") {
    return { processGroup: value };
  }
  if (selectorKind.value === "agentGroupId") {
    return { agentGroupId: value };
  }
  return {
    processIds: value
      .split(",")
      .map((id) => id.trim())
      .filter(Boolean),
  };
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

function statusStyle(status: string): string {
  const s = (status || "").toUpperCase();
  if (s === "SUCCESS" || s === "COMPLETED") {
    return "background-color: #D1FAE5; color: #065F46";
  }
  if (s === "TIMEOUT" || s === "PARTIAL") {
    return "background-color: #FEF3C7; color: #92400E";
  }
  if (s === "FAILED" || s === "DENIED" || s === "CONFLICT" || s === "UNAVAILABLE" || s === "INVALID") {
    return "background-color: #FEE2E2; color: #991B1B";
  }
  return "background-color: #E5E7EB; color: #374151";
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
    selectorValue.value = "";
    await queryClient.invalidateQueries({ queryKey: ["batches"] });
    const id = resp.batch?.batchId;
    if (id) {
      await router.push(`/batches/${encodeURIComponent(id)}`);
    }
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
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
    const blob = new Blob([resp.content], { type: resp.contentType || "application/json" });
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
                  :style="statusStyle(detail.status)"
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
              :style="statusStyle('TIMEOUT')"
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
                    :style="statusStyle(target.status)"
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
      <h1>{{ t("batch.title") }}</h1>
      <div class="banner" role="status">{{ t("batch.localOnly") }}</div>
      <p v-if="listPending" class="muted">{{ t("batch.loading") }}</p>
      <p v-else-if="errorText && !listQuery.data" class="error" role="alert">{{ errorText }}</p>
      <template v-else>
        <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
        <div class="card">
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
                    :style="statusStyle(batch.status)"
                  >{{ batch.status }}</span>
                </td>
                <td>{{ summaryOf(batch).success }}</td>
                <td>{{ summaryOf(batch).failed }}</td>
                <td>
                  <span
                    class="status-timeout"
                    data-status="TIMEOUT"
                    :style="statusStyle('TIMEOUT')"
                  >{{ summaryOf(batch).timeout }}</span>
                </td>
                <td>{{ formatCreated(batch.createdUnixMs) }}</td>
              </tr>
              <tr v-if="!batches.length">
                <td colspan="7" class="muted">{{ t("batch.noBatches") }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <form v-if="canExecute" class="card create-batch" @submit.prevent="onCreate">
          <h2>{{ t("batch.create") }}</h2>
          <label class="field">
            {{ t("batch.type") }}
            <select v-model="createType" class="input" name="type">
              <option value="START">START</option>
              <option value="STOP">STOP</option>
              <option value="RESTART">RESTART</option>
            </select>
          </label>
          <label class="field">
            {{ t("batch.selector") }}
            <select v-model="selectorKind" class="input" name="selectorKind">
              <option value="processIds">{{ t("batch.selectorProcessIds") }}</option>
              <option value="processGroup">{{ t("batch.selectorProcessGroup") }}</option>
              <option value="agentGroupId">{{ t("batch.selectorAgentGroup") }}</option>
            </select>
          </label>
          <label class="field">
            <input
              v-model="selectorValue"
              class="input"
              name="selectorValue"
              type="text"
              :placeholder="t('batch.processIdsPlaceholder')"
              autocomplete="off"
            />
          </label>
          <p class="muted">{{ t("batch.configUpdateCli") }}</p>
          <button class="btn btn-primary" type="submit" :disabled="!createReady || acting">
            {{ t("batch.create") }}
          </button>
        </form>
      </template>
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
.error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.875rem;
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
.create-batch {
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
.status-badge,
.status-timeout {
  display: inline-flex;
  align-items: center;
  border-radius: 999px;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
}
.card > h2 {
  padding: 1.25rem 1.25rem 0;
}
</style>
