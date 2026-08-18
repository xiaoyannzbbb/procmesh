<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, onMounted, onUnmounted, ref } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, formatAge, type Freshness } from "../lib/freshness";
import { withTarget } from "../lib/headers";
import { newOperationId } from "../lib/opid";
import { useBackupClient, useNodeClient, useProcessClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const SINKS = ["fs", "s3", "peer"] as const;

const { t } = useI18n();
const POLL_MS = 5000;
const client = useBackupClient();
const nodeClient = useNodeClient();
const processClient = useProcessClient();
const queryClient = useQueryClient();
const actionError = ref("");
const actionNotice = ref("");

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canManage = computed(() => perms.value.has("backup.manage"));

const createSink = ref<(typeof SINKS)[number]>("fs");
const createProcessIds = ref("");
const createPeerNodeIds = ref("");

type RestoreTargetForm = { processId: string; expectedRevision: string };
type RestoreSnapshot = {
  snapshotId: string;
  nodeId: string;
  sink: string;
  sourceNodeId: string;
  processIds: string[];
  revisionRanges: { processId?: string; minRevision?: bigint | number; maxRevision?: bigint | number }[];
};

const restoreOpen = ref(false);
const restoreSnapshot = ref<RestoreSnapshot | null>(null);
const restoreTargets = ref<RestoreTargetForm[]>([]);
const lastPeerNodeIds = ref<string[]>([]);
const nodesUnavailable = ref(false);

async function collectAlivePeerIds(): Promise<string[]> {
  try {
    const res = await nodeClient.listNodes({});
    nodesUnavailable.value = false;
    return (res.nodes ?? [])
      .filter((n) => (n.state || "").toUpperCase() === "ALIVE" && n.nodeId)
      .map((n) => n.nodeId);
  } catch {
    nodesUnavailable.value = true;
    return [];
  }
}

const listQuery = useQuery({
  queryKey: ["backups"],
  queryFn: async () => {
    const peerNodeIds = await collectAlivePeerIds();
    lastPeerNodeIds.value = peerNodeIds;
    return client.listBackups({
      includeS3: true,
      peerNodeIds,
    });
  },
  refetchInterval: POLL_MS,
});

const entries = computed(() => listQuery.data.value?.entries ?? []);
const hasStale = computed(() => entries.value.some((e) => freshnessOf(e.freshness) === STALE));
const rows = computed(() => entries.value.map(mapEntry));

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  const err = listQuery.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

const listPending = computed(() => listQuery.isPending.value && !listQuery.data.value);
const showEmptyCatalog = computed(() => !listPending.value && !hasStale.value && !rows.value.length);
const showPeerHint = computed(
  () => !listPending.value && (nodesUnavailable.value || lastPeerNodeIds.value.length === 0),
);

const createReady = computed(() => {
  if (createSink.value !== "peer") {
    return true;
  }
  return parseLines(createPeerNodeIds.value).length > 0;
});

const restoreOwner = computed(() => restoreSnapshot.value?.nodeId ?? "");
const restoreReady = computed(() => {
  if (!restoreOpen.value || !restoreOwner.value) {
    return false;
  }
  if (!restoreTargets.value.length) {
    return false;
  }
  return restoreTargets.value.every((target) => target.expectedRevision.trim() !== "");
});

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function freshnessOf(raw: string | undefined): Freshness {
  if (raw === LIVE || raw === STALE || raw === UNKNOWN) {
    return raw;
  }
  return UNKNOWN;
}

function parseLines(raw: string): string[] {
  return raw
    .split(/[\n,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function shortSha(sha: string): string {
  if (!sha) {
    return "—";
  }
  return sha.length > 12 ? sha.slice(0, 12) : sha;
}

function mapEntry(
  entry: {
    snapshot?: {
      snapshotId?: string;
      nodeId?: string;
      sink?: string;
      location?: string;
      sourceNodeId?: string;
      sha256?: string;
      processIds?: string[];
      revisionRanges?: { processId?: string; minRevision?: bigint | number; maxRevision?: bigint | number }[];
    };
    sourceNode?: string;
    freshness?: string;
    lastUpdatedUnixMs?: bigint | number;
  },
  index: number,
) {
  const snapshot = entry.snapshot;
  const freshness = freshnessOf(entry.freshness);
  const lastUpdatedUnixMs = Number(entry.lastUpdatedUnixMs ?? 0);
  return {
    key: snapshot?.snapshotId || `${entry.sourceNode ?? "node"}:${index}`,
    snapshotId: snapshot?.snapshotId || "—",
    sink: snapshot?.sink || "—",
    node: snapshot?.nodeId || entry.sourceNode || "—",
    processCount: snapshot?.processIds?.length ?? 0,
    sha256: shortSha(snapshot?.sha256 ?? ""),
    freshness,
    lastUpdated: formatAge(Date.now(), lastUpdatedUnixMs),
    canAct: Boolean(canManage.value && snapshot?.snapshotId && canRestoreEntry(entry, snapshot)),
    snapshot: snapshot
      ? {
          snapshotId: snapshot.snapshotId ?? "",
          nodeId: snapshot.nodeId ?? "",
          sink: snapshot.sink ?? "",
          sourceNodeId: snapshot.sourceNodeId || entry.sourceNode || "",
          processIds: snapshot.processIds ?? [],
          revisionRanges: snapshot.revisionRanges ?? [],
        }
      : null,
  };
}

function canRestoreEntry(
  entry: { sourceNode?: string },
  snapshot: { sink?: string; sourceNodeId?: string },
): boolean {
  const sink = snapshot.sink || "";
  if (sink !== "s3") {
    return true;
  }
  const source = entry.sourceNode || snapshot.sourceNodeId || "";
  return source !== "" && source !== "s3";
}

function prefillRevision(
  processId: string,
  ranges: RestoreSnapshot["revisionRanges"],
): string {
  const range = ranges.find((r) => r.processId === processId);
  if (range?.maxRevision === undefined || range.maxRevision === null) {
    return "";
  }
  return String(range.maxRevision);
}

function liveRevision(raw: unknown): string {
  if (raw === undefined || raw === null || raw === "") {
    return "";
  }
  return String(raw);
}

async function fetchLiveRevision(processId: string, ownerId: string, fallback: string): Promise<string> {
  try {
    const res = await processClient.getProcess({ idOrName: processId }, { headers: withTarget(ownerId) });
    const live = liveRevision(res.process?.spec?.latestRevision);
    if (live !== "") {
      return live;
    }
    return fallback;
  } catch {
    return fallback;
  }
}

async function openRestore(snapshot: RestoreSnapshot): Promise<void> {
  if (!canManage.value || !snapshot.snapshotId) {
    return;
  }
  actionError.value = "";
  actionNotice.value = "";
  restoreSnapshot.value = snapshot;
  restoreTargets.value = [];
  restoreOpen.value = true;
  const processIds = snapshot.processIds.length ? snapshot.processIds : [];
  restoreTargets.value = await Promise.all(
    processIds.map(async (processId) => ({
      processId,
      expectedRevision: await fetchLiveRevision(
        processId,
        snapshot.nodeId,
        prefillRevision(processId, snapshot.revisionRanges),
      ),
    })),
  );
}

function closeRestore(): void {
  restoreOpen.value = false;
  restoreSnapshot.value = null;
  restoreTargets.value = [];
}

const createMut = useMutation({
  mutationFn: () =>
    client.createBackup({
      meta: mutationMeta(),
      sink: createSink.value,
      processIds: parseLines(createProcessIds.value),
      targetNodeIds: createSink.value === "peer" ? parseLines(createPeerNodeIds.value) : [],
    }),
  onSuccess: async () => {
    createProcessIds.value = "";
    createPeerNodeIds.value = "";
    actionNotice.value = "";
    await queryClient.invalidateQueries({ queryKey: ["backups"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const deleteMut = useMutation({
  mutationFn: (row: { snapshotId: string; sink: string; sourceNodeId: string }) =>
    client.deleteBackup({
      meta: mutationMeta(),
      snapshotId: row.snapshotId,
      sink: row.sink,
      sourceNodeId: row.sourceNodeId,
    }),
  onSuccess: async () => {
    await queryClient.invalidateQueries({ queryKey: ["backups"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const restoreMut = useMutation({
  mutationFn: () => {
    const snap = restoreSnapshot.value;
    if (!snap) {
      throw new Error("missing snapshot");
    }
    return client.restoreBackup({
      meta: mutationMeta(),
      snapshotId: snap.snapshotId,
      sink: snap.sink,
      sourceNodeId: snap.sourceNodeId,
      targets: restoreTargets.value.map((target) => ({
        processId: target.processId,
        expectedRevision: BigInt(target.expectedRevision.trim()),
      })),
    });
  },
  onSuccess: async (res) => {
    const results = res.results ?? [];
    const conflicts = results.filter((r) => (r.status || "").toUpperCase() === "CONFLICT");
    const failures = results.filter((r) => {
      const status = (r.status || "").toUpperCase();
      return status && status !== "SUCCESS";
    });
    if (conflicts.length) {
      actionNotice.value = "";
      actionError.value = t("backup.restoreConflict", {
        detail: conflicts.map((r) => `${r.processId}: ${r.error || r.status}`).join("; "),
      });
      return;
    }
    if (failures.length) {
      actionNotice.value = "";
      actionError.value = t("backup.restoreFailed", {
        detail: failures.map((r) => `${r.processId}: ${r.error || r.status}`).join("; "),
      });
      return;
    }
    actionError.value = "";
    actionNotice.value = t("backup.restoreSuccess");
    closeRestore();
    await queryClient.invalidateQueries({ queryKey: ["backups"] });
  },
  onError: (err: unknown) => {
    actionNotice.value = "";
    actionError.value = formatRemoteError(err);
  },
});

const acting = computed(
  () => createMut.isPending.value || deleteMut.isPending.value || restoreMut.isPending.value,
);

function onRestoreKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape" && restoreOpen.value && !acting.value) {
    closeRestore();
  }
}

onMounted(() => document.addEventListener("keydown", onRestoreKeydown));
onUnmounted(() => document.removeEventListener("keydown", onRestoreKeydown));

async function onCreate(): Promise<void> {
  if (!canManage.value || !createReady.value || acting.value) {
    return;
  }
  actionError.value = "";
  actionNotice.value = "";
  try {
    await createMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onDelete(row: { snapshot: RestoreSnapshot | null }): Promise<void> {
  if (!canManage.value || !row.snapshot || acting.value) {
    return;
  }
  if (!window.confirm(t("backup.deleteConfirm", { id: row.snapshot.snapshotId }))) {
    return;
  }
  actionError.value = "";
  actionNotice.value = "";
  try {
    await deleteMut.mutateAsync({
      snapshotId: row.snapshot.snapshotId,
      sink: row.snapshot.sink,
      sourceNodeId: row.snapshot.sourceNodeId,
    });
  } catch {
    // onError already recorded
  }
}

async function onConfirmRestore(): Promise<void> {
  if (!canManage.value || !restoreReady.value || acting.value) {
    return;
  }
  actionError.value = "";
  actionNotice.value = "";
  try {
    await restoreMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page">
    <h1>{{ t("backup.title") }}</h1>
    <div v-if="hasStale" class="banner backup-stale-banner" role="status">{{ t("backup.staleBanner") }}</div>
    <p v-if="showPeerHint" class="muted">{{ t("backup.peerHint") }}</p>
    <p v-if="listPending" class="muted">{{ t("backup.loading") }}</p>
    <p v-else-if="errorText && !listQuery.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <p v-else-if="actionNotice" class="notice" role="status">{{ actionNotice }}</p>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("backup.snapshotId") }}</th>
              <th>{{ t("backup.sink") }}</th>
              <th>{{ t("backup.node") }}</th>
              <th>{{ t("backup.processCount") }}</th>
              <th>{{ t("backup.sha256") }}</th>
              <th>{{ t("backup.freshness") }}</th>
              <th>{{ t("backup.lastUpdated") }}</th>
              <th v-if="canManage"></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in rows"
              :key="row.key"
              :data-freshness="row.freshness"
              :class="{ 'row-stale': row.freshness === 'STALE' }"
            >
              <td class="mono">{{ row.snapshotId }}</td>
              <td>{{ row.sink }}</td>
              <td class="mono">{{ row.node }}</td>
              <td>{{ row.processCount }}</td>
              <td class="mono">{{ row.sha256 }}</td>
              <td><FreshnessBadge :status="row.freshness" /></td>
              <td>{{ row.lastUpdated }}</td>
              <td v-if="canManage">
                <div v-if="row.canAct" class="row-actions">
                  <button type="button" class="btn" data-action="restore" :disabled="acting" @click="openRestore(row.snapshot!)">
                    {{ t("backup.restore") }}
                  </button>
                  <button type="button" class="btn btn-danger" data-action="delete" :disabled="acting" @click="onDelete(row)">
                    {{ t("backup.delete") }}
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="showEmptyCatalog">
              <td :colspan="canManage ? 8 : 7" class="muted empty-catalog">{{ t("backup.noBackups") }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <form v-if="canManage" class="card form-card create-backup" @submit.prevent="onCreate">
        <h2>{{ t("backup.create") }}</h2>
        <label class="field">
          {{ t("backup.sink") }}
          <select v-model="createSink" class="input" name="sink">
            <option v-for="sink in SINKS" :key="sink" :value="sink">{{ sink }}</option>
          </select>
        </label>
        <label class="field">
          {{ t("backup.processIds") }}
          <textarea
            v-model="createProcessIds"
            class="input textarea"
            name="processIds"
            rows="2"
            :placeholder="t('backup.processIdsPlaceholder')"
          />
        </label>
        <label v-if="createSink === 'peer'" class="field">
          {{ t("backup.peerNodeIds") }}
          <textarea
            v-model="createPeerNodeIds"
            class="input textarea"
            name="peerNodeIds"
            rows="3"
            :placeholder="t('backup.peerNodeIdsPlaceholder')"
          />
        </label>
        <p v-if="createSink === 'peer' && !createReady" class="muted">{{ t("backup.peerRequired") }}</p>
        <button
          class="btn btn-primary"
          type="submit"
          data-action="create"
          :disabled="!createReady || acting"
        >
          {{ t("backup.create") }}
        </button>
      </form>
    </template>

    <div v-if="restoreOpen && restoreSnapshot" class="restore-backdrop" data-restore-dialog>
      <section class="restore-panel" role="dialog" aria-modal="true">
        <h2>{{ t("backup.restoreConfirm") }}</h2>
        <dl class="facts">
          <div>
            <dt>{{ t("backup.snapshotId") }}</dt>
            <dd class="mono">{{ restoreSnapshot.snapshotId }}</dd>
          </div>
          <div data-restore-owner>
            <dt>{{ t("backup.owner") }}</dt>
            <dd class="mono">{{ restoreSnapshot.nodeId }}</dd>
          </div>
          <div>
            <dt>{{ t("backup.sink") }}</dt>
            <dd>{{ restoreSnapshot.sink }}</dd>
          </div>
        </dl>
        <div v-for="target in restoreTargets" :key="target.processId" class="field">
          <label>
            {{ t("backup.process") }} {{ target.processId }} — {{ t("backup.expectedRevision") }}
            <input
              v-model="target.expectedRevision"
              class="input"
              name="expectedRevision"
              type="text"
              inputmode="numeric"
              autocomplete="off"
            />
          </label>
        </div>
        <div class="restore-actions">
          <button type="button" class="btn" :disabled="acting" @click="closeRestore">
            {{ t("backup.cancel") }}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            data-action="confirm-restore"
            :disabled="!restoreReady || acting"
            @click="onConfirmRestore"
          >
            {{ t("backup.confirm") }}
          </button>
        </div>
      </section>
    </div>
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
.notice {
  margin: 0;
  color: var(--color-live-fg);
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
.form-card {
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
.textarea {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  min-height: 4rem;
  resize: vertical;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
.row-stale {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
tr[data-freshness="STALE"] {
  background-color: #fef3c7;
  color: #92400e;
}
.row-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 0.75rem 1.25rem;
  margin: 0 0 1rem;
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
.restore-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: grid;
  place-items: center;
  padding: 1rem;
  background: rgba(0, 0, 0, 0.55);
}
.restore-panel {
  width: min(100%, 32rem);
  padding: 1.5rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  box-shadow: 0 1rem 3rem rgba(0, 0, 0, 0.3);
  color: var(--color-text);
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.restore-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.75rem;
}
</style>
