<template>
  <div class="language-switcher" :class="[{ collapsed }, `variant-${variant}`]">
    <button
      v-for="lang in languages"
      :key="lang.code"
      :class="{ active: currentLanguage === lang.code }"
      @click="setLanguage(lang.code)"
      :data-testid="`lang-${lang.code}`"
      :title="lang.name"
      :aria-label="lang.name"
    >
      <span class="lang-code">{{ lang.code.toUpperCase() }}</span>
      <span class="lang-name">{{ lang.name }}</span>
      <Check v-if="isMenuVariant && currentLanguage === lang.code" :size="17" class="lang-check" aria-hidden="true" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from '../lib/useI18n'
import { Check } from 'lucide-vue-next'
import { computed } from 'vue'

interface Props {
  collapsed?: boolean
  variant?: 'segmented' | 'menu'
}

const props = withDefaults(defineProps<Props>(), { variant: 'segmented' })

const { currentLanguage, setLanguage } = useI18n()
const isMenuVariant = computed(() => props.variant === 'menu')

const languages = [
  { code: 'en', name: 'English' },
  { code: 'zh', name: '中文' }
] as const
</script>

<style scoped>
.language-switcher {
  display: flex;
  gap: 0.375rem;
  padding: 0.25rem;
  background: var(--color-bg);
  border-radius: 8px;
  border: 1px solid var(--color-border);
}

.language-switcher.collapsed {
  flex-direction: column;
  padding: 0.25rem;
}

.language-switcher.variant-menu {
  flex-direction: column;
  gap: 0.125rem;
  padding: 0;
  border: 0;
  background: transparent;
}

.language-switcher.variant-menu button {
  justify-content: flex-start;
  width: 100%;
  padding: 0.625rem 0.75rem;
}

.language-switcher.variant-menu button.active,
.language-switcher.variant-menu button.active:hover {
  background: var(--color-hover);
  color: var(--color-text);
}

.variant-menu .lang-code {
  display: none;
}

.variant-menu .lang-name {
  margin-left: 0;
}

.lang-check {
  margin-left: auto;
  color: var(--color-accent);
}

.language-switcher button {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0.375rem 0.75rem;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--color-muted);
  font-size: 0.8125rem;
  font-weight: 500;
  cursor: pointer;
  min-height: 44px;
  transition: all 0.2s;
  white-space: nowrap;
}

.language-switcher button:hover {
  background: color-mix(in srgb, var(--color-text) 8%, transparent);
  color: var(--color-text);
}

.language-switcher button:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.language-switcher button.active {
  background: var(--color-accent);
  color: white;
}

.language-switcher button.active:hover {
  background: color-mix(in srgb, var(--color-accent) 90%, black);
}

.lang-code {
  font-variant-numeric: tabular-nums;
  letter-spacing: 0.02em;
}

.lang-name {
  display: inline;
  margin-left: 0.375rem;
}

.language-switcher.collapsed .lang-name {
  display: none;
}

.language-switcher.collapsed button {
  padding: 0.375rem 0.5rem;
  min-width: 36px;
}
</style>
