import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ErrorInfoSchema, ProcessSpecSchema } from "../gen/procmesh/v1/api_pb";
import { session } from "../lib/session";
import ProcessConfigPanel from "./ProcessConfigPanel.vue";
import panelSource from "./ProcessConfigPanel.vue?raw";

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          processConfig: {
            conflictBanner: "409 Conflict — reload and retry",
            loading: "Loading…",
            config: {
              title: "Config",
              reload: "Reload",
              processId: "Process ID",
              latestRevision: "Latest Revision",
              readOnlyNote: "process_id and latest_revision are read-only.",
              specLabel: "ProcessSpec JSON",
              commentLabel: "Comment",
              save: "Save",
            },
            editor: {
              modeLabel: "Editor mode",
              mode: { form: "Form", json: "JSON" },
              errorSummary: "Fix the following errors",
              validation: {
                invalidName: "Enter a valid process name",
                required: "This field is required",
              },
            },
            history: {
              title: "History",
              loading: "Loading history…",
              noRevisions: "No revisions",
              table: {
                select: "Select",
                revision: "Revision",
                operator: "Operator",
                time: "Time",
                comment: "Comment",
                rollback: "Rollback",
              },
              diff: {
                title: "Diff",
                loading: "Loading diff…",
                empty: "(empty)",
              },
              rollbackConfirm: "Rollback to revision {revision}? This writes a new revision.",
            },
          },
        },
      },
    },
  });
});

const mounted: Array<{ unmount: () => void }> = [];

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function conflictError(): ConnectError {
  return new ConnectError("revision mismatch", Code.FailedPrecondition, undefined, [
    { desc: ErrorInfoSchema, value: { code: "CONFLICT", message: "revision mismatch" } },
  ]);
}

function sampleSpec() {
  return create(ProcessSpecSchema, {
    processId: "p1",
    name: "web",
    command: "sleep",
    args: ["30"],
    group: "services",
    workingDirectory: "/srv/web",
    environment: { PORT: "8080" },
    instances: 2,
    autostart: true,
    latestRevision: 3n,
  });
}

function fullSampleSpec() {
  return create(ProcessSpecSchema, {
    processId: "p1",
    name: "web",
    ownerAgentId: "node-a",
    group: "services.core",
    command: "/usr/bin/web",
    args: ["serve", "--port=8080"],
    workingDirectory: "/srv/web",
    runAsUser: "svc-web",
    environment: { MODE: "prod", PORT: "8080" },
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
      command: "/usr/bin/check-web",
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
    latestRevision: 3n,
  });
}

type MountOpts = {
  updateConfig?: ReturnType<typeof vi.fn>;
  spec?: ReturnType<typeof sampleSpec>;
  queryClient?: QueryClient;
  revisions?: Array<{
    revision: bigint;
    operator: string;
    timestampUnixMs: bigint;
    comment: string;
    diff?: string;
  }>;
  diff?: string;
  permissions?: string[];
};

