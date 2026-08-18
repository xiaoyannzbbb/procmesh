# Process Config Dual Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a schema-driven field form and protobuf JSON dual-mode editor that covers every `ProcessSpec` configuration field without regressing save, revision, conflict, or unsaved-change behavior.

**Architecture:** A typed UI schema defines scalar controls, sections, options, units, and visibility. Pure utilities own `ProcessSpec` conversion and validation; a focused Vue form renders the schema plus repeatable row editors; `ProcessConfigPanel` owns the canonical draft and synchronizes modes before switching or saving.

**Tech Stack:** Vue 3 Composition API, TypeScript, Buf Protobuf ES, Vitest, Vue Test Utils, i18next, Lucide Vue, existing CSS tokens.

**Spec:** `docs/superpowers/specs/2026-08-17-process-config-dual-editor-design.md`

## Global Constraints

- Cover every field in `procmesh.v1.ProcessSpec`, including all nested messages and repeated/map fields.
- Keep `processId` and `latestRevision` read-only and restore their loaded values before submission.
- Use protobuf JSON as the JSON-mode contract; do not add a JSON-schema or editor dependency.
- Keep raw API units: milliseconds, seconds, bytes, and CPU quota millis.
- Do not apply backend defaults or erase health fields hidden by the selected type.
- Invalid active-mode content blocks mode switching and saving.
- Preserve current CAS conflict, refetch protection, permission, and dirty-close behavior.
- Add English and Simplified Chinese copy with complete i18n type generation.
- Work test-first and keep all changes uncommitted for user review.

---

## File Map

- Create `web/src/pages/processConfigSchema.ts`: typed UI schema and exhaustive field-path inventory.
- Create `web/src/pages/processConfigForm.ts`: form types, protobuf conversions, JSON synchronization, and validation.
- Create `web/src/pages/processConfigForm.test.ts`: utility round-trip, coverage, and validation tests.
- Create `web/src/pages/ProcessConfigForm.vue`: schema renderer and row editors.
- Create `web/src/pages/ProcessConfigForm.test.ts`: form rendering, conditional visibility, row editing, and accessibility tests.
- Modify `web/src/pages/ProcessConfigPanel.vue`: dual-mode orchestration and drawer layout.
- Modify `web/src/pages/ProcessConfigPanel.test.ts`: mode synchronization, save, conflict, and dirty-state integration tests.
- Modify `web/public/locales/en/common.json` and `web/public/locales/zh/common.json`: editor labels, options, help, and errors.
- Regenerate `web/src/types/i18n.d.ts` using the existing script.

---

### Task 1: Typed Schema, Conversion, and Validation

**Files:**
- Create: `web/src/pages/processConfigSchema.ts`
- Create: `web/src/pages/processConfigForm.ts`
- Test: `web/src/pages/processConfigForm.test.ts`

**Interfaces:**
- Produces: `ProcessConfigFormState`, `ProcessConfigIssue`, `PROCESS_CONFIG_SECTIONS`, `PROCESS_CONFIG_FIELD_PATHS`, `specToProcessConfigForm`, `processConfigFormToSpec`, `parseProcessConfigJson`, `stringifyProcessConfigJson`, `validateProcessConfigForm`, and `validateProcessSpec`.
- Consumes: generated `ProcessSpecSchema` and nested message schemas from `web/src/gen/procmesh/v1/api_pb.ts`.

- [ ] **Step 1: Write failing exhaustive round-trip and schema-coverage tests**

Create a fully populated spec and assert every leaf survives form and JSON round trips. Assert the exported field inventory exactly equals the expected set, including read-only fields.

