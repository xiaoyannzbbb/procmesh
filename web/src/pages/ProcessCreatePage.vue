<script setup lang="ts">
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, nextTick, ref, watch, type ComponentPublicInstance } from "vue";
import { useRoute, useRouter } from "vue-router";
import { create } from "@bufbuild/protobuf";
import { ChevronLeft, FileCode2, LoaderCircle, Plus, Server, SlidersHorizontal } from "lucide-vue-next";
import FreshnessBadge from "../components/FreshnessBadge.vue";
import { ProcessSpecSchema, type ProcessSpec } from "../gen/procmesh/v1/api_pb";
import { withTarget } from "../lib/headers";
import { newOperationId } from "../lib/opid";
import { LIVE } from "../lib/freshness";
import { remoteCreateBlocked } from "../lib/remoteProcess";
import { useNodeClient, useProcessClient } from "../lib/rpc";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import { mapNode, type NodeView } from "./clusterView";
import { formatRemoteError } from "./processView";
import ProcessConfigForm from "./ProcessConfigForm.vue";
import {
  emptyProcessConfigForm,
  parseProcessConfigYaml,
  processConfigFormToSpec,
  specToProcessConfigForm,
  stringifyProcessConfigDraftYaml,
  validateProcessConfigForm,
  validateProcessSpec,
  type ProcessConfigFormState,
  type ProcessConfigIssue,
} from "./processConfigForm";

const EDITOR_MODE = { form: "form", yaml: "yaml" } as const;
const CREATE_HIDDEN_PATHS = ["processId", "ownerAgentId", "latestRevision"] as const;
const CREATE_YAML_OMIT_KEYS = ["process_id", "owner_agent_id", "latest_revision"] as const;
const EDITOR_IDS = {
  yaml: "process-create-yaml",
  yamlError: "process-create-yaml-error",
  yamlHint: "process-create-yaml-hint",
  comment: "process-create-comment",
  commentHint: "process-create-comment-hint",
  owners: "process-create-owners",
} as const;
type EditorMode = "form" | "yaml";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const nodes = useNodeClient();
const processes = useProcessClient();
const queryClient = useQueryClient();

const canCreate = computed(() => (session.value?.permissions ?? []).includes("process.create"));
const formDraft = ref<ProcessConfigFormState>(emptyProcessConfigForm());
const editorMode = ref<EditorMode>("form");
const editorText = ref("");
const yamlError = ref("");
const formIssues = ref<ProcessConfigIssue[]>([]);
const validateRequested = ref(0);
const comment = ref("");
const selectedOwnerIds = ref<string[]>([]);
const submitting = ref(false);
const actionError = ref("");
const results = ref<string[]>([]);
const errorSummary = ref<HTMLElement | null>(null);

function setErrorSummary(el: Element | ComponentPublicInstance | null): void {
  errorSummary.value = el instanceof HTMLElement ? el : null;
}

const nodesQuery = useQuery({
  queryKey: ["nodes"],
  queryFn: () => nodes.listNodes({}),
});

const nodeViews = computed(() => {
  const now = Date.now();
  return (nodesQuery.data.value?.nodes ?? []).map((raw) => mapNode(raw, now));
});

const nodesLoading = computed(() => nodesQuery.isPending.value && !nodesQuery.data.value);
const nodesError = computed(() => (
  nodesQuery.error.value ? formatRemoteError(nodesQuery.error.value) : ""
));
const selectedOwnerCount = computed(() => selectedOwnerIds.value.length);

const preselectedOwners = computed(() => {
  const raw = route.query.owners;
  const value = Array.isArray(raw) ? raw.join(",") : typeof raw === "string" ? raw : "";
  return value.split(",").map((id) => id.trim()).filter(Boolean);
});

const initialized = ref(false);
watch(
  [nodeViews, () => nodesQuery.isFetched.value],
  ([views, fetched]) => {
    if (initialized.value || !fetched) {
      return;
    }
    const eligible = new Set(views.filter((node) => !remoteCreateBlocked(node)).map((node) => node.nodeId));
    const wanted = preselectedOwners.value.filter((id) => eligible.has(id));
    if (wanted.length) {
      selectedOwnerIds.value = wanted;
    } else if (eligible.size === 1) {
      selectedOwnerIds.value = [...eligible];
    }
    initialized.value = true;
  },
  { immediate: true },
);

