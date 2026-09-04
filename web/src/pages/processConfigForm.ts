import {
  create,
  fromJson,
  ScalarType,
  toJson,
  type DescMessage,
  type JsonValue,
} from "@bufbuild/protobuf";
import { parse as parseYaml, stringify as stringifyYaml } from "yaml";
import {
  BackoffSchema,
  HealthCheckSchema,
  LogPolicySchema,
  ProcessSpecSchema,
  ResourceLimitSchema,
  RestartPolicySchema,
  type ProcessSpec,
} from "../gen/procmesh/v1/process_types_pb";

export type ProcessConfigEnvironmentEntry = { key: string; value: string };
export type ProcessConfigDependency = { processName: string; condition: string };

export type ProcessConfigFormState = {
  processId: string;
  name: string;
  ownerAgentId: string;
  group: string;
  command: string;
  args: string[];
  workingDirectory: string;
  runAsUser: string;
  environment: ProcessConfigEnvironmentEntry[];
  instances: string;
  autostart: boolean;
  stopSignal: string;
  killSignal: string;
  stopTimeoutMs: string;
  startupPriority: string;
  restart: {
    mode: string;
    maxRetries: string;
    retryWindowMs: string;
    backoff: { initialMs: string; maxMs: string; multiplier: string };
  };
  health: {
    type: string;
    url: string;
    method: string;
    address: string;
    command: string;
    expectedStatus: string;
    args: string[];
    initialDelayMs: string;
    intervalMs: string;
    timeoutMs: string;
    failureThreshold: string;
    successThreshold: string;
    restartOnFailure: boolean;
    restartCooldownMs: string;
  };
  log: {
    directory: string;
    redirectStderr: boolean;
    maxSize: string;
    maxFiles: string;
    maxAgeSeconds: string;
    compress: boolean;
  };
  resources: { cpuQuotaMillis: string; memoryBytes: string; openFiles: string };
  dependencies: ProcessConfigDependency[];
  latestRevision: string;
  hasRestart: boolean;
  hasRestartBackoff: boolean;
  hasHealth: boolean;
  hasLog: boolean;
  hasResources: boolean;
};

export type ProcessConfigIssueCode =
  | "invalidName"
  | "invalidGroup"
  | "required"
  | "minimumOne"
  | "minimumZero"
  | "retryWindowRequired"
  | "multiplierMinimum"
  | "httpUrlRequired"
  | "tcpAddressRequired"
  | "execCommandRequired"
  | "duplicateEnvironmentKey"
  | "duplicateDependency"
  | "invalidInteger"
  | "int32OutOfRange"
  | "int64OutOfRange"
  | "invalidDecimal"
  | "invalidOption"
  | "invalidLogDirectory";

export type ProcessConfigIssue = {
  path: string;
  code: ProcessConfigIssueCode;
};

const INT32_MIN = -2_147_483_648n;
const INT32_MAX = 2_147_483_647n;
const INT64_MIN = -9_223_372_036_854_775_808n;
const INT64_MAX = 9_223_372_036_854_775_807n;
const INTEGER_TEXT = /^-?\d+$/;
const DECIMAL_TEXT = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/;
const RESTART_MODES = new Set(["", "never", "always", "on-failure"]);
const HEALTH_TYPES = new Set(["", "alive", "http", "tcp", "exec"]);
const DEPENDENCY_CONDITIONS = new Set(["STARTED", "HEALTHY"]);

function decimal(value: bigint | number): string {
  return String(value);
}

function parseInteger(value: string, path: string, min: bigint, max: bigint): bigint {
  if (!INTEGER_TEXT.test(value)) {
    throw new Error(`${path} must be a base-10 integer`);
  }
  const parsed = BigInt(value);
  if (parsed < min || parsed > max) {
    throw new Error(`${path} is outside its protobuf integer range`);
  }
  return parsed;
}

function parseInt32(value: string, path: string): number {
  return Number(parseInteger(value, path, INT32_MIN, INT32_MAX));
}

function parseInt64(value: string, path: string): bigint {
  return parseInteger(value, path, INT64_MIN, INT64_MAX);
}

