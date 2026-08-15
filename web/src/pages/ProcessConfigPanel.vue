<script setup lang="ts">
import { create, fromJson, toJson, type JsonValue } from "@bufbuild/protobuf";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref, watch } from "vue";
import { ProcessSpecSchema, type ProcessSpec } from "../gen/procmesh/v1/api_pb";
import { isConflict } from "../lib/connecterr";
import { withTarget } from "../lib/headers";
import { newOperationId } from "../lib/opid";
import { useConfigClient } from "../lib/rpc";
import { session } from "../lib/session";
import { formatRemoteError } from "./processView";

const CONFLICT_BANNER = "409 Conflict — reload and retry";

const props = defineProps<{
  idOrName: string;
  targetNodeId: string;
}>();

const config = useConfigClient();
const queryClient = useQueryClient();
const editorText = ref("");
const comment = ref("");
const loadedSpec = ref<ProcessSpec | null>(null);
const conflictText = ref("");
const actionError = ref("");
const selected = ref<string[]>([]);
const saving = ref(false);
const rollingBack = ref(false);

const canUpdate = computed(() => (session.value?.permissions ?? []).includes("process.config.update"));
const targetOpts = computed(() => ({ headers: withTarget(props.targetNodeId) }));
const enabled = computed(() => props.idOrName.length > 0 && props.targetNodeId.length > 0);

const configQuery = useQuery({
  queryKey: computed(() => ["process-config", props.idOrName, props.targetNodeId]),
  queryFn: () => config.getConfig({ idOrName: props.idOrName }, targetOpts.value),
  enabled,
});

const historyQuery = useQuery({
  queryKey: computed(() => ["process-history", props.idOrName, props.targetNodeId]),
  queryFn: () => config.history({ idOrName: props.idOrName }, targetOpts.value),
  enabled,
});

const diffQuery = useQuery({
  queryKey: computed(() => ["process-diff", props.idOrName, props.targetNodeId, ...selected.value]),
  queryFn: () => {
    const nums = selected.value.map((v) => Number(v)).sort((a, b) => a - b);
    return config.diff(
      {
        idOrName: props.idOrName,
        fromRevision: BigInt(nums[0]),
        toRevision: BigInt(nums[1]),
      },
      targetOpts.value,
    );
  },
  enabled: computed(() => enabled.value && selected.value.length === 2),
});

watch(
  () => configQuery.data.value?.spec,
  (spec) => {
    if (!spec) {
      return;
    }
    const next = create(ProcessSpecSchema, spec);
    loadedSpec.value = next;
    editorText.value = JSON.stringify(toJson(ProcessSpecSchema, next), null, 2);
  },
  { immediate: true },
);

const revisions = computed(() => historyQuery.data.value?.revisions ?? []);

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  if (configQuery.error.value) {
    return formatRemoteError(configQuery.error.value);
  }
  if (historyQuery.error.value) {
    return formatRemoteError(historyQuery.error.value);
  }
  return "";
});

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function applySpec(spec: ProcessSpec | undefined): void {
  if (!spec) {
    return;
  }
  const next = create(ProcessSpecSchema, spec);
  loadedSpec.value = next;
  editorText.value = JSON.stringify(toJson(ProcessSpecSchema, next), null, 2);
}

async function refresh(): Promise<void> {
  await queryClient.invalidateQueries({ queryKey: ["process-config", props.idOrName, props.targetNodeId] });
  await queryClient.invalidateQueries({ queryKey: ["process-history", props.idOrName, props.targetNodeId] });
  await queryClient.invalidateQueries({ queryKey: ["process", props.idOrName, props.targetNodeId] });
}

async function onReload(): Promise<void> {
  conflictText.value = "";
  actionError.value = "";
  await refresh();
}

function toggleRevision(rev: bigint | number): void {
  const key = String(rev);
  if (selected.value.includes(key)) {
    selected.value = selected.value.filter((v) => v !== key);
    return;
  }
  selected.value = selected.value.length >= 2 ? [selected.value[1], key] : [...selected.value, key];
}

function formatTime(ms: bigint | number | undefined): string {
  const n = typeof ms === "bigint" ? Number(ms) : (ms ?? 0);
  if (!n) {
    return "—";
  }
  return new Date(n).toISOString();
}

async function onSave(): Promise<void> {
  conflictText.value = "";
  actionError.value = "";
  if (!canUpdate.value || !loadedSpec.value) {
    return;
  }
  let parsed: ProcessSpec;
  try {
    parsed = fromJson(ProcessSpecSchema, JSON.parse(editorText.value) as JsonValue);
  } catch (err) {
    actionError.value = err instanceof Error ? err.message : "Invalid JSON";
    return;
  }
  parsed.processId = loadedSpec.value.processId;
  parsed.latestRevision = loadedSpec.value.latestRevision;
  saving.value = true;
  try {
    const out = await config.updateConfig(
      {
        meta: mutationMeta(),
        idOrName: props.idOrName,
        expectedRevision: loadedSpec.value.latestRevision,
        spec: parsed,
        comment: comment.value,
      },
      targetOpts.value,
    );
    applySpec(out.spec);
    await queryClient.invalidateQueries({ queryKey: ["process-history", props.idOrName, props.targetNodeId] });
    await queryClient.invalidateQueries({ queryKey: ["process", props.idOrName, props.targetNodeId] });
  } catch (err) {
    if (isConflict(err)) {
      conflictText.value = CONFLICT_BANNER;
      return;
    }
    actionError.value = formatRemoteError(err);
  } finally {
    saving.value = false;
  }
}

