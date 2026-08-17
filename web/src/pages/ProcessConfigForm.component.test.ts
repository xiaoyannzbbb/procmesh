import { create } from "@bufbuild/protobuf";
import { mount, type VueWrapper } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { nextTick } from "vue";
import { beforeEach, describe, expect, it } from "vitest";
import enCommon from "../../public/locales/en/common.json";
import zhCommon from "../../public/locales/zh/common.json";
import { ProcessSpecSchema } from "../gen/procmesh/v1/api_pb";
import {
  processConfigFormToSpec,
  specToProcessConfigForm,
  type ProcessConfigFormState,
  type ProcessConfigIssue,
  type ProcessConfigIssueCode,
} from "./processConfigForm";
import ProcessConfigForm from "./ProcessConfigForm.vue";
import { PROCESS_CONFIG_FIELDS, PROCESS_CONFIG_SECTIONS } from "./processConfigSchema";

let i18n: typeof i18next;

const sectionLabels = {
  identity: "Identity",
  execution: "Execution",
  runtime: "Runtime",
  restart: "Restart policy",
  health: "Health check",
  logsResources: "Logs and resources",
  environment: "Environment",
  dependencies: "Dependencies",
};

const validationTranslationKeys: Record<ProcessConfigIssueCode, string> = {
  invalidName: "processConfig.editor.validation.invalidName",
  invalidGroup: "processConfig.editor.validation.invalidGroup",
  required: "processConfig.editor.validation.required",
  minimumOne: "processConfig.editor.validation.minimumOne",
  minimumZero: "processConfig.editor.validation.minimumZero",
  retryWindowRequired: "processConfig.editor.validation.retryWindowRequired",
  multiplierMinimum: "processConfig.editor.validation.multiplierMinimum",
  httpUrlRequired: "processConfig.editor.validation.httpUrlRequired",
  tcpAddressRequired: "processConfig.editor.validation.tcpAddressRequired",
  execCommandRequired: "processConfig.editor.validation.execCommandRequired",
  duplicateEnvironmentKey: "processConfig.editor.validation.duplicateEnvironmentKey",
  duplicateDependency: "processConfig.editor.validation.duplicateDependency",
  invalidInteger: "processConfig.editor.validation.invalidInteger",
  int32OutOfRange: "processConfig.editor.validation.int32OutOfRange",
  int64OutOfRange: "processConfig.editor.validation.int64OutOfRange",
  invalidDecimal: "processConfig.editor.validation.invalidDecimal",
  invalidOption: "processConfig.editor.validation.invalidOption",
};

