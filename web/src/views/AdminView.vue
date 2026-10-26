<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type ActivityEvent, type AdminUser } from '../api'
import { formatDateTime } from '../lib/format'

const route = useRoute()
const router = useRouter()

const actions = ['run-start', 'run-end', 'run-input', 'run-stop', 'run-kill', 'login', 'logout', 'session-revoke', 'config-reload', 'history-cleanup']
const methodLabels: Record<string, string> = { gitlab: 'GitLab', oidc: 'SSO', ldap: 'LDAP', local: 'built-in' }
const userStatus: Record<AdminUser['status'], { text: string; tone: string }> = {
  active: { text: 'Active now', tone: 'ok' },
  'signed-in': { text: 'Signed in', tone: 'info' },
  'signed-out': { text: 'Signed out', tone: 'warn' },
}

const q = (key: string) => (typeof route.query[key] === 'string' ? (route.query[key] as string) : '')
const tab = computed(() => (q('tab') === 'activity' ? 'activity' : 'users'))
const filter = ref({ user: '', action: '', outcome: '', script: '' })

const users = ref<AdminUser[]>([])
const events = ref<ActivityEvent[]>([])
const loading = ref(false)
const error = ref('')

function setQuery(next: Record<string, string>) {
  const query = Object.fromEntries(Object.entries(next).filter(([, v]) => v))
  void router.replace({ query })
}

function showTab(t: 'users' | 'activity') {
  setQuery(t === 'activity' ? { tab: t, ...filter.value } : {})
}

function applyFilter() {
  setQuery({ tab: 'activity', ...filter.value })
}

function userActivity(name: string) {
  filter.value = { user: name, action: '', outcome: '', script: '' }
  applyFilter()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    if (tab.value === 'users') {
      users.value = await api.adminUsers()
    } else {
      filter.value = { user: q('user'), action: q('action'), outcome: q('outcome'), script: q('script') }
      events.value = await api.activity({ ...filter.value, limit: 500 })
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function signOutEverywhere(name: string) {
  if (!confirm(`Sign ${name} out of every browser? They can sign in again unless their account is blocked at the identity provider.`)) return
  error.value = ''
  try {
    await api.adminSignOut(name)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
  await load()
}

function details(e: ActivityEvent): string {
  const parts: string[] = []
  if (e.status) parts.push(e.status)
  if (e.exit_code !== undefined) parts.push(`exit ${e.exit_code}`)
  if (e.reason && e.reason !== e.status) parts.push(e.reason)
  if (e.count) parts.push(`${e.count} removed`)
  if (e.values && Object.keys(e.values).length) parts.push(JSON.stringify(e.values))
  return parts.join(' · ')
}

watch(() => route.query, load, { immediate: true })
</script>

<template>
  <div class="row">
    <h1>Admin</h1>
    <span class="spacer" />
    <button class="btn" type="button" @click="load">Refresh</button>
  </div>
  <div class="tabs" role="tablist">
    <button role="tab" type="button" :aria-selected="tab === 'users'" :class="{ active: tab === 'users' }" @click="showTab('users')">Users</button>
    <button role="tab" type="button" :aria-selected="tab === 'activity'" :class="{ active: tab === 'activity' }" @click="showTab('activity')">
      Activity
    </button>
  </div>
  <p v-if="error" class="alert" role="alert">{{ error }}</p>

  <template v-if="tab === 'users'">
    <p class="muted">Everyone who signed in or used the server. "Active now" means a request in the last 5 minutes.</p>
    <p v-if="loading && !users.length" class="muted">Loading…</p>
    <div v-else class="table-wrap">
      <table>
        <thead>
          <tr>
            <th scope="col">User</th>
            <th scope="col">Status</th>
            <th scope="col">Last activity</th>
            <th scope="col">Last sign-in</th>
            <th scope="col">Address</th>
            <th scope="col">Runs</th>
            <th scope="col">Sessions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in users" :key="u.name">
            <td>
              <strong>{{ u.name }}</strong>
              <span v-if="u.admin" class="badge tone-info" style="margin-left: 0.35rem">admin</span>
              <div v-if="u.groups?.length" class="muted small">{{ u.groups.join(', ') }}</div>
            </td>
            <td><span class="badge" :class="`tone-${userStatus[u.status].tone}`">{{ userStatus[u.status].text }}</span></td>
            <td>{{ formatDateTime(u.last_seen) }}</td>
            <td>{{ formatDateTime(u.last_login) || '—' }}</td>
            <td>{{ u.last_ip || '—' }}</td>
            <td>
              <RouterLink :to="{ name: 'runs', query: { user: u.name } }">{{ u.runs }}</RouterLink>
              <RouterLink v-if="u.running" :to="{ name: 'running' }" class="muted"> ({{ u.running }} running)</RouterLink>
              · <a href="#" @click.prevent="userActivity(u.name)">activity</a>
            </td>
            <td>
              <template v-if="u.sessions">
                {{ u.sessions }}
                <span class="muted small">({{ (u.methods ?? []).map((m) => methodLabels[m] ?? m).join(', ') }})</span>
                <button class="btn btn-quiet small" type="button" @click="signOutEverywhere(u.name)">Sign out everywhere</button>
              </template>
              <span v-else class="muted">—</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </template>

  <template v-else>
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
            <td :title="e.source_ip">{{ e.user || '—' }}</td>
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
</template>