function parseDecimal(value: string, path: string): number {
  if (!DECIMAL_TEXT.test(value)) {
    throw new Error(`${path} must be a decimal number`);
  }
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    throw new Error(`${path} must be finite`);
  }
  return parsed;
}

export function emptyProcessConfigForm(): ProcessConfigFormState {
  // Create-only defaults must stay aligned with backend and runtime defaults.
  return specToProcessConfigForm(create(ProcessSpecSchema, {
    instances: 1,
    stopSignal: "SIGTERM",
    killSignal: "SIGKILL",
    stopTimeoutMs: 10_000n,
    restart: {
      mode: "on-failure",
      backoff: {
        initialMs: 1_000n,
        maxMs: 60_000n,
        multiplier: 2,
      },
    },
    health: {
      type: "alive",
      method: "GET",
      expectedStatus: 200,
      timeoutMs: 1_000n,
      failureThreshold: 1,
      successThreshold: 1,
    },
    log: {
      maxSize: 104_857_600n,
      maxFiles: 10,
      maxAgeSeconds: 604_800n,
      compress: true,
    },
  }));
}

export function specToProcessConfigForm(spec: ProcessSpec): ProcessConfigFormState {
  const restart = spec.restart;
  const health = spec.health;
  const log = spec.log;
  const resources = spec.resources;
  const backoff = restart?.backoff;

  return {
    processId: spec.processId,
    name: spec.name,
    ownerAgentId: spec.ownerAgentId,
    group: spec.group,
    command: spec.command,
    args: [...spec.args],
    workingDirectory: spec.workingDirectory,
    runAsUser: spec.runAsUser,
    environment: Object.entries(spec.environment).map(([key, value]) => ({ key, value })),
    instances: decimal(spec.instances),
    autostart: spec.autostart,
    stopSignal: spec.stopSignal,
    killSignal: spec.killSignal,
    stopTimeoutMs: decimal(spec.stopTimeoutMs),
    startupPriority: decimal(spec.startupPriority),
    restart: {
      mode: restart?.mode ?? "",
      maxRetries: decimal(restart?.maxRetries ?? 0),
      retryWindowMs: decimal(restart?.retryWindowMs ?? 0n),
      backoff: {
        initialMs: decimal(backoff?.initialMs ?? 0n),
        maxMs: decimal(backoff?.maxMs ?? 0n),
        multiplier: decimal(backoff?.multiplier ?? 0),
      },
    },
    health: {
      type: health?.type ?? "",
      url: health?.url ?? "",
      method: health?.method ?? "",
      address: health?.address ?? "",
      command: health?.command ?? "",
      expectedStatus: decimal(health?.expectedStatus ?? 0),
      args: [...(health?.args ?? [])],
      initialDelayMs: decimal(health?.initialDelayMs ?? 0n),
      intervalMs: decimal(health?.intervalMs ?? 0n),
      timeoutMs: decimal(health?.timeoutMs ?? 0n),
      failureThreshold: decimal(health?.failureThreshold ?? 0),
      successThreshold: decimal(health?.successThreshold ?? 0),
      restartOnFailure: health?.restartOnFailure ?? false,
      restartCooldownMs: decimal(health?.restartCooldownMs ?? 0n),
    },
    log: {
      directory: log?.directory ?? "",
      redirectStderr: log?.redirectStderr ?? false,
      maxSize: decimal(log?.maxSize ?? 0n),
      maxFiles: decimal(log?.maxFiles ?? 0),
      maxAgeSeconds: decimal(log?.maxAgeSeconds ?? 0n),
      compress: log?.compress ?? false,
    },
    resources: {
      cpuQuotaMillis: decimal(resources?.cpuQuotaMillis ?? 0n),
      memoryBytes: decimal(resources?.memoryBytes ?? 0n),
      openFiles: decimal(resources?.openFiles ?? 0n),
    },
    dependencies: spec.dependencies.map(({ processName, condition }) => ({ processName, condition })),
    latestRevision: decimal(spec.latestRevision),
    hasRestart: restart !== undefined,
    hasRestartBackoff: backoff !== undefined,
    hasHealth: health !== undefined,
    hasLog: log !== undefined,
    hasResources: resources !== undefined,
  };
}