function processConfigTranslationKeys(): string[] {
  const schemaKeys = PROCESS_CONFIG_FIELDS.flatMap((field) => [
    field.labelKey,
    field.helpKey,
    field.unitKey,
    ...(field.options?.map((option) => option.labelKey) ?? []),
  ]).filter((key): key is string => typeof key === "string");

  return [...new Set([
    ...PROCESS_CONFIG_SECTIONS.map((section) => section.labelKey),
    ...schemaKeys,
    "processConfig.editor.modeLabel",
    "processConfig.editor.mode.form",
    "processConfig.editor.mode.json",
    "processConfig.editor.add.argument",
    "processConfig.editor.add.healthArgument",
    "processConfig.editor.add.environment",
    "processConfig.editor.add.dependency",
    "processConfig.editor.remove.argument",
    "processConfig.editor.remove.healthArgument",
    "processConfig.editor.remove.environment",
    "processConfig.editor.remove.dependency",
    "processConfig.editor.environmentKey",
    "processConfig.editor.environmentValue",
    "processConfig.editor.dependencyProcess",
    "processConfig.editor.dependencyCondition",
    "processConfig.editor.errorSummary",
    ...Object.values(validationTranslationKeys),
  ])].sort();
}

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          processConfig: {
            editor: {
              section: sectionLabels,
              field: {
                processId: "Process ID",
                name: "Name",
                ownerAgentId: "Owner agent",
                group: "Group",
                command: "Command",
                args: "Arguments",
                workingDirectory: "Working directory",
                runAsUser: "Run as user",
                environment: "Environment",
                instances: "Instances",
                autostart: "Autostart",
                stopSignal: "Stop signal",
                killSignal: "Kill signal",
                stopTimeoutMs: "Stop timeout",
                startupPriority: "Startup priority",
                restartMode: "Restart mode",
                maxRetries: "Maximum retries",
                retryWindowMs: "Retry window",
                initialMs: "Initial backoff",
                maxMs: "Maximum backoff",
                multiplier: "Backoff multiplier",
                healthType: "Health type",
                url: "URL",
                method: "Method",
                address: "Address",
                healthCommand: "Health command",
                expectedStatus: "Expected status",
                healthArgs: "Health arguments",
                initialDelayMs: "Initial delay",
                intervalMs: "Interval",
                timeoutMs: "Timeout",
                failureThreshold: "Failure threshold",
                successThreshold: "Success threshold",
                restartOnFailure: "Restart on failure",
                restartCooldownMs: "Restart cooldown",
                maxSize: "Maximum log size",
                maxFiles: "Maximum log files",
                maxAgeSeconds: "Maximum log age",
                compress: "Compress logs",
                cpuQuotaMillis: "CPU quota",
                memoryBytes: "Memory",
                openFiles: "Open files",
                dependencies: "Dependencies",
                latestRevision: "Latest revision",
              },
              unit: { ms: "milliseconds", seconds: "seconds", bytes: "bytes", millis: "millis" },
              option: {
                restart: { never: "Never", always: "Always", "on-failure": "On failure" },
                health: { alive: "Alive", http: "HTTP", tcp: "TCP", exec: "Exec" },
                dependency: { STARTED: "Started", HEALTHY: "Healthy" },
              },
              add: {
                argument: "Add argument",
                healthArgument: "Add health argument",
                environment: "Add environment variable",
                dependency: "Add dependency",
              },
              remove: {
                argument: "Remove argument {{number}}",
                healthArgument: "Remove health argument {{number}}",
                environment: "Remove environment variable {{number}}",
                dependency: "Remove dependency {{number}}",
              },
              environmentKey: "Key",
              environmentValue: "Value",
              dependencyProcess: "Process name",
              dependencyCondition: "Condition",
              errorSummary: "Fix the following fields",
              validation: {
                invalidName: "Enter a valid process name",
                required: "This field is required",
                duplicateEnvironmentKey: "Environment keys must be unique",
              },
            },
          },
        },
      },
    },
  });
});

describe("ProcessConfigForm translations", () => {
  it("resolves every editor key in English and Simplified Chinese", async () => {
    const resourceI18n = i18next.createInstance();
    await resourceI18n.init({
      defaultNS: "common",
      fallbackLng: false,
      resources: {
        en: { common: enCommon },
        zh: { common: zhCommon },
      },
    });

    for (const language of ["en", "zh"] as const) {
      const translate = resourceI18n.getFixedT(language, "common") as unknown as (key: string) => string;
      for (const key of processConfigTranslationKeys()) {
        const translated = translate(key);
        expect(translated, `${language} is missing ${key}`).not.toBe(key);
        expect(translated.trim(), `${language} has an empty ${key}`).not.toBe("");
      }
    }
  });
});

