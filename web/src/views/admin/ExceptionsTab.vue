<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, type Access, type PolicyException } from '../../api'
import { formatDateTime } from '../../lib/format'
import { endOfDay, environmentLabel, exceptionProblems, isActive, lastExpiryDate, scriptLabel, toDateInput, type ExceptionDraft } from '../../lib/policy'
import { changePolicy, policyState } from '../../lib/policyState'
import PolicyStatus from './PolicyStatus.vue'
import SelectorPicker from './SelectorPicker.vue'
import { environmentGroups, scriptGroups } from './pickers'

const props = defineProps<{ user?: string }>()

const show = ref<'active' | 'expired'>('active')
const draft = ref<ExceptionDraft | null>(null)
const tried = ref(false)
const now = () => new Date()

const maxDays = computed(() => policyState.view?.max_exception_days ?? 0)
const list = computed(() => {
  const all = policyState.view?.policy.exceptions ?? []
  const t = now()
  return all
    .filter((e) => (show.value === 'active') === isActive(e.expires, t))
    .filter((e) => !props.user || e.user.toLowerCase() === props.user.toLowerCase())
    .sort((a, b) => a.expires.localeCompare(b.expires))
})
const knownUsers = computed(() => policyState.view?.policy.users.map((u) => u.name) ?? [])
const problems = computed(() => (draft.value ? exceptionProblems(draft.value, now(), maxDays.value) : []))

function create() {
  tried.value = false
  const tomorrow = new Date(Date.now() + 24 * 3600 * 1000)
  draft.value = { user: props.user ?? '', scripts: [], environments: [], access: 'run', reason: '', expires: toDateInput(tomorrow) }
}

async function save() {
  tried.value = true
  const d = draft.value
  if (!d || problems.value.length) return
  const ok = await changePolicy((rev) =>
    api.createException(rev, {
      user: d.user.trim(),
      scripts: d.scripts,
      environments: d.environments,
      access: d.access as Access,
      reason: d.reason.trim(),
      expires: endOfDay(d.expires).toISOString(),
    }),
  )
  if (ok) draft.value = null
}

async function end(e: PolicyException) {
  if (!confirm(`End the exception of ${e.user} now?`)) return
  await changePolicy((rev) => api.endException(rev, e.id))
}
</script>

<template>
  <p class="muted">
    An exception gives one user extra access until a date, with a reason (e.g. an incident or a ticket). It ends by itself; every grant
    and end is in the activity log.
    <template v-if="maxDays">At most {{ maxDays }} days.</template>
  </p>
  <PolicyStatus warnings />
  <div class="row" style="margin-bottom: 1rem">
    <div class="tabs" role="tablist" style="margin: 0; border: 0">
      <button role="tab" type="button" :aria-selected="show === 'active'" :class="{ active: show === 'active' }" @click="show = 'active'">Active</button>
      <button role="tab" type="button" :aria-selected="show === 'expired'" :class="{ active: show === 'expired' }" @click="show = 'expired'">Ended</button>
    </div>
    <span v-if="user" class="muted">for {{ user }}</span>
    <span class="spacer" />
    <button class="btn" type="button" @click="create">New exception</button>
  </div>

  <form v-if="draft" class="card stack" style="margin-bottom: 1rem" novalidate @submit.prevent="save">
    <strong>New exception</strong>
    <div class="form-grid">
      <div class="field">
        <label for="exc-user">User</label>
        <input id="exc-user" v-model="draft.user" type="text" list="exc-users" autocomplete="off" />
        <datalist id="exc-users">
          <option v-for="u in knownUsers" :key="u" :value="u" />
        </datalist>
      </div>
      <fieldset class="field">
        <legend>Access</legend>
        <div class="checks">
          <label><input v-model="draft.access" type="radio" value="view" /> view</label>
          <label><input v-model="draft.access" type="radio" value="run" /> run</label>
        </div>
      </fieldset>
      <SelectorPicker v-if="policyState.view" v-model="draft.scripts" legend="Scripts" :groups="scriptGroups(policyState.view)" all-label="All scripts" />
      <SelectorPicker
        v-if="policyState.view"
        v-model="draft.environments"
        legend="Environments"
        :groups="environmentGroups(policyState.view)"
        all-label="All environments (and scripts without one)"
      />
      <div class="field">
        <label for="exc-expires">Ends at the end of</label>
        <input id="exc-expires" v-model="draft.expires" type="date" :min="toDateInput(now())" :max="maxDays ? lastExpiryDate(now(), maxDays) : undefined" />
      </div>
      <div class="field wide">
        <label for="exc-reason">Reason</label>
        <input id="exc-reason" v-model="draft.reason" type="text" placeholder="e.g. INC-1234: fix the import on acceptance" />
      </div>
    </div>
    <ul v-if="tried && problems.length" class="alert">
      <li v-for="p in problems" :key="p">{{ p }}</li>
    </ul>
    <div class="row">
      <button class="btn btn-primary" type="submit" :disabled="policyState.saving">Grant</button>
      <button class="btn btn-quiet" type="button" @click="draft = null">Cancel</button>
    </div>
  </form>

  <p v-if="!list.length" class="muted">{{ show === 'active' ? 'No active exceptions.' : 'No exceptions ended in the last 30 days.' }}</p>
  <div v-else class="table-wrap">
    <table>
      <thead>
        <tr>
          <th scope="col">User</th>
          <th scope="col">Access</th>
          <th scope="col">Scripts</th>
          <th scope="col">Environments</th>
          <th scope="col">Reason</th>
          <th scope="col">{{ show === 'active' ? 'Ends' : 'Ended' }}</th>
          <th scope="col">Granted by</th>
          <th v-if="show === 'active'" scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="e in list" :key="e.id" :class="{ muted: show === 'expired' }">
          <td>
            <strong>{{ e.user }}</strong>
          </td>
          <td>
            <span class="badge" :class="e.access === 'run' ? 'tone-ok' : 'tone-info'">{{ e.access }}</span>
          </td>
          <td>{{ e.scripts.map(scriptLabel).join(', ') }}</td>
          <td>{{ e.environments.map(environmentLabel).join(', ') }}</td>
          <td>{{ e.reason }}</td>
          <td>{{ formatDateTime(e.expires) }}</td>
          <td>
            {{ e.created_by }}
            <div class="muted small">{{ formatDateTime(e.created) }}</div>
          </td>
          <td v-if="show === 'active'">
            <button class="btn btn-quiet small tone-bad" type="button" @click="end(e)">End now</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
