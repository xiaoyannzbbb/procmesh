<script setup lang="ts">
/* eslint-disable i18next/no-literal-string -- Template literals are non-visible schema paths and selectors; visible copy uses t(). */
import { Plus, Trash2 } from "lucide-vue-next";
import { computed, nextTick, ref } from "vue";
import { useI18n } from "../lib/useI18n";
import type { ProcessConfigFormState, ProcessConfigIssue } from "./processConfigForm";
import {
  PROCESS_CONFIG_FIELDS,
  PROCESS_CONFIG_SECTIONS,
  type ProcessConfigField,
  type ProcessConfigFieldPath,
  type ProcessConfigSectionId,
} from "./processConfigSchema";

const props = withDefaults(defineProps<{
  modelValue: ProcessConfigFormState;
  issues: ProcessConfigIssue[];
  validateRequested: number;
  hiddenPaths?: readonly ProcessConfigFieldPath[];
}>(), {
  hiddenPaths: () => [],
});

const emit = defineEmits<{
  "update:modelValue": [value: ProcessConfigFormState];
  "blur-field": [path: string];
}>();

const { t: typedTranslate } = useI18n();
const t = typedTranslate as unknown as (key: string, options?: Record<string, unknown>) => string;
const formRoot = ref<HTMLElement | null>(null);

const collectionPaths = new Set<ProcessConfigFieldPath>([
  "args",
  "health.args",
  "environment",
  "dependencies",
]);

const executionFieldOrder = new Map<ProcessConfigFieldPath, number>([
  ["workingDirectory", 0],
  ["runAsUser", 1],
  ["command", 2],
]);

const hiddenPaths = computed(() => new Set(props.hiddenPaths ?? []));

const fieldsBySection = computed(() => {
  const sections = new Map<ProcessConfigSectionId, ProcessConfigField[]>();
  for (const section of PROCESS_CONFIG_SECTIONS) {
    sections.set(section.id, []);
  }
  for (const field of PROCESS_CONFIG_FIELDS) {
    if (
      !hiddenPaths.value.has(field.path)
      && !collectionPaths.has(field.path)
      && (!field.visible || field.visible(props.modelValue))
    ) {
      sections.get(field.section)?.push(field);
    }
  }
  sections.get("execution")?.sort((left, right) => (
    (executionFieldOrder.get(left.path) ?? Number.MAX_SAFE_INTEGER)
    - (executionFieldOrder.get(right.path) ?? Number.MAX_SAFE_INTEGER)
  ));
  return sections;
});

function cloneModel(): ProcessConfigFormState {
  return JSON.parse(JSON.stringify(props.modelValue)) as ProcessConfigFormState;
}

function fieldId(path: string): string {
  return `process-config-${path.replaceAll(".", "-")}`;
}

function fieldValue(path: ProcessConfigFieldPath): string | boolean {
  const segments = path.split(".");
  let value: unknown = props.modelValue;
  for (const segment of segments) {
    value = (value as Record<string, unknown>)[segment];
  }
  return value as string | boolean;
}

function setFieldValue(model: ProcessConfigFormState, path: ProcessConfigFieldPath, value: string | boolean): void {
  const segments = path.split(".");
  let target: Record<string, unknown> = model as unknown as Record<string, unknown>;
  for (const segment of segments.slice(0, -1)) {
    target = target[segment] as Record<string, unknown>;
  }
  target[segments.at(-1)!] = value;
}

function updateField(field: ProcessConfigField, event: Event): void {
  const control = event.currentTarget as HTMLInputElement | HTMLSelectElement;
  const value = control instanceof HTMLInputElement && control.type === "checkbox"
    ? control.checked
    : control.value;
  const next = cloneModel();
  setFieldValue(next, field.path, value);
  emit("update:modelValue", next);
}

function inputMode(field: ProcessConfigField): "numeric" | "decimal" | undefined {
  if (field.control === "integer") {
    return "numeric";
  }
  if (field.control === "decimal") {
    return "decimal";
  }
  return undefined;
}

function issueFor(path: string): ProcessConfigIssue | undefined {
  return props.issues.find((issue) => issue.path === path);
}

function issueId(path: string): string {
  return `${fieldId(path)}-error`;
}

function issueMessage(issue: ProcessConfigIssue): string {
  return t(`processConfig.editor.validation.${issue.code}`);
}

