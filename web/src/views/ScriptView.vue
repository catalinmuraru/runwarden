<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiError, type Option, type ScriptDetail } from '../api'
import ParamField from '../components/ParamField.vue'
import RunPanel from '../components/RunPanel.vue'
import { accessSummary, decorateOptions, startBlocker } from '../lib/access'
import { buildRunBody, dependencyQuery, fieldErrors, initialValue, type FieldValue } from '../lib/form'
import { renderMarkdown } from '../lib/markdown'
import { formatSeconds } from '../lib/format'
import { refreshRunning } from '../lib/running'

const props = defineProps<{ name: string }>()
const router = useRouter()
const route = useRoute()
// The run shown under the form: the one just started here, or one opened with ?run=.
const runId = computed(() => (typeof route.query.run === 'string' ? route.query.run : ''))

const script = ref<ScriptDetail | null>(null)
const loadError = ref('')
const values = reactive<Record<string, FieldValue>>({})
const touched = reactive(new Set<string>())
const options = reactive<Record<string, Option[]>>({})
const optionHints = reactive<Record<string, string>>({})
const errors = ref<Record<string, string>>({})
const formErrors = ref<string[]>([])
const busyRun = ref('') // the user's run in progress, when only one is allowed
const submitting = ref(false)
const timers = new Map<string, ReturnType<typeof setTimeout>>()

const blocker = computed(() => (script.value ? startBlocker(script.value, values, options) : ''))
const environments = computed(() => (script.value ? accessSummary(script.value) : { run: [], view: [] }))

async function load() {
  script.value = null
  loadError.value = ''
  try {
    const s = await api.script(props.name)
    const prefill = (history.state?.prefill ?? {}) as Record<string, unknown>
    for (const key of Object.keys(values)) delete values[key]
    for (const p of s.parameters) {
      values[p.name] = initialValue(p, prefill[p.name])
      if (p.values && !p.dynamic_values) options[p.name] = p.values
    }
    script.value = s
    document.title = `${s.name} · Runwarden`
  } catch (e) {
    loadError.value = e instanceof ApiError && e.status === 404 ? 'This script does not exist or is not available to you.' : String(e)
  }
}

// Dynamic values: reload a parameter's list (debounced) whenever the parameters it depends on change.
const dynamicParams = computed(() => script.value?.parameters.filter((p) => p.dynamic_values) ?? [])
watch(
  () => dynamicParams.value.map((p) => [p.name, JSON.stringify(dependencyQuery(p, values))]),
  (now, before) => {
    for (const [name, key] of now) {
      const prev = before?.find(([n]) => n === name)?.[1]
      if (prev === key) continue
      clearTimeout(timers.get(name))
      timers.set(name, setTimeout(() => void loadOptions(name), prev === undefined ? 0 : 300))
    }
  },
  { immediate: true },
)

async function loadOptions(name: string) {
  const p = script.value?.parameters.find((x) => x.name === name)
  if (!p) return
  const query = dependencyQuery(p, values)
  if (!query) {
    options[name] = []
    optionHints[name] = `Choose ${p.depends_on?.join(', ')} first`
    return
  }
  optionHints[name] = 'Loading…'
  try {
    options[name] = await api.values(props.name, name, query)
    optionHints[name] = ''
  } catch (e) {
    options[name] = []
    optionHints[name] =
      e instanceof ApiError && e.status === 429
        ? 'Busy loading other lists. Change a field or reload to try again.'
        : e instanceof ApiError && e.details.length
          ? e.details.join('; ')
          : 'Could not load the values'
  }
}

function update(name: string, v: FieldValue) {
  values[name] = v
  touched.add(name)
  if (errors.value[name]) {
    const { [name]: _, ...rest } = errors.value
    errors.value = rest
  }
}

async function run() {
  if (!script.value) return
  submitting.value = true
  errors.value = {}
  formErrors.value = []
  busyRun.value = ''
  try {
    const { id } = await api.startRun(props.name, buildRunBody(script.value.parameters, values, touched))
    refreshRunning()
    await router.replace({ name: 'script', params: { name: props.name }, query: { run: id } })
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) {
      formErrors.value = [e.message]
    } else if (e instanceof ApiError && e.status === 400) {
      const mapped = fieldErrors(e.details)
      errors.value = mapped.fields
      formErrors.value = mapped.other.length || Object.keys(mapped.fields).length ? mapped.other : [e.message]
    } else if (e instanceof ApiError && e.status === 429 && e.message.includes('already have a run')) {
      formErrors.value = ['You already have a run in progress. Wait for it to finish or stop it first.']
      busyRun.value = e.details[0] ?? ''
    } else if (e instanceof ApiError && e.status === 429) {
      formErrors.value = ['Too many scripts are running right now. Try again in a moment.']
    } else {
      formErrors.value = [e instanceof Error ? e.message : String(e)]
    }
  } finally {
    submitting.value = false
  }
}

watch(() => props.name, load, { immediate: true })
onBeforeUnmount(() => timers.forEach((t) => clearTimeout(t)))
</script>

<template>
  <p v-if="loadError" class="alert" role="alert">{{ loadError }}</p>
  <p v-else-if="!script" class="muted">Loading…</p>
  <div v-else class="script-page">
    <form class="script-top" novalidate @submit.prevent="run">
      <div class="script-title">
        <h1>{{ script.name }}</h1>
        <span class="muted small">{{ script.group || 'Other' }}</span>
        <span class="spacer" />
        <RouterLink class="small" :to="{ name: 'runs', query: { script: script.name } }">Previous runs</RouterLink>
      </div>
      <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown escapes everything first -->
      <div v-if="script.description" class="description" v-html="renderMarkdown(script.description)" />
      <p v-if="environments.run.length || environments.view.length || script.timeout_seconds" class="muted small">
        <template v-if="environments.run.length">You can run it on {{ environments.run.join(', ') }}.</template>
        <template v-if="environments.view.length"> View only on {{ environments.view.join(', ') }}.</template>
        <template v-if="script.timeout_seconds"> Runs are stopped automatically after {{ formatSeconds(script.timeout_seconds) }}.</template>
      </p>

      <div v-if="script.parameters.length" class="form-grid">
        <ParamField
          v-for="p in script.parameters"
          :key="p.name"
          :param="p"
          :script="script.name"
          :model-value="values[p.name]"
          :options="decorateOptions(p, options[p.name])"
          :options-hint="optionHints[p.name]"
          :error="errors[p.name]"
          @update:model-value="update(p.name, $event)"
        />
      </div>

      <div v-if="formErrors.length" class="alert" role="alert">
        Could not start the script.
        <ul>
          <li v-for="(m, i) in formErrors" :key="i">{{ m }}</li>
        </ul>
        <RouterLink v-if="busyRun" :to="{ name: 'run', params: { id: busyRun } }">Open your running run</RouterLink>
      </div>
      <div class="row">
        <button class="btn btn-primary" type="submit" :disabled="submitting || !!blocker">
          {{ submitting ? 'Starting…' : 'Execute' }}
        </button>
        <span v-if="blocker" class="muted small" role="status">{{ blocker }}</span>
      </div>
    </form>

    <RunPanel v-if="runId" :id="runId" />
  </div>
</template>
