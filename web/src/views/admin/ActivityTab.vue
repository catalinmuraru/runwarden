<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type ActivityEvent } from '../../api'
import { formatDateTime } from '../../lib/format'

const route = useRoute()
const router = useRouter()

const actions = [
  'run-start',
  'run-end',
  'run-input',
  'run-stop',
  'run-kill',
  'login',
  'logout',
  'session-revoke',
  'policy-change',
  'exception-expired',
  'config-reload',
  'history-cleanup',
]
const q = (key: string) => (typeof route.query[key] === 'string' ? (route.query[key] as string) : '')
const filter = ref({ user: '', action: '', outcome: '', script: '' })
const events = ref<ActivityEvent[]>([])
const loading = ref(false)
const error = ref('')

function applyFilter() {
  const query = Object.fromEntries(Object.entries({ tab: 'activity', ...filter.value }).filter(([, v]) => v))
  void router.replace({ query })
}

async function load() {
  loading.value = true
  error.value = ''
  filter.value = { user: q('user'), action: q('action'), outcome: q('outcome'), script: q('script') }
  try {
    events.value = await api.activity({ ...filter.value, limit: 500 })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function details(e: ActivityEvent): string {
  const parts: string[] = []
  if (e.policy_object) parts.push(`${e.policy_object}: ${e.policy_change ?? ''}`)
  if (e.environments?.length && e.environments[0] !== '-') parts.push(`in ${e.environments.join(', ')}`)
  if (e.status) parts.push(e.status)
  if (e.exit_code !== undefined) parts.push(`exit ${e.exit_code}`)
  if (e.reason && e.reason !== e.status) parts.push(e.reason)
  if (e.count) parts.push(`${e.count} removed`)
  if (e.values && Object.keys(e.values).length) parts.push(JSON.stringify(e.values))
  return parts.join(' · ')
}

watch(() => route.query, load, { immediate: true })
defineExpose({ load })
</script>

<template>
  <form class="row filters" @submit.prevent="applyFilter">
    <label>User <input v-model.trim="filter.user" type="text" /></label>
    <label>
      Action
      <select v-model="filter.action">
        <option value="">Any</option>
        <option v-for="a in actions" :key="a" :value="a">{{ a }}</option>
      </select>
    </label>
    <label>
      Result
      <select v-model="filter.outcome">
        <option value="">Any</option>
        <option value="success">success</option>
        <option value="failure">failure</option>
      </select>
    </label>
    <label>Script <input v-model.trim="filter.script" type="text" /></label>
    <button class="btn" type="submit">Filter</button>
  </form>
  <p v-if="error" class="alert" role="alert">{{ error }}</p>
  <p v-if="loading && !events.length" class="muted">Loading…</p>
  <p v-else-if="!events.length" class="muted">No matching activity.</p>
  <div v-else class="table-wrap">
    <table>
      <thead>
        <tr>
          <th scope="col">Time</th>
          <th scope="col">User</th>
          <th scope="col">Action</th>
          <th scope="col">Result</th>
          <th scope="col">Script / run</th>
          <th scope="col">Details</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(e, i) in events" :key="i">
          <td>{{ formatDateTime(e.time) }}</td>
          <td :title="[e.source_ip, e.roles?.join(', ')].filter(Boolean).join(' · ')">{{ e.user || '—' }}</td>
          <td>{{ e.action }}</td>
          <td><span class="badge" :class="e.outcome === 'success' ? 'tone-ok' : 'tone-bad'">{{ e.outcome }}</span></td>
          <td>
            {{ e.script }}
            <RouterLink v-if="e.run_id" :to="{ name: 'run', params: { id: e.run_id } }" class="muted small">{{ e.run_id }}</RouterLink>
          </td>
          <td class="small">{{ details(e) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