function ownerBlocked(node: NodeView): boolean {
  return remoteCreateBlocked(node);
}

function ownerReason(node: NodeView): string {
  if (node.freshness !== LIVE) {
    return t("processes.create.ownerUnknown");
  }
  if (node.disableRemoteCreate) {
    return t("processes.create.ownerDisabled");
  }
  return "";
}

function ownerCheckboxId(nodeId: string): string {
  return `process-create-owner-${nodeId}`;
}

function ownerReasonId(nodeId: string): string {
  return `${ownerCheckboxId(nodeId)}-reason`;
}

function toggleOwner(node: NodeView): void {
  if (ownerBlocked(node)) {
    return;
  }
  if (selectedOwnerIds.value.includes(node.nodeId)) {
    selectedOwnerIds.value = selectedOwnerIds.value.filter((id) => id !== node.nodeId);
    return;
  }
  selectedOwnerIds.value = [...selectedOwnerIds.value, node.nodeId];
}

function currentSpec(): ProcessSpec | null {
  yamlError.value = "";
  formIssues.value = [];
  if (editorMode.value === "yaml") {
    try {
      const spec = parseProcessConfigYaml(editorText.value);
      const issues = validateProcessSpec(spec);
      if (issues.length) {
        formIssues.value = issues;
        validateRequested.value += 1;
        return null;
      }
      return spec;
    } catch {
      yamlError.value = t("processConfig.config.invalidYaml");
      return null;
    }
  }
  const issues = validateProcessConfigForm(formDraft.value);
  if (issues.length) {
    formIssues.value = issues;
    validateRequested.value += 1;
    return null;
  }
  return processConfigFormToSpec(formDraft.value);
}

function draftYamlFromForm(form: ProcessConfigFormState): string {
  try {
    return stringifyProcessConfigDraftYaml(form, CREATE_YAML_OMIT_KEYS);
  } catch {
    return stringifyProcessConfigDraftYaml(emptyProcessConfigForm(), CREATE_YAML_OMIT_KEYS);
  }
}

function switchEditorMode(next: EditorMode): void {
  if (next === editorMode.value) {
    return;
  }
  if (next === EDITOR_MODE.yaml) {
    yamlError.value = "";
    formIssues.value = [];
    editorText.value = draftYamlFromForm(formDraft.value);
    editorMode.value = next;
    return;
  }
  try {
    formDraft.value = specToProcessConfigForm(parseProcessConfigYaml(editorText.value));
  } catch {
    yamlError.value = t("processConfig.config.invalidYaml");
    return;
  }
  yamlError.value = "";
  formIssues.value = [];
  editorMode.value = next;
}

async function focusFirstProblem(): Promise<void> {
  await nextTick();
  if (actionError.value) {
    errorSummary.value?.focus();
    return;
  }
  const yamlField = document.getElementById(EDITOR_IDS.yaml);
  if (yamlError.value && yamlField instanceof HTMLElement) {
    yamlField.focus();
    return;
  }
  const formSummary = document.querySelector<HTMLElement>("[data-error-summary]");
  formSummary?.focus();
}

