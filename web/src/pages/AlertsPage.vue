<script setup lang="ts">
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref, watch } from "vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, formatAge, type Freshness } from "../lib/freshness";
import { newOperationId } from "../lib/opid";
import { useAlertClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const CHANNEL_TYPES = ["WEB", "WEBHOOK", "EMAIL", "WECOM", "DINGTALK", "SLACK"] as const;
const SECRET_KEYS = new Set(["hmac_secret", "password", "secret"]);

const { t } = useI18n();
const POLL_MS = 5000;
const client = useAlertClient();
const queryClient = useQueryClient();
const actionError = ref("");

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canManage = computed(() => perms.value.has("alert.manage"));

const channelId = ref("");
const channelType = ref<(typeof CHANNEL_TYPES)[number]>("WEBHOOK");
const channelName = ref("");
const channelEnabled = ref(true);
const channelConfig = ref("{}");

const dedupWindowSec = ref(600);
const notifyOnResolve = ref(true);
const cpuHighPercent = ref(90);
const memoryHighPercent = ref(90);
const diskHighPercent = ref(90);
const highConsecutiveMins = ref(2);
const suspectTooLongSec = ref(120);
const policyHydrated = ref(false);

function applyPolicy(p: {
  dedupWindowSec?: bigint | number;
  notifyOnResolve?: boolean;
  cpuHighPercent?: number;
  memoryHighPercent?: number;
  diskHighPercent?: number;
  highConsecutiveMins?: number;
  suspectTooLongSec?: bigint | number;
}): void {
  dedupWindowSec.value = Number(p.dedupWindowSec ?? 600);
  notifyOnResolve.value = Boolean(p.notifyOnResolve);
  cpuHighPercent.value = p.cpuHighPercent ?? 90;
  memoryHighPercent.value = p.memoryHighPercent ?? 90;
  diskHighPercent.value = p.diskHighPercent ?? 90;
  highConsecutiveMins.value = p.highConsecutiveMins ?? 2;
  suspectTooLongSec.value = Number(p.suspectTooLongSec ?? 120);
}

const listQuery = useQuery({
  queryKey: ["alerts"],
  queryFn: () => client.listAlerts({}),
  refetchInterval: POLL_MS,
});

const channelsQuery = useQuery({
  queryKey: ["alert-channels"],
  queryFn: () => client.listAlertChannels({}),
  refetchInterval: POLL_MS,
});

const policyQuery = useQuery({
  queryKey: ["alert-policy"],
  queryFn: () => client.getAlertPolicy({}),
  refetchInterval: POLL_MS,
});

const entries = computed(() => listQuery.data.value?.entries ?? []);
const channels = computed(() => (channelsQuery.data.value?.channels ?? []).map(mapChannel));
const hasStale = computed(() => entries.value.some((e) => freshnessOf(e.freshness) === STALE));
const rows = computed(() => entries.value.map(mapEntry));

const errorText = computed(() => {
  if (actionError.value) {
    return actionError.value;
  }
  const err = listQuery.error.value ?? channelsQuery.error.value ?? policyQuery.error.value;
  if (!err) {
    return "";
  }
  return formatRemoteError(err);
});

const listPending = computed(() => listQuery.isPending.value && !listQuery.data.value);
const showEmptyInbox = computed(() => !listPending.value && !hasStale.value && !rows.value.length);

watch(
  () => policyQuery.data.value?.policy,
  (p) => {
    if (!p || policyHydrated.value) {
      return;
    }
    applyPolicy(p);
    policyHydrated.value = true;
  },
  { immediate: true },
);

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

function mapEntry(
  entry: {
    alert?: {
      alertId?: string;
      fingerprint?: string;
      type?: string;
      severity?: string;
      nodeId?: string;
      processId?: string;
      state?: string;
    };
    sourceNode?: string;
    freshness?: string;
    lastUpdatedUnixMs?: bigint | number;
  },
  index: number,
) {
  const alert = entry.alert;
  const freshness = freshnessOf(entry.freshness);
  const lastUpdatedUnixMs = Number(entry.lastUpdatedUnixMs ?? 0);
  return {
    key: alert?.alertId || `${entry.sourceNode ?? "node"}:${index}`,
    fingerprint: alert?.fingerprint || "—",
    type: alert?.type || "—",
    severity: alert?.severity || "—",
    state: (alert?.state || "").toUpperCase(),
    node: alert?.nodeId || entry.sourceNode || "—",
    process: alert?.processId || "—",
    freshness,
    lastUpdated: formatAge(Date.now(), lastUpdatedUnixMs),
  };
}

function mapChannel(ch: { channelId?: string; type?: string; name?: string; enabled?: boolean; configJson?: string }) {
  return {
    channelId: ch.channelId ?? "",
    type: ch.type ?? "",
    name: ch.name ?? "",
    enabled: Boolean(ch.enabled),
    configJson: redactConfigJson(ch.configJson ?? ""),
  };
}

function stripSecrets(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(stripSecrets);
  }
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, val] of Object.entries(value as Record<string, unknown>)) {
      if (SECRET_KEYS.has(key) || key.toLowerCase() === "authorization") {
        continue;
      }
      out[key] = stripSecrets(val);
    }
    return out;
  }
  return value;
}

