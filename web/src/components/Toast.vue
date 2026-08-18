<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { AlertCircle, CheckCircle, Info, X, XCircle } from "lucide-vue-next";
import { useI18n } from "../lib/useI18n";

export type ToastType = "success" | "error" | "info" | "warning";

const props = defineProps<{
  show: boolean;
  message: string;
  type?: ToastType;
  duration?: number;
}>();

const emit = defineEmits<{
  close: [];
}>();

const { t } = useI18n();
const POLITE_LIVE_REGION = "polite";

const visible = ref(false);
const timer = ref<ReturnType<typeof setTimeout> | null>(null);

const icon = computed(() => {
  switch (props.type) {
    case "success":
      return CheckCircle;
    case "error":
      return XCircle;
    case "warning":
      return AlertCircle;
    default:
      return Info;
  }
});

const iconColor = computed(() => {
  switch (props.type) {
    case "success":
      return "#10b981";
    case "error":
      return "#ef4444";
    case "warning":
      return "#f59e0b";
    default:
      return "#3b82f6";
  }
});

function close(): void {
  visible.value = false;
  if (timer.value) {
    clearTimeout(timer.value);
    timer.value = null;
  }
  setTimeout(() => emit("close"), 300);
}

watch(
  () => props.show,
  (show) => {
    if (show) {
      visible.value = true;
      if (timer.value) {
        clearTimeout(timer.value);
      }
      const duration = props.duration ?? 5000;
      if (duration > 0) {
        timer.value = setTimeout(close, duration);
      }
    } else {
      visible.value = false;
    }
  },
  { immediate: true },
);

onMounted(() => {
  if (props.show) {
    visible.value = true;
    const duration = props.duration ?? 5000;
    if (duration > 0) {
      timer.value = setTimeout(close, duration);
    }
  }
});
</script>

<template>
  <Teleport to="body">
    <Transition name="toast">
      <div v-if="visible" class="toast-container" role="alert" :aria-live="POLITE_LIVE_REGION">
        <div class="toast" :class="`toast-${type || 'info'}`">
          <component :is="icon" :size="20" :color="iconColor" class="toast-icon" />
          <p class="toast-message">{{ message }}</p>
          <button type="button" class="toast-close" :aria-label="t('actions.close')" @click="close">
            <X :size="18" />
          </button>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.toast-container {
  position: fixed;
  top: 1.5rem;
  right: 1.5rem;
  z-index: 9999;
  pointer-events: auto;
}

.toast {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  min-width: 20rem;
  max-width: 28rem;
  padding: 1rem 1.25rem;
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md, 0.5rem);
  box-shadow: 0 10px 25px rgba(0, 0, 0, 0.15);
}

.toast-icon {
  flex-shrink: 0;
}

.toast-message {
  flex: 1;
  margin: 0;
  font-size: 0.875rem;
  line-height: 1.5;
  color: var(--color-text);
  word-break: break-word;
}

.toast-close {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 2.75rem;
  height: 2.75rem;
  padding: 0;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
  transition: all 150ms;
}

.toast-close:hover {
  background: var(--color-hover);
  color: var(--color-text);
}

.toast-close:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.toast-success {
  border-left: 4px solid #10b981;
}

.toast-error {
  border-left: 4px solid #ef4444;
}

.toast-warning {
  border-left: 4px solid #f59e0b;
}

.toast-info {
  border-left: 4px solid #3b82f6;
}

.toast-enter-active,
.toast-leave-active {
  transition: all 300ms cubic-bezier(0.4, 0, 0.2, 1);
}

.toast-enter-from {
  opacity: 0;
  transform: translateX(100%) scale(0.9);
}

.toast-leave-to {
  opacity: 0;
  transform: translateX(100%) scale(0.95);
}

@media (max-width: 640px) {
  .toast-container {
    top: 1rem;
    right: 1rem;
    left: 1rem;
  }

  .toast {
    min-width: auto;
    max-width: 100%;
  }
}
</style>
