import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ProcessSpecSchema } from "../gen/procmesh/v1/api_pb";
import {
  parseProcessConfigJson,
  processConfigFormToSpec,
  specToProcessConfigForm,
  stringifyProcessConfigJson,
  validateProcessConfigForm,
  validateProcessSpec,
  type ProcessConfigFormState,
} from "./processConfigForm";
import { PROCESS_CONFIG_FIELDS, PROCESS_CONFIG_FIELD_PATHS } from "./processConfigSchema";

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

const expectedAllLeafPaths = [
  "processId",
  "name",
  "ownerAgentId",
  "group",
  "command",
  "args",
  "workingDirectory",
  "runAsUser",
  "environment",
  "instances",
  "autostart",
  "stopSignal",
  "killSignal",
  "stopTimeoutMs",
  "startupPriority",
  "restart.mode",
  "restart.maxRetries",
  "restart.retryWindowMs",
  "restart.backoff.initialMs",
  "restart.backoff.maxMs",
  "restart.backoff.multiplier",
  "health.type",
  "health.url",
  "health.method",
  "health.address",
  "health.command",
  "health.expectedStatus",
  "health.args",
  "health.initialDelayMs",
  "health.intervalMs",
  "health.timeoutMs",
  "health.failureThreshold",
  "health.successThreshold",
  "health.restartOnFailure",
  "health.restartCooldownMs",
  "log.maxSize",
  "log.maxFiles",
  "log.maxAgeSeconds",
  "log.compress",
  "resources.cpuQuotaMillis",
  "resources.memoryBytes",
  "resources.openFiles",
  "dependencies",
  "latestRevision",
] as const;

describe("ProcessSpec form conversion", () => {
  it("preserves every ProcessSpec leaf through a form round trip", () => {
    expect(processConfigFormToSpec(specToProcessConfigForm(fullSpec))).toEqual(fullSpec);
  });

  it("preserves every ProcessSpec leaf through protobuf JSON", () => {
    expect(parseProcessConfigJson(stringifyProcessConfigJson(fullSpec))).toEqual(fullSpec);
  });

  it("enumerates every ProcessSpec leaf, including read-only values", () => {
    expect(PROCESS_CONFIG_FIELD_PATHS).toEqual(expectedAllLeafPaths);
  });

  it("does not invent absent optional protobuf messages", () => {
    const minimal = create(ProcessSpecSchema, { name: "api", command: "/bin/api", instances: 1 });

    expect(processConfigFormToSpec(specToProcessConfigForm(minimal))).toEqual(minimal);
  });

  it.each([
    ["restart", (form: ProcessConfigFormState) => { form.restart.mode = "never"; }],
    ["restart.backoff", (form: ProcessConfigFormState) => { form.restart.backoff.initialMs = "250"; }],
    ["health", (form: ProcessConfigFormState) => { form.health.type = "alive"; }],
    ["log", (form: ProcessConfigFormState) => { form.log.compress = true; }],
    ["resources", (form: ProcessConfigFormState) => { form.resources.memoryBytes = "1024"; }],
  ])("creates an absent %s message when one of its fields is edited", (path, mutate) => {
    const minimal = create(ProcessSpecSchema, { name: "api", command: "/bin/api", instances: 1 });
    const form = specToProcessConfigForm(minimal);

    mutate(form);

    const converted = processConfigFormToSpec(form);
    if (path === "restart.backoff") {
      expect(converted.restart?.backoff?.initialMs).toBe(250n);
      return;
    }
    expect(converted[path as "restart" | "health" | "log" | "resources"]).toBeDefined();
  });

  it("keeps int64 input as decimal text without losing precision", () => {
    const spec = create(ProcessSpecSchema, {
      name: "api",
      command: "/bin/api",
      instances: 1,
      stopTimeoutMs: 9_223_372_036_854_775_807n,
    });

    const form = specToProcessConfigForm(spec);
    expect(form.stopTimeoutMs).toBe("9223372036854775807");
    expect(processConfigFormToSpec(form).stopTimeoutMs).toBe(9_223_372_036_854_775_807n);
  });
});