function issueLabel(path: string): string {
  const field = PROCESS_CONFIG_FIELDS.find((candidate) => candidate.path === path);
  if (field) {
    return t(field.labelKey);
  }

  const rowField = path.match(/^(args|health\.args|environment|dependencies)\.(\d+)(?:\.(key|value|processName|condition))?$/);
  if (!rowField) {
    return path;
  }

  const [, collection, index, property] = rowField;
  const labelKey = property === "key"
    ? "processConfig.editor.environmentKey"
    : property === "value"
      ? "processConfig.editor.environmentValue"
      : property === "processName"
        ? "processConfig.editor.dependencyProcess"
        : property === "condition"
          ? "processConfig.editor.dependencyCondition"
          : collection === "health.args"
            ? "processConfig.editor.field.healthArgs"
            : "processConfig.editor.field.args";
  return `${t(labelKey)} ${Number(index) + 1}`;
}

function describedBy(path: string): string | undefined {
  return issueFor(path) ? issueId(path) : undefined;
}

function emitModel(next: ProcessConfigFormState): void {
  emit("update:modelValue", next);
}

function updateArgument(path: "args" | "health.args", index: number, event: Event): void {
  const next = cloneModel();
  const args = path === "args" ? next.args : next.health.args;
  args[index] = (event.currentTarget as HTMLInputElement).value;
  emitModel(next);
}

function addArgument(path: "args" | "health.args"): void {
  const next = cloneModel();
  (path === "args" ? next.args : next.health.args).push("");
  emitModel(next);
}

function removeArgument(path: "args" | "health.args", index: number): void {
  const next = cloneModel();
  (path === "args" ? next.args : next.health.args).splice(index, 1);
  emitModel(next);
}

function addEnvironment(): void {
  const next = cloneModel();
  next.environment.push({ key: "", value: "" });
  emitModel(next);
}

function updateEnvironment(index: number, property: "key" | "value", event: Event): void {
  const next = cloneModel();
  next.environment[index][property] = (event.currentTarget as HTMLInputElement).value;
  emitModel(next);
}

function removeEnvironment(index: number): void {
  const next = cloneModel();
  next.environment.splice(index, 1);
  emitModel(next);
}

function addDependency(): void {
  const next = cloneModel();
  next.dependencies.push({ processName: "", condition: "STARTED" });
  emitModel(next);
}

function updateDependency(index: number, property: "processName" | "condition", event: Event): void {
  const next = cloneModel();
  next.dependencies[index][property] = (event.currentTarget as HTMLInputElement | HTMLSelectElement).value;
  emitModel(next);
}

function removeDependency(index: number): void {
  const next = cloneModel();
  next.dependencies.splice(index, 1);
  emitModel(next);
}

function focusTarget(path: string): HTMLElement | undefined {
  const targets = formRoot.value?.querySelectorAll<HTMLElement>("[data-field], [data-collection]") ?? [];
  return Array.from(targets).find((target) => (
    target.dataset.field === path || target.dataset.collection === path
  ));
}

async function focusIssue(path: string): Promise<void> {
  await nextTick();
  focusTarget(path)?.focus();
}

function onFieldBlur(event: FocusEvent): void {
  const path = (event.target as HTMLElement | null)?.dataset.field;
  if (path) {
    emit("blur-field", path);
  }
}

defineExpose({ focusIssue });
</script>

