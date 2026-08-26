<script setup lang="ts">
import { create, toJson } from "@bufbuild/protobuf";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, nextTick, ref, watch, type ComponentPublicInstance } from "vue";
import { FileCode2, Pencil, RefreshCw } from "lucide-vue-next";
import { stringify as stringifyYaml } from "yaml";
import Drawer from "../components/Drawer.vue";
import { ProcessSpecSchema, type ProcessSpec } from "../gen/procmesh/v1/api_pb";
import { isConflict } from "../lib/connecterr";
import { withTarget } from "../lib/headers";
import { newOperationId } from "../lib/opid";
import { useConfigClient, useProcessClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import ProcessConfigForm from "./ProcessConfigForm.vue";
import {
  parseProcessConfigYaml,
  processConfigToYamlValue,
  processConfigFormToSpec,
  specToProcessConfigForm,
  stringifyProcessConfigYaml,
  validateProcessConfigForm,
  validateProcessSpec,
  type ProcessConfigFormState,
  type ProcessConfigIssue,
} from "./processConfigForm";
import { formatRemoteError } from "./processView";

const { t } = useI18n();

const CONFLICT_BANNER = computed(() => t("processConfig.conflictBanner"));
const SECTION_IDS = {
  identity: "identity",
  execution: "execution",
  runtime: "runtime",
  policies: "policies",
  environment: "environment",
  dependencies: "dependencies",
} as const;
const POLICY_KEYS = {
  restart: "restart",
  health: "health",
  log: "log",
  resources: "resources",
} as const;
const EDITOR_IDS = {
  yaml: "process-config-yaml",
  yamlError: "process-config-yaml-error",
  comment: "process-config-comment",
} as const;
const EDITOR_FIELDS = { yaml: "config-yaml" } as const;
const EDITOR_MODE = { form: "form", yaml: "yaml" } as const;
const EDITOR_MODES = [EDITOR_MODE.form, EDITOR_MODE.yaml] as const;
const DRAWER_SIZE = "wide" as const;
type EditorMode = "form" | "yaml";
type ProcessConfigFormHandle = {
  focusIssue(path: string): Promise<void>;
};
type MutationTarget = {
  idOrName: string;
  targetNodeId: string;
  selectionGeneration: number;
  requestToken: symbol;
  options: { headers: HeadersInit };
  configKey: readonly ["process-config", string, string];
  historyKey: readonly ["process-history", string, string];
  processKey: readonly ["process", string, string];
};

const props = defineProps<{
  idOrName: string;
  targetNodeId: string;
  ownerNodeHostname?: string;
}>();

const config = useConfigClient();
const processes = useProcessClient();
const queryClient = useQueryClient();
const editorMode = ref<EditorMode>("form");
const formDraft = ref<ProcessConfigFormState | null>(null);
const formEditor = ref<ProcessConfigFormHandle | null>(null);
const editorText = ref("");
const editorBaseline = ref("");
const editorOpen = ref(false);
const formIssues = ref<ProcessConfigIssue[]>([]);
const validateRequested = ref(0);
const yamlError = ref("");
const yamlTextarea = ref<HTMLTextAreaElement | null>(null);
const comment = ref("");
const loadedSpec = ref<ProcessSpec | null>(null);
const conflictText = ref("");
const actionError = ref("");
const selected = ref<string[]>([]);
const saving = ref(false);
const rollingBack = ref(false);
let selectionGeneration = 0;
let activeMutationToken: symbol | null = null;

const canUpdate = computed(() => (session.value?.permissions ?? []).includes("process.config.update"));
const targetOpts = computed(() => ({ headers: withTarget(props.targetNodeId) }));
const enabled = computed(() => props.idOrName.length > 0 && props.targetNodeId.length > 0);

const acceptRemoteSpec = ref(true);

const configQuery = useQuery({
  queryKey: computed(() => ["process-config", props.idOrName, props.targetNodeId]),
  queryFn: () => config.getConfig({ idOrName: props.idOrName }, targetOpts.value),
  enabled,
  refetchOnWindowFocus: false,
});

const processQuery = useQuery({
  queryKey: computed(() => ["process", props.idOrName, props.targetNodeId]),
  queryFn: () => processes.getProcess({ idOrName: props.idOrName }, targetOpts.value),
  enabled,
  refetchOnWindowFocus: false,
});

const logPathPending = computed(() => {
  const instances = processQuery.data.value?.process?.instances ?? [];
  return instances.some((inst) => inst.logPathPending);
});

const historyQuery = useQuery({
  queryKey: computed(() => ["process-history", props.idOrName, props.targetNodeId]),
  queryFn: () => config.history({ idOrName: props.idOrName }, targetOpts.value),
  enabled,
  refetchOnWindowFocus: false,
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
  refetchOnWindowFocus: false,
});

const configPending = computed(() => configQuery.isPending.value);
const historyPending = computed(() => historyQuery.isPending.value);
const diffPending = computed(() => diffQuery.isPending.value);
const diffError = computed(() => diffQuery.error.value);
const diffText = computed(() => diffQuery.data.value?.diff ?? "");
const displayYaml = computed(() => {
  if (!loadedSpec.value) {
    return "";
  }
  return stringifyProcessConfigYaml(loadedSpec.value);
});
const displayObject = computed<Record<string, unknown>>(() => {
  if (!loadedSpec.value) {
    return {};
  }
  return processConfigToYamlValue(loadedSpec.value);
});
const environmentEntries = computed(() => Object.entries(loadedSpec.value?.environment ?? {}));

function canonicalizeJson(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(canonicalizeJson);
  }
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>)
        .sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0)
        .map(([key, entry]) => [key, canonicalizeJson(entry)]),
    );
  }
  return value;
}

