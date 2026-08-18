<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Template enums and accessibility/form attributes are non-copy literals; visible copy uses t(). */
import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import {
  AlertCircle,
  CheckCircle2,
  Clipboard,
  ExternalLink,
  Filter,
  LoaderCircle,
  Mail,
  Plus,
  RefreshCw,
  Search,
  Send,
  Settings2,
  SlidersHorizontal,
  Trash2,
  Webhook,
  X,
} from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import ConfirmDialog from "../components/ConfirmDialog.vue";
import Drawer from "../components/Drawer.vue";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { LIVE, STALE, UNKNOWN, formatAge, type Freshness } from "../lib/freshness";
import { newOperationId } from "../lib/opid";
import { useAlertClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { formatRemoteError } from "./processView";

const CHANNEL_TYPES = ["WEB", "WEBHOOK", "EMAIL", "WECOM", "DINGTALK", "SLACK"] as const;
const SECRET_KEYS = new Set(["hmac_secret", "password", "secret"]);
const POLL_MS = 5000;

type ChannelType = (typeof CHANNEL_TYPES)[number];
type WorkspaceView = "inbox" | "settings";
type SettingsSection = "channels" | "policy";
type StateFilter = "ALL" | "FIRING" | "RESOLVED";

type AlertEntryInput = {
  alert?: {
    alertId?: string;
    fingerprint?: string;
    type?: string;
    severity?: string;
    nodeId?: string;
    processId?: string;
    payloadJson?: string;
    state?: string;
    firstUnixMs?: bigint | number;
    lastUnixMs?: bigint | number;
    notifiedUnixMs?: bigint | number;
    resolvedUnixMs?: bigint | number;
    lastError?: string;
  };
  sourceNode?: string;
  freshness?: string;
  lastUpdatedUnixMs?: bigint | number;
};

type AlertRow = {
  key: string;
  alertId: string;
  fingerprint: string;
  type: string;
  severity: string;
  state: string;
  node: string;
  process: string;
  payloadJson: string;
  firstUnixMs: number;
  lastUnixMs: number;
  notifiedUnixMs: number;
  resolvedUnixMs: number;
  lastError: string;
  sourceNode: string;
  freshness: Freshness;
  lastUpdatedUnixMs: number;
  lastUpdated: string;
  placeholder: boolean;
};

type ChannelRow = {
  channelId: string;
  type: ChannelType | string;
  name: string;
  enabled: boolean;
  configJson: string;
  summary: string;
};

const { t } = useI18n();
const client = useAlertClient();
const queryClient = useQueryClient();

const activeView = ref<WorkspaceView>("inbox");
const settingsSection = ref<SettingsSection>("channels");
const stateFilter = ref<StateFilter>("ALL");
const nodeFilter = ref("");
const severityFilter = ref("ALL");
const typeFilter = ref("ALL");
const searchQuery = ref("");
const actionFeedback = ref<{ type: "success" | "error"; message: string } | null>(null);
const detailRow = ref<AlertRow | null>(null);
const detailLoading = ref(false);
const detailError = ref("");
const copyState = ref<"idle" | "copied" | "failed">("idle");
const channelToDelete = ref<ChannelRow | null>(null);
const channelDrawerOpen = ref(false);
const testingChannelId = ref("");

const perms = computed(() => new Set(session.value?.permissions ?? []));
const canManage = computed(() => perms.value.has("alert.manage"));

const channelId = ref("");
const channelType = ref<ChannelType>("WEBHOOK");
const channelName = ref("");
const channelEnabled = ref(true);
const channelEndpoint = ref("");
const channelSecret = ref("");
const channelSmtpHost = ref("");
const channelSmtpPort = ref(25);
const channelUsername = ref("");
const channelFrom = ref("");
const channelRecipients = ref("");
const channelStartTls = ref(false);
const channelConfig = ref("{}");
const channelAdvanced = ref(false);
const hydratingChannel = ref(false);

const dedupWindowSec = ref(600);
const notifyOnResolve = ref(true);
const cpuHighPercent = ref(90);
const memoryHighPercent = ref(90);
const diskHighPercent = ref(90);
const highConsecutiveMins = ref(2);
const suspectTooLongSec = ref(120);
const policyHydrated = ref(false);

function toNumber(value: bigint | number | undefined): number {
  return Number(value ?? 0);
}

function freshnessOf(raw: string | undefined): Freshness {
  if (raw === LIVE || raw === STALE || raw === UNKNOWN) {
    return raw;
  }
  return UNKNOWN;
}

function mapEntry(entry: AlertEntryInput, index: number): AlertRow {
  const alert = entry.alert;
  const freshness = freshnessOf(entry.freshness);
  const lastUpdatedUnixMs = toNumber(entry.lastUpdatedUnixMs);
  const placeholder = !alert;
  return {
    key: alert?.alertId || `${entry.sourceNode ?? "node"}:${index}`,
    alertId: alert?.alertId ?? "",
    fingerprint: alert?.fingerprint || "—",
    type: alert?.type || "DATA_UNAVAILABLE",
    severity: (alert?.severity || (placeholder ? "UNKNOWN" : "—")).toUpperCase(),
    state: (alert?.state || "").toUpperCase(),
    node: alert?.nodeId || entry.sourceNode || "—",
    process: alert?.processId || "",
    payloadJson: alert?.payloadJson || "",
    firstUnixMs: toNumber(alert?.firstUnixMs),
    lastUnixMs: toNumber(alert?.lastUnixMs),
    notifiedUnixMs: toNumber(alert?.notifiedUnixMs),
    resolvedUnixMs: toNumber(alert?.resolvedUnixMs),
    lastError: alert?.lastError || "",
    sourceNode: entry.sourceNode || alert?.nodeId || "",
    freshness,
    lastUpdatedUnixMs,
    lastUpdated: formatAge(Date.now(), lastUpdatedUnixMs),
    placeholder,
  };
}

function parseConfig(raw: string): Record<string, unknown> {
  try {
    const value: unknown = JSON.parse(raw || "{}");
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
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

function prettyJson(raw: string): string {
  if (!raw.trim()) {
    return "{}";
  }
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

function shortId(value: string): string {
  if (!value || value.length <= 28) {
    return value || "—";
  }
  return `${value.slice(0, 12)}…${value.slice(-10)}`;
}

function humanizeCode(value: string): string {
  return value
    .replaceAll("_", " ")
    .toLowerCase()
    .replace(/(^|\s)\S/g, (letter) => letter.toUpperCase());
}

const typeLabelKeys = {
  AGENT_FAILED: "alert.types.agentFailed",
  AGENT_SUSPECT_TOO_LONG: "alert.types.agentSuspectTooLong",
  PROCESS_EXIT: "alert.types.processExit",
  PROCESS_FATAL: "alert.types.processFatal",
  PROCESS_CRASH_LOOP: "alert.types.processCrashLoop",
  HEALTH_FAILED: "alert.types.healthFailed",
  CPU_HIGH: "alert.types.cpuHigh",
  MEMORY_HIGH: "alert.types.memoryHigh",
  DISK_HIGH: "alert.types.diskHigh",
  CONTROL_NO_QUORUM: "alert.types.controlNoQuorum",
  LOCAL_DB_ERROR: "alert.types.localDbError",
  CERT_EXPIRING: "alert.types.certExpiring",
  VERSION_MISMATCH: "alert.types.versionMismatch",
  DATA_UNAVAILABLE: "alert.dataUnavailable",
} as const;

const channelTypeLabelKeys = {
  WEB: "alert.channelTypes.WEB",
  WEBHOOK: "alert.channelTypes.WEBHOOK",
  EMAIL: "alert.channelTypes.EMAIL",
  WECOM: "alert.channelTypes.WECOM",
  DINGTALK: "alert.channelTypes.DINGTALK",
  SLACK: "alert.channelTypes.SLACK",
} as const;

function typeLabel(type: string): string {
  const key = typeLabelKeys[type as keyof typeof typeLabelKeys];
  return key ? t(key) : humanizeCode(type);
}

function severityLabel(severity: string): string {
  if (severity === "CRITICAL") {
    return t("alert.critical");
  }
  if (severity === "WARNING") {
    return t("alert.warning");
  }
  if (severity === "UNKNOWN") {
    return t("alert.unknown");
  }
  return severity || "—";
}

function stateLabel(state: string): string {
  if (state === "FIRING") {
    return t("alert.firing");
  }
  if (state === "RESOLVED") {
    return t("alert.resolved");
  }
  return state || t("alert.dataUnavailable");
}

function formatDate(value: number): string {
  if (!value) {
    return t("alert.notAvailable");
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function channelTypeLabel(type: string): string {
  const key = channelTypeLabelKeys[type as ChannelType];
  return key ? t(key) : type;
}

function channelSummary(type: string, raw: string): string {
  const config = parseConfig(raw);
  const endpoint = String(config.url ?? config.webhook_url ?? "");
  if (type === "WEB") {
    return t("alert.channelBuiltIn");
  }
  if (type === "EMAIL") {
    const host = String(config.smtp_host ?? "");
    return host || t("alert.configNotSet");
  }
  return endpoint || t("alert.configNotSet");
}

function mapChannel(ch: {
  channelId?: string;
  type?: string;
  name?: string;
  enabled?: boolean;
  configJson?: string;
}): ChannelRow {
  const type = ch.type ?? "";
  const configJson = redactConfigJson(ch.configJson ?? "");
  return {
    channelId: ch.channelId ?? "",
    type,
    name: ch.name ?? "",
    enabled: Boolean(ch.enabled),
    configJson,
    summary: channelSummary(type, configJson),
  };
}

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
  queryKey: computed(() => ["alerts", { targetNode: nodeFilter.value }]),
  queryFn: () => client.listAlerts({ limit: 200, targetNode: nodeFilter.value, state: "" }),
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

const entries = computed(() => (listQuery.data.value?.entries ?? []) as AlertEntryInput[]);
const rawRows = computed(() => entries.value.map(mapEntry));
const channels = computed(() => (channelsQuery.data.value?.channels ?? []).map(mapChannel));
const hasStale = computed(() => rawRows.value.some((row) => row.freshness !== LIVE));
const nodeOptions = computed(() => uniqueSorted(rawRows.value.map((row) => row.node).filter(Boolean)));
const severityOptions = computed(() => uniqueSorted(rawRows.value.map((row) => row.severity).filter(Boolean)));
const typeOptions = computed(() => uniqueSorted(rawRows.value.map((row) => row.type).filter(Boolean)));

function uniqueSorted(values: string[]): string[] {
  return [...new Set(values)].sort((a, b) => a.localeCompare(b));
}

const rows = computed(() => {
  const query = searchQuery.value.trim().toLowerCase();
  return rawRows.value
    .filter((row) => stateFilter.value === "ALL" || row.state === stateFilter.value)
    .filter((row) => severityFilter.value === "ALL" || row.severity === severityFilter.value)
    .filter((row) => typeFilter.value === "ALL" || row.type === typeFilter.value)
    .filter((row) => {
      if (!query) {
        return true;
      }
      return [row.fingerprint, row.type, row.node, row.process].some((value) => value.toLowerCase().includes(query));
    })
    .sort((a, b) => {
      const stateRank = (state: string) => (state === "FIRING" ? 0 : state === "RESOLVED" ? 1 : 2);
      const severityRank = (severity: string) => (severity === "CRITICAL" ? 0 : severity === "WARNING" ? 1 : 2);
      return (
        stateRank(a.state) - stateRank(b.state) ||
        severityRank(a.severity) - severityRank(b.severity) ||
        b.lastUnixMs - a.lastUnixMs ||
        b.lastUpdatedUnixMs - a.lastUpdatedUnixMs
      );
    });
});

const firingCount = computed(() => rawRows.value.filter((row) => row.state === "FIRING").length);
const resolvedCount = computed(() => rawRows.value.filter((row) => row.state === "RESOLVED").length);
const dataIssueCount = computed(() => rawRows.value.filter((row) => row.freshness !== LIVE).length);
const listPending = computed(() => listQuery.isPending.value && !listQuery.data.value);
const listRefreshing = computed(() => listQuery.isFetching.value && !listPending.value);
const showEmptyInbox = computed(() => !listPending.value && !rows.value.length);
const isFiltered = computed(
  () =>
    stateFilter.value !== "ALL" ||
    severityFilter.value !== "ALL" ||
    typeFilter.value !== "ALL" ||
    Boolean(nodeFilter.value) ||
    Boolean(searchQuery.value.trim()),
);
const lastRefresh = computed(() => {
  const value = Number(listQuery.dataUpdatedAt.value ?? 0);
  return value ? formatAge(Date.now(), value) : t("alert.notAvailable");
});
const listError = computed(() => (listQuery.error.value ? formatRemoteError(listQuery.error.value) : ""));
const settingsError = computed(() => {
  const error = channelsQuery.error.value ?? policyQuery.error.value;
  return error ? formatRemoteError(error) : "";
});
const detailTitle = computed(() => {
  const row = detailRow.value;
  return row ? `${typeLabel(row.type)} · ${shortId(row.fingerprint)}` : t("alert.details");
});
const actionFeedbackRole = computed(() => (actionFeedback.value?.type === "error" ? "alert" : "status"));
const channelNameError = computed(() => {
  const value = channelName.value.trim();
  if (!value) {
    return t("alert.channelNameRequired");
  }
  return /^[A-Za-z0-9._-]{1,64}$/.test(value) ? "" : t("alert.channelNameInvalid");
});
const channelSecretKey = computed(() => {
  if (channelType.value === "WEBHOOK") {
    return "hmac_secret";
  }
  if (channelType.value === "DINGTALK") {
    return "secret";
  }
  if (channelType.value === "EMAIL") {
    return "password";
  }
  return "";
});
const channelEndpointKey = computed(() => (channelType.value === "WEBHOOK" ? "url" : "webhook_url"));
const showEndpoint = computed(() => channelType.value !== "WEB");
const showSecret = computed(() => Boolean(channelSecretKey.value));

watch(
  () => policyQuery.data.value?.policy,
  (policy) => {
    if (!policy || policyHydrated.value) {
      return;
    }
    applyPolicy(policy);
    policyHydrated.value = true;
  },
  { immediate: true },
);

watch(channelType, (type) => {
  if (hydratingChannel.value) {
    return;
  }
  if (type === "WEB") {
    channelEndpoint.value = "";
    channelSecret.value = "";
  }
});

function mutationMeta() {
  return {
    operationId: newOperationId(),
    operator: session.value?.username ?? "",
  };
}

function notify(type: "success" | "error", message: string): void {
  actionFeedback.value = { type, message };
}

function resetFilters(): void {
  stateFilter.value = "ALL";
  nodeFilter.value = "";
  severityFilter.value = "ALL";
  typeFilter.value = "ALL";
  searchQuery.value = "";
}

async function refreshAlerts(): Promise<void> {
  try {
    await listQuery.refetch();
  } catch (error) {
    notify("error", formatRemoteError(error));
  }
}

async function openDetail(row: AlertRow): Promise<void> {
  detailRow.value = row;
  detailError.value = "";
  copyState.value = "idle";
  if (!row.alertId) {
    return;
  }
  detailLoading.value = true;
  try {
    const response = await client.getAlert({ alertId: row.alertId });
    if (response.entry) {
      detailRow.value = mapEntry(response.entry as AlertEntryInput, 0);
    }
  } catch (error) {
    detailError.value = formatRemoteError(error);
  } finally {
    detailLoading.value = false;
  }
}

function closeDetail(): void {
  detailRow.value = null;
  detailError.value = "";
  copyState.value = "idle";
}

async function copyFingerprint(): Promise<void> {
  const fingerprint = detailRow.value?.fingerprint;
  if (!fingerprint || fingerprint === "—") {
    return;
  }
  try {
    await navigator.clipboard.writeText(fingerprint);
    copyState.value = "copied";
  } catch {
    copyState.value = "failed";
  }
}

function resetChannelForm(): void {
  channelId.value = "";
  channelType.value = "WEBHOOK";
  channelName.value = "";
  channelEnabled.value = true;
  channelEndpoint.value = "";
  channelSecret.value = "";
  channelSmtpHost.value = "";
  channelSmtpPort.value = 25;
  channelUsername.value = "";
  channelFrom.value = "";
  channelRecipients.value = "";
  channelStartTls.value = false;
  channelConfig.value = "{}";
  channelAdvanced.value = false;
}

function openCreateChannel(): void {
  resetChannelForm();
  channelDrawerOpen.value = true;
}

function closeChannelDrawer(): void {
  if (putChannelMut.isPending.value) {
    return;
  }
  channelDrawerOpen.value = false;
  resetChannelForm();
}

function hydrateChannelConfig(raw: string): void {
  const config = parseConfig(raw);
  channelConfig.value = redactConfigJson(raw);
  channelEndpoint.value = String(config.url ?? config.webhook_url ?? "");
  channelSmtpHost.value = String(config.smtp_host ?? "");
  channelSmtpPort.value = Number(config.smtp_port ?? 25);
  channelUsername.value = String(config.username ?? "");
  channelFrom.value = String(config.from ?? "");
  channelRecipients.value = Array.isArray(config.to) ? config.to.join(", ") : String(config.to ?? "");
  channelStartTls.value = Boolean(config.starttls);
  channelSecret.value = "";
}

function loadChannel(channel: ChannelRow): void {
  if (!canManage.value) {
    return;
  }
  hydratingChannel.value = true;
  channelId.value = channel.channelId;
  channelType.value = CHANNEL_TYPES.includes(channel.type as ChannelType)
    ? (channel.type as ChannelType)
    : "WEBHOOK";
  channelName.value = channel.name;
  channelEnabled.value = channel.enabled;
  hydrateChannelConfig(channel.configJson);
  hydratingChannel.value = false;
  activeView.value = "settings";
  settingsSection.value = "channels";
  channelDrawerOpen.value = true;
}

function parseRecipients(): string[] {
  return channelRecipients.value
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);
}

function buildChannelConfig(): string {
  if (channelAdvanced.value) {
    return channelConfig.value.trim() || "{}";
  }
  const config = parseConfig(channelConfig.value);
  for (const key of ["url", "webhook_url", "smtp_host", "smtp_port", "username", "from", "to", "starttls"]) {
    delete config[key];
  }
  if (channelType.value !== "WEB") {
    if (channelType.value === "EMAIL") {
      config.smtp_host = channelSmtpHost.value.trim();
      config.smtp_port = Number(channelSmtpPort.value || 25);
      config.username = channelUsername.value.trim();
      config.from = channelFrom.value.trim();
      config.to = parseRecipients();
      config.starttls = channelStartTls.value;
    } else {
      config[channelEndpointKey.value] = channelEndpoint.value.trim();
    }
  }
  if (channelSecret.value.trim() && channelSecretKey.value) {
    config[channelSecretKey.value] = channelSecret.value.trim();
  }
  return JSON.stringify(config);
}

const putChannelMut = useMutation({
  mutationFn: () =>
    client.putAlertChannel({
      meta: mutationMeta(),
      channelId: channelId.value,
      type: channelType.value,
      name: channelName.value.trim(),
      enabled: channelEnabled.value,
      configJson: buildChannelConfig(),
    }),
  onSuccess: async () => {
    channelDrawerOpen.value = false;
    resetChannelForm();
    notify("success", t("alert.channelSaved"));
    await queryClient.invalidateQueries({ queryKey: ["alert-channels"] });
  },
  onError: (error: unknown) => notify("error", formatRemoteError(error)),
});

const testChannelMut = useMutation({
  mutationFn: (id: string) =>
    client.testAlertChannel({
      meta: mutationMeta(),
      channelId: id,
    }),
  onSuccess: () => notify("success", t("alert.channelTestSent")),
  onError: (error: unknown) => notify("error", formatRemoteError(error)),
  onSettled: () => {
    testingChannelId.value = "";
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
    notify("success", t("alert.channelDeleted"));
    await queryClient.invalidateQueries({ queryKey: ["alert-channels"] });
  },
  onError: (error: unknown) => notify("error", formatRemoteError(error)),
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
  onSuccess: async (response) => {
    if (response?.policy) {
      applyPolicy(response.policy);
    }
    notify("success", t("alert.policySaved"));
    await queryClient.invalidateQueries({ queryKey: ["alert-policy"] });
  },
  onError: (error: unknown) => notify("error", formatRemoteError(error)),
});

const acting = computed(
  () => putChannelMut.isPending.value || deleteChannelMut.isPending.value || testChannelMut.isPending.value || putPolicyMut.isPending.value,
);
const channelSaving = computed(() => putChannelMut.isPending.value);
const policySaving = computed(() => putPolicyMut.isPending.value);
const deleteChannelPending = computed(() => deleteChannelMut.isPending.value);

async function testChannel(channel: ChannelRow): Promise<void> {
  if (!canManage.value || !channel.channelId || channel.type === "WEB" || acting.value) {
    return;
  }
  testingChannelId.value = channel.channelId;
  try {
    await testChannelMut.mutateAsync(channel.channelId);
  } catch {
    // onError already recorded
  }
}

async function onSaveChannel(): Promise<void> {
  if (!canManage.value || channelNameError.value || acting.value) {
    return;
  }
  try {
    await putChannelMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}

function requestDeleteChannel(channel: ChannelRow): void {
  if (!canManage.value || !channel.channelId || acting.value) {
    return;
  }
  channelToDelete.value = channel;
}

async function confirmDeleteChannel(): Promise<void> {
  const channel = channelToDelete.value;
  if (!channel || acting.value) {
    return;
  }
  try {
    await deleteChannelMut.mutateAsync(channel.channelId);
  } catch {
    // onError already recorded
  } finally {
    channelToDelete.value = null;
  }
}

async function onSavePolicy(): Promise<void> {
  if (!canManage.value || acting.value) {
    return;
  }
  try {
    await putPolicyMut.mutateAsync();
  } catch {
    // onError already recorded
  }
}
</script>

<template>
  <div class="page alerts-page">
    <header class="page-heading">
      <div>
        <div class="page-eyebrow"><AlertCircle :size="16" aria-hidden="true" /> {{ t("alert.eyebrow") }}</div>
        <h1>{{ t("alert.title") }}</h1>
      </div>
      <div class="heading-actions">
        <button
          type="button"
          class="btn"
          :class="{ 'is-active': activeView === 'settings' }"
          :aria-pressed="activeView === 'settings'"
          @click="activeView = activeView === 'settings' ? 'inbox' : 'settings'"
        >
          <Settings2 :size="17" aria-hidden="true" />
          {{ t("alert.settings") }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="listRefreshing" @click="refreshAlerts">
          <LoaderCircle v-if="listRefreshing" class="spin" :size="17" aria-hidden="true" />
          <RefreshCw v-else :size="17" aria-hidden="true" />
          {{ t("alert.refresh") }}
        </button>
      </div>
    </header>

    <div v-if="actionFeedback" class="action-feedback" :class="`feedback-${actionFeedback.type}`" :role="actionFeedbackRole">
      <CheckCircle2 v-if="actionFeedback.type === 'success'" :size="17" aria-hidden="true" />
      <AlertCircle v-else :size="17" aria-hidden="true" />
      <span>{{ actionFeedback.message }}</span>
      <button type="button" class="feedback-close" :aria-label="t('actions.close')" @click="actionFeedback = null">
        <X :size="16" aria-hidden="true" />
      </button>
    </div>

    <section class="summary-strip" :aria-label="t('alert.summary')" aria-live="polite">
      <div class="summary-item summary-critical">
        <span class="summary-value">{{ firingCount }}</span>
        <span class="summary-label">{{ t("alert.firing") }}</span>
      </div>
      <div class="summary-item">
        <span class="summary-value">{{ resolvedCount }}</span>
        <span class="summary-label">{{ t("alert.resolved") }}</span>
      </div>
      <div class="summary-item" :class="{ 'summary-warning': dataIssueCount > 0 }">
        <span class="summary-value">{{ dataIssueCount }}</span>
        <span class="summary-label">{{ t("alert.dataIssues") }}</span>
      </div>
      <div class="summary-meta">
        <span>{{ t("alert.lastRefresh", { age: lastRefresh }) }}</span>
        <FreshnessBadge :status="hasStale ? STALE : LIVE" />
      </div>
    </section>

    <div v-if="hasStale || listError" class="banner alert-stale-banner" role="status">
      <AlertCircle :size="18" aria-hidden="true" />
      <span>{{ t("alert.staleBanner") }}</span>
    </div>

    <section class="workspace-panel" :class="{ 'is-hidden': activeView !== 'inbox' }" aria-labelledby="alerts-list-title">
      <div class="section-heading">
        <div>
          <h2 id="alerts-list-title">{{ t("alert.inbox") }}</h2>
          <p class="section-hint">{{ t("alert.inboxHint", { count: rows.length }) }}</p>
        </div>
        <span class="result-count">{{ t("alert.resultCount", { count: rows.length }) }}</span>
      </div>

      <div class="filter-toolbar" role="region" :aria-label="t('alert.filters')">
        <div class="state-tabs" role="group" :aria-label="t('alert.stateFilter')">
          <button
            v-for="state in ([['ALL', 'alert.all'], ['FIRING', 'alert.firing'], ['RESOLVED', 'alert.resolved']] as const)"
            :key="state[0]"
            type="button"
            class="state-tab"
            :class="{ selected: stateFilter === state[0] }"
            :aria-pressed="stateFilter === state[0]"
            @click="stateFilter = state[0]"
          >
            {{ t(state[1]) }}
          </button>
        </div>
        <label class="filter-control search-control">
          <span class="sr-only">{{ t("alert.search") }}</span>
          <Search :size="16" aria-hidden="true" />
          <input v-model="searchQuery" type="search" :placeholder="t('alert.searchPlaceholder')" />
        </label>
        <label class="filter-control">
          <span>{{ t("alert.node") }}</span>
          <select v-model="nodeFilter">
            <option value="">{{ t("alert.allNodes") }}</option>
            <option v-for="node in nodeOptions" :key="node" :value="node">{{ shortId(node) }}</option>
          </select>
        </label>
        <label class="filter-control">
          <span>{{ t("alert.severity") }}</span>
          <select v-model="severityFilter">
            <option value="ALL">{{ t("alert.allSeverities") }}</option>
            <option v-for="severity in severityOptions" :key="severity" :value="severity">{{ severityLabel(severity) }}</option>
          </select>
        </label>
        <label class="filter-control">
          <span>{{ t("alert.type") }}</span>
          <select v-model="typeFilter">
            <option value="ALL">{{ t("alert.allTypes") }}</option>
            <option v-for="type in typeOptions" :key="type" :value="type">{{ typeLabel(type) }}</option>
          </select>
        </label>
        <button v-if="isFiltered" type="button" class="btn btn-quiet clear-filters" @click="resetFilters">
          <Filter :size="16" aria-hidden="true" />
          {{ t("alert.clearFilters") }}
        </button>
      </div>

      <p v-if="listPending" class="loading-state" role="status">
        <LoaderCircle class="spin" :size="18" aria-hidden="true" /> {{ t("alert.loading") }}
      </p>
      <div v-else-if="listError && !listQuery.data" class="error-state" role="alert">
        <AlertCircle :size="20" aria-hidden="true" />
        <div>
          <strong>{{ t("alert.loadFailed") }}</strong>
          <p>{{ listError }}</p>
          <button type="button" class="btn" @click="refreshAlerts">{{ t("alert.retry") }}</button>
        </div>
      </div>
      <template v-else>
        <div class="table-scroll desktop-alert-table">
          <table class="table alert-table">
            <thead>
              <tr>
                <th>{{ t("alert.alert") }}</th>
                <th>{{ t("alert.severity") }}</th>
                <th>{{ t("alert.state") }}</th>
                <th>{{ t("alert.node") }}</th>
                <th>{{ t("alert.process") }}</th>
                <th>{{ t("alert.freshness") }}</th>
                <th>{{ t("alert.lastUpdated") }}</th>
                <th><span class="sr-only">{{ t("alert.actions") }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in rows"
                :key="row.key"
                :data-freshness="row.freshness"
                :class="{ 'row-stale': row.freshness !== LIVE }"
              >
                <td class="alert-primary-cell">
                  <button type="button" class="alert-title-button" @click="openDetail(row)">
                    <span class="alert-type">{{ typeLabel(row.type) }}</span>
                    <span class="mono alert-fingerprint" :title="row.fingerprint">{{ shortId(row.fingerprint) }}</span>
                  </button>
                </td>
                <td>
                  <span class="severity-badge" :class="`severity-${row.severity.toLowerCase()}`">
                    <span class="status-dot" aria-hidden="true" />{{ severityLabel(row.severity) }}
                  </span>
                </td>
                <td>
                  <span
                    class="alert-state"
                    :class="{ 'alert-firing': row.state === 'FIRING', 'alert-resolved': row.state === 'RESOLVED' }"
                    :data-state="row.state || undefined"
                  >{{ stateLabel(row.state) }}</span>
                </td>
                <td class="entity-cell">
                  <a v-if="row.node !== '—'" class="entity-link mono" :href="`/nodes/${encodeURIComponent(row.node)}`" :title="row.node">{{ shortId(row.node) }}</a>
                  <span v-else>—</span>
                </td>
                <td class="entity-cell">
                  <a v-if="row.process" class="entity-link" :href="`/processes/${encodeURIComponent(row.process)}`">{{ row.process }}</a>
                  <span v-else>—</span>
                </td>
                <td><FreshnessBadge :status="row.freshness" /></td>
                <td class="time-cell" :title="formatDate(row.lastUpdatedUnixMs)">{{ row.lastUpdated }}</td>
                <td>
                  <button type="button" class="icon-text-button" @click="openDetail(row)">
                    <ExternalLink :size="15" aria-hidden="true" /> {{ t("alert.view") }}
                  </button>
                </td>
              </tr>
              <tr v-if="showEmptyInbox">
                <td colspan="8" class="empty-inbox">
                  <div class="empty-state">
                    <CheckCircle2 :size="28" aria-hidden="true" />
                    <strong>{{ isFiltered ? t("alert.noFilterResults") : t("alert.noAlerts") }}</strong>
                    <span>{{ isFiltered ? t("alert.clearFiltersHint") : t("alert.noAlertsHint") }}</span>
                    <button v-if="isFiltered" type="button" class="btn btn-quiet" @click="resetFilters">{{ t("alert.clearFilters") }}</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="mobile-alert-list">
          <button v-for="row in rows" :key="row.key" type="button" class="alert-mobile-item" @click="openDetail(row)">
            <span class="alert-mobile-topline">
              <span class="alert-type">{{ typeLabel(row.type) }}</span>
              <span class="severity-badge" :class="`severity-${row.severity.toLowerCase()}`"><span class="status-dot" aria-hidden="true" />{{ severityLabel(row.severity) }}</span>
            </span>
            <span class="mono alert-fingerprint" :title="row.fingerprint">{{ shortId(row.fingerprint) }}</span>
            <span class="alert-mobile-meta">
              <span>{{ shortId(row.node) }}</span>
              <span>{{ row.process || "—" }}</span>
              <span>{{ row.lastUpdated }}</span>
            </span>
            <span class="alert-mobile-bottomline">
              <span class="alert-state" :class="{ 'alert-firing': row.state === 'FIRING', 'alert-resolved': row.state === 'RESOLVED' }">{{ stateLabel(row.state) }}</span>
              <FreshnessBadge :status="row.freshness" />
            </span>
          </button>
          <div v-if="showEmptyInbox" class="empty-state mobile-empty-state">
            <CheckCircle2 :size="28" aria-hidden="true" />
            <strong>{{ isFiltered ? t("alert.noFilterResults") : t("alert.noAlerts") }}</strong>
            <span>{{ isFiltered ? t("alert.clearFiltersHint") : t("alert.noAlertsHint") }}</span>
            <button v-if="isFiltered" type="button" class="btn btn-quiet" @click="resetFilters">{{ t("alert.clearFilters") }}</button>
          </div>
        </div>
      </template>
    </section>

    <section class="workspace-panel settings-panel" :class="{ 'is-hidden': activeView !== 'settings' }" aria-labelledby="settings-title">
      <div class="section-heading">
        <div>
          <h2 id="settings-title">{{ t("alert.settings") }}</h2>
          <p class="section-hint">{{ t("alert.settingsHint") }}</p>
        </div>
        <Settings2 :size="20" aria-hidden="true" />
      </div>
      <div class="settings-tabs" role="tablist" :aria-label="t('alert.settings')">
        <button type="button" role="tab" :aria-selected="settingsSection === 'channels'" :class="{ selected: settingsSection === 'channels' }" @click="settingsSection = 'channels'">
          <Webhook :size="17" aria-hidden="true" /> {{ t("alert.channels") }}
        </button>
        <button type="button" role="tab" :aria-selected="settingsSection === 'policy'" :class="{ selected: settingsSection === 'policy' }" @click="settingsSection = 'policy'">
          <SlidersHorizontal :size="17" aria-hidden="true" /> {{ t("alert.policy") }}
        </button>
      </div>

      <p v-if="settingsError" class="error settings-error" role="alert">{{ settingsError }}</p>

      <section v-show="settingsSection === 'channels'" class="settings-section" aria-labelledby="channels-title">
        <div class="section-heading compact-heading">
          <div>
            <h3 id="channels-title">{{ t("alert.channels") }}</h3>
            <p class="section-hint">{{ t("alert.channelsHint") }}</p>
          </div>
          <div class="channel-heading-actions">
            <span class="result-count">{{ channels.length }}</span>
            <button v-if="canManage" type="button" class="btn btn-primary" data-action="create-channel" @click="openCreateChannel">
              <Plus :size="17" aria-hidden="true" /> {{ t("alert.createChannel") }}
            </button>
          </div>
        </div>
        <div class="channel-list">
          <div v-for="channel in channels" :key="channel.channelId || channel.name" class="channel-row">
            <button v-if="canManage" type="button" class="channel-main" @click="loadChannel(channel)">
              <span class="channel-icon" aria-hidden="true"><Mail v-if="channel.type === 'EMAIL'" :size="18" /><Webhook v-else :size="18" /></span>
              <span class="channel-copy">
                <strong>{{ channel.name }}</strong>
                <span>{{ channelTypeLabel(channel.type) }} · {{ channel.summary }}</span>
              </span>
            </button>
            <div v-else class="channel-main">
              <span class="channel-icon" aria-hidden="true"><Mail v-if="channel.type === 'EMAIL'" :size="18" /><Webhook v-else :size="18" /></span>
              <span class="channel-copy"><strong>{{ channel.name }}</strong><span>{{ channelTypeLabel(channel.type) }} · {{ channel.summary }}</span></span>
            </div>
            <div class="channel-row-actions">
              <span class="channel-status" :class="channel.enabled ? 'status-enabled' : 'status-disabled'">
                <span class="status-dot" aria-hidden="true" />{{ t(channel.enabled ? "alert.enabled" : "alert.disabled") }}
              </span>
              <button v-if="canManage && channel.type !== 'WEB'" type="button" class="icon-text-button" data-action="test-channel" :disabled="acting" @click="testChannel(channel)">
                <LoaderCircle v-if="testingChannelId === channel.channelId" class="spin" :size="16" aria-hidden="true" />
                <Send v-else :size="16" aria-hidden="true" /> {{ t("alert.testChannel") }}
              </button>
              <button v-if="canManage" type="button" class="icon-button danger-button" :aria-label="t('alert.deleteChannel', { name: channel.name })" :disabled="acting" @click="requestDeleteChannel(channel)">
                <Trash2 :size="17" aria-hidden="true" />
              </button>
            </div>
          </div>
          <div v-if="!channels.length" class="settings-empty">
            <Webhook :size="24" aria-hidden="true" />
            <strong>{{ t("alert.noChannels") }}</strong>
            <span>{{ t("alert.noChannelsHint") }}</span>
          </div>
        </div>
      </section>

      <form v-show="settingsSection === 'policy'" class="policy-form settings-section" @submit.prevent="onSavePolicy">
        <div class="form-section-heading">
          <div>
            <h3>{{ t("alert.policy") }}</h3>
            <p class="section-hint">{{ t("alert.policyHint") }}</p>
          </div>
        </div>
        <div class="policy-grid">
          <label class="field policy-field"><span>{{ t("alert.dedupWindowSec") }}</span><input v-model.number="dedupWindowSec" class="input" name="dedupWindowSec" type="number" min="1" :disabled="!canManage" /><small>{{ t("alert.dedupWindowHint") }}</small></label>
          <label class="toggle-field policy-toggle"><input v-model="notifyOnResolve" name="notifyOnResolve" type="checkbox" :disabled="!canManage" /><span><strong>{{ t("alert.notifyOnResolve") }}</strong><small>{{ t("alert.notifyOnResolveHint") }}</small></span></label>
          <label class="field policy-field"><span>{{ t("alert.cpuHighPercent") }}</span><input v-model.number="cpuHighPercent" class="input" name="cpuHighPercent" type="number" min="1" max="100" :disabled="!canManage" /><small>{{ t("alert.thresholdHint", { duration: highConsecutiveMins }) }}</small></label>
          <label class="field policy-field"><span>{{ t("alert.memoryHighPercent") }}</span><input v-model.number="memoryHighPercent" class="input" name="memoryHighPercent" type="number" min="1" max="100" :disabled="!canManage" /><small>{{ t("alert.thresholdHint", { duration: highConsecutiveMins }) }}</small></label>
          <label class="field policy-field"><span>{{ t("alert.diskHighPercent") }}</span><input v-model.number="diskHighPercent" class="input" name="diskHighPercent" type="number" min="1" max="100" :disabled="!canManage" /><small>{{ t("alert.thresholdHint", { duration: highConsecutiveMins }) }}</small></label>
          <label class="field policy-field"><span>{{ t("alert.highConsecutiveMins") }}</span><input v-model.number="highConsecutiveMins" class="input" name="highConsecutiveMins" type="number" min="1" max="60" :disabled="!canManage" /><small>{{ t("alert.consecutiveHint") }}</small></label>
          <label class="field policy-field"><span>{{ t("alert.suspectTooLongSec") }}</span><input v-model.number="suspectTooLongSec" class="input" name="suspectTooLongSec" type="number" min="1" max="86400" :disabled="!canManage" /><small>{{ t("alert.suspectHint") }}</small></label>
        </div>
        <div v-if="canManage" class="form-actions"><button class="btn btn-primary" type="submit" data-action="save-policy" :disabled="acting" :aria-busy="policySaving"><LoaderCircle v-if="policySaving" class="spin" :size="17" aria-hidden="true" />{{ policySaving ? t("alert.saving") : t("alert.savePolicy") }}</button></div>
      </form>
    </section>
  </div>

  <Drawer :open="channelDrawerOpen" :title="channelId ? t('alert.editChannel') : t('alert.createChannel')" :close-label="t('actions.close')" @close="closeChannelDrawer">
    <form v-if="canManage" class="channel-form drawer-form" @submit.prevent="onSaveChannel">
      <p class="section-hint drawer-form-hint">{{ t("alert.channelFormHint") }}</p>
      <div class="form-grid form-grid-two">
        <label class="field">
          <span>{{ t("alert.name") }}</span>
          <input v-model="channelName" class="input" name="channelName" type="text" autocomplete="off" maxlength="64" pattern="[A-Za-z0-9._-]{1,64}" required />
          <small v-if="channelName && channelNameError" class="field-error">{{ channelNameError }}</small>
        </label>
        <label class="field">
          <span>{{ t("alert.type") }}</span>
          <select v-model="channelType" class="input" name="channelType">
            <option v-for="type in CHANNEL_TYPES" :key="type" :value="type">{{ channelTypeLabel(type) }}</option>
          </select>
        </label>
      </div>
      <label class="toggle-field">
        <input v-model="channelEnabled" name="channelEnabled" type="checkbox" />
        <span><strong>{{ t("alert.enabled") }}</strong><small>{{ t("alert.enabledHint") }}</small></span>
      </label>
      <div v-if="channelType === 'EMAIL'" class="form-grid form-grid-two">
        <label class="field"><span>{{ t("alert.smtpHost") }}</span><input v-model="channelSmtpHost" class="input" name="smtpHost" type="text" /></label>
        <label class="field"><span>{{ t("alert.smtpPort") }}</span><input v-model.number="channelSmtpPort" class="input" name="smtpPort" type="number" min="1" max="65535" /></label>
        <label class="field"><span>{{ t("alert.username") }}</span><input v-model="channelUsername" class="input" name="smtpUsername" type="text" autocomplete="username" /></label>
        <label class="field"><span>{{ t("alert.sender") }}</span><input v-model="channelFrom" class="input" name="sender" type="email" /></label>
        <label class="field field-wide"><span>{{ t("alert.recipients") }}</span><input v-model="channelRecipients" class="input" name="recipients" type="text" :placeholder="t('alert.recipientsPlaceholder')" /></label>
        <label class="toggle-field"><input v-model="channelStartTls" name="startTls" type="checkbox" /><span><strong>{{ t("alert.startTls") }}</strong><small>{{ t("alert.startTlsHint") }}</small></span></label>
      </div>
      <label v-if="showEndpoint" class="field">
        <span>{{ t("alert.webhookUrl") }}</span>
        <input v-model="channelEndpoint" class="input" name="channelEndpoint" type="url" :placeholder="t('alert.webhookUrlPlaceholder')" />
      </label>
      <label v-if="showSecret" class="field">
        <span>{{ channelType === 'EMAIL' ? t("alert.password") : t("alert.signingSecret") }}</span>
        <input v-model="channelSecret" class="input" name="channelSecret" type="password" autocomplete="new-password" :placeholder="t('alert.secretPlaceholder')" />
      </label>
      <details class="advanced-config">
        <summary>{{ t("alert.advancedConfig") }}</summary>
        <label class="field">
          <span>{{ t("alert.configJson") }}</span>
          <textarea v-model="channelConfig" class="input textarea" name="channelConfig" rows="5" spellcheck="false" />
          <small>{{ t("alert.configJsonHint") }}</small>
        </label>
        <label class="toggle-field advanced-toggle"><input v-model="channelAdvanced" type="checkbox" /><span><strong>{{ t("alert.useAdvancedConfig") }}</strong><small>{{ t("alert.useAdvancedConfigHint") }}</small></span></label>
      </details>
      <div class="form-actions drawer-form-actions">
        <button class="btn" type="button" :disabled="channelSaving" @click="closeChannelDrawer">{{ t("actions.cancel") }}</button>
        <button class="btn btn-primary" type="submit" data-action="save-channel" :disabled="Boolean(channelNameError) || acting" :aria-busy="channelSaving">
          <LoaderCircle v-if="channelSaving" class="spin" :size="17" aria-hidden="true" />
          {{ channelSaving ? t("alert.saving") : t("alert.saveChannel") }}
        </button>
      </div>
    </form>
  </Drawer>

  <Drawer :open="Boolean(detailRow)" :title="detailTitle" :close-label="t('actions.close')" size="wide" @close="closeDetail">
    <div v-if="detailLoading" class="drawer-loading" role="status"><LoaderCircle class="spin" :size="22" aria-hidden="true" /> {{ t("alert.loadingDetail") }}</div>
    <div v-else-if="detailRow" class="alert-detail">
      <div v-if="detailError" class="detail-error" role="alert"><AlertCircle :size="18" aria-hidden="true" />{{ detailError }}</div>
      <div class="detail-status-row">
        <span class="severity-badge" :class="`severity-${detailRow.severity.toLowerCase()}`"><span class="status-dot" aria-hidden="true" />{{ severityLabel(detailRow.severity) }}</span>
        <span class="alert-state" :class="{ 'alert-firing': detailRow.state === 'FIRING', 'alert-resolved': detailRow.state === 'RESOLVED' }">{{ stateLabel(detailRow.state) }}</span>
        <FreshnessBadge :status="detailRow.freshness" />
      </div>
      <section class="detail-section">
        <div class="detail-section-heading"><h3>{{ t("alert.identity") }}</h3><button type="button" class="icon-text-button" @click="copyFingerprint"><Clipboard :size="15" aria-hidden="true" />{{ copyState === 'copied' ? t('alert.copied') : t('alert.copyFingerprint') }}</button></div>
        <dl class="detail-facts">
          <div><dt>{{ t("alert.type") }}</dt><dd>{{ typeLabel(detailRow.type) }}</dd></div>
          <div><dt>{{ t("alert.fingerprint") }}</dt><dd class="mono breakable">{{ detailRow.fingerprint }}</dd></div>
          <div><dt>{{ t("alert.node") }}</dt><dd><a v-if="detailRow.node !== '—'" class="entity-link mono" :href="`/nodes/${encodeURIComponent(detailRow.node)}`">{{ detailRow.node }}</a><span v-else>—</span></dd></div>
          <div><dt>{{ t("alert.process") }}</dt><dd><a v-if="detailRow.process" class="entity-link" :href="`/processes/${encodeURIComponent(detailRow.process)}`">{{ detailRow.process }}</a><span v-else>—</span></dd></div>
          <div><dt>{{ t("alert.sourceNode") }}</dt><dd class="mono breakable">{{ detailRow.sourceNode || "—" }}</dd></div>
        </dl>
      </section>
      <section class="detail-section"><h3>{{ t("alert.timeline") }}</h3><dl class="detail-facts"><div><dt>{{ t("alert.firstSeen") }}</dt><dd>{{ formatDate(detailRow.firstUnixMs) }}</dd></div><div><dt>{{ t("alert.lastSeen") }}</dt><dd>{{ formatDate(detailRow.lastUnixMs) }}</dd></div><div><dt>{{ t("alert.resolvedAt") }}</dt><dd>{{ formatDate(detailRow.resolvedUnixMs) }}</dd></div><div><dt>{{ t("alert.notifiedAt") }}</dt><dd>{{ formatDate(detailRow.notifiedUnixMs) }}</dd></div></dl></section>
      <section v-if="detailRow.lastError" class="detail-section detail-error-block"><h3>{{ t("alert.notificationError") }}</h3><pre>{{ detailRow.lastError }}</pre></section>
      <section class="detail-section"><h3>{{ t("alert.payload") }}</h3><pre class="payload-block">{{ prettyJson(detailRow.payloadJson) }}</pre></section>
    </div>
  </Drawer>

  <ConfirmDialog
    :open="Boolean(channelToDelete)"
    :title="t('alert.deleteChannelTitle')"
    :message="t('alert.deleteChannelMessage', { name: channelToDelete?.name ?? '' })"
    :confirm-label="t('alert.delete')"
    :cancel-label="t('actions.cancel')"
    :pending="deleteChannelPending"
    @cancel="channelToDelete = null"
    @confirm="confirmDeleteChannel"
  />
</template>

<style scoped>
.alerts-page {
  gap: 1.25rem;
}

.page-heading,
.section-heading,
.form-section-heading,
.detail-section-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
}

.page-eyebrow {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  margin-bottom: 0.35rem;
  color: var(--color-accent);
  font-size: 0.78rem;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

h1,
h2,
h3,
p {
  margin-top: 0;
}

h1 {
  margin-bottom: 0;
  font-size: clamp(1.5rem, 2vw, 2rem);
  font-weight: 700;
  letter-spacing: 0;
}

h2 {
  margin-bottom: 0.25rem;
  font-size: 1.12rem;
  font-weight: 700;
}

h3 {
  margin-bottom: 0.25rem;
  font-size: 1rem;
  font-weight: 700;
}

.heading-actions,
.form-actions,
.detail-status-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.5rem;
}

.btn.is-active {
  border-color: color-mix(in srgb, var(--color-accent) 48%, var(--color-border));
  background: color-mix(in srgb, var(--color-accent) 10%, var(--color-card));
  color: var(--color-accent);
}

.btn-quiet {
  border-color: transparent;
  background: transparent;
  box-shadow: none;
}

.btn-quiet:hover {
  border-color: var(--color-border);
  background: var(--color-bg);
  box-shadow: none;
}

.action-feedback {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  min-height: 2.75rem;
  padding: 0.65rem 0.8rem;
  border: 1px solid var(--color-border);
  border-left: 4px solid var(--color-accent);
  border-radius: 8px;
  background: var(--color-card);
  font-size: 0.875rem;
}

.action-feedback.feedback-error {
  border-left-color: var(--color-danger);
  color: var(--color-danger);
}

.feedback-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  margin-left: auto;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
}

.summary-strip {
  display: flex;
  align-items: stretch;
  flex-wrap: wrap;
  gap: 0;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  box-shadow: var(--shadow-sm);
}

.summary-item {
  display: flex;
  min-width: 7.5rem;
  align-items: baseline;
  gap: 0.5rem;
  padding: 0.85rem 1rem;
  border-right: 1px solid var(--color-border);
}

.summary-value {
  font-size: 1.35rem;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.summary-label,
.summary-meta {
  color: var(--color-muted);
  font-size: 0.8rem;
}

.summary-critical .summary-value {
  color: var(--color-danger);
}

.summary-warning .summary-value {
  color: #a16207;
}

.summary-meta {
  display: flex;
  align-items: center;
  gap: 0.65rem;
  margin-left: auto;
  padding: 0.85rem 1rem;
}

.banner {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  border-radius: 8px;
  padding: 0.75rem 1rem;
  font-size: 0.875rem;
  line-height: 1.4;
}

.alert-stale-banner {
  background: var(--color-stale);
  color: var(--color-stale-fg);
}

.workspace-panel {
  min-width: 0;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  box-shadow: var(--shadow-sm);
}

.workspace-panel.is-hidden {
  display: none;
}

.workspace-panel > .section-heading,
.settings-panel > .section-heading {
  padding: 1.25rem 1.25rem 0;
}

.section-hint {
  margin-bottom: 0;
  color: var(--color-muted);
  font-size: 0.82rem;
  line-height: 1.45;
}

.result-count {
  flex: 0 0 auto;
  color: var(--color-muted);
  font-size: 0.8rem;
  font-variant-numeric: tabular-nums;
}

.filter-toolbar {
  display: flex;
  align-items: flex-end;
  flex-wrap: wrap;
  gap: 0.65rem;
  padding: 1rem 1.25rem;
  border-top: 1px solid var(--color-border);
  border-bottom: 1px solid var(--color-border);
  background: color-mix(in srgb, var(--color-bg) 55%, var(--color-card));
}

.state-tabs,
.settings-tabs {
  display: inline-flex;
  align-items: center;
  gap: 0.2rem;
  padding: 0.2rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
}

.state-tab,
.settings-tabs button {
  display: inline-flex;
  min-height: 2.5rem;
  align-items: center;
  justify-content: center;
  gap: 0.4rem;
  padding: 0.45rem 0.75rem;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--color-muted);
  font-size: 0.82rem;
  font-weight: 600;
  cursor: pointer;
}

.state-tab.selected,
.settings-tabs button.selected {
  background: var(--color-text);
  color: var(--color-card);
}

.filter-control {
  display: flex;
  min-width: 9rem;
  flex: 1 1 9rem;
  flex-direction: column;
  gap: 0.3rem;
  color: var(--color-muted);
  font-size: 0.75rem;
  font-weight: 600;
}

.filter-control select,
.filter-control input {
  min-width: 0;
  min-height: 2.75rem;
  border: 1px solid var(--color-border);
  border-radius: 7px;
  background: var(--color-card);
  color: var(--color-text);
  padding: 0.55rem 0.7rem;
  font: inherit;
  font-size: 0.85rem;
}

.search-control {
  position: relative;
  min-width: min(18rem, 100%);
  flex-basis: 14rem;
}

.search-control > svg {
  position: absolute;
  left: 0.7rem;
  bottom: 0.78rem;
  color: var(--color-muted);
  pointer-events: none;
}

.search-control input {
  padding-left: 2.1rem;
}

.clear-filters {
  min-height: 2.75rem;
  white-space: nowrap;
}

.table-scroll {
  overflow-x: auto;
}

.alert-table {
  min-width: 64rem;
}

.alert-table th,
.alert-table td {
  padding: 0.85rem 0.8rem;
  vertical-align: middle;
}

.alert-table th {
  white-space: nowrap;
  text-transform: none;
  letter-spacing: 0;
}

.alert-primary-cell {
  min-width: 17rem;
}

.alert-title-button {
  display: flex;
  min-width: 0;
  flex-direction: column;
  align-items: flex-start;
  gap: 0.28rem;
  padding: 0.15rem 0;
  border: 0;
  background: transparent;
  color: var(--color-text);
  text-align: left;
  cursor: pointer;
}

.alert-title-button:hover .alert-type {
  color: var(--color-accent);
}

.alert-type {
  font-weight: 700;
}

.alert-fingerprint {
  max-width: 18rem;
  overflow: hidden;
  color: var(--color-muted);
  font-size: 0.75rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.severity-badge,
.channel-status {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  border-radius: 999px;
  padding: 0.28rem 0.55rem;
  font-size: 0.75rem;
  font-weight: 700;
  white-space: nowrap;
}

.severity-critical {
  background: color-mix(in srgb, var(--color-danger) 12%, transparent);
  color: var(--color-danger);
}

.severity-warning {
  background: #fef3c7;
  color: #92400e;
}

.severity-unknown {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}

.status-dot {
  width: 0.42rem;
  height: 0.42rem;
  flex: 0 0 0.42rem;
  border-radius: 50%;
  background: currentColor;
}

.alert-state {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  font-weight: 700;
  white-space: nowrap;
}

.alert-state::before {
  width: 0.45rem;
  height: 0.45rem;
  border-radius: 50%;
  background: currentColor;
  content: "";
}

.alert-firing {
  color: var(--color-danger);
}

.alert-resolved {
  color: var(--color-live-fg);
}

.row-stale {
  background: color-mix(in srgb, var(--color-stale) 24%, var(--color-card));
}

.entity-cell {
  max-width: 12rem;
}

.entity-link {
  color: var(--color-accent);
  text-decoration: none;
}

.entity-link:hover {
  text-decoration: underline;
}

.time-cell {
  color: var(--color-muted);
  font-size: 0.8rem;
  white-space: nowrap;
}

.icon-text-button,
.icon-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 0.35rem;
  min-height: 2.75rem;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--color-accent);
  font-size: 0.8rem;
  font-weight: 600;
  cursor: pointer;
}

.icon-button {
  width: 2.75rem;
  padding: 0;
}

.icon-text-button:hover,
.icon-button:hover {
  background: color-mix(in srgb, var(--color-accent) 10%, transparent);
}

.danger-button {
  color: var(--color-danger);
}

.danger-button:hover {
  background: color-mix(in srgb, var(--color-danger) 10%, transparent);
}

.empty-inbox {
  padding: 2.5rem 1rem !important;
}

.empty-state,
.settings-empty {
  display: flex;
  align-items: center;
  flex-direction: column;
  gap: 0.5rem;
  color: var(--color-muted);
  text-align: center;
}

.empty-state svg,
.settings-empty svg {
  color: var(--color-live-fg);
}

.empty-state strong,
.settings-empty strong {
  color: var(--color-text);
}

.empty-state span,
.settings-empty span {
  font-size: 0.82rem;
}

.mobile-alert-list {
  display: none;
}

.settings-panel {
  padding-bottom: 1.25rem;
}

.settings-panel > .section-heading {
  padding-bottom: 1rem;
}

.settings-tabs {
  margin: 0 1.25rem 1.25rem;
}

.settings-section {
  padding: 0 1.25rem;
}

.compact-heading {
  align-items: center;
  margin-bottom: 0.8rem;
}

.channel-heading-actions,
.channel-row-actions {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.channel-list {
  border: 1px solid var(--color-border);
  border-radius: 8px;
  overflow: hidden;
}

.channel-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  min-width: 0;
  padding: 0.75rem 0.85rem;
  border-bottom: 1px solid var(--color-border);
}

.channel-row:last-child {
  border-bottom: 0;
}

.channel-main {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 0.7rem;
  padding: 0.2rem;
  border: 0;
  background: transparent;
  color: var(--color-text);
  text-align: left;
  cursor: pointer;
}

.channel-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2.25rem;
  height: 2.25rem;
  flex: 0 0 2.25rem;
  border-radius: 8px;
  background: color-mix(in srgb, var(--color-accent) 10%, transparent);
  color: var(--color-accent);
}

.channel-copy {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 0.18rem;
}

.channel-copy strong,
.channel-copy span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.channel-copy span {
  color: var(--color-muted);
  font-size: 0.8rem;
}

.status-enabled {
  background: var(--color-live);
  color: var(--color-live-fg);
}

.status-disabled {
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
}

.settings-empty {
  padding: 2rem 1rem;
}

.drawer-form {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.drawer-form-hint {
  margin-bottom: 0;
}

.drawer-form-actions {
  justify-content: flex-end;
  padding-top: 0.25rem;
}

.form-section {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  margin-top: 1.25rem;
  padding: 1.15rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: color-mix(in srgb, var(--color-bg) 48%, var(--color-card));
}

.form-grid,
.policy-grid {
  display: grid;
  gap: 1rem;
}

.form-grid-two {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.policy-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.field-wide {
  grid-column: 1 / -1;
}

.field {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 0.35rem;
  color: var(--color-muted);
  font-size: 0.82rem;
  font-weight: 600;
}

.field small,
.toggle-field small {
  color: var(--color-muted);
  font-size: 0.75rem;
  font-weight: 400;
  line-height: 1.4;
}

.field-error {
  color: var(--color-danger) !important;
}

.input {
  min-height: 2.75rem;
}

.textarea {
  min-height: 7rem;
  resize: vertical;
}

.toggle-field {
  display: flex;
  min-height: 2.75rem;
  align-items: flex-start;
  gap: 0.65rem;
  color: var(--color-text);
  font-size: 0.85rem;
}

.toggle-field input {
  width: 1.2rem;
  height: 1.2rem;
  flex: 0 0 1.2rem;
  margin-top: 0.1rem;
  accent-color: var(--color-accent);
}

.toggle-field > span {
  display: flex;
  flex-direction: column;
  gap: 0.18rem;
}

.advanced-config {
  border-top: 1px solid var(--color-border);
  padding-top: 0.8rem;
}

.advanced-config summary {
  min-height: 2.75rem;
  color: var(--color-muted);
  cursor: pointer;
  font-size: 0.82rem;
  font-weight: 600;
}

.advanced-config[open] summary {
  margin-bottom: 0.8rem;
  color: var(--color-text);
}

.advanced-toggle {
  margin-top: 0.8rem;
}

.policy-form {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.policy-field,
.policy-toggle {
  padding: 0.85rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
}

.loading-state,
.drawer-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.55rem;
  min-height: 8rem;
  color: var(--color-muted);
}

.error-state {
  display: flex;
  align-items: flex-start;
  gap: 0.7rem;
  margin: 1rem;
  padding: 1rem;
  border: 1px solid color-mix(in srgb, var(--color-danger) 30%, var(--color-border));
  border-radius: 8px;
  color: var(--color-danger);
}

.error-state p {
  margin: 0.35rem 0 0.75rem;
  color: var(--color-text);
  font-size: 0.85rem;
}

.error-state .btn {
  color: var(--color-text);
}

.settings-error {
  margin: 0 1.25rem 1rem;
}

.drawer-loading {
  min-height: 12rem;
}

.alert-detail {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
}

.detail-error {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.7rem 0.8rem;
  border-radius: 8px;
  background: color-mix(in srgb, var(--color-danger) 10%, transparent);
  color: var(--color-danger);
  font-size: 0.85rem;
}

.detail-section {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding-top: 1rem;
  border-top: 1px solid var(--color-border);
}

.detail-section h3 {
  margin: 0;
}

.detail-facts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem;
  margin: 0;
}

.detail-facts div {
  min-width: 0;
  padding: 0.7rem;
  border-radius: 7px;
  background: var(--color-bg);
}

.detail-facts dt {
  margin-bottom: 0.25rem;
  color: var(--color-muted);
  font-size: 0.72rem;
  font-weight: 600;
}

.detail-facts dd {
  margin: 0;
  font-size: 0.85rem;
}

.breakable {
  overflow-wrap: anywhere;
}

.payload-block,
.detail-error-block pre {
  max-height: 18rem;
  overflow: auto;
  margin: 0;
  padding: 0.85rem;
  border: 1px solid var(--color-border);
  border-radius: 7px;
  background: #111827;
  color: #e5e7eb;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.78rem;
  line-height: 1.55;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.detail-error-block pre {
  max-height: 10rem;
  background: color-mix(in srgb, var(--color-danger) 8%, var(--color-card));
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

.spin {
  animation: alert-spin 0.9s linear infinite;
}

@keyframes alert-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 860px) {
  .channel-row {
    flex-wrap: wrap;
  }

  .channel-row-actions {
    width: 100%;
    justify-content: flex-end;
  }
  .summary-meta {
    width: 100%;
    margin-left: 0;
    border-top: 1px solid var(--color-border);
  }

  .summary-item {
    flex: 1 1 7rem;
  }

  .filter-toolbar {
    align-items: stretch;
  }

  .state-tabs,
  .search-control,
  .filter-control,
  .clear-filters {
    flex: 1 1 100%;
  }

  .state-tab {
    flex: 1;
  }

  .desktop-alert-table {
    display: none;
  }

  .mobile-alert-list {
    display: flex;
    flex-direction: column;
    gap: 0.65rem;
    padding: 0.85rem;
  }

  .alert-mobile-item {
    display: flex;
    min-width: 0;
    flex-direction: column;
    align-items: stretch;
    gap: 0.6rem;
    padding: 0.9rem;
    border: 1px solid var(--color-border);
    border-radius: 8px;
    background: var(--color-card);
    color: var(--color-text);
    text-align: left;
    cursor: pointer;
  }

  .alert-mobile-item:hover {
    border-color: color-mix(in srgb, var(--color-accent) 50%, var(--color-border));
  }

  .alert-mobile-topline,
  .alert-mobile-bottomline,
  .alert-mobile-meta {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }

  .alert-mobile-meta {
    justify-content: flex-start;
    color: var(--color-muted);
    font-size: 0.78rem;
  }

  .alert-mobile-meta span {
    max-width: 33%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mobile-empty-state {
    padding: 2rem 1rem;
  }

  .form-grid-two,
  .policy-grid {
    grid-template-columns: 1fr;
  }

  .field-wide {
    grid-column: auto;
  }
}

@media (max-width: 520px) {
  .page-heading,
  .section-heading,
  .form-section-heading,
  .detail-section-heading {
    flex-direction: column;
  }

  .heading-actions {
    width: 100%;
  }

  .heading-actions .btn {
    flex: 1;
  }

  .settings-tabs {
    display: flex;
    margin-left: 1rem;
    margin-right: 1rem;
  }

  .settings-tabs button {
    flex: 1;
  }

  .settings-section,
  .workspace-panel > .section-heading,
  .settings-panel > .section-heading {
    padding-left: 1rem;
    padding-right: 1rem;
  }

  .detail-facts {
    grid-template-columns: 1fr;
  }
}

@media (prefers-reduced-motion: reduce) {
  .spin {
    animation: none;
  }
}
</style>
