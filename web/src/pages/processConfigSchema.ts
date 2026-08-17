import type { ProcessConfigFormState } from "./processConfigForm";

export const PROCESS_CONFIG_SECTION_IDS = [
  "identity",
  "execution",
  "runtime",
  "restart",
  "health",
  "logsResources",
  "environment",
  "dependencies",
] as const;

export type ProcessConfigSectionId = (typeof PROCESS_CONFIG_SECTION_IDS)[number];

export type ProcessConfigSection = {
  id: ProcessConfigSectionId;
  labelKey: string;
};

export type ProcessConfigFieldPath =
  | "processId"
  | "name"
  | "ownerAgentId"
  | "group"
  | "command"
  | "args"
  | "workingDirectory"
  | "runAsUser"
  | "environment"
  | "instances"
  | "autostart"
  | "stopSignal"
  | "killSignal"
  | "stopTimeoutMs"
  | "startupPriority"
  | "restart.mode"
  | "restart.maxRetries"
  | "restart.retryWindowMs"
  | "restart.backoff.initialMs"
  | "restart.backoff.maxMs"
  | "restart.backoff.multiplier"
  | "health.type"
  | "health.url"
  | "health.method"
  | "health.address"
  | "health.command"
  | "health.expectedStatus"
  | "health.args"
  | "health.initialDelayMs"
  | "health.intervalMs"
  | "health.timeoutMs"
  | "health.failureThreshold"
  | "health.successThreshold"
  | "health.restartOnFailure"
  | "health.restartCooldownMs"
  | "log.maxSize"
  | "log.maxFiles"
  | "log.maxAgeSeconds"
  | "log.compress"
  | "resources.cpuQuotaMillis"
  | "resources.memoryBytes"
  | "resources.openFiles"
  | "dependencies"
  | "latestRevision";

export type ProcessConfigControl = "text" | "integer" | "decimal" | "select" | "boolean" | "readonly";

export type ProcessConfigField = {
  path: ProcessConfigFieldPath;
  section: ProcessConfigSectionId;
  control: ProcessConfigControl;
  labelKey: string;
  helpKey?: string;
  unitKey?: string;
  options?: readonly { value: string; labelKey: string }[];
  visible?: (form: ProcessConfigFormState) => boolean;
};

export const PROCESS_CONFIG_SECTIONS: readonly ProcessConfigSection[] = [
  { id: "identity", labelKey: "processConfig.editor.section.identity" },
  { id: "execution", labelKey: "processConfig.editor.section.execution" },
  { id: "runtime", labelKey: "processConfig.editor.section.runtime" },
  { id: "restart", labelKey: "processConfig.editor.section.restart" },
  { id: "health", labelKey: "processConfig.editor.section.health" },
  { id: "logsResources", labelKey: "processConfig.editor.section.logsResources" },
  { id: "environment", labelKey: "processConfig.editor.section.environment" },
  { id: "dependencies", labelKey: "processConfig.editor.section.dependencies" },
] as const;

const restartModes = ["never", "always", "on-failure"] as const;
const healthTypes = ["alive", "http", "tcp", "exec"] as const;
const dependencyConditions = ["STARTED", "HEALTHY"] as const;

function options(values: readonly string[], prefix: string) {
  return values.map((value) => ({ value, labelKey: `${prefix}.${value}` }));
}