function completeModel(): ProcessConfigFormState {
  return {
    processId: "p1",
    name: "api",
    ownerAgentId: "node-a",
    group: "services.core",
    command: "/usr/bin/api",
    args: ["serve", "--port=8080"],
    workingDirectory: "/srv/api",
    runAsUser: "svc-api",
    environment: [{ key: "PORT", value: "8080" }, { key: "MODE", value: "prod" }],
    instances: "2",
    autostart: true,
    stopSignal: "SIGTERM",
    killSignal: "SIGKILL",
    stopTimeoutMs: "10000",
    startupPriority: "20",
    restart: {
      mode: "on-failure",
      maxRetries: "5",
      retryWindowMs: "60000",
      backoff: { initialMs: "500", maxMs: "30000", multiplier: "2" },
    },
    health: {
      type: "http",
      url: "http://127.0.0.1:8080/health",
      method: "GET",
      address: "127.0.0.1:8080",
      command: "/usr/bin/check-api",
      expectedStatus: "204",
      args: ["--quiet"],
      initialDelayMs: "1000",
      intervalMs: "5000",
      timeoutMs: "1000",
      failureThreshold: "3",
      successThreshold: "2",
      restartOnFailure: true,
      restartCooldownMs: "30000",
    },
    log: { maxSize: "104857600", maxFiles: "10", maxAgeSeconds: "604800", compress: true },
    resources: { cpuQuotaMillis: "500", memoryBytes: "536870912", openFiles: "4096" },
    dependencies: [{ processName: "db", condition: "HEALTHY" }],
    latestRevision: "7",
    hasRestart: true,
    hasRestartBackoff: true,
    hasHealth: true,
    hasLog: true,
    hasResources: true,
  };
}

function mountForm(
  modelValue = completeModel(),
  options: { issues?: ProcessConfigIssue[]; validateRequested?: number; attachTo?: HTMLElement } = {},
): VueWrapper {
  return mount(ProcessConfigForm, {
    props: {
      modelValue,
      issues: options.issues ?? [],
      validateRequested: options.validateRequested ?? 0,
    },
    attachTo: options.attachTo,
    global: { plugins: [[I18NextVue, { i18next: i18n }]] },
  });
}

async function acceptLastUpdate(wrapper: VueWrapper): Promise<ProcessConfigFormState> {
  const emitted = wrapper.emitted<ProcessConfigFormState[]>("update:modelValue");
  const next = emitted?.at(-1)?.[0];
  expect(next).toBeDefined();
  await wrapper.setProps({ modelValue: next });
  return next!;
}

async function changeHealthType(wrapper: VueWrapper, type: string): Promise<ProcessConfigFormState> {
  await wrapper.get('[data-field="health.type"]').setValue(type);
  return acceptLastUpdate(wrapper);
}