<template>
  <div ref="formRoot" class="process-config-form" @blur.capture="onFieldBlur">
    <div
      v-if="issues.length && validateRequested > 0"
      class="error-summary"
      data-error-summary
      role="alert"
      tabindex="-1"
    >
      <p>{{ t("processConfig.editor.errorSummary") }}</p>
      <ul>
        <li v-for="issue in issues" :key="`${issue.path}-${issue.code}`">
          <a :href="`#${fieldId(issue.path)}`" @click.prevent="focusIssue(issue.path)">
            {{ issueLabel(issue.path) }}: {{ issueMessage(issue) }}
          </a>
        </li>
      </ul>
    </div>

    <fieldset
      v-for="section in PROCESS_CONFIG_SECTIONS"
      :key="section.id"
      class="editor-section"
      :data-section="section.id"
    >
      <legend>{{ t(section.labelKey) }}</legend>
      <div class="field-grid">
        <div
          v-for="field in fieldsBySection.get(section.id)"
          :key="field.path"
          class="field-control"
          :class="{ 'boolean-control': field.control === 'boolean' }"
          :data-control="field.path"
        >
          <label :for="fieldId(field.path)">
            <input
              v-if="field.control === 'boolean'"
              :id="fieldId(field.path)"
              type="checkbox"
              :checked="Boolean(fieldValue(field.path))"
              :data-field="field.path"
              :aria-invalid="issueFor(field.path) ? 'true' : undefined"
              :aria-describedby="describedBy(field.path)"
              @change="updateField(field, $event)"
            />
            <span>{{ t(field.labelKey) }}</span>
          </label>

          <select
            v-if="field.control === 'select'"
            :id="fieldId(field.path)"
            class="input"
            :value="String(fieldValue(field.path))"
            :data-field="field.path"
            :aria-invalid="issueFor(field.path) ? 'true' : undefined"
            :aria-describedby="describedBy(field.path)"
            @change="updateField(field, $event)"
          >
            <option value=""></option>
            <option v-for="option in field.options" :key="option.value" :value="option.value">
              {{ t(option.labelKey) }}
            </option>
          </select>

          <div v-else-if="field.control !== 'boolean'" class="input-with-unit">
            <input
              :id="fieldId(field.path)"
              class="input"
              type="text"
              :value="String(fieldValue(field.path))"
              :readonly="field.control === 'readonly'"
              :aria-required="field.path === 'name' || field.path === 'command' ? 'true' : undefined"
              :inputmode="inputMode(field)"
              :data-field="field.path"
              :aria-invalid="issueFor(field.path) ? 'true' : undefined"
              :aria-describedby="describedBy(field.path)"
              @input="updateField(field, $event)"
            />
            <span v-if="field.unitKey" class="unit">{{ t(field.unitKey) }}</span>
          </div>
          <p
            v-if="issueFor(field.path)"
            :id="issueId(field.path)"
            class="field-error"
            :data-error="field.path"
            role="alert"
          >
            {{ issueMessage(issueFor(field.path)!) }}
          </p>
        </div>

        <div v-if="section.id === 'execution'" class="collection-editor" data-collection="args">
          <div v-for="(argument, index) in modelValue.args" :key="index" class="collection-row argument-row" data-row="args">
            <label :for="fieldId(`args.${index}`)">
              {{ t("processConfig.editor.field.args") }} {{ index + 1 }}
            </label>
            <div class="row-controls">
              <input
                :id="fieldId(`args.${index}`)"
                class="input"
                type="text"
                :value="argument"
                :data-field="`args.${index}`"
                @input="updateArgument('args', index, $event)"
              />
              <button
                type="button"
                class="icon-button"
                data-action="remove-argument"
                :aria-label="t('processConfig.editor.remove.argument', { number: index + 1 })"
                @click="removeArgument('args', index)"
              >
                <Trash2 :size="18" aria-hidden="true" />
              </button>
            </div>
          </div>
          <button type="button" class="btn add-button" data-action="add-argument" @click="addArgument('args')">
            <Plus :size="16" aria-hidden="true" />
            {{ t("processConfig.editor.add.argument") }}
          </button>
        </div>

        <div
          v-if="section.id === 'health' && modelValue.health.type === 'exec'"
          class="collection-editor"
          data-collection="health.args"
        >
          <div
            v-for="(argument, index) in modelValue.health.args"
            :key="index"
            class="collection-row argument-row"
            data-row="health-args"
          >
            <label :for="fieldId(`health.args.${index}`)">
              {{ t("processConfig.editor.field.healthArgs") }} {{ index + 1 }}
            </label>
            <div class="row-controls">
              <input
                :id="fieldId(`health.args.${index}`)"
                class="input"
                type="text"
                :value="argument"
                :data-field="`health.args.${index}`"
                @input="updateArgument('health.args', index, $event)"
              />
              <button
                type="button"
                class="icon-button"
                data-action="remove-health-argument"
                :aria-label="t('processConfig.editor.remove.healthArgument', { number: index + 1 })"
                @click="removeArgument('health.args', index)"
              >
                <Trash2 :size="18" aria-hidden="true" />
              </button>
            </div>
          </div>
          <button
            type="button"
            class="btn add-button"
            data-action="add-health-argument"
            @click="addArgument('health.args')"
          >
            <Plus :size="16" aria-hidden="true" />
            {{ t("processConfig.editor.add.healthArgument") }}
          </button>
        </div>

        <div
          v-if="section.id === 'environment'"
          :id="fieldId('environment')"
          class="collection-editor"
          data-collection="environment"
          tabindex="-1"
          :aria-invalid="issueFor('environment') ? 'true' : undefined"
          :aria-describedby="describedBy('environment')"
        >
          <div
            v-for="(entry, index) in modelValue.environment"
            :key="index"
            class="collection-row environment-row"
            data-row="environment"
          >
            <label :for="fieldId(`environment.${index}.key`)">
              {{ t("processConfig.editor.environmentKey") }}
              <input
                :id="fieldId(`environment.${index}.key`)"
                class="input"
                type="text"
                :value="entry.key"
                :data-field="`environment.${index}.key`"
                @input="updateEnvironment(index, 'key', $event)"
              />
            </label>
            <label :for="fieldId(`environment.${index}.value`)">
              {{ t("processConfig.editor.environmentValue") }}
              <input
                :id="fieldId(`environment.${index}.value`)"
                class="input"
                type="text"
                :value="entry.value"
                :data-field="`environment.${index}.value`"
                @input="updateEnvironment(index, 'value', $event)"
              />
            </label>
            <button
              type="button"
              class="icon-button row-remove"
              data-action="remove-environment"
              :aria-label="t('processConfig.editor.remove.environment', { number: index + 1 })"
              @click="removeEnvironment(index)"
            >
              <Trash2 :size="18" aria-hidden="true" />
            </button>
          </div>
          <p
            v-if="issueFor('environment')"
            :id="issueId('environment')"
            class="field-error"
            data-error="environment"
            role="alert"
          >
            {{ issueMessage(issueFor("environment")!) }}
          </p>
          <button type="button" class="btn add-button" data-action="add-environment" @click="addEnvironment">
            <Plus :size="16" aria-hidden="true" />
            {{ t("processConfig.editor.add.environment") }}
          </button>
        </div>

        <div
          v-if="section.id === 'dependencies'"
          :id="fieldId('dependencies')"
          class="collection-editor"
          data-collection="dependencies"
          tabindex="-1"
          :aria-invalid="issueFor('dependencies') ? 'true' : undefined"
          :aria-describedby="describedBy('dependencies')"
        >
          <div
            v-for="(dependency, index) in modelValue.dependencies"
            :key="index"
            class="collection-row dependency-row"
            data-row="dependencies"
          >
            <label :for="fieldId(`dependencies.${index}.processName`)">
              {{ t("processConfig.editor.dependencyProcess") }}
              <input
                :id="fieldId(`dependencies.${index}.processName`)"
                class="input"
                type="text"
                :value="dependency.processName"
                :data-field="`dependencies.${index}.processName`"
                :aria-invalid="issueFor(`dependencies.${index}.processName`) ? 'true' : undefined"
                :aria-describedby="describedBy(`dependencies.${index}.processName`)"
                @input="updateDependency(index, 'processName', $event)"
              />
              <span
                v-if="issueFor(`dependencies.${index}.processName`)"
                :id="issueId(`dependencies.${index}.processName`)"
                class="field-error"
                :data-error="`dependencies.${index}.processName`"
                role="alert"
              >
                {{ issueMessage(issueFor(`dependencies.${index}.processName`)!) }}
              </span>
            </label>
            <label :for="fieldId(`dependencies.${index}.condition`)">
              {{ t("processConfig.editor.dependencyCondition") }}
              <select
                :id="fieldId(`dependencies.${index}.condition`)"
                class="input"
                :value="dependency.condition"
                :data-field="`dependencies.${index}.condition`"
                :aria-invalid="issueFor(`dependencies.${index}.condition`) ? 'true' : undefined"
                :aria-describedby="describedBy(`dependencies.${index}.condition`)"
                @change="updateDependency(index, 'condition', $event)"
              >
                <option value="STARTED">{{ t("processConfig.editor.option.dependency.STARTED") }}</option>
                <option value="HEALTHY">{{ t("processConfig.editor.option.dependency.HEALTHY") }}</option>
              </select>
              <span
                v-if="issueFor(`dependencies.${index}.condition`)"
                :id="issueId(`dependencies.${index}.condition`)"
                class="field-error"
                :data-error="`dependencies.${index}.condition`"
                role="alert"
              >
                {{ issueMessage(issueFor(`dependencies.${index}.condition`)!) }}
              </span>
            </label>
            <button
              type="button"
              class="icon-button row-remove"
              data-action="remove-dependency"
              :aria-label="t('processConfig.editor.remove.dependency', { number: index + 1 })"
              @click="removeDependency(index)"
            >
              <Trash2 :size="18" aria-hidden="true" />
            </button>
          </div>
          <p
            v-if="issueFor('dependencies')"
            :id="issueId('dependencies')"
            class="field-error"
            data-error="dependencies"
            role="alert"
          >
            {{ issueMessage(issueFor("dependencies")!) }}
          </p>
          <button type="button" class="btn add-button" data-action="add-dependency" @click="addDependency">
            <Plus :size="16" aria-hidden="true" />
            {{ t("processConfig.editor.add.dependency") }}
          </button>
        </div>
      </div>
    </fieldset>
  </div>
