<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch, type ComponentPublicInstance } from "vue";
import { X } from "lucide-vue-next";

const props = withDefaults(defineProps<{
  open: boolean;
  title?: string;
  closeLabel?: string;
}>(), {
  title: "",
  closeLabel: "Close",
});

const emit = defineEmits<{
  close: [];
}>();

const drawerRef = ref<HTMLElement | null>(null);
const panelRef = ref<HTMLElement | null>(null);
let previousActiveElement: HTMLElement | null = null;
const DRAWER_Z_INDEX = 1100;

const FOCUSABLE_SELECTOR = [
  "button:not([disabled])",
  "[href]",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

function focusableElements(): HTMLElement[] {
  return Array.from(panelRef.value?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? []).filter(
    (element) => element.getAttribute("aria-hidden") !== "true",
  );
}

function setDrawerRef(element: Element | ComponentPublicInstance | null): void {
  drawerRef.value = element instanceof HTMLElement ? element : null;
}

function setPanelRef(element: Element | ComponentPublicInstance | null): void {
  panelRef.value = element instanceof HTMLElement ? element : null;
}

function close(): void {
  emit("close");
}

function onBackdropClick(e: MouseEvent): void {
  if (e.target === drawerRef.value) {
    emit("close");
  }
}

function onDocumentKeydown(e: KeyboardEvent): void {
  if (!props.open) {
    return;
  }
  if (e.key === "Escape") {
    emit("close");
    return;
  }
  if (e.key === "Tab") {
    trapFocus(e);
  }
}

function trapFocus(e: KeyboardEvent): void {
  const elements = focusableElements();
  if (!elements.length) {
    e.preventDefault();
    panelRef.value?.focus();
    return;
  }
  const first = elements[0];
  const last = elements[elements.length - 1];
  const active = document.activeElement as HTMLElement | null;
  if (!active || !elements.includes(active)) {
    e.preventDefault();
    (e.shiftKey ? last : first).focus();
    return;
  }
  if (e.shiftKey && active === first) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && active === last) {
    e.preventDefault();
    first.focus();
  }
}

onMounted(() => {
  document.addEventListener("keydown", onDocumentKeydown);
});

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
    } else {
      document.body.style.overflow = "";
      await nextTick();
      previousActiveElement?.focus();
      previousActiveElement = null;
    }
  },
  { flush: "post" },
);
</script>

<template>
  <Teleport to="body">
    <Transition name="drawer">
      <div
        v-if="open"
        :ref="setDrawerRef"
        class="drawer-backdrop"
        :style="{ zIndex: DRAWER_Z_INDEX }"
        @click="onBackdropClick"
      >
        <div
          :ref="setPanelRef"
          class="drawer-panel"
          role="dialog"
          :aria-modal="true"
          :aria-label="title"
          tabindex="-1"
        >
          <div class="drawer-header">
            <h2 v-if="title" class="drawer-title">{{ title }}</h2>
            <button type="button" class="drawer-close" :aria-label="closeLabel" @click="close">
              <X :size="20" aria-hidden="true" />
            </button>
          </div>
          <div class="drawer-content">
            <slot />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.drawer-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  justify-content: flex-end;
}

.drawer-panel {
  width: 100%;
  max-width: 32rem;
  background: var(--color-card);
  box-shadow: -4px 0 24px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.drawer-panel:focus {
  outline: none;
}

.drawer-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 1.25rem 1.5rem;
  border-bottom: 1px solid var(--color-border);
}

.drawer-title {
  margin: 0;
  font-size: 1.125rem;
  font-weight: 600;
  color: var(--color-text);
}

.drawer-close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  padding: 0;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
  transition: all 150ms;
}

.drawer-close:hover {
  background: var(--color-hover);
  color: var(--color-text);
}

.drawer-close:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.drawer-content {
  flex: 1;
  overflow-y: auto;
  padding: 1.5rem;
}

.drawer-enter-active,
.drawer-leave-active {
  transition: opacity 200ms;
}

.drawer-enter-active .drawer-panel,
.drawer-leave-active .drawer-panel {
  transition: transform 250ms cubic-bezier(0.4, 0, 0.2, 1);
}

.drawer-enter-from,
.drawer-leave-to {
  opacity: 0;
}

.drawer-enter-from .drawer-panel,
.drawer-leave-to .drawer-panel {
  transform: translateX(100%);
}

@media (max-width: 640px) {
  .drawer-panel {
    max-width: 100%;
  }
}

@media (prefers-reduced-motion: reduce) {
  .drawer-enter-active,
  .drawer-leave-active,
  .drawer-enter-active .drawer-panel,
  .drawer-leave-active .drawer-panel {
    transition-duration: 1ms;
  }
}
</style>