function semanticSpecJson(spec: ProcessSpec): string {
  return JSON.stringify(canonicalizeJson(toJson(ProcessSpecSchema, spec)));
}

const normalizedDraft = computed(() => {
  if (editorMode.value === "form" && formDraft.value) {
    try {
      return semanticSpecJson(processConfigFormToSpec(formDraft.value));
    } catch {
      return JSON.stringify(formDraft.value);
    }
  }
  try {
    return semanticSpecJson(parseProcessConfigYaml(editorText.value));
  } catch {
    return editorText.value;
  }
});
const editorDirty = computed(
  () => normalizedDraft.value !== editorBaseline.value || comment.value.trim().length > 0,
);

watch(
  () => [props.idOrName, props.targetNodeId] as const,
  ([idOrName, targetNodeId], [previousIdOrName, previousTargetNodeId]) => {
    if (idOrName === previousIdOrName && targetNodeId === previousTargetNodeId) {
      return;
    }
    selectionGeneration += 1;
    activeMutationToken = null;
    acceptRemoteSpec.value = true;
    loadedSpec.value = null;
    editorOpen.value = false;
    formDraft.value = null;
    editorText.value = "";
    editorBaseline.value = "";
    formIssues.value = [];
    validateRequested.value = 0;
    yamlError.value = "";
    comment.value = "";
    conflictText.value = "";
    actionError.value = "";
    selected.value = [];
    saving.value = false;
    rollingBack.value = false;
  },
);

