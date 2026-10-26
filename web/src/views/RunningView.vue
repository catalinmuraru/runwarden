<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'
import { formatDateTime, formatDuration } from '../lib/format'
import { refreshRunning, running } from '../lib/running'
import { session } from '../session'

const router = useRouter()
const now = ref(Date.now())
const error = ref('')
let ticker: ReturnType<typeof setInterval> | undefined

async function stop(id: string) {
  error.value = ''
  try {
    await api.stop(id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
  refreshRunning()
}

onMounted(() => {
  refreshRunning()
  ticker = setInterval(() => (now.value = Date.now()), 1000)
})
onBeforeUnmount(() => clearInterval(ticker))
</script>

<template>
  <div class="row">
    <h1>Running</h1>
    <span class="muted">{{ session.me?.admin ? 'All runs in progress on this server' : 'Your runs in progress' }}</span>
    <span class="spacer" />
    <button class="btn" type="button" @click="refreshRunning">Refresh</button>
  </div>
  <p v-if="error" class="alert" role="alert">{{ error }}</p>

  <p v-if="!running.loaded" class="muted">Loading…</p>
  <p v-else-if="running.runs.length === 0" class="muted">Nothing is running.</p>
  <div v-else class="table-wrap">
    <table>
      <thead>
        <tr>
          <th scope="col">Started</th>
          <th scope="col">Script</th>
          <th scope="col">User</th>
          <th scope="col">Running for</th>
          <th scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in running.runs" :key="r.id" class="clickable" @click="router.push({ name: 'run', params: { id: r.id } })">
          <td>
            <RouterLink :to="{ name: 'run', params: { id: r.id } }" @click.stop>{{ formatDateTime(r.started) }}</RouterLink>
          </td>
          <td>{{ r.script }}</td>
          <td>{{ r.user }}</td>
          <td>{{ formatDuration(r.started, undefined, now) }}</td>
          <td style="text-align: right">
            <button class="btn" type="button" @click.stop="stop(r.id)">Stop</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
