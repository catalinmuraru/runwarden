<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type AdminUser } from '../../api'
import { formatDateTime } from '../../lib/format'
import { assignableRoles } from '../../lib/policy'
import { changePolicy, policyState } from '../../lib/policyState'
import PolicyStatus from './PolicyStatus.vue'

const emit = defineEmits<{ activity: [string]; access: [string] }>()

const methodLabels: Record<string, string> = { gitlab: 'GitLab', oidc: 'SSO', ldap: 'LDAP', local: 'built-in' }
const userStatus: Record<AdminUser['status'], { text: string; tone: string }> = {
  active: { text: 'Active now', tone: 'ok' },
  'signed-in': { text: 'Signed in', tone: 'info' },
  'signed-out': { text: 'Signed out', tone: 'warn' },
  never: { text: 'Not signed in yet', tone: 'warn' },
}

const users = ref<AdminUser[]>([])
const loading = ref(false)
const error = ref('')
const editing = ref('') // user whose roles are being edited
const draft = ref<string[]>([])
const newUser = ref('')
const newRoles = ref<string[]>([])

const roles = computed(() => (policyState.view ? assignableRoles(policyState.view.policy) : []))

async function load() {
  loading.value = true
  error.value = ''
  try {
    users.value = await api.adminUsers()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function edit(u: AdminUser) {
  editing.value = u.name
  draft.value = u.roles.filter((r) => !(u.fixed && r === 'devops'))
}

function toggle(list: string[], role: string, on: boolean): string[] {
  return on ? [...list.filter((r) => r !== role), role] : list.filter((r) => r !== role)
}

async function save(name: string, list: string[]) {
  if (await changePolicy((rev) => api.setUserRoles(rev, name, list))) {
    editing.value = ''
    await load()
    return true
  }
  return false
}

async function addUser() {
  if (!newUser.value.trim() || !newRoles.value.length) return
  if (await save(newUser.value.trim(), newRoles.value)) {
    newUser.value = ''
    newRoles.value = []
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

onMounted(load)
defineExpose({ load })
</script>

<template>
  <p class="muted">
    Everyone who signed in, and users given roles in advance. Users without a role are guests: they can sign in but see nothing. "Active
    now" means a request in the last 5 minutes. Groups from the identity provider are shown for reference only; they grant no rights.
  </p>
  <PolicyStatus />
  <p v-if="error" class="alert" role="alert">{{ error }}</p>
  <p v-if="loading && !users.length" class="muted">Loading…</p>
  <div v-else class="table-wrap">
    <table>
      <thead>
        <tr>
          <th scope="col">User</th>
          <th scope="col">Roles</th>
          <th scope="col">Status</th>
          <th scope="col">Last activity</th>
          <th scope="col">Address</th>
          <th scope="col">Runs</th>
          <th scope="col">Sessions</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in users" :key="u.name">
          <td>
            <strong>{{ u.name }}</strong>
            <div v-if="u.groups?.length" class="muted small" title="Groups from the identity provider (not used for rights)">{{ u.groups.join(', ') }}</div>
          </td>
          <td>
            <template v-if="editing === u.name">
              <div class="checks">
                <label v-for="r in roles" :key="r">
                  <input
                    type="checkbox"
                    :checked="draft.includes(r) || (u.fixed && r === 'devops')"
                    :disabled="u.fixed && r === 'devops'"
                    @change="draft = toggle(draft, r, ($event.target as HTMLInputElement).checked)"
                  />
                  {{ r }}
                </label>
              </div>
              <div class="row" style="margin-top: 0.35rem">
                <button class="btn btn-primary small" type="button" :disabled="policyState.saving" @click="save(u.name, draft)">Save</button>
                <button class="btn btn-quiet small" type="button" @click="editing = ''">Cancel</button>
              </div>
            </template>
            <template v-else>
              <span v-for="r in u.roles" :key="r" class="badge" :class="{ 'tone-info': r === 'devops' }" style="margin-right: 0.25rem">{{ r }}</span>
              <span v-if="!u.roles.length" class="badge">guest</span>
              <span v-if="u.fixed" class="muted small" title="BOOTSTRAP_DEVOPS in the deployment"> (devops from the deployment)</span>
              <div class="small">
                <button v-if="u.name !== 'admin@local'" class="btn btn-quiet small" type="button" @click="edit(u)">Change roles</button>
                <a href="#" @click.prevent="emit('access', u.name)">access</a>
                <span v-if="u.exceptions" class="tone-warn"> · {{ u.exceptions }} exception(s)</span>
              </div>
            </template>
          </td>
          <td><span class="badge" :class="`tone-${userStatus[u.status].tone}`">{{ userStatus[u.status].text }}</span></td>
          <td>{{ formatDateTime(u.last_seen) || '—' }}</td>
          <td>{{ u.last_ip || '—' }}</td>
          <td>
            <RouterLink :to="{ name: 'runs', query: { user: u.name } }">{{ u.runs }}</RouterLink>
            <RouterLink v-if="u.running" :to="{ name: 'running' }" class="muted"> ({{ u.running }} running)</RouterLink>
            · <a href="#" @click.prevent="emit('activity', u.name)">activity</a>
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

  <form class="card stack" style="margin-top: 1rem" @submit.prevent="addUser">
    <strong>Give roles to a user who has not signed in yet</strong>
    <div class="row">
      <label class="sr-only" for="new-user">User name</label>
      <input id="new-user" v-model="newUser" type="text" placeholder="User name as it signs in" style="max-width: 18rem" />
      <div class="checks">
        <label v-for="r in roles" :key="r">
          <input type="checkbox" :checked="newRoles.includes(r)" @change="newRoles = toggle(newRoles, r, ($event.target as HTMLInputElement).checked)" />
          {{ r }}
        </label>
      </div>
      <button class="btn" type="submit" :disabled="!newUser.trim() || !newRoles.length || policyState.saving">Add</button>
    </div>
  </form>
</template>
