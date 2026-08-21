<script setup lang="ts">
import { computed } from "vue";
import { session } from "../lib/session";
import { useI18n } from "../lib/useI18n";

const { t } = useI18n();

const canRead = computed(() => (session.value?.permissions ?? []).includes("replication.read"));
</script>

<template>
  <div class="page" :data-permission="canRead ? 'granted' : 'denied'">
    <h1>{{ t("nav.disasterReplica") }}</h1>
    <p v-if="canRead" class="muted">{{ t("common.noData") }}</p>
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
.muted {
  color: var(--color-muted);
}
</style>