describe("ProcessConfigForm schema rendering", () => {
  it("renders every schema section and generated scalar control with native semantics", () => {
    const model = completeModel();
    const wrapper = mountForm(model);
    const collectionPaths = new Set(["args", "health.args", "environment", "dependencies"]);
    const visibleScalarPaths = PROCESS_CONFIG_FIELDS
      .filter((field) => !collectionPaths.has(field.path) && (!field.visible || field.visible(model)))
      .map((field) => field.path);

    expect(wrapper.findAll("fieldset")).toHaveLength(8);
    expect(wrapper.findAll("legend").map((legend) => legend.text())).toEqual(Object.values(sectionLabels));
    expect(visibleScalarPaths.every((path) => wrapper.find(`[data-field="${path}"]`).exists())).toBe(true);
    expect(wrapper.get('[data-field="name"]').attributes("aria-required")).toBe("true");
    expect(wrapper.get('[data-field="command"]').element.tagName).toBe("INPUT");
    expect(wrapper.get('[data-field="restart.mode"]').element.tagName).toBe("SELECT");
    expect(wrapper.get('[data-field="autostart"]').attributes("type")).toBe("checkbox");
    expect(wrapper.get('label[for="process-config-name"]').text()).toBe("Name");
  });

  it("shows explicit API units and keeps server-owned fields read-only", () => {
    const wrapper = mountForm();

    expect(wrapper.get('[data-control="stopTimeoutMs"]').text()).toContain("milliseconds");
    expect(wrapper.get('[data-control="log.maxAgeSeconds"]').text()).toContain("seconds");
    expect(wrapper.get('[data-control="resources.memoryBytes"]').text()).toContain("bytes");
    expect(wrapper.get('[data-field="processId"]').attributes("readonly")).toBeDefined();
    expect(wrapper.get('[data-field="latestRevision"]').attributes("readonly")).toBeDefined();
  });

  it("renders only health controls for the selected type and preserves hidden values", async () => {
    const wrapper = mountForm();

    expect(wrapper.find('[data-field="health.url"]').exists()).toBe(true);
    expect(wrapper.find('[data-field="health.method"]').exists()).toBe(true);
    expect(wrapper.find('[data-field="health.expectedStatus"]').exists()).toBe(true);
    expect(wrapper.find('[data-field="health.address"]').exists()).toBe(false);
    expect(wrapper.find('[data-field="health.command"]').exists()).toBe(false);

    let next = await changeHealthType(wrapper, "tcp");
    expect(wrapper.find('[data-field="health.address"]').exists()).toBe(true);
    expect(wrapper.find('[data-field="health.url"]').exists()).toBe(false);
    expect(next.health.url).toBe("http://127.0.0.1:8080/health");
    expect(next.health.command).toBe("/usr/bin/check-api");

    next = await changeHealthType(wrapper, "exec");
    expect(wrapper.find('[data-field="health.command"]').exists()).toBe(true);
    expect(wrapper.find('[data-collection="health.args"]').exists()).toBe(true);
    expect(wrapper.find('[data-field="health.address"]').exists()).toBe(false);
    expect(next.health.address).toBe("127.0.0.1:8080");

    next = await changeHealthType(wrapper, "alive");
    expect(wrapper.find('[data-field="health.url"]').exists()).toBe(false);
    expect(wrapper.find('[data-field="health.address"]').exists()).toBe(false);
    expect(wrapper.find('[data-field="health.command"]').exists()).toBe(false);
    expect(wrapper.find('[data-collection="health.args"]').exists()).toBe(false);
    expect(next.health.args).toEqual(["--quiet"]);
  });

  it("retains nested edits from absent messages and creates them during conversion", async () => {
    const minimal = create(ProcessSpecSchema, { name: "api", command: "/bin/api", instances: 1 });
    const model = specToProcessConfigForm(minimal);
    const wrapper = mountForm(model);

    expect({
      restart: model.hasRestart,
      backoff: model.hasRestartBackoff,
      health: model.hasHealth,
      log: model.hasLog,
      resources: model.hasResources,
    }).toEqual({ restart: false, backoff: false, health: false, log: false, resources: false });

    await wrapper.get('[data-field="restart.mode"]').setValue("always");
    await acceptLastUpdate(wrapper);
    await wrapper.get('[data-field="restart.backoff.initialMs"]').setValue("250");
    await acceptLastUpdate(wrapper);
    await wrapper.get('[data-field="health.type"]').setValue("alive");
    await acceptLastUpdate(wrapper);
    await wrapper.get('[data-field="health.intervalMs"]').setValue("5000");
    await acceptLastUpdate(wrapper);
    await wrapper.get('[data-field="log.maxSize"]').setValue("1048576");
    await acceptLastUpdate(wrapper);
    await wrapper.get('[data-field="resources.memoryBytes"]').setValue("536870912");
    const next = await acceptLastUpdate(wrapper);

    expect(next.restart.mode).toBe("always");
    expect(next.restart.backoff.initialMs).toBe("250");
    expect(next.health.type).toBe("alive");
    expect(next.health.intervalMs).toBe("5000");
    expect(next.log.maxSize).toBe("1048576");
    expect(next.resources.memoryBytes).toBe("536870912");
    expect({
      restart: next.hasRestart,
      backoff: next.hasRestartBackoff,
      health: next.hasHealth,
      log: next.hasLog,
      resources: next.hasResources,
    }).toEqual({ restart: false, backoff: false, health: false, log: false, resources: false });

    const converted = processConfigFormToSpec(next);
    expect(converted.restart?.mode).toBe("always");
    expect(converted.restart?.backoff?.initialMs).toBe(250n);
    expect(converted.health?.type).toBe("alive");
    expect(converted.health?.intervalMs).toBe(5000n);
    expect(converted.log?.maxSize).toBe(1048576n);
    expect(converted.resources?.memoryBytes).toBe(536870912n);
  });
});