async function onSubmit(): Promise<void> {
  actionError.value = "";
  results.value = [];
  if (!canCreate.value) {
    return;
  }
  const spec = currentSpec();
  if (!spec) {
    await focusFirstProblem();
    return;
  }
  const owners = selectedOwnerIds.value;
  if (!owners.length) {
    actionError.value = t("processes.create.needOwner");
    await focusFirstProblem();
    return;
  }
  submitting.value = true;
  const failures: string[] = [];
  const created: string[] = [];
  try {
    for (const nodeId of owners) {
      const next = create(ProcessSpecSchema, spec);
      next.processId = "";
      next.ownerAgentId = nodeId;
      next.latestRevision = 0n;
      try {
        const out = await processes.applyProcess(
          {
            meta: {
              operationId: newOperationId(),
              operator: session.value?.username ?? "",
            },
            expectedRevision: 0n,
            spec: next,
            comment: comment.value,
          },
          { headers: withTarget(nodeId) },
        );
        created.push(`${out.spec?.name || spec.name}@${nodeId}`);
      } catch (err) {
        failures.push(`${nodeId}: ${formatRemoteError(err)}`);
      }
    }
  } finally {
    submitting.value = false;
    await queryClient.invalidateQueries({ queryKey: ["processes"] });
    await queryClient.invalidateQueries({ queryKey: ["nodes"] });
  }
  if (created.length && !failures.length) {
    const first = created[0]?.split("@")[0] || spec.name;
    await router.push(`/processes/${encodeURIComponent(first)}`);
    return;
  }
  results.value = [...created.map((name) => t("processes.create.createdOne", { name })), ...failures];
  if (failures.length) {
    actionError.value = t("processes.create.partial", {
      success: created.length,
      total: owners.length,
      detail: failures.slice(0, 3).join(" · "),
    });
    await focusFirstProblem();
  }
}
</script>

