<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, type RunMeta } from '../api'
import TerminalPanel from './TerminalPanel.vue'
import { formatDateTime, formatDuration, statusLabel } from '../lib/format'
import { refreshRunning } from '../lib/running'
import { RunStream, type StreamState } from '../lib/stream'

// One run: status, controls, live output, input and files. `details` adds who/when/arguments.
const props = defineProps<{ id: string; details?: boolean }>()
const emit = defineEmits<{ loaded: [meta: RunMeta] }>()
const router = useRouter()

const meta = ref<RunMeta | null>(null)
const loadError = ref('')
const terminal = ref<InstanceType<typeof TerminalPanel>>()
const state = ref<StreamState>('connecting')
const input = ref('')
const hideInput = ref(false)
const actionError = ref('')
const now = ref(Date.now())
let stream: RunStream | undefined
let ticker: ReturnType<typeof setInterval> | undefined

const running = computed(() => meta.value?.status === 'running')
// Viewers (view access to the environment) can watch but not control others' runs.
const canControl = computed(() => meta.value?.can_control !== false)
const status = computed(() => statusLabel(meta.value?.status ?? ''))

async function refresh() {
  try {
    meta.value = await api.run(props.id)
    emit('loaded', meta.value)
  } catch (e) {
    loadError.value = e instanceof ApiError && e.status === 404 ? 'This run does not exist or is not visible to you.' : String(e)
  }
}

function follow() {
  stream?.dispose()
  terminal.value?.clear()
  stream = new RunStream((offset) => api.socketURL(props.id, offset), {
    onOutput: (data) => terminal.value?.write(data),
    onResult: () => {
      // The history record is completed right after the result; give it a moment for files.
      void refresh()
      setTimeout(() => void refresh(), 1000)
      refreshRunning()
    },
    onState: (s) => (state.value = s),
    onError: (m) => (actionError.value = m),
  })
  stream.connect()
}

async function sendInput() {
  const line = input.value
  const secret = hideInput.value
  input.value = ''
  actionError.value = ''
  // On a terminal the script's terminal echoes input itself (hidden input comes back masked).
  if (!meta.value?.terminal) {
    terminal.value?.write(secret ? '\x1b[2m(hidden input sent)\x1b[0m\n' : `\x1b[2m${line}\x1b[0m\n`)
  }
  if (!stream?.send({ type: 'input', line, secret })) {
    await api.input(props.id, line, secret).catch((e: Error) => (actionError.value = e.message))
  }
}

async function act(action: 'stop' | 'kill' | 'close_input') {
  actionError.value = ''
  if (action === 'kill' && !confirm('Kill this run and all of its processes immediately?')) return
  if (stream?.send({ type: action })) return
  try {
    if (action === 'stop') await api.stop(props.id)
    else if (action === 'kill') await api.kill(props.id)
    else await api.closeInput(props.id)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  }
}

function runAgain() {
  if (!meta.value) return
  void router.push({ name: 'script', params: { name: meta.value.script }, state: { prefill: JSON.parse(JSON.stringify(meta.value.values ?? {})) } })
}

async function start() {
  loadError.value = ''
  actionError.value = ''
  meta.value = null
  await refresh()
  if (meta.value) follow()
}

onMounted(() => {
  void start()
  ticker = setInterval(() => (now.value = Date.now()), 1000)
})
watch(() => props.id, start)
onBeforeUnmount(() => {
  stream?.dispose()
  clearInterval(ticker)
})
</script>

<template>
  <section class="run-panel" aria-label="Output">
    <p v-if="loadError" class="alert" role="alert">{{ loadError }}</p>
    <div class="run-bar">
      <strong>Output</strong>
      <template v-if="meta">
        <span class="badge" :class="`tone-${status.tone}`" role="status">{{ status.text }}</span>
        <span v-if="meta.exit_code !== undefined" class="muted small">exit code {{ meta.exit_code }}</span>
        <span class="muted small">{{ formatDuration(meta.started, meta.finished, now) }}</span>
        <span v-if="state === 'reconnecting'" class="muted small">reconnecting…</span>
        <RouterLink v-if="!details" class="small" :to="{ name: 'run', params: { id: meta.id } }">Details</RouterLink>
      </template>
      <span class="spacer" />
      <template v-if="running && canControl">
        <button class="btn" type="button" @click="act('stop')">Stop</button>
        <button class="btn btn-danger" type="button" @click="act('kill')">Kill</button>
      </template>
      <span v-else-if="running" class="muted small">View only</span>
      <button v-else-if="details && meta" class="btn" type="button" @click="runAgain">Run again</button>
    </div>

    <dl v-if="details && meta" class="meta card">
      <dt>Started by</dt>
      <dd>{{ meta.user }}</dd>
      <template v-if="meta.environments?.length && meta.environments[0] !== '-'">
        <dt>Environments</dt>
        <dd>{{ meta.environments.join(', ') }}</dd>
      </template>
      <template v-if="meta.secrets?.length">
        <dt>Secrets</dt>
        <dd title="Given to the script and masked in its output">{{ meta.secrets.join(', ') }}</dd>
      </template>
      <template v-if="meta.clusters?.length">
        <dt>Kubernetes</dt>
        <dd title="The script's KUBECONFIG holds these clusters, one context per environment">
          <div v-for="c in meta.clusters" :key="c.environment">
            {{ c.environment }}: <code>{{ c.server }}</code><template v-if="c.namespace"> (namespace {{ c.namespace }})</template>
          </div>
        </dd>
      </template>
      <dt>Started</dt>
      <dd>{{ formatDateTime(meta.started) }}</dd>
      <dt>Arguments</dt>
      <dd><code>{{ meta.args?.length ? meta.args.join(' ') : '(none)' }}</code></dd>
      <template v-for="(v, k) in meta.values ?? {}" :key="k">
        <dt>{{ k }}</dt>
        <dd>{{ Array.isArray(v) ? v.join(', ') : String(v) }}</dd>
      </template>
    </dl>

    <TerminalPanel ref="terminal" />
    <p v-if="meta?.output_truncated" class="muted small">Output was truncated: the run printed more than the storage limit.</p>

    <form v-if="running && canControl" class="row" @submit.prevent="sendInput">
      <label for="stdin" class="sr-only">Input for the script</label>
      <input
        id="stdin"
        v-model="input"
        :type="hideInput ? 'password' : 'text'"
        autocomplete="off"
        placeholder="Type an answer and press Enter"
        style="flex: 1; min-width: 12rem"
      />
      <label class="row"><input v-model="hideInput" type="checkbox" /> hide</label>
      <button class="btn" type="submit">Send</button>
      <button class="btn btn-quiet" type="button" title="Send end-of-input" @click="act('close_input')">Close input</button>
    </form>
    <p v-if="actionError" class="alert" role="alert">{{ actionError }}</p>

    <div v-if="meta?.files?.length" class="row small">
      <strong>Files:</strong>
      <a v-for="f in meta.files" :key="f" :href="api.fileURL(meta.id, f)" download>{{ f }}</a>
    </div>
  </section>
</template>