watch(
  () => configQuery.data.value?.spec,
  (spec) => {
    if (!spec || !acceptRemoteSpec.value) {
      return;
    }
    applySpec(spec);
    acceptRemoteSpec.value = false;
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

function captureMutationTarget(): MutationTarget {
  const idOrName = props.idOrName;
  const targetNodeId = props.targetNodeId;
  const requestToken = Symbol();
  activeMutationToken = requestToken;
  return {
    idOrName,
    targetNodeId,
    selectionGeneration,
    requestToken,
    options: { headers: withTarget(targetNodeId) },
    configKey: ["process-config", idOrName, targetNodeId],
    historyKey: ["process-history", idOrName, targetNodeId],
    processKey: ["process", idOrName, targetNodeId],
  };
}

function ownsVisibleMutation(target: MutationTarget): boolean {
  return props.idOrName === target.idOrName
    && props.targetNodeId === target.targetNodeId
    && selectionGeneration === target.selectionGeneration
    && activeMutationToken === target.requestToken;
}

function applySpec(spec: ProcessSpec | undefined): void {
  if (!spec) {
    return;
  }
  const next = create(ProcessSpecSchema, spec);
  loadedSpec.value = next;
  editorText.value = stringifyProcessConfigYaml(next);
}

function openEditor(): void {
  if (!canUpdate.value || !loadedSpec.value) {
    return;
  }
  const openingSpec = create(ProcessSpecSchema, loadedSpec.value);
  editorMode.value = "form";
  formDraft.value = specToProcessConfigForm(openingSpec);
  editorText.value = stringifyProcessConfigYaml(openingSpec);
  editorBaseline.value = semanticSpecJson(openingSpec);
  formIssues.value = [];
  validateRequested.value = 0;
  comment.value = "";
  yamlError.value = "";
  actionError.value = "";
  conflictText.value = "";
  editorOpen.value = true;
}

function closeEditor(): void {
  if (saving.value) {
    return;
  }
  if (editorDirty.value && !window.confirm(t("processConfig.config.unsavedConfirm"))) {
    return;
  }
  editorOpen.value = false;
  yamlError.value = "";
  actionError.value = "";
}

function updateFormDraft(next: ProcessConfigFormState): void {
  formDraft.value = next;
  formIssues.value = [];
}

function validateFormField(path: string): void {
  if (!formDraft.value) {
    return;
  }
  const collectionPath = path.match(/^(environment|dependencies)\./)?.[1];
  formIssues.value = validateProcessConfigForm(formDraft.value).filter((issue) => (
    issue.path === path || (collectionPath !== undefined && issue.path === collectionPath)
  ));
}

function issueMessage(issue: ProcessConfigIssue): string {
  return t(`processConfig.editor.validation.${issue.code}`);
}

function showFormIssues(issues: ProcessConfigIssue[]): void {
  formIssues.value = issues;
  validateRequested.value += 1;
  const firstIssue = issues[0];
  if (firstIssue) {
    void nextTick(() => formEditor.value?.focusIssue(firstIssue.path));
  }
}

function showYamlError(message: string): void {
  yamlError.value = message;
  void nextTick(() => yamlTextarea.value?.focus());
}

function setYamlTextarea(element: Element | ComponentPublicInstance | null): void {
  yamlTextarea.value = element instanceof HTMLTextAreaElement ? element : null;
}

function setFormEditor(element: Element | ComponentPublicInstance | null): void {
  formEditor.value = element as unknown as ProcessConfigFormHandle | null;
}

function synchronizeActiveMode(synchronizeInactiveDraft = true): ProcessSpec | null {
  if (editorMode.value === "form") {
    if (!formDraft.value) {
      return null;
    }
    const issues = validateProcessConfigForm(formDraft.value);
    if (issues.length > 0) {
      showFormIssues(issues);
      return null;
    }
    const spec = processConfigFormToSpec(formDraft.value);
    if (synchronizeInactiveDraft) {
      editorText.value = stringifyProcessConfigYaml(spec);
    }
    return spec;
  }

  let spec: ProcessSpec;
  try {
    spec = parseProcessConfigYaml(editorText.value);
  } catch {
    showYamlError(t("processConfig.config.invalidYaml"));
    return null;
  }
  const issues = validateProcessSpec(spec);
  if (issues.length > 0) {
    showYamlError(issueMessage(issues[0]));
    return null;
  }
  if (synchronizeInactiveDraft) {
    formDraft.value = specToProcessConfigForm(spec);
  }
  return spec;
}

function switchEditorMode(next: EditorMode): void {
  if (next === editorMode.value) {
    return;
  }
  if (!synchronizeActiveMode()) {
    return;
  }
  formIssues.value = [];
  yamlError.value = "";
  editorMode.value = next;
}

function nestedYaml(key: string): string {
  const value = displayObject.value[key];
  if (value === undefined || value === null) {
    return t("processConfig.config.empty");
  }
  return stringifyYaml(value, { lineWidth: 0 }).trimEnd();
}

function textOrEmpty(value: string): string {
  return value || t("processConfig.config.empty");
}

function cacheSpec(spec: ProcessSpec | undefined, queryKey: MutationTarget["configKey"]): ProcessSpec | null {
  if (!spec) {
    return null;
  }
  const next = create(ProcessSpecSchema, spec);
  const cached = queryClient.getQueryData<{ spec?: ProcessSpec }>(queryKey)?.spec;
  if (cached && cached.latestRevision > next.latestRevision) {
    return create(ProcessSpecSchema, cached);
  }
  queryClient.setQueryData(queryKey, { spec: next });
  return next;
}

async function refresh(): Promise<void> {
  await queryClient.invalidateQueries({ queryKey: ["process-config", props.idOrName, props.targetNodeId] });
  await queryClient.invalidateQueries({ queryKey: ["process-history", props.idOrName, props.targetNodeId] });
  await queryClient.invalidateQueries({ queryKey: ["process", props.idOrName, props.targetNodeId] });
}

async function onReload(): Promise<void> {
  conflictText.value = "";
  actionError.value = "";
  acceptRemoteSpec.value = true;
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
  yamlError.value = "";
  if (!canUpdate.value || !loadedSpec.value) {
    return;
  }
  const parsed = synchronizeActiveMode(false);
  if (!parsed) {
    return;
  }
  parsed.processId = loadedSpec.value.processId;
  parsed.latestRevision = loadedSpec.value.latestRevision;
  const target = captureMutationTarget();
  const request = {
    meta: mutationMeta(),
    idOrName: target.idOrName,
    expectedRevision: loadedSpec.value.latestRevision,
    spec: parsed,
    comment: comment.value,
  };
  saving.value = true;
  rollingBack.value = false;
  try {
    const out = await config.updateConfig(request, target.options);
    const next = cacheSpec(out.spec, target.configKey);
    if (ownsVisibleMutation(target)) {
      applySpec(next ?? undefined);
      editorBaseline.value = semanticSpecJson(next ?? parsed);
      comment.value = "";
      editorOpen.value = false;
    }
    await queryClient.invalidateQueries({ queryKey: target.historyKey });
    await queryClient.invalidateQueries({ queryKey: target.processKey });
  } catch (err) {
    if (!ownsVisibleMutation(target)) {
      return;
    }
    if (isConflict(err)) {
      conflictText.value = CONFLICT_BANNER.value;
      return;
    }
    actionError.value = formatRemoteError(err);
  } finally {
    if (ownsVisibleMutation(target)) {
      saving.value = false;
      rollingBack.value = false;
      activeMutationToken = null;
    }
  }
}

async function onRollback(toRevision: bigint | number): Promise<void> {
  conflictText.value = "";
  actionError.value = "";
  if (!canUpdate.value || !loadedSpec.value) {
    return;
  }
  const to = typeof toRevision === "bigint" ? toRevision : BigInt(toRevision);
  if (!window.confirm(t("processConfig.history.rollbackConfirm", { revision: String(to) }))) {
    return;
  }
  const target = captureMutationTarget();
  const request = {
    meta: mutationMeta(),
    idOrName: target.idOrName,
    toRevision: to,
    expectedRevision: loadedSpec.value.latestRevision,
    comment: comment.value,
  };
  saving.value = false;
  rollingBack.value = true;
  try {
    const out = await config.rollback(request, target.options);
    const next = cacheSpec(out.spec, target.configKey);
    if (ownsVisibleMutation(target)) {
      applySpec(next ?? undefined);
    }
    await queryClient.invalidateQueries({ queryKey: target.historyKey });
    await queryClient.invalidateQueries({ queryKey: target.processKey });
  } catch (err) {
    if (!ownsVisibleMutation(target)) {
      return;
    }
    if (isConflict(err)) {
      conflictText.value = CONFLICT_BANNER.value;
      return;
    }
    actionError.value = formatRemoteError(err);
  } finally {
    if (ownsVisibleMutation(target)) {
      saving.value = false;
      rollingBack.value = false;
      activeMutationToken = null;
    }
  }
}
</script>

<template>
  <div class="panel">
    <div v-if="conflictText" class="banner conflict" role="alert">{{ conflictText }}</div>
    <div v-if="logPathPending" class="banner" role="status">{{ t("processConfig.logPathPending") }}</div>
    <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <p v-if="configPending && !loadedSpec" class="muted">{{ t("processConfig.loading") }}</p>

    <section v-if="loadedSpec" class="card config-card">
      <div class="title-row config-header">
        <div class="title-copy">
          <div class="title-line">
            <h2>{{ t("processConfig.config.title") }}</h2>
            <span class="revision-badge">{{ t("processConfig.config.revision", { revision: String(loadedSpec.latestRevision) }) }}</span>
          </div>
          <p class="muted config-subtitle">{{ loadedSpec.name || props.idOrName }}</p>
        </div>
        <div class="header-actions">
          <button type="button" class="btn" @click="onReload">
            <RefreshCw :size="16" aria-hidden="true" />
            {{ t("processConfig.config.reload") }}
          </button>
          <button
            v-if="canUpdate"
            type="button"
            class="btn btn-primary"
            data-action="edit-config"
            @click="openEditor"
          >
            <Pencil :size="16" aria-hidden="true" />
            {{ t("processConfig.config.edit") }}
          </button>
        </div>
      </div>

      <div class="config-section" :data-section="SECTION_IDS.identity">
        <h3>{{ t("processConfig.config.sections.identity") }}</h3>
        <dl class="facts config-facts">
          <div>
            <dt>{{ t("processConfig.config.processId") }}</dt>
            <dd class="mono breakable">{{ textOrEmpty(loadedSpec.processId) }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.name") }}</dt>
            <dd>{{ textOrEmpty(loadedSpec.name) }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.group") }}</dt>
            <dd>{{ textOrEmpty(loadedSpec.group) }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.owner") }}</dt>
            <dd class="mono breakable">
              <RouterLink
                v-if="targetNodeId"
                :to="`/nodes/${encodeURIComponent(targetNodeId)}`"
              >
                {{ ownerNodeHostname || loadedSpec.ownerAgentId || "—" }}
              </RouterLink>
              <span v-else>{{ textOrEmpty(loadedSpec.ownerAgentId) }}</span>
            </dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.latestRevision") }}</dt>
            <dd class="numeric">{{ String(loadedSpec.latestRevision) }}</dd>
          </div>
        </dl>
      </div>

      <div class="config-section" :data-section="SECTION_IDS.execution">
        <h3>{{ t("processConfig.config.sections.execution") }}</h3>
        <dl class="facts config-facts">
          <div class="fact-wide">
            <dt>{{ t("processConfig.config.command") }}</dt>
            <dd class="command-line">
              <code>{{ textOrEmpty(loadedSpec.command) }}</code>
              <code v-for="(arg, index) in loadedSpec.args" :key="`${index}-${arg}`" class="argument">{{ arg }}</code>
            </dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.workingDirectory") }}</dt>
            <dd class="mono breakable">{{ textOrEmpty(loadedSpec.workingDirectory) }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.runAsUser") }}</dt>
            <dd>{{ textOrEmpty(loadedSpec.runAsUser) }}</dd>
          </div>
        </dl>
      </div>

      <div class="config-section" :data-section="SECTION_IDS.runtime">
        <h3>{{ t("processConfig.config.sections.runtime") }}</h3>
        <dl class="facts config-facts">
          <div>
            <dt>{{ t("processConfig.config.instances") }}</dt>
            <dd class="numeric">{{ loadedSpec.instances }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.autostart") }}</dt>
            <dd>{{ t(loadedSpec.autostart ? "processConfig.config.enabled" : "processConfig.config.disabled") }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.startupPriority") }}</dt>
            <dd class="numeric">{{ loadedSpec.startupPriority }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.stopTimeout") }}</dt>
            <dd class="numeric">{{ t("processConfig.config.milliseconds", { value: String(loadedSpec.stopTimeoutMs) }) }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.stopSignal") }}</dt>
            <dd class="mono">{{ textOrEmpty(loadedSpec.stopSignal) }}</dd>
          </div>
          <div>
            <dt>{{ t("processConfig.config.killSignal") }}</dt>
            <dd class="mono">{{ textOrEmpty(loadedSpec.killSignal) }}</dd>
          </div>
        </dl>
      </div>

      <div class="config-section" :data-section="SECTION_IDS.policies">
        <h3>{{ t("processConfig.config.sections.policies") }}</h3>
        <div class="policy-grid">
          <div>
            <h4>{{ t("processConfig.config.restartPolicy") }}</h4>
            <pre>{{ nestedYaml(POLICY_KEYS.restart) }}</pre>
          </div>
          <div>
            <h4>{{ t("processConfig.config.healthCheck") }}</h4>
            <pre>{{ nestedYaml(POLICY_KEYS.health) }}</pre>
          </div>
          <div>
            <h4>{{ t("processConfig.config.logPolicy") }}</h4>
            <pre>{{ nestedYaml(POLICY_KEYS.log) }}</pre>
          </div>
          <div>
            <h4>{{ t("processConfig.config.resources") }}</h4>
            <pre>{{ nestedYaml(POLICY_KEYS.resources) }}</pre>
          </div>
        </div>
      </div>

      <div class="config-section split-sections">
        <div :data-section="SECTION_IDS.environment">
          <h3>{{ t("processConfig.config.sections.environment") }}</h3>
          <dl v-if="environmentEntries.length" class="key-value-list">
            <div v-for="([key, value]) in environmentEntries" :key="key">
              <dt class="mono breakable">{{ key }}</dt>
              <dd class="mono breakable">{{ value }}</dd>
            </div>
          </dl>
          <p v-else class="muted empty-state">{{ t("processConfig.config.empty") }}</p>
        </div>
        <div :data-section="SECTION_IDS.dependencies">
          <h3>{{ t("processConfig.config.sections.dependencies") }}</h3>
          <ul v-if="loadedSpec.dependencies.length" class="dependency-list">
            <li v-for="dependency in loadedSpec.dependencies" :key="`${dependency.processName}-${dependency.condition}`">
              <span class="mono breakable">{{ dependency.processName }}</span>
              <span class="dependency-condition">{{ dependency.condition }}</span>
            </li>
          </ul>
          <p v-else class="muted empty-state">{{ t("processConfig.config.empty") }}</p>
        </div>
      </div>

      <details class="yaml-details">
        <summary>
          <FileCode2 :size="16" aria-hidden="true" />
          {{ t("processConfig.config.fullYaml") }}
        </summary>
        <pre class="config-yaml-viewer">{{ displayYaml }}</pre>
      </details>
    </section>

    <Drawer
      :open="editorOpen"
      :title="t('processConfig.config.editTitle')"
      :close-label="t('actions.close')"
      :size="DRAWER_SIZE"
      @close="closeEditor"
    >
      <form class="config-form drawer-form" @submit.prevent="onSave">
        <div v-if="conflictText" class="banner conflict" role="alert">{{ conflictText }}</div>
        <div v-if="logPathPending" class="banner" role="status">{{ t("processConfig.logPathPending") }}</div>
        <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
        <p class="muted drawer-intro">{{ t("processConfig.config.editHint") }}</p>
        <div class="editor-mode" role="group" :aria-label="t('processConfig.editor.modeLabel')">
          <button
            v-for="mode in EDITOR_MODES"
            :key="mode"
            type="button"
            class="editor-mode-button"
            :class="{ active: editorMode === mode }"
            :data-editor-mode="mode"
            :aria-pressed="editorMode === mode"
            @click="switchEditorMode(mode)"
          >
            {{ t(`processConfig.editor.mode.${mode}`) }}
          </button>
        </div>
        <ProcessConfigForm
          v-if="editorMode === EDITOR_MODE.form && formDraft"
          :ref="setFormEditor"
          :model-value="formDraft"
          :issues="formIssues"
          :validate-requested="validateRequested"
          @update:model-value="updateFormDraft"
          @blur-field="validateFormField"
        />
        <div v-else-if="editorMode === EDITOR_MODE.yaml" class="yaml-editor">
          <label class="field" :for="EDITOR_IDS.yaml">
            <span>{{ t("processConfig.config.specLabel") }}</span>
            <textarea
              :id="EDITOR_IDS.yaml"
              :ref="setYamlTextarea"
              v-model="editorText"
              class="input editor"
              :data-field="EDITOR_FIELDS.yaml"
              spellcheck="false"
              rows="24"
              :aria-invalid="Boolean(yamlError)"
              :aria-describedby="yamlError ? EDITOR_IDS.yamlError : undefined"
              @input="yamlError = ''"
            />
          </label>
          <p
            v-if="yamlError"
            :id="EDITOR_IDS.yamlError"
            class="field-error"
            :data-error="EDITOR_FIELDS.yaml"
            role="alert"
          >
            {{ yamlError }}
          </p>
        </div>
        <div class="drawer-actions">
          <label class="field drawer-comment" :for="EDITOR_IDS.comment">
            <span>{{ t("processConfig.config.commentLabel") }}</span>
            <input :id="EDITOR_IDS.comment" v-model="comment" class="input" type="text" />
          </label>
          <div class="drawer-action-buttons">
            <button
              type="button"
              class="btn"
              data-action="cancel-config-edit"
              :disabled="saving"
              @click="closeEditor"
            >
              {{ t("actions.cancel") }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !targetNodeId">
              {{ saving ? t("processConfig.config.saving") : t("processConfig.config.save") }}
            </button>
          </div>
        </div>
      </form>
    </Drawer>

    <section class="card">
      <h2>{{ t("processConfig.history.title") }}</h2>
      <p v-if="historyPending && !revisions.length" class="muted">{{ t("processConfig.history.loading") }}</p>
      <table v-else class="table">
        <thead>
          <tr>
            <th>{{ t("processConfig.history.table.select") }}</th>
            <th>{{ t("processConfig.history.table.revision") }}</th>
            <th>{{ t("processConfig.history.table.operator") }}</th>
            <th>{{ t("processConfig.history.table.time") }}</th>
            <th>{{ t("processConfig.history.table.comment") }}</th>
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
                {{ t("processConfig.history.table.rollback") }}
              </button>
            </td>
          </tr>
          <tr v-if="!revisions.length">
            <td :colspan="canUpdate ? 6 : 5" class="muted">{{ t("processConfig.history.noRevisions") }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="selected.length === 2" class="diff-block">
        <h3>{{ t("processConfig.history.diff.title") }} {{ selected.slice().sort((a, b) => Number(a) - Number(b)).join(" → ") }}</h3>
        <p v-if="diffPending" class="muted">{{ t("processConfig.history.diff.loading") }}</p>
        <p v-else-if="diffError" class="error" role="alert">
          {{ formatRemoteError(diffError) }}
        </p>
        <pre v-else class="diff">{{ diffText || t("processConfig.history.diff.empty") }}</pre>
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
.config-header {
  align-items: flex-start;
  margin-bottom: 0;
  padding-bottom: 1rem;
}
.title-copy {
  min-width: 0;
}
.title-line,
.header-actions,
.command-line {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
}
.title-line {
  gap: 0.625rem;
}
.header-actions {
  justify-content: flex-end;
  gap: 0.5rem;
}
.config-subtitle {
  margin: 0.25rem 0 0;
}
.revision-badge {
  display: inline-flex;
  align-items: center;
  min-height: 1.5rem;
  border-radius: 999px;
  background: var(--color-live);
  color: var(--color-live-fg);
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
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
h4 {
  margin: 0;
  font-size: 0.75rem;
  font-weight: 650;
  color: var(--color-muted);
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
  border-radius: 8px;
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
  border-radius: var(--radius-sm);
  background: var(--color-card);
  padding: 1.25rem;
  overflow: auto;
}
.config-card {
  overflow: hidden;
}
.config-section {
  border-top: 1px solid var(--color-border);
  padding: 1rem 0;
}
.config-section > h3,
.split-sections h3 {
  margin-bottom: 0.75rem;
  color: var(--color-text);
  font-size: 0.8125rem;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 0.85rem 1.25rem;
  margin: 0;
}
.config-facts {
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 12rem), 1fr));
  gap: 1rem 1.5rem;
}
.fact-wide {
  grid-column: 1 / -1;
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
.breakable {
  min-width: 0;
  overflow-wrap: anywhere;
}
.numeric {
  font-variant-numeric: tabular-nums;
}
.command-line {
  gap: 0.375rem;
}
.command-line code {
  max-width: 100%;
  border-radius: 5px;
  background: var(--color-bg);
  padding: 0.25rem 0.4375rem;
  overflow-wrap: anywhere;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8125rem;
  font-weight: 550;
}
.command-line .argument {
  color: var(--color-muted);
  font-weight: 500;
}
.policy-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem 1.5rem;
}
.policy-grid > div {
  min-width: 0;
}
.policy-grid pre,
.config-yaml-viewer {
  margin: 0.375rem 0 0;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg);
  padding: 0.75rem;
  overflow: auto;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.75rem;
  line-height: 1.5;
}
.policy-grid pre {
  min-height: 4.5rem;
  max-height: 12rem;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.split-sections {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1.5rem;
}
.key-value-list {
  margin: 0;
}
.key-value-list > div,
.dependency-list li {
  display: grid;
  grid-template-columns: minmax(6rem, 0.7fr) minmax(0, 1.3fr);
  gap: 0.75rem;
  border-top: 1px solid var(--color-border);
  padding: 0.625rem 0;
}
.key-value-list > div:first-child,
.dependency-list li:first-child {
  border-top: 0;
  padding-top: 0;
}
.key-value-list dt,
.key-value-list dd {
  margin: 0;
  font-size: 0.75rem;
  line-height: 1.45;
}
.key-value-list dt {
  color: var(--color-muted);
}
.dependency-list {
  margin: 0;
  padding: 0;
  list-style: none;
}
.dependency-condition {
  justify-self: start;
  border-radius: 999px;
  background: var(--color-unknown);
  color: var(--color-unknown-fg);
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 600;
}
.empty-state {
  margin: 0;
}
.yaml-details {
  border-top: 1px solid var(--color-border);
  padding-top: 1rem;
}
.yaml-details summary {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  width: fit-content;
  border-radius: 5px;
  color: var(--color-text);
  cursor: pointer;
  font-size: 0.8125rem;
  font-weight: 600;
}
.yaml-details summary::marker {
  color: var(--color-muted);
}
.config-yaml-viewer {
  max-height: 28rem;
  white-space: pre;
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
.drawer-form {
  min-width: 0;
  min-height: 100%;
}
.drawer-intro {
  margin: 0;
  max-width: 65ch;
  line-height: 1.5;
}
.editor-mode {
  display: grid;
  align-self: flex-start;
  grid-template-columns: repeat(2, minmax(5rem, 1fr));
  gap: 0.25rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg);
  padding: 0.25rem;
}
.editor-mode-button {
  min-height: 2.25rem;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--color-muted);
  padding: 0.5rem 0.75rem;
  cursor: pointer;
  font: inherit;
  font-size: 0.8125rem;
  font-weight: 600;
}
.editor-mode-button:hover {
  background: var(--color-hover);
  color: var(--color-text);
}
.editor-mode-button.active {
  background: var(--color-card);
  box-shadow: 0 0 0 1px var(--color-border);
  color: var(--color-text);
}
.editor-mode-button:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.yaml-editor {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 0.75rem;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-size: 0.8rem;
  color: var(--color-muted);
}
.editor {
  box-sizing: border-box;
  width: 100%;
  max-width: 100%;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
  line-height: 1.45;
  resize: vertical;
  min-height: min(32rem, 55vh);
  tab-size: 2;
}
.actions {
  display: flex;
  gap: 0.5rem;
}
.field-error {
  margin: -0.5rem 0 0;
  color: var(--color-danger);
  font-size: 0.8125rem;
  line-height: 1.45;
}
.drawer-actions {
  position: sticky;
  bottom: -1.5rem;
  display: flex;
  align-items: flex-end;
  gap: 0.5rem;
  margin: auto -1.5rem -1.5rem;
  border-top: 1px solid var(--color-border);
  background: var(--color-card);
  padding: 1rem 1.5rem;
}
.drawer-comment {
  min-width: 0;
  flex: 1 1 16rem;
}
.drawer-comment .input {
  box-sizing: border-box;
  width: 100%;
}
.drawer-action-buttons {
  display: flex;
  flex: 0 0 auto;
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

@media (max-width: 720px) {
  .config-header,
  .split-sections {
    grid-template-columns: 1fr;
  }
  .config-header {
    align-items: stretch;
    flex-direction: column;
  }
  .header-actions {
    justify-content: flex-start;
  }
  .policy-grid,
  .split-sections {
    grid-template-columns: 1fr;
  }
  .header-actions .btn {
    flex: 1;
  }
  .key-value-list > div,
  .dependency-list li {
    grid-template-columns: 1fr;
    gap: 0.25rem;
  }
  .editor-mode {
    align-self: stretch;
  }
  .editor-mode-button,
  .drawer-actions .input,
  .drawer-actions .btn {
    min-height: 44px;
  }
  .drawer-actions {
    align-items: stretch;
    flex-direction: column;
  }
  .drawer-comment {
    flex: 0 1 auto;
  }
  .drawer-action-buttons .btn {
    flex: 1;
  }
}
</style>
