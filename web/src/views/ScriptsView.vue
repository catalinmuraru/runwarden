<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type ScriptSummary } from '../api'
import { groupScripts } from '../lib/format'
import { summary } from '../lib/markdown'

const scripts = ref<ScriptSummary[]>([])
const filter = ref('')
const loading = ref(true)
const error = ref('')

onMounted(async () => {
  try {
    scripts.value = await api.scripts()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})

const groups = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const visible = q
    ? scripts.value.filter((s) => `${s.name} ${s.group ?? ''} ${s.description ?? ''}`.toLowerCase().includes(q))
    : scripts.value
  return groupScripts(visible)
})
</script>

<template>
  <div class="row">
    <h1>Scripts</h1>
    <span class="spacer" />
    <label class="sr-only" for="filter">Filter scripts</label>
    <input id="filter" v-model="filter" type="search" placeholder="Filter scripts…" style="max-width: 18rem" />
  </div>

  <p v-if="loading" class="muted">Loading…</p>
  <p v-else-if="error" class="alert" role="alert">{{ error }}</p>
  <p v-else-if="scripts.length === 0" class="muted">No scripts are available to you.</p>
  <p v-else-if="groups.length === 0" class="muted">No script matches “{{ filter }}”.</p>

  <details v-for="[group, list] in groups" :key="group" class="group" open>
    <summary>{{ group }} <span class="muted">({{ list.length }})</span></summary>
    <div class="script-list">
      <RouterLink
        v-for="s in list"
        :key="s.name"
        :to="{ name: 'script', params: { name: s.name } }"
        class="card script-card"
      >
        <strong>{{ s.name }}</strong>
        <span class="muted">{{ summary(s.description) }}</span>
      </RouterLink>
    </div>
  </details>
</template>