```ts
const fullSpec = create(ProcessSpecSchema, {
  processId: "p1",
  name: "api",
  ownerAgentId: "node-a",
  group: "services.core",
  command: "/usr/bin/api",
  args: ["serve", "--port=8080"],
  workingDirectory: "/srv/api",
  runAsUser: "svc-api",
  environment: { PORT: "8080", MODE: "prod" },
  instances: 2,
  autostart: true,
  stopSignal: "SIGTERM",
  killSignal: "SIGKILL",
  stopTimeoutMs: 10_000n,
  startupPriority: 20,
  restart: {
    mode: "on-failure",
    maxRetries: 5,
    retryWindowMs: 60_000n,
    backoff: { initialMs: 500n, maxMs: 30_000n, multiplier: 2 },
  },
  health: {
    type: "http",
    url: "http://127.0.0.1:8080/health",
    method: "GET",
    address: "127.0.0.1:8080",
    command: "/usr/bin/check-api",
    expectedStatus: 204,
    args: ["--quiet"],
    initialDelayMs: 1_000n,
    intervalMs: 5_000n,
    timeoutMs: 1_000n,
    failureThreshold: 3,
    successThreshold: 2,
    restartOnFailure: true,
    restartCooldownMs: 30_000n,
  },
  log: { maxSize: 104_857_600n, maxFiles: 10, maxAgeSeconds: 604_800n, compress: true },
  resources: { cpuQuotaMillis: 500n, memoryBytes: 536_870_912n, openFiles: 4096n },
  dependencies: [{ processName: "db", condition: "HEALTHY" }],
  latestRevision: 7n,
});

expect(processConfigFormToSpec(specToProcessConfigForm(fullSpec))).toEqual(fullSpec);
expect(parseProcessConfigJson(stringifyProcessConfigJson(fullSpec))).toEqual(fullSpec);
expect(PROCESS_CONFIG_FIELD_PATHS).toEqual(EXPECTED_ALL_LEAF_PATHS);
```

- [ ] **Step 2: Run the utility test and verify RED**

Run: `cd web && npm test -- processConfigForm.test.ts`

Expected: FAIL because `processConfigForm.ts` and `processConfigSchema.ts` do not exist.

- [ ] **Step 3: Implement form types, field schema, and lossless conversions**

Represent integer inputs as decimal strings to preserve `int64` precision while editing. Define schema controls with exact paths and conditional visibility.

```ts
export type ProcessConfigField = {
  path: ProcessConfigFieldPath;
  section: ProcessConfigSectionId;
  control: "text" | "integer" | "decimal" | "select" | "boolean" | "readonly";
  labelKey: string;
  helpKey?: string;
  unitKey?: string;
  options?: readonly { value: string; labelKey: string }[];
  visible?: (form: ProcessConfigFormState) => boolean;
};

export function specToProcessConfigForm(spec: ProcessSpec): ProcessConfigFormState;
export function processConfigFormToSpec(form: ProcessConfigFormState): ProcessSpec;
export function stringifyProcessConfigJson(spec: ProcessSpec): string;
export function parseProcessConfigJson(text: string): ProcessSpec;
```

Use `create(ProcessSpecSchema, ...)` and the generated nested schemas so optional nested messages are valid protobuf messages. Convert `bigint` with base-10 strings and reject unsafe or malformed values instead of routing them through `number`.

- [ ] **Step 4: Run the utility test and verify GREEN**

Run: `cd web && npm test -- processConfigForm.test.ts`

Expected: round-trip and field-inventory tests PASS.

- [ ] **Step 5: Write failing validation tests**

Add table-driven cases for name, group, command, instances, retry window, multiplier, HTTP URL scheme, TCP address, exec command, duplicate environment key, duplicate dependency, invalid integer text, `int32` bounds, and `int64` bounds.

```ts
it.each([
  ["name", (f: ProcessConfigFormState) => { f.name = "1bad"; }, "invalidName"],
  ["command", (f) => { f.command = ""; }, "required"],
  ["instances", (f) => { f.instances = "0"; }, "minimumOne"],
  ["restart.retryWindowMs", (f) => { f.restart.maxRetries = "1"; f.restart.retryWindowMs = "0"; }, "retryWindowRequired"],
  ["health.url", (f) => { f.health.type = "http"; f.health.url = "file:///tmp/x"; }, "httpUrlRequired"],
])("reports %s", (path, mutate, code) => {
  const form = specToProcessConfigForm(fullSpec);
  mutate(form);
  expect(validateProcessConfigForm(form)).toContainEqual(expect.objectContaining({ path, code }));
});
```

