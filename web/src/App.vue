<script setup lang="ts">
import LanguageSwitcher from './components/LanguageSwitcher.vue'
</script>

<template>
  <div id="app">
    <header>
      <LanguageSwitcher />
    </header>
    <RouterView />
  </div>
</template>

