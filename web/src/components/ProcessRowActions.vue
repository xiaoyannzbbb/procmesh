<script setup lang="ts">
import { OctagonX, Play, RotateCw, Square } from "lucide-vue-next";
import { computed } from "vue";
import { useI18n } from "../lib/useI18n";
import type { ClusterProcessRow } from "../pages/processView";

type ProcessAction = "start" | "stop" | "restart" | "kill";

const props = defineProps<{
  row: ClusterProcessRow;
  acting?: boolean;
  canStart: boolean;
  canStop: boolean;
  canRestart: boolean;
}>();

const emit = defineEmits<{
  action: [action: ProcessAction];
}>();

const { t } = useI18n();

const primary = computed<ProcessAction>(() => {
  const observed = props.row.observed;
  if (observed === "RUNNING" || observed === "STARTING" || observed === "STOPPING") {
    return "stop";
  }
  if (observed === "BACKOFF" || observed === "FATAL") {
    return "restart";
  }
  return "start";
});

function allowed(action: ProcessAction): boolean {
  if (action === "start") {
    return props.canStart;
  }
  if (action === "restart") {
    return props.canRestart;
  }
  return props.canStop;
}

function disabled(action: ProcessAction): boolean {
  return Boolean(props.acting) || !props.row.ownerNodeId || !allowed(action);
}

function reason(action: ProcessAction): string {
  if (props.acting) {
    return t("processes.actions.busy");
  }
  if (!props.row.ownerNodeId) {
    return t("processes.actions.disabledNoOwner");
  }
  if (!allowed(action)) {
    return t("processes.actions.disabledNoPermission");
  }
  return "";
}

function label(action: ProcessAction): string {
  if (action === "start") {
    return t("processes.actions.start");
  }
  if (action === "stop") {
    return t("processes.actions.stop");
  }
  if (action === "restart") {
    return t("processes.actions.restart");
  }
  return t("processes.actions.forceStop");
}

function titleFor(action: ProcessAction): string {
  return reason(action) || label(action);
}
</script>

<template>
  <div class="row-actions">
    <button
      type="button"
      class="icon-btn"
      :class="{ primary: primary === 'start' }"
      :disabled="disabled('start')"
      :title="titleFor('start')"
      :aria-label="label('start')"
      @click.stop="emit('action', 'start')"
    >
      <Play :size="16" />
    </button>
    <button
      type="button"
      class="icon-btn"
      :class="{ primary: primary === 'stop' }"
      :disabled="disabled('stop')"
      :title="titleFor('stop')"
      :aria-label="label('stop')"
      @click.stop="emit('action', 'stop')"
    >
      <Square :size="14" />
    </button>
    <button
      type="button"
      class="icon-btn"
      :class="{ primary: primary === 'restart' }"
      :disabled="disabled('restart')"
      :title="titleFor('restart')"
      :aria-label="label('restart')"
      @click.stop="emit('action', 'restart')"
    >
      <RotateCw :size="16" />
    </button>
    <button
      type="button"
      class="icon-btn danger"
      :disabled="disabled('kill')"
      :title="titleFor('kill')"
      :aria-label="label('kill')"
      @click.stop="emit('action', 'kill')"
    >
      <OctagonX :size="16" />
    </button>
  </div>
</template>

<style scoped>
.row-actions {
  display: inline-flex;
  align-items: center;
  justify-content: flex-end;
  gap: 0.125rem;
}

.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 28px;
  min-height: 28px;
  padding: 0;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text);
  cursor: pointer;
  transition: background 0.15s, color 0.15s, border-color 0.15s;
}

.icon-btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-text) 7%, transparent);
}

.icon-btn.primary {
  border-color: color-mix(in srgb, var(--color-accent) 35%, var(--color-border));
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
}

.icon-btn.primary:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-accent) 18%, transparent);
}

.icon-btn.danger {
  color: var(--color-danger);
}

.icon-btn:disabled {
  opacity: 0.38;
  cursor: not-allowed;
}

@media (prefers-reduced-motion: reduce) {
  .icon-btn {
    transition: none;
  }
}
</style>