async function mountPanel(opts: MountOpts | ReturnType<typeof vi.fn> = {}) {
  const resolved = typeof opts === "function" ? { updateConfig: opts } : opts;
  const updateConfig = resolved.updateConfig ?? vi.fn().mockRejectedValue(conflictError());
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: resolved.permissions ?? ["process.config.update", "process.config.read"],
  };
  const queryClient =
    resolved.queryClient ??
    new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
  const spec = resolved.spec ?? sampleSpec();
  const configClient = {
    getConfig: vi.fn().mockResolvedValue({ spec }),
    updateConfig,
    history: vi.fn().mockResolvedValue({ revisions: resolved.revisions ?? [] }),
    diff: vi.fn().mockResolvedValue({ diff: resolved.diff ?? "" }),
    rollback: vi.fn(),
  };
  const wrapper = mount(ProcessConfigPanel, {
    props: { idOrName: "web", targetNodeId: "n1" },
    global: {
      plugins: [[VueQueryPlugin, { queryClient }], [I18NextVue, { i18next: i18n }]],
      provide: { configClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, updateConfig, configClient, queryClient };
}

async function openEditor(wrapper: Awaited<ReturnType<typeof mountPanel>>["wrapper"]): Promise<void> {
  await wrapper.get('[data-action="edit-config"]').trigger("click");
  await flushPromises();
  await wrapper.vm.$nextTick();
}

function editorTextarea(): HTMLTextAreaElement {
  return document.querySelector<HTMLTextAreaElement>('[data-field="config-json"]')!;
}

function drawerField(path: string): HTMLInputElement | HTMLSelectElement {
  return document.querySelector<HTMLInputElement | HTMLSelectElement>(`[data-field="${path}"]`)!;
}

async function setDrawerField(path: string, value: string): Promise<void> {
  const field = drawerField(path);
  field.value = value;
  field.dispatchEvent(new Event(field instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  await flushPromises();
}

function editorMode(mode: "form" | "json"): HTMLButtonElement {
  return document.querySelector<HTMLButtonElement>(`[data-editor-mode="${mode}"]`)!;
}

async function switchEditorMode(mode: "form" | "json"): Promise<void> {
  editorMode(mode).click();
  await flushPromises();
}

function activeEditorMode(): "form" | "json" | undefined {
  return document.querySelector<HTMLButtonElement>('[data-editor-mode][aria-pressed="true"]')?.dataset
    .editorMode as "form" | "json" | undefined;
}

function setEditorText(value: string): void {
  const textarea = editorTextarea();
  textarea.value = value;
  textarea.dispatchEvent(new Event("input", { bubbles: true }));
}

function submitEditor(): void {
  document.querySelector<HTMLFormElement>("form.config-form")!.dispatchEvent(
    new Event("submit", { bubbles: true, cancelable: true }),
  );
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("ProcessConfigPanel", () => {
  it("shows a structured read-only configuration before editing", async () => {
    const { wrapper } = await mountPanel();

    expect(wrapper.find("form.config-form").exists()).toBe(false);
    expect(wrapper.find("textarea").exists()).toBe(false);
    expect(wrapper.get('[data-section="execution"]').text()).toContain("sleep");
    expect(wrapper.get('[data-section="execution"]').text()).toContain("30");
    expect(wrapper.get('[data-section="environment"]').text()).toContain("PORT");
    expect(wrapper.get('[data-section="environment"]').text()).toContain("8080");
    expect(wrapper.get(".config-json-viewer").text()).toContain('"name": "web"');
  });

  it("opens form-first with an accessible segmented mode control and footer controls", async () => {
    const { wrapper } = await mountPanel();

    await openEditor(wrapper);

    expect(document.querySelector(".drawer-panel-wide")).not.toBeNull();
    expect(activeEditorMode()).toBe("form");
    expect(editorMode("form").getAttribute("aria-pressed")).toBe("true");
    expect(editorMode("json").getAttribute("aria-pressed")).toBe("false");
    expect(editorMode("form").parentElement?.getAttribute("aria-label")).toBe("Editor mode");
    expect(drawerField("name").value).toBe("web");
    expect(document.querySelector(".drawer-content .drawer-actions")).not.toBeNull();
    expect(document.querySelector(".drawer-actions #process-config-comment")).not.toBeNull();
  });

  it("keeps drawer editor styles sticky, overflow-safe, touch-sized, and on the spacing grid", () => {
    expect(panelSource).toMatch(/\.drawer-actions\s*\{(?=[^}]*position:\s*sticky)(?=[^}]*bottom:\s*-1\.5rem)[^}]*\}/s);
    expect(panelSource).toMatch(/\.drawer-form\s*\{(?=[^}]*min-width:\s*0)[^}]*\}/s);
    expect(panelSource).toMatch(/\.json-editor\s*\{(?=[^}]*min-width:\s*0)[^}]*\}/s);
    expect(panelSource).toMatch(/\.editor\s*\{(?=[^}]*box-sizing:\s*border-box)(?=[^}]*max-width:\s*100%)[^}]*\}/s);
    expect(panelSource).toMatch(/@media \(max-width:\s*720px\)[\s\S]*\.editor-mode-button,[\s\S]*\.drawer-actions \.btn\s*\{[^}]*min-height:\s*44px/s);
    expect(panelSource).toMatch(/@media \(max-width:\s*720px\)[\s\S]*\.drawer-comment\s*\{[^}]*flex:\s*0 1 auto/s);
    expect(panelSource).toMatch(/\.field\s*\{(?=[^}]*gap:\s*0\.25rem)[^}]*\}/s);
    expect(panelSource).toMatch(/\.field-error\s*\{(?=[^}]*margin:\s*-0\.5rem 0 0)[^}]*\}/s);
  });

  it("synchronizes valid edits in both directions", async () => {
    const { wrapper } = await mountPanel();

    await openEditor(wrapper);
    await setDrawerField("name", "api");
    await switchEditorMode("json");
    expect(activeEditorMode()).toBe("json");
    expect(editorTextarea().value).toContain('"name": "api"');

    setEditorText(editorTextarea().value.replace('"instances": 2', '"instances": 3'));
    await switchEditorMode("form");
    expect(activeEditorMode()).toBe("form");
    expect(drawerField("instances").value).toBe("3");
  });

  it("closes after a successful save from JSON mode", async () => {
    const saved = create(ProcessSpecSchema, {
      ...sampleSpec(),
      name: "api",
      latestRevision: 4n,
    });
    const { wrapper } = await mountPanel({ updateConfig: vi.fn().mockResolvedValue({ spec: saved }) });

    await openEditor(wrapper);
    await switchEditorMode("json");
    const textarea = editorTextarea();
    expect(textarea.value).toContain('"name": "web"');
    setEditorText(textarea.value.replace('"name": "web"', '"name": "api"'));
    submitEditor();
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(wrapper.get(".config-json-viewer").text()).toContain('"name": "api"');
  });

  it("keeps invalid JSON in the drawer and shows an inline error", async () => {
    const updateConfig = vi.fn().mockResolvedValue({ spec: sampleSpec() });
    const { wrapper } = await mountPanel({ updateConfig });
    await openEditor(wrapper);
    await switchEditorMode("json");

    setEditorText("{");
    await switchEditorMode("form");
    expect(activeEditorMode()).toBe("json");
    expect(document.activeElement).toBe(editorTextarea());
    expect(editorTextarea().getAttribute("aria-describedby")).toBe("process-config-json-error");

    submitEditor();
    await flushPromises();

    expect(updateConfig).not.toHaveBeenCalled();
    expect(document.querySelector('[data-error="config-json"]')?.textContent).toBeTruthy();
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    expect(document.activeElement).toBe(editorTextarea());
  });

  it("blocks switching and saving an invalid form, keeps its summary, and focuses the first issue", async () => {
    const updateConfig = vi.fn().mockResolvedValue({ spec: sampleSpec() });
    const { wrapper } = await mountPanel({ updateConfig });
    await openEditor(wrapper);

    await setDrawerField("name", "1bad");
    await switchEditorMode("json");

    expect(activeEditorMode()).toBe("form");
    expect(document.querySelector('[data-error="name"]')).not.toBeNull();
    expect(document.querySelector("[data-error-summary]")).not.toBeNull();
    expect(document.activeElement).toBe(drawerField("name"));

    submitEditor();
    await flushPromises();
    expect(updateConfig).not.toHaveBeenCalled();
    expect(document.querySelector("[data-error-summary]")).not.toBeNull();
    expect(document.activeElement).toBe(drawerField("name"));
  });

  it("validates an individual field on blur without showing the full error summary", async () => {
    const { wrapper } = await mountPanel();
    await openEditor(wrapper);

    const name = drawerField("name");
    name.value = "1bad";
    name.dispatchEvent(new Event("input", { bubbles: true }));
    name.dispatchEvent(new FocusEvent("blur", { bubbles: true }));
    await flushPromises();

    expect(document.querySelector('[data-error="name"]')).not.toBeNull();
    expect(document.querySelector("[data-error-summary]")).toBeNull();
  });

  it("asks before closing form and JSON drafts with semantic unsaved changes", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { wrapper } = await mountPanel({ updateConfig: vi.fn().mockResolvedValue({ spec: sampleSpec() }) });
    await openEditor(wrapper);

    await setDrawerField("name", "api");
    document.querySelector<HTMLButtonElement>('[data-action="cancel-config-edit"]')!.click();
    await wrapper.vm.$nextTick();

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();

    confirm.mockReturnValue(true);
    document.querySelector<HTMLButtonElement>('[data-action="cancel-config-edit"]')!.click();
    await wrapper.vm.$nextTick();
    expect(document.querySelector('[role="dialog"]')).toBeNull();

    confirm.mockClear();
    confirm.mockReturnValue(false);
    await openEditor(wrapper);
    await switchEditorMode("json");
    setEditorText(editorTextarea().value.replace('"name": "web"', '"name": "api"'));
    document.querySelector<HTMLButtonElement>('[data-action="cancel-config-edit"]')!.click();
    await wrapper.vm.$nextTick();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  });

  it("ignores JSON formatting and whitespace-only comments when checking dirty state", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { wrapper } = await mountPanel();
    await openEditor(wrapper);
    await switchEditorMode("json");

    const normalized = JSON.parse(editorTextarea().value) as object;
    setEditorText(JSON.stringify(normalized));
    const commentInput = document.querySelector<HTMLInputElement>("#process-config-comment")!;
    commentInput.value = "   ";
    commentInput.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector<HTMLButtonElement>('[data-action="cancel-config-edit"]')!.click();
    await wrapper.vm.$nextTick();

    expect(confirm).not.toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("ignores protobuf map insertion order in JSON-originated dirty checks", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { wrapper } = await mountPanel({ spec: fullSampleSpec() });
    await openEditor(wrapper);
    await switchEditorMode("json");

    const reordered = JSON.parse(editorTextarea().value) as { environment: Record<string, string> };
    reordered.environment = { PORT: "8080", MODE: "prod" };
    setEditorText(JSON.stringify(reordered));
    document.querySelector<HTMLButtonElement>('[data-action="cancel-config-edit"]')!.click();
    await wrapper.vm.$nextTick();

    expect(confirm).not.toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("ignores protobuf map row order in form-originated dirty checks", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { wrapper } = await mountPanel({ spec: fullSampleSpec() });
    await openEditor(wrapper);

    document.querySelectorAll<HTMLButtonElement>('[data-action="remove-environment"]')[0].click();
    await flushPromises();
    document.querySelector<HTMLButtonElement>('[data-action="add-environment"]')!.click();
    await flushPromises();
    await setDrawerField("environment.1.key", "MODE");
    await setDrawerField("environment.1.value", "prod");
    expect(
      Array.from(document.querySelectorAll<HTMLInputElement>('[data-field$=".key"]')).map((input) => input.value),
    ).toEqual(["PORT", "MODE"]);
    expect(
      Array.from(document.querySelectorAll<HTMLInputElement>('[data-field$=".value"]')).map((input) => input.value),
    ).toEqual(["8080", "prod"]);
    document.querySelector<HTMLButtonElement>('[data-action="cancel-config-edit"]')!.click();
    await wrapper.vm.$nextTick();

    expect(confirm).not.toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("submits every field while restoring loaded ownership and revision", async () => {
    const loaded = fullSampleSpec();
    const saved = create(ProcessSpecSchema, { ...loaded, name: "api", latestRevision: 4n });
    const updateConfig = vi.fn().mockResolvedValue({ spec: saved });
    const { wrapper } = await mountPanel({ spec: loaded, updateConfig });
    await openEditor(wrapper);
    await switchEditorMode("json");
    setEditorText(
      editorTextarea().value
        .replace('"processId": "p1"', '"processId": "other"')
        .replace('"name": "web"', '"name": "api"')
        .replace('"latestRevision": "3"', '"latestRevision": "99"'),
    );
    await switchEditorMode("form");

    submitEditor();
    await flushPromises();

    expect(updateConfig).toHaveBeenCalledTimes(1);
    const request = updateConfig.mock.calls[0][0];
    expect(request.spec).toEqual(create(ProcessSpecSchema, { ...loaded, name: "api" }));
    expect(request.expectedRevision).toBe(3n);
    expect(request.meta.operationId).toBeTruthy();
    expect(request.meta.operator).toBe("admin");
  });

  it("keeps editing unavailable without update permission", async () => {
    const { wrapper, updateConfig } = await mountPanel({ permissions: ["process.config.read"] });

    expect(wrapper.find('[data-action="edit-config"]').exists()).toBe(false);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(updateConfig).not.toHaveBeenCalled();
  });

  it("shows 409 Conflict when UpdateConfig throws CONFLICT", async () => {
    const { wrapper, updateConfig } = await mountPanel();
    await openEditor(wrapper);
    submitEditor();
    await flushPromises();
    expect(updateConfig).toHaveBeenCalledTimes(1);
    const [req] = updateConfig.mock.calls[0];
    expect(req.expectedRevision).toBe(3n);
    expect(req.meta?.operationId).toBeTruthy();
    expect(wrapper.text()).toContain("409 Conflict");
    expect(updateConfig).toHaveBeenCalledTimes(1);
  });

  it("shows diff text after selecting two revisions", async () => {
    const { wrapper, configClient } = await mountPanel({
      updateConfig: vi.fn().mockResolvedValue({ spec: sampleSpec() }),
      revisions: [
        { revision: 1n, operator: "ada", timestampUnixMs: 1n, comment: "first" },
        { revision: 2n, operator: "bob", timestampUnixMs: 2n, comment: "second" },
      ],
      diff: "--- old\n+++ new",
    });
    const boxes = wrapper.findAll('input[type="checkbox"]');
    expect(boxes.length).toBe(2);
    await boxes[0].setValue(true);
    await boxes[1].setValue(true);
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(configClient.diff).toHaveBeenCalled();
    expect(wrapper.text()).not.toContain("Loading diff…");
    expect(wrapper.text()).toContain("--- old");
    expect(wrapper.text()).toContain("+++ new");
  });

  it("does not overwrite textarea or expected_revision on refetch while editing or after 409", async () => {
    const { wrapper, updateConfig, configClient, queryClient } = await mountPanel();
    await openEditor(wrapper);
    await switchEditorMode("json");
    const editedObject = JSON.parse(editorTextarea().value) as Record<string, unknown>;
    editedObject.name = "api";
    const edited = JSON.stringify(editedObject);
    setEditorText(edited);

    const newer = create(ProcessSpecSchema, {
      processId: "p1",
      name: "other",
      command: "sleep",
      latestRevision: 4n,
    });
    configClient.getConfig.mockResolvedValue({ spec: newer });
    await queryClient.invalidateQueries({ queryKey: ["process-config"] });
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(editorTextarea().value).toBe(edited);

    submitEditor();
    await flushPromises();
    expect(wrapper.text()).toContain("409 Conflict");

    await queryClient.invalidateQueries({ queryKey: ["process-config"] });
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(editorTextarea().value).toBe(edited);
    expect(wrapper.text()).toContain("409 Conflict");

    submitEditor();
    await flushPromises();
    expect(updateConfig).toHaveBeenCalledTimes(2);
    expect(updateConfig.mock.calls[0][0].expectedRevision).toBe(3n);
    expect(updateConfig.mock.calls[1][0].expectedRevision).toBe(3n);
  });

  it("resets remote ownership and revision when the process target changes", async () => {
    const updateConfig = vi.fn().mockResolvedValue({ spec: sampleSpec() });
    const { wrapper, configClient } = await mountPanel({ updateConfig });
    const nextSpec = create(ProcessSpecSchema, {
      processId: "p2",
      name: "api",
      ownerAgentId: "node-b",
      command: "/usr/bin/api",
      instances: 1,
      latestRevision: 9n,
    });
    configClient.getConfig.mockResolvedValue({ spec: nextSpec });

    await wrapper.setProps({ idOrName: "api", targetNodeId: "n2" });
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(wrapper.get(".config-json-viewer").text()).toContain('"processId": "p2"');
    expect(wrapper.get(".config-json-viewer").text()).toContain('"name": "api"');
    await openEditor(wrapper);
    submitEditor();
    await flushPromises();

    const request = updateConfig.mock.calls.at(-1)?.[0];
    expect(request.idOrName).toBe("api");
    expect(request.expectedRevision).toBe(9n);
    expect(request.spec.processId).toBe("p2");
    expect(request.spec.ownerAgentId).toBe("node-b");
  });

  it("keeps a late save success scoped to its originating target while the next target saves", async () => {
    const firstSave = deferred<{ spec: ReturnType<typeof sampleSpec> }>();
    const secondSave = deferred<{ spec: ReturnType<typeof sampleSpec> }>();
    const updateConfig = vi.fn()
      .mockReturnValueOnce(firstSave.promise)
      .mockReturnValueOnce(secondSave.promise);
    const { wrapper, configClient, queryClient } = await mountPanel({ updateConfig });
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");

    await openEditor(wrapper);
    await setDrawerField("name", "web-draft");
    submitEditor();
    await flushPromises();

    const targetBSpec = create(ProcessSpecSchema, {
      processId: "p2",
      name: "api",
      command: "/usr/bin/api",
      instances: 1,
      latestRevision: 9n,
    });
    configClient.getConfig.mockResolvedValue({ spec: targetBSpec });
    await wrapper.setProps({ idOrName: "api", targetNodeId: "n2" });
    await flushPromises();
    await openEditor(wrapper);
    const targetBDialog = Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"]')).find(
      (dialog) => dialog.querySelector<HTMLInputElement>('[data-field="name"]')?.value === "api",
    )!;
    const nameInput = targetBDialog.querySelector<HTMLInputElement>('[data-field="name"]')!;
    nameInput.value = "api-draft";
    nameInput.dispatchEvent(new Event("input", { bubbles: true }));
    const commentInput = targetBDialog.querySelector<HTMLInputElement>("#process-config-comment")!;
    commentInput.value = "target B comment";
    commentInput.dispatchEvent(new Event("input", { bubbles: true }));
    targetBDialog.querySelector<HTMLFormElement>("form.config-form")!.dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    );
    await flushPromises();
    expect(updateConfig).toHaveBeenCalledTimes(2);

    const targetASaved = create(ProcessSpecSchema, {
      ...sampleSpec(),
      name: "web-saved",
      latestRevision: 4n,
    });
    firstSave.resolve({ spec: targetASaved });
    await flushPromises();
    await wrapper.vm.$nextTick();

    const targetACache = queryClient.getQueryData<{ spec: ReturnType<typeof sampleSpec> }>([
      "process-config",
      "web",
      "n1",
    ]);
    const targetBCache = queryClient.getQueryData<{ spec: ReturnType<typeof sampleSpec> }>([
      "process-config",
      "api",
      "n2",
    ]);
    expect(targetACache?.spec.name).toBe("web-saved");
    expect(targetBCache?.spec.name).toBe("api");
    expect(wrapper.get(".config-json-viewer").text()).toContain('"name": "api"');
    expect(targetBDialog.isConnected).toBe(true);
    expect(targetBDialog.querySelector<HTMLInputElement>("#process-config-comment")?.value).toBe("target B comment");
    expect(targetBDialog.querySelector<HTMLButtonElement>('form.config-form button[type="submit"]')?.disabled).toBe(true);

    const firstRequestOptions = updateConfig.mock.calls[0][1] as { headers: Record<string, string> };
    expect(updateConfig.mock.calls[0][0].idOrName).toBe("web");
    expect(firstRequestOptions.headers["Procmesh-Target-Node"]).toBe("n1");
    const invalidatedKeys = invalidateQueries.mock.calls.map(([filters]) => filters.queryKey);
    expect(invalidatedKeys).toContainEqual(["process-history", "web", "n1"]);
    expect(invalidatedKeys).toContainEqual(["process", "web", "n1"]);
    expect(invalidatedKeys).not.toContainEqual(["process-history", "api", "n2"]);
    expect(invalidatedKeys).not.toContainEqual(["process", "api", "n2"]);

    secondSave.resolve({
      spec: create(ProcessSpecSchema, { ...targetBSpec, name: "api-saved", latestRevision: 10n }),
    });
    await flushPromises();
  });

  it.each([
    ["conflict", conflictError()],
    ["ordinary error", new ConnectError("rollback failed", Code.Internal)],
  ])("ignores a late rollback %s from the previous target", async (_label, lateError) => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const rollback = deferred<{ spec: ReturnType<typeof sampleSpec> }>();
    const { wrapper, configClient } = await mountPanel({
      updateConfig: vi.fn().mockResolvedValue({ spec: sampleSpec() }),
      revisions: [{ revision: 2n, operator: "admin", timestampUnixMs: 1n, comment: "before" }],
    });
    configClient.rollback.mockReturnValueOnce(rollback.promise);

    await wrapper.get("tbody button").trigger("click");
    await flushPromises();
    expect(configClient.rollback).toHaveBeenCalledTimes(1);

    const targetBSpec = create(ProcessSpecSchema, {
      processId: "p2",
      name: "api",
      command: "/usr/bin/api",
      instances: 1,
      latestRevision: 9n,
    });
    configClient.getConfig.mockResolvedValue({ spec: targetBSpec });
    await wrapper.setProps({ idOrName: "api", targetNodeId: "n2" });
    await flushPromises();

    rollback.reject(lateError);
    await flushPromises();
    await wrapper.vm.$nextTick();

    const requestOptions = configClient.rollback.mock.calls[0][1] as { headers: Record<string, string> };
    expect(configClient.rollback.mock.calls[0][0].idOrName).toBe("web");
    expect(requestOptions.headers["Procmesh-Target-Node"]).toBe("n1");
    expect(wrapper.get(".config-json-viewer").text()).toContain('"name": "api"');
    expect(wrapper.find(".banner.conflict").exists()).toBe(false);
    expect(wrapper.find('p.error[role="alert"]').exists()).toBe(false);
  });

  it("remount after save uses new latest as expected_revision", async () => {
    const saved = create(ProcessSpecSchema, {
      ...sampleSpec(),
      name: "api",
      latestRevision: 4n,
    });
    const updateConfig = vi.fn().mockResolvedValue({ spec: saved });
    const first = await mountPanel({ updateConfig });
    await openEditor(first.wrapper);
    await setDrawerField("name", "api");
    submitEditor();
    await flushPromises();
    expect(first.wrapper.get(".config-json-viewer").text()).toContain('"name": "api"');
    expect(first.wrapper.text()).toContain("Latest Revision4");

    first.wrapper.unmount();
    const idx = mounted.indexOf(first.wrapper);
    if (idx >= 0) {
      mounted.splice(idx, 1);
    }

    const second = await mountPanel({
      queryClient: first.queryClient,
      updateConfig,
      spec: sampleSpec(),
    });
    expect(second.wrapper.get(".config-json-viewer").text()).toContain('"name": "api"');
    expect(second.wrapper.text()).toContain("Latest Revision4");
    await openEditor(second.wrapper);
    submitEditor();
    await flushPromises();
    expect(updateConfig.mock.calls.at(-1)?.[0].expectedRevision).toBe(4n);
  });
});