</template>

<style scoped>
.process-config-form {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 1rem;
}

.editor-section {
  min-width: 0;
  margin: 0;
  border: 0;
  border-top: 1px solid var(--color-border);
  padding: 1rem 0 0;
}

.editor-section:first-child {
  border-top: 0;
  padding-top: 0;
}

legend {
  padding: 0 0 0.75rem;
  color: var(--color-text);
  font-size: 0.9375rem;
  font-weight: 600;
}

.field-grid {
  display: grid;
  min-width: 0;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem 1rem;
}

.field-control {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 0.5rem;
}

.field-control[data-control="command"] {
  grid-column: 1 / -1;
}

.field-control > label,
.collection-editor label {
  color: var(--color-muted);
  font-size: 0.8125rem;
  font-weight: 500;
}

.boolean-control {
  align-self: end;
}

.boolean-control > label {
  display: flex;
  min-height: 2.5rem;
  align-items: center;
  gap: 0.5rem;
  color: var(--color-text);
  cursor: pointer;
}

.boolean-control input {
  width: 1rem;
  height: 1rem;
  accent-color: var(--color-accent);
}

.input-with-unit {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.5rem;
}

.input-with-unit .input {
  min-width: 0;
}

.input[readonly] {
  background: var(--color-bg);
  color: var(--color-muted);
  cursor: default;
}