function hasEditedNumber(value: string): boolean {
  return value !== "0";
}

function shouldIncludeRestartBackoff(form: ProcessConfigFormState): boolean {
  return form.hasRestartBackoff
    || hasEditedNumber(form.restart.backoff.initialMs)
    || hasEditedNumber(form.restart.backoff.maxMs)
    || hasEditedNumber(form.restart.backoff.multiplier);
}

function shouldIncludeRestart(form: ProcessConfigFormState): boolean {
  return form.hasRestart
    || form.restart.mode !== ""
    || hasEditedNumber(form.restart.maxRetries)
    || hasEditedNumber(form.restart.retryWindowMs)
    || shouldIncludeRestartBackoff(form);
}

function shouldIncludeHealth(form: ProcessConfigFormState): boolean {
  const health = form.health;
  return form.hasHealth
    || health.type !== ""
    || health.url !== ""
    || health.method !== ""
    || health.address !== ""
    || health.command !== ""
    || health.args.length > 0
    || hasEditedNumber(health.expectedStatus)
    || hasEditedNumber(health.initialDelayMs)
    || hasEditedNumber(health.intervalMs)
    || hasEditedNumber(health.timeoutMs)
    || hasEditedNumber(health.failureThreshold)
    || hasEditedNumber(health.successThreshold)
    || health.restartOnFailure
    || hasEditedNumber(health.restartCooldownMs);
}

function shouldIncludeLog(form: ProcessConfigFormState): boolean {
  return form.hasLog
    || form.log.directory.trim() !== ""
    || form.log.redirectStderr
    || hasEditedNumber(form.log.maxSize)
    || hasEditedNumber(form.log.maxFiles)
    || hasEditedNumber(form.log.maxAgeSeconds)
    || form.log.compress;
}

function shouldIncludeResources(form: ProcessConfigFormState): boolean {
  return form.hasResources
    || hasEditedNumber(form.resources.cpuQuotaMillis)
    || hasEditedNumber(form.resources.memoryBytes)
    || hasEditedNumber(form.resources.openFiles);
}

export function processConfigFormToSpec(form: ProcessConfigFormState): ProcessSpec {
  const includeRestartBackoff = shouldIncludeRestartBackoff(form);
  const restart = shouldIncludeRestart(form)
    ? create(RestartPolicySchema, {
        mode: form.restart.mode,
        maxRetries: parseInt32(form.restart.maxRetries, "restart.maxRetries"),
        retryWindowMs: parseInt64(form.restart.retryWindowMs, "restart.retryWindowMs"),
        backoff: includeRestartBackoff
          ? create(BackoffSchema, {
              initialMs: parseInt64(form.restart.backoff.initialMs, "restart.backoff.initialMs"),
              maxMs: parseInt64(form.restart.backoff.maxMs, "restart.backoff.maxMs"),
              multiplier: parseDecimal(form.restart.backoff.multiplier, "restart.backoff.multiplier"),
            })
          : undefined,
      })
    : undefined;
  const health = shouldIncludeHealth(form)
    ? create(HealthCheckSchema, {
        type: form.health.type,
        url: form.health.url,
        method: form.health.method,
        address: form.health.address,
        command: form.health.command,
        expectedStatus: parseInt32(form.health.expectedStatus, "health.expectedStatus"),
        args: [...form.health.args],
        initialDelayMs: parseInt64(form.health.initialDelayMs, "health.initialDelayMs"),
        intervalMs: parseInt64(form.health.intervalMs, "health.intervalMs"),
        timeoutMs: parseInt64(form.health.timeoutMs, "health.timeoutMs"),
        failureThreshold: parseInt32(form.health.failureThreshold, "health.failureThreshold"),
        successThreshold: parseInt32(form.health.successThreshold, "health.successThreshold"),
        restartOnFailure: form.health.restartOnFailure,
        restartCooldownMs: parseInt64(form.health.restartCooldownMs, "health.restartCooldownMs"),
      })
    : undefined;
  const log = shouldIncludeLog(form)
    ? create(LogPolicySchema, {
        directory: form.log.directory,
        redirectStderr: form.log.redirectStderr,
        maxSize: parseInt64(form.log.maxSize, "log.maxSize"),
        maxFiles: parseInt32(form.log.maxFiles, "log.maxFiles"),
        maxAgeSeconds: parseInt64(form.log.maxAgeSeconds, "log.maxAgeSeconds"),
        compress: form.log.compress,
      })
    : undefined;
  const resources = shouldIncludeResources(form)
    ? create(ResourceLimitSchema, {
        cpuQuotaMillis: parseInt64(form.resources.cpuQuotaMillis, "resources.cpuQuotaMillis"),
        memoryBytes: parseInt64(form.resources.memoryBytes, "resources.memoryBytes"),
        openFiles: parseInt64(form.resources.openFiles, "resources.openFiles"),
      })
    : undefined;

  return create(ProcessSpecSchema, {
    processId: form.processId,
    name: form.name,
    ownerAgentId: form.ownerAgentId,
    group: form.group,
    command: form.command,
    args: [...form.args],
    workingDirectory: form.workingDirectory,
    runAsUser: form.runAsUser,
    environment: Object.fromEntries(form.environment.map(({ key, value }) => [key, value])),
    instances: parseInt32(form.instances, "instances"),
    autostart: form.autostart,
    stopSignal: form.stopSignal,
    killSignal: form.killSignal,
    stopTimeoutMs: parseInt64(form.stopTimeoutMs, "stopTimeoutMs"),
    startupPriority: parseInt32(form.startupPriority, "startupPriority"),
    restart,
    health,
    log,
    resources,
    dependencies: form.dependencies.map(({ processName, condition }) => ({ processName, condition })),
    latestRevision: parseInt64(form.latestRevision, "latestRevision"),
  });
}