<template>
  <form class="page create-form" :aria-busy="submitting" @submit.prevent="onSubmit">
    <header class="page-header">
      <div class="title-block">
        <RouterLink class="back" to="/processes">
          <ChevronLeft :size="16" aria-hidden="true" />
          {{ t("processes.create.back") }}
        </RouterLink>
        <div class="eyebrow">{{ t("processes.eyebrow") }}</div>
        <h1>{{ t("processes.create.title") }}</h1>
        <p class="subtitle">{{ t("processes.create.hint") }}</p>
      </div>
      <div class="header-actions">
        <button type="submit" class="btn btn-primary" :disabled="submitting || !canCreate">
          <LoaderCircle v-if="submitting" class="spin" :size="16" aria-hidden="true" />
          <Plus v-else :size="16" aria-hidden="true" />
          {{ submitting ? t("processes.create.submitBusy") : t("processes.create.submit") }}
        </button>
      </div>
    </header>

    <div
      v-if="!canCreate"
      class="banner danger"
      role="alert"
    >
      {{ t("processes.create.noPermission") }}
    </div>
    <div
      v-if="actionError"
      :ref="setErrorSummary"
      class="banner danger"
      role="alert"
      tabindex="-1"
    >
      <strong>{{ t("processes.create.errorTitle") }}</strong>
      <p>{{ actionError }}</p>
      <a
        v-if="actionError === t('processes.create.needOwner')"
        class="banner-link"
        :href="`#${EDITOR_IDS.owners}`"
      >
        {{ t("processes.create.owners") }}
      </a>
    </div>
    <ul v-if="results.length" class="results" aria-live="polite">
      <li v-for="line in results" :key="line">{{ line }}</li>
    </ul>

    <div class="create-layout">
      <section :id="EDITOR_IDS.owners" class="card owners-card">
        <div class="section-head">
          <span class="step" aria-hidden="true"></span>
          <div class="section-copy">
            <h2>{{ t("processes.create.owners") }}</h2>
            <p class="muted">{{ t("processes.create.ownersHint") }}</p>
          </div>
          <span v-if="selectedOwnerCount" class="owner-count">
            {{ t("processes.create.ownersSelected", { count: selectedOwnerCount }) }}
          </span>
        </div>

        <p v-if="nodesError" class="error" role="alert">
          {{ t("processes.create.ownersError", { detail: nodesError }) }}
        </p>
        <div
          v-else-if="nodesLoading"
          class="owner-skeleton"
          aria-busy="true"
          :aria-label="t('processes.create.ownersLoading')"
        >
          <div v-for="n in 3" :key="n" class="skeleton-row" />
        </div>
        <ul v-else-if="nodeViews.length" class="owner-list">
          <li v-for="node in nodeViews" :key="node.nodeId">
            <label
              class="owner-row"
              :class="{
                blocked: ownerBlocked(node),
                selected: selectedOwnerIds.includes(node.nodeId),
              }"
              :for="ownerCheckboxId(node.nodeId)"
            >
              <input
                :id="ownerCheckboxId(node.nodeId)"
                type="checkbox"
                :checked="selectedOwnerIds.includes(node.nodeId)"
                :disabled="ownerBlocked(node)"
                :title="ownerReason(node)"
                :aria-describedby="ownerBlocked(node) ? ownerReasonId(node.nodeId) : undefined"
                @change="toggleOwner(node)"
              />
              <span class="owner-icon" aria-hidden="true">
                <Server :size="16" />
              </span>
              <span class="owner-body">
                <span class="owner-name">{{ node.hostname || node.nodeId }}</span>
                <span class="owner-id">{{ t("processes.create.ownerNodeId") }} · {{ node.nodeId }}</span>
                <span class="owner-meta">
                  <FreshnessBadge :status="node.freshness" />
                  <span>{{ node.lastUpdated }}</span>
                  <span>{{ t("processes.create.ownerProcesses", { count: node.processCount }) }}</span>
                </span>
                <span
                  v-if="ownerBlocked(node)"
                  :id="ownerReasonId(node.nodeId)"
                  class="owner-reason"
                >
                  {{ ownerReason(node) }}
                </span>
              </span>
            </label>
          </li>
        </ul>
        <p v-else class="empty">{{ t("processes.create.noNodes") }}</p>
      </section>

      <section class="card spec-card">
        <div class="section-head">
          <span class="step" aria-hidden="true"></span>
          <div class="section-copy">
            <h2>{{ t("processes.create.spec") }}</h2>
          </div>
          <div class="editor-mode" role="group" :aria-label="t('processConfig.editor.modeLabel')">
            <button
              type="button"
              class="editor-mode-button"
              :class="{ active: editorMode === EDITOR_MODE.form }"
              :aria-pressed="editorMode === EDITOR_MODE.form"
              @click="switchEditorMode(EDITOR_MODE.form)"
            >
              <SlidersHorizontal :size="14" aria-hidden="true" />
              {{ t("processConfig.editor.mode.form") }}
            </button>
            <button
              type="button"
              class="editor-mode-button"
              :class="{ active: editorMode === EDITOR_MODE.yaml }"
              :aria-pressed="editorMode === EDITOR_MODE.yaml"
              @click="switchEditorMode(EDITOR_MODE.yaml)"
            >
              <FileCode2 :size="14" aria-hidden="true" />
              {{ t("processConfig.editor.mode.yaml") }}
            </button>
          </div>
        </div>

        <ProcessConfigForm
          v-if="editorMode === EDITOR_MODE.form"
          :model-value="formDraft"
          :issues="formIssues"
          :validate-requested="validateRequested"
          :hidden-paths="CREATE_HIDDEN_PATHS"
          @update:model-value="formDraft = $event"
        />
        <div v-else class="yaml-editor">
          <div
            v-if="formIssues.length"
            class="banner danger yaml-issues"
            data-error-summary
            role="alert"
            tabindex="-1"
          >
            <strong>{{ t("processConfig.editor.errorSummary") }}</strong>
            <ul>
              <li v-for="issue in formIssues" :key="`${issue.path}-${issue.code}`">
                {{ issue.path }}: {{ t(`processConfig.editor.validation.${issue.code}`) }}
              </li>
            </ul>
          </div>
          <label class="field" :for="EDITOR_IDS.yaml">
            <span>{{ t("processConfig.config.specLabel") }}</span>
            <textarea
              :id="EDITOR_IDS.yaml"
              v-model="editorText"
              class="input editor"
              rows="24"
              spellcheck="false"
              :aria-invalid="yamlError ? 'true' : undefined"
              :aria-describedby="yamlError ? EDITOR_IDS.yamlError : EDITOR_IDS.yamlHint"
            />
          </label>
          <p :id="EDITOR_IDS.yamlHint" class="field-hint">{{ t("processes.create.yamlHint") }}</p>
          <p v-if="yamlError" :id="EDITOR_IDS.yamlError" class="field-error" role="alert">{{ yamlError }}</p>
        </div>

        <label class="field" :for="EDITOR_IDS.comment">
          <span>{{ t("processConfig.config.commentLabel") }}</span>
          <input
            :id="EDITOR_IDS.comment"
            v-model="comment"
            class="input"
            type="text"
            :aria-describedby="EDITOR_IDS.commentHint"
          />
        </label>
        <p :id="EDITOR_IDS.commentHint" class="field-hint">{{ t("processes.create.commentHint") }}</p>
      </section>
    </div>

    <div class="form-actions">
      <RouterLink class="btn" to="/processes">{{ t("actions.cancel") }}</RouterLink>
      <button type="submit" class="btn btn-primary" :disabled="submitting || !canCreate">
        <LoaderCircle v-if="submitting" class="spin" :size="16" aria-hidden="true" />
        <Plus v-else :size="16" aria-hidden="true" />
        {{ submitting ? t("processes.create.submitBusy") : t("processes.create.submit") }}
      </button>
    </div>
  </form>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  padding-bottom: 0.5rem;
  scroll-padding-top: 6rem;
}