.unit {
  flex: 0 0 auto;
  color: var(--color-muted);
  font-size: 0.75rem;
  overflow-wrap: anywhere;
}

.collection-editor {
  display: flex;
  min-width: 0;
  grid-column: 1 / -1;
  flex-direction: column;
  gap: 0.75rem;
}

.collection-row {
  display: grid;
  min-width: 0;
  gap: 0.5rem;
}

.collection-row label {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 0.5rem;
}

.argument-row {
  grid-template-columns: minmax(0, 1fr);
}

.row-controls {
  display: grid;
  min-width: 0;
  grid-template-columns: minmax(0, 1fr) 2.75rem;
  gap: 0.5rem;
}

.environment-row {
  grid-template-columns: repeat(2, minmax(0, 1fr)) 2.75rem;
}

.dependency-row {
  grid-template-columns: minmax(0, 1fr) minmax(7rem, 0.7fr) 2.75rem;
}

.icon-button {
  display: inline-flex;
  width: 2.75rem;
  min-width: 2.75rem;
  height: 2.75rem;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  color: var(--color-danger);
  cursor: pointer;
}

.icon-button:hover {
  border-color: var(--color-danger);
  background: color-mix(in srgb, var(--color-danger) 7%, var(--color-card));
}

.row-remove {
  align-self: end;
}

.add-button {
  align-self: flex-start;
}

.field-error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.8125rem;
  line-height: 1.4;
  overflow-wrap: anywhere;
}

.error-summary {
  border-left: 3px solid var(--color-danger);
  padding: 0.75rem 1rem;
  background: color-mix(in srgb, var(--color-danger) 6%, var(--color-card));
}

.error-summary p {
  margin: 0;
  font-weight: 600;
}

.error-summary ul {
  margin: 0.5rem 0 0;
  padding-left: 1.25rem;
}

.error-summary a {
  color: var(--color-danger);
  overflow-wrap: anywhere;
}

@media (max-width: 640px) {
  .field-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .input,
  .boolean-control > label {
    min-height: 44px;
  }

  .input-with-unit {
    align-items: stretch;
    flex-direction: column;
    gap: 0.25rem;
  }

  .environment-row,
  .dependency-row {
    grid-template-columns: minmax(0, 1fr) 2.75rem;
  }

  .environment-row label,
  .dependency-row label {
    grid-column: 1;
  }

  .environment-row .row-remove,
  .dependency-row .row-remove {
    grid-column: 2;
    grid-row: 1 / span 2;
  }

  .btn.add-button {
    min-height: 44px;
  }
}
</style>
