<script setup lang="ts">
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { create } from "@bufbuild/protobuf";
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
  stringifyProcessConfigYaml,
  validateProcessConfigForm,
  validateProcessSpec,
  type ProcessConfigFormState,
  type ProcessConfigIssue,
} from "./processConfigForm";

const EDITOR_MODE = { form: "form", yaml: "yaml" } as const;
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

const nodesQuery = useQuery({
  queryKey: ["nodes"],
  queryFn: () => nodes.listNodes({}),
});

const nodeViews = computed(() => {
  const now = Date.now();
  return (nodesQuery.data.value?.nodes ?? []).map((raw) => mapNode(raw, now));
});

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

function switchEditorMode(next: EditorMode): void {
  if (next === editorMode.value) {
    return;
  }
  const spec = currentSpec();
  if (!spec) {
    return;
  }
  if (next === "yaml") {
    editorText.value = stringifyProcessConfigYaml(spec);
  } else {
    formDraft.value = specToProcessConfigForm(spec);
  }
  editorMode.value = next;
}

async function onSubmit(): Promise<void> {
  actionError.value = "";
  results.value = [];
  if (!canCreate.value) {
    return;
  }
  const spec = currentSpec();
  if (!spec) {
    return;
  }
  const owners = selectedOwnerIds.value;
  if (!owners.length) {
    actionError.value = t("processes.create.needOwner");
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
  }
}
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <RouterLink class="back" to="/processes">{{ t("processDetail.back") }}</RouterLink>
        <h1>{{ t("processes.create.title") }}</h1>
        <p class="muted">{{ t("processes.create.hint") }}</p>
      </div>
      <button type="button" class="btn btn-primary" :disabled="submitting || !canCreate" @click="onSubmit">
        {{ t("processes.create.submit") }}
      </button>
    </header>
    <p v-if="!canCreate" class="error" role="alert">{{ t("processes.create.noPermission") }}</p>
    <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
    <ul v-if="results.length" class="results">
      <li v-for="line in results" :key="line">{{ line }}</li>
    </ul>

    <section class="card">
      <h2>{{ t("processes.create.owners") }}</h2>
      <p class="muted">{{ t("processes.create.ownersHint") }}</p>
      <ul class="owner-list">
        <li v-for="node in nodeViews" :key="node.nodeId">
          <label class="owner-row" :class="{ blocked: ownerBlocked(node) }">
            <input
              type="checkbox"
              :checked="selectedOwnerIds.includes(node.nodeId)"
              :disabled="ownerBlocked(node)"
              :title="ownerReason(node)"
              @change="toggleOwner(node)"
            />
            <span>{{ node.hostname || node.nodeId }}</span>
            <span v-if="ownerBlocked(node)" class="muted">{{ ownerReason(node) }}</span>
          </label>
        </li>
      </ul>
      <p v-if="!nodeViews.length" class="muted">{{ t("processes.create.noNodes") }}</p>
    </section>

    <section class="card">
      <div class="editor-mode" role="group" :aria-label="t('processConfig.editor.modeLabel')">
        <button
          type="button"
          class="editor-mode-button"
          :class="{ active: editorMode === EDITOR_MODE.form }"
          @click="switchEditorMode(EDITOR_MODE.form)"
        >
          {{ t("processConfig.editor.mode.form") }}
        </button>
        <button
          type="button"
          class="editor-mode-button"
          :class="{ active: editorMode === EDITOR_MODE.yaml }"
          @click="switchEditorMode(EDITOR_MODE.yaml)"
        >
          {{ t("processConfig.editor.mode.yaml") }}
        </button>
      </div>
      <ProcessConfigForm
        v-if="editorMode === EDITOR_MODE.form"
        :model-value="formDraft"
        :issues="formIssues"
        :validate-requested="validateRequested"
        @update:model-value="formDraft = $event"
      />
      <label v-else class="field">
        <span>{{ t("processConfig.config.specLabel") }}</span>
        <textarea v-model="editorText" class="input editor" rows="24" spellcheck="false" />
        <p v-if="yamlError" class="field-error" role="alert">{{ yamlError }}</p>
      </label>
      <label class="field">
        <span>{{ t("processConfig.config.commentLabel") }}</span>
        <input v-model="comment" class="input" type="text" />
      </label>
    </section>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  align-items: flex-start;
}
.back {
  display: inline-block;
  margin-bottom: 0.5rem;
}
.card {
  margin-top: 1rem;
  padding: 1rem;
}
.owner-list {
  list-style: none;
  padding: 0;
  margin: 0.75rem 0 0;
  display: grid;
  gap: 0.5rem;
}
.owner-row {
  display: flex;
  gap: 0.5rem;
  align-items: center;
}
.owner-row.blocked {
  opacity: 0.7;
}
.editor-mode {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 1rem;
}
.editor-mode-button.active {
  font-weight: 600;
}
.editor {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  min-height: 16rem;
}
.results {
  margin: 0.75rem 0;
}
.field {
  display: grid;
  gap: 0.35rem;
  margin-top: 1rem;
}
</style>