describe("ProcessConfigForm collections", () => {
  it("edits process and health arguments in order without mutating the input model", async () => {
    const original = completeModel();
    original.health.type = "exec";
    const wrapper = mountForm(original);

    expect(wrapper.findAll('[data-row="args"]')).toHaveLength(2);
    await wrapper.findAll('[data-field^="args."]')[1].setValue("--port=9090");
    let next = await acceptLastUpdate(wrapper);
    expect(next.args).toEqual(["serve", "--port=9090"]);
    expect(original.args).toEqual(["serve", "--port=8080"]);

    await wrapper.get('[data-action="add-argument"]').trigger("click");
    next = await acceptLastUpdate(wrapper);
    expect(next.args).toEqual(["serve", "--port=9090", ""]);
    await wrapper.findAll('[data-action="remove-argument"]')[0].trigger("click");
    next = await acceptLastUpdate(wrapper);
    expect(next.args).toEqual(["--port=9090", ""]);

    expect(wrapper.findAll('[data-row="health-args"]')).toHaveLength(1);
    await wrapper.get('[data-action="add-health-argument"]').trigger("click");
    next = await acceptLastUpdate(wrapper);
    expect(next.health.args).toEqual(["--quiet", ""]);
    await wrapper.findAll('[data-field^="health.args."]')[1].setValue("--verbose");
    next = await acceptLastUpdate(wrapper);
    expect(next.health.args).toEqual(["--quiet", "--verbose"]);
    await wrapper.findAll('[data-action="remove-health-argument"]')[0].trigger("click");
    next = await acceptLastUpdate(wrapper);
    expect(next.health.args).toEqual(["--verbose"]);
  });

  it("edits environment and dependency rows while preserving row order and values", async () => {
    const original = completeModel();
    const wrapper = mountForm(original);

    await wrapper.get('[data-action="add-environment"]').trigger("click");
    let next = await acceptLastUpdate(wrapper);
    expect(next.environment).toEqual([
      { key: "PORT", value: "8080" },
      { key: "MODE", value: "prod" },
      { key: "", value: "" },
    ]);
    await wrapper.findAll('[data-field$=".key"]')[2].setValue("DEBUG");
    await acceptLastUpdate(wrapper);
    await wrapper.findAll('[data-field$=".value"]')[2].setValue("true");
    next = await acceptLastUpdate(wrapper);
    expect(next.environment[2]).toEqual({ key: "DEBUG", value: "true" });
    await wrapper.findAll('[data-action="remove-environment"]')[0].trigger("click");
    next = await acceptLastUpdate(wrapper);
    expect(next.environment).toEqual([
      { key: "MODE", value: "prod" },
      { key: "DEBUG", value: "true" },
    ]);
    expect(original.environment).toHaveLength(2);

    await wrapper.get('[data-action="add-dependency"]').trigger("click");
    next = await acceptLastUpdate(wrapper);
    expect(next.dependencies).toEqual([
      { processName: "db", condition: "HEALTHY" },
      { processName: "", condition: "STARTED" },
    ]);
    await wrapper.findAll('[data-field$=".processName"]')[1].setValue("cache");
    next = await acceptLastUpdate(wrapper);
    expect(next.dependencies[1]).toEqual({ processName: "cache", condition: "STARTED" });
    await wrapper.findAll('[data-field$=".condition"]')[1].setValue("HEALTHY");
    next = await acceptLastUpdate(wrapper);
    expect(next.dependencies[1]).toEqual({ processName: "cache", condition: "HEALTHY" });
    await wrapper.findAll('[data-action="remove-dependency"]')[0].trigger("click");
    next = await acceptLastUpdate(wrapper);
    expect(next.dependencies).toEqual([{ processName: "cache", condition: "HEALTHY" }]);
  });

  it("gives icon-only removal buttons accessible names and hides their icons", () => {
    const model = completeModel();
    model.health.type = "exec";
    const wrapper = mountForm(model);

    const removals = [
      wrapper.findAll('[data-action="remove-argument"]')[0],
      wrapper.findAll('[data-action="remove-health-argument"]')[0],
      wrapper.findAll('[data-action="remove-environment"]')[0],
      wrapper.findAll('[data-action="remove-dependency"]')[0],
    ];
    expect(removals.map((button) => button.attributes("aria-label"))).toEqual([
      "Remove argument 1",
      "Remove health argument 1",
      "Remove environment variable 1",
      "Remove dependency 1",
    ]);
    for (const button of removals) {
      expect(button.get("svg").attributes("aria-hidden")).toBe("true");
    }
    expect(wrapper.get('[data-action="add-environment"]').text()).toBe("Add environment variable");
  });
});