.page-header {
  position: sticky;
  top: 0;
  z-index: 20;
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 1rem;
  margin: -0.25rem -0.25rem 0;
  padding: 0.25rem;
  background: color-mix(in srgb, var(--color-bg) 92%, transparent);
  backdrop-filter: blur(8px);
}

.title-block {
  min-width: 0;
}

.back {
  display: inline-flex;
  align-items: center;
  gap: 0.15rem;
  min-height: 2.75rem;
  margin: 0 0 0.15rem -0.35rem;
  padding: 0 0.35rem;
  color: var(--color-muted);
  font-size: 0.875rem;
  text-decoration: none;
}

.back:hover {
  color: var(--color-text);
  text-decoration: underline;
}

.eyebrow {
  color: var(--color-accent);
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

h1 {
  margin: 0.2rem 0 0;
  font-size: 1.5rem;
  font-weight: 700;
  letter-spacing: -0.02em;
  text-wrap: balance;
}

.subtitle,
.muted,
.field-hint {
  margin: 0.3rem 0 0;
  color: var(--color-muted);
  font-size: 0.875rem;
  line-height: 1.5;
}

.header-actions,
.form-actions {
  display: flex;
  flex-shrink: 0;
  align-items: center;
  gap: 0.75rem;
}

.form-actions {
  justify-content: flex-end;
  padding-top: 0.25rem;
}

.banner {
  border: 1px solid var(--color-border);
  border-radius: 8px;
  padding: 0.875rem 1rem;
  background: var(--color-card);
}

.banner.danger {
  border-color: color-mix(in srgb, var(--color-danger) 35%, var(--color-border));
  background: color-mix(in srgb, var(--color-danger) 7%, var(--color-card));
  color: var(--color-danger);
}

.banner strong {
  display: block;
  margin-bottom: 0.25rem;
}

.banner p {
  margin: 0;
  color: inherit;
  line-height: 1.5;
}

.banner-link {
  display: inline-block;
  margin-top: 0.5rem;
  color: inherit;
  font-weight: 600;
}

.create-layout {
  display: grid;
  align-items: start;
  gap: 1rem;
  counter-reset: create-step;
}

.card {
  min-width: 0;
  margin: 0;
  padding: 1.25rem;
}

.owners-card,
.spec-card {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.section-head {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  gap: 0.75rem 1rem;
}

.step {
  display: inline-flex;
  width: 1.75rem;
  height: 1.75rem;
  flex-shrink: 0;
  align-items: center;
  justify-content: center;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
  font-size: 0.75rem;
  font-weight: 700;
}

.step::before {
  counter-increment: create-step;
  content: counter(create-step);
}

.section-copy {
  min-width: 0;
  flex: 1 1 8rem;
}

h2 {
  margin: 0;
  font-size: 1rem;
  font-weight: 650;
}

.section-copy .muted {
  margin: 0.25rem 0 0;
}

.owner-count {
  margin-left: auto;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-accent) 10%, transparent);
  color: var(--color-accent);
  padding: 0.25rem 0.65rem;
  font-size: 0.75rem;
  font-weight: 650;
}