describe("ProcessSpec form validation", () => {
  it.each<[
    path: string,
    mutate: (form: ProcessConfigFormState) => void,
    code: string,
  ]>([
    ["name", (form) => { form.name = "1bad"; }, "invalidName"],
    ["group", (form) => { form.group = "bad group"; }, "invalidGroup"],
    ["command", (form) => { form.command = ""; }, "required"],
    ["instances", (form) => { form.instances = "0"; }, "minimumOne"],
    ["restart.retryWindowMs", (form) => { form.restart.maxRetries = "1"; form.restart.retryWindowMs = "0"; }, "retryWindowRequired"],
    ["restart.backoff.multiplier", (form) => { form.restart.backoff.multiplier = "0.5"; }, "multiplierMinimum"],
    ["health.url", (form) => { form.health.type = "http"; form.health.url = "file:///tmp/api"; }, "httpUrlRequired"],
    ["health.address", (form) => { form.health.type = "tcp"; form.health.address = ""; }, "tcpAddressRequired"],
    ["health.command", (form) => { form.health.type = "exec"; form.health.command = ""; }, "execCommandRequired"],
    ["environment", (form) => { form.environment.push({ key: "PORT", value: "9090" }); }, "duplicateEnvironmentKey"],
    ["dependencies", (form) => { form.dependencies.push({ processName: "db", condition: "STARTED" }); }, "duplicateDependency"],
    ["stopTimeoutMs", (form) => { form.stopTimeoutMs = "one-second"; }, "invalidInteger"],
    ["health.expectedStatus", (form) => { form.health.expectedStatus = "2147483648"; }, "int32OutOfRange"],
    ["stopTimeoutMs", (form) => { form.stopTimeoutMs = "9223372036854775808"; }, "int64OutOfRange"],
  ])("reports %s", (path, mutate, code) => {
    const form = specToProcessConfigForm(fullSpec);
    mutate(form);

    expect(validateProcessConfigForm(form)).toContainEqual(expect.objectContaining({ path, code }));
  });

  it("validates ProcessSpec through its form representation", () => {
    const invalidSpec = create(ProcessSpecSchema, { ...fullSpec, command: "" });

    expect(validateProcessSpec(invalidSpec)).toContainEqual({ path: "command", code: "required" });
  });

  it("validates an edited nested message even when it was absent in the source spec", () => {
    const minimal = create(ProcessSpecSchema, { name: "api", command: "/bin/api", instances: 1 });
    const form = specToProcessConfigForm(minimal);
    form.health.type = "http";
    form.health.url = "file:///tmp/api";

    expect(validateProcessConfigForm(form)).toContainEqual({ path: "health.url", code: "httpUrlRequired" });
  });

  it.each([
    ["restart.mode", (form: ProcessConfigFormState) => { form.restart.mode = "sometimes"; }],
    ["health.type", (form: ProcessConfigFormState) => { form.health.type = "icmp"; }],
    ["dependencies.0.condition", (form: ProcessConfigFormState) => { form.dependencies[0].condition = "READY"; }],
  ])("rejects an unsupported option for %s", (path, mutate) => {
    const form = specToProcessConfigForm(fullSpec);
    mutate(form);

    expect(validateProcessConfigForm(form)).toContainEqual({ path, code: "invalidOption" });
  });

  it.each([
    ["log.maxFiles", (form: ProcessConfigFormState) => { form.log.maxFiles = "-1"; }, "minimumZero"],
    ["restart.backoff.multiplier", (form: ProcessConfigFormState) => { form.restart.backoff.multiplier = "many"; }, "invalidDecimal"],
    ["dependencies.0.processName", (form: ProcessConfigFormState) => { form.dependencies[0].processName = ""; }, "required"],
  ])("covers validation branch %s", (path, mutate, code) => {
    const form = specToProcessConfigForm(fullSpec);
    mutate(form);

    expect(validateProcessConfigForm(form)).toContainEqual({ path, code });
  });
});

describe("ProcessSpec field schema", () => {
  it.each([
    ["http", ["health.url", "health.method", "health.expectedStatus"]],
    ["tcp", ["health.address"]],
    ["exec", ["health.command", "health.args"]],
  ])("shows the fields for the %s health type", (type, expectedPaths) => {
    const form = specToProcessConfigForm(fullSpec);
    form.health.type = type;
    const visiblePaths = PROCESS_CONFIG_FIELDS
      .filter((field) => !field.visible || field.visible(form))
      .map((field) => field.path);

    expect(visiblePaths).toEqual(expect.arrayContaining(expectedPaths));
  });
});

export { fullSpec };
