<script setup lang="ts">
import { X } from "lucide-vue-next";
import {
  nextTick,
  onMounted,
  onUnmounted,
  ref,
  useId,
  watch,
  type ComponentPublicInstance,
} from "vue";

export interface PermissionGroup {
  key: string;
  label: string;
  permissions: string[];
}

const props = defineProps<{
  open: boolean;
  title: string;
  summary: string;
  closeLabel: string;
  emptyLabel: string;
  groups: PermissionGroup[];
}>();

const emit = defineEmits<{
  close: [];
}>();

const backdropRef = ref<HTMLElement | null>(null);
const panelRef = ref<HTMLElement | null>(null);
const titleId = useId();
const summaryId = useId();
let previousActiveElement: HTMLElement | null = null;

const FOCUSABLE_SELECTOR = [
  "button:not([disabled])",
  "[href]",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

function setBackdropRef(element: Element | ComponentPublicInstance | null): void {
  backdropRef.value = element instanceof HTMLElement ? element : null;
}

function setPanelRef(element: Element | ComponentPublicInstance | null): void {
  panelRef.value = element instanceof HTMLElement ? element : null;
}

function focusableElements(): HTMLElement[] {
  return Array.from(panelRef.value?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? []);
}

function close(): void {
  emit("close");
}

function onBackdropClick(event: MouseEvent): void {
  if (event.target === backdropRef.value) {
    close();
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
    first.focus();
  } else if (event.shiftKey && active === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && active === last) {
    event.preventDefault();
    first.focus();
  }
}

function onDocumentKeydown(event: KeyboardEvent): void {
  if (!props.open) return;
  if (event.key === "Escape") {
    close();
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
      (focusableElements()[0] ?? panelRef.value)?.focus();
      return;
    }
    document.body.style.overflow = "";
    await nextTick();
    previousActiveElement?.focus();
    previousActiveElement = null;
  },
  { flush: "post" },
);
</script>

<template>
  <Teleport to="body">
    <Transition name="permission-dialog">
      <div
        v-if="open"
        :ref="setBackdropRef"
        class="permission-backdrop"
        @click="onBackdropClick"
      >
        <section
          :ref="setPanelRef"
          class="permission-panel"
          data-testid="permission-dialog"
          role="dialog"
          :aria-modal="true"
          :aria-labelledby="titleId"
          :aria-describedby="summaryId"
          tabindex="-1"
        >
          <header class="permission-header">
            <span class="permission-heading">
              <h2 :id="titleId">{{ title }}</h2>
              <span :id="summaryId" class="permission-summary">{{ summary }}</span>
            </span>
            <button type="button" class="permission-close" :aria-label="closeLabel" @click="close">
              <X :size="20" aria-hidden="true" />
            </button>
          </header>

          <div class="permission-content">
            <p v-if="!groups.length" class="permission-empty">{{ emptyLabel }}</p>
            <div v-else class="permission-groups">
              <section
                v-for="group in groups"
                :key="group.key"
                class="permission-group"
                :data-permission-group="group.key"
              >
                <h3>{{ group.label }}</h3>
                <ul>
                  <li
                    v-for="permission in group.permissions"
                    :key="permission"
                    class="permission-item"
                    data-permission
                  >
                    {{ permission }}
                  </li>
                </ul>
              </section>
            </div>
          </div>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.permission-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1150;
  display: grid;
  place-items: center;
  padding: 1rem;
  background: rgba(0, 0, 0, 0.55);
}

.permission-panel {
  display: flex;
  flex-direction: column;
  width: min(100%, 44rem);
  max-height: calc(100dvh - 2rem);
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-card);
  color: var(--color-text);
  box-shadow: 0 1rem 3rem rgba(0, 0, 0, 0.3);
}

.permission-panel:focus {
  outline: none;
}

.permission-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
  padding: 1.25rem 1.5rem;
  border-bottom: 1px solid var(--color-border);
}

.permission-heading {
  display: flex;
  flex-direction: column;
  min-width: 0;
  gap: 0.25rem;
}

.permission-heading h2 {
  margin: 0;
  font-size: 1.125rem;
  font-weight: 650;
  overflow-wrap: anywhere;
}

.permission-summary {
  color: var(--color-muted);
  font-size: 0.8125rem;
}

.permission-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2.75rem;
  height: 2.75rem;
  flex: 0 0 2.75rem;
  padding: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
}

.permission-close:hover {
  background: var(--color-hover);
  color: var(--color-text);
}

.permission-close:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.permission-content {
  overflow-y: auto;
  padding: 0 1.5rem 1.5rem;
}

.permission-groups {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 1.5rem;
}

.permission-group {
  min-width: 0;
  padding: 1.25rem 0;
  border-bottom: 1px solid var(--color-border);
}

.permission-group h3 {
  margin: 0 0 0.625rem;
  font-size: 0.875rem;
  font-weight: 650;
}

.permission-group ul {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.permission-item {
  max-width: 100%;
  padding: 0.25rem 0.5rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-hover);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.75rem;
  font-weight: 500;
  overflow-wrap: anywhere;
}

.permission-empty {
  margin: 0;
  padding: 2.5rem 0;
  color: var(--color-muted);
  text-align: center;
}

.permission-dialog-enter-active,
.permission-dialog-leave-active {
  transition: opacity 160ms ease;
}

.permission-dialog-enter-active .permission-panel,
.permission-dialog-leave-active .permission-panel {
  transition: transform 180ms ease;
}

.permission-dialog-enter-from,
.permission-dialog-leave-to {
  opacity: 0;
}

.permission-dialog-enter-from .permission-panel,
.permission-dialog-leave-to .permission-panel {
  transform: translateY(0.5rem);
}

@media (max-width: 640px) {
  .permission-backdrop {
    padding: 0.5rem;
  }

  .permission-panel {
    max-height: calc(100dvh - 1rem);
  }

  .permission-header,
  .permission-content {
    padding-right: 1rem;
    padding-left: 1rem;
  }

  .permission-groups {
    grid-template-columns: 1fr;
  }
}

@media (prefers-reduced-motion: reduce) {
  .permission-dialog-enter-active,
  .permission-dialog-leave-active,
  .permission-dialog-enter-active .permission-panel,
  .permission-dialog-leave-active .permission-panel {
    transition: none;
  }
}
</style>
