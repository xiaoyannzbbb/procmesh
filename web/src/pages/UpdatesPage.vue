<script setup lang="ts">
import { useI18n } from "../lib/useI18n";

const { t } = useI18n();
</script>

<template>
  <div class="page">
    <h1>{{ t("updates.title") }}</h1>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
h1 {
  margin: 0;
  font-size: 1.35rem;
  font-weight: 650;
}
</style>