export const PROCESS_CONFIG_FIELDS: readonly ProcessConfigField[] = [
  { path: "processId", section: "identity", control: "readonly", labelKey: "processConfig.editor.field.processId" },
  { path: "name", section: "identity", control: "text", labelKey: "processConfig.editor.field.name" },
  { path: "ownerAgentId", section: "identity", control: "text", labelKey: "processConfig.editor.field.ownerAgentId" },
  { path: "group", section: "identity", control: "text", labelKey: "processConfig.editor.field.group" },
  { path: "command", section: "execution", control: "text", labelKey: "processConfig.editor.field.command" },
  { path: "args", section: "execution", control: "text", labelKey: "processConfig.editor.field.args" },
  { path: "workingDirectory", section: "execution", control: "text", labelKey: "processConfig.editor.field.workingDirectory" },
  { path: "runAsUser", section: "execution", control: "text", labelKey: "processConfig.editor.field.runAsUser" },
  { path: "environment", section: "environment", control: "text", labelKey: "processConfig.editor.field.environment" },
  { path: "instances", section: "runtime", control: "integer", labelKey: "processConfig.editor.field.instances" },
  { path: "autostart", section: "runtime", control: "boolean", labelKey: "processConfig.editor.field.autostart" },
  { path: "stopSignal", section: "runtime", control: "text", labelKey: "processConfig.editor.field.stopSignal" },
  { path: "killSignal", section: "runtime", control: "text", labelKey: "processConfig.editor.field.killSignal" },
  { path: "stopTimeoutMs", section: "runtime", control: "integer", labelKey: "processConfig.editor.field.stopTimeoutMs", unitKey: "processConfig.editor.unit.ms" },
  { path: "startupPriority", section: "runtime", control: "integer", labelKey: "processConfig.editor.field.startupPriority" },
  { path: "restart.mode", section: "restart", control: "select", labelKey: "processConfig.editor.field.restartMode", options: options(restartModes, "processConfig.editor.option.restart") },
  { path: "restart.maxRetries", section: "restart", control: "integer", labelKey: "processConfig.editor.field.maxRetries" },
  { path: "restart.retryWindowMs", section: "restart", control: "integer", labelKey: "processConfig.editor.field.retryWindowMs", unitKey: "processConfig.editor.unit.ms" },
  { path: "restart.backoff.initialMs", section: "restart", control: "integer", labelKey: "processConfig.editor.field.initialMs", unitKey: "processConfig.editor.unit.ms" },
  { path: "restart.backoff.maxMs", section: "restart", control: "integer", labelKey: "processConfig.editor.field.maxMs", unitKey: "processConfig.editor.unit.ms" },
  { path: "restart.backoff.multiplier", section: "restart", control: "decimal", labelKey: "processConfig.editor.field.multiplier" },
  { path: "health.type", section: "health", control: "select", labelKey: "processConfig.editor.field.healthType", options: options(healthTypes, "processConfig.editor.option.health") },
  { path: "health.url", section: "health", control: "text", labelKey: "processConfig.editor.field.url", visible: (form) => form.health.type === "http" },
  { path: "health.method", section: "health", control: "text", labelKey: "processConfig.editor.field.method", visible: (form) => form.health.type === "http" },
  { path: "health.address", section: "health", control: "text", labelKey: "processConfig.editor.field.address", visible: (form) => form.health.type === "tcp" },
  { path: "health.command", section: "health", control: "text", labelKey: "processConfig.editor.field.healthCommand", visible: (form) => form.health.type === "exec" },
  { path: "health.expectedStatus", section: "health", control: "integer", labelKey: "processConfig.editor.field.expectedStatus", visible: (form) => form.health.type === "http" },
  { path: "health.args", section: "health", control: "text", labelKey: "processConfig.editor.field.healthArgs", visible: (form) => form.health.type === "exec" },
  { path: "health.initialDelayMs", section: "health", control: "integer", labelKey: "processConfig.editor.field.initialDelayMs", unitKey: "processConfig.editor.unit.ms" },
  { path: "health.intervalMs", section: "health", control: "integer", labelKey: "processConfig.editor.field.intervalMs", unitKey: "processConfig.editor.unit.ms" },
  { path: "health.timeoutMs", section: "health", control: "integer", labelKey: "processConfig.editor.field.timeoutMs", unitKey: "processConfig.editor.unit.ms" },
  { path: "health.failureThreshold", section: "health", control: "integer", labelKey: "processConfig.editor.field.failureThreshold" },
  { path: "health.successThreshold", section: "health", control: "integer", labelKey: "processConfig.editor.field.successThreshold" },
  { path: "health.restartOnFailure", section: "health", control: "boolean", labelKey: "processConfig.editor.field.restartOnFailure" },
  { path: "health.restartCooldownMs", section: "health", control: "integer", labelKey: "processConfig.editor.field.restartCooldownMs", unitKey: "processConfig.editor.unit.ms" },
  { path: "log.maxSize", section: "logsResources", control: "integer", labelKey: "processConfig.editor.field.maxSize", unitKey: "processConfig.editor.unit.bytes" },
  { path: "log.maxFiles", section: "logsResources", control: "integer", labelKey: "processConfig.editor.field.maxFiles" },
  { path: "log.maxAgeSeconds", section: "logsResources", control: "integer", labelKey: "processConfig.editor.field.maxAgeSeconds", unitKey: "processConfig.editor.unit.seconds" },
  { path: "log.compress", section: "logsResources", control: "boolean", labelKey: "processConfig.editor.field.compress" },
  { path: "resources.cpuQuotaMillis", section: "logsResources", control: "integer", labelKey: "processConfig.editor.field.cpuQuotaMillis", unitKey: "processConfig.editor.unit.millis" },
  { path: "resources.memoryBytes", section: "logsResources", control: "integer", labelKey: "processConfig.editor.field.memoryBytes", unitKey: "processConfig.editor.unit.bytes" },
  { path: "resources.openFiles", section: "logsResources", control: "integer", labelKey: "processConfig.editor.field.openFiles" },
  { path: "dependencies", section: "dependencies", control: "select", labelKey: "processConfig.editor.field.dependencies", options: options(dependencyConditions, "processConfig.editor.option.dependency") },
  { path: "latestRevision", section: "identity", control: "readonly", labelKey: "processConfig.editor.field.latestRevision" },
] as const;

export const PROCESS_CONFIG_FIELD_PATHS = PROCESS_CONFIG_FIELDS.map((field) => field.path) as ProcessConfigFieldPath[];