- [ ] **Step 6: Run validation tests and verify RED**

Run: `cd web && npm test -- processConfigForm.test.ts`

Expected: FAIL because validation functions are missing.

- [ ] **Step 7: Implement structured validation issues**

```ts
export type ProcessConfigIssue = {
  path: string;
  code: ProcessConfigIssueCode;
};

export function validateProcessConfigForm(form: ProcessConfigFormState): ProcessConfigIssue[];
export function validateProcessSpec(spec: ProcessSpec): ProcessConfigIssue[] {
  return validateProcessConfigForm(specToProcessConfigForm(spec));
}
```

Keep utilities translation-free; Vue components map `code` to `processConfig.editor.validation.<code>`.

- [ ] **Step 8: Run utility tests and refactor while green**

Run: `cd web && npm test -- processConfigForm.test.ts`

Expected: all utility tests PASS with no warnings.

- [ ] **Step 9: Review checkpoint (no commit)**

Run: `git diff --check && git status --short`

Expected: only the design, plan, and Task 1 files are modified/untracked; do not commit.

---

### Task 2: Schema-Driven Field Form Component

**Files:**
- Create: `web/src/pages/ProcessConfigForm.vue`
- Create: `web/src/pages/ProcessConfigForm.test.ts`

**Interfaces:**
- Consumes: `ProcessConfigFormState`, `ProcessConfigIssue`, and `PROCESS_CONFIG_SECTIONS` from Task 1.
- Produces: a controlled component with `modelValue`, `issues`, `validateRequested`, and `update:modelValue`; exposes `focusIssue(path: string): void`.

- [ ] **Step 1: Write failing rendering and visibility tests**

Mount the form with a complete model and assert section legends, generated scalar controls, explicit units, and read-only semantics. Switch `health.type` through all four values and assert only the applicable HTTP/TCP/exec controls are visible while hidden values remain in emitted state.

```ts
expect(wrapper.get('[data-field="name"]').attributes("aria-required")).toBe("true");
expect(wrapper.get('[data-field="processId"]').attributes("readonly")).toBeDefined();
await wrapper.get('[data-field="health.type"]').setValue("tcp");
expect(wrapper.find('[data-field="health.address"]').exists()).toBe(true);
expect(wrapper.find('[data-field="health.url"]').exists()).toBe(false);
expect(wrapper.emitted("update:modelValue")?.at(-1)?.[0].health.url).toBe(originalUrl);
```

- [ ] **Step 2: Run component test and verify RED**

Run: `cd web && npm test -- ProcessConfigForm.test.ts`

Expected: FAIL because `ProcessConfigForm.vue` does not exist.

- [ ] **Step 3: Implement schema rendering and responsive section layout**

Render fields with `v-for` over each section's schema. Use native input/select/checkbox semantics, visible labels, `aria-describedby`, `aria-invalid`, and stable `data-field` attributes. Use existing CSS variables, 4/8px spacing, 44px minimum control height at narrow widths, and no nested decorative cards.

```vue
<fieldset v-for="section in PROCESS_CONFIG_SECTIONS" :key="section.id" class="editor-section">
  <legend>{{ t(section.labelKey) }}</legend>
  <template v-for="field in visibleFields(section.id)" :key="field.path">
    <label :for="fieldId(field.path)">{{ t(field.labelKey) }}</label>
    <select v-if="field.control === 'select'" :id="fieldId(field.path)" :data-field="field.path" />
    <input v-else :id="fieldId(field.path)" :data-field="field.path" />
    <p v-if="issueFor(field.path)" role="alert">{{ issueMessage(field.path) }}</p>
  </template>
</fieldset>
```

- [ ] **Step 4: Run rendering tests and verify GREEN**

Run: `cd web && npm test -- ProcessConfigForm.test.ts`

Expected: schema rendering and conditional visibility tests PASS.

- [ ] **Step 5: Write failing collection and error-focus tests**

