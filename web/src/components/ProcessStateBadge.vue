<script setup lang="ts">
import { computed } from "vue";
import { processStateTone, type ProcessStateKind } from "../lib/processStateTone";
import { useProcessState } from "../lib/useProcessState";

const props = defineProps<{
  kind: ProcessStateKind;
  state: string;
}>();

const { translateDesiredState, translateObservedState, translateHealthState } = useProcessState();

const label = computed(() => {
  switch (props.kind) {
    case "desired":
      return translateDesiredState(props.state);
    case "health":
      return translateHealthState(props.state);
    default:
      return translateObservedState(props.state);
  }
});

const tone = computed(() => processStateTone(props.kind, props.state));
</script>

<template>
  <span class="state-pill" :class="tone" :data-kind="kind" :data-state="state">
    {{ label }}
  </span>
</template>
