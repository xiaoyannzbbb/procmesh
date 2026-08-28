<script setup lang="ts">
withDefaults(
  defineProps<{
    size?: number;
  }>(),
  {
    size: 28,
  },
);
</script>

<template>
  <img
    class="brand-mark"
    src="/favicon.svg"
    :width="size"
    :height="size"
    alt=""
  />
</template>

<style scoped>
.brand-mark {
  display: block;
  flex-shrink: 0;
}
</style>