Test ordered process/health arguments, environment rows, dependencies, accessible icon removal buttons, inline errors, linked error summary, and exposed focus behavior.

```ts
await wrapper.get('[data-action="add-environment"]').trigger("click");
expect(wrapper.findAll('[data-row="environment"]')).toHaveLength(3);
await wrapper.findAll('[data-action="remove-environment"]')[0].trigger("click");
expect(wrapper.findAll('[data-row="environment"]')).toHaveLength(2);
expect(wrapper.get('[data-error-summary]').attributes("tabindex")).toBe("-1");
```

- [ ] **Step 6: Run collection tests and verify RED**

Run: `cd web && npm test -- ProcessConfigForm.test.ts`

Expected: FAIL because collection controls and focus summary are absent.

- [ ] **Step 7: Implement repeatable row editors and accessible errors**

Use Lucide `Plus` and `Trash2` icons beside visible add text or accessible icon-only remove labels. Emit cloned state for every change. Preserve array order and map row values. Implement `defineExpose({ focusIssue })` using stable field IDs.

- [ ] **Step 8: Run form tests and refactor while green**

Run: `cd web && npm test -- ProcessConfigForm.test.ts processConfigForm.test.ts`

Expected: both suites PASS with no Vue warnings.

- [ ] **Step 9: Review checkpoint (no commit)**

Run: `git diff --check && git status --short`

Expected: Task 1 and Task 2 changes remain uncommitted.

---

### Task 3: Drawer Dual-Mode Integration

**Files:**
- Modify: `web/src/pages/ProcessConfigPanel.vue`
- Modify: `web/src/pages/ProcessConfigPanel.test.ts`

**Interfaces:**
- Consumes: conversion/validation functions from Task 1 and `ProcessConfigForm.vue` from Task 2.
- Produces: a drawer with `form` and `json` modes using one synchronized canonical draft.

- [ ] **Step 1: Rewrite editor integration tests to expect form-first behavior**

Preserve existing read-only, save, conflict, refetch, and revision assertions. Change editor helpers to select modes explicitly, then add tests for valid form-to-JSON-to-form synchronization.

```ts
await openEditor(wrapper);
expect(document.querySelector('[data-editor-mode="form"][aria-pressed="true"]')).not.toBeNull();
await setDrawerField("name", "api");
clickEditorMode("json");
expect(editorTextarea().value).toContain('"name": "api"');
setEditorText(editorTextarea().value.replace('"instances": 2', '"instances": 3'));
clickEditorMode("form");
expect(drawerField("instances").value).toBe("3");
```

- [ ] **Step 2: Run panel tests and verify RED**

Run: `cd web && npm test -- ProcessConfigPanel.test.ts`

Expected: FAIL because form mode and mode controls do not exist.

- [ ] **Step 3: Implement canonical draft and segmented mode control**

Replace the JSON-only drawer state with:

```ts
const editorMode = ref<"form" | "json">("form");
const formDraft = ref<ProcessConfigFormState | null>(null);
const editorText = ref("");
const formIssues = ref<ProcessConfigIssue[]>([]);

function switchEditorMode(next: "form" | "json"): void;
function synchronizeActiveMode(): ProcessSpec | null;
```

Opening initializes both representations from a cloned loaded spec. Switching validates/synchronizes the active representation before changing mode. Dirty state compares a normalized draft serialization plus comment against the opening baseline.

- [ ] **Step 4: Run synchronization tests and verify GREEN**

Run: `cd web && npm test -- ProcessConfigPanel.test.ts`

Expected: form-first and valid switching tests PASS.

- [ ] **Step 5: Write failing invalid-switch, save, and ownership tests**

Assert invalid form blocks JSON mode, invalid JSON blocks form mode, invalid active content blocks RPC submission, and successful form save sends every field while forcing loaded `processId` and `latestRevision`.

```ts
await setDrawerField("name", "1bad");
clickEditorMode("json");
expect(activeEditorMode()).toBe("form");
expect(document.querySelector('[data-error="name"]')).not.toBeNull();

await setDrawerField("name", "api");
submitEditor();
expect(updateConfig.mock.calls[0][0].spec.processId).toBe("p1");
expect(updateConfig.mock.calls[0][0].spec.latestRevision).toBe(3n);
```