async function onRollback(toRevision: bigint | number): Promise<void> {
  conflictText.value = "";
  actionError.value = "";
  if (!canUpdate.value || !loadedSpec.value) {
    return;
  }
  const to = typeof toRevision === "bigint" ? toRevision : BigInt(toRevision);
  if (!window.confirm(`Rollback to revision ${String(to)}? This writes a new revision.`)) {
    return;
  }
  rollingBack.value = true;
  try {
    const out = await config.rollback(
      {
        meta: mutationMeta(),
        idOrName: props.idOrName,
        toRevision: to,
        expectedRevision: loadedSpec.value.latestRevision,
        comment: comment.value,
      },
      targetOpts.value,
    );
    applySpec(out.spec);
    await queryClient.invalidateQueries({ queryKey: ["process-history", props.idOrName, props.targetNodeId] });
    await queryClient.invalidateQueries({ queryKey: ["process", props.idOrName, props.targetNodeId] });
  } catch (err) {
    if (isConflict(err)) {
      conflictText.value = CONFLICT_BANNER;
      return;
    }
    actionError.value = formatRemoteError(err);
  } finally {
    rollingBack.value = false;
  }
}
</script>

<template>
  <div class="panel">
    <div v-if="conflictText" class="banner conflict" role="alert">{{ conflictText }}</div>
    <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <p v-if="configQuery.isPending && !loadedSpec" class="muted">Loading…</p>

    <section v-if="loadedSpec" class="card">
      <div class="title-row">
        <h2>Config</h2>
        <button type="button" class="btn" @click="onReload">Reload</button>
      </div>
      <dl class="facts">
        <div>
          <dt>Process ID</dt>
          <dd class="mono">{{ loadedSpec.processId || "—" }}</dd>
        </div>
        <div>
          <dt>Latest Revision</dt>
          <dd>{{ String(loadedSpec.latestRevision) }}</dd>
        </div>
      </dl>
      <p class="muted note">process_id and latest_revision are read-only.</p>
      <form class="config-form" @submit.prevent="onSave">
        <label class="field">
          <span>ProcessSpec JSON</span>
          <textarea
            v-model="editorText"
            class="input editor"
            :readonly="!canUpdate"
            spellcheck="false"
            rows="18"
          />
        </label>
        <label class="field">
          <span>Comment</span>
          <input v-model="comment" class="input" type="text" :readonly="!canUpdate" />
        </label>
        <div class="actions">
          <button type="submit" class="btn btn-primary" :disabled="!canUpdate || saving || !targetNodeId">
            Save
          </button>
        </div>
      </form>
    </section>

    <section class="card">
      <h2>History</h2>
      <p v-if="historyQuery.isPending && !revisions.length" class="muted">Loading history…</p>
      <table v-else class="table">
        <thead>
          <tr>
            <th>Select</th>
            <th>Revision</th>
            <th>Operator</th>
            <th>Time</th>
            <th>Comment</th>
            <th v-if="canUpdate"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="rev in revisions" :key="String(rev.revision)">
            <td>
              <input
                type="checkbox"
                :checked="selected.includes(String(rev.revision))"
                @change="toggleRevision(rev.revision)"
              />
            </td>
            <td>{{ String(rev.revision) }}</td>
            <td>{{ rev.operator || "—" }}</td>
            <td>{{ formatTime(rev.timestampUnixMs) }}</td>
            <td>{{ rev.comment || "—" }}</td>
            <td v-if="canUpdate">
              <button
                type="button"
                class="btn"
                :disabled="rollingBack || !targetNodeId"
                @click="onRollback(rev.revision)"
              >
                Rollback
              </button>
            </td>
          </tr>
          <tr v-if="!revisions.length">
            <td :colspan="canUpdate ? 6 : 5" class="muted">No revisions</td>
          </tr>
        </tbody>
      </table>
      <div v-if="selected.length === 2" class="diff-block">
        <h3>Diff {{ selected.slice().sort((a, b) => Number(a) - Number(b)).join(" → ") }}</h3>
        <p v-if="diffQuery.isPending" class="muted">Loading diff…</p>
        <p v-else-if="diffQuery.error.value" class="error" role="alert">
          {{ formatRemoteError(diffQuery.error.value) }}
        </p>
        <pre v-else class="diff">{{ diffQuery.data.value?.diff || "(empty)" }}</pre>
      </div>
    </section>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  margin-bottom: 0.75rem;
}
h2 {
  margin: 0;
  font-size: 1.05rem;
  font-weight: 650;
}
h3 {
  margin: 0 0 0.5rem;
  font-size: 0.85rem;
  font-weight: 600;
}
.muted {
  color: var(--color-muted);
  font-size: 0.875rem;
}
.note {
  margin: 0.75rem 0;
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
}
.conflict {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-card);
  padding: 1.25rem;
  overflow: auto;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 0.85rem 1.25rem;
  margin: 0;
}
.facts div {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
}
.facts dt {
  font-size: 0.75rem;
  color: var(--color-muted);
}
.facts dd {
  margin: 0;
  font-size: 0.95rem;
  font-weight: 550;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
  font-weight: 500;
}
.config-form {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  font-size: 0.8rem;
  color: var(--color-muted);
}
.editor {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
  line-height: 1.45;
  resize: vertical;
  min-height: 16rem;
}
.actions {
  display: flex;
  gap: 0.5rem;
}
.diff-block {
  margin-top: 1rem;
}
.diff {
  margin: 0;
  padding: 0.75rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.75rem;
  white-space: pre-wrap;
  overflow: auto;
  max-height: 20rem;
}
</style>
