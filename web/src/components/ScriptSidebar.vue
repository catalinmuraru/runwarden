<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { groupScripts } from '../lib/format'
import { summary } from '../lib/markdown'
import { notifications, setNotifications, unavailableReason } from '../lib/notify'
import { running } from '../lib/running'
import { filterScripts, saveOpenGroups, savedOpenGroups, scripts } from '../lib/scripts'
import { session, signOut } from '../session'

const route = useRoute()
const filter = ref('')
const open = reactive(savedOpenGroups())

const groups = computed(() => groupScripts(filterScripts(scripts.list, filter.value)))
const current = computed(() => (route.name === 'script' ? String(route.params.name) : ''))
const searching = computed(() => filter.value.trim() !== '')

// Open the group of the script on screen, so it is visible in the list.
watch(
  [current, () => scripts.list],
  () => {
    const s = scripts.list.find((x) => x.name === current.value)
    if (s) open.add(s.group?.trim() || 'Other')
  },
  { immediate: true },
)

function toggle(group: string) {
  if (searching.value) return
  if (open.has(group)) open.delete(group)
  else open.add(group)
  saveOpenGroups(open)
}

const notifyTitle = computed(() => {
  const reason = unavailableReason()
  if (reason) return reason
  return notifications.enabled ? 'Notifications on: you are told when your runs finish' : 'Notify me when my runs finish'
})

async function toggleNotifications() {
  const reason = unavailableReason()
  if (reason) {
    alert(reason)
    return
  }
  await setNotifications(!notifications.enabled)
  if (!notifications.enabled && unavailableReason()) alert(unavailableReason())
}

async function logout() {
  const providerLogout = await signOut()
  window.location.assign(providerLogout ?? '/')
}
</script>

<template>
  <aside class="sidebar" aria-label="Scripts">
    <div class="sidebar-head">
      <RouterLink to="/" class="brand">Runwarden</RouterLink>
      <label class="sr-only" for="sidebar-search">Search scripts</label>
      <input id="sidebar-search" v-model="filter" type="search" placeholder="Search…" autocomplete="off" />
    </div>

    <nav class="script-nav" aria-label="Scripts">
      <p v-if="scripts.loading && !scripts.loaded" class="muted pad">Loading…</p>
      <p v-else-if="scripts.error" class="alert pad" role="alert">{{ scripts.error }}</p>
      <p v-else-if="scripts.loaded && !scripts.list.length" class="muted pad">No scripts</p>
      <p v-else-if="!groups.length" class="muted pad">No match</p>
      <div v-for="[group, list] in groups" :key="group">
        <button
          class="group-head"
          type="button"
          :aria-expanded="searching || open.has(group)"
          @click="toggle(group)"
        >
          <span class="chevron" aria-hidden="true">{{ searching || open.has(group) ? '▾' : '▸' }}</span>
          {{ group }}
        </button>
        <template v-if="searching || open.has(group)">
          <RouterLink
            v-for="s in list"
            :key="s.name"
            :to="{ name: 'script', params: { name: s.name } }"
            class="script-link"
            :class="{ active: s.name === current }"
            :title="summary(s.description) || s.name"
            >{{ s.name }}</RouterLink
          >
        </template>
      </div>
    </nav>

    <div class="sidebar-foot">
      <nav class="side-links" aria-label="Main">
        <RouterLink to="/running" :class="{ active: $route.name === 'running' }">
          Running<span v-if="running.runs.length" class="count" :aria-label="`${running.runs.length} in progress`">{{ running.runs.length }}</span>
        </RouterLink>
        <RouterLink to="/runs" :class="{ active: $route.name === 'runs' || $route.name === 'run' }">History</RouterLink>
        <RouterLink v-if="session.me?.admin" to="/admin" :class="{ active: $route.name === 'admin' }">Admin</RouterLink>
      </nav>
      <div class="side-user">
        <span class="name" :title="session.me?.groups.join(', ')">
          {{ session.me?.name }}
          <span class="muted small" title="Your role: what you may see and run">{{ session.me?.roles.join(', ') || 'guest' }}</span>
        </span>
        <button class="btn btn-quiet" type="button" :aria-pressed="notifications.enabled" :title="notifyTitle" @click="toggleNotifications">
          {{ notifications.enabled ? '🔔' : '🔕' }}<span class="sr-only">{{ notifyTitle }}</span>
        </button>
        <button v-if="session.me?.auth_type !== 'none'" class="btn btn-quiet" type="button" @click="logout">Sign out</button>
      </div>
    </div>
  </aside>
</template>