.owner-list {
  display: grid;
  gap: 0.5rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.owner-row {
  display: grid;
  grid-template-columns: 1.125rem 2rem minmax(0, 1fr);
  gap: 0.75rem;
  align-items: start;
  min-height: 44px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  padding: 0.75rem;
  cursor: pointer;
  transition: border-color 0.15s ease, box-shadow 0.15s ease, background 0.15s ease;
}

.owner-row:hover:not(.blocked) {
  border-color: color-mix(in srgb, var(--color-accent) 45%, var(--color-border));
}

.owner-row.selected {
  border-color: var(--color-accent);
  background: color-mix(in srgb, var(--color-accent) 8%, var(--color-card));
  box-shadow: 0 0 0 1px var(--color-accent);
}

.owner-row.blocked {
  cursor: not-allowed;
  background: var(--color-bg);
}

.owner-row input {
  width: 1.125rem;
  height: 1.125rem;
  margin: 0.2rem 0 0;
  accent-color: var(--color-accent);
}

.owner-icon {
  display: inline-flex;
  width: 2rem;
  height: 2rem;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
  color: var(--color-muted);
}

.owner-body {
  display: grid;
  min-width: 0;
  gap: 0.2rem;
}

.owner-name {
  color: var(--color-text);
  font-weight: 600;
  overflow-wrap: anywhere;
}

.owner-id,
.owner-meta,
.owner-reason {
  color: var(--color-muted);
  font-size: 0.75rem;
  line-height: 1.4;
  overflow-wrap: anywhere;
}

.owner-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.35rem 0.65rem;
}

.owner-reason {
  color: var(--color-danger);
}

.owner-skeleton {
  display: grid;
  gap: 0.5rem;
}

.skeleton-row {
  height: 4.5rem;
  border-radius: 8px;
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
  animation: pulse 1.2s ease-in-out infinite;
}

.empty,
.error {
  margin: 0;
  font-size: 0.875rem;
  line-height: 1.5;
}

.error {
  color: var(--color-danger);
}

.editor-mode {
  display: grid;
  grid-template-columns: repeat(2, minmax(5rem, 1fr));
  gap: 0.25rem;
  margin-left: auto;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg);
  padding: 0.25rem;
}

.editor-mode-button {
  display: inline-flex;
  min-height: 2.75rem;
  align-items: center;
  justify-content: center;
  gap: 0.35rem;
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
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
  color: var(--color-text);
}

.editor-mode-button.active {
  background: var(--color-card);
  box-shadow: 0 0 0 1px var(--color-border);
  color: var(--color-text);
}

.yaml-editor,
.field {
  display: grid;
  gap: 0.35rem;
}

.yaml-issues ul {
  margin: 0.35rem 0 0;
  padding-left: 1.25rem;
}

.yaml-issues li {
  overflow-wrap: anywhere;
}

.field > span {
  color: var(--color-muted);
  font-size: 0.8125rem;
  font-weight: 500;
}

.field-hint {
  margin: 0;
}

.field-error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.8125rem;
  line-height: 1.4;
  overflow-wrap: anywhere;
}

.editor {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  min-height: 16rem;
  resize: vertical;
  tab-size: 2;
}

.results {
  margin: 0;
  padding-left: 1.25rem;
  color: var(--color-muted);
  font-size: 0.875rem;
  line-height: 1.5;
}

.spin {
  animation: spin 800ms linear infinite;
}

@keyframes pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.55;
  }
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

@media (min-width: 1024px) {
  .create-layout {
    grid-template-columns: minmax(20rem, 26rem) minmax(0, 1fr);
  }

  .owners-card {
    position: sticky;
    top: 5.5rem;
    align-self: start;
    max-height: calc(100dvh - 8rem);
    overflow: auto;
  }
}

@media (max-width: 768px) {
  .page-header {
    position: static;
    margin: 0;
    padding: 0;
    background: transparent;
    backdrop-filter: none;
  }

  .page-header,
  .form-actions {
    align-items: stretch;
    flex-direction: column;
  }

  .header-actions,
  .form-actions,
  .editor-mode {
    width: 100%;
  }

  .owner-count {
    margin-left: 0;
  }

  .header-actions .btn,
  .form-actions .btn,
  .editor-mode-button {
    width: 100%;
    min-height: 2.75rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .page-header,
  .owner-row,
  .skeleton-row,
  .spin {
    animation: none;
    transition: none;
    backdrop-filter: none;
  }
}
</style>
