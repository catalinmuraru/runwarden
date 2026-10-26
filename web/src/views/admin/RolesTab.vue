<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '../../api'
import { BUILT_IN_ROLES } from '../../lib/policy'
import { changePolicy, policyState } from '../../lib/policyState'
import PolicyStatus from './PolicyStatus.vue'

const emit = defineEmits<{ access: [string]; rules: [string] }>()

const editing = ref('')
const draft = ref({ name: '', description: '' })
const adding = ref({ name: '', description: '' })

const rows = computed(() => {
  const v = policyState.view
  if (!v) return []
  const p = v.policy
  const holders = (role: string) => {
    const names = new Set(p.users.filter((u) => u.roles.includes(role)).map((u) => u.name.toLowerCase()))
    if (role === 'devops') v.bootstrap.forEach((b) => names.add(b.toLowerCase()))
    return names.size
  }
  return p.roles.map((r) => ({
    ...r,
    builtIn: BUILT_IN_ROLES.includes(r.name),
    users: holders(r.name),
    rules: p.rules.filter((x) => x.role === r.name).length,
  }))
})

function edit(name: string, description = '') {
  editing.value = name
  draft.value = { name, description }
}

async function save(old: string) {
  if (await changePolicy((rev) => api.updateRole(rev, old, draft.value))) editing.value = ''
}

async function remove(name: string) {
  if (!confirm(`Delete the role ${name}? Users lose it.`)) return
  await changePolicy((rev) => api.deleteRole(rev, name))
}

async function add() {
  if (await changePolicy((rev) => api.createRole(rev, { name: adding.value.name.trim(), description: adding.value.description }))) {
    adding.value = { name: '', description: '' }
  }
}
</script>

<template>
  <p class="muted">
    A user's rights are the union of their roles' rules and their active exceptions. <strong>guest</strong> is every user without a role
    (no access); <strong>devops</strong> has every right and the Admin area. The other roles get rights only from their rules.
  </p>
  <PolicyStatus />
  <div class="table-wrap">
    <table>
      <thead>
        <tr>
          <th scope="col">Role</th>
          <th scope="col">Description</th>
          <th scope="col">Users</th>
          <th scope="col">Rules</th>
          <th scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in rows" :key="r.name">
          <template v-if="editing === r.name">
            <td>
              <label class="sr-only" :for="`role-name-${r.name}`">Name</label>
              <input :id="`role-name-${r.name}`" v-model="draft.name" type="text" :disabled="r.builtIn" />
            </td>
            <td colspan="3">
              <label class="sr-only" :for="`role-desc-${r.name}`">Description</label>
              <input :id="`role-desc-${r.name}`" v-model="draft.description" type="text" style="width: 100%" />
            </td>
            <td class="row">
              <button class="btn btn-primary small" type="button" :disabled="policyState.saving" @click="save(r.name)">Save</button>
              <button class="btn btn-quiet small" type="button" @click="editing = ''">Cancel</button>
            </td>
          </template>
          <template v-else>
            <td>
              <strong>{{ r.name }}</strong> <span v-if="r.builtIn" class="muted small">built-in</span>
            </td>
            <td>{{ r.description }}</td>
            <td>{{ r.name === 'guest' ? '—' : r.users }}</td>
            <td>
              <template v-if="r.name === 'guest' || r.name === 'devops'">—</template>
              <a v-else href="#" @click.prevent="emit('rules', r.name)">{{ r.rules }}</a>
            </td>
            <td class="row">
              <a v-if="r.name !== 'guest'" href="#" class="small" @click.prevent="emit('access', r.name)">access</a>
              <button class="btn btn-quiet small" type="button" @click="edit(r.name, r.description)">Edit</button>
              <button v-if="!r.builtIn" class="btn btn-quiet small tone-bad" type="button" :disabled="r.rules > 0" :title="r.rules ? 'Delete its rules first' : ''" @click="remove(r.name)">
                Delete
              </button>
            </td>
          </template>
        </tr>
      </tbody>
    </table>
  </div>

  <form class="card row" style="margin-top: 1rem" @submit.prevent="add">
    <strong>New role</strong>
    <label class="sr-only" for="role-new-name">Name</label>
    <input id="role-new-name" v-model="adding.name" type="text" placeholder="name, e.g. support" style="max-width: 12rem" />
    <label class="sr-only" for="role-new-desc">Description</label>
    <input id="role-new-desc" v-model="adding.description" type="text" placeholder="What it is for" style="flex: 1; min-width: 12rem" />
    <button class="btn" type="submit" :disabled="!adding.name.trim() || policyState.saving">Add</button>
  </form>
</template>
