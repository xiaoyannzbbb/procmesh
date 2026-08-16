<template>
  <div class="language-switcher">
    <button
      :class="{ active: currentLanguage === 'en' }"
      @click="setLanguage('en')"
      data-testid="lang-en"
    >
      English
    </button>
    <button
      :class="{ active: currentLanguage === 'zh' }"
      @click="setLanguage('zh')"
      data-testid="lang-zh"
    >
      中文
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from '../lib/useI18n'

const { currentLanguage, setLanguage } = useI18n()
</script>

<style scoped>
.language-switcher {
  display: flex;
  gap: 0.5rem;
}

.language-switcher button {
  padding: 0.25rem 0.75rem;
  border: 1px solid #e5e7eb;
  border-radius: 0.375rem;
  background: transparent;
  cursor: pointer;
  transition: all 0.2s;
}

.language-switcher button:hover {
  background: #f3f4f6;
}

.language-switcher button.active {
  background: #3b82f6;
  color: white;
  border-color: #3b82f6;
}
</style>
