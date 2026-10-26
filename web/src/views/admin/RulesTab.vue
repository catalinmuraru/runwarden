<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type Access, type Rule } from '../../api'
import { environmentLabel, ruleProblems, ruleRoles, scriptLabel } from '../../lib/policy'
import { changePolicy, policyState } from '../../lib/policyState'
import PolicyStatus from './PolicyStatus.vue'
import SelectorPicker from './SelectorPicker.vue'
import { environmentGroups, scriptGroups } from './pickers'

const props = defineProps<{ role?: string }>()

interface Draft {
  id: string // empty for a new rule
  role: string
  scripts: string[]
  environments: string[]
  access: Access | ''
}
const empty = (): Draft => ({ id: '', role: props.role ?? '', scripts: [], environments: [], access: '' })
const draft = ref<Draft | null>(null)
const shown = ref(props.role ?? '')
watch(
  () => props.role,
  (r) => (shown.value = r ?? ''),
)

const roles = computed(() => (policyState.view ? ruleRoles(policyState.view.policy) : []))
const grouped = computed(() => {
  const rules = policyState.view?.policy.rules ?? []
  return roles.value
    .filter((r) => !shown.value || r === shown.value)
    .map((role) => ({ role, rules: rules.filter((x) => x.role === role) }))
})
const problems = computed(() => (draft.value ? ruleProblems(draft.value) : []))
const tried = ref(false)

function edit(r: Rule) {
  tried.value = false
  draft.value = { ...r, scripts: [...r.scripts], environments: [...r.environments] }
}

function create(role = '') {
  tried.value = false
  draft.value = { ...empty(), role: role || shown.value }
}

async function save() {
  tried.value = true
  const d = draft.value
  if (!d || problems.value.length) return
  const rule = { role: d.role, scripts: d.scripts, environments: d.environments, access: d.access as Access }
  const ok = await changePolicy((rev) => (d.id ? api.updateRule(rev, { ...rule, id: d.id }) : api.createRule(rev, rule)))
  if (ok) draft.value = null
}

async function remove(r: Rule) {
  if (!confirm(`Delete this rule of ${r.role}? Users with only this rule lose the access.`)) return
  await changePolicy((rev) => api.deleteRule(rev, r.id))
}
</script>

<template>
  <p class="muted">
    A rule gives a role <strong>view</strong> (see the script, its form and its runs) or <strong>run</strong> access to scripts, chosen by
    name or tag, in environments. Rules only add access: when several match, the highest wins. Nothing matches → no access.
  </p>
  <PolicyStatus warnings />
  <div class="row" style="margin-bottom: 1rem">
    <label class="row">
      Role
      <select v-model="shown">
        <option value="">All</option>
        <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
      </select>
    </label>
    <span class="spacer" />
    <button class="btn" type="button" @click="create()">New rule</button>
  </div>

  <form v-if="draft" class="card stack" style="margin-bottom: 1rem" novalidate @submit.prevent="save">
    <strong>{{ draft.id ? `Change rule ${draft.id}` : 'New rule' }}</strong>
    <div class="form-grid">
      <div class="field">
        <label for="rule-role">Role</label>
        <select id="rule-role" v-model="draft.role">
          <option value="" disabled>Choose…</option>
          <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
        </select>
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
    </div>
    <ul v-if="tried && problems.length" class="alert">
      <li v-for="p in problems" :key="p">{{ p }}</li>
    </ul>
    <div class="row">
      <button class="btn btn-primary" type="submit" :disabled="policyState.saving">Save</button>
      <button class="btn btn-quiet" type="button" @click="draft = null">Cancel</button>
    </div>
  </form>

  <p v-if="!roles.length" class="muted">No role can have rules yet.</p>
  <section v-for="g in grouped" :key="g.role">
    <h2>{{ g.role }}</h2>
    <p v-if="!g.rules.length" class="muted">
      No rules: users with only this role see nothing.
      <a href="#" @click.prevent="create(g.role)">Add one</a>
    </p>
    <div v-else class="table-wrap">
      <table>
        <thead>
          <tr>
            <th scope="col">Access</th>
            <th scope="col">Scripts</th>
            <th scope="col">Environments</th>
            <th scope="col"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in g.rules" :key="r.id">
            <td>
              <span class="badge" :class="r.access === 'run' ? 'tone-ok' : 'tone-info'">{{ r.access }}</span>
            </td>
            <td>{{ r.scripts.map(scriptLabel).join(', ') }}</td>
            <td>{{ r.environments.map(environmentLabel).join(', ') }}</td>
            <td class="row">
              <button class="btn btn-quiet small" type="button" @click="edit(r)">Edit</button>
              <button class="btn btn-quiet small tone-bad" type="button" @click="remove(r)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
