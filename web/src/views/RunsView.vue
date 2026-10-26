<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type RunMeta } from '../api'
import { formatDateTime, formatDuration, statusLabel } from '../lib/format'
import { session } from '../session'

const route = useRoute()
const router = useRouter()
const runs = ref<RunMeta[]>([])
const scripts = ref<string[]>([])
const users = ref<string[]>([])
const loading = ref(true)
const error = ref('')
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined

const statuses = ['running', 'finished', 'failed', 'stopped', 'killed', 'timed_out', 'interrupted']

// Filters live in the URL so filtered views can be linked and reloaded.
function queryFilter(key: 'script' | 'status' | 'user') {
  return computed({
    get: () => (typeof route.query[key] === 'string' ? (route.query[key] as string) : ''),
    set: (v: string) => {
      const query = { ...route.query, [key]: v || undefined }
      void router.replace({ query })
    },
  })
}
const script = queryFilter('script')
const status = queryFilter('status')
const user = queryFilter('user')

async function load() {
  try {
    runs.value = await api.runs({ script: script.value || undefined, status: status.value || undefined, user: user.value || undefined })
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  void load()
  scripts.value = (await api.scripts().catch(() => [])).map((s) => s.name)
  if (session.me?.admin) users.value = (await api.adminUsers().catch(() => [])).map((u) => u.name)
  // Refresh while runs are in progress.
  timer = setInterval(() => {
    now.value = Date.now()
    if (runs.value.some((r) => r.status === 'running')) void load()
  }, 3000)
})
onBeforeUnmount(() => clearInterval(timer))
watch(() => [script.value, status.value, user.value], load)
</script>

<template>
  <div class="row">
    <h1>History</h1>
    <span class="spacer" />
    <label for="script-filter" class="muted">Script</label>
    <select id="script-filter" v-model="script" style="max-width: 16rem">
      <option value="">All scripts</option>
      <option v-for="s in scripts" :key="s" :value="s">{{ s }}</option>
    </select>
    <label for="status-filter" class="muted">Status</label>
    <select id="status-filter" v-model="status" style="max-width: 10rem">
      <option value="">Any</option>
      <option v-for="s in statuses" :key="s" :value="s">{{ statusLabel(s).text }}</option>
    </select>
    <template v-if="session.me?.admin">
      <label for="user-filter" class="muted">User</label>
      <select id="user-filter" v-model="user" style="max-width: 12rem">
        <option value="">Everyone</option>
        <option v-if="user && !users.includes(user)" :value="user">{{ user }}</option>
        <option v-for="u in users" :key="u" :value="u">{{ u }}</option>
      </select>
    </template>
    <button class="btn" type="button" @click="load">Refresh</button>
  </div>

  <p v-if="loading" class="muted">Loading…</p>
  <p v-else-if="error" class="alert" role="alert">{{ error }}</p>
  <p v-else-if="runs.length === 0" class="muted">No runs found.</p>
  <div v-else class="table-wrap">
    <table>
      <thead>
        <tr>
          <th scope="col">Started</th>
          <th scope="col">Script</th>
          <th scope="col">User</th>
          <th scope="col">Status</th>
          <th scope="col">Duration</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="r in runs"
          :key="r.id"
          class="clickable"
          @click="router.push({ name: 'run', params: { id: r.id } })"
        >
          <td>
            <RouterLink :to="{ name: 'run', params: { id: r.id } }" @click.stop>{{ formatDateTime(r.started) }}</RouterLink>
          </td>
          <td>{{ r.script }}</td>
          <td>{{ r.user }}</td>
          <td>
            <span class="badge" :class="`tone-${statusLabel(r.status).tone}`">{{ statusLabel(r.status).text }}</span>
            <span v-if="r.exit_code !== undefined && r.status !== 'finished'" class="muted"> (exit {{ r.exit_code }})</span>
          </td>
          <td>{{ formatDuration(r.started, r.finished, now) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
