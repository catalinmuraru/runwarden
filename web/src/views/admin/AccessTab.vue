<script setup lang="ts">
// The resulting access of a user or role: scripts × environments.
import { computed, ref, watch } from 'vue'
import { api, type EffectiveAccess } from '../../api'
import { NO_ENVIRONMENT, assignableRoles } from '../../lib/policy'
import { policyState } from '../../lib/policyState'

const props = defineProps<{ user?: string; role?: string }>()
const emit = defineEmits<{ select: [{ user?: string; role?: string }] }>()

const kind = ref<'user' | 'role'>(props.role ? 'role' : 'user')
const who = ref(props.role ?? props.user ?? '')
const result = ref<EffectiveAccess | null>(null)
const error = ref('')
const loading = ref(false)
const onlyVisible = ref(true)

const roles = computed(() => (policyState.view ? assignableRoles(policyState.view.policy).filter((r) => r !== 'devops') : []))
const columns = computed(() => result.value?.environments ?? [])
const rows = computed(() => (result.value?.scripts ?? []).filter((s) => !onlyVisible.value || Object.keys(s.access).length))

async function load() {
  result.value = null
  error.value = ''
  if (!who.value.trim()) return
  loading.value = true
  try {
    result.value = await api.effective(kind.value === 'user' ? { user: who.value.trim() } : { role: who.value })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function submit() {
  emit('select', kind.value === 'user' ? { user: who.value.trim() } : { role: who.value })
}

watch(
  () => [props.user, props.role],
  () => {
    kind.value = props.role ? 'role' : 'user'
    who.value = props.role ?? props.user ?? ''
    void load()
  },
  { immediate: true },
)
watch(() => policyState.view?.policy.revision, load)
</script>

<template>
  <p class="muted">What a user (roles, exceptions) or a role may do. Hover a cell to see which rule or exception grants it.</p>
  <form class="row filters" @submit.prevent="submit">
    <label><input v-model="kind" type="radio" value="user" /> User</label>
    <label><input v-model="kind" type="radio" value="role" /> Role</label>
    <template v-if="kind === 'user'">
      <label class="sr-only" for="access-user">User</label>
      <input id="access-user" v-model="who" type="text" placeholder="User name" list="access-users" />
      <datalist id="access-users">
        <option v-for="u in policyState.view?.policy.users ?? []" :key="u.name" :value="u.name" />
      </datalist>
    </template>
    <template v-else>
      <label class="sr-only" for="access-role">Role</label>
      <select id="access-role" v-model="who">
        <option value="" disabled>Choose…</option>
        <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
      </select>
    </template>
    <button class="btn" type="submit">Show</button>
    <label><input v-model="onlyVisible" type="checkbox" /> only scripts with access</label>
  </form>
  <p v-if="error" class="alert" role="alert">{{ error }}</p>
  <p v-if="loading" class="muted">Loading…</p>
  <template v-else-if="result">
    <p class="muted">
      Roles: <strong>{{ result.roles.length ? result.roles.join(', ') : 'none (guest)' }}</strong>
    </p>
    <p v-if="!rows.length" class="muted">No access to any script.</p>
    <div v-else class="table-wrap">
      <table class="matrix">
        <thead>
          <tr>
            <th scope="col">Script</th>
            <th v-for="c in columns" :key="c" scope="col">{{ c === NO_ENVIRONMENT ? 'no environment' : c }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in rows" :key="s.name">
            <th scope="row">
              {{ s.name }}
              <div v-if="s.tags?.length" class="muted small">{{ s.tags.join(', ') }}</div>
            </th>
            <td v-for="c in columns" :key="c" :title="s.access[c]?.by">
              <span v-if="s.access[c]" class="badge" :class="s.access[c].access === 'run' ? 'tone-ok' : 'tone-info'">{{ s.access[c].access }}</span>
              <span v-else class="muted">–</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </template>
</template>