describe("ProcessConfigPanel i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      processConfig: {
        loading: "Loading…",
        config: {
          title: "Config",
          reload: "Reload",
          processId: "Process ID",
          latestRevision: "Latest Revision",
          save: "Save",
        },
        history: {
          title: "History",
          loading: "Loading history…",
          table: {
            select: "Select",
            revision: "Revision",
            operator: "Operator",
            time: "Time",
            comment: "Comment",
            rollback: "Rollback",
          },
        },
      },
    });

    const { wrapper } = await mountPanel();
    const text = wrapper.text();
    expect(text).toContain("Config");
    expect(text).toContain("Reload");
    expect(text).toContain("Process ID");
    expect(text).toContain("Latest Revision");
    expect(text).toContain("History");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      processConfig: {
        loading: "加载中…",
        config: {
          title: "配置",
          reload: "重新加载",
          processId: "进程ID",
          latestRevision: "最新版本",
          save: "保存",
        },
        history: {
          title: "历史",
          loading: "加载历史中…",
          table: {
            select: "选择",
            revision: "版本",
            operator: "操作者",
            time: "时间",
            comment: "备注",
            rollback: "回滚",
          },
        },
      },
    });

    const { wrapper } = await mountPanel();
    const text = wrapper.text();
    expect(text).toContain("配置");
    expect(text).toContain("重新加载");
    expect(text).toContain("进程ID");
    expect(text).toContain("最新版本");
    expect(text).toContain("历史");
  });
});