describe("ProcessConfigForm errors and focus", () => {
  it("labels same-code summary links with distinct row targets without changing inline errors", async () => {
    const model = completeModel();
    model.dependencies.push({ processName: "cache", condition: "STARTED" });
    const issues: ProcessConfigIssue[] = [
      { path: "dependencies.0.processName", code: "required" },
      { path: "dependencies.1.processName", code: "required" },
    ];
    const wrapper = mountForm(model, { issues, validateRequested: 1 });

    expect(wrapper.findAll('[data-error^="dependencies."]').map((error) => error.text())).toEqual([
      "This field is required",
      "This field is required",
    ]);
    const links = wrapper.get('[data-error-summary]').findAll("a");
    expect(links.map((link) => link.attributes("href"))).toEqual([
      "#process-config-dependencies-0-processName",
      "#process-config-dependencies-1-processName",
    ]);
    expect(links.map((link) => link.text())).toEqual([
      "Process name 1: This field is required",
      "Process name 2: This field is required",
    ]);
  });

  it("keeps inline errors and a linked summary without overriding parent-managed issue focus", async () => {
    const host = document.createElement("div");
    document.body.append(host);
    const issues: ProcessConfigIssue[] = [
      { path: "name", code: "invalidName" },
      { path: "environment", code: "duplicateEnvironmentKey" },
      { path: "dependencies.0.processName", code: "required" },
    ];
    const wrapper = mountForm(completeModel(), { issues, attachTo: host });
    const command = wrapper.get<HTMLInputElement>('[data-field="command"]');
    command.element.focus();

    expect(wrapper.find('[data-error-summary]').exists()).toBe(false);
    const name = wrapper.get('[data-field="name"]');
    expect(name.attributes("aria-invalid")).toBe("true");
    expect(name.attributes("aria-describedby")).toBe("process-config-name-error");
    expect(wrapper.get("#process-config-name-error").text()).toBe("Enter a valid process name");
    const environment = wrapper.get('[data-collection="environment"]');
    expect(environment.attributes("aria-invalid")).toBe("true");
    expect(environment.attributes("aria-describedby")).toBe("process-config-environment-error");
    const dependency = wrapper.get('[data-field="dependencies.0.processName"]');
    expect(dependency.attributes("aria-invalid")).toBe("true");
    expect(dependency.attributes("aria-describedby")).toBe("process-config-dependencies-0-processName-error");
    expect(document.activeElement).toBe(command.element);

    await wrapper.setProps({ validateRequested: 1 });
    const summary = wrapper.get<HTMLElement>('[data-error-summary]');
    expect(summary.attributes("tabindex")).toBe("-1");
    expect(summary.attributes("role")).toBe("alert");
    expect(summary.findAll("a").map((link) => link.attributes("href"))).toEqual([
      "#process-config-name",
      "#process-config-environment",
      "#process-config-dependencies-0-processName",
    ]);
    await nextTick();
    expect(document.activeElement).toBe(command.element);

    wrapper.unmount();
    host.remove();
  });

  it("exposes focusIssue and focuses stable scalar and collection-row targets after rendering", async () => {
    const host = document.createElement("div");
    document.body.append(host);
    const wrapper = mountForm(completeModel(), { attachTo: host });
    const exposed = wrapper.vm as unknown as { focusIssue: (path: string) => Promise<void> };

    await exposed.focusIssue("name");
    expect(document.activeElement).toBe(wrapper.get('[data-field="name"]').element);
    await exposed.focusIssue("dependencies.0.processName");
    expect(document.activeElement).toBe(wrapper.get('[data-field="dependencies.0.processName"]').element);

    wrapper.unmount();
    host.remove();
  });
});