function redactConfigJson(raw: string): string {
  const text = raw.trim();
  if (!text) {
    return "{}";
  }
  try {
    return JSON.stringify(stripSecrets(JSON.parse(text)));
  } catch {
    return text;
  }
}

function stateLabel(state: string): string {
  if (state === "FIRING") {
    return t("alert.firing");
  }
  if (state === "RESOLVED") {
    return t("alert.resolved");
  }
  return state || "—";
}

const putChannelMut = useMutation({
  mutationFn: () =>
    client.putAlertChannel({
      meta: mutationMeta(),
      channelId: channelId.value,
      type: channelType.value,
      name: channelName.value.trim(),
      enabled: channelEnabled.value,
      configJson: channelConfig.value,
    }),
  onSuccess: async () => {
    resetChannelForm();
    await queryClient.invalidateQueries({ queryKey: ["alert-channels"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const deleteChannelMut = useMutation({
  mutationFn: (id: string) =>
    client.deleteAlertChannel({
      meta: mutationMeta(),
      channelId: id,
    }),
  onSuccess: async () => {
    resetChannelForm();
    await queryClient.invalidateQueries({ queryKey: ["alert-channels"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const putPolicyMut = useMutation({
  mutationFn: () =>
    client.putAlertPolicy({
      meta: mutationMeta(),
      policy: {
        dedupWindowSec: BigInt(dedupWindowSec.value),
        notifyOnResolve: notifyOnResolve.value,
        cpuHighPercent: cpuHighPercent.value,
        memoryHighPercent: memoryHighPercent.value,
        diskHighPercent: diskHighPercent.value,
        highConsecutiveMins: highConsecutiveMins.value,
        suspectTooLongSec: BigInt(suspectTooLongSec.value),
      },
    }),
  onSuccess: async (res) => {
    if (res?.policy) {
      applyPolicy(res.policy);
    }
    await queryClient.invalidateQueries({ queryKey: ["alert-policy"] });
  },
  onError: (err: unknown) => {
    actionError.value = formatRemoteError(err);
  },
});

const acting = computed(
  () => putChannelMut.isPending.value || deleteChannelMut.isPending.value || putPolicyMut.isPending.value,
);

function resetChannelForm(): void {
  channelId.value = "";
  channelType.value = "WEBHOOK";
  channelName.value = "";
  channelEnabled.value = true;
  channelConfig.value = "{}";
}

function loadChannel(ch: { channelId: string; type: string; name: string; enabled: boolean; configJson: string }): void {
  if (!canManage.value) {
    return;
  }
  channelId.value = ch.channelId;
  channelType.value = CHANNEL_TYPES.includes(ch.type as (typeof CHANNEL_TYPES)[number])
    ? (ch.type as (typeof CHANNEL_TYPES)[number])
    : "WEBHOOK";
  channelName.value = ch.name;
  channelEnabled.value = ch.enabled;
  channelConfig.value = redactConfigJson(ch.configJson);
}

async function onSaveChannel(): Promise<void> {
  if (!canManage.value || !channelName.value.trim() || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await putChannelMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

async function onDeleteChannel(id: string): Promise<void> {
  if (!canManage.value || !id || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await deleteChannelMut.mutateAsync(id);
  } catch {
    // onError already recorded
  }
}

async function onSavePolicy(): Promise<void> {
  if (!canManage.value || acting.value) {
    return;
  }
  actionError.value = "";
  try {
    await putPolicyMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page">
    <h1>{{ t("alert.title") }}</h1>
    <div v-if="hasStale" class="banner alert-stale-banner" role="status">{{ t("alert.staleBanner") }}</div>
    <p v-if="listPending" class="muted">{{ t("alert.loading") }}</p>
    <p v-else-if="errorText && !listQuery.data" class="error" role="alert">{{ errorText }}</p>
    <template v-else>
      <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
      <div class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("alert.fingerprint") }}</th>
              <th>{{ t("alert.type") }}</th>
              <th>{{ t("alert.severity") }}</th>
              <th>{{ t("alert.state") }}</th>
              <th>{{ t("alert.node") }}</th>
              <th>{{ t("alert.process") }}</th>
              <th>{{ t("alert.freshness") }}</th>
              <th>{{ t("alert.lastUpdated") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in rows"
              :key="row.key"
              :data-freshness="row.freshness"
              :class="{ 'row-stale': row.freshness === 'STALE' }"
            >
              <td class="mono">{{ row.fingerprint }}</td>
              <td>{{ row.type }}</td>
              <td>{{ row.severity }}</td>
              <td>
                <span
                  class="alert-state"
                  :class="{ 'alert-firing': row.state === 'FIRING' }"
                  :data-state="row.state || undefined"
                >{{ stateLabel(row.state) }}</span>
              </td>
              <td class="mono">{{ row.node }}</td>
              <td>{{ row.process }}</td>
              <td><FreshnessBadge :status="row.freshness" /></td>
              <td>{{ row.lastUpdated }}</td>
            </tr>
            <tr v-if="showEmptyInbox">
              <td colspan="8" class="muted empty-inbox">{{ t("alert.noAlerts") }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <section class="card form-card">
        <h2>{{ t("alert.channels") }}</h2>
        <table class="table">
          <thead>
            <tr>
              <th>{{ t("alert.name") }}</th>
              <th>{{ t("alert.type") }}</th>
              <th>{{ t("alert.enabled") }}</th>
              <th>{{ t("alert.config") }}</th>
              <th v-if="canManage"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="ch in channels" :key="ch.channelId || ch.name">
              <td>
                <button v-if="canManage" type="button" class="linkish" @click="loadChannel(ch)">{{ ch.name }}</button>
                <span v-else>{{ ch.name }}</span>
              </td>
              <td>{{ ch.type }}</td>
              <td>{{ t(ch.enabled ? "alert.enabled" : "alert.disabled") }}</td>
              <td class="mono config-cell">{{ ch.configJson }}</td>
              <td v-if="canManage">
                <button
                  type="button"
                  class="btn btn-danger"
                  data-action="delete-channel"
                  :disabled="acting"
                  @click="onDeleteChannel(ch.channelId)"
                >
                  {{ t("alert.delete") }}
                </button>
              </td>
            </tr>
            <tr v-if="!channels.length">
              <td :colspan="canManage ? 5 : 4" class="muted">{{ t("alert.noChannels") }}</td>
            </tr>
          </tbody>
        </table>
        <form v-if="canManage" class="channel-form" @submit.prevent="onSaveChannel">
          <h3>{{ t("alert.createChannel") }}</h3>
          <label class="field">
            {{ t("alert.type") }}
            <select v-model="channelType" class="input" name="channelType">
              <option v-for="typ in CHANNEL_TYPES" :key="typ" :value="typ">{{ typ }}</option>
            </select>
          </label>
          <label class="field">
            {{ t("alert.name") }}
            <input v-model="channelName" class="input" name="channelName" type="text" autocomplete="off" />
          </label>
          <label class="check">
            <input v-model="channelEnabled" name="channelEnabled" type="checkbox" />
            {{ t("alert.enabled") }}
          </label>
          <label class="field">
            {{ t("alert.config") }}
            <textarea v-model="channelConfig" class="input textarea" name="channelConfig" rows="4" />
          </label>
          <button
            class="btn btn-primary"
            type="submit"
            data-action="save-channel"
            :disabled="!channelName.trim() || acting"
          >
            {{ t("alert.save") }}
          </button>
        </form>
      </section>

      <form class="card form-card policy-form" @submit.prevent="onSavePolicy">
        <h2>{{ t("alert.policy") }}</h2>
        <label class="field">
          {{ t("alert.dedupWindowSec") }}
          <input v-model.number="dedupWindowSec" class="input" name="dedupWindowSec" type="number" min="1" :disabled="!canManage" />
        </label>
        <label class="check">
          <input v-model="notifyOnResolve" name="notifyOnResolve" type="checkbox" :disabled="!canManage" />
          {{ t("alert.notifyOnResolve") }}
        </label>
        <label class="field">
          {{ t("alert.cpuHighPercent") }}
          <input v-model.number="cpuHighPercent" class="input" name="cpuHighPercent" type="number" min="1" max="100" :disabled="!canManage" />
        </label>
        <label class="field">
          {{ t("alert.memoryHighPercent") }}
          <input v-model.number="memoryHighPercent" class="input" name="memoryHighPercent" type="number" min="1" max="100" :disabled="!canManage" />
        </label>
        <label class="field">
          {{ t("alert.diskHighPercent") }}
          <input v-model.number="diskHighPercent" class="input" name="diskHighPercent" type="number" min="1" max="100" :disabled="!canManage" />
        </label>
        <label class="field">
          {{ t("alert.highConsecutiveMins") }}
          <input v-model.number="highConsecutiveMins" class="input" name="highConsecutiveMins" type="number" min="1" max="60" :disabled="!canManage" />
        </label>
        <label class="field">
          {{ t("alert.suspectTooLongSec") }}
          <input v-model.number="suspectTooLongSec" class="input" name="suspectTooLongSec" type="number" min="1" max="86400" :disabled="!canManage" />
        </label>
        <button
          v-if="canManage"
          class="btn btn-primary"
          type="submit"
          data-action="save-policy"
          :disabled="acting"
        >
          {{ t("alert.save") }}
        </button>
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
h3 {
  margin: 0 0 0.5rem;
  font-size: 0.95rem;
  font-weight: 600;
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
.form-card {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 1.25rem;
}
.channel-form,
.policy-form {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  font-size: 0.875rem;
  color: var(--color-muted);
}
.check {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.875rem;
  color: var(--color-muted);
}
.textarea {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  min-height: 6rem;
  resize: vertical;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
}
.config-cell {
  max-width: 28rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.alert-state {
  font-weight: 600;
}
.alert-firing {
  color: var(--color-danger);
}
.row-stale {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}
tr[data-freshness="STALE"] {
  background-color: #fef3c7;
  color: #92400e;
}
.linkish {
  border: 0;
  background: none;
  padding: 0;
  color: var(--color-accent);
  cursor: pointer;
  font: inherit;
}
.linkish:hover {
  text-decoration: underline;
}
</style>
