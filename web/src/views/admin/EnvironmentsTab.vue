<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../../api'
import { environmentOf, parseList } from '../../lib/policy'
import { changePolicy, policyState } from '../../lib/policyState'
import PolicyStatus from './PolicyStatus.vue'

interface Row {
  name: string
  match: string // comma-separated patterns
}
const rows = ref<Row[]>([])
const unknown = ref('')
const sample = ref('')
const dirty = ref(false)

function reset() {
  const p = policyState.view?.policy
  rows.value = (p?.environments ?? []).map((e) => ({ name: e.name, match: (e.match ?? []).join(', ') }))
  unknown.value = p?.unknown_environment ?? ''
  dirty.value = false
}
watch(() => policyState.view?.policy.revision, reset, { immediate: true })

const edited = computed(() => ({
  environments: rows.value.map((r) => ({ name: r.name.trim(), match: parseList(r.match) })),
  unknown_environment: unknown.value,
}))
const sampleEnv = computed(() => (sample.value.trim() ? environmentOf(edited.value, sample.value) : ''))

function change(fn: () => void) {
  fn()
  dirty.value = true
}

function move(i: number, by: number) {
  change(() => {
    const [r] = rows.value.splice(i, 1)
    rows.value.splice(i + by, 0, r)
  })
}

async function save() {
  const e = edited.value
  await changePolicy((rev) => api.putEnvironments(rev, e.environments, e.unknown_environment))
}
</script>

<template>
  <p class="muted">
    Environments group the values that scripts' environment parameters can take (e.g. namespaces or stages), so rules can say where a
    role may view or run. A value belongs to the first environment whose name or pattern matches (<code>*</code> any characters,
    <code>?</code> one). Order them from least to most sensitive: values that match nothing count as the unknown environment.
  </p>
  <PolicyStatus />
  <div class="table-wrap">
    <table>
      <thead>
        <tr>
          <th scope="col">Order</th>
          <th scope="col">Name</th>
          <th scope="col">Patterns (comma-separated)</th>
          <th scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(r, i) in rows" :key="i">
          <td class="row">
            <button class="btn btn-quiet small" type="button" :disabled="i === 0" :aria-label="`Move ${r.name} up`" @click="move(i, -1)">↑</button>
            <button class="btn btn-quiet small" type="button" :disabled="i === rows.length - 1" :aria-label="`Move ${r.name} down`" @click="move(i, 1)">↓</button>
          </td>
          <td>
            <label class="sr-only" :for="`env-name-${i}`">Name</label>
            <input :id="`env-name-${i}`" v-model="r.name" type="text" @input="dirty = true" />
          </td>
          <td>
            <label class="sr-only" :for="`env-match-${i}`">Patterns</label>
            <input :id="`env-match-${i}`" v-model="r.match" type="text" style="width: 100%" placeholder="e.g. dev*, test*" @input="dirty = true" />
          </td>
          <td>
            <button class="btn btn-quiet small tone-bad" type="button" @click="change(() => rows.splice(i, 1))">Remove</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
  <div class="row" style="margin-top: 0.75rem">
    <button class="btn" type="button" @click="change(() => rows.push({ name: '', match: '' }))">Add environment</button>
    <label class="row">
      Values that match nothing count as
      <select v-model="unknown" @change="dirty = true">
        <option value="">the last (most sensitive) one</option>
        <option v-for="r in rows.filter((x) => x.name.trim())" :key="r.name" :value="r.name.trim()">{{ r.name }}</option>
      </select>
    </label>
  </div>
  <div class="row" style="margin-top: 1rem">
    <button class="btn btn-primary" type="button" :disabled="!dirty || policyState.saving" @click="save">Save environments</button>
    <button class="btn btn-quiet" type="button" :disabled="!dirty" @click="reset">Undo changes</button>
  </div>

  <div class="card row" style="margin-top: 1.5rem">
    <label for="env-sample">Test a value</label>
    <input id="env-sample" v-model="sample" type="text" placeholder="e.g. prod-west" style="max-width: 16rem" />
    <span v-if="sampleEnv" role="status">→ <strong>{{ sampleEnv }}</strong></span>
  </div>
</template>