- [ ] **Step 6: Run integration tests and verify RED**

Run: `cd web && npm test -- ProcessConfigPanel.test.ts`

Expected: new invalid-mode and full-submit assertions FAIL.

- [ ] **Step 7: Implement guarded switching, save validation, sticky footer, and focus handling**

Map utility issue codes to translated messages in the form. On failed save, show and focus the error summary/first invalid field. Keep the existing conflict branch unchanged so drafts and `expectedRevision` survive 409 and background refetches. Keep comment, cancel, and save in a sticky footer within the drawer scroll surface.

- [ ] **Step 8: Run focused integration suites and refactor while green**

Run: `cd web && npm test -- ProcessConfigPanel.test.ts ProcessConfigForm.test.ts processConfigForm.test.ts Drawer.test.ts`

Expected: all focused suites PASS with no warnings.

- [ ] **Step 9: Review checkpoint (no commit)**

Run: `git diff --check && git status --short`

Expected: all feature files remain uncommitted.

---

### Task 4: Internationalization and Complete Verification

**Files:**
- Modify: `web/public/locales/en/common.json`
- Modify: `web/public/locales/zh/common.json`
- Modify (generated): `web/src/types/i18n.d.ts`
- Modify: `web/src/pages/ProcessConfigPanel.test.ts`
- Modify: `web/src/pages/ProcessConfigForm.test.ts`

**Interfaces:**
- Consumes: every `labelKey`, `helpKey`, `unitKey`, option key, and validation code exported or referenced by Tasks 1-3.
- Produces: complete English/Chinese resources and generated typed translation keys.

- [ ] **Step 1: Write failing translation-completeness assertions**

Add a test that flattens every key referenced by `PROCESS_CONFIG_SECTIONS` and confirms both loaded resource languages return non-key text. Include editor mode labels, collection actions, units, options, error summary, and all validation codes.

```ts
for (const key of processConfigTranslationKeys()) {
  expect(i18n.getFixedT("en")(key)).not.toBe(key);
  expect(i18n.getFixedT("zh")(key)).not.toBe(key);
}
```

- [ ] **Step 2: Run translation tests and verify RED**

Run: `cd web && npm test -- ProcessConfigForm.test.ts ProcessConfigPanel.test.ts`

Expected: FAIL and report the newly required missing keys.

- [ ] **Step 3: Add matching English and Chinese editor resources**

Add corresponding trees under `processConfig.editor` in both `common.json` files. Keep field names concise, put units in suffix copy, and make validation text state both cause and correction.

- [ ] **Step 4: Generate types and verify i18n completeness**

Run: `cd web && npm run i18n:types && npm run i18n:check`

Expected: generated `web/src/types/i18n.d.ts` includes all new keys and completeness check PASS.

- [ ] **Step 5: Run full automated verification**

Run: `cd web && npm test`

Expected: full Vitest suite PASS.

Run: `cd web && npm run lint`

Expected: ESLint PASS with no errors.

Run: `cd web && npm run build:check`

Expected: Vue type-check and production Vite build PASS.

- [ ] **Step 6: Run browser verification against the requested route**

Start the existing Vite server or use a free port, then open:

`http://localhost:5173/processes/clock?node=a0ba0978-70ed-4664-8d80-133c6c862f86`

Verify desktop and 375px viewport screenshots, drawer scrolling, sticky footer, form/JSON synchronization, conditional health fields, keyboard focus after validation, no content overlap or horizontal page scroll, reduced-motion behavior, and both available themes. Confirm the route still uses the live authenticated session and do not mutate production-like process data during visual checks unless saving is explicitly safe.

- [ ] **Step 7: Final uncommitted diff review**

Run: `git diff --check && git status --short && git diff --stat`

Expected: no whitespace errors; only scoped design, plan, form/editor, tests, locale, and generated i18n type files are changed. Do not commit.
