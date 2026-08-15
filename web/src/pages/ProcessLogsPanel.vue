<script setup lang="ts">
import { computed, onUnmounted, ref } from "vue";
import { withTarget } from "../lib/headers";
import { useLogClient } from "../lib/rpc";
import { session } from "../lib/session";
import { formatRemoteError } from "./processView";

const props = withDefaults(
  defineProps<{
    idOrName: string;
    targetNodeId: string;
    instances?: string[];
  }>(),
  { instances: () => [] },
);

const logs = useLogClient();
const streamName = ref("stdout");
const instanceId = ref("");
const logText = ref("");
const errorText = ref("");
const streaming = ref(false);
const busy = ref(false);
let abort: AbortController | null = null;

const canDownload = computed(() => (session.value?.permissions ?? []).includes("process.logs.download"));
const targetOpts = computed(() => ({ headers: withTarget(props.targetNodeId) }));

function stopStream(): void {
  abort?.abort();
  abort = null;
  streaming.value = false;
}

onUnmounted(() => {
  stopStream();
});

async function tail(): Promise<void> {
  stopStream();
  errorText.value = "";
  busy.value = true;
  try {
    const out = await logs.tailLogs(
      {
        idOrName: props.idOrName,
        instanceId: instanceId.value,
        stream: streamName.value,
        lines: 100,
      },
      targetOpts.value,
    );
    logText.value = (out.lines ?? []).join("\n");
  } catch (err) {
    errorText.value = formatRemoteError(err);
  } finally {
    busy.value = false;
  }
}

async function startStream(): Promise<void> {
  stopStream();
  errorText.value = "";
  const ac = new AbortController();
  abort = ac;
  streaming.value = true;
  try {
    const iter = logs.streamLogs(
      {
        idOrName: props.idOrName,
        instanceId: instanceId.value,
        stream: streamName.value,
      },
      { ...targetOpts.value, signal: ac.signal },
    );
    const dec = new TextDecoder();
    for await (const chunk of iter) {
      if (chunk.data?.byteLength) {
        logText.value += dec.decode(chunk.data, { stream: true });
      }
      if (chunk.eof) {
        break;
      }
    }
  } catch (err) {
    if (!ac.signal.aborted) {
      errorText.value = formatRemoteError(err);
    }
  } finally {
    if (abort === ac) {
      streaming.value = false;
      abort = null;
    }
  }
}

async function download(): Promise<void> {
  if (!canDownload.value) {
    return;
  }
  errorText.value = "";
  busy.value = true;
  try {
    const parts: BlobPart[] = [];
    const iter = logs.downloadLogs(
      {
        idOrName: props.idOrName,
        instanceId: instanceId.value,
        stream: streamName.value,
      },
      targetOpts.value,
    );
    for await (const chunk of iter) {
      if (chunk.data?.byteLength) {
        parts.push(chunk.data.slice());
      }
    }
    const blob = new Blob(parts, { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${props.idOrName}-${streamName.value || "stdout"}.log`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  } catch (err) {
    errorText.value = formatRemoteError(err);
  } finally {
    busy.value = false;
  }
}

void tail();
</script>

<template>
  <section class="card">
    <div class="title-row">
      <h2>Logs</h2>
      <div class="controls">
        <label class="field">
          <span>Stream</span>
          <select v-model="streamName" class="input" @change="tail">
            <option value="stdout">stdout</option>
            <option value="stderr">stderr</option>
          </select>
        </label>
        <label class="field">
          <span>Instance</span>
          <select v-model="instanceId" class="input" @change="tail">
            <option value="">All instances</option>
            <option v-for="id in instances" :key="id" :value="id">{{ id }}</option>
          </select>
        </label>
        <button type="button" class="btn" :disabled="busy || !targetNodeId" @click="tail">Tail 100</button>
        <button type="button" class="btn" :disabled="streaming || !targetNodeId" @click="startStream">Stream</button>
        <button v-if="streaming" type="button" class="btn" @click="stopStream">Stop</button>
        <button
          v-if="canDownload"
          type="button"
          class="btn"
          :disabled="busy || !targetNodeId"
          @click="download"
        >
          Download
        </button>
      </div>
    </div>
    <p v-if="errorText" class="error" role="alert">{{ errorText }}</p>
    <pre class="log-window" tabindex="0">{{ logText }}</pre>
  </section>
</template>

<style scoped>
.card {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-card);
  padding: 1.25rem;
}
.title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  justify-content: space-between;
  gap: 0.75rem;
  margin-bottom: 0.75rem;
}
h2 {
  margin: 0;
  font-size: 1.05rem;
  font-weight: 650;
}
.controls {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 0.5rem;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-size: 0.75rem;
  color: var(--color-muted);
}
.field .input {
  width: auto;
  min-width: 8rem;
}
.error {
  margin: 0 0 0.75rem;
  color: var(--color-danger);
  font-size: 0.875rem;
}
.log-window {
  margin: 0;
  min-height: 16rem;
  max-height: 28rem;
  overflow: auto;
  padding: 0.75rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.75rem;
  line-height: 1.45;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