const PROTOBUF_64_BIT_INTEGER_TYPES = new Set<ScalarType>([
  ScalarType.INT64,
  ScalarType.UINT64,
  ScalarType.FIXED64,
  ScalarType.SFIXED64,
  ScalarType.SINT64,
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function protobufJsonToYamlValue(schema: DescMessage, value: Record<string, unknown>): Record<string, unknown> {
  const result = { ...value };
  for (const field of schema.fields) {
    const fieldValue = value[field.name];
    if (fieldValue === undefined || fieldValue === null) {
      continue;
    }
    if (field.fieldKind === "scalar" && PROTOBUF_64_BIT_INTEGER_TYPES.has(field.scalar)) {
      result[field.name] = BigInt(fieldValue as string);
      continue;
    }
    if (field.fieldKind === "message" && isRecord(fieldValue)) {
      result[field.name] = protobufJsonToYamlValue(field.message, fieldValue);
      continue;
    }
    if (field.fieldKind === "list" && Array.isArray(fieldValue)) {
      if (field.listKind === "scalar" && PROTOBUF_64_BIT_INTEGER_TYPES.has(field.scalar)) {
        result[field.name] = fieldValue.map((entry) => BigInt(entry as string));
      } else if (field.listKind === "message") {
        result[field.name] = fieldValue.map((entry) => (
          isRecord(entry) ? protobufJsonToYamlValue(field.message, entry) : entry
        ));
      }
      continue;
    }
    if (field.fieldKind === "map" && isRecord(fieldValue)) {
      if (field.mapKind === "scalar" && PROTOBUF_64_BIT_INTEGER_TYPES.has(field.scalar)) {
        result[field.name] = Object.fromEntries(
          Object.entries(fieldValue).map(([key, entry]) => [key, BigInt(entry as string)]),
        );
      } else if (field.mapKind === "message") {
        result[field.name] = Object.fromEntries(
          Object.entries(fieldValue).map(([key, entry]) => [
            key,
            isRecord(entry) ? protobufJsonToYamlValue(field.message, entry) : entry,
          ]),
        );
      }
    }
  }
  return result;
}

function yamlIntegersToProtobufJson(value: unknown): JsonValue {
  if (typeof value === "bigint") {
    return value.toString();
  }
  if (Array.isArray(value)) {
    return value.map(yamlIntegersToProtobufJson);
  }
  if (isRecord(value)) {
    return Object.fromEntries(
      Object.entries(value).map(([key, entry]) => [key, yamlIntegersToProtobufJson(entry)]),
    );
  }
  return value as JsonValue;
}

export function processConfigToYamlValue(spec: ProcessSpec): Record<string, unknown> {
  const protobufJson = toJson(ProcessSpecSchema, spec, { useProtoFieldName: true }) as Record<string, unknown>;
  return protobufJsonToYamlValue(ProcessSpecSchema, protobufJson);
}

export function stringifyProcessConfigYaml(spec: ProcessSpec): string {
  return stringifyYaml(processConfigToYamlValue(spec), {
    aliasDuplicateObjects: false,
    lineWidth: 0,
  });
}

function yamlInteger(value: string): number | bigint {
  if (!INTEGER_TEXT.test(value)) {
    return 0;
  }
  const parsed = BigInt(value);
  if (parsed >= BigInt(Number.MIN_SAFE_INTEGER) && parsed <= BigInt(Number.MAX_SAFE_INTEGER)) {
    return Number(parsed);
  }
  return parsed;
}

function yamlDecimal(value: string): number {
  if (!DECIMAL_TEXT.test(value)) {
    return 0;
  }
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export function processConfigFormToDraftYamlValue(form: ProcessConfigFormState): Record<string, unknown> {
  return {
    process_id: form.processId,
    name: form.name,
    owner_agent_id: form.ownerAgentId,
    group: form.group,
    command: form.command,
    args: [...form.args],
    working_directory: form.workingDirectory,
    run_as_user: form.runAsUser,
    environment: Object.fromEntries(form.environment.map(({ key, value }) => [key, value])),
    instances: yamlInteger(form.instances),
    autostart: form.autostart,
    stop_signal: form.stopSignal,
    kill_signal: form.killSignal,
    stop_timeout_ms: yamlInteger(form.stopTimeoutMs),
    startup_priority: yamlInteger(form.startupPriority),
    restart: {
      mode: form.restart.mode,
      max_retries: yamlInteger(form.restart.maxRetries),
      retry_window_ms: yamlInteger(form.restart.retryWindowMs),
      backoff: {
        initial_ms: yamlInteger(form.restart.backoff.initialMs),
        max_ms: yamlInteger(form.restart.backoff.maxMs),
        multiplier: yamlDecimal(form.restart.backoff.multiplier),
      },
    },
    health: {
      type: form.health.type,
      url: form.health.url,
      method: form.health.method,
      address: form.health.address,
      command: form.health.command,
      expected_status: yamlInteger(form.health.expectedStatus),
      args: [...form.health.args],
      initial_delay_ms: yamlInteger(form.health.initialDelayMs),
      interval_ms: yamlInteger(form.health.intervalMs),
      timeout_ms: yamlInteger(form.health.timeoutMs),
      failure_threshold: yamlInteger(form.health.failureThreshold),
      success_threshold: yamlInteger(form.health.successThreshold),
      restart_on_failure: form.health.restartOnFailure,
      restart_cooldown_ms: yamlInteger(form.health.restartCooldownMs),
    },
    log: {
      directory: form.log.directory,
      redirect_stderr: form.log.redirectStderr,
      max_size: yamlInteger(form.log.maxSize),
      max_files: yamlInteger(form.log.maxFiles),
      max_age_seconds: yamlInteger(form.log.maxAgeSeconds),
      compress: form.log.compress,
    },
    resources: {
      cpu_quota_millis: yamlInteger(form.resources.cpuQuotaMillis),
      memory_bytes: yamlInteger(form.resources.memoryBytes),
      open_files: yamlInteger(form.resources.openFiles),
    },
    dependencies: form.dependencies.map(({ processName, condition }) => ({
      process_name: processName,
      condition,
    })),
    latest_revision: yamlInteger(form.latestRevision),
  };
}

export function stringifyProcessConfigDraftYaml(
  form: ProcessConfigFormState,
  omitKeys: readonly string[] = [],
): string {
  const value = processConfigFormToDraftYamlValue(form);
  for (const key of omitKeys) {
    delete value[key];
  }
  return stringifyYaml(value, {
    aliasDuplicateObjects: false,
    lineWidth: 0,
  });
}

export function parseProcessConfigYaml(text: string): ProcessSpec {
  const value: unknown = parseYaml(text, {
    intAsBigInt: true,
    maxAliasCount: 100,
    prettyErrors: true,
  });
  return fromJson(ProcessSpecSchema, yamlIntegersToProtobufJson(value));
}

function addIssue(issues: ProcessConfigIssue[], path: string, code: ProcessConfigIssueCode): void {
  issues.push({ path, code });
}

function validateInteger(
  issues: ProcessConfigIssue[],
  path: string,
  value: string,
  min: bigint,
  max: bigint,
  rangeCode: "int32OutOfRange" | "int64OutOfRange",
): bigint | undefined {
  if (!INTEGER_TEXT.test(value)) {
    addIssue(issues, path, "invalidInteger");
    return undefined;
  }
  const parsed = BigInt(value);
  if (parsed < min || parsed > max) {
    addIssue(issues, path, rangeCode);
    return undefined;
  }
  return parsed;
}

function validateNonNegative(
  issues: ProcessConfigIssue[],
  path: string,
  value: string,
  min: bigint,
  max: bigint,
  rangeCode: "int32OutOfRange" | "int64OutOfRange",
): bigint | undefined {
  const parsed = validateInteger(issues, path, value, min, max, rangeCode);
  if (parsed !== undefined && parsed < 0n) {
    addIssue(issues, path, "minimumZero");
  }
  return parsed;
}

function validateDecimal(issues: ProcessConfigIssue[], path: string, value: string): number | undefined {
  if (!DECIMAL_TEXT.test(value)) {
    addIssue(issues, path, "invalidDecimal");
    return undefined;
  }
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    addIssue(issues, path, "invalidDecimal");
    return undefined;
  }
  return parsed;
}

function hasAllowedHttpScheme(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

export function validateProcessConfigForm(form: ProcessConfigFormState): ProcessConfigIssue[] {
  const issues: ProcessConfigIssue[] = [];
  if (!/^[a-zA-Z][a-zA-Z0-9_-]{0,62}$/.test(form.name)) {
    addIssue(issues, "name", "invalidName");
  }
  const group = form.group.trim();
  if (group && !/^[A-Za-z0-9._-]{1,64}$/.test(group)) {
    addIssue(issues, "group", "invalidGroup");
  }
  if (!form.command.trim()) {
    addIssue(issues, "command", "required");
  }

  const instances = validateInteger(issues, "instances", form.instances, INT32_MIN, INT32_MAX, "int32OutOfRange");
  if (instances !== undefined && instances < 1n) {
    addIssue(issues, "instances", "minimumOne");
  }
  validateNonNegative(issues, "stopTimeoutMs", form.stopTimeoutMs, INT64_MIN, INT64_MAX, "int64OutOfRange");
  validateInteger(issues, "startupPriority", form.startupPriority, INT32_MIN, INT32_MAX, "int32OutOfRange");
  validateInteger(issues, "latestRevision", form.latestRevision, INT64_MIN, INT64_MAX, "int64OutOfRange");

  if (shouldIncludeRestart(form)) {
    if (!RESTART_MODES.has(form.restart.mode)) {
      addIssue(issues, "restart.mode", "invalidOption");
    }
    const maxRetries = validateNonNegative(issues, "restart.maxRetries", form.restart.maxRetries, INT32_MIN, INT32_MAX, "int32OutOfRange");
    const retryWindow = validateNonNegative(issues, "restart.retryWindowMs", form.restart.retryWindowMs, INT64_MIN, INT64_MAX, "int64OutOfRange");
    if (maxRetries !== undefined && retryWindow !== undefined && maxRetries > 0n && retryWindow <= 0n) {
      addIssue(issues, "restart.retryWindowMs", "retryWindowRequired");
    }
    if (shouldIncludeRestartBackoff(form)) {
      validateNonNegative(issues, "restart.backoff.initialMs", form.restart.backoff.initialMs, INT64_MIN, INT64_MAX, "int64OutOfRange");
      validateNonNegative(issues, "restart.backoff.maxMs", form.restart.backoff.maxMs, INT64_MIN, INT64_MAX, "int64OutOfRange");
      const multiplier = validateDecimal(issues, "restart.backoff.multiplier", form.restart.backoff.multiplier);
      if (multiplier !== undefined && multiplier !== 0 && multiplier < 1) {
        addIssue(issues, "restart.backoff.multiplier", "multiplierMinimum");
      }
    }
  }

  if (shouldIncludeHealth(form)) {
    if (!HEALTH_TYPES.has(form.health.type)) {
      addIssue(issues, "health.type", "invalidOption");
    }
    if (form.health.type === "http" && !hasAllowedHttpScheme(form.health.url)) {
      addIssue(issues, "health.url", "httpUrlRequired");
    }
    if (form.health.type === "tcp" && !form.health.address.trim()) {
      addIssue(issues, "health.address", "tcpAddressRequired");
    }
    if (form.health.type === "exec" && !form.health.command.trim()) {
      addIssue(issues, "health.command", "execCommandRequired");
    }
    validateInteger(issues, "health.expectedStatus", form.health.expectedStatus, INT32_MIN, INT32_MAX, "int32OutOfRange");
    validateNonNegative(issues, "health.initialDelayMs", form.health.initialDelayMs, INT64_MIN, INT64_MAX, "int64OutOfRange");
    validateNonNegative(issues, "health.intervalMs", form.health.intervalMs, INT64_MIN, INT64_MAX, "int64OutOfRange");
    validateNonNegative(issues, "health.timeoutMs", form.health.timeoutMs, INT64_MIN, INT64_MAX, "int64OutOfRange");
    validateNonNegative(issues, "health.failureThreshold", form.health.failureThreshold, INT32_MIN, INT32_MAX, "int32OutOfRange");
    validateNonNegative(issues, "health.successThreshold", form.health.successThreshold, INT32_MIN, INT32_MAX, "int32OutOfRange");
    validateNonNegative(issues, "health.restartCooldownMs", form.health.restartCooldownMs, INT64_MIN, INT64_MAX, "int64OutOfRange");
  }

  if (shouldIncludeLog(form)) {
    const directory = form.log.directory.trim();
    if (directory !== "" && !directory.startsWith("/")) {
      addIssue(issues, "log.directory", "invalidLogDirectory");
    }
    validateNonNegative(issues, "log.maxSize", form.log.maxSize, INT64_MIN, INT64_MAX, "int64OutOfRange");
    validateNonNegative(issues, "log.maxFiles", form.log.maxFiles, INT32_MIN, INT32_MAX, "int32OutOfRange");
    validateNonNegative(issues, "log.maxAgeSeconds", form.log.maxAgeSeconds, INT64_MIN, INT64_MAX, "int64OutOfRange");
  }
  if (shouldIncludeResources(form)) {
    validateNonNegative(issues, "resources.cpuQuotaMillis", form.resources.cpuQuotaMillis, INT64_MIN, INT64_MAX, "int64OutOfRange");
    validateNonNegative(issues, "resources.memoryBytes", form.resources.memoryBytes, INT64_MIN, INT64_MAX, "int64OutOfRange");
    validateNonNegative(issues, "resources.openFiles", form.resources.openFiles, INT64_MIN, INT64_MAX, "int64OutOfRange");
  }

  const environmentKeys = new Set<string>();
  for (const { key } of form.environment) {
    if (environmentKeys.has(key)) {
      addIssue(issues, "environment", "duplicateEnvironmentKey");
      break;
    }
    environmentKeys.add(key);
  }
  const dependencyNames = new Set<string>();
  for (const [index, dependency] of form.dependencies.entries()) {
    if (!dependency.processName.trim()) {
      addIssue(issues, `dependencies.${index}.processName`, "required");
    }
    if (dependencyNames.has(dependency.processName)) {
      addIssue(issues, "dependencies", "duplicateDependency");
      break;
    }
    if (!DEPENDENCY_CONDITIONS.has(dependency.condition)) {
      addIssue(issues, `dependencies.${index}.condition`, "invalidOption");
    }
    dependencyNames.add(dependency.processName);
  }

  return issues;
}

export function validateProcessSpec(spec: ProcessSpec): ProcessConfigIssue[] {
  return validateProcessConfigForm(specToProcessConfigForm(spec));
}
