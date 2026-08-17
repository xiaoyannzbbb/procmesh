<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { X } from "lucide-vue-next";

const props = defineProps<{
  open: boolean;
  title?: string;
}>();

const emit = defineEmits<{
  close: [];
}>();

const drawerRef = ref<HTMLElement | null>(null);

function onBackdropClick(e: MouseEvent): void {
  if (e.target === drawerRef.value) {
    emit("close");
  }
}

function onEscape(e: KeyboardEvent): void {
  if (e.key === "Escape" && props.open) {
    emit("close");
  }
}

onMounted(() => {
  document.addEventListener("keydown", onEscape);
});

onUnmounted(() => {
  document.removeEventListener("keydown", onEscape);
});

watch(
  () => props.open,
  (isOpen) => {
    if (isOpen) {
      document.body.style.overflow = "hidden";
    } else {
      document.body.style.overflow = "";
    }
  },
);
</script>

<template>
  <Teleport to="body">
    <Transition name="drawer">
      <div v-if="open" ref="drawerRef" class="drawer-backdrop" @click="onBackdropClick">
        <div class="drawer-panel" role="dialog" aria-modal="true" :aria-label="title">
          <div class="drawer-header">
            <h2 v-if="title" class="drawer-title">{{ title }}</h2>
            <button type="button" class="drawer-close" aria-label="Close" @click="emit('close')">
              <X :size="20" />
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
  z-index: 50;
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
</style>
