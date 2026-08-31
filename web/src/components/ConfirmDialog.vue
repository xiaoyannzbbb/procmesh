<script setup lang="ts">
import { LoaderCircle, TriangleAlert } from "lucide-vue-next";
import {
  nextTick,
  onMounted,
  onUnmounted,
  ref,
  useId,
  watch,
  type ComponentPublicInstance,
} from "vue";

const props = withDefaults(defineProps<{
  open: boolean;
  title: string;
  message: string;
  confirmLabel: string;
  cancelLabel: string;
  pending?: boolean;
}>(), {
  pending: false,
});

const emit = defineEmits<{
  cancel: [];
  confirm: [];
}>();

const panelRef = ref<HTMLElement | null>(null);
const cancelButtonRef = ref<HTMLButtonElement | null>(null);
const titleId = useId();
const messageId = useId();
let previousActiveElement: HTMLElement | null = null;

const FOCUSABLE_SELECTOR = [
  "button:not([disabled])",
  "[href]",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

function setPanelRef(element: Element | ComponentPublicInstance | null): void {
  panelRef.value = element instanceof HTMLElement ? element : null;
}

function setCancelButtonRef(element: Element | ComponentPublicInstance | null): void {
  cancelButtonRef.value = element instanceof HTMLButtonElement ? element : null;
}

function focusableElements(): HTMLElement[] {
  return Array.from(panelRef.value?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? []);
}

function cancel(): void {
  if (!props.pending) {
    emit("cancel");
  }
}

function confirm(): void {
  if (!props.pending) {
    emit("confirm");
  }
}

function trapFocus(event: KeyboardEvent): void {
  const elements = focusableElements();
  if (!elements.length) {
    event.preventDefault();
    panelRef.value?.focus();
    return;
  }

  const first = elements[0];
  const last = elements[elements.length - 1];
  const active = document.activeElement as HTMLElement | null;
  if (!active || !elements.includes(active)) {
    event.preventDefault();
    (event.shiftKey ? last : first).focus();
  } else if (event.shiftKey && active === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && active === last) {
    event.preventDefault();
    first.focus();
  }
}

function onDocumentKeydown(event: KeyboardEvent): void {
  if (!props.open) {
    return;
  }
  if (event.key === "Escape") {
    cancel();
  } else if (event.key === "Tab") {
    trapFocus(event);
  }
}

onMounted(() => document.addEventListener("keydown", onDocumentKeydown));

onUnmounted(() => {
  document.removeEventListener("keydown", onDocumentKeydown);
  if (props.open) {
    document.body.style.overflow = "";
    previousActiveElement?.focus();
  }
});

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      previousActiveElement = document.activeElement as HTMLElement | null;
      document.body.style.overflow = "hidden";
      await nextTick();
      (cancelButtonRef.value ?? panelRef.value)?.focus();
      return;
    }

    document.body.style.overflow = "";
    await nextTick();
    previousActiveElement?.focus();
    previousActiveElement = null;
  },
  { flush: "post", immediate: true },
);
</script>

<template>
  <Teleport to="body">
    <Transition name="confirm-dialog">
      <div v-if="open" class="confirm-backdrop" @click.self="cancel">
        <section
          :ref="setPanelRef"
          class="confirm-panel"
          role="dialog"
          :aria-modal="true"
          :aria-labelledby="titleId"
          :aria-describedby="messageId"
          :aria-busy="pending"
          tabindex="-1"
        >
          <div class="confirm-heading">
            <span class="confirm-icon" aria-hidden="true">
              <TriangleAlert :size="20" />
            </span>
            <h2 :id="titleId">{{ title }}</h2>
          </div>
          <div :id="messageId" class="confirm-copy">
            <p>{{ message }}</p>
            <div v-if="$slots.extra" class="confirm-extra">
              <slot name="extra" />
            </div>
          </div>
          <div class="confirm-actions">
            <button
              :ref="setCancelButtonRef"
              type="button"
              class="btn"
              :disabled="pending"
              @click="cancel"
            >
              {{ cancelLabel }}
            </button>
            <button type="button" class="btn btn-danger" :disabled="pending" @click="confirm">
              <LoaderCircle v-if="pending" class="confirm-spinner" :size="16" aria-hidden="true" />
              {{ confirmLabel }}
            </button>
          </div>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.confirm-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: grid;
  place-items: center;
  padding: 1rem;
  background: rgba(0, 0, 0, 0.55);
}

.confirm-panel {
  width: min(100%, 36rem);
  max-height: min(90vh, 40rem);
  overflow: auto;
  padding: 1.5rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  box-shadow: 0 1rem 3rem rgba(0, 0, 0, 0.3);
  color: var(--color-text);
}

.confirm-panel:focus {
  outline: none;
}

.confirm-heading {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.confirm-heading h2 {
  margin: 0;
  font-size: 1.125rem;
  font-weight: 650;
}

.confirm-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  flex: 0 0 2rem;
  border-radius: 50%;
  background: color-mix(in srgb, var(--color-danger) 14%, transparent);
  color: var(--color-danger);
}

.confirm-copy {
  margin: 1rem 0 1.5rem;
  color: var(--color-muted);
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.confirm-copy p {
  margin: 0;
}

.confirm-extra {
  margin-top: 0.85rem;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

.confirm-extra :deep(h3) {
  margin: 0 0 0.35rem;
  color: var(--color-text);
  font-size: 0.8rem;
  font-weight: 650;
}

.confirm-extra :deep(ul) {
  margin: 0;
  padding-left: 1.1rem;
  max-height: 8rem;
  overflow: auto;
}

.confirm-extra :deep(li) {
  color: var(--color-text);
  font-size: 0.875rem;
  line-height: 1.45;
}

.confirm-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.75rem;
}

.confirm-actions .btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
  min-width: 5.5rem;
}

.confirm-spinner {
  animation: confirm-spin 800ms linear infinite;
}

.confirm-dialog-enter-active,
.confirm-dialog-leave-active {
  transition: opacity 150ms ease;
}

.confirm-dialog-enter-active .confirm-panel,
.confirm-dialog-leave-active .confirm-panel {
  transition: transform 150ms ease, opacity 150ms ease;
}

.confirm-dialog-enter-from,
.confirm-dialog-leave-to,
.confirm-dialog-enter-from .confirm-panel,
.confirm-dialog-leave-to .confirm-panel {
  opacity: 0;
}

.confirm-dialog-enter-from .confirm-panel,
.confirm-dialog-leave-to .confirm-panel {
  transform: translateY(0.5rem) scale(0.98);
}

@keyframes confirm-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .confirm-dialog-enter-active,
  .confirm-dialog-leave-active,
  .confirm-dialog-enter-active .confirm-panel,
  .confirm-dialog-leave-active .confirm-panel {
    transition: none;
  }
}
</style>
